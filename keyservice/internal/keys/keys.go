// Package keys generates and hashes API keys.
package keys

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
)

// alphabet is lowercase => private to this package. It is an internal detail
// callers never need to see.
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Generate returns a fresh "ak_live_<32 base62 chars>" using crypto/rand.
// Capitalized => exported => callable as keys.Generate() from other packages.
func Generate() (string, error) {
	const bodyLen = 32
	const maxByte = 62 * 4 // 248 — largest multiple of 62 that fits in a byte

	out := make([]byte, bodyLen)
	buf := make([]byte, 1)
	for i := 0; i < bodyLen; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= maxByte { // rejection sampling — drop bytes that would bias % 62
			continue
		}
		out[i] = alphabet[buf[0]%62]
		i++
	}
	return "ak_live_" + string(out), nil
}

// Hash returns HMAC-SHA256(pepper, key) — 32 raw bytes for the BYTEA column.
func Hash(pepper, key string) []byte {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(key))
	return mac.Sum(nil)
}
