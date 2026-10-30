// Package notifycfg edits the host notify file (~/.config/arkism/notify.json, 0600) that crewrun reads for
// Telegram and Slack notices. Secrets go in once and never come back out: List returns only the last four.
// Other channels (email) and unknown keys in the file are kept as they are.
package notifycfg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

var ErrInvalid = errors.New("invalid_request")

// TelegramAPI is overridable for tests.
var TelegramAPI = "https://api.telegram.org"

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	tgTokenRe = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{20,}$`)
	tgChatRe  = regexp.MustCompile(`^(-?\d{3,20}|@[A-Za-z0-9_]{4,})$`)
	slackRe   = regexp.MustCompile(`^https://hooks\.slack\.com/(services|workflows)/[A-Za-z0-9/_-]{20,}$`)
	mu        sync.Mutex
)

// Dest is one saved destination as the screen sees it.
type Dest struct {
	Channel string `json:"channel"` // telegram | slack
	Name    string `json:"name"`
	Last4   string `json:"last4"`
	ChatID  string `json:"chat_id,omitempty"` // telegram only; not a secret
}

func last4(s string) string {
	r := []rune(s)
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return string(r)
}

func read(path string) (map[string]map[string]map[string]any, error) {
	out := map[string]map[string]map[string]any{}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	// Keep every channel, not just the two edited here, so email destinations survive a save.
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func write(path string, cfg map[string]map[string]map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	return host.AtomicWrite(path, string(raw))
}

// Path is the one notify file the server's 설정 › 메신저 writes and every workflow runner reads:
// ARKISM_CREW_NOTIFY, else ~/.config/arkism/notify.json. It does not follow ARKISM_AGENTS_STATE,
// because the desktop runs its server without it and its runner with it. A file saved at the old
// place (ARKISM_AGENTS_STATE/notify.json) is still used while the new one does not exist.
func Path() string {
	if v := os.Getenv("ARKISM_CREW_NOTIFY"); v != "" {
		return v
	}
	p := filepath.Join(host.Home, ".config", "arkism", "notify.json")
	if old := filepath.Join(host.AgentsState, "notify.json"); old != p {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Stat(old); err == nil {
				return old
			}
		}
	}
	return p
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// ScopeKey names a workspace/project in Key. Each part is path-escaped, so an ID holding "/" can never
// make two scopes share a key ("a/b"+"c" vs "a"+"b/c").
func ScopeKey(workspace, project string) string {
	return url.PathEscape(workspace) + "/" + url.PathEscape(project)
}

// Key is how a destination is stored: "<scope>/<name>", so one scope never sees or edits another's
// (crewrun looks up its own scope first). An empty scope is the host's own, hand-edited entries.
func Key(scope, name string) string {
	if scope == "" {
		return name
	}
	return scope + "/" + name
}

// List returns the scope's telegram and slack destinations, secrets masked.
func List(path, scope string) ([]Dest, error) {
	mu.Lock()
	defer mu.Unlock()
	cfg, err := read(path)
	if err != nil {
		return nil, err
	}
	out := []Dest{}
	for _, ch := range []string{"telegram", "slack"} {
		for k, d := range cfg[ch] {
			name, ok := strings.CutPrefix(k, Key(scope, ""))
			if !ok || strings.Contains(name, "/") {
				continue
			}
			secret := str(d, "bot_token")
			if ch == "slack" {
				secret = str(d, "webhook_url")
			}
			if secret == "" {
				continue
			}
			x := Dest{Channel: ch, Name: name, Last4: last4(secret)}
			if ch == "telegram" {
				x.ChatID = str(d, "chat_id")
			}
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Channel+"/"+out[i].Name < out[j].Channel+"/"+out[j].Name
	})
	return out, nil
}

// Save adds or replaces a destination. telegram: bot_token + chat_id; slack: webhook_url.
// A telegram save without bot_token keeps the stored token (changing only the chat).
func Save(path, scope, channel, name string, f map[string]string) error {
	if name == "" {
		name = "default"
	}
	if !nameRe.MatchString(name) {
		return ErrInvalid
	}
	key := Key(scope, name)
	mu.Lock()
	defer mu.Unlock()
	cfg, err := read(path)
	if err != nil {
		return err
	}
	dest := map[string]any{}
	switch channel {
	case "telegram":
		tok, chat := strings.TrimSpace(f["bot_token"]), strings.TrimSpace(f["chat_id"])
		if tok == "" {
			tok = str(cfg["telegram"][key], "bot_token")
		}
		if !tgTokenRe.MatchString(tok) || !tgChatRe.MatchString(chat) {
			return ErrInvalid
		}
		dest["bot_token"], dest["chat_id"] = tok, chat
	case "slack":
		u := strings.TrimSpace(f["webhook_url"])
		if !slackRe.MatchString(u) {
			return ErrInvalid
		}
		dest["webhook_url"] = u
	default:
		return ErrInvalid
	}
	if cfg[channel] == nil {
		cfg[channel] = map[string]map[string]any{}
	}
	cfg[channel][key] = dest
	return write(path, cfg)
}

// Remove deletes a destination; a missing one is not an error.
func Remove(path, scope, channel, name string) error {
	if channel != "telegram" && channel != "slack" || !nameRe.MatchString(name) {
		return ErrInvalid
	}
	mu.Lock()
	defer mu.Unlock()
	cfg, err := read(path)
	if err != nil {
		return err
	}
	key := Key(scope, name)
	if _, ok := cfg[channel][key]; !ok {
		return nil
	}
	delete(cfg[channel], key)
	if len(cfg[channel]) == 0 {
		delete(cfg, channel)
	}
	return write(path, cfg)
}

// Test sends one short message through a saved destination. Errors never carry the token or URL.
func Test(ctx context.Context, path, scope, channel, name string) error {
	mu.Lock()
	cfg, err := read(path)
	mu.Unlock()
	if err != nil {
		return err
	}
	d, ok := cfg[channel][Key(scope, name)]
	if !ok {
		return ErrInvalid
	}
	const msg = "Arkism 연결 시험이에요. 이 메시지가 보이면 알림이 잘 가요."
	var target string
	body := map[string]any{"text": msg}
	switch channel {
	case "telegram":
		target = TelegramAPI + "/bot" + str(d, "bot_token") + "/sendMessage"
		body = map[string]any{"chat_id": str(d, "chat_id"), "text": msg}
	case "slack":
		target = str(d, "webhook_url")
	default:
		return ErrInvalid
	}
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return errors.New("bad destination")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return errors.New("send failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	return nil
}
