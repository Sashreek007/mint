//go:build live

package integration

import (
	"net/http"
	"os"
	"testing"
)

// mintEnv (live mode, `go test -tags live`) targets an already-running stack
// started with `docker compose up`. Override the URL with MINT_URL; the default
// is localhost:8080. If nothing is listening, the suite skips rather than fails.
func mintEnv(t *testing.T) string {
	t.Helper()
	url := os.Getenv("MINT_URL")
	if url == "" {
		url = "http://localhost:8080"
	}
	resp, err := http.Get(url + "/healthz")
	if err != nil {
		t.Skipf("live stack unreachable at %s (%v) — run `docker compose up -d` first", url, err)
	}
	resp.Body.Close()
	return url
}
