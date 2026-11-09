package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
)

// fakeOAuth is one provider's device, token, exchange and models endpoints.
type fakeOAuth struct {
	srv                       *httptest.Server
	pending, refreshes        atomic.Int32
	refreshStatus             int
	lastRefreshForm, lastNous string
	modelsAuth, modelsHeader  string
	mu                        sync.Mutex
}

func newFakeOAuth(t *testing.T, githubStyle bool) *fakeOAuth {
	f := &fakeOAuth{refreshStatus: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"device_authorization_endpoint": f.srv.URL + "/device", "token_endpoint": f.srv.URL + "/token"})
	})
	mux.HandleFunc("POST /device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dev-1", "user_code": "ABCD-1234", "verification_uri": f.srv.URL + "/activate", "expires_in": 600, "interval": 1})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshes.Add(1)
			f.mu.Lock()
			f.lastRefreshForm, f.lastNous = r.Form.Get("refresh_token"), r.Header.Get("x-nous-refresh-token")
			f.mu.Unlock()
			time.Sleep(20 * time.Millisecond) // widen the window a second refresh would race through
			if f.refreshStatus != 200 {
				w.WriteHeader(f.refreshStatus)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"rt-SECRET-1 was reused"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600})
			return
		}
		if f.pending.Add(1) < 3 {
			if !githubStyle {
				w.WriteHeader(400)
			}
			w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "refresh_token": "rt-SECRET-1", "expires_in": 3600})
	})
	mux.HandleFunc("GET /exchange", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token at-1" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "cop-1", "expires_at": time.Now().Add(30 * time.Minute).Unix(), "endpoints": map[string]string{"api": f.srv.URL + "/api"}})
	})
	models := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.modelsAuth, f.modelsHeader = r.Header.Get("Authorization"), r.Header.Get("Copilot-Integration-Id")
		f.mu.Unlock()
		w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`))
	}
	mux.HandleFunc("GET /api/models", models)
	mux.HandleFunc("GET /v1/models", models)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// oauthTestEnv points the store at a temp folder, with the file store unless keyring is true, and
// makes polling instant.
func oauthTestEnv(t *testing.T, keyring bool) {
	t.Helper()
	oldState, oldSleep, oldNow, oldProviders := host.AgentsState, oauthSleep, oauthNow, OAuthProviders
	host.AgentsState = t.TempDir()
	oauthSleep = func(context.Context, time.Duration) error { return nil }
	if keyring {
		keystore.UseMock(nil)
	} else {
		keystore.UseMock(errors.New("no store"))
	}
	t.Cleanup(func() {
		host.AgentsState, oauthSleep, oauthNow, OAuthProviders = oldState, oldSleep, oldNow, oldProviders
		keystore.UseMock(errors.New("no store"))
	})
}

func fakeProviders(f *fakeOAuth) {
	OAuthProviders = map[string]*OAuthProvider{
		"xai": {ID: "xai", Name: "xAI", Discovery: f.srv.URL + "/.well-known/openid-configuration", ClientID: "c", Scope: "s", Base: f.srv.URL + "/v1"},
		"nous": {ID: "nous", Name: "Nous", DeviceURL: f.srv.URL + "/device", TokenURL: f.srv.URL + "/token", ClientID: "c",
			RefreshHeader: "x-nous-refresh-token", Base: f.srv.URL + "/v1"},
		"copilot": {ID: "copilot", Name: "Copilot", DeviceURL: f.srv.URL + "/device", TokenURL: f.srv.URL + "/token", ClientID: "c",
			ExchangeURL: f.srv.URL + "/exchange", Headers: "Copilot-Integration-Id: test"},
	}
}

func TestOAuthDeviceLoginRefreshAndLogout(t *testing.T) {
	oauthTestEnv(t, false)
	f := newFakeOAuth(t, false)
	fakeProviders(f)
	if OAuthStatus("xai") != "missing" {
		t.Fatal("no login yet must read missing")
	}
	var out bytes.Buffer
	if err := OAuthLogin(context.Background(), "xai", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ABCD-1234") || strings.Contains(out.String(), "at-1") || strings.Contains(out.String(), "rt-SECRET-1") {
		t.Fatalf("login output must show the code and never a token: %q", out.String())
	}
	path, _ := oauthPath("xai", ".json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file store must be 0600: %v %v", st, err)
	}
	base, token, _, err := OAuthConnection("xai")
	if err != nil || token != "at-1" || base != f.srv.URL+"/v1" || OAuthStatus("xai") != "connected" {
		t.Fatalf("fresh login: %q %q %v", base, token, err)
	}
	// Near expiry: many concurrent callers, one refresh, rotated token stored.
	oauthNow = func() time.Time { return time.Now().Add(59 * time.Minute) }
	var wg sync.WaitGroup
	tokens := make([]string, 8)
	for i := range tokens {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, tokens[i], _, _ = OAuthConnection("xai") }(i)
	}
	wg.Wait()
	if n := f.refreshes.Load(); n != 1 {
		t.Fatalf("one refresh for concurrent callers, got %d", n)
	}
	for _, tok := range tokens {
		if tok != "at-2" {
			t.Fatalf("every caller gets the refreshed token: %v", tokens)
		}
	}
	if f.lastRefreshForm != "rt-SECRET-1" {
		t.Fatal("standard refresh sends the refresh token in the form")
	}
	if c, _ := loadCred("xai"); c.Refresh != "rt-2" {
		t.Fatal("rotated refresh token must be stored")
	}
	if got := OAuthModels("xai"); len(got) != 2 || got[0] != "model-a" || f.modelsAuth != "Bearer at-2" {
		t.Fatalf("models: %v auth %q", got, f.modelsAuth)
	}
	if err := OAuthLogout("xai"); err != nil || OAuthStatus("xai") != "missing" {
		t.Fatalf("logout: %v %s", err, OAuthStatus("xai"))
	}
	if _, _, _, err := OAuthConnection("xai"); !errors.Is(err, ErrOAuthLogin) {
		t.Fatal("signed out must need a login")
	}
}

func TestOAuthRefreshRefusedNeedsLoginAndHidesSecrets(t *testing.T) {
	oauthTestEnv(t, true) // credential store path
	f := newFakeOAuth(t, false)
	fakeProviders(f)
	if err := OAuthLogin(context.Background(), "nous", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	nousPath, _ := oauthPath("nous", ".json")
	if _, err := os.Stat(nousPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("with a credential store no token file is written")
	}
	f.refreshStatus = 400
	oauthNow = func() time.Time { return time.Now().Add(2 * time.Hour) }
	_, _, _, err := OAuthConnection("nous")
	if !errors.Is(err, ErrOAuthLogin) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("refused refresh: %v", err)
	}
	if f.lastNous != "rt-SECRET-1" || f.lastRefreshForm != "" {
		t.Fatal("nous sends its refresh token in the header only")
	}
	if OAuthStatus("nous") != "expired" {
		t.Fatal("a refused refresh reads as expired")
	}
	if _, _, _, err := OAuthConnection("nous"); !errors.Is(err, ErrOAuthLogin) || f.refreshes.Load() != 1 {
		t.Fatal("an invalid login is not refreshed again")
	}
}

func TestOAuthCopilotExchangeAndExpire(t *testing.T) {
	oauthTestEnv(t, false)
	f := newFakeOAuth(t, true) // pending answers come as 200 with an error field
	fakeProviders(f)
	if err := OAuthLogin(context.Background(), "copilot", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	base, token, headers, err := OAuthConnection("copilot")
	if err != nil || token != "cop-1" || base != f.srv.URL+"/api" || headers != "Copilot-Integration-Id: test" {
		t.Fatalf("exchange: %q %q %q %v", base, token, headers, err)
	}
	if got := OAuthModels("copilot"); len(got) != 2 || f.modelsHeader != "test" {
		t.Fatalf("copilot models: %v %q", got, f.modelsHeader)
	}
	OAuthExpire("copilot", "cop-1") // a 401 on the API token: the next call exchanges again
	if c, _ := loadCred("copilot"); !OAuthProviders["copilot"].stale(c) {
		t.Fatal("expired token must be stale")
	}
	if _, token, _, err := OAuthConnection("copilot"); err != nil || token != "cop-1" {
		t.Fatalf("re-exchange: %q %v", token, err)
	}
}

func TestOAuthLoginRefusesInsecureEndpoints(t *testing.T) {
	oauthTestEnv(t, false)
	OAuthProviders = map[string]*OAuthProvider{"xai": {ID: "xai", DeviceURL: "http://example.com/device", TokenURL: "https://example.com/token"}}
	if err := OAuthLogin(context.Background(), "xai", &bytes.Buffer{}); err == nil || err.Error() != "oauth_endpoint_invalid" {
		t.Fatalf("plain http to another host must be refused: %v", err)
	}
	if OAuthLogin(context.Background(), "nope", &bytes.Buffer{}) == nil || OAuthLogout("nope") == nil {
		t.Fatal("unknown provider")
	}
}
