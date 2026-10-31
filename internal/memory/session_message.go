package memory

// 세션 받은편지함.
//
// 실행 중인 다른 에이전트 세션(Claude Code, Codex, ZCode, Hermes 등)에 짧은
// 메시지를 남긴다. 받는 세션은 다음 턴의 훅이나 MCP 도구로 꺼내 읽는다.
// 메시지는 언제나 data_only_untrusted 자료이며 지시·승인 권한이 없다.
//
// 규칙:
//   - send는 write:session_messages, take는 read:session_messages 권한을 본다.
//   - 같은 scope/event_id/principal 재전송은 같은 메시지를 답하고, 다른 내용이면 409다.
//   - take는 한 트랜잭션에서 꺼낸 메시지를 delivered로 바꾸고 본문을 지운다.
//     보낸 세션·받는 세션·시간 메타데이터만 남는다.

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	colSessionMessages = "session_messages"
	kindSessionMessage = "session_message"

	sessionMessageStatusDelivered = "delivered"
	// sessionMessageMaxText bounds one message (characters), so a hook can show two per turn.
	sessionMessageMaxText   = 2000
	sessionMessageTakeLimit = 20
)

// Claude/Codex UUIDs, Hermes run ids and the MCP fallback "mcp:<thread>".
var sessionMessageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// SessionMessage is JSON-silent: bodies leave only through /v1/session-messages/take.
type SessionMessage struct {
	FromSession   string     `json:"-" bson:"from_session"`
	FromOrigin    string     `json:"-" bson:"from_origin,omitempty"`
	TargetSession string     `json:"-" bson:"target_session"`
	Text          string     `json:"-" bson:"text,omitempty"`
	RequestHash   string     `json:"-" bson:"request_hash"`
	DeliveredAt   *time.Time `json:"-" bson:"delivered_at,omitempty"`
}

type SessionMessageSendInput struct {
	Scope         Scope  `json:"scope"`
	EventID       string `json:"event_id"`
	TargetSession string `json:"target_session"`
	FromSession   string `json:"from_session"`
	FromOrigin    string `json:"from_origin,omitempty"`
	Text          string `json:"text"`
}

type SessionMessageSendOutput struct {
	ID            string    `json:"id"`
	TargetSession string    `json:"target_session"`
	CreatedAt     time.Time `json:"created_at"`
	Authority     string    `json:"authority"`
}

type SessionMessageTakeInput struct {
	Scope   Scope  `json:"scope"`
	Session string `json:"session"`
	Limit   int    `json:"limit,omitempty"`
}

type SessionMessageItem struct {
	ID            string    `json:"id"`
	FromSession   string    `json:"from_session"`
	FromOrigin    string    `json:"from_origin,omitempty"`
	TargetSession string    `json:"target_session"`
	Text          string    `json:"text"`
	CreatedAt     time.Time `json:"created_at"`
}

type SessionMessageTakeOutput struct {
	Items     []SessionMessageItem `json:"items"`
	More      bool                 `json:"more"`
	Authority string               `json:"authority"`
}

type sessionMessageRowsReader interface {
	SessionMessageRows(context.Context, Scope, string, int) ([]Record, error)
}

func isSessionMessage(r Record) bool {
	return r.Kind == kindSessionMessage && r.SessionMessage != nil
}

// SendSessionMessage leaves one message for another session.
func (s *Service) SendSessionMessage(ctx context.Context, token string, in SessionMessageSendInput) (SessionMessageSendOutput, error) {
	if s.Repo == nil {
		return SessionMessageSendOutput{}, ErrUnavailable
	}
	in.Text = strings.TrimSpace(in.Text)
	if !validScope(in.Scope) || !workCheckpointIDPattern.MatchString(in.EventID) || !sessionMessageIDPattern.MatchString(in.TargetSession) ||
		!sessionMessageIDPattern.MatchString(in.FromSession) || in.FromOrigin != "" && !sessionMessageIDPattern.MatchString(in.FromOrigin) ||
		in.Text == "" || !utf8.ValidString(in.Text) || utf8.RuneCountInString(in.Text) > sessionMessageMaxText {
		return SessionMessageSendOutput{}, ErrInvalid
	}
	if containsSecret(in.Text) || containsSecret(structuredText(in.Scope)) {
		return SessionMessageSendOutput{}, ErrSecretDetected
	}
	d, err := s.authorize(ctx, token, "write:"+colSessionMessages, in.Scope, in.TargetSession, in.Text)
	if err != nil {
		return SessionMessageSendOutput{}, err
	}
	if d.Role != roleHuman && d.Role != roleService && d.Role != roleAgent {
		return SessionMessageSendOutput{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, d)
	requestHash := ContentHash(structuredText(in))
	id := ContentHash(strings.Join([]string{structuredText(in.Scope), in.EventID, d.PrincipalID}, "\n"))
	now := time.Now().UTC().Truncate(time.Millisecond)
	var out SessionMessageSendOutput
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		previous, err := tx.Get(ctx, colSessionMessages, in.Scope, id)
		switch {
		case err == nil:
			if !isSessionMessage(previous) || previous.SessionMessage.RequestHash != requestHash {
				return ErrConflict
			}
			out = SessionMessageSendOutput{ID: id, TargetSession: previous.SessionMessage.TargetSession, CreatedAt: previous.CreatedAt, Authority: AuthorityDataOnly}
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if err := tx.Insert(ctx, colSessionMessages, Record{
			ID: id, Scope: in.Scope, Kind: kindSessionMessage, Status: statusActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			SessionMessage: &SessionMessage{FromSession: in.FromSession, FromOrigin: in.FromOrigin, TargetSession: in.TargetSession, Text: in.Text, RequestHash: requestHash},
		}); err != nil {
			return err
		}
		out = SessionMessageSendOutput{ID: id, TargetSession: in.TargetSession, CreatedAt: now, Authority: AuthorityDataOnly}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colSessionMessages, id)
	})
	if err != nil {
		return SessionMessageSendOutput{}, err
	}
	return out, nil
}

// TakeSessionMessages hands over the session's waiting messages once: each is
// marked delivered and its body is erased in the same transaction.
func (s *Service) TakeSessionMessages(ctx context.Context, token string, in SessionMessageTakeInput) (SessionMessageTakeOutput, error) {
	out := SessionMessageTakeOutput{Items: []SessionMessageItem{}, Authority: AuthorityDataOnly}
	if s.Repo == nil {
		return out, ErrUnavailable
	}
	if in.Limit == 0 {
		in.Limit = sessionMessageTakeLimit
	}
	if !validScope(in.Scope) || !sessionMessageIDPattern.MatchString(in.Session) || in.Limit < 1 || in.Limit > sessionMessageTakeLimit {
		return out, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:"+colSessionMessages, in.Scope, in.Session, "")
	if err != nil {
		return out, err
	}
	store, ok := s.Repo.(sessionMessageRowsReader)
	if !ok {
		return out, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, d)
	rows, err := store.SessionMessageRows(ctx, in.Scope, in.Session, in.Limit+1)
	if err != nil {
		return out, err
	}
	if len(rows) > in.Limit {
		rows, out.More = rows[:in.Limit], true
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Items = []SessionMessageItem{}
		for _, row := range rows {
			current, err := tx.Get(ctx, colSessionMessages, in.Scope, row.ID)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			// Another turn took it first, or the row belongs to someone else.
			if !isSessionMessage(current) || !live(current) || current.SessionMessage.TargetSession != in.Session {
				continue
			}
			m := *current.SessionMessage
			out.Items = append(out.Items, SessionMessageItem{ID: current.ID, FromSession: m.FromSession, FromOrigin: m.FromOrigin, TargetSession: m.TargetSession, Text: m.Text, CreatedAt: current.CreatedAt})
			next := current
			delivered := m
			delivered.Text, delivered.DeliveredAt = "", &now
			next.SessionMessage = &delivered
			next.Status, next.Revision, next.UpdatedAt = sessionMessageStatusDelivered, current.Revision+1, now
			if err := tx.Replace(ctx, colSessionMessages, next, current.Revision); err != nil {
				return err
			}
			if err := audit(ctx, tx, in.Scope, d.PrincipalID, "read:"+colSessionMessages+":take", current.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SessionMessageTakeOutput{Items: []SessionMessageItem{}, Authority: AuthorityDataOnly}, err
	}
	return out, nil
}

var _ sessionMessageRowsReader = (*Postgres)(nil)

// SessionMessageRows lists one session's waiting messages, oldest first.
func (p *Postgres) SessionMessageRows(ctx context.Context, scope Scope, session string, limit int) ([]Record, error) {
	if checkScope(scope) != nil || !sessionMessageIDPattern.MatchString(session) || limit < 1 || limit > sessionMessageTakeLimit+1 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colSessionMessages, scope, `status='active' AND doc->'session_message'->>'target_session'=$6`, []any{session}, "updated_at ASC, id ASC", limit)
}

func sessionMessageRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/session-messages/send", func(w http.ResponseWriter, r *http.Request) {
		var in SessionMessageSendInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.SendSessionMessage(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/session-messages/take", func(w http.ResponseWriter, r *http.Request) {
		var in SessionMessageTakeInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.TakeSessionMessages(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}
