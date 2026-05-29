package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	//HOSTNAME is automatically set by docker in every container to the containers short id

	replicaID := os.Getenv("HOSTNAME")
	if replicaID == "" {
		replicaID = "local"
	}
	// Read DSN, fail fast if missing
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	adminToken := os.Getenv("ADMIN_TOKEN")
	if adminToken == "" {
		log.Fatal("ADMIN_TOKEN is required")
	}
	keyPepper := os.Getenv("KEY_PEPPER")
	if keyPepper == "" {
		log.Fatal("KEY_PEPPER is required")
	}

	// Parse DSN into a pgxpool. Config and set sizing knobs
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("invalid DATABASE_URL: %v", err)

	}

	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	// Open the pool with a 5s startup timeout
	startupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(startupCtx, cfg)
	if err != nil {
		log.Fatalf("pgxpool.NewWithConfig: %v", err)

	}
	defer pool.Close()
	// Ping to prove Postgres is actually rechable
	if err := pool.Ping(startupCtx); err != nil {
		log.Fatalf("postgres ping failed: %v", err)
	}

	log.Printf("postgres ok: max_conns=%d", cfg.MaxConns)

	// HHTP handlers
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","replica":"%s"}`+"\n", replicaID)
	})

	mux.HandleFunc("POST /admin/tenants", func(w http.ResponseWriter, r *http.Request) {

		// auth
		if !authAdmin(r, adminToken) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		//parge the body
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

		//insert and return the created row
		var tenant struct {
			ID        string    `json:"id"`
			Name      string    `json:"name"`
			Status    string    `json:"status"`
			CreatedAt time.Time `json:"created_at"`
		}

		err := pool.QueryRow(r.Context(),
			`INSERT INTO tenants (name)
			values ($1)
			RETURNING id, name, status, created_at`,
			req.Name,
		).Scan(&tenant.ID, &tenant.Name, &tenant.Status, &tenant.CreatedAt)
		if err != nil {
			log.Printf("insert tenant: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// 201 and JSON body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(tenant)
	})

	mux.HandleFunc("POST /v1/tenants/{id}/keys", func(w http.ResponseWriter, r *http.Request) {
		// auth — same admin gate as before
		if !authAdmin(r, adminToken) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// pull {id} out of the URL path
		tenantID := r.PathValue("id")

		// body: just a name
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

		// generate the plaintext key, derive prefix + hash
		fullKey, err := generateKey()
		if err != nil {
			log.Printf("generateKey: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		keyPrefix := fullKey[:20]              // "ak_live_" + first 12 random chars
		keyHash := hashKey(keyPepper, fullKey) //  BYTEA

		// insert; let Postgres validate the tenant via the FK + uuid cast
		var id string
		var createdAt time.Time
		err = pool.QueryRow(r.Context(),
			`INSERT INTO api_keys (tenant_id, name, key_prefix, key_hash)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`,
			tenantID, req.Name, keyPrefix, keyHash,
		).Scan(&id, &createdAt)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch pgErr.Code {
				case "22P02": // invalid_text_representation — {id} wasn't a UUID
					writeJSONError(w, http.StatusBadRequest, "invalid tenant id")
					return
				case "23503": // foreign_key_violation — no such tenant
					writeJSONError(w, http.StatusNotFound, "tenant not found")
					return
				}
			}
			log.Printf("insert api_key: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// 201 — the ONLY time the plaintext key is ever returned
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(struct {
			ID        string    `json:"id"`
			TenantID  string    `json:"tenant_id"`
			Name      string    `json:"name"`
			Key       string    `json:"key"`
			KeyPrefix string    `json:"key_prefix"`
			CreatedAt time.Time `json:"created_at"`
		}{id, tenantID, req.Name, fullKey, keyPrefix, createdAt})
	})

	addr := ":8080"
	//Binds to port 8080.
	//Loops forever on accept().
	//For each accepted TCP connection: spawns a goroutine that reads HTTP, looks up the right handler in mux, calls it.
	//Blocks the calling goroutine (here, main) until the listener errors.
	log.Printf("keyservice replica=%s listening on %s", replicaID, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func authAdmin(r *http.Request, expected string) bool {
	got := r.Header.Get("X-Admin-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`+"\n", msg)
}

const keyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" //base62 alphabet

func generateKey() (string, error) {
	const bodyLen = 32
	const maxByte = 62 * 4

	out := make([]byte, bodyLen)
	buf := make([]byte, 1)

	for i := 0; i < bodyLen; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= maxByte {
			continue
		}
		out[i] = keyAlphabet[buf[0]%62]
		i++
	}
	return "ak_live_" + string(out), nil
}

// hashKey returns HMAC sha256 - 32 raw bytes for the bytea column
func hashKey(pepper, key string) []byte {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(key))
	return mac.Sum(nil)
}
