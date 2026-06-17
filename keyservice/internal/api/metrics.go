package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

//RED metrics. One counter (rate + errors, split by status) and one histogram
//Duration. promauto registers them with the fefault registry automatically

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by method, route and status code.",
	}, []string{"method", "route", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds",
		Help: "HTTP request latency in seconds.",
		//default buckets are useless for this service, so using more dense buckets
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .05, .075, .1, .25, .5, 1},
	}, []string{"method", "route"})

	// cache tier hit/miss on /validate — the 99.7% hit-rate story
	validateCacheEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "validate_cache_events_total",
		Help: "Cache lookups on /validate by tier (l1, l2, miss).",
	}, []string{"tier"})

	// valid keys rejected after the gate — split by why
	validateRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "validate_rejected_total",
		Help: "Valid keys rejected on /validate by reason (rate, quota).",
	}, []string{"reason"})
)

//statusRecorder wraps http.ResponseWriter to capture the status code, which the
//stdlib swallos after writeHEADER is called

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// metricsMiddleware records RED for every request, exactly once
// around the real handler chain
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK} // default 200 if handler never sets it
		next.ServeHTTP(rec, r)

		route := routeLabel(r)
		httpRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// routeLabel returns the matched route
func routeLabel(r *http.Request) string {
	if r.Pattern == "" {
		return "other"
	}

	if _, path, ok := strings.Cut(r.Pattern, " "); ok {
		return path
	}
	return r.Pattern
}
