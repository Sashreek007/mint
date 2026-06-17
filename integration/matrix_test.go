// Package integration drives Mint end-to-end the way a real customer does:
// requests hit the demo product, whose one-line keyservice-go middleware calls
// Mint to authenticate + rate-limit + meter. Each test asserts one row of the
// verdict matrix.
//
// Two run modes share this file; mintEnv(t) supplies the Mint base URL:
//   - default        → testcontainers spins docker-compose.test.yml (hermetic)
//   - `-tags live`   → run against an already-running `docker compose up` stack
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sashreek007/mint/demo/product"
	keysvc "github.com/Sashreek007/mint/keyservice-go"
)

// adminToken matches ADMIN_TOKEN in the test stack (and the dev default).
const adminToken = "just-works-for-now"

// startProduct boots the demo product in-process, pointed at the given Mint URL.
// This is the real consumer path: the product's middleware calls Mint per request.
func startProduct(t *testing.T, mintURL string) string {
	t.Helper()
	client := keysvc.New(mintURL)
	srv := httptest.NewServer(product.Handler(client))
	t.Cleanup(srv.Close)
	return srv.URL
}

// ---- the verdict matrix ----

func TestValidKeyAllowed(t *testing.T) {
	mint := mintEnv(t)
	prod := startProduct(t, mint)
	tid := createTenant(t, mint, "valid", nil)
	_, key := issueKey(t, mint, tid)

	status, body := get(t, prod, key)
	if status != http.StatusOK {
		t.Fatalf("valid key: want 200, got %d (%s)", status, body)
	}
	if !strings.Contains(body, tid) {
		t.Fatalf("valid key: want tenant %s echoed, got %s", tid, body)
	}
}

func TestMissingKeyRejected(t *testing.T) {
	mint := mintEnv(t)
	prod := startProduct(t, mint)
	if status, _ := get(t, prod, ""); status != http.StatusUnauthorized {
		t.Fatalf("missing key: want 401, got %d", status)
	}
}

func TestGarbageKeyRejected(t *testing.T) {
	mint := mintEnv(t)
	prod := startProduct(t, mint)
	if status, _ := get(t, prod, "ak_live_not_a_real_key"); status != http.StatusUnauthorized {
		t.Fatalf("garbage key: want 401, got %d", status)
	}
}

func TestRevokedKeyRejected(t *testing.T) {
	mint := mintEnv(t)
	prod := startProduct(t, mint)
	tid := createTenant(t, mint, "revoke", nil)
	keyID, key := issueKey(t, mint, tid)

	if status, _ := get(t, prod, key); status != http.StatusOK {
		t.Fatalf("pre-revoke: want 200, got %d", status)
	}
	revokeKey(t, mint, keyID)

	// Invalidation is eventually consistent: the Postgres update + L2 delete are
	// synchronous, but per-replica L1 eviction rides pub/sub. Poll briefly.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _ := get(t, prod, key)
		if status == http.StatusUnauthorized {
			return // revoked key now rejected — good
		}
		if time.Now().After(deadline) {
			t.Fatalf("revoked key still accepted after 5s (last status %d)", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestQuotaExceeded(t *testing.T) {
	mint := mintEnv(t)
	prod := startProduct(t, mint)
	quota := int64(5)
	tid := createTenant(t, mint, "quota", &quota)
	_, key := issueKey(t, mint, tid)

	allowed, quotaHit := 0, 0
	for i := 0; i < int(quota)+5; i++ {
		status, body := get(t, prod, key) // sequential: stays under the rate limit
		switch {
		case status == http.StatusOK:
			allowed++
		case status == http.StatusTooManyRequests && strings.Contains(body, "quota"):
			quotaHit++
		default:
			t.Fatalf("quota run: unexpected status %d (%s)", status, body)
		}
	}
	if allowed != int(quota) {
		t.Fatalf("quota: want exactly %d allowed, got %d", quota, allowed)
	}
	if quotaHit == 0 {
		t.Fatal("quota: want some quota_exceeded after the cap, got 0")
	}
}

func TestRateLimited(t *testing.T) {
	mint := mintEnv(t)
	prod := startProduct(t, mint)
	tid := createTenant(t, mint, "rate", nil) // unlimited quota → only rate limiting bites
	_, key := issueKey(t, mint, tid)

	const n = 500
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			statuses <- tryGet(prod, key) // concurrent burst beyond the bucket allowance
		})
	}
	wg.Wait()
	close(statuses)

	allowed, limited := 0, 0
	for s := range statuses {
		switch s {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if allowed == 0 {
		t.Fatal("rate: want some allowed within the burst, got 0")
	}
	if limited == 0 {
		t.Fatalf("rate: want some rate_limited out of %d, got 0 (allowed=%d)", n, allowed)
	}
}

func TestMintUnreachableFailsClosed(t *testing.T) {
	// Point the product's SDK client at a dead address. The middleware can't reach
	// Mint, so it fails closed → 503 (never a silent pass).
	prod := startProduct(t, "http://127.0.0.1:1")
	if status, _ := get(t, prod, "any-key"); status != http.StatusServiceUnavailable {
		t.Fatalf("mint down: want 503 fail-closed, got %d", status)
	}
}

// ---- helpers ----

// get hits the product's price endpoint with the key, returns (status, body).
func get(t *testing.T, productURL, key string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, productURL+"/v1/price/AAPL", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET product: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// tryGet is get without *testing.T — safe to call from goroutines (0 on error).
func tryGet(productURL, key string) int {
	req, _ := http.NewRequest(http.MethodGet, productURL+"/v1/price/AAPL", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func createTenant(t *testing.T, mintURL, name string, quota *int64) string {
	t.Helper()
	body := map[string]any{"name": name}
	if quota != nil {
		body["monthly_quota"] = *quota
	}
	var out struct {
		ID string `json:"id"`
	}
	admin(t, http.MethodPost, mintURL+"/admin/tenants", body, &out)
	if out.ID == "" {
		t.Fatal("createTenant: empty id in response")
	}
	return out.ID
}

func issueKey(t *testing.T, mintURL, tenantID string) (keyID, plaintext string) {
	t.Helper()
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	admin(t, http.MethodPost, fmt.Sprintf("%s/v1/tenants/%s/keys", mintURL, tenantID),
		map[string]any{"name": "test"}, &out)
	if out.ID == "" || out.Key == "" {
		t.Fatalf("issueKey: empty id/key (id=%q key=%q)", out.ID, out.Key)
	}
	return out.ID, out.Key
}

func revokeKey(t *testing.T, mintURL, keyID string) {
	t.Helper()
	admin(t, http.MethodPost, fmt.Sprintf("%s/v1/keys/%s/revoke", mintURL, keyID), nil, nil)
}

// admin performs an authenticated admin API call and decodes the JSON response.
func admin(t *testing.T, method, url string, in, out any) {
	t.Helper()
	var rdr io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("X-Admin-Token", adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("admin %s %s: status %d: %s", method, url, resp.StatusCode, b)
	}
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
}
