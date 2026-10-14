package crewrun

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
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
	to := c["to"]
	if to == "" {
		to = "default"
	}
	msg := textOf(input)
	if c["message"] != "" {
		msg = render(c["message"], input)
	}
	msg = clip(msg, 4000)
	switch c["channel"] {
	case "telegram":
		d, ok := cfg.Telegram[to]
		if !ok || d.BotToken == "" {
			return nil, failf("notify", "no telegram destination %q", to)
		}
		base := e.TelegramAPI
		if base == "" {
			base = "https://api.telegram.org"
		}
		err = e.postJSON(ctx, base+"/bot"+d.BotToken+"/sendMessage", map[string]any{"chat_id": d.ChatID, "text": msg})
	case "slack":
		d, ok := cfg.Slack[to]
		if !ok || d.WebhookURL == "" {
			return nil, failf("notify", "no slack destination %q", to)
		}
		err = e.postJSON(ctx, d.WebhookURL, map[string]any{"text": msg})
	case "email":
		d, ok := cfg.Email[to]
		if !ok || d.Host == "" || d.From == "" || len(d.To) == 0 {
			return nil, failf("notify", "no email destination %q", to)
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
		if sendMail(ctx, net.JoinHostPort(d.Host, port), d.Host, auth, d.From, d.To, []byte(body)) != nil {
			err = failf("notify", "email send failed")
		}
	default:
		return nil, failf("bad_config", "channel must be telegram, slack or email")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"sent": true, "channel": c["channel"], "to": to}, nil
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
	} else if !loopbackAddr(addr) {
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

func (e *Engine) postJSON(ctx context.Context, target string, body any) error {
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(b))
	if err != nil {
		return failf("notify", "bad destination")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return failf("notify", "send failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return failf("notify", "%s", fmt.Sprintf("answered %d", resp.StatusCode))
	}
	return nil
}
