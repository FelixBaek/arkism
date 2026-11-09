package memory

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/httpguard"
)

// Outbound webhooks: a person names an https endpoint and the memory events it
// wants. A state transition queues one delivery job per matching webhook in its
// own transaction (outbox); the worker signs and sends it with retries. A payload carries
// ids, kind, status, scope and times; memory text only when a person turned
// include_content on, and never text marked private, restricted or secret.

const (
	colWebhooks         = "webhooks"
	webhookKind         = "webhook"
	webhookDeliveryKind = "webhook_delivery"
	webhooksMax         = 20
	webhookSendTimeout  = 10 * time.Second
	webhookResponseMax  = 64 << 10
	// WebhookSignatureHeader carries "t=<unix seconds>,v1=<hex HMAC-SHA256(secret, t + "." + body)>".
	WebhookSignatureHeader = "X-Arkism-Signature"
)

// Memory events a webhook may subscribe to. Observation events join this list
// when observations exist; queueMemoryEvent needs no other change.
const (
	EventCandidateCreated = "candidate.created"
	EventMemoryApproved   = "memory.approved"
	EventMemoryRejected   = "memory.rejected"
	EventMemoryUpdated    = "memory.updated"
	EventMemoryDeleted    = "memory.deleted"
	// EventConsolidationCompleted: one observation consolidation run finished (not on dry runs).
	EventConsolidationCompleted = "consolidation.completed"
	// EventIngestCompleted: a knowledge document or uploaded text file was stored.
	EventIngestCompleted = "ingest.completed"
	// EventDefenseTriggered: a space defense rule blocked or redacted incoming text.
	EventDefenseTriggered = "defense.triggered"
	eventWebhookTest      = "webhook.test"
)

var webhookEvents = []string{EventCandidateCreated, EventMemoryApproved, EventMemoryRejected, EventMemoryUpdated, EventMemoryDeleted,
	EventConsolidationCompleted, EventIngestCompleted, EventDefenseTriggered}

type Webhook struct {
	URL            string   `json:"url" bson:"url"`
	Events         []string `json:"events" bson:"events"`
	IncludeContent bool     `json:"include_content" bson:"include_content"`
	Active         bool     `json:"active" bson:"active"`
	// SealedKey is the signing secret under Service.WebhookKey; it never leaves the server.
	SealedKey string `json:"-" bson:"sealed_key"`
	// Method is POST (empty), PUT or PATCH; TimeoutSeconds 0 means webhookSendTimeout.
	Method         string `json:"method,omitempty" bson:"method,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" bson:"timeout_seconds,omitempty"`
	// SealedHTTP holds the extra headers and query parameters (webhookHTTP) sealed like
	// the secret: they often carry tokens, so reads show only their names.
	SealedHTTP  string   `json:"-" bson:"sealed_http,omitempty"`
	HeaderNames []string `json:"header_names,omitempty" bson:"header_names,omitempty"`
	QueryNames  []string `json:"query_param_names,omitempty" bson:"query_param_names,omitempty"`
}

// webhookHTTP is the sealed part of a webhook's request shape. URL is set when
// the address itself carries a key (a chat app's token in the path); Webhook.URL
// then shows it masked.
type webhookHTTP struct {
	Headers map[string]string `json:"headers,omitempty"`
	Query   map[string]string `json:"query,omitempty"`
	URL     string            `json:"url,omitempty"`
}

// webhookShownURL splits raw into the URL reads show and the URL to seal ("" when nothing needs sealing).
// Each path part is masked alone, so the host and the other parts stay readable.
func webhookShownURL(raw string) (shown, sealed string) {
	if !containsSecret(raw) {
		return raw, ""
	}
	parts := strings.Split(raw, "/")
	for i, p := range parts {
		parts[i] = redactSecrets(p)
	}
	if shown = strings.Join(parts, "/"); shown == raw || containsSecret(shown) {
		shown = redactSecrets(raw)
	}
	return shown, raw
}

// WebhookSubject is what an event is about, without its text.
type WebhookSubject struct {
	Collection string    `json:"collection" bson:"collection"`
	ID         string    `json:"id" bson:"id"`
	Kind       string    `json:"kind" bson:"kind"`
	Status     string    `json:"status" bson:"status"`
	Revision   int64     `json:"revision" bson:"revision"`
	Scope      Scope     `json:"scope" bson:"scope"`
	CreatedAt  time.Time `json:"created_at,omitzero" bson:"created_at"`
	UpdatedAt  time.Time `json:"updated_at,omitzero" bson:"updated_at"`
}

// WebhookDelivery is one queued event for one webhook and its last attempt.
type WebhookDelivery struct {
	WebhookID  string          `json:"webhook_id" bson:"webhook_id"`
	Event      string          `json:"event" bson:"event"`
	OccurredAt time.Time       `json:"occurred_at" bson:"occurred_at"`
	Subject    *WebhookSubject `json:"subject,omitempty" bson:"subject,omitempty"`
	HTTPStatus int             `json:"http_status,omitempty" bson:"http_status,omitempty"`
	LatencyMS  int64           `json:"latency_ms,omitempty" bson:"latency_ms,omitempty"`
	Error      string          `json:"error,omitempty" bson:"error,omitempty"`
	AttemptAt  *time.Time      `json:"attempted_at,omitempty" bson:"attempted_at,omitempty"`
}

type WebhookInput struct {
	Scope          Scope             `json:"scope"`
	URL            string            `json:"url"`
	Events         []string          `json:"events"`
	Secret         string            `json:"secret"`
	IncludeContent bool              `json:"include_content,omitempty"`
	Active         *bool             `json:"active,omitempty"`
	Method         string            `json:"method,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	QueryParams    map[string]string `json:"query_params,omitempty"`
}

// WebhookUpdate changes only the fields it carries.
type WebhookUpdate struct {
	Scope            Scope    `json:"scope"`
	ExpectedRevision int64    `json:"expected_revision"`
	URL              *string  `json:"url,omitempty"`
	Events           []string `json:"events,omitempty"`
	Secret           *string  `json:"secret,omitempty"`
	IncludeContent   *bool    `json:"include_content,omitempty"`
	Active           *bool    `json:"active,omitempty"`
	Method           *string  `json:"method,omitempty"`
	TimeoutSeconds   *int     `json:"timeout_seconds,omitempty"`
	// Headers and QueryParams replace the stored set when present; an empty object clears it.
	Headers     map[string]string `json:"headers,omitempty"`
	QueryParams map[string]string `json:"query_params,omitempty"`
}

type WebhookView struct {
	ID             string   `json:"id"`
	Revision       int64    `json:"revision"`
	Scope          Scope    `json:"scope"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	IncludeContent bool     `json:"include_content"`
	Active         bool     `json:"active"`
	SecretSet      bool     `json:"secret_set"`
	Method         string   `json:"method"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	// HeaderNames and QueryParamNames list what is set; values are never returned.
	HeaderNames     []string  `json:"header_names"`
	QueryParamNames []string  `json:"query_param_names"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type WebhookList struct {
	Items []WebhookView `json:"items"`
}

type WebhookDeliveryView struct {
	ID       string          `json:"id"`
	Status   string          `json:"status"` // pending | running | retry | done | failed
	Attempts int             `json:"attempts"`
	NextAt   *time.Time      `json:"next_attempt_at,omitempty"`
	Delivery WebhookDelivery `json:"delivery"`
}

type WebhookDeliveryPage struct {
	Items      []WebhookDeliveryView `json:"items"`
	NextBefore string                `json:"next_before,omitempty"`
}

func webhookView(r Record) WebhookView {
	w := r.Webhook
	return WebhookView{ID: r.ID, Revision: r.Revision, Scope: r.Scope, URL: w.URL, Events: w.Events, IncludeContent: w.IncludeContent,
		Active: w.Active, SecretSet: w.SealedKey != "", Method: w.method(), TimeoutSeconds: int(w.timeout() / time.Second),
		HeaderNames: nonNil(w.HeaderNames), QueryParamNames: nonNil(w.QueryNames), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (w Webhook) method() string {
	if w.Method == "" {
		return http.MethodPost
	}
	return w.Method
}

func (w Webhook) timeout() time.Duration {
	if w.TimeoutSeconds == 0 {
		return webhookSendTimeout
	}
	return time.Duration(w.TimeoutSeconds) * time.Second
}

const (
	webhookTimeoutMax   = 30
	webhookHTTPPairsMax = 16
	webhookHTTPValueMax = 1024
)

var webhookHeaderName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// webhookReservedHeaders are set by the transport or the signature; a hook may not override them.
var webhookReservedHeaders = []string{"host", "content-length", "content-type", "transfer-encoding", "connection", "upgrade", "te",
	"trailer", "keep-alive", "expect", "cookie", "traceparent", "tracestate"}

func validWebhookMethod(m string) bool {
	return m == "" || m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch
}

func validWebhookTimeout(n int) bool {
	return n >= 0 && n <= webhookTimeoutMax
}

// validWebhookPairs bounds a header or query map: few pairs, short printable values.
func validWebhookPairs(m map[string]string, header bool) bool {
	if len(m) > webhookHTTPPairsMax {
		return false
	}
	for k, v := range m {
		if len(v) > webhookHTTPValueMax || strings.ContainsFunc(k+v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false
		}
		lk := strings.ToLower(k)
		if header && (!webhookHeaderName.MatchString(k) || slices.Contains(webhookReservedHeaders, lk) || strings.HasPrefix(lk, "proxy-") || strings.HasPrefix(lk, "x-arkism-")) ||
			!header && (k == "" || len(k) > 128) {
			return false
		}
	}
	return true
}

func webhookHTTPAAD(scope Scope, id string) []byte {
	return []byte("arkism/webhook-http/v1\x00" + structuredText(scope) + "\x00" + id)
}

// sealWebhookHTTP stores headers and query values sealed and only their sorted names in the clear.
func sealWebhookHTTP(key []byte, w *Webhook, h webhookHTTP, scope Scope, id string) error {
	w.SealedHTTP, w.HeaderNames, w.QueryNames = "", webhookNames(h.Headers), webhookNames(h.Query)
	if len(h.Headers) == 0 && len(h.Query) == 0 && h.URL == "" {
		return nil
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	w.SealedHTTP, err = sealAEAD(key, raw, webhookHTTPAAD(scope, id))
	return err
}

func webhookNames(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	return catalogSortedKeys(m)
}

func (s *Service) openWebhookHTTP(w *Webhook, scope Scope, id string) (webhookHTTP, error) {
	var h webhookHTTP
	if w.SealedHTTP == "" {
		return h, nil
	}
	raw, err := openAEAD(s.WebhookKey, w.SealedHTTP, webhookHTTPAAD(scope, id), errWebhookSecret)
	if err != nil {
		return h, err
	}
	return h, json.Unmarshal(raw, &h)
}

// webhookRequestURL adds the hook's query parameters to its checked URL. The host
// does not change, so the address checks made on the URL (and at connect) still hold.
func webhookRequestURL(raw string, query map[string]string) (string, error) {
	if len(query) == 0 {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalid
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validWebhookShape(method string, timeout int, headers, query map[string]string) bool {
	return validWebhookMethod(method) && validWebhookTimeout(timeout) && validWebhookPairs(headers, true) && validWebhookPairs(query, false)
}

func webhookDeliveryView(r Record) WebhookDeliveryView {
	return WebhookDeliveryView{ID: r.ID, Status: r.Status, Attempts: r.Attempts, NextAt: r.RunAt, Delivery: *r.WebhookDelivery}
}

// checkWebhookURL: https, or http on loopback; no credentials or fragment; an IP
// literal must pass the outbound address check. Names are checked again after
// DNS at every connection (httpguard.OutboundClient).
func checkWebhookURL(raw string, allowPrivate bool) error {
	if len(raw) > 2048 || validateEndpoint(raw) != nil {
		return ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return ErrInvalid
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if httpguard.CheckOutboundIP(ip, allowPrivate) != nil {
			return ErrInvalid
		}
	} else if !allowPrivate && isLoopbackHost(host) {
		return ErrInvalid
	}
	return nil
}

func validWebhookEvents(events []string) bool {
	if len(events) == 0 || len(events) > len(webhookEvents) {
		return false
	}
	for i, e := range events {
		if !slices.Contains(webhookEvents, e) || slices.Contains(events[:i], e) {
			return false
		}
	}
	return true
}

func validWebhookSecret(s string) bool {
	return len(s) >= 16 && len(s) <= 256 && strings.TrimSpace(s) == s
}

func webhookSecretAAD(scope Scope, id string) []byte {
	return []byte("arkism/webhook-secret/v1\x00" + structuredText(scope) + "\x00" + id)
}

var errWebhookSecret = errors.New("webhook_secret_unreadable")

func (s *Service) humanWebhooks(ctx context.Context, token string, scope Scope, id string) (Decision, error) {
	if !validScope(scope) || id != "" && !validRunLabel(id, 256) {
		return Decision{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colWebhooks, scope, id, "")
	if err != nil {
		return d, err
	}
	if d.Role != roleHuman {
		return d, ErrDenied
	}
	return d, nil
}

func (s *Service) webhookStore() (scopedRowStore, error) {
	store, ok := s.Repo.(scopedRowStore)
	if !ok {
		return nil, ErrUnavailable
	}
	return store, nil
}

func (s *Service) CreateWebhook(ctx context.Context, token string, in WebhookInput) (WebhookView, error) {
	if checkWebhookURL(in.URL, s.WebhookAllowPrivate) != nil || !validWebhookEvents(in.Events) || !validWebhookSecret(in.Secret) ||
		!validWebhookShape(in.Method, in.TimeoutSeconds, in.Headers, in.QueryParams) {
		return WebhookView{}, ErrInvalid
	}
	d, err := s.humanWebhooks(ctx, token, in.Scope, "")
	if err != nil {
		return WebhookView{}, err
	}
	store, err := s.webhookStore()
	if err != nil {
		return WebhookView{}, err
	}
	have, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colWebhooks, Kind: webhookKind, Scope: in.Scope, Limit: webhooksMax})
	if err != nil {
		return WebhookView{}, err
	}
	if len(have) >= webhooksMax {
		return WebhookView{}, ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := timeOrderedID(now.UnixMilli())
	sealed, err := sealAEAD(s.WebhookKey, []byte(in.Secret), webhookSecretAAD(in.Scope, id))
	if err != nil {
		return WebhookView{}, err
	}
	w := Webhook{Events: slices.Clone(in.Events), IncludeContent: in.IncludeContent, Active: in.Active == nil || *in.Active, SealedKey: sealed,
		Method: in.Method, TimeoutSeconds: in.TimeoutSeconds}
	h := webhookHTTP{Headers: in.Headers, Query: in.QueryParams}
	w.URL, h.URL = webhookShownURL(in.URL)
	if err = sealWebhookHTTP(s.WebhookKey, &w, h, in.Scope, id); err != nil {
		return WebhookView{}, err
	}
	r := Record{ID: id, Scope: in.Scope, Kind: webhookKind, Status: statusActive, Revision: 1, Webhook: &w,
		CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Insert(ctx, colWebhooks, r); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colWebhooks, id)
	})
	if err != nil {
		return WebhookView{}, err
	}
	return webhookView(r), nil
}

func (s *Service) ListWebhooks(ctx context.Context, token string, scope Scope) (WebhookList, error) {
	if _, err := s.humanWebhooks(ctx, token, scope, ""); err != nil {
		return WebhookList{}, err
	}
	store, err := s.webhookStore()
	if err != nil {
		return WebhookList{}, err
	}
	rows, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colWebhooks, Kind: webhookKind, Scope: scope, Limit: webhooksMax})
	if err != nil {
		return WebhookList{}, err
	}
	out := WebhookList{Items: []WebhookView{}}
	for _, r := range rows {
		if r.Webhook != nil {
			out.Items = append(out.Items, webhookView(r))
		}
	}
	return out, nil
}

func (s *Service) GetWebhook(ctx context.Context, token string, scope Scope, id string) (WebhookView, error) {
	if _, err := s.humanWebhooks(ctx, token, scope, id); err != nil {
		return WebhookView{}, err
	}
	r, err := s.Repo.Get(ctx, colWebhooks, scope, id)
	if err != nil {
		return WebhookView{}, err
	}
	if r.Webhook == nil {
		return WebhookView{}, ErrNotFound
	}
	return webhookView(r), nil
}

// validWebhookUpdate checks every field the update resends; omitted fields stay as stored.
func (s *Service) validWebhookUpdate(in WebhookUpdate) bool {
	return !(in.ExpectedRevision < 1 || in.URL != nil && checkWebhookURL(*in.URL, s.WebhookAllowPrivate) != nil ||
		in.Events != nil && !validWebhookEvents(in.Events) || in.Secret != nil && !validWebhookSecret(*in.Secret) ||
		in.Method != nil && !validWebhookMethod(*in.Method) || in.TimeoutSeconds != nil && !validWebhookTimeout(*in.TimeoutSeconds) ||
		!validWebhookPairs(in.Headers, true) || !validWebhookPairs(in.QueryParams, false))
}

func (s *Service) UpdateWebhook(ctx context.Context, token, id string, in WebhookUpdate) (WebhookView, error) {
	if !s.validWebhookUpdate(in) {
		return WebhookView{}, ErrInvalid
	}
	d, err := s.humanWebhooks(ctx, token, in.Scope, id)
	if err != nil {
		return WebhookView{}, err
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colWebhooks, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Webhook == nil {
			return ErrNotFound
		}
		if r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		w := *r.Webhook
		if err = s.applyWebhookUpdate(&w, in, id); err != nil {
			return err
		}
		prev := r.Revision
		r.Webhook, r.Revision, r.UpdatedAt = &w, r.Revision+1, time.Now().UTC().Truncate(time.Millisecond)
		if err = tx.Replace(ctx, colWebhooks, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, "update:"+colWebhooks, id)
	})
	if err != nil {
		return WebhookView{}, err
	}
	return webhookView(out), nil
}

// webhookOrigin is scheme://host:port of raw, or raw itself when it does not parse.
func webhookOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func (s *Service) applyWebhookUpdate(w *Webhook, in WebhookUpdate, id string) error {
	if in.Method != nil {
		w.Method = *in.Method
	}
	if in.TimeoutSeconds != nil {
		w.TimeoutSeconds = *in.TimeoutSeconds
	}
	if in.Headers != nil || in.QueryParams != nil || in.URL != nil {
		if err := s.applyWebhookHTTPUpdate(w, in, id); err != nil {
			return err
		}
	}
	if in.Events != nil {
		w.Events = slices.Clone(in.Events)
	}
	if in.IncludeContent != nil {
		w.IncludeContent = *in.IncludeContent
	}
	if in.Active != nil {
		w.Active = *in.Active
	}
	if in.Secret != nil {
		sealed, err := sealAEAD(s.WebhookKey, []byte(*in.Secret), webhookSecretAAD(in.Scope, id))
		if err != nil {
			return err
		}
		w.SealedKey = sealed
	}
	return nil
}

// applyWebhookHTTPUpdate reseals the URL, headers and query parameters the update changes.
func (s *Service) applyWebhookHTTPUpdate(w *Webhook, in WebhookUpdate, id string) error {
	h, err := s.openWebhookHTTP(w, in.Scope, id)
	if err != nil {
		return err
	}
	// A new origin must not inherit sealed credentials meant for the old one:
	// parts the same update does not resend are dropped.
	moved := in.URL != nil && webhookOrigin(*in.URL) != webhookOrigin(cmp.Or(h.URL, w.URL))
	if in.Headers != nil || moved {
		h.Headers = in.Headers
	}
	if in.QueryParams != nil || moved {
		h.Query = in.QueryParams
	}
	if in.URL != nil {
		w.URL, h.URL = webhookShownURL(*in.URL)
	}
	return sealWebhookHTTP(s.WebhookKey, w, h, in.Scope, id)
}

// DeleteWebhook removes the webhook; its queued deliveries end as failed "webhook_gone".
func (s *Service) DeleteWebhook(ctx context.Context, token string, scope Scope, id string) error {
	d, err := s.humanWebhooks(ctx, token, scope, id)
	if err != nil {
		return err
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colWebhooks, scope, id)
		if err != nil {
			return err
		}
		if r.Webhook == nil {
			return ErrNotFound
		}
		if err = tx.Delete(ctx, colWebhooks, scope, id, r.Revision); err != nil {
			return err
		}
		return audit(ctx, tx, scope, d.PrincipalID, "delete:"+colWebhooks, id)
	})
}

func (s *Service) ListWebhookDeliveries(ctx context.Context, token string, scope Scope, id, before string, limit int) (WebhookDeliveryPage, error) {
	if limit < 0 || limit > 100 || before != "" && !validRunLabel(before, 64) {
		return WebhookDeliveryPage{}, ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if _, err := s.GetWebhook(ctx, token, scope, id); err != nil {
		return WebhookDeliveryPage{}, err
	}
	store, err := s.webhookStore()
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	rows, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colJobs, Kind: webhookDeliveryKind, Scope: scope,
		Path: []string{"webhook_delivery", "webhook_id"}, Value: id, Before: before, Limit: limit + 1})
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	out := WebhookDeliveryPage{Items: []WebhookDeliveryView{}}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextBefore = rows[limit-1].ID
	}
	for _, r := range rows {
		if r.WebhookDelivery != nil {
			out.Items = append(out.Items, webhookDeliveryView(r))
		}
	}
	return out, nil
}

// TestWebhook queues a "webhook.test" delivery (even for an inactive webhook).
func (s *Service) TestWebhook(ctx context.Context, token string, scope Scope, id string) (WebhookDeliveryView, error) {
	if _, err := s.GetWebhook(ctx, token, scope, id); err != nil {
		return WebhookDeliveryView{}, err
	}
	job := webhookDeliveryJob(scope, id, eventWebhookTest, nil, time.Now())
	if err := s.Repo.Insert(ctx, colJobs, job); err != nil {
		return WebhookDeliveryView{}, err
	}
	return webhookDeliveryView(job), nil
}

func webhookDeliveryJob(scope Scope, webhookID, event string, subject *WebhookSubject, at time.Time) Record {
	now := at.UTC().Truncate(time.Millisecond)
	return Record{ID: timeOrderedID(now.UnixMilli()), Scope: scope, Kind: webhookDeliveryKind, Status: JobStatusPending, Revision: 1,
		CreatedAt: now, UpdatedAt: now, WebhookDelivery: &WebhookDelivery{WebhookID: webhookID, Event: event, OccurredAt: now, Subject: subject}}
}

// queueMemoryEvent queues, inside the transaction that makes the change, one
// delivery job per active webhook subscribed to event in the record's scope (and
// its agent-less scope), so a committed change always has its deliveries and a
// rolled-back one has none. An empty event or a store without webhooks queues nothing.
func queueMemoryEvent(ctx context.Context, tx Tx, event, col string, r Record) error {
	store, ok := tx.(scopedRowStore)
	if event == "" || !ok || r.ID == "" || !validScope(r.Scope) {
		return nil
	}
	subject := &WebhookSubject{Collection: col, ID: r.ID, Kind: r.Kind, Status: r.Status, Revision: r.Revision, Scope: r.Scope, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	scopes := []Scope{r.Scope}
	if r.Scope.AgentID != "" {
		scopes = append(scopes, Scope{WorkspaceID: r.Scope.WorkspaceID, ProjectID: r.Scope.ProjectID, UserID: r.Scope.UserID})
	}
	now := time.Now()
	for _, sc := range scopes {
		hooks, err := store.ListScopedRows(ctx, scopedRowQuery{Collection: colWebhooks, Kind: webhookKind, Scope: sc, Limit: webhooksMax})
		if err != nil {
			return err
		}
		for _, h := range hooks {
			if h.Webhook != nil && h.Webhook.Active && slices.Contains(h.Webhook.Events, event) {
				if err = tx.Insert(ctx, colJobs, webhookDeliveryJob(sc, h.ID, event, subject, now)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// queueEvent queues event for r in a transaction of its own, for work that commits
// in several transactions (a consolidation run, a defense check, a file create).
// Delivery is best effort here: the work already committed, so a failed queue is
// not reported as a failure of that work.
func (s *Service) queueEvent(ctx context.Context, event, col string, r Record) {
	_ = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error { return queueMemoryEvent(ctx, tx, event, col, r) })
}

// eventSubject is the subject record of an event that has no row of its own.
func eventSubject(scope Scope, kind, status string) Record {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return Record{ID: timeOrderedID(now.UnixMilli()), Scope: scope, Kind: kind, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now}
}

// createdEvent names the webhook event of a created record ("" for none).
func createdEvent(col string) string {
	if col == "memory_candidates" {
		return EventCandidateCreated
	}
	return ""
}

// queueTransitionEvent queues the webhook event of a transition (writeTransition),
// if any; an approved candidate is reported as the memory it became.
func queueTransitionEvent(ctx context.Context, tx Tx, col, status string, result Record) error {
	subject := colMemories
	if col == "memory_candidates" && status != "approved" {
		subject = col
	}
	return queueMemoryEvent(ctx, tx, transitionEvent(col, status), subject, result)
}

// transitionEvent names the webhook event of a committed transition ("" for none).
func transitionEvent(col, status string) string {
	switch {
	case col == "memory_candidates" && status == "approved":
		return EventMemoryApproved
	case (col == "memory_candidates" || col == colMemories) && status == "rejected":
		return EventMemoryRejected
	case col == colMemories:
		return EventMemoryUpdated
	}
	return ""
}

// webhookContent is the text a webhook may carry for its subject: none unless the
// record still exists unerased and is not private, restricted, private-marked or secret.
func (s *Service) webhookContent(ctx context.Context, subject *WebhookSubject) (string, bool) {
	if subject == nil || subject.Collection != colMemories && subject.Collection != "memory_candidates" {
		return "", false
	}
	r, err := s.Repo.Get(ctx, subject.Collection, subject.Scope, subject.ID)
	if err != nil || r.ContentErased || r.PrivacyQuarantined || r.Sensitivity == "private" || r.Sensitivity == "restricted" {
		return "", false
	}
	text := r.Text
	if containsSecret(recordContent(r)) || redactSessionPrivate(text) != text {
		return "", false
	}
	return text, true
}

type webhookPayload struct {
	ID         string          `json:"id"`
	Event      string          `json:"event"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       *WebhookSubject `json:"data,omitempty"`
	Content    *string         `json:"content,omitempty"`
}

// deliverWebhook sends one claimed delivery job. It records the attempt on the
// job and reports whether the job is finished; a failed send is retried by
// FinishJob with exponential backoff up to maxJobAttempts.
func (s *Service) deliverWebhook(ctx context.Context, job *Record) (bool, error) {
	if job.WebhookDelivery == nil {
		return true, ErrInvalid
	}
	d := *job.WebhookDelivery
	h, err := s.Repo.Get(ctx, colWebhooks, job.Scope, d.WebhookID)
	if errors.Is(err, ErrNotFound) || err == nil && (h.Webhook == nil || !h.Webhook.Active && d.Event != eventWebhookTest) {
		d.Error = "webhook_gone"
		return false, s.recordWebhookAttempt(ctx, job, d)
	}
	if err != nil {
		return false, err
	}
	secret, err := openAEAD(s.WebhookKey, h.Webhook.SealedKey, webhookSecretAAD(h.Scope, h.ID), errWebhookSecret)
	extra, httpErr := s.openWebhookHTTP(h.Webhook, h.Scope, h.ID)
	target, urlErr := webhookRequestURL(cmp.Or(extra.URL, h.Webhook.URL), extra.Query)
	if err != nil || httpErr != nil || urlErr != nil {
		d.Error = "secret_unavailable"
		return false, s.recordWebhookAttempt(ctx, job, d)
	}
	payload := webhookPayload{ID: job.ID, Event: d.Event, OccurredAt: d.OccurredAt, Data: d.Subject}
	if h.Webhook.IncludeContent {
		if text, ok := s.webhookContent(ctx, d.Subject); ok {
			payload.Content = &text
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return true, ErrInvalid
	}
	header := http.Header{}
	for k, v := range extra.Headers {
		header.Set(k, v)
	}
	header.Set(WebhookSignatureHeader, SignWebhook(secret, time.Now(), body))
	header.Set("X-Arkism-Event", d.Event)
	header.Set("X-Arkism-Delivery", job.ID)
	start := time.Now()
	_, status, err := sendBody(ctx, httpguard.OutboundClient(s.WebhookAllowPrivate, h.Webhook.timeout(), nil), h.Webhook.method(), target, header, body, webhookResponseMax)
	d.HTTPStatus, d.LatencyMS, d.Error = status, time.Since(start).Milliseconds(), ""
	ok := err == nil && status >= 200 && status < 300
	switch {
	case err != nil:
		d.Error = webhookErrorCode(err)
	case !ok:
		d.Error = "http_" + strconv.Itoa(status)
	}
	return ok, s.recordWebhookAttempt(ctx, job, d)
}

// webhookTerminal: a gone webhook or a client error the receiver will repeat
// (4xx other than 408 timeout and 429 rate limit) is not retried.
func webhookTerminal(d WebhookDelivery) bool {
	return d.Error == "webhook_gone" || d.HTTPStatus >= 400 && d.HTTPStatus < 500 && d.HTTPStatus != http.StatusRequestTimeout && d.HTTPStatus != http.StatusTooManyRequests
}

func webhookErrorCode(err error) string {
	var blocked *httpguard.BlockedAddress
	if errors.As(err, &blocked) {
		return "blocked_address"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return captureErrorCode(err)
}

// recordWebhookAttempt writes the attempt result onto the leased job (advancing
// job.Revision so FinishJob still matches). A terminal outcome ends the job as failed.
func (s *Service) recordWebhookAttempt(ctx context.Context, job *Record, d WebhookDelivery) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	d.AttemptAt = &now
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, job.Scope, job.ID)
		if err != nil {
			return err
		}
		if cur.Revision != job.Revision || cur.LeaseToken != job.LeaseToken {
			return ErrConflict
		}
		prev := cur.Revision
		cur.WebhookDelivery, cur.Revision, cur.UpdatedAt = &d, cur.Revision+1, now
		if webhookTerminal(d) {
			cur.Attempts = maxJobAttempts // nothing left to retry
		}
		if err = tx.Replace(ctx, colJobs, cur, prev); err != nil {
			return err
		}
		*job = cur
		return nil
	})
}

// SignWebhook returns the signature header value for body at time t.
func SignWebhook(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// VerifyWebhook checks a signature header against body: the HMAC must match and
// the timestamp lie within tolerance of now (replay window).
func VerifyWebhook(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	ts, sig, ok := strings.Cut(header, ",v1=")
	ts, found := strings.CutPrefix(ts, "t=")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if !ok || !found || err != nil {
		return false
	}
	at := time.Unix(sec, 0)
	if at.Before(now.Add(-tolerance)) || at.After(now.Add(tolerance)) {
		return false
	}
	return hmac.Equal([]byte(SignWebhook(secret, at, body)), []byte("t="+ts+",v1="+sig))
}
