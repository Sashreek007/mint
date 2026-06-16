// Package api wires HTTP handlers to the store. It owns everything HTTP and
// nothing SQL.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Sashreek007/mint/keyservice/internal/cache"
	"github.com/Sashreek007/mint/keyservice/internal/keys"
	"github.com/Sashreek007/mint/keyservice/internal/ratelimit"
	"github.com/Sashreek007/mint/keyservice/internal/store"
	"github.com/Sashreek007/mint/keyservice/internal/usage"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

const (
	ttlValid   = 5 * time.Minute
	ttlInvalid = 30 * time.Second
)

// Server holds the handlers' dependencies. Lowercase fields => private; they
// are injected once via New and never mutated.
type Server struct {
	store      *store.Store
	cache      *cache.Cache
	l2         *cache.L2
	rdb        *redis.Client
	limiter    *ratelimit.Limiter
	adminToken string
	keyPepper  string
	replicaID  string

	l1Hits atomic.Int64
	l2Hits atomic.Int64
	misses atomic.Int64
}

func New(st *store.Store, c *cache.Cache, l2 *cache.L2, rdb *redis.Client, limiter *ratelimit.Limiter, adminToken, keyPepper, replicaID string) *Server {
	return &Server{store: st, cache: c, l2: l2, rdb: rdb, limiter: limiter, adminToken: adminToken, keyPepper: keyPepper, replicaID: replicaID}
}

// Routes builds the router. All registration happens here, once — the lesson
// from the 502 bug: registration is startup wiring, not handler code.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /admin/tenants", s.handleCreateTenant)
	mux.HandleFunc("POST /v1/tenants/{id}/keys", s.handleCreateKey)
	mux.HandleFunc("POST /v1/validate", s.handleValidate)
	mux.HandleFunc("POST /v1/keys/{id}/revoke", s.handleRevokeKey)
	mux.HandleFunc("GET /v1/cache/stats", s.handleCacheStats)
	mux.HandleFunc("GET /v1/tenants/{id}/usage", s.handleTenantUsage)
	mux.Handle("GET /metrics", promhttp.Handler()) // ← expose the metrics page
	return metricsMiddleware(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","replica":%q}`+"\n", s.replicaID)
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Name         string `json:"name"`
		MonthlyQuota *int64 `json:"monthly_quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	tenant, err := s.store.CreateTenant(r.Context(), req.Name, req.MonthlyQuota)
	if err != nil {
		log.Printf("create tenant: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(tenant)
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tenantID := r.PathValue("id")

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	fullKey, err := keys.Generate()
	if err != nil {
		log.Printf("generate key: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	keyPrefix := fullKey[:20]
	keyHash := keys.Hash(s.keyPepper, fullKey)

	created, err := s.store.CreateAPIKey(r.Context(), tenantID, req.Name, keyPrefix, keyHash)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidTenantID):
			writeJSONError(w, http.StatusBadRequest, "invalid tenant id")
		case errors.Is(err, store.ErrTenantNotFound):
			writeJSONError(w, http.StatusNotFound, "tenant not found")
		default:
			log.Printf("create api key: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// Embed the stored metadata and add the plaintext key (shown once).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		store.APIKey
		Key string `json:"key"`
	}{created, fullKey})
}

func (s *Server) authAdmin(r *http.Request) bool {
	got := r.Header.Get("X-Admin-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) == 1
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`+"\n", msg)
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	rawKey, ok := strings.CutPrefix(authHeader, "Bearer ")
	if !ok || rawKey == "" {
		writeJSONError(w, http.StatusBadRequest, "missing bearer token")
		return
	}

	keyHash := keys.Hash(s.keyPepper, rawKey)
	cacheKey := string(keyHash)
	ctx := r.Context()

	// --- L1: in-process ---
	if v, hit := s.cache.Get(cacheKey); hit {
		s.l1Hits.Add(1)
		validateCacheEvents.WithLabelValues("l1").Inc()
		s.writeValidateResult(w, r, cacheKey, v.(cache.Result))
		return
	}

	// --- L2: Redis ---
	if res, hit := s.l2.Get(ctx, cacheKey); hit {
		s.l2Hits.Add(1)
		validateCacheEvents.WithLabelValues("l2").Inc()
		s.cache.Set(cacheKey, res, ttlFor(res)) // backfill L1
		s.writeValidateResult(w, r, cacheKey, res)
		return
	}

	// --- L3: Postgres (source of truth) ---
	s.misses.Add(1)
	validateCacheEvents.WithLabelValues("miss").Inc()
	vk, err := s.store.ValidateKey(ctx, keyHash)
	var res cache.Result
	if err != nil {
		if !errors.Is(err, store.ErrKeyNotValid) {
			log.Printf("validate key: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return // real DB error → don't cache
		}
		res = cache.Result{Valid: false}
	} else {
		res = cache.Result{Valid: true, TenantID: vk.TenantID, KeyID: vk.KeyID, MonthlyQuota: vk.MonthlyQuota}
	}

	// write through to both cache tiers
	s.cache.Set(cacheKey, res, ttlFor(res))
	s.l2.Set(ctx, cacheKey, res, ttlFor(res))
	s.writeValidateResult(w, r, cacheKey, res)
}

// ttlFor picks the TTL based on whether the result is valid.
func ttlFor(res cache.Result) time.Duration {
	if res.Valid {
		return ttlValid
	}
	return ttlInvalid
}

func (s *Server) writeValidateResult(w http.ResponseWriter, r *http.Request, cacheKey string, res cache.Result) {
	w.Header().Set("Content-Type", "application/json")

	// invalid keys: 401, no rate-limit needed
	if !res.Valid {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(struct {
			Valid bool `json:"valid"`
		}{false})
		return
	}

	//valid key: one Redis round-trip does rate-limit + quota + usage meter
	switch s.limiter.Check(r.Context(), s.rdb, cacheKey, res.TenantID, res.MonthlyQuota) {
	case ratelimit.RateLimited:
		validateRejected.WithLabelValues("rate").Inc()
		w.WriteHeader(http.StatusTooManyRequests) // 429
		_ = json.NewEncoder(w).Encode(struct {
			Valid bool   `json:"valid"`
			Error string `json:"error"`
		}{true, "rate limit exceeded"})
		return
	case ratelimit.QuotaExceeded:
		validateRejected.WithLabelValues("quota").Inc()
		w.WriteHeader(http.StatusTooManyRequests) // 429
		_ = json.NewEncoder(w).Encode(struct {
			Valid bool   `json:"valid"`
			Error string `json:"error"`
		}{true, "monthly quota exceeded"})
		return
	}

	// valid + within limit: 200
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Valid    bool   `json:"valid"`
		TenantID string `json:"tenant_id"`
		KeyID    string `json:"key_id"`
	}{true, res.TenantID, res.KeyID})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	keyID := r.PathValue("id")
	ctx := r.Context()

	//revoke in postgres, get the hash back
	keyHash, err := s.store.RevokeKey(ctx, keyID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidTenantID):
			writeJSONError(w, http.StatusBadRequest, "invalid key id")
		case errors.Is(err, store.ErrKeyNotValid):
			writeJSONError(w, http.StatusNotFound, "key not found or already revoked")
		default:
			log.Printf("revoke key: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	cacheKey := string(keyHash)

	//evict from L2 cache
	s.l2.Delete(ctx, cacheKey)

	if err := cache.PublishRevocation(ctx, s.rdb, cacheKey); err != nil {
		log.Printf("publish revocation: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	l1 := s.l1Hits.Load()
	l2 := s.l2Hits.Load()
	miss := s.misses.Load()
	total := l1 + l2 + miss

	var hitRate float64
	if total > 0 {
		hitRate = float64(l1+l2) / float64(total)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		L1Hits  int64   `json:"l1_hits"`
		L2Hits  int64   `json:"l2_hits"`
		Misses  int64   `json:"misses"`
		Total   int64   `json:"total"`
		HitRate float64 `json:"hit_rate"`
		Replica string  `json:"replica"`
	}{l1, l2, miss, total, hitRate, s.replicaID})
}

func (s *Server) handleTenantUsage(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tenantID := r.PathValue("id")
	ctx := r.Context()
	quota, err := s.store.GetTenantQuota(ctx, tenantID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidTenantID):
			writeJSONError(w, http.StatusBadRequest, "invalid tenant id")
		case errors.Is(err, store.ErrTenantNotFound):
			writeJSONError(w, http.StatusNotFound, "tenant not found")
		default:
			log.Printf("get tenant quota: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// live current-period count from Redis: miss (or error) -> 0 (fail-soft)
	period := usage.CurrentPeriod(time.Now())
	used, _ := s.rdb.Get(ctx, usage.Key(period, tenantID)).Int64()

	var remaining *int64
	if quota != nil {
		rem := *quota - used
		if rem < 0 {
			rem = 0
		}
		remaining = &rem
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		TenantID  string `json:"tenant_id"`
		Period    string `json:"period"`
		Used      int64  `json:"used"`
		Quota     *int64 `json:"quota"`
		Remaining *int64 `json:"remaining"`
	}{tenantID, period, used, quota, remaining})
}
