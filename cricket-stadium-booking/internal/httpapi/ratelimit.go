package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// An entry idle longer than limiterIdleTTL is evicted; the janitor runs
	// every limiterSweepInterval. Without eviction the map grows one entry per
	// distinct client IP forever — an unbounded memory leak under a flood of
	// spoofed/rotating source IPs, which is exactly the abuse this limiter
	// exists to blunt.
	limiterIdleTTL       = 10 * time.Minute
	limiterSweepInterval = time.Minute
)

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*limiterEntry
	r        rate.Limit
	b        int
}

func newIPRateLimiter(r rate.Limit, b int) *ipRateLimiter {
	l := &ipRateLimiter{limiters: make(map[string]*limiterEntry), r: r, b: b}
	go l.cleanupLoop()
	return l
}

func (l *ipRateLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	e, ok := l.limiters[ip]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(l.r, l.b)}
		l.limiters[ip] = e
	}
	e.lastSeen = now
	l.mu.Unlock()
	return e.limiter.Allow()
}

// cleanupLoop evicts entries whose last request is older than limiterIdleTTL.
// It runs for the life of the process (the limiter is created once at startup);
// a full-token idle limiter carries no rate-limiting state worth preserving, so
// dropping and lazily recreating it is safe.
func (l *ipRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(limiterSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-limiterIdleTTL)
		l.mu.Lock()
		for ip, e := range l.limiters {
			if e.lastSeen.Before(cutoff) {
				delete(l.limiters, ip)
			}
		}
		l.mu.Unlock()
	}
}

// rateLimitMiddleware caps mutating requests per client IP (CODE_REVIEW.md
// finding #7): combined with no-auth, an unthrottled client could otherwise
// flood /hold with random buyer_ids to lock every seat in the stadium.
// A simple in-memory per-IP limiter is sufficient for this build's scope —
// it doesn't survive a restart or scale across multiple instances. A real
// deployment would move this to a shared store or a gateway/LB layer, per
// real-scale-topology.md's horizontal-scaling section.
func rateLimitMiddleware(next http.Handler) http.Handler {
	limiter := newIPRateLimiter(10, 20) // 10 req/s sustained, burst 20, per IP

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !limiter.allow(ip) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
