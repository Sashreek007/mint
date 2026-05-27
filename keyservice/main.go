package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
