package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Sashreek007/mint/keyservice/internal/api"
	"github.com/Sashreek007/mint/keyservice/internal/cache"
	"github.com/Sashreek007/mint/keyservice/internal/ratelimit"
	"github.com/Sashreek007/mint/keyservice/internal/store"
	"github.com/Sashreek007/mint/keyservice/internal/usage"
)

func main() {
	// Structured JSON logs. SetDefault also routes the stdlib log package through
	// slog, so existing log.Printf calls become JSON too.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
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
	flushInterval := 30 * time.Second
	if v := os.Getenv("FLUSH_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			flushInterval = d
		}
	}
	prewarmLimit := 1000 // default
	if v := os.Getenv("PREWARM_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			prewarmLimit = n
		}
	}
	rateLimit := 100 // tokens/sec
	if v := os.Getenv("RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rateLimit = n
		}
	}
	rateBurst := 200 // bucket capacity
	if v := os.Getenv("RATE_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rateBurst = n
		}
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
	// Pre-warm L1 with the hot key set (skip if PREWARM_LIMIT=0).
	if prewarmLimit > 0 {
		keysList, err := st.RecentActiveKeys(startupCtx, prewarmLimit)
		if err != nil {
			log.Printf("prewarm failed (continuing cold): %v", err)
		} else {
			for _, k := range keysList {
				c.Set(string(k.KeyHash), cache.Result{
					Valid:        true,
					TenantID:     k.TenantID,
					KeyID:        k.KeyID,
					MonthlyQuota: k.MonthlyQuota,
				}, time.Hour) // long TTL; pub/sub evicts on revoke
			}
			log.Printf("prewarmed %d keys into L1", len(keysList))
		}
	}
	l2 := cache.NewL2(rdb)
	limiter := ratelimit.New(rateLimit, rateBurst) // 100 req/sec, burst 200, per key
	srv := api.New(st, c, l2, rdb, limiter, adminToken, keyPepper, replicaID)

	go cache.SubscribeRevocations(context.Background(), rdb, c)
	flusher := usage.NewFlusher(rdb, st, replicaID, flushInterval)
	go flusher.Run(context.Background())
	addr := ":8080"
	log.Printf("keyservice replica=%s listening on %s", replicaID, addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
