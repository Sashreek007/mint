package usage

import (
	"strings"
	"time"
)

const Prefix = "usage:"

// ParseKey splits "usage:2026-06:<tenant>" into its period and tenant id
// ok is false if the key isn't a well formed usage key
func ParseKey(key string) (period, tenantID string, ok bool) {
	rest, found := strings.CutPrefix(key, Prefix)
	if !found {
		return "", "", false
	}
	return strings.Cut(rest, ":")
}

// CurrentPeriod is the month bucket in UTC
func CurrentPeriod(now time.Time) string {
	return now.UTC().Format("2006-01")
}

// Key builds the Redis counter key: usage:<period>:<tenant_id>
func Key(period, tenantID string) string {
	return Prefix + period + ":" + tenantID
}
