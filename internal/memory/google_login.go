package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

// GoogleLogin lets the desktop app sign in with a Google account instead of a
// pasted token. The app runs the browser half (PKCE, loopback redirect, an
// installed-app client); this server holds the client secret,
// redeems the code with Google itself and, for an allowed verified e-mail,
// hands back the desktop principal's token. That principal is separate from
// the owner token the agent hooks use, so it can be revoked on its own.
type GoogleLogin struct {
	ClientID     string
	ClientSecret string
	Allowed      map[string]bool // lower-cased e-mail addresses
	DesktopToken string
	HTTP         *http.Client
	TokenURL     string // Google's token endpoint; tests point it elsewhere
}

type GoogleLoginInput struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

type GoogleLoginResult struct {
	Token string `json:"token"`
	Email string `json:"email"`
}

const googleTokenURL = "https://oauth2.googleapis.com/token"

// loopbackRedirect accepts only http://127.0.0.1:port/... or http://localhost:port/...,
// the redirect an installed-app client may use; anything else could leak the code.
func loopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"
}

func (g *GoogleLogin) Login(ctx context.Context, in GoogleLoginInput) (GoogleLoginResult, error) {
	if len(in.Code) == 0 || len(in.Code) > 2048 || len(in.CodeVerifier) < 43 || len(in.CodeVerifier) > 128 || !loopbackRedirect(in.RedirectURI) {
		return GoogleLoginResult{}, ErrInvalid
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {in.Code}, "code_verifier": {in.CodeVerifier},
		"redirect_uri": {in.RedirectURI}, "client_id": {g.ClientID}, "client_secret": {g.ClientSecret},
	}
	endpoint := g.TokenURL
	if endpoint == "" {
		endpoint = googleTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return GoogleLoginResult{}, ErrUnavailable
	}
	req.Header.Set(headerContentType, "application/x-www-form-urlencoded")
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return GoogleLoginResult{}, ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return GoogleLoginResult{}, ErrUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		// A wrong, reused or expired code is the caller's problem, not an outage.
		if resp.StatusCode >= 500 {
			return GoogleLoginResult{}, ErrUnavailable
		}
		return GoogleLoginResult{}, ErrDenied
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.IDToken == "" {
		return GoogleLoginResult{}, ErrDenied
	}
	email, err := g.verifiedEmail(tok.IDToken, time.Now())
	if err != nil {
		return GoogleLoginResult{}, err
	}
	return GoogleLoginResult{Token: g.DesktopToken, Email: email}, nil
}

// verifiedEmail reads the ID token's claims. Its signature is not checked: the
// token came straight from Google's token endpoint over TLS in exchange for a
// code only this client could redeem (OpenID Connect Core 3.1.3.7, item 6).
func (g *GoogleLogin) verifiedEmail(idToken string, now time.Time) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ErrDenied
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrDenied
	}
	var c struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Exp           int64  `json:"exp"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return "", ErrDenied
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	issuer := c.Iss == "https://accounts.google.com" || c.Iss == "accounts.google.com"
	if !issuer || c.Aud != g.ClientID || now.Unix() >= c.Exp || !c.EmailVerified || !g.Allowed[email] {
		return "", ErrDenied
	}
	return email, nil
}

var errGoogleLoginOff = errors.New("google login not configured")

func googleLoginRoutes(mux routeRegistrar, g *GoogleLogin) {
	attempts := httpguard.NewLimiter(10, 6*time.Second)
	mux.HandleFunc("GET /v1/auth/google", func(w http.ResponseWriter, r *http.Request) {
		if g == nil {
			respond(w, nil, errors.Join(ErrNotFound, errGoogleLoginOff))
			return
		}
		respond(w, map[string]string{"client_id": g.ClientID}, nil)
	})
	mux.HandleFunc("POST /v1/auth/google", func(w http.ResponseWriter, r *http.Request) {
		if g == nil {
			respond(w, nil, errors.Join(ErrNotFound, errGoogleLoginOff))
			return
		}
		if !attempts.Allow() {
			w.Header().Set("Retry-After", "6")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"code": "rate_limited"})
			return
		}
		var in GoogleLoginInput
		if !decode(w, r, &in) {
			return
		}
		out, e := g.Login(r.Context(), in)
		respond(w, out, e)
	})
}
