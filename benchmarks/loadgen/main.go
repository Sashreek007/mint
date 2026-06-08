// loadgen — a fast Go load generator for mint /validate.
//
// Unlike `hey` (one key) and the old Python script (too slow, ~2.8k rps), this
// drives a REALISTIC workload at HIGH throughput:
//   - many unique keys across many tenants
//   - Zipf-skewed traffic (a few hot keys dominate, like real customers)
//   - a configurable fraction of invalid keys
//
// and reports BOTH throughput/latency AND (via /v1/cache/stats) the hit rate.
//
// Usage:
//
//	go run . -keys 1000 -requests 300000 -concurrency 50
//
// Flags:
//
//	-base         base URL (default http://localhost:8080)
//	-admin        admin token (default just-works-for-now)
//	-keys         number of unique keys to create
//	-requests     total validate requests to send
//	-concurrency  number of concurrent workers
//	-invalid      fraction of requests using a garbage key (default 0.05)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	base        = flag.String("base", "http://localhost:8080", "base URL")
	admin       = flag.String("admin", "just-works-for-now", "admin token")
	nKeys       = flag.Int("keys", 1000, "unique keys to create")
	nRequests   = flag.Int("requests", 300000, "total requests")
	concurrency = flag.Int("concurrency", 50, "concurrent workers")
	invalidFrac = flag.Float64("invalid", 0.05, "fraction of invalid keys")
)

// a shared HTTP client with connection reuse (keep-alive) for high throughput
var client = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 200,
		MaxConnsPerHost:     200,
	},
}

func mustPost(path string, body any, headers map[string]string) []byte {
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, _ := http.NewRequest("POST", *base+path, buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out
}

func main() {
	flag.Parse()
	rng := rand.New(rand.NewSource(42)) // fixed seed → reproducible traffic

	// --- build keys across tenants (~10 keys per tenant) ---
	fmt.Printf("building %d keys ...\n", *nKeys)
	nTenants := *nKeys / 10
	if nTenants < 1 {
		nTenants = 1
	}
	keys := make([]string, 0, *nKeys)
	for t := 0; t < nTenants && len(keys) < *nKeys; t++ {
		var tr struct {
			ID string `json:"id"`
		}
		json.Unmarshal(mustPost("/admin/tenants",
			map[string]string{"name": fmt.Sprintf("t%d", t)},
			map[string]string{"X-Admin-Token": *admin}), &tr)
		for k := 0; k < 10 && len(keys) < *nKeys; k++ {
			var kr struct {
				Key string `json:"key"`
			}
			json.Unmarshal(mustPost(fmt.Sprintf("/v1/tenants/%s/keys", tr.ID),
				map[string]string{"name": fmt.Sprintf("k%d", k)},
				map[string]string{"X-Admin-Token": *admin}), &kr)
			keys = append(keys, kr.Key)
		}
	}
	fmt.Printf("  created %d keys across %d tenants\n", len(keys), nTenants)

	// --- build a Zipf-skewed request plan (a few hot keys dominate) ---
	fmt.Printf("planning %d requests (Zipf-skewed, %.0f%% invalid) ...\n",
		*nRequests, *invalidFrac*100)
	zipf := rand.NewZipf(rng, 1.2, 1, uint64(len(keys)-1))
	plan := make([]string, *nRequests)
	for i := range plan {
		if rng.Float64() < *invalidFrac {
			plan[i] = "ak_live_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" // garbage
		} else {
			plan[i] = keys[zipf.Uint64()]
		}
	}

	// --- fire requests concurrently ---
	fmt.Printf("firing at concurrency %d ...\n", *concurrency)
	var (
		idx       atomic.Int64
		done      atomic.Int64
		code200   atomic.Int64
		code401   atomic.Int64
		codeOther atomic.Int64
	)
	latencies := make([][]time.Duration, *concurrency)
	var wg sync.WaitGroup

	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			local := make([]time.Duration, 0, *nRequests / *concurrency + 1)
			for {
				i := idx.Add(1) - 1
				if i >= int64(*nRequests) {
					break
				}
				key := plan[i]
				t0 := time.Now()
				req, _ := http.NewRequest("POST", *base+"/v1/validate", nil)
				req.Header.Set("Authorization", "Bearer "+key)
				resp, err := client.Do(req)
				lat := time.Since(t0)
				local = append(local, lat)
				if err != nil {
					codeOther.Add(1)
				} else {
					switch resp.StatusCode {
					case 200:
						code200.Add(1)
					case 401:
						code401.Add(1)
					default:
						codeOther.Add(1)
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				done.Add(1)
			}
			latencies[w] = local
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// --- aggregate latencies ---
	var all []time.Duration
	for _, l := range latencies {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	pct := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		return all[int(float64(len(all))*p)]
	}

	fmt.Printf("\n=== throughput ===\n")
	fmt.Printf("  %d requests in %.1fs  =  %.0f rps\n",
		*nRequests, elapsed.Seconds(), float64(*nRequests)/elapsed.Seconds())
	fmt.Printf("=== latency ===\n")
	fmt.Printf("  p50 %v   p95 %v   p99 %v\n", pct(0.50), pct(0.95), pct(0.99))
	fmt.Printf("=== status codes ===\n")
	fmt.Printf("  200: %d   401: %d   other: %d\n",
		code200.Load(), code401.Load(), codeOther.Load())
	fmt.Printf("\n=== cache stats: sum across replicas ===\n")
	fmt.Printf("  for c in $(docker compose ps -q keyservice); do \\\n")
	fmt.Printf("    docker exec $c wget -qO- localhost:8080/v1/cache/stats; echo; done\n")
}
