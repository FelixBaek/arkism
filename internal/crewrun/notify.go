package crewrun

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// NotifyConfig is the host file ~/.config/arkism/notify.json (0600). Each
// channel maps destination names to how to reach them; none of it is ever
// stored in Arkism.
//
//	{"telegram": {"default": {"bot_token": "...", "chat_id": "..."}},
//	 "slack":    {"default": {"webhook_url": "https://hooks.slack.com/..."}},
//	 "email":    {"default": {"host": "smtp.example.com", "port": "587", "user": "...",
//	                          "pass": "...", "from": "bot@example.com", "to": ["me@example.com"]}}}
type NotifyConfig struct {
	Telegram map[string]struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	} `json:"telegram"`
	Slack map[string]struct {
		WebhookURL string `json:"webhook_url"`
	} `json:"slack"`
	Email map[string]struct {
		Host string   `json:"host"`
		Port string   `json:"port"`
		User string   `json:"user"`
		Pass string   `json:"pass"`
		From string   `json:"from"`
		To   []string `json:"to"`
	} `json:"email"`
}

// pickDest prefers the runner scope's own destination ("<workspace>/<project>/<name>", saved from the
// desktop) over a host-wide hand-edited one of the same name.
// A name holding "/" is refused outright: it could name another scope's destination.
func pickDest[T any](m map[string]T, scope, to string) (T, bool) {
	if strings.Contains(to, "/") {
		var zero T
		return zero, false
	}
	if scope != "" {
		if d, ok := m[scope+"/"+to]; ok {
			return d, true
		}
	}
	d, ok := m[to]
	return d, ok
}

// Errors here never include URLs or addresses: a bot token or webhook URL
// would otherwise reach the run record.
func (e *Engine) notify(ctx context.Context, c map[string]string, input any) (any, error) {
	raw, err := readPrivateFile(e.NotifyFile)
	if err != nil {
		return nil, failf("notify", "notify.json unavailable (missing, or not chmod 600)")
	}
	var cfg NotifyConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, failf("notify", "notify.json unreadable")
	}
	to := cmp.Or(c["to"], "default")
	msg := textOf(input)
	if c["message"] != "" {
		msg = render(c["message"], input)
	}
	msg = clip(msg, 4000)
	switch c["channel"] {
	case "telegram":
		err = e.notifyTelegram(ctx, cfg, to, msg)
	case "slack":
		d, ok := pickDest(cfg.Slack, e.NotifyScope, to)
		if !ok || d.WebhookURL == "" {
			return nil, failf("notify", "no slack destination %q", to)
		}
		err = e.postJSON(ctx, d.WebhookURL, map[string]any{"text": msg})
	case "email":
		err = e.notifyEmail(ctx, cfg, c, to, msg)
	default:
		return nil, failf("bad_config", "channel must be telegram, slack or email")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"sent": true, "channel": c["channel"], "to": to}, nil
}

func (e *Engine) notifyTelegram(ctx context.Context, cfg NotifyConfig, to, msg string) error {
	d, ok := pickDest(cfg.Telegram, e.NotifyScope, to)
	if !ok || d.BotToken == "" {
		return failf("notify", "no telegram destination %q", to)
	}
	base := cmp.Or(e.TelegramAPI, "https://api.telegram.org")
	return e.postJSON(ctx, base+"/bot"+d.BotToken+"/sendMessage", map[string]any{"chat_id": d.ChatID, "text": msg})
}

func (e *Engine) notifyEmail(ctx context.Context, cfg NotifyConfig, c map[string]string, to, msg string) error {
	d, ok := pickDest(cfg.Email, e.NotifyScope, to)
	if !ok || d.Host == "" || d.From == "" || len(d.To) == 0 {
		return failf("notify", "no email destination %q", to)
	}
	port := d.Port
	if port == "" {
		port = "587"
	}
	var auth smtp.Auth
	if d.User != "" {
		auth = smtp.PlainAuth("", d.User, d.Pass, d.Host)
	}
	subject := c["subject"]
	if subject == "" {
		subject = "Arkism 알림"
	}
	body := "From: " + d.From + "\r\nTo: " + strings.Join(d.To, ", ") + "\r\nSubject: " + strings.ReplaceAll(subject, "\n", " ") +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + msg
	if retryMail(ctx, func() error {
		return sendMail(ctx, net.JoinHostPort(d.Host, port), d.Host, auth, d.From, d.To, []byte(body))
	}) != nil {
		return failf("notify", "email send failed")
	}
	return nil
}

// sendMail is smtp.SendMail bounded by a minute and by ctx, so a server that
// accepts the connection and then stalls cannot hold the node (or shutdown).
func sendMail(ctx context.Context, addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if err = c.Hello("localhost"); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err = c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	} else if tcp, _ := conn.RemoteAddr().(*net.TCPAddr); tcp == nil || !tcp.IP.IsLoopback() {
		return errors.New("smtp server " + host + " does not offer STARTTLS; refusing to send in clear text")
	}
	if auth != nil {
		if err = c.Auth(auth); err != nil {
			return err
		}
	}
	if err = c.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err = c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(msg); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Notifications are at-least-once: a lost acknowledgement may cause a duplicate.
// Permanent HTTP failures are never retried. No destination or response body is logged.
func (e *Engine) postJSON(ctx context.Context, target string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return failf("notify", "invalid message")
	}
	for attempt := 0; attempt < 3; attempt++ {
		delay := (200 * time.Millisecond << attempt) + time.Duration(mathrand.IntN(100))*time.Millisecond
		retry, after, err := e.postJSONAttempt(ctx, target, b)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
		delay, err = notificationRetryDelay(after, delay)
		if err != nil {
			return err
		}

		if !retry || attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return failf("notify", "send cancelled")
		case <-time.After(delay):
		}
	}
	return failf("notify", "send failed after 3 attempts")
}

// One attempt owns its response and deadline; retries never retain an open body.
func (e *Engine) postJSONAttempt(ctx context.Context, target string, body []byte) (bool, string, error) {
	op, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(op, "POST", target, bytes.NewReader(body))
	if err != nil {
		return false, "", failf("notify", "bad destination")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return true, "", nil
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	if status >= 200 && status < 300 {
		return false, "", nil
	}
	if status == 429 || status >= 500 {
		return true, resp.Header.Get("Retry-After"), nil
	}
	return false, "", failf("notify", "answered %d", status)
}

func notificationRetryDelay(header string, delay time.Duration) (time.Duration, error) {
	if n, err := strconv.Atoi(header); err == nil && n > 0 {
		if n > 30 {
			return 0, failf("notify", "retry deferred beyond notification budget")
		}
		return time.Duration(n) * time.Second, nil
	}
	if until, err := http.ParseTime(header); err == nil {
		wait := time.Until(until)
		if wait > 30*time.Second {
			return 0, failf("notify", "retry deferred beyond notification budget")
		}
		if wait > delay {
			return wait, nil
		}
	}
	return delay, nil
}

func retryMail(ctx context.Context, send func() error) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := send()
		if err == nil {
			return nil
		}
		var smtpErr *textproto.Error
		var netErr net.Error
		transient := errors.As(err, &smtpErr) && smtpErr.Code >= 400 && smtpErr.Code < 500 || errors.As(err, &netErr)
		if !transient || attempt == 2 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After((200 * time.Millisecond << attempt) + time.Duration(mathrand.IntN(100))*time.Millisecond):
		}
	}
	return errors.New("notification attempts exhausted")
}
