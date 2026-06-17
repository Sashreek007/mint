// Command burst hammers the demo product with concurrent requests using one API
// key, then prints how many Mint allowed vs rate-limited vs quota-exceeded.
// It's the live demo of Mint's enforcement: watch 200s turn into 429s.
//
//	go run ./cmd/burst -key <API_KEY> -n 1000 -c 50
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://localhost:9000/v1/price/AAPL", "endpoint to hammer")
	key := flag.String("key", "", "API key sent as Bearer token")
	n := flag.Int("n", 1000, "total requests to fire")
	c := flag.Int("c", 50, "concurrent workers")
	flag.Parse()

	// Lock-free tally: atomics let 50 goroutines count without a mutex.
	var allowed, rateLimited, quota, invalid, unavailable, errs atomic.Int64

	// One pooled connection per worker. The default MaxIdleConnsPerHost is 2,
	// which would throttle 50 workers into reconnecting constantly — bump it so
	// the throughput number is honest.
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{MaxIdleConns: *c, MaxIdleConnsPerHost: *c},
	}

	// Classic worker pool: c goroutines pull jobs off a channel until it closes.
	jobs := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < *c; i++ {
		wg.Go(func() {
			for range jobs {
				req, _ := http.NewRequest(http.MethodGet, *url, nil)
				req.Header.Set("Authorization", "Bearer "+*key)
				resp, err := client.Do(req)
				if err != nil {
					errs.Add(1)
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusOK:
					allowed.Add(1)
				case http.StatusUnauthorized:
					invalid.Add(1)
				case http.StatusTooManyRequests:
					// rate vs quota are both 429 — the body's message disambiguates.
					if strings.Contains(string(body), "quota") {
						quota.Add(1)
					} else {
						rateLimited.Add(1)
					}
				case http.StatusServiceUnavailable:
					unavailable.Add(1) // Mint unreachable → fail-closed
				default:
					errs.Add(1)
				}
			}
		})
	}

	// Live progress line, redrawn 4x/sec with \r.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Printf("\rallowed=%d  rate_limited=%d  quota_exceeded=%d  invalid=%d  unavailable=%d   ",
					allowed.Load(), rateLimited.Load(), quota.Load(), invalid.Load(), unavailable.Load())
			}
		}
	}()

	start := time.Now()
	for i := 0; i < *n; i++ {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
	close(done)
	dur := time.Since(start)

	fmt.Printf("\rallowed=%d  rate_limited=%d  quota_exceeded=%d  invalid=%d  unavailable=%d  errors=%d\n",
		allowed.Load(), rateLimited.Load(), quota.Load(), invalid.Load(), unavailable.Load(), errs.Load())
	fmt.Printf("%d requests in %s = %.0f req/s\n", *n, dur.Round(time.Millisecond), float64(*n)/dur.Seconds())
}
