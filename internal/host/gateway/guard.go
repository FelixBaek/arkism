package gateway

// The local token check in front of Handler. The gateway binds 127.0.0.1, but any local process
// (or an SSH forward) could still reach it; the token says the caller was set up by this host's
// installer. /healthz stays open: it only names the service and pid for the control probe.

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/httpguard"
)

// Guard requires the local token before Next sees a request.
type Guard struct {
	Next     http.Handler
	Token    string
	Attempts *httpguard.Limiter
	// Optional lets a call without the header through (one-release grace, ARKISM_GATEWAY_TOKEN_OPTIONAL=1);
	// a wrong token is refused either way.
	Optional bool
	// Warn receives one line per minute while tokenless calls are let through.
	Warn io.Writer
}

// tokenless counts calls the grace flag let through (shown on /healthz while the flag is on).
var tokenless atomic.Int64

var warned struct {
	sync.Mutex
	at time.Time
}

func unauthorized(w http.ResponseWriter, message string) {
	status, body := 401, mustPy(host.Obj{{Key: "type", Value: "error"}, {Key: "error", Value: host.Obj{
		{Key: "type", Value: "authentication_error"}, {Key: "message", Value: message}}}})
	w.Header().Set("content-type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="arkism-gateway"`)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (g Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if path, _ := splitURL(r.RequestURI); path == "/healthz" {
		g.Next.ServeHTTP(w, r)
		return
	}
	values := r.Header.Values(host.GatewayTokenHeader)
	r.Header.Del(host.GatewayTokenHeader) // never forwarded, whatever happens next
	switch {
	case len(values) == 1 && g.Token != "" && subtle.ConstantTimeCompare([]byte(values[0]), []byte(g.Token)) == 1:
	case len(values) == 0 && g.Optional:
		n := tokenless.Add(1)
		warned.Lock()
		if g.Warn != nil && time.Since(warned.at) >= time.Minute {
			warned.at = time.Now()
			fmt.Fprintf(g.Warn, "arkism gateway: %d call(s) without %s (ARKISM_GATEWAY_TOKEN_OPTIONAL=1); rerun install-arkism\n",
				n, host.GatewayTokenHeader)
		}
		warned.Unlock()
	case len(values) == 0:
		if !g.allowAttempt(w) {
			return
		}
		unauthorized(w, "gateway_token_missing: send the "+host.GatewayTokenHeader+" header (run install-arkism to set ANTHROPIC_CUSTOM_HEADERS)")
		return
	default:
		if !g.allowAttempt(w) {
			return
		}
		unauthorized(w, "gateway_token_invalid: the "+host.GatewayTokenHeader+" header does not match this host's gateway token")
		return
	}
	g.Next.ServeHTTP(w, r)
}

// gatewayToken is ARKISM_GATEWAY_TOKEN when set, else the token file (made on first start).
func gatewayToken() (string, error) {
	if v := os.Getenv("ARKISM_GATEWAY_TOKEN"); v != "" {
		if !host.ValidGatewayToken(v) {
			return "", fmt.Errorf("ARKISM_GATEWAY_TOKEN must be 64 lowercase hex characters")
		}
		return v, nil
	}
	return host.EnsureGatewayToken(host.GatewayTokenFile)
}

func tokenOptional() bool { return os.Getenv("ARKISM_GATEWAY_TOKEN_OPTIONAL") == "1" }

func (g Guard) allowAttempt(w http.ResponseWriter) bool {
	if g.Attempts == nil || g.Attempts.Allow() {
		return true
	}
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	return false
}
