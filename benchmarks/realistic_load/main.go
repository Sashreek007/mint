// realistic_load — a sustained, lifelike load generator for filling the Mint
// Grafana dashboard with realistic data.
//
// Unlike loadgen (fixed request count, fires as fast as possible), this models a
// real workload over TIME:
//   - many tenants, a varied number of keys each (2–19), like real customers
//   - ~1/3 of tenants are quota-capped, so they exceed their monthly quota and
//     produce 429-QUOTA rejects (the rest are unlimited)
//   - Zipf-skewed traffic — a few hot keys dominate and exceed their per-key
//     rate limit, producing 429-RATE rejects
//   - the aggregate request rate follows a sine wave (so the graphs undulate,
//     not a flat line) for a configurable duration
//   - 5% invalid keys → 401s
//
// Run it, then watch http://localhost:3000/d/mint-validate (set the time range
// to "Last 5 minutes").
//
// Usage:  cd benchmarks/realistic_load && go run . -duration 2m
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var (
	base        = flag.String("base", "http://localhost:8080", "base URL")
	admin       = flag.String("admin", "just-works-for-now", "admin token")
	duration    = flag.Duration("duration", 2*time.Minute, "how long to run")
	nTenants    = flag.Int("tenants", 50, "number of tenants")
	concurrency = flag.Int("concurrency", 150, "worker goroutines")
	rateMin     = flag.Float64("rate-min", 800, "requests/sec at the wave trough")
	rateMax     = flag.Float64("rate-max", 3000, "requests/sec at the wave peak")
	wavePeriod  = flag.Duration("wave", 30*time.Second, "rate wave period")
	invalidFrac = flag.Float64("invalid", 0.05, "fraction of requests using a garbage key")
)

var client = &http.Client{
	Timeout:   5 * time.Second,
	Transport: &http.Transport{MaxIdleConns: 400, MaxIdleConnsPerHost: 400, MaxConnsPerHost: 400},
}

func post(path string, body any, hdr map[string]string) []byte {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest("POST", *base+path, r)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out
}

func main() {
	flag.Parse()
	rng := rand.New(rand.NewSource(7)) // fixed seed → reproducible population & traffic
	adminHdr := map[string]string{"X-Admin-Token": *admin}

	// --- build a realistic tenant/key population ---
	fmt.Printf("creating %d tenants (varied keys, ~1/3 quota-capped) ...\n", *nTenants)
	var keys []string
	capped := 0
	for t := 0; t < *nTenants; t++ {
		body := map[string]any{"name": fmt.Sprintf("tenant-%02d", t)}
		if t%3 == 0 { // cap ~1/3 with a low monthly quota so they 429-quota during the run
			body["monthly_quota"] = int64(3000 + rng.Intn(5000)) // 3k–8k
			capped++
		}
		var tr struct {
			ID string `json:"id"`
		}
		json.Unmarshal(post("/admin/tenants", body, adminHdr), &tr)

		nk := 2 + rng.Intn(18) // 2–19 keys per tenant
		for k := 0; k < nk; k++ {
			var kr struct {
				Key string `json:"key"`
			}
			json.Unmarshal(post(fmt.Sprintf("/v1/tenants/%s/keys", tr.ID),
				map[string]string{"name": fmt.Sprintf("key-%d", k)}, adminHdr), &kr)
			keys = append(keys, kr.Key)
		}
	}
	fmt.Printf("  %d keys across %d tenants (%d quota-capped)\n", len(keys), *nTenants, capped)

	zipf := rand.NewZipf(rng, 1.2, 1, uint64(len(keys)-1)) // a few hot keys dominate
	const garbage = "ak_live_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

	// --- counters ---
	var c200, c401, c429, cOther atomic.Int64

	// --- workers ---
	jobs := make(chan string, *concurrency*4)
	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				req, _ := http.NewRequest("POST", *base+"/v1/validate", nil)
				req.Header.Set("Authorization", "Bearer "+key)
				resp, err := client.Do(req)
				switch {
				case err != nil:
					cOther.Add(1)
				default:
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					switch resp.StatusCode {
					case 200:
						c200.Add(1)
					case 401:
						c401.Add(1)
					case 429:
						c429.Add(1)
					default:
						cOther.Add(1)
					}
				}
			}
		}()
	}

	// --- periodic stats ---
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(10 * time.Second)
		defer tk.Stop()
		start := time.Now()
		var last int64
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				tot := c200.Load() + c401.Load() + c429.Load() + cOther.Load()
				fmt.Printf("[%4.0fs] ~%5.0f rps | 200=%d 401=%d 429=%d other=%d\n",
					time.Since(start).Seconds(), float64(tot-last)/10.0,
					c200.Load(), c401.Load(), c429.Load(), cOther.Load())
				last = tot
			}
		}
	}()

	// --- pacer: dispatch at a sine-wave rate for the duration ---
	fmt.Printf("running %s at %.0f–%.0f rps (%.0fs waves), %.0f%% invalid ...\n",
		*duration, *rateMin, *rateMax, wavePeriod.Seconds(), *invalidFrac*100)
	const tick = 10 * time.Millisecond
	start := time.Now()
	deadline := start.Add(*duration)
	for now := time.Now(); now.Before(deadline); now = time.Now() {
		elapsed := now.Sub(start).Seconds()
		frac := 0.5 + 0.5*math.Sin(2*math.Pi*elapsed/wavePeriod.Seconds())
		rate := *rateMin + (*rateMax-*rateMin)*frac
		for i := 0; i < int(rate*tick.Seconds()); i++ {
			if rng.Float64() < *invalidFrac {
				jobs <- garbage
			} else {
				jobs <- keys[zipf.Uint64()]
			}
		}
		time.Sleep(tick)
	}
	close(jobs)
	wg.Wait()
	close(stop)

	fmt.Printf("\nDONE: 200=%d 401=%d 429=%d other=%d (total %d)\n",
		c200.Load(), c401.Load(), c429.Load(), cOther.Load(),
		c200.Load()+c401.Load()+c429.Load()+cOther.Load())
}
