package keysvc

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type ctxKey int

const (
	tenantKey ctxKey = iota
	keyIDKey
)

// Middleware authenticates each request via Mint before calling next. On a valid
// key it injects tenant/key id into the context; otherwise it rejects.
func (c *Client) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := bearer(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "missing api key")
			return
		}
		res, err := c.Validate(r.Context(), key)
		if err != nil { // couldn't reach Mint
			if c.failOpen {
				next.ServeHTTP(w, r) // availability over strictness
				return
			}
			writeErr(w, http.StatusServiceUnavailable, "auth unavailable") // fail closed
			return
		}
		switch res.Outcome {
		case Allowed:
			ctx := context.WithValue(r.Context(), tenantKey, res.TenantID)
			ctx = context.WithValue(ctx, keyIDKey, res.KeyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		case RateLimited:
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
		case QuotaExceeded:
			writeErr(w, http.StatusTooManyRequests, "monthly quota exceeded")
		default: // Invalid
			writeErr(w, http.StatusUnauthorized, "invalid api key")
		}
	})
}

func bearer(r *http.Request) (string, bool) {
	return strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// TenantID / KeyID let a handler read who the authenticated caller is.
func TenantID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantKey).(string)
	return v, ok
}
func KeyID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(keyIDKey).(string)
	return v, ok
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
