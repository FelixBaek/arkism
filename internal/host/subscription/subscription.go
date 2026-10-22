package subscription

// Local subscription identity and usage.
// JWT claims are local hints, not verified authentication or authorization.
// Only FetchUsage performs I/O beyond reading the explicitly supplied folder.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // reset times name IANA zones; Windows has no zone database
	"unicode/utf8"
)

// MaxBytes bounds credential files, CLI output and usage answers.
const MaxBytes = 512 * 1024

// AuthClaim is the JWT claim that carries the ChatGPT plan and account.
const AuthClaim = "https://api.openai.com/auth"

var (
	ansi      = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-_]`)
	usageLine = regexp.MustCompile(`(?i)^Current (session|week)(?: \(([^)]+)\))?:\s*(\d+(?:\.\d+)?)% used(?:\s*·\s*resets (.+))?$`)
	expiredRe = regexp.MustCompile(`(?i)\b401\b|not (?:logged|signed) in|signed out|(?:sign-in|session|token) (?:has )?expired|unauthorized|authentication (?:failed|required)|invalid (?:access )?token`)
	deniedRe  = regexp.MustCompile(`(?i)\b403\b|forbidden`)
	labelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	tokenRe   = regexp.MustCompile(`^[\x21-\x7e]+$`)
	jwtPartRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	zoneRe    = regexp.MustCompile(`\s*\(([^)]+)\)$`)
	resetRe   = regexp.MustCompile(`(?i)^(?:([A-Za-z]{3}) (\d{1,2})(?:,? (\d{4}))?(?: at |, ))?(\d{1,2})(?::(\d{2}))?(am|pm)$`)
	spaceRun  = regexp.MustCompile(`\s+`)

	// CodexBase is the ChatGPT backend that both the usage reading and the Codex messages use.
	CodexBase = "https://chatgpt.com"
	// Client never uses an environment proxy, never follows redirects and speaks HTTP/1.1.
	Client = &http.Client{
		Transport:     &http.Transport{Proxy: nil, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	now = time.Now
)

var errInvalid = errors.New("invalid value")

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func result(t time.Time, status string, quotas []any) map[string]any {
	if quotas == nil {
		quotas = []any{}
	}
	if status == "" {
		status = "unavailable"
	}
	return map[string]any{"quotas": quotas, "usage_status": status, "usage_checked_at": iso(t)}
}

// number is a finite JSON number (never a bool); strings counts numeric strings too.
func number(v any, strs bool) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		var err error
		if f, err = strconv.ParseFloat(string(x), 64); err != nil {
			return 0, false
		}
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case float64:
		f = x
	case string:
		s := strings.TrimSpace(x)
		if !strs || strings.ContainsAny(s, "xX") {
			return 0, false
		}
		var err error
		if f, err = strconv.ParseFloat(s, 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

// timestamp is a non-negative Unix time within Python's datetime range (year 9999).
func timestamp(v any) (time.Time, bool) {
	n, ok := number(v, false)
	if !ok || n < 0 || n >= 253402300800 {
		return time.Time{}, false
	}
	sec := math.Floor(n)
	us := math.RoundToEven((n - sec) * 1e6)
	return time.Unix(int64(sec), int64(us)*1000).UTC(), true
}

func label(v any, limit int) (string, bool) {
	s, ok := v.(string)
	return s, ok && utf8.RuneCountInString(s) <= limit && labelRe.MatchString(s)
}

// Token is v when it is a printable ASCII token of 1–65536 characters.
func Token(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && len(s) > 0 && len(s) <= 65536 && tokenRe.MatchString(s)
}

// StrictJSON parses one JSON value, refusing invalid UTF-8, duplicate fields, integers longer
// than 128 characters, NaN/Infinity and trailing data.
func StrictJSON(raw []byte) (any, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return v, nil
}

func strictValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := key.(string)
				if _, dup := obj[k]; dup {
					return nil, errors.New("duplicate JSON field")
				}
				if obj[k], err = strictValue(dec); err != nil {
					return nil, err
				}
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := strictValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
		return nil, errors.New("invalid JSON")
	case json.Number:
		if !strings.ContainsAny(string(t), ".eE") && len(t) > 128 {
			return nil, errors.New("JSON integer too large")
		}
	}
	return tok, nil
}

// Credentials reads a credential object, refusing links, special files and oversize files.
func Credentials(path string) (map[string]any, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return nil, errors.New("invalid credential file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, errors.New("credential file too large")
	}
	v, err := StrictJSON(raw)
	obj, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, errors.New("invalid credential object")
	}
	return obj, nil
}

func jwtClaims(v any) (map[string]any, error) {
	token, ok := Token(v)
	parts := strings.Split(token, ".")
	if !ok || len(parts) != 3 {
		return nil, errors.New("invalid JWT")
	}
	var decoded [3][]byte
	for i, part := range parts {
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if !jwtPartRe.MatchString(part) || err != nil {
			return nil, errors.New("invalid JWT encoding")
		}
		decoded[i] = raw
	}
	if head, err := StrictJSON(decoded[0]); err != nil || reflect.TypeOf(head) != reflect.TypeOf(map[string]any{}) {
		return nil, errors.New("invalid JWT header")
	}
	claims, err := StrictJSON(decoded[1])
	obj, ok := claims.(map[string]any)
	if err != nil || !ok {
		return nil, errors.New("invalid JWT claims")
	}
	return obj, nil
}

// Login reads cli's login in configDir. The token it returns is private: it is never part of
// the public identity, and it is "" unless the login is connected.
func Login(cli, configDir string) (map[string]any, string) {
	out, token, err := login(cli, configDir)
	if err != nil {
		return map[string]any{"status": "missing"}, ""
	}
	return out, token
}

func login(cli, dir string) (map[string]any, string, error) {
	out := map[string]any{"status": "missing"}
	secrets := map[string]bool{}
	var token string
	var plan, account any
	var expiries []time.Time
	var ok bool
	var err error
	switch cli {
	case "claude":
		token, plan, account, expiries, ok, err = claudeLogin(dir, out, secrets)
	case "codex":
		token, plan, account, expiries, ok, err = codexLogin(dir, secrets)
	default:
		return out, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return out, "", nil
	}
	out["status"] = "connected"
	for _, e := range expiries {
		if !e.After(now()) {
			out["status"] = "expired"
		}
	}
	if p, ok := label(plan, 40); ok && !secrets[p] {
		out["plan"] = p
	}
	if a, ok := label(account, 128); ok && !secrets[a] {
		out["account_id"] = a
	}
	if out["status"] != "connected" {
		token = ""
	}
	return out, token, nil
}

// claudeLogin reads Claude's credentials; ok=false with a nil error means not logged in.
func claudeLogin(dir string, out map[string]any, secrets map[string]bool) (token string, plan, account any, expiries []time.Time, ok bool, err error) {
	creds, err := Credentials(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return "", nil, nil, nil, false, err
	}
	oauth, ok := creds["claudeAiOauth"].(map[string]any)
	if !ok {
		return "", nil, nil, nil, false, nil
	}
	tok, ok := Token(oauth["accessToken"])
	if !ok {
		// A signed-out or partially refreshed file can still describe its subscription.
		// Show that hint without claiming a login.
		if p, ok := label(oauth["subscriptionType"], 40); ok && oauth["subscriptionType"] != oauth["accessToken"] {
			out["plan"] = p
		}
		return "", nil, nil, nil, false, nil
	}
	token, secrets[tok] = tok, true
	plan, account = oauth["subscriptionType"], oauth["accountId"]
	if v, has := oauth["expiresAt"]; has {
		millis, ok := number(v, false)
		if !ok {
			return "", nil, nil, nil, false, nil
		}
		expiry, ok := timestamp(millis / 1000)
		if !ok {
			return "", nil, nil, nil, false, nil
		}
		expiries = append(expiries, expiry)
	}
	return token, plan, account, expiries, true, nil
}

// codexLogin reads Codex's credentials; ok=false with a nil error means not logged in.
func codexLogin(dir string, secrets map[string]bool) (token string, plan, account any, expiries []time.Time, ok bool, err error) {
	auth, err := Credentials(filepath.Join(dir, "auth.json"))
	if err != nil {
		return "", nil, nil, nil, false, err
	}
	if auth["auth_mode"] == "apikey" {
		return "", nil, nil, nil, false, nil
	}
	tokens, ok := auth["tokens"].(map[string]any)
	tok, ok2 := Token(tokens["access_token"])
	if !ok || !ok2 {
		return "", nil, nil, nil, false, nil
	}
	token, secrets[tok] = tok, true
	var claims []map[string]any
	access := -1
	if id := tokens["id_token"]; id != nil {
		claim, err := jwtClaims(id)
		if err != nil {
			return "", nil, nil, nil, false, err
		}
		claims, secrets[id.(string)] = append(claims, claim), true
	}
	if strings.Contains(token, ".") {
		claim, err := jwtClaims(token)
		if err != nil {
			return "", nil, nil, nil, false, err
		}
		claims, access = append(claims, claim), len(claims)
	}
	if a, ok := label(tokens["account_id"], 128); ok {
		account = a
	}
	for i, claim := range claims {
		if auth, ok := claim[AuthClaim].(map[string]any); ok {
			if p, ok := label(auth["chatgpt_plan_type"], 40); ok && plan == nil {
				plan = p
			}
			if a, ok := label(auth["chatgpt_account_id"], 128); ok && account == nil {
				account = a
			}
		}
		// An id_token describes the login. It can be older than a freshly renewed
		// access token and must not disable that token.
		if v, has := claim["exp"]; has && i == access {
			expiry, ok := timestamp(v)
			if !ok {
				return "", nil, nil, nil, false, nil
			}
			expiries = append(expiries, expiry)
		}
	}
	return token, plan, account, expiries, true, nil
}

// Identity reads only the credentials in configDir: no CLI, no refresh, no token in the answer.
func Identity(cli, configDir string) map[string]any {
	out, _ := Login(cli, configDir)
	return out
}

var months = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}

// resetAt reads the CLI's "resets ..." text: the first matching clock after now within the
// window (plus two hours), else the latest one before now.
func resetAt(text string, t time.Time, span time.Duration) (time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, false
	}
	zone, text, ok := resetZone(text, t)
	if !ok {
		return time.Time{}, false
	}
	m := resetRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	ref := t.In(zone)
	hour, minute, ok := resetClock(m)
	if !ok {
		return time.Time{}, false
	}
	var dates []time.Time // naive wall clocks, labelled UTC
	if m[1] != "" {
		if dates, ok = resetDates(m, ref, hour, minute); !ok {
			return time.Time{}, false
		}
	} else {
		base := time.Date(ref.Year(), ref.Month(), ref.Day(), hour, minute, 0, 0, time.UTC)
		for day := -2; day <= 2; day++ {
			dates = append(dates, base.AddDate(0, 0, day))
		}
	}
	var candidates []time.Time
	for _, d := range dates {
		candidates = append(candidates, wallInstants(d, zone)...)
	}
	if len(candidates) == 0 {
		return time.Time{}, false
	}
	if m[3] != "" {
		return datedReset(candidates, t), true
	}
	best := nearestReset(candidates, t, span)
	return best, !best.IsZero()
}

// resetZone strips a trailing time zone name; without one, t's local offset applies.
func resetZone(text string, t time.Time) (*time.Location, string, bool) {
	_, offset := t.In(time.Local).Zone()
	zone := time.FixedZone("", offset)
	if m := zoneRe.FindStringSubmatchIndex(text); m != nil {
		name := text[m[2]:m[3]]
		loc, err := time.LoadLocation(name)
		if err != nil || name == "Local" {
			return nil, "", false
		}
		zone, text = loc, strings.TrimSpace(text[:m[0]])
	}
	return zone, text, true
}

// resetClock reads the 12-hour clock as a 24-hour hour and minute.
func resetClock(m []string) (int, int, bool) {
	hour, _ := strconv.Atoi(m[4])
	minute := 0
	if m[5] != "" {
		minute, _ = strconv.Atoi(m[5])
	}
	if hour < 1 || hour > 12 || minute >= 60 {
		return 0, 0, false
	}
	hour %= 12
	if strings.ToLower(m[6]) == "pm" {
		hour += 12
	}
	return hour, minute, true
}

// resetDates lists the valid wall clocks for a month and day, in the given year or around ref's.
func resetDates(m []string, ref time.Time, hour, minute int) ([]time.Time, bool) {
	month := 0
	for i, name := range months {
		if name == strings.ToLower(m[1]) {
			month = i + 1
		}
	}
	if month == 0 {
		return nil, false
	}
	day, _ := strconv.Atoi(m[2])
	years := []int{ref.Year() - 1, ref.Year(), ref.Year() + 1}
	if m[3] != "" {
		y, _ := strconv.Atoi(m[3])
		years = []int{y}
	}
	var dates []time.Time
	for _, y := range years {
		d := time.Date(y, time.Month(month), day, hour, minute, 0, 0, time.UTC)
		if y >= 1 && y <= 9999 && d.Year() == y && int(d.Month()) == month && d.Day() == day {
			dates = append(dates, d)
		}
	}
	return dates, true
}

// datedReset picks the earliest candidate not before t, else the latest.
func datedReset(candidates []time.Time, t time.Time) time.Time {
	var best time.Time
	for _, c := range candidates {
		if !c.Before(t) && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if best.IsZero() {
		for _, c := range candidates {
			if c.After(best) {
				best = c
			}
		}
	}
	return best
}

// nearestReset picks the earliest candidate within the window, else the latest one before t.
func nearestReset(candidates []time.Time, t time.Time, span time.Duration) time.Time {
	var best time.Time
	for _, c := range candidates {
		if !c.Before(t) && !c.After(t.Add(span+2*time.Hour)) && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if best.IsZero() {
		for _, c := range candidates {
			if c.Before(t) && c.After(best) {
				best = c
			}
		}
	}
	return best
}

// wallInstants are the instants whose wall clock in loc reads naive: two for a clock repeated
// when DST ends, none for one skipped when it starts.
func wallInstants(naive time.Time, loc *time.Location) []time.Time {
	var out []time.Time
	seen := map[int]bool{}
	for at, end := naive.Add(-30*time.Hour), naive.Add(30*time.Hour); !at.After(end); {
		local := at.In(loc)
		if _, offset := local.Zone(); !seen[offset] {
			seen[offset] = true
			u := naive.Add(-time.Duration(offset) * time.Second)
			w := u.In(loc)
			if w.Year() == naive.Year() && w.YearDay() == naive.YearDay() && w.Hour() == naive.Hour() && w.Minute() == naive.Minute() && w.Second() == 0 {
				out = append(out, u.UTC())
			}
		}
		_, next := local.ZoneBounds()
		if !next.After(at) {
			break
		}
		at = next
	}
	return out
}

// splitLines is Python's str.splitlines.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			if r == '\n' && i > 0 && s[i-1] == '\r' {
				start = i + 1
				continue
			}
			lines = append(lines, s[start:i])
			start = i + utf8.RuneLen(r)
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// ParseClaudeUsage reads `claude -p /usage` output.
func ParseClaudeUsage(text string, t time.Time) map[string]any {
	if len(text) > MaxBytes {
		return result(t, "", nil)
	}
	text = ansi.ReplaceAllString(text, "")
	if expiredRe.MatchString(text) {
		return result(t, "expired", nil)
	}
	if deniedRe.MatchString(text) {
		return result(t, "", nil)
	}
	var order []string
	quotas := map[string]map[string]any{}
	for _, line := range splitLines(text) {
		m := usageLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		used, ok := number(m[3], true)
		if !ok || used < 0 || used > 100 {
			return result(t, "", nil)
		}
		window, span := "week", 7*24*time.Hour
		if strings.ToLower(m[1]) == "session" {
			window, span = "5h", 5*time.Hour
		}
		quota := map[string]any{"window": window, "left_pct": int(math.Ceil(100 - used))}
		if scope := strings.ToLower(strings.TrimSpace(m[2])); scope != "" && scope != "all models" {
			model := spaceRun.ReplaceAllString(scope, "-")
			if _, ok := label(model, 15); !ok {
				return result(t, "", nil)
			}
			quota["window"], quota["model"] = "week-"+model, model
		}
		if reset, ok := resetAt(m[4], t, span); ok {
			quota["resets_at"] = iso(reset)
		}
		key := quota["window"].(string)
		if old, ok := quotas[key]; ok {
			if !reflect.DeepEqual(old, quota) {
				return result(t, "", nil)
			}
			continue
		}
		order, quotas[key] = append(order, key), quota
	}
	list := []any{}
	for _, key := range order {
		list = append(list, quotas[key])
	}
	if len(list) == 0 {
		return result(t, "", nil)
	}
	return result(t, "ok", list)
}

// ParseCodexUsage reads the ChatGPT usage answer.
func ParseCodexUsage(obj any, t time.Time) map[string]any {
	body, ok := obj.(map[string]any)
	if _, failed := body["error"]; !ok || failed {
		return result(t, "", nil)
	}
	quotas := []any{}
	if rate, ok := body["rate_limit"].(map[string]any); ok {
		for _, name := range []string{"primary_window", "secondary_window"} {
			if rate[name] == nil {
				continue
			}
			quota, ok := codexQuota(rate[name])
			if !ok {
				return result(t, "", nil)
			}
			for _, previous := range quotas {
				if previous.(map[string]any)["window"] == quota["window"] {
					return result(t, "", nil)
				}
			}
			quotas = append(quotas, quota)
		}
	}
	status := "unavailable"
	if len(quotas) > 0 {
		status = "ok"
	}
	out := result(t, status, quotas)
	if credit, ok := body["credits"].(map[string]any); ok {
		credits := map[string]any{}
		if balance, ok := number(credit["balance"], true); ok && balance >= 0 {
			credits["balance"] = balance
		}
		if unlimited, ok := credit["unlimited"].(bool); ok {
			credits["unlimited"] = unlimited
		}
		if len(credits) > 0 {
			out["credits"] = credits
		}
	}
	return out
}

// codexQuota reads one rate-limit window.
func codexQuota(v any) (map[string]any, bool) {
	window, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	used, ok := number(window["used_percent"], false)
	seconds, _ := number(window["limit_window_seconds"], false)
	if !ok || used < 0 || used > 100 || (seconds != 18000 && seconds != 604800) {
		return nil, false
	}
	quota := map[string]any{"window": "week", "left_pct": int(math.Ceil(100 - used))}
	if seconds == 18000 {
		quota["window"] = "5h"
	}
	if window["reset_at"] != nil {
		reset, ok := timestamp(window["reset_at"])
		if !ok {
			return nil, false
		}
		quota["resets_at"] = iso(reset)
	}
	return quota, true
}

func claudeOutput(configDir, executable string, timeout time.Duration) (string, string, int, error) {
	drop := map[string]bool{"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "AUTH_TOKEN": true, "ANTHROPIC_BASE_URL": true,
		"BASE_URL": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR": true,
		"ANTHROPIC_API_KEY_FILE_DESCRIPTOR": true, "CLAUDE_CONFIG_DIR": true}
	var env []string
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !drop[name] {
			env = append(env, kv)
		}
	}
	// No settings sources: the probe runs every minute and must not fire the user's SessionStart hooks
	cmd := exec.Command(executable, "-p", "/usage", "--setting-sources", "")
	cmd.Env = append(env, "CLAUDE_CONFIG_DIR="+resolve(configDir))
	cmd.Dir = configDir
	out, errs, code, err := runBounded(cmd, true, true, MaxBytes, timeout)
	return strings.ToValidUTF8(string(out), "�"), strings.ToValidUTF8(string(errs), "�"), code, err
}

// FetchUsage takes a fresh reading and returns only normalized data and safe statuses.
func FetchUsage(cli, configDir, executable string, timeout float64) map[string]any {
	t := now()
	login, token := Login(cli, configDir)
	if login["status"] != "connected" {
		if login["status"] == "expired" {
			return result(t, "expired", nil)
		}
		return result(t, "", nil)
	}
	if !(timeout > 0 && timeout <= 60) {
		return result(t, "", nil)
	}
	limit := time.Duration(timeout * float64(time.Second))
	switch cli {
	case "claude":
		out, errs, code, err := claudeOutput(configDir, executable, limit)
		if err != nil {
			return result(t, "", nil)
		}
		combined := ansi.ReplaceAllString(out+"\n"+errs, "")
		if expiredRe.MatchString(combined) {
			return result(t, "expired", nil)
		}
		if code != 0 || deniedRe.MatchString(combined) {
			return result(t, "", nil)
		}
		return ParseClaudeUsage(out, t)
	case "codex":
		return codexUsage(token, login, t, limit)
	}
	return result(t, "", nil)
}

func codexUsage(token string, login map[string]any, t time.Time, limit time.Duration) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CodexBase+"/backend-api/wham/usage", nil)
	if err != nil {
		return result(t, "", nil)
	}
	req.Header["Authorization"] = []string{"Bearer " + token}
	req.Header["Accept"] = []string{"application/json"}
	req.Header["Accept-Encoding"] = []string{"identity"}
	req.Header["User-Agent"] = []string{""} // none, like http.client
	if account, ok := login["account_id"].(string); ok && account != "" {
		req.Header["ChatGPT-Account-Id"] = []string{account}
	}
	resp, err := Client.Do(req)
	if err != nil {
		return result(t, "", nil)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return result(t, "expired", nil)
		}
		return result(t, "", nil)
	}
	if resp.ContentLength > MaxBytes {
		return result(t, "", nil)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes || (resp.ContentLength >= 0 && int64(len(raw)) != resp.ContentLength) {
		return result(t, "", nil)
	}
	obj, err := StrictJSON(raw)
	if err != nil {
		return result(t, "", nil)
	}
	return ParseCodexUsage(obj, t)
}
