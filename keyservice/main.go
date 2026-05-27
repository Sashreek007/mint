package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
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

	addr := ":8080"
	log.Printf("keyservice replica=%s listening on %s", replicaID, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func authAdmin(r *http.Request, expected string) bool {
	got := r.Header.Get("X-Admin-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
