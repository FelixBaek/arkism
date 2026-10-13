package memory

// Provider adapters and the shared HTTP helpers they use (embedders are in
// gemini.go and tei.go).
//
// Everything is configuration driven. No adapter invents a provider revision,
// a scope, an authority label or an evidence reference, and every failure is
// fail closed: callers get a sentinel error from model.go, never a provider
// message, body or credential.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	embedInputQuery    = "query"
	embedInputDocument = "document"

	defaultDimension   = 1024
	defaultTimeout     = 30 * time.Second
	defaultResponseMax = 4 << 20
	defaultRequestMax  = 8 << 20
	roleHuman          = "human"
	roleAgent          = "agent"
	roleService        = "service"
	rolePipeline       = "pipeline"
)

// Principal roles a decision may claim. This is a principal role, not the RBAC
// permission name from docs/08 section 10.
var decisionRoles = map[string]bool{
	roleHuman:    true,
	roleAgent:    true,
	roleService:  true,
	rolePipeline: true,
}

// LocalStandaloneAuthorizer is deliberately limited to one loopback-only
// deployment. It accepts separate owner and worker credentials by SHA-256
// digest; callers never send those digests.
type LocalStandaloneAuthorizer struct {
	ownerHash  [sha256.Size]byte
	workerHash [sha256.Size]byte
}

type LocalStandaloneConfig struct {
	OwnerTokenSHA256  string
	WorkerTokenSHA256 string
}

func standaloneTokenHash(value string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) != len(out) {
		return out, fmt.Errorf("standalone token digest: %w", ErrInvalid)
	}
	copy(out[:], raw)
	return out, nil
}

func NewLocalStandaloneAuthorizer(cfg LocalStandaloneConfig) (*LocalStandaloneAuthorizer, error) {
	owner, err := standaloneTokenHash(cfg.OwnerTokenSHA256)
	if err != nil {
		return nil, err
	}
	worker, err := standaloneTokenHash(cfg.WorkerTokenSHA256)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(owner[:], worker[:]) == 1 {
		return nil, fmt.Errorf("standalone owner and worker credentials must differ: %w", ErrInvalid)
	}
	return &LocalStandaloneAuthorizer{ownerHash: owner, workerHash: worker}, nil
}

func (a *LocalStandaloneAuthorizer) Authorize(_ context.Context, token string, request Authorization) (Decision, error) {
	if a == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(request.Action) == "" || !validScope(request.Scope) {
		return Decision{}, ErrDenied
	}
	sum := sha256.Sum256([]byte(token))
	principal, role, decisionID := "", "", ""
	if subtle.ConstantTimeCompare(sum[:], a.ownerHash[:]) == 1 {
		principal, role, decisionID = "local-owner", roleHuman, "local-owner"
	} else if subtle.ConstantTimeCompare(sum[:], a.workerHash[:]) == 1 {
		principal, role, decisionID = "local-worker", roleService, "local-worker"
	} else {
		return Decision{}, ErrDenied
	}
	return Decision{
		Context: request.Context, DecisionID: decisionID, Allowed: true,
		PrincipalID: principal, Role: role, Scope: request.Scope,
		ExternalTransfer: true, ContentSafe: true,
	}, nil
}

// postJSON performs one bounded JSON POST. Redirects are never followed, so a
// caller credential cannot be replayed to another host, and only the status
// code and a bounded body travel back to the caller.
func postJSON(ctx context.Context, client *http.Client, endpoint, credential string, payload any, maxRequest, maxResponse int64) ([]byte, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encode request: %w", ErrInvalid)
	}
	if int64(len(encoded)) > maxRequest {
		return nil, 0, fmt.Errorf("request body exceeds %d bytes: %w", maxRequest, ErrInvalid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", ErrInvalid)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := client.Do(req)
	if err != nil {
		// Keep the transport cause in the chain (deadline, connection
		// refused, reset) so callers can classify the failure into fixed
		// reason codes. The text itself never reaches an API response.
		return nil, 0, fmt.Errorf("call dependency: %w: %w", err, ErrUnavailable)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", ErrUnavailable)
	}
	if int64(len(body)) > maxResponse {
		return nil, resp.StatusCode, fmt.Errorf("response body exceeds %d bytes: %w", maxResponse, ErrUnavailable)
	}
	return body, resp.StatusCode, nil
}

// decodeStrict reads a response envelope that must not carry unknown fields.
func decodeStrict(body []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrUnavailable
	}
	return nil
}

// dependencyStatusError classifies a non success status without copying any
// provider message into the error.
func dependencyStatusError(operation string, status int) error {
	if status == http.StatusTooManyRequests || status >= 500 {
		return fmt.Errorf("%s: dependency status %d: %w", operation, status, ErrUnavailable)
	}
	return fmt.Errorf("%s: dependency status %d: %w", operation, status, ErrInvalid)
}

// providerClient keeps the injected transport but always blocks redirects and
// keeps one bounded timeout.
func providerClient(injected *http.Client, timeout time.Duration) *http.Client {
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if injected != nil {
		client.Transport = injected.Transport
		if client.Timeout <= 0 {
			client.Timeout = injected.Timeout
		}
	}
	if client.Timeout <= 0 {
		client.Timeout = defaultTimeout
	}
	return client
}

// validateEndpoint accepts https anywhere and plain http only on loopback, so
// httptest and local development work while a credential never travels in the
// clear to a remote host.
func validateEndpoint(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("endpoint url required: %w", ErrInvalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("endpoint url unreadable: %w", ErrInvalid)
	}
	if parsed.Scheme == "https" && parsed.Host != "" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("endpoint url must use https (loopback http for dev): %w", ErrInvalid)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func positiveOr(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

// LoopbackHost lets the command layer require a loopback dependency without
// duplicating host parsing.
func LoopbackHost(host string) bool { return isLoopbackHost(host) }
