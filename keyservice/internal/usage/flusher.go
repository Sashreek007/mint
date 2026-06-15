package usage

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Sashreek007/mint/keyservice/internal/store"
	"github.com/redis/go-redis/v9"
)

const leaseKey = "lock:usage-flush"

// Flusher periodically mirrors the live Redis usage counters into Postgres.
type Flusher struct {
	rdb       *redis.Client
	store     *store.Store
	replicaID string
	interval  time.Duration
}

func NewFlusher(rdb *redis.Client, st *store.Store, replicaID string, interval time.Duration) *Flusher {
	return &Flusher{rdb: rdb, store: st, replicaID: replicaID, interval: interval}
}

// Run blocks, flushing once per interval until ctx is cancelled, Only the
// replica that wins the lease flushes each round; the others skip.
func (f *Flusher) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !f.acquireLease(ctx) {
				continue
			}
			n, err := f.flushOnce(ctx)
			if err != nil {
				log.Printf("usage flush failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("usage flush: mirrored %d counter(s) to postgres (replica=%s)", n, f.replicaID)
			}
		}
	}
}

// acquireLease grabs the once per leave flush lock. SET NX EX is atomic.
// Exactly one replica wins, and the lock self-expires so a crashed leader cant
// block forever. the flush is idempotent,
// so this is an optimization (skip duplicate writes).
func (f *Flusher) acquireLease(ctx context.Context) bool {
	ok, err := f.rdb.SetNX(ctx, leaseKey, f.replicaID, f.interval).Result()
	if err != nil {
		return false
	}
	return ok
}

// flushOnce reads every usafe counter from Redis and mirrors it to Postgres in
// one batched UPSERT. Returns how many counters were mirrored.
func (f *Flusher) flushOnce(ctx context.Context) (int, error) {
	// SCAN( never KEYS) for usage:* - non blocking, cursor based
	var keys []string
	iter := f.rdb.Scan(ctx, 0, Prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	// MGET all counts in a single round-trip
	vals, err := f.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, err
	}
	// Piar each key with its count, build the batch
	var rows []store.Usage
	for i, key := range keys {
		if vals[i] == nil {
			continue // key expired between SCAN and MGET
		}

		period, tenantID, ok := ParseKey(key)
		if !ok {
			continue
		}
		count, err := toInt64(vals[i])
		if err != nil {
			continue
		}
		rows = append(rows, store.Usage{tenantID: tenantID, Period: period, Count: count})
	}

	//One batched UPSERT mirrors them all
	if err := f.store.UpsertUsage(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func toInt64(v any) (int64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("usage valye not a string: %T", v)
	}
	return strconv.ParseInt(s, 10, 64)
}
