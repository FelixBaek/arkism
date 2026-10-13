package memory

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeIDToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

type fakeGoogle struct {
	claims map[string]any
	form   map[string][]string
}

// googleLoginFixture answers the token endpoint with an ID token carrying f.claims;
// the code "used" is refused the way Google refuses a redeemed code.
func googleLoginFixture(t *testing.T) (*GoogleLogin, GoogleLoginInput, *fakeGoogle) {
	f := &fakeGoogle{}
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.form = r.PostForm
		if r.PostForm.Get("code") == "used" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": fakeIDToken(f.claims)})
	}))
	t.Cleanup(google.Close)
	g := &GoogleLogin{ClientID: "client-1", ClientSecret: "secret-1", Allowed: map[string]bool{"me@example.com": true}, DesktopToken: "desktop-token", TokenURL: google.URL}
	in := GoogleLoginInput{Code: "code-1", CodeVerifier: strings.Repeat("v", 43), RedirectURI: "http://127.0.0.1:53111/callback"}
	return g, in, f
}

func goodClaims(change map[string]any) map[string]any {
	c := map[string]any{"iss": "https://accounts.google.com", "aud": "client-1", "exp": time.Now().Add(time.Hour).Unix(), "email": "Me@Example.com", "email_verified": true}
	for k, v := range change {
		c[k] = v
	}
	return c
}

func TestGoogleLoginHandsTheDesktopTokenToAnAllowedVerifiedAccount(t *testing.T) {
	g, in, f := googleLoginFixture(t)
	f.claims = goodClaims(nil)
	out, err := g.Login(testCtx, in)
	if err != nil || out.Token != "desktop-token" || out.Email != "me@example.com" {
		t.Fatal("allowed account refused", out, err)
	}
	if f.form["client_secret"][0] != "secret-1" || f.form["code_verifier"][0] != in.CodeVerifier || f.form["redirect_uri"][0] != in.RedirectURI {
		t.Fatal("code redeemed without PKCE or secret", f.form)
	}
}

func TestGoogleLoginRefusesEveryOtherToken(t *testing.T) {
	g, in, f := googleLoginFixture(t)
	for name, change := range map[string]map[string]any{
		"other app":     {"aud": "client-2"},
		"other issuer":  {"iss": "https://evil.example"},
		"expired":       {"exp": time.Now().Add(-time.Minute).Unix()},
		"unverified":    {"email_verified": false},
		"other account": {"email": "someone@example.com"},
	} {
		f.claims = goodClaims(change)
		if out, err := g.Login(testCtx, in); !errors.Is(err, ErrDenied) || out.Token != "" {
			t.Fatal(name, "was let in", out, err)
		}
	}
	in.Code = "used"
	if _, err := g.Login(testCtx, in); !errors.Is(err, ErrDenied) {
		t.Fatal("rejected code is not a denial", err)
	}
}

func TestGoogleLoginTakesOnlyLoopbackRedirects(t *testing.T) {
	g, in, _ := googleLoginFixture(t)
	for _, redirect := range []string{"https://127.0.0.1:1/cb", "http://example.com:80/cb", "http://127.0.0.1/cb", "http://127.0.0.1:1/cb?x=1"} {
		in.RedirectURI = redirect
		if _, err := g.Login(testCtx, in); !errors.Is(err, ErrInvalid) {
			t.Fatal("non-loopback redirect accepted", redirect, err)
		}
	}
}

func TestGoogleLoginRoutesWithoutConfigurationAreNotFound(t *testing.T) {
	h := Handler(&Service{})
	for _, req := range []*http.Request{httptest.NewRequest("GET", "/v1/auth/google", nil), httptest.NewRequest("POST", "/v1/auth/google", strings.NewReader(`{}`))} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatal("google login answered while off", req.Method, w.Code)
		}
	}
	w := httptest.NewRecorder()
	Handler(&Service{GoogleLogin: &GoogleLogin{ClientID: "client-1"}}).ServeHTTP(w, httptest.NewRequest("GET", "/v1/auth/google", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "client-1") || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("config route", w.Code, w.Body.String())
	}
}
