package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Sashreek007/mint/keyservice/internal/api"
	"github.com/Sashreek007/mint/keyservice/internal/cache"
	"github.com/Sashreek007/mint/keyservice/internal/store"
)

func main() {
	// HOSTNAME is set by Docker to the container's short id.
	replicaID := os.Getenv("HOSTNAME")
	if replicaID == "" {
		replicaID = "local"
	}

	// --- config: read required env vars, fail fast if missing ---
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

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatal("REDIS_URL is required")
	}

	// Shared 5s startup deadline for all dependency connects (redis + postgres).
	startupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// --- redis client ---
	redisOpt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("invalid REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(redisOpt)
	defer rdb.Close()

	if err := rdb.Ping(startupCtx).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}
	log.Printf("redis ok: %s", redisOpt.Addr)
	_ = rdb

	// --- connection pool ---
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("invalid DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(startupCtx, cfg)
	if err != nil {
		log.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(startupCtx); err != nil {
		log.Fatalf("postgres ping failed: %v", err)
	}
	log.Printf("postgres ok: max_conns=%d", cfg.MaxConns)

	st := store.New(pool)
	c := cache.New()
	srv := api.New(st, c, adminToken, keyPepper, replicaID)

	addr := ":8080"
	log.Printf("keyservice replica=%s listening on %s", replicaID, addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
