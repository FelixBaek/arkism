package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// Host Google transport, no agent runtime.
//
// Refreshing the host Gemini CLI login needs that CLI's OAuth client id/secret. They are not in
// this repository: set ARKISM_GOOGLE_CLIENT_ID and ARKISM_GOOGLE_CLIENT_SECRET, or write
// ~/.config/arkism/google-oauth.json as {"client_id": "...", "client_secret": "..."}.
// Credentials remain in the host's existing login folder; this file never writes it.

// Endpoints and limits; variables so tests can point them at local servers and shrink them.
var (
	googleAPI             = "https://generativelanguage.googleapis.com/v1beta"
	googleAssist          = "https://cloudcode-pa.googleapis.com/v1internal"
	googleOAuth           = "https://oauth2.googleapis.com/token"
	googleOAuthClientFile = filepath.Join(host.Home, ".config/arkism/google-oauth.json")
	googleMaxInput        = 8 << 20
	googleMaxOutput       = 16 << 20
	googleDeadline        = 600.0 // seconds for one whole request, login included
)

// Clocks: googleMono is time.monotonic (seconds), googleWall is time.time. Tests replace them.
var (
	googleStart = time.Now()
	googleMono  = func() float64 { return time.Since(googleStart).Seconds() }
	googleWall  = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
)

var googleModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

// googleClient never uses an environment proxy and never follows redirects.
var googleClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	return &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}()

type googleSigKey struct {
	owner string
	hash  [32]byte
}

type googleSig struct {
	model   string
	expires float64
}

var (
	googleLock     sync.Mutex
	googleAuthLock = make(chan struct{}, 1)
	googleSlots    = make(chan struct{}, 8)
	googleCache    *googleAuth
	// googleSignatures keeps dict semantics: googleSigOrder is insertion order, re-setting keeps place.
	googleSignatures = map[googleSigKey]googleSig{}
	googleSigOrder   []googleSigKey
)

// GoogleError is a fixed public code and status; Started means generation may have begun.
type GoogleError struct {
	Code    string
	Status  int
	Started bool
}

func (e *GoogleError) Error() string { return e.Code }

func googleErr(code string, status int, started bool) *GoogleError {
	return &GoogleError{Code: code, Status: status, Started: started}
}

func googleEncoded(value any) (string, error) { return JSON(value, "invalid_request") }

var googlePrepareFields = []string{"contents", "systemInstruction", "tools", "toolConfig",
	"generationConfig", "safetySettings", "cachedContent", "labels"}

// googlePrepare validates a native Gemini body for model and returns an independent copy (prepare).
func googlePrepare(body any, model string) (host.Obj, error) {
	if !googleModel.MatchString(model) {
		return nil, googleErr("invalid_request", 400, false)
	}
	invalid := googleErr("unsupported_or_invalid_request", 400, false)
	wired, err := Wire(body, googlePrepareFields, "invalid_request")
	if err != nil {
		return nil, invalid
	}
	contents, ok := getd(wired, "contents", nil).([]any)
	if !ok || len(contents) == 0 {
		return nil, invalid
	}
	for _, item := range contents {
		content, ok := item.(host.Obj)
		if !ok {
			return nil, invalid
		}
		if role := getd(content, "role", "user"); role != "user" && role != "model" {
			return nil, invalid
		}
		parts, ok := get(content, "parts").([]any)
		if !ok || len(parts) == 0 {
			return nil, invalid
		}
		for _, part := range parts {
			if o, ok := part.(host.Obj); !ok || len(o) == 0 {
				return nil, invalid
			}
		}
	}
	text, err := googleEncoded(wired)
	if err != nil || nesting(wired, 0) > maxNesting {
		return nil, invalid
	}
	if len(text) > googleMaxInput {
		return nil, googleErr("input_too_large", 413, false)
	}
	return clone(wired).(host.Obj), nil
}

// googleSignatures hashes every thought signature in contents (_signatures).
func googleSignatureHashes(contents []any) ([][32]byte, error) {
	var out [][32]byte
	for _, content := range contents {
		parts, _ := getd(asObj(content), "parts", []any{}).([]any)
		for _, item := range parts {
			part := asObj(item)
			if has(part, "thoughtSignature") && has(part, "thought_signature") {
				return nil, googleErr("invalid_signature", 400, false)
			}
			signature, ok := part.Get("thoughtSignature")
			if !ok {
				signature = get(part, "thought_signature")
			}
			if signature == nil {
				continue
			}
			s, ok := signature.(string)
			if !ok || s == "" {
				return nil, googleErr("invalid_signature", 400, false)
			}
			out = append(out, sha256.Sum256([]byte(s)))
		}
	}
	return out, nil
}

func asObj(v any) host.Obj {
	o, _ := v.(host.Obj)
	return o
}

// googleSetSignature is dict assignment: a new key goes last, an existing one keeps its place.
func googleSetSignature(key googleSigKey, value googleSig) {
	if _, ok := googleSignatures[key]; !ok {
		googleSigOrder = append(googleSigOrder, key)
	}
	googleSignatures[key] = value
}

// googleRemember records the thought signatures of a response page for owner (remember).
func googleRemember(page host.Obj, owner, model string) error {
	candidates, _ := getd(page, "candidates", []any{}).([]any)
	contents := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		contents = append(contents, getd(asObj(candidate), "content", host.Obj{}))
	}
	hashes, err := googleSignatureHashes(contents)
	if err != nil {
		return err
	}
	googleLock.Lock()
	defer googleLock.Unlock()
	now := googleMono()
	kept := googleSigOrder[:0]
	for _, key := range googleSigOrder {
		if googleSignatures[key].expires <= now {
			delete(googleSignatures, key)
		} else {
			kept = append(kept, key)
		}
	}
	googleSigOrder = kept
	for _, hash := range hashes {
		googleSetSignature(googleSigKey{owner, hash}, googleSig{model, now + 900})
	}
	for len(googleSigOrder) > 4096 {
		delete(googleSignatures, googleSigOrder[0])
		googleSigOrder = googleSigOrder[1:]
	}
	return nil
}

// googleContinuationModel is the model that signed body's history for owner, "" when unsigned
// (continuation_model).
func googleContinuationModel(body host.Obj, owner string) (string, error) {
	contents, _ := get(body, "contents").([]any)
	hashes, err := googleSignatureHashes(contents)
	if err != nil || len(hashes) == 0 {
		return "", err
	}
	googleLock.Lock()
	defer googleLock.Unlock()
	models := map[string]bool{}
	model := ""
	for _, hash := range hashes {
		value, ok := googleSignatures[googleSigKey{owner, hash}]
		if !ok || value.expires <= googleMono() {
			return "", googleErr("conversation_mismatch", 409, false)
		}
		models[value.model], model = true, value.model
	}
	if len(models) != 1 {
		return "", googleErr("conversation_mismatch", 409, false)
	}
	for _, hash := range hashes {
		googleSetSignature(googleSigKey{owner, hash}, googleSig{model, googleMono() + 900})
	}
	return model, nil
}

// googleRemaining is the time left before began's deadline.
func googleRemaining(began float64) (time.Duration, error) {
	left := googleDeadline - (googleMono() - began)
	if left <= 0 {
		return 0, googleErr("upstream_timeout", 504, false)
	}
	return time.Duration(left * float64(time.Second)), nil
}

// googleTimeout reports a socket timeout (Python's TimeoutError).
func googleTimeout(err error) bool {
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout()
}

// googleOpen sends one request with no retry; headerWait bounds only the wait for the response
// head, while ctx bounds the whole exchange. A non-2xx answer is closed and returned as status.
func googleOpen(ctx context.Context, rawURL, contentType, payload string, headers map[string]string,
	headerWait time.Duration) (*http.Response, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	req = req.WithContext(ctx)
	timer := time.AfterFunc(headerWait, func() { cancel(context.DeadlineExceeded) })
	resp, err := googleClient.Do(req)
	stopped := timer.Stop()
	if err != nil {
		if !stopped && context.Cause(ctx) == context.DeadlineExceeded {
			err = context.DeadlineExceeded
		}
		cancel(nil)
		return nil, 0, err
	}
	if !stopped { // the head arrived just as the timer fired; the body is unusable
		resp.Body.Close()
		cancel(nil)
		return nil, 0, context.DeadlineExceeded
	}
	resp.Body = googleBody{resp.Body, cancel} // cancel lives as long as the body
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, resp.StatusCode, nil
	}
	return resp, 0, nil
}

type googleBody struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
}

func (b googleBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel(nil)
	return err
}

func googleContentType(resp *http.Response) string {
	kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return "text/plain"
	}
	return kind
}

// googleJSONPost is _json_post: one bounded login request whose answer must be a JSON object.
func googleJSONPost(rawURL string, body host.Obj, headers map[string]string, form bool, began float64) (host.Obj, error) {
	unavailable := googleErr("login_unavailable", 503, false)
	timeout := googleErr("upstream_timeout", 504, false)
	var payload, contentType string
	if form {
		pairs := make([]string, len(body))
		for i, kv := range body {
			pairs[i] = url.QueryEscape(kv.Key) + "=" + url.QueryEscape(fmt.Sprint(kv.Value))
		}
		payload, contentType = strings.Join(pairs, "&"), "application/x-www-form-urlencoded"
	} else {
		text, err := googleEncoded(body)
		if err != nil {
			return nil, unavailable
		}
		payload, contentType = text, "application/json"
	}
	left, err := googleRemaining(began)
	if err != nil {
		return nil, timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), left)
	defer cancel()
	resp, status, err := googleOpen(ctx, rawURL, contentType, payload, headers, min(20*time.Second, left))
	if err != nil {
		if googleTimeout(err) {
			return nil, timeout
		}
		return nil, unavailable
	}
	if status != 0 {
		return nil, unavailable
	}
	defer resp.Body.Close()
	var raw bytes.Buffer
	for chunk, err := range googleChunks(resp, nil, began, 1<<20) {
		if err != nil {
			var gerr *GoogleError
			if googleTimeout(err) || errors.As(err, &gerr) && gerr.Code == "upstream_timeout" {
				return nil, timeout
			}
			return nil, unavailable
		}
		raw.Write(chunk)
	}
	result, err := Loads(raw.String(), "invalid_response")
	if obj, ok := result.(host.Obj); err == nil && ok {
		return obj, nil
	}
	return nil, unavailable
}

// googleOAuthClient is (client_id, client_secret) for refreshing the host login: environment
// first, then the local file (oauth_client).
func googleOAuthClient() (string, string, error) {
	id, secret := os.Getenv("ARKISM_GOOGLE_CLIENT_ID"), os.Getenv("ARKISM_GOOGLE_CLIENT_SECRET")
	if id == "" || secret == "" {
		id, secret = "", ""
		if raw, err := os.ReadFile(googleOAuthClientFile); err == nil {
			if value, err := host.Loads(string(raw)); err == nil {
				if o, ok := value.(map[string]any); ok {
					id, _ = o["client_id"].(string)
					secret, _ = o["client_secret"].(string)
				}
			}
		}
	}
	if id == "" || secret == "" {
		// Clear, fixed code: the operator must configure the client, nothing is guessed.
		return "", "", googleErr("google_oauth_client_missing", 503, false)
	}
	return id, secret, nil
}

// googleAuth is what credentials returns: key for API mode, token and project for the login.
type googleAuth struct {
	key, token, project string
	stamp               string
	expires             float64
	secrets             []string
}

// googleFinite is type(v) in (int, float) and math.isfinite(v); bools are not numbers.
func googleFinite(v any) (float64, bool) {
	if !isNumber(v) {
		return 0, false
	}
	f := number(v)
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

// googleCredentials returns a fresh host token/project without changing login or launching a
// CLI (credentials).
func googleCredentials(began float64) (*googleAuth, error) {
	unavailable := googleErr("login_unavailable", 503, false)
	if host.APIModeOn("gemini") {
		rows, err := host.LoadKeys()
		if err != nil {
			return nil, unavailable
		}
		for _, row := range rows {
			if row["provider"] == "google" {
				if key, ok := subscription.Token(row["key"]); ok {
					return &googleAuth{key: key, secrets: []string{key}}, nil
				}
			}
		}
		return nil, unavailable
	}
	home := os.Getenv("GEMINI_CLI_HOME")
	if home == "" {
		home = host.Home
	}
	folder := filepath.Join(home, ".gemini")
	var override any
	if project := os.Getenv("GOOGLE_CLOUD_PROJECT"); project != "" {
		override = project
	} else if project := os.Getenv("GOOGLE_CLOUD_PROJECT_ID"); project != "" {
		override = project
	}
	login, err := subscription.Credentials(filepath.Join(folder, "oauth_creds.json"))
	if err != nil {
		return nil, unavailable
	}
	access, accessOK := subscription.Token(login["access_token"])
	refresh, refreshOK := subscription.Token(login["refresh_token"])
	expiry, finite := googleFinite(login["expiry_date"])
	if !accessOK || !finite || expiry <= 0 {
		return nil, unavailable
	}
	stampText, err := host.Dumps([]any{folder, login, override}, host.DumpOpts{Compact: true, NoASCII: true, SortKeys: true})
	if err != nil {
		return nil, unavailable
	}
	stamp := fmt.Sprintf("%x", sha256.Sum256([]byte(stampText)))

	left, err := googleRemaining(began)
	if err != nil {
		return nil, err
	}
	wait := time.NewTimer(left)
	select {
	case googleAuthLock <- struct{}{}:
		wait.Stop()
	case <-wait.C:
		return nil, googleErr("upstream_timeout", 504, false)
	}
	defer func() { <-googleAuthLock }()
	if _, err := googleRemaining(began); err != nil {
		return nil, err
	}
	if cached := googleCache; cached != nil && cached.stamp == stamp && cached.expires > googleWall()+60 {
		return cached, nil
	}
	secrets := []string{access}
	if refreshOK && refresh != access {
		secrets = append(secrets, refresh)
	}
	expires := expiry / 1000
	if expires <= googleWall()+60 {
		if !refreshOK {
			return nil, unavailable
		}
		id, secret, err := googleOAuthClient()
		if err != nil {
			return nil, err
		}
		renewed, err := googleJSONPost(googleOAuth, host.Obj{{Key: "client_id", Value: id},
			{Key: "client_secret", Value: secret}, {Key: "refresh_token", Value: refresh},
			{Key: "grant_type", Value: "refresh_token"}}, nil, true, began)
		if err != nil {
			return nil, err
		}
		token, ok := subscription.Token(get(renewed, "access_token"))
		seconds, finite := googleFinite(get(renewed, "expires_in"))
		if !ok || !finite || !(60 < seconds && seconds <= 86400) {
			return nil, unavailable
		}
		access = token
		if !googleContains(secrets, access) {
			secrets = append(secrets, access)
		}
		expires = googleWall() + seconds
	}
	headers := map[string]string{"authorization": "Bearer " + access}
	payload := host.Obj{{Key: "metadata", Value: host.Obj{{Key: "ideType", Value: "IDE_UNSPECIFIED"},
		{Key: "platform", Value: "PLATFORM_UNSPECIFIED"}, {Key: "pluginType", Value: "GEMINI"}}}}
	if override != nil {
		payload.Set("cloudaicompanionProject", override)
	}
	info, err := googleJSONPost(googleAssist+":loadCodeAssist", payload, headers, false, began)
	if err != nil {
		return nil, err
	}
	project := get(info, "cloudaicompanionProject")
	if !truthy(project) {
		project = override
	}
	name, ok := project.(string)
	if _, tier := get(info, "currentTier").(host.Obj); !tier || !ok || !googleProject.MatchString(name) {
		return nil, googleErr("google_setup_required", 503, false)
	}
	googleCache = &googleAuth{stamp: stamp, expires: min(expires, googleWall()+300), token: access,
		project: name, secrets: secrets}
	return googleCache, nil
}

var googleProject = regexp.MustCompile(`^[a-z][a-z0-9-]{4,61}[a-z0-9]$`)

func googleContains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// googleChunks reads resp in bounded pieces, masking secrets even across piece boundaries.
// Failures are yielded once: *GoogleError for deadline, size and truncation, else the read error.
func googleChunks(resp *http.Response, secrets []string, began float64, limit int) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		var tail []byte
		total, margin := 0, 0
		for _, key := range secrets {
			margin = max(margin, len(key)-1)
		}
		buf := make([]byte, 65536)
		for {
			if googleDeadline-(googleMono()-began) <= 0 {
				yield(nil, googleErr("upstream_timeout", 504, true))
				return
			}
			n, err := resp.Body.Read(buf)
			if n > 0 {
				total += n
				if googleMono()-began > googleDeadline {
					yield(nil, googleErr("upstream_timeout", 504, true))
					return
				}
				if total > limit {
					yield(nil, googleErr("upstream_too_large", 502, true))
					return
				}
				data := append(tail, buf[:n]...)
				for _, key := range secrets {
					data = bytes.ReplaceAll(data, []byte(key), []byte("***"))
				}
				end := max(0, len(data)-margin)
				if !yield(bytes.Clone(data[:end]), nil) {
					return
				}
				tail = bytes.Clone(data[end:])
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				// The body ended before its declared length.
				yield(nil, googleErr("upstream_truncated", 502, true))
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
		if googleMono()-began > googleDeadline {
			yield(nil, googleErr("upstream_timeout", 504, true))
			return
		}
		if len(tail) > 0 {
			yield(tail, nil)
		}
	}
}

// googlePage is _page: one native page, unwrapped from the login API's envelope.
func googlePage(value any, wrapped bool) (host.Obj, error) {
	page, ok := value.(host.Obj)
	if !ok || has(page, "error") {
		return nil, Code("invalid_response")
	}
	if wrapped {
		inner, ok := get(page, "response").(host.Obj)
		if !ok {
			return nil, Code("invalid_response")
		}
		inner = clone(inner).(host.Obj)
		if trace, ok := get(page, "traceId").(string); ok {
			inner.Set("responseId", trace)
		}
		page = inner
	}
	if _, ok := getd(page, "candidates", []any{}).([]any); !ok {
		return nil, Code("invalid_response")
	}
	return page, nil
}

// googleCompletion is Completion: tools and terminal frames are held until every candidate
// really finishes.
type googleCompletion struct {
	seen, finished map[int64]bool
	blocked, usage bool
}

func newGoogleCompletion() *googleCompletion {
	return &googleCompletion{seen: map[int64]bool{}, finished: map[int64]bool{}}
}

var googleUsageKeys = []string{"promptTokenCount", "candidatesTokenCount", "totalTokenCount",
	"thoughtsTokenCount", "cachedContentTokenCount"}

func (c *googleCompletion) accept(page host.Obj) (hold bool, err error) {
	defer catch(&err)
	if feedback, ok := get(page, "promptFeedback").(host.Obj); ok && truthy(get(feedback, "blockReason")) {
		c.blocked = true
	}
	usage, hasUsage := page.Get("usageMetadata")
	hasUsage = hasUsage && usage != nil
	if hasUsage {
		o, ok := usage.(host.Obj)
		if !ok {
			fail("invalid_usage")
		}
		for _, key := range googleUsageKeys {
			if value, ok := o.Get(key); ok {
				if n, ok := host.Int(value); !ok || !isInt(value) || n < 0 {
					fail("invalid_usage")
				}
			}
		}
		c.usage = c.usage || has(o, "totalTokenCount")
	}
	hold = c.blocked || (hasUsage && !truthy(get(page, "candidates")))
	candidates, _ := getd(page, "candidates", []any{}).([]any)
	for _, item := range candidates {
		candidate, ok := item.(host.Obj)
		if !ok {
			fail("invalid_response")
		}
		raw := getd(candidate, "index", 0)
		index, ok := host.Int(raw)
		if !ok || !isInt(raw) || index < 0 || index >= 16 || c.finished[index] {
			fail("invalid_response")
		}
		c.seen[index] = true
		content, ok := getd(candidate, "content", host.Obj{}).(host.Obj)
		if !ok {
			fail("invalid_response")
		}
		parts, ok := getd(content, "parts", []any{}).([]any)
		if !ok {
			fail("invalid_response")
		}
		for _, item := range parts {
			part, ok := item.(host.Obj)
			if !ok {
				fail("invalid_response")
			}
			if text, ok := part.Get("text"); ok {
				if _, ok := text.(string); !ok {
					fail("invalid_response")
				}
			}
			if signature, ok := part.Get("thoughtSignature"); ok {
				if s, ok := signature.(string); !ok || s == "" {
					fail("invalid_response")
				}
			}
			if value, ok := part.Get("functionCall"); ok {
				call, ok := value.(host.Obj)
				if !ok {
					fail("invalid_response")
				}
				if name, ok := get(call, "name").(string); !ok || name == "" {
					fail("invalid_response")
				}
				if _, ok := getd(call, "args", host.Obj{}).(host.Obj); !ok {
					fail("invalid_response")
				}
				hold = true
			}
		}
		if reason, ok := candidate.Get("finishReason"); ok && reason != nil {
			if s, ok := reason.(string); !ok || s == "" || s == "FINISH_REASON_UNSPECIFIED" {
				fail("invalid_response")
			}
			c.finished[index] = true
			hold = true
		}
	}
	return hold, nil
}

func (c *googleCompletion) finish() error {
	if c.blocked && len(c.seen) > 0 {
		return Code("invalid_response")
	}
	if !c.blocked && (len(c.seen) == 0 || len(c.finished) != len(c.seen) || !c.usage) {
		return Code("truncated_stream")
	}
	return nil
}

// googleCountContents rewrites thought parts the way the login API's countTokens expects: the
// request-side thought marker, not the response-only flag (_count_contents).
func googleCountContents(contents []any) ([]any, error) {
	contents = clone(contents).([]any)
	for _, content := range contents {
		parts := get(content, "parts").([]any)
		for i, item := range parts {
			part := item.(host.Obj)
			if get(part, "thought") != true {
				continue
			}
			part.Del("thought")
			parts[i] = part
			if has(part, "functionCall") || has(part, "functionResponse") || has(part, "inlineData") || has(part, "fileData") {
				continue
			}
			text, ok := getd(part, "text", "").(string)
			if !ok {
				return nil, googleErr("invalid_request", 400, false)
			}
			if text != "" {
				text += "\n"
			}
			part.Set("text", text+"[Thought: true]")
			parts[i] = part
		}
	}
	return contents, nil
}

var errGoogleStop = errors.New("stopped")

// googleIterContent yields validated native pages (host.Obj); a failure ends the sequence with
// (nil, *GoogleError). Stopping early closes the connection and frees the slot (iter_content).
// Each run sends generation at most once. A variable so gateway tests can fake it.
var googleIterContent = googleContent

func googleContent(body any, model string, stream, count bool) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		if err := googleRun(body, model, stream, count, func(page host.Obj) bool { return yield(page, nil) }); err != nil {
			yield(nil, err)
		}
	}
}

// googleRun never calls yield from inside a recover scope, so a consumer's panic passes through.
func googleRun(input any, model string, stream, count bool, yield func(host.Obj) bool) error {
	body, err := googlePrepare(input, model)
	if err != nil {
		return err
	}
	if count && (len(body) != 1 || body[0].Key != "contents") {
		return googleErr("unsupported_field", 400, false)
	}
	select {
	case googleSlots <- struct{}{}:
	default:
		return googleErr("google_capacity", 503, false)
	}
	defer func() { <-googleSlots }()
	began, started := googleMono(), false
	invalid := func(err error) error {
		var gerr *GoogleError
		switch {
		case errors.As(err, &gerr):
			return gerr
		case googleTimeout(err):
			return googleErr("upstream_timeout", 504, started)
		case started:
			return googleErr("upstream_invalid_response", 502, true)
		}
		return googleErr("login_unavailable", 503, false)
	}

	auth, err := googleCredentials(began)
	if err != nil {
		return err
	}
	wrapped := auth.token != ""
	method := "generateContent"
	if count {
		method = "countTokens"
	} else if stream {
		method = "streamGenerateContent"
	}
	var headers map[string]string
	var payload any
	var target string
	if wrapped {
		headers = map[string]string{"authorization": "Bearer " + auth.token}
		if count {
			contents, err := googleCountContents(get(body, "contents").([]any))
			if err != nil {
				return err
			}
			payload = host.Obj{{Key: "request", Value: host.Obj{{Key: "model", Value: "models/" + model},
				{Key: "contents", Value: contents}}}}
		} else {
			payload = host.Obj{{Key: "model", Value: model}, {Key: "project", Value: auth.project},
				{Key: "user_prompt_id", Value: googleUUID()}, {Key: "request", Value: body}}
		}
		target = googleAssist + ":" + method
	} else {
		headers, payload = map[string]string{"x-goog-api-key": auth.key}, body
		target = googleAPI + "/models/" + model + ":" + method
	}
	if stream && !count {
		target += "?alt=sse"
	}
	text, err := googleEncoded(payload)
	if err != nil {
		return invalid(err)
	}
	left := googleDeadline - (googleMono() - began)
	if left <= 0 {
		return googleErr("upstream_timeout", 504, false)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(left*float64(time.Second)))
	defer cancel()
	resp, status, err := googleOpen(ctx, target, "application/json", text, headers, time.Duration(left*float64(time.Second)))
	if err != nil {
		if googleTimeout(err) {
			return googleErr("upstream_timeout", 504, true)
		}
		return googleErr("upstream_unreachable", 502, true)
	}
	if status != 0 {
		if status < 400 || status > 599 {
			status = 502
		}
		return googleErr(fmt.Sprintf("upstream_http_%d", status), status, false)
	}
	started = true
	defer resp.Body.Close()
	chunks := googleChunks(resp, auth.secrets, began, googleMaxOutput)

	if !stream || count {
		if googleContentType(resp) != "application/json" {
			return invalid(Code("invalid_response"))
		}
		var raw bytes.Buffer
		for chunk, err := range chunks {
			if err != nil {
				return invalid(err)
			}
			raw.Write(chunk)
		}
		value, err := Loads(raw.String(), "invalid_response")
		if err != nil {
			return invalid(err)
		}
		var page host.Obj
		if count {
			page, _ = value.(host.Obj)
			total, ok := host.Int(get(page, "totalTokens"))
			if page == nil || !ok || !isInt(get(page, "totalTokens")) || total < 0 {
				return invalid(Code("invalid_usage"))
			}
		} else {
			if page, err = googlePage(value, wrapped); err != nil {
				return invalid(err)
			}
			complete := newGoogleCompletion()
			if _, err := complete.accept(page); err != nil {
				return invalid(err)
			}
			if err := complete.finish(); err != nil {
				return invalid(err)
			}
		}
		yield(page)
		return nil
	}

	if googleContentType(resp) != "text/event-stream" {
		return invalid(Code("invalid_stream"))
	}
	next, stop := iter.Pull2(func(inner func(string, error) bool) {
		err := SSEFrames(chunks, func(_, data string) error {
			if !inner(data, nil) {
				return errGoogleStop
			}
			return nil
		})
		if err != nil && err != errGoogleStop {
			inner("", err)
		}
	})
	defer stop()
	complete, holding := newGoogleCompletion(), false
	var pending []host.Obj
	for {
		data, err, ok := next()
		if !ok {
			break
		}
		if err != nil {
			return invalid(err)
		}
		value, err := Loads(data, "invalid_response")
		if err != nil {
			return invalid(err)
		}
		page, err := googlePage(value, wrapped)
		if err != nil {
			return invalid(err)
		}
		hold, err := complete.accept(page)
		if err != nil {
			return invalid(err)
		}
		if holding = hold || holding; holding {
			pending = append(pending, page)
		} else if !yield(page) {
			return nil
		}
	}
	if err := complete.finish(); err != nil {
		return invalid(err)
	}
	for _, page := range pending {
		if !yield(page) {
			return nil
		}
	}
	return nil
}

// googleUUID is str(uuid.uuid4()).
func googleUUID() string {
	h := newHex()
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
