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
	"time"

	"github.com/Sashreek007/mint/keyservice/internal/cache"
	"github.com/Sashreek007/mint/keyservice/internal/keys"
	"github.com/Sashreek007/mint/keyservice/internal/store"
)

// cachesResult is what we store in L1 for a given key hash.
type cachedResult struct {
	valid    bool
	tenantID string
	keyID    string
}

const (
	ttlValid   = 5 * time.Minute
	ttlInvalid = 30 * time.Second
)

// Server holds the handlers' dependencies. Lowercase fields => private; they
// are injected once via New and never mutated.
type Server struct {
	store      *store.Store
	cache      *cache.Cache
	adminToken string
	keyPepper  string
	replicaID  string
}

func New(st *store.Store, c *cache.Cache, adminToken, keyPepper, replicaID string) *Server {
	return &Server{store: st, cache: c, adminToken: adminToken, keyPepper: keyPepper, replicaID: replicaID}
}

// Routes builds the router. All registration happens here, once — the lesson
// from the 502 bug: registration is startup wiring, not handler code.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /admin/tenants", s.handleCreateTenant)
	mux.HandleFunc("POST /v1/tenants/{id}/keys", s.handleCreateKey)
	mux.HandleFunc("POST /v1/validate", s.handleValidate)
	return mux
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

	tenant, err := s.store.CreateTenant(r.Context(), req.Name)
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

	// L1 check
	if v, hit := s.cache.Get(cacheKey); hit {
		s.writeValidateResult(w, v.(cachedResult))
		return
	}

	// L1 miss → Postgres
	vk, err := s.store.ValidateKey(r.Context(), keyHash)
	if err != nil {
		if errors.Is(err, store.ErrKeyNotValid) {
			cr := cachedResult{valid: false}
			s.cache.Set(cacheKey, cr, ttlInvalid)
			s.writeValidateResult(w, cr)
			return
		}
		log.Printf("validate key: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	cr := cachedResult{valid: true, tenantID: vk.TenantID, keyID: vk.KeyID}
	s.cache.Set(cacheKey, cr, ttlValid)
	s.writeValidateResult(w, cr)
}

func (s *Server) writeValidateResult(w http.ResponseWriter, cr cachedResult) {
	w.Header().Set("Content-Type", "application/json")
	if !cr.valid {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(struct {
			Valid bool `json:"valid"`
		}{false})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(struct {
		Valid    bool   `json:"valid"`
		TenantID string `json:"tenant_id"`
		KeyID    string `json:"key_id"`
	}{true, cr.tenantID, cr.keyID})
}
