package memory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

type sessionMessageFake struct{ *fakeRepo }

func (r *sessionMessageFake) SessionMessageRows(_ context.Context, scope Scope, session string, limit int) ([]Record, error) {
	rows := []Record{}
	for _, row := range r.rows(colSessionMessages) {
		if sameScope(row.Scope, scope) && row.Status == statusActive && isSessionMessage(row) && row.SessionMessage.TargetSession == session {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UpdatedAt.Before(rows[j].UpdatedAt) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func sessionMessageFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.svc.Repo = &sessionMessageFake{f.repo}
	return f
}

func sendSessionMessage(f *fixture, token, event, target, text string) (SessionMessageSendOutput, error) {
	return f.svc.SendSessionMessage(testCtx, token, SessionMessageSendInput{Scope: scopeA, EventID: event, TargetSession: target, FromSession: "claude-1", FromOrigin: "claude", Text: text})
}

func TestSessionMessageDeliveredOnceAsData(t *testing.T) {
	f := sessionMessageFixture(t)
	sent, err := sendSessionMessage(f, f.agent, "evt-1", "codex-thread-1", "PR #526 checks passed; merge is blocked on review.")
	if err != nil || sent.Authority != AuthorityDataOnly || sent.TargetSession != "codex-thread-1" {
		t.Fatalf("send = %+v, %v", sent, err)
	}
	if other, err := f.svc.TakeSessionMessages(testCtx, f.agent, SessionMessageTakeInput{Scope: scopeA, Session: "codex-thread-2"}); err != nil || len(other.Items) != 0 {
		t.Fatalf("other session took %+v, %v", other, err)
	}
	got, err := f.svc.TakeSessionMessages(testCtx, f.agent, SessionMessageTakeInput{Scope: scopeA, Session: "codex-thread-1"})
	if err != nil || got.Authority != AuthorityDataOnly || len(got.Items) != 1 {
		t.Fatalf("take = %+v, %v", got, err)
	}
	item := got.Items[0]
	if item.ID != sent.ID || item.FromSession != "claude-1" || item.FromOrigin != "claude" || item.TargetSession != "codex-thread-1" || item.Text == "" || item.CreatedAt.IsZero() {
		t.Fatalf("item = %+v", item)
	}
	if again, err := f.svc.TakeSessionMessages(testCtx, f.agent, SessionMessageTakeInput{Scope: scopeA, Session: "codex-thread-1"}); err != nil || len(again.Items) != 0 {
		t.Fatalf("second take = %+v, %v", again, err)
	}
	row, err := f.repo.Get(testCtx, colSessionMessages, scopeA, sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := row.SessionMessage
	if row.Status != sessionMessageStatusDelivered || m.Text != "" || m.DeliveredAt == nil || m.FromSession != "claude-1" || m.TargetSession != "codex-thread-1" {
		t.Fatalf("stored after delivery = %+v %+v", row, m)
	}
}

func TestSessionMessageSendIsIdempotentPerEvent(t *testing.T) {
	f := sessionMessageFixture(t)
	first, err := sendSessionMessage(f, f.agent, "evt-1", "codex-thread-1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := sendSessionMessage(f, f.agent, "evt-1", "codex-thread-1", "hello"); err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := sendSessionMessage(f, f.agent, "evt-1", "codex-thread-1", "changed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay err = %v", err)
	}
	if n := len(f.repo.rows(colSessionMessages)); n != 1 {
		t.Fatalf("rows = %d", n)
	}
}

func TestSessionMessageRejectsBadInputSecretsAndUngrantedPrincipals(t *testing.T) {
	f := sessionMessageFixture(t)
	for _, tc := range []struct{ target, text string }{{"", "x"}, {"bad id", "x"}, {"codex-1", "  "}, {"codex-1", strings.Repeat("가", sessionMessageMaxText+1)}} {
		if _, err := sendSessionMessage(f, f.agent, "evt", tc.target, tc.text); !errors.Is(err, ErrInvalid) {
			t.Fatalf("send(%q) err = %v", tc.target, err)
		}
	}
	if _, err := sendSessionMessage(f, f.agent, "evt", "codex-1", "key sk-ant-api03-"+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("secret err = %v", err)
	}
	if _, err := sendSessionMessage(f, f.work, "evt", "codex-1", "hello"); !errors.Is(err, ErrDenied) {
		t.Fatalf("worker send err = %v", err)
	}
	if _, err := f.svc.TakeSessionMessages(testCtx, f.agent, SessionMessageTakeInput{Scope: scopeA, Session: "bad id"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("take bad id err = %v", err)
	}
	if len(f.repo.rows(colSessionMessages)) != 0 {
		t.Fatal("rejected sends stored a row")
	}
}

func TestSessionMessageTakeLimitReportsMore(t *testing.T) {
	f := sessionMessageFixture(t)
	for _, e := range []string{"evt-1", "evt-2", "evt-3"} {
		if _, err := sendSessionMessage(f, f.agent, e, "codex-1", "note "+e); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.svc.TakeSessionMessages(testCtx, f.agent, SessionMessageTakeInput{Scope: scopeA, Session: "codex-1", Limit: 2})
	if err != nil || len(first.Items) != 2 || !first.More {
		t.Fatalf("first = %+v, %v", first, err)
	}
	rest, err := f.svc.TakeSessionMessages(testCtx, f.agent, SessionMessageTakeInput{Scope: scopeA, Session: "codex-1", Limit: 2})
	if err != nil || len(rest.Items) != 1 || rest.More {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
}
