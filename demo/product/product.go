// Package product is a tiny demo backend — a stock-price API, the kind of thing
// a trading app polls many times a second. Every route is gated by Mint in ONE
// line. The point isn't the prices; it's that you can hammer GET /v1/price/{sym}
// with ../cmd/burst and watch Mint allow, then rate-limit, then reject on quota —
// exactly how Mint protects a real high-traffic read API.
package product

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"

	keysvc "github.com/Sashreek007/mint/keyservice-go"
)

// Handler wires the product's routes and wraps them in Mint's auth middleware.
// The single client.Middleware call gates EVERY route below — no per-handler
// auth, rate-limit, or quota code anywhere in this file.
func Handler(client *keysvc.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/price/{symbol}", price)
	mux.HandleFunc("GET /v1/whoami", whoami)
	return client.Middleware(mux)
}

// price returns a (fake) live share price for a ticker symbol. By the time it
// runs, the middleware has already had Mint check the key + rate limit + quota.
func price(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	tenant, _ := keysvc.TenantID(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"symbol": symbol,
		"usd":    priceOf(symbol),
		"at":     time.Now().UTC().Format(time.RFC3339),
		"tenant": tenant,
	})
}

// priceOf derives a stable per-share price from the symbol, plus a tiny time-based
// wiggle so repeated polls look "live". A real API would hit a market feed.
func priceOf(symbol string) float64 {
	h := fnv.New32a()
	h.Write([]byte(symbol))
	base := float64(h.Sum32()%900) + 20 // a plausible share price, stable per symbol
	wiggle := float64(time.Now().UnixNano()%500) / 100.0
	return math.Round((base+wiggle)*100) / 100
}

// whoami echoes the authenticated identity — proof the middleware injected the
// tenant and key ids into the request context.
func whoami(w http.ResponseWriter, r *http.Request) {
	tenant, _ := keysvc.TenantID(r.Context())
	key, _ := keysvc.KeyID(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"tenant": tenant, "key": key})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
