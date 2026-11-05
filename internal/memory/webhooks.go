package memory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
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
	eventWebhookTest      = "webhook.test"
)

var webhookEvents = []string{EventCandidateCreated, EventMemoryApproved, EventMemoryRejected, EventMemoryUpdated, EventMemoryDeleted}

type Webhook struct {
	URL            string   `json:"url" bson:"url"`
	Events         []string `json:"events" bson:"events"`
	IncludeContent bool     `json:"include_content" bson:"include_content"`
	Active         bool     `json:"active" bson:"active"`
	// SealedKey is the signing secret under Service.WebhookKey; it never leaves the server.
	SealedKey string `json:"-" bson:"sealed_key"`
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
	Scope          Scope    `json:"scope"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	Secret         string   `json:"secret"`
	IncludeContent bool     `json:"include_content,omitempty"`
	Active         *bool    `json:"active,omitempty"`
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
}

type WebhookView struct {
	ID             string    `json:"id"`
	Revision       int64     `json:"revision"`
	Scope          Scope     `json:"scope"`
	URL            string    `json:"url"`
	Events         []string  `json:"events"`
	IncludeContent bool      `json:"include_content"`
	Active         bool      `json:"active"`
	SecretSet      bool      `json:"secret_set"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
		Active: w.Active, SecretSet: w.SealedKey != "", CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func webhookDeliveryView(r Record) WebhookDeliveryView {
	return WebhookDeliveryView{ID: r.ID, Status: r.Status, Attempts: r.Attempts, NextAt: r.RunAt, Delivery: *r.WebhookDelivery}
}

// checkWebhookURL: https, or http on loopback; no credentials or fragment; an IP
// literal must pass the outbound address check. Names are checked again after
// DNS at every connection (httpguard.OutboundClient).
func checkWebhookURL(raw string, allowPrivate bool) error {
	if len(raw) > 2048 || validateEndpoint(raw) != nil || containsSecret(raw) {
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
	if checkWebhookURL(in.URL, s.WebhookAllowPrivate) != nil || !validWebhookEvents(in.Events) || !validWebhookSecret(in.Secret) {
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
	w := Webhook{URL: in.URL, Events: slices.Clone(in.Events), IncludeContent: in.IncludeContent, Active: in.Active == nil || *in.Active, SealedKey: sealed}
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

func (s *Service) UpdateWebhook(ctx context.Context, token, id string, in WebhookUpdate) (WebhookView, error) {
	if in.ExpectedRevision < 1 || in.URL != nil && checkWebhookURL(*in.URL, s.WebhookAllowPrivate) != nil ||
		in.Events != nil && !validWebhookEvents(in.Events) || in.Secret != nil && !validWebhookSecret(*in.Secret) {
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
		if err = applyWebhookUpdate(s.WebhookKey, &w, in, id); err != nil {
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

func applyWebhookUpdate(key []byte, w *Webhook, in WebhookUpdate, id string) error {
	if in.URL != nil {
		w.URL = *in.URL
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
		sealed, err := sealAEAD(key, []byte(*in.Secret), webhookSecretAAD(in.Scope, id))
		if err != nil {
			return err
		}
		w.SealedKey = sealed
	}
	return nil
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
	if err != nil {
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
	header.Set(WebhookSignatureHeader, SignWebhook(secret, time.Now(), body))
	header.Set("X-Arkism-Event", d.Event)
	header.Set("X-Arkism-Delivery", job.ID)
	start := time.Now()
	_, status, err := postBody(ctx, httpguard.OutboundClient(s.WebhookAllowPrivate, webhookSendTimeout, nil), h.Webhook.URL, header, body, webhookResponseMax)
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
