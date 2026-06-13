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

// Verdict is the outcode of the combined rate-limit +quota check
type Verdict int

const (
	Allowed Verdict = iota
	RateLimited
	QuotaExceeded
)

// usageGrace keeps a period's counter alive past month-end so the flusher
// can mirror the final value to Postgres before Redis drops the key
const usageGrace = 48 * time.Hour

func (l *Limiter) Check(ctx context.Context, rdb *redis.Client, keyHash, tenantID string, quota int64) Verdict {
	now := time.Now().UTC()
	period := now.Format("2006-01")
	firstOfNext := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	usageTTL := int(firstOfNext.Sub(now).Seconds()) + int(usageGrace.Seconds())
	usageKey := "usage:" + period + ":" + tenantID

	nowSec := float64(now.UnixNano()) / 1e9
	res, err := l.script.Run(ctx, rdb,
		[]string{"ratelimit:" + keyHash, usageKey}, //KEYS[1], KEYS[2]
		l.rate, l.capacity, nowSec, quota, usageTTL, //ARGV[1..5]
	).Int()
	if err != nil {
		return Allowed
	}
	switch res {
	case 0:
		return RateLimited
	case 2:
		return QuotaExceeded
	default:
		return Allowed
	}
}
