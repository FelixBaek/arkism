// Package httpguard supplies the shared local HTTP boundary. SSH forwards must
// address the loopback endpoint; forwarded Host headers are never trusted.
package httpguard

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

func Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// Only local listeners require a loopback Host. Explicit server-mode
		// listeners retain their own DNS name and transport authentication.
		if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			host, _, _ := net.SplitHostPort(addr.String())
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && !LoopbackHost(r.Host) {
				http.Error(w, "invalid host", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func LoopbackHost(authority string) bool {
	host := authority
	if strings.Contains(authority, ":") {
		var err error
		var port string
		host, port, err = net.SplitHostPort(authority)
		if err == nil {
			n, e := strconv.Atoi(port)
			if e != nil || n < 1 || n > 65535 {
				return false
			}
		}
		if err != nil {
			// A bracketed IPv6 literal without a port is also legal.
			if !strings.HasPrefix(authority, "[") || !strings.HasSuffix(authority, "]") {
				return false
			}
			host = authority[1 : len(authority)-1]
		}
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Limiter is a constant-memory, process-wide token bucket. It deliberately
// ignores client-controlled IP headers and bounds expensive login attempts.
type Limiter struct {
	mu       sync.Mutex
	tokens   float64
	last     time.Time
	burst    int
	interval time.Duration
}

func NewLimiter(burst int, interval time.Duration) *Limiter {
	return &Limiter{tokens: float64(burst), last: time.Now(), burst: burst, interval: interval}
}

func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens = min(float64(l.burst), l.tokens+now.Sub(l.last).Seconds()/l.interval.Seconds())
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
