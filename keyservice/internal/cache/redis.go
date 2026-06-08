package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

type L2 struct {
	rdb *redis.Client
}

func NewL2(rdb *redis.Client) *L2 {
	return &L2{rdb: rdb}
}

const l2Prefix = "validate:"

//Get returns (results,treu) on hit . A miss OR any Redis error returns (zero, false)

func (l *L2) Get(ctx context.Context, keyHash string) (Result, bool) {
	val, err := l.rdb.Get(ctx, l2Prefix+keyHash).Bytes()
	if err != nil {
		return Result{}, false
	}
	var r Result
	if err := json.Unmarshal(val, &r); err != nil {
		return Result{}, false
	}
	return r, true
}

// set strores a result as JSON with TTL. Erros are ignored
func (l *L2) Set(ctx context.Context, keyHash string, r Result, ttl time.Duration) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = l.rdb.Set(ctx, l2Prefix+keyHash, b, ttl).Err()
}

// Delete removes a key from the L2 cache
func (l *L2) Delete(ctx context.Context, keyHash string) {
	_ = l.rdb.Del(ctx, l2Prefix+keyHash).Err()
}
