package subscription

// Account logins the server model can use through the gateway. copilot, xai and nous sign in with
// the device-code flow (RFC 8628): `arkism oauth login <id>` prints a link and a code, the owner
// approves in a browser, and the tokens go to the OS credential store (keystore), or without one
// to a 0600 file under the agents state folder. Nothing is active until that login. Access tokens
// are refreshed shortly before they expire, one refresh at a time across processes; the stored
// tokens never reach a log, an error message or the screen. cursor signs in with its own CLI.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
)

// OAuthProvider is one device-code login and the OpenAI-compatible endpoint its token opens.
type OAuthProvider struct {
	ID, Name                 string
	Discovery                string // OpenID configuration that names the device and token endpoints, when the provider has one
	DeviceURL, TokenURL      string
	ClientID, Scope          string // public client: device-code clients have no secret
	ExchangeURL              string // copilot: swaps the account token for a short-lived API token
	RefreshHeader            string // nous: the refresh token travels in this header, not the form
	Base                     string // inference base; copilot's comes from the exchange
	Headers                  string // extra "Name: Value" lines the inference endpoint expects
	refreshSkew, maxLoginAge time.Duration
}

// OAuthProviders are the device-code logins, by provider id (the `<provider>/<model>` prefix).
var OAuthProviders = map[string]*OAuthProvider{
	"copilot": {ID: "copilot", Name: "GitHub Copilot", DeviceURL: "https://github.com/login/device/code",
		TokenURL: "https://github.com/login/oauth/access_token", ClientID: "Iv1.b507a08c87ecfe98", Scope: "read:user",
		ExchangeURL: "https://api.github.com/copilot_internal/v2/token",
		Headers:     "Copilot-Integration-Id: vscode-chat\nEditor-Version: arkism/1"},
	"xai": {ID: "xai", Name: "xAI", Discovery: "https://auth.x.ai/.well-known/openid-configuration",
		DeviceURL: "https://auth.x.ai/oauth2/device/code", ClientID: "b1a00492-073a-47ea-816f-4c329264a828",
		Scope: "openid profile email offline_access grok-cli:access api:access", Base: "https://api.x.ai/v1"},
	"nous": {ID: "nous", Name: "Nous Portal", DeviceURL: "https://portal.nousresearch.com/api/oauth/device/code",
		TokenURL: "https://portal.nousresearch.com/api/oauth/token", ClientID: "hermes-cli", Scope: "inference:mint_agent_key",
		RefreshHeader: "x-nous-refresh-token", Base: "https://inference-api.nousresearch.com/v1"},
}

// oauthCred is what one login stores; it is never printed or sent anywhere but its own provider.
type oauthCred struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token,omitempty"` // copilot: the account token the exchange takes
	Expires int64  `json:"expires_at,omitempty"`    // unix seconds; 0 = not known
	Base    string `json:"base,omitempty"`          // copilot: the API host the exchange named
	Invalid bool   `json:"invalid,omitempty"`       // the provider refused a refresh: sign in again
}

const oauthService = "arkism-oauth"

var (
	oauthClient = &http.Client{Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	oauthMu    sync.Map // id → *sync.Mutex: one refresh per login in this process; the lock file covers other processes
	oauthNow   = time.Now
	oauthSleep = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
	// ErrOAuthLogin means the provider has no usable login: sign in (again) with `arkism oauth login`.
	ErrOAuthLogin = errors.New("oauth_login_required")
)

// CursorBin is the cursor CLI, which keeps its own login (`cursor-agent login`).
func CursorBin() string { return host.Bin("ARKISM_CURSOR_BIN", "cursor-agent") }

func oauthPath(id, ext string) string { return filepath.Join(host.AgentsState, "oauth", id+ext) }

// loadCred reads a login from the credential store, else its file; nil when there is none.
func loadCred(id string) (*oauthCred, error) {
	var raw []byte
	if keystore.Available() {
		if v, err := keystore.Get("keyring:" + oauthService + "/" + id); err == nil {
			raw = []byte(v)
		}
	}
	if raw == nil {
		b, err := os.ReadFile(oauthPath(id, ".json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		raw = b
	}
	var c oauthCred
	if json.Unmarshal(raw, &c) != nil || c.Access == "" && c.Refresh == "" {
		return nil, errors.New("oauth_store_invalid")
	}
	return &c, nil
}

// saveCred stores a login in the credential store (then drops any file copy), else in a 0600 file.
func saveCred(id string, c *oauthCred) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if keystore.Available() {
		if _, err := keystore.PutIn(oauthService, id, string(raw)); err == nil {
			_ = os.Remove(oauthPath(id, ".json"))
			return nil
		}
	}
	path := oauthPath(id, ".json")
	f, err := os.CreateTemp(filepath.Dir(path), ".oauth-") // 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// OAuthLogout forgets a login everywhere it may be stored.
func OAuthLogout(id string) error {
	if OAuthProviders[id] == nil {
		return errors.New("unknown_provider")
	}
	unlock, err := oauthLock(id)
	if err != nil {
		return err
	}
	defer unlock()
	if keystore.Available() {
		if err := keystore.Delete("keyring:" + oauthService + "/" + id); err != nil {
			return err
		}
	}
	if err := os.Remove(oauthPath(id, ".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// oauthLock serialises refreshes and writes of one login, in this process and across processes.
// A slow provider holds only its own login.
func oauthLock(id string) (func(), error) {
	v, _ := oauthMu.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	if err := os.MkdirAll(filepath.Dir(oauthPath(id, "")), 0o700); err != nil {
		mu.Unlock()
		return nil, err
	}
	unlock, err := lockFile(oauthPath(id, ".lock"), 0o600, true)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	return func() { unlock(); mu.Unlock() }, nil
}

// OAuthStatus is the login's health: "connected", "expired" (sign in again) or "missing".
func OAuthStatus(id string) string {
	c, err := loadCred(id)
	switch {
	case err != nil || c != nil && c.Invalid:
		return "expired"
	case c == nil:
		return "missing"
	case c.Refresh == "" && c.Expires != 0 && oauthNow().Unix() >= c.Expires:
		return "expired"
	}
	return "connected"
}

// OAuthConnection is what the gateway needs to call the provider: its base, a fresh bearer token
// and its extra headers. ErrOAuthLogin when there is no usable login.
func OAuthConnection(id string) (base, token, headers string, err error) {
	p := OAuthProviders[id]
	if p == nil {
		return "", "", "", errors.New("unknown_provider")
	}
	c, err := loadCred(id)
	if err == nil && c != nil && !c.Invalid && !p.stale(c) {
		return p.base(c), c.Access, p.Headers, nil
	}
	if err != nil || c == nil || c.Invalid {
		return "", "", "", ErrOAuthLogin
	}
	c, err = p.refresh(context.Background())
	if err != nil {
		return "", "", "", err
	}
	return p.base(c), c.Access, p.Headers, nil
}

// OAuthExpire marks the stored access token as used up after the provider refused it (a 401), so
// the next call refreshes instead of sending it again.
func OAuthExpire(id, token string) {
	unlock, err := oauthLock(id)
	if err != nil {
		return
	}
	defer unlock()
	if c, err := loadCred(id); err == nil && c != nil && c.Access == token && c.Refresh != "" {
		c.Expires = 1
		_ = saveCred(id, c)
	}
}

func (p *OAuthProvider) base(c *oauthCred) string {
	if c.Base != "" {
		return c.Base
	}
	return p.Base
}

func (p *OAuthProvider) stale(c *oauthCred) bool {
	skew := p.refreshSkew
	if skew == 0 {
		skew = 2 * time.Minute
	}
	return c.Expires != 0 && oauthNow().Add(skew).Unix() >= c.Expires
}

// refresh renews the access token under the login's lock. It rereads the store first: another
// process may have refreshed already, and a single-use refresh token must never be sent twice.
func (p *OAuthProvider) refresh(ctx context.Context) (*oauthCred, error) {
	unlock, err := oauthLock(p.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := loadCred(p.ID)
	if err != nil || c == nil || c.Invalid || c.Refresh == "" {
		return nil, ErrOAuthLogin
	}
	if !p.stale(c) {
		return c, nil
	}
	next, err := p.renew(ctx, c)
	if errors.Is(err, ErrOAuthLogin) {
		c.Invalid = true
		_ = saveCred(p.ID, c)
	}
	if err != nil {
		return nil, err
	}
	return next, saveCred(p.ID, next)
}

// renew asks the provider for a new access token: copilot exchanges its account token, the others
// use the refresh-token grant. A 400/401/403 answer means the login is gone (ErrOAuthLogin).
func (p *OAuthProvider) renew(ctx context.Context, c *oauthCred) (*oauthCred, error) {
	if p.ExchangeURL != "" {
		return p.exchange(ctx, c.Refresh)
	}
	tokenURL, err := p.tokenURL(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {p.ClientID}}
	header := map[string]string{}
	if p.RefreshHeader != "" {
		header[p.RefreshHeader] = c.Refresh
	} else {
		form.Set("refresh_token", c.Refresh)
	}
	status, body, err := oauthPost(ctx, tokenURL, form, header)
	if err != nil {
		return nil, err
	}
	if status == 400 || status == 401 || status == 403 {
		return nil, ErrOAuthLogin
	}
	next, err := tokenReply(status, body)
	if err != nil {
		return nil, err
	}
	if next.Refresh == "" {
		next.Refresh = c.Refresh // a provider that does not rotate keeps the old one valid
	}
	return next, nil
}

// exchange swaps a copilot account token for an API token and the API host it is good for.
func (p *OAuthProvider) exchange(ctx context.Context, account string) (*oauthCred, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ExchangeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+account)
	req.Header.Set("Accept", "application/json")
	status, body, err := oauthDo(req)
	if err != nil {
		return nil, err
	}
	if status == 401 || status == 403 || status == 404 { // signed out, or no subscription on the account
		return nil, ErrOAuthLogin
	}
	var reply struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if status != 200 || json.Unmarshal(body, &reply) != nil || reply.Token == "" {
		return nil, fmt.Errorf("oauth_exchange_failed: http %d", status)
	}
	base := strings.TrimRight(reply.Endpoints.API, "/")
	if base == "" {
		base = "https://api.githubcopilot.com"
	}
	if oauthURL(base) != nil {
		return nil, errors.New("oauth_exchange_failed: endpoint")
	}
	return &oauthCred{Access: reply.Token, Refresh: account, Expires: reply.ExpiresAt, Base: base}, nil
}

// tokenURL is the token endpoint: the configured one, else the provider's discovery document.
func (p *OAuthProvider) tokenURL(ctx context.Context) (string, error) {
	if p.TokenURL != "" || p.Discovery == "" {
		return p.TokenURL, oauthURL(p.TokenURL)
	}
	_, token, err := p.discover(ctx)
	return token, err
}

func (p *OAuthProvider) discover(ctx context.Context) (device, token string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Discovery, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	status, body, err := oauthDo(req)
	if err != nil {
		return "", "", err
	}
	var doc struct {
		Device string `json:"device_authorization_endpoint"`
		Token  string `json:"token_endpoint"`
	}
	if status != 200 || json.Unmarshal(body, &doc) != nil || oauthURL(doc.Token) != nil {
		return "", "", fmt.Errorf("oauth_discovery_failed: http %d", status)
	}
	if doc.Device == "" {
		doc.Device = p.DeviceURL
	}
	return doc.Device, doc.Token, oauthURL(doc.Device)
}

// oauthURL accepts https, or http to this machine (test servers and local proxies).
func oauthURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("oauth_endpoint_invalid")
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("oauth_endpoint_invalid")
}

func oauthPost(ctx context.Context, target string, form url.Values, header map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return oauthDo(req)
}

// oauthDo sends one request; transport errors lose their URL detail, which can carry a query.
func oauthDo(req *http.Request) (int, []byte, error) {
	resp, err := oauthClient.Do(req)
	if err != nil {
		return 0, nil, errors.New("oauth_unreachable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, errors.New("oauth_unreachable")
	}
	return resp.StatusCode, body, nil
}

// tokenReply reads an access-token answer; the provider's own error code is the only detail kept.
func tokenReply(status int, body []byte) (*oauthCred, error) {
	var reply struct {
		Access    string  `json:"access_token"`
		Refresh   string  `json:"refresh_token"`
		ExpiresIn float64 `json:"expires_in"`
		Error     string  `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return nil, fmt.Errorf("oauth_token_failed: http %d", status)
	}
	if reply.Error != "" || reply.Access == "" || status != 200 {
		code := reply.Error
		if !idRe.MatchString(code) {
			code = "http_" + fmt.Sprint(status)
		}
		return nil, errors.New("oauth_token_failed: " + code)
	}
	c := &oauthCred{Access: reply.Access, Refresh: reply.Refresh}
	if reply.ExpiresIn > 0 {
		c.Expires = oauthNow().Unix() + int64(reply.ExpiresIn)
	}
	return c, nil
}

// OAuthLogin runs the device-code sign-in: it prints where to approve, waits for the approval and
// stores the tokens. copilot also checks at once that the account has a subscription.
func OAuthLogin(ctx context.Context, id string, out io.Writer) error {
	p := OAuthProviders[id]
	if p == nil {
		return errors.New("unknown_provider")
	}
	deviceURL, tokenURL := p.DeviceURL, p.TokenURL
	if p.Discovery != "" {
		var err error
		if deviceURL, tokenURL, err = p.discover(ctx); err != nil {
			return err
		}
	}
	if oauthURL(deviceURL) != nil || oauthURL(tokenURL) != nil {
		return errors.New("oauth_endpoint_invalid")
	}
	status, body, err := oauthPost(ctx, deviceURL, url.Values{"client_id": {p.ClientID}, "scope": {p.Scope}}, nil)
	if err != nil {
		return err
	}
	var device struct {
		DeviceCode string  `json:"device_code"`
		UserCode   string  `json:"user_code"`
		URI        string  `json:"verification_uri"`
		Complete   string  `json:"verification_uri_complete"`
		ExpiresIn  float64 `json:"expires_in"`
		Interval   float64 `json:"interval"`
	}
	if status != 200 || json.Unmarshal(body, &device) != nil || device.DeviceCode == "" || device.UserCode == "" || oauthURL(device.URI) != nil {
		return fmt.Errorf("oauth_device_failed: http %d", status)
	}
	link := device.URI
	if device.Complete != "" && oauthURL(device.Complete) == nil {
		link = device.Complete
	}
	fmt.Fprintf(out, "%s: open %s and enter the code %s\n", p.Name, link, device.UserCode)
	c, err := p.poll(ctx, tokenURL, device.DeviceCode, device.Interval, device.ExpiresIn)
	if err != nil {
		return err
	}
	if p.ExchangeURL != "" {
		if c, err = p.exchange(ctx, c.Access); errors.Is(err, ErrOAuthLogin) {
			return errors.New("oauth_no_subscription")
		} else if err != nil {
			return err
		}
	}
	unlock, err := oauthLock(id)
	if err != nil {
		return err
	}
	defer unlock()
	if err := saveCred(id, c); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: signed in\n", p.Name)
	return nil
}

// poll asks for the token until the owner approves, the code expires or ctx ends (RFC 8628 §3.5).
func (p *OAuthProvider) poll(ctx context.Context, tokenURL, deviceCode string, interval, expiresIn float64) (*oauthCred, error) {
	wait := time.Duration(max(interval, 5) * float64(time.Second))
	life := p.maxLoginAge
	if life == 0 {
		life = time.Duration(max(min(expiresIn, 1800), 60) * float64(time.Second))
	}
	deadline := oauthNow().Add(life)
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {deviceCode}, "client_id": {p.ClientID}}
	for oauthNow().Before(deadline) {
		if err := oauthSleep(ctx, wait); err != nil {
			return nil, err
		}
		status, body, err := oauthPost(ctx, tokenURL, form, nil)
		if err != nil {
			continue // a dropped connection is retried until the code expires
		}
		c, err := tokenReply(status, body)
		switch {
		case err == nil:
			return c, nil
		case strings.HasSuffix(err.Error(), ": authorization_pending"):
		case strings.HasSuffix(err.Error(), ": slow_down"):
			wait += 5 * time.Second
		default:
			return nil, err // access_denied, expired_token or a broken answer
		}
	}
	return nil, errors.New("oauth_token_failed: expired_token")
}

// OAuthModels lists the models the login can call (the provider's /models), nil when it cannot.
func OAuthModels(id string) []string {
	base, token, headers, err := OAuthConnection(id)
	if err != nil {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	for _, h := range host.HeaderPairs(map[string]any{"headers": headers}) {
		req.Header.Set(h[0], h[1])
	}
	status, body, err := oauthDo(req)
	if err != nil || status != 200 {
		if status == 401 {
			OAuthExpire(id, token)
		}
		return nil
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	out := []string{}
	for _, m := range list.Data {
		if m.ID != "" && len(out) < 300 {
			out = append(out, m.ID)
		}
	}
	return out
}

func init() { host.Register("oauth", runOAuth) }

// runOAuth is `arkism oauth login|logout|status [provider]`.
func runOAuth(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "status" {
		for _, id := range []string{"copilot", "xai", "nous"} {
			fmt.Fprintf(stdout, "%s\t%s\n", id, OAuthStatus(id))
		}
		return 0
	}
	if len(args) != 2 || OAuthProviders[args[1]] == nil || args[0] != "login" && args[0] != "logout" {
		fmt.Fprintln(stderr, "usage: arkism oauth login|logout copilot|xai|nous, or arkism oauth status")
		return 2
	}
	var err error
	if args[0] == "logout" {
		err = OAuthLogout(args[1])
	} else {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		err = OAuthLogin(ctx, args[1], stdout)
	}
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return 0
}
