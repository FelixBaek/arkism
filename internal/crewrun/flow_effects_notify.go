package crewrun

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The private URL and exact bytes are prepared together. After claim, sending
// uses these bytes directly; it never rereads configuration or rerenders input.
type preparedFlowNotify struct {
	request FlowEffectRequest
	url     string
	body    []byte
	channel string
	to      string
}

func effectDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

var telegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var telegramChat = regexp.MustCompile(`^(?:-?[0-9]+|@[A-Za-z0-9_]+)$`)

func (e *Engine) prepareFlowNotify(c map[string]string, input any) (preparedFlowNotify, error) {
	p := preparedFlowNotify{channel: c["channel"], to: cmp.Or(c["to"], "default")}
	if len(p.to) > 128 || strings.ContainsAny(p.to, "/{}\r\n") {
		return p, failf("effect_unsupported", "알림 목적지 별명은 고정된 이름이어야 해요")
	}
	msg := textOf(input)
	if c["message"] != "" {
		msg = render(c["message"], input)
	}
	if !utf8.ValidString(msg) || utf8.RuneCountInString(msg) > 1500 {
		return p, failf("effect_preview_limit", "승인할 알림 본문은 1500자 이하여야 해요; 내용을 자르지 않고 멈췄어요")
	}
	raw, err := readPrivateFile(e.NotifyFile)
	if err != nil {
		return p, failf("notify", "notify.json unavailable (missing, or not chmod 600)")
	}
	var cfg NotifyConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return p, failf("notify", "notify.json unreadable")
	}
	target, err := p.configure(cfg, e.NotifyScope, msg)
	if err != nil {
		return p, err
	}
	p.request.Adapter = "notify." + p.channel + ".v1"
	p.request.TargetHash = effectDigest([]byte(p.url + "\x00" + target))
	p.request.ArgsHash = effectDigest(append([]byte(p.request.Adapter+"\x00"+p.request.TargetHash+"\x00"), p.body...))
	p.request.Preview = fmt.Sprintf("외부 알림을 한 번 보낼까요?\n채널: %s\n목적지: %s (%s)\n대상 지문: %s\n\n%s", p.channel, p.to, target, p.request.TargetHash[:16], msg)
	if utf8.RuneCountInString(p.request.Preview) > 1900 {
		return p, failf("effect_preview_limit", "알림 승인 미리보기가 너무 길어요")
	}
	return p, nil
}

func (p *preparedFlowNotify) configure(cfg NotifyConfig, scope, msg string) (string, error) {
	switch p.channel {
	case "telegram":
		d, ok := pickDest(cfg.Telegram, scope, p.to)
		if !ok || !telegramToken.MatchString(d.BotToken) || !telegramChat.MatchString(d.ChatID) || len(d.ChatID) > 128 {
			return "", failf("notify", "valid Telegram destination required")
		}
		p.url = "https://api.telegram.org/bot" + d.BotToken + "/sendMessage"
		p.body, _ = json.Marshal(map[string]string{"chat_id": d.ChatID, "text": msg})
		// Chat ID belongs to the exact target identity, as well as the payload.
		return d.ChatID, nil
	case "slack":
		d, ok := pickDest(cfg.Slack, scope, p.to)
		if !ok || !validFlowSlackURL(d.WebhookURL) {
			return "", failf("notify", "a hooks.slack.com incoming webhook is required")
		}
		p.url = d.WebhookURL
		p.body, _ = json.Marshal(map[string]string{"text": msg})
		return "hooks.slack.com", nil
	default:
		return "", failf("effect_unsupported", "FLOW effects support existing Telegram and Slack notifications only")
	}
}

func validFlowSlackURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host != "hooks.slack.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	return len(parts) == 4 && parts[0] == "services" && parts[1] != "" && parts[2] != "" && parts[3] != "" && u.RawPath == ""
}

func (p preparedFlowNotify) result(id string) result {
	return result{out: map[string]any{"sent": true, "channel": p.channel, "to": p.to, "effect_id": id}}
}

func (e *Engine) sendFlowNotify(ctx context.Context, p preparedFlowNotify) FlowEffectReceipt {
	receipt := FlowEffectReceipt{Status: "unknown"}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return receipt
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(p.body))
	if err != nil {
		return receipt
	}
	req.Header.Set("Content-Type", "application/json")
	client := *e.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req) // Exactly one application dispatch; never retry an unknown write.
	if err != nil {
		return receipt
	}
	defer resp.Body.Close()
	receipt.HTTPStatus = resp.StatusCode
	b, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(b) > 65536 {
		return receipt
	}
	receipt.ResponseHash = effectDigest(b)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && flowNotifyAcknowledged(p.channel, b) {
		receipt.Status = "succeeded"
	}
	return receipt
}

func flowNotifyAcknowledged(channel string, b []byte) bool {
	if channel == "slack" {
		return strings.TrimSpace(string(b)) == "ok"
	}
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	return json.Unmarshal(b, &out) == nil && out.OK && out.Result.MessageID > 0
}
