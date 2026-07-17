package httpapi

import (
	"net"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	r        rate.Limit
	b        int
}

func newIPRateLimiter(r rate.Limit, b int) *ipRateLimiter {
	return &ipRateLimiter{limiters: make(map[string]*rate.Limiter), r: r, b: b}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	lim, ok := l.limiters[ip]
	if !ok {
		lim = rate.NewLimiter(l.r, l.b)
		l.limiters[ip] = lim
	}
	l.mu.Unlock()
	return lim.Allow()
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
