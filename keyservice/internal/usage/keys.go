package usage

import "strings"

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
