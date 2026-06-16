package keysvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Outcome int

const (
	Allowed Outcome = iota
	Invalid
	RateLimited
	QuotaExceeded
)

type Result struct {
	Outcome  Outcome
	TenantID string
	KeyID    string
}

type Client struct {
	baseURL  string
	http     *http.Client
	failOpen bool
}

type Option func(*Client)

func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }
func WithTimeout(d time.Duration) Option   { return func(c *Client) { c.http.Timeout = d } }
func WithFailOpen() Option                 { return func(c *Client) { c.failOpen = true } }

func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 2 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Validate calls Mint's POST /v1/validate. The returned error is non-nil ONLY on
// transport failure (Mint unreachable / 5xx); auth & limit decisions come back in
// Result.Outcome.
func (c *Client) Validate(ctx context.Context, key string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/validate", nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	var body struct {
		Valid    bool   `json:"valid"`
		TenantID string `json:"tenant_id"`
		KeyID    string `json:"key_id"`
		Error    string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)

	switch resp.StatusCode {
	case http.StatusOK:
		return Result{Outcome: Allowed, TenantID: body.TenantID, KeyID: body.KeyID}, nil
	case http.StatusUnauthorized, http.StatusBadRequest:
		return Result{Outcome: Invalid}, nil
	case http.StatusTooManyRequests:
		if strings.Contains(body.Error, "quota") {
			return Result{Outcome: QuotaExceeded}, nil
		}
		return Result{Outcome: RateLimited}, nil
	default:
		return Result{}, fmt.Errorf("keyservice: unexpected status %d", resp.StatusCode)
	}
}
