package ratelimit

import (
	"context"
	_ "embed"
	"time"

	"github.com/redis/go-redis/v9"
)

// This is Go's embed feature.
// At compile time, Go reads bucket.lua and
// bakes its contents into the binary as the string bucketScript.
// So the script ships inside the binary — no separate .lua file to deploy,
// no file-not-found at runtime.
//
//go:embed bucket.lua
var bucketScript string

// Limiter runts the token-bucket script against Redis
type Limiter struct {
	script   *redis.Script
	rate     int //tps
	capacity int
}

func New(rate, capacity int) *Limiter {
	return &Limiter{
		script:   redis.NewScript(bucketScript),
		rate:     rate,
		capacity: capacity,
	}
}

// Allow reports whether a request for KeyHash may proceed.
// Faiils open if Redis errors, avialbility is more important
func (l *Limiter) Allow(ctx context.Context, rdb *redis.Client, keyHash string) bool {
	now := float64(time.Now().UnixNano()) / 1e9 //current time in secs
	res, err := l.script.Run(ctx, rdb,
		[]string{"ratelimit" + keyHash}, //KEYS[1]
		l.rate, l.capacity, now,         //ARGV[1..3]
	).Int()
	if err != nil {
		return true
	}
	return res == 1
}
