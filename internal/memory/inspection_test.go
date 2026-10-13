package memory

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

var syntheticSecrets = []string{
	"mongodb+srv://user:pa/ss@cluster.example/db", "redis://default:pw/withslash@cache:6379",
	"sk-abcdefghijklmnopqrstuvwx", "al-abcdefghijklmnopqrstuvwx", "AKIAABCDEFGHIJKLMNOP",
	"AIzaabcdefghijklmnopqrstuvwxy", "ghp_abcdefghijklmnopqrstuvwxyz123456", "github_pat_abcdefghijklmnopqrstuvwxyz123456",
	"xox" + "b-1234567890-abcdefghijklmnop", "Bearer abcdefghijklmnop", "Basic YWRtaW46cGFzc3dvcmQ=",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.abcdefghijklmnop",
	"password='tiny'", `{"api_key":"abcdefgh123456789"}`, "비밀번호: 아주짧음",
	"mongodb+srv://sample:syntheticpassword@example.invalid/db",
	"-----BEGIN PRIVATE KEY-----\nFAKEKEYDATA\n-----END PRIVATE KEY-----",
}

func TestIngestionBlocksSecretsWithoutPersistingValues(t *testing.T) {
	for i, secret := range syntheticSecrets {
		if safeTransfer(secret) {
			t.Fatalf("case %d allowed external transfer", i)
		}
		f := newFixture(t)
		_, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "sample " + secret}))
		if !errors.Is(e, ErrSecretDetected) {
			t.Fatalf("case %d was not blocked: %v", i, e)
		}
		if len(f.repo.rows("sources")) != 0 || len(f.repo.rows("jobs")) != 0 {
			t.Fatal("blocked content persisted")
		}
		audits := f.repo.rows("audit_events")
		if len(audits) != 1 {
			t.Fatal("missing value-free blocked audit")
		}
		if strings.Contains(structuredText(audits), secret) || audits[0].Text != "" || audits[0].Target.ID != "" {
			t.Fatal("secret or source hash in audit")
		}
	}
}
func TestIngestionRedactionAndPrivacy(t *testing.T) {
	for i, secret := range syntheticSecrets {
		f := newFixture(t)
		r, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "sample " + secret, Summary: secret, Keywords: []string{secret}, OnSecret: "redact"}))
		if e != nil {
			t.Fatalf("redaction case %d: %v", i, e)
		}
		if r.Inspection == nil || !r.Inspection.Redacted || r.Inspection.RulesVersion != inspectionVersion || strings.Contains(structuredText(r), secret) {
			t.Fatalf("bad redaction case %d", i)
		}
	}
	f := newFixture(t)
	r := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "contact person@example.invalid", Sensitivity: "public"}))
	if r.Sensitivity != "private" || !r.Inspection.PersonalData {
		t.Fatal("personal data not classified")
	}
	r = f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "010-1234-5678", Sensitivity: "restricted"}))
	if r.Sensitivity != "restricted" {
		t.Fatal("classification downgraded")
	}
}
func TestInspectionCoversSecondaryFieldsAndKeepsReferences(t *testing.T) {
	f := newFixture(t)
	secret := syntheticSecrets[0]
	for _, in := range []CreateInput{
		{Scope: scopeA, Text: "safe", Summary: secret},
		{Scope: scopeA, Text: "safe", Keywords: []string{secret}},
		{Scope: scopeA, Text: "safe", Sources: []SourceRef{{SourceID: secret, Locator: "original"}}, OnSecret: "redact"},
	} {
		if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(in)); !errors.Is(e, ErrSecretDetected) {
			t.Fatal("secondary secret accepted")
		}
	}
	f.repo.failAudit = true
	if _, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: secret, OnSecret: "redact"})); e == nil {
		t.Fatal("audit failure ignored")
	}
	if len(f.repo.rows("sources")) != 0 {
		t.Fatal("redaction transaction did not roll back")
	}
}

type inspectionAuth struct {
	Authorizer
	payloads []string
	fail     bool
}

func (a *inspectionAuth) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	a.payloads = append(a.payloads, structuredText(req))
	if a.fail {
		return Decision{}, ErrUnavailable
	}
	return a.Authorizer.Authorize(ctx, token, req)
}
func TestInspectionHTTPAndPolicyBoundary(t *testing.T) {
	f := newFixture(t)
	auth := &inspectionAuth{Authorizer: f.svc.Auth}
	f.svc.Auth = auth
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	for _, col := range []string{"sources", "memory_candidates", "tool_calls", "evaluations"} {
		for _, secret := range syntheticSecrets {
			in := CreateInput{Scope: scopeA, Text: secret}
			req := httptest.NewRequest("POST", "/v1/"+col, strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != 422 || strings.Contains(w.Body.String(), secret) {
				t.Fatalf("%s status %d", col, w.Code)
			}
			for _, stored := range f.repo.store {
				if strings.Contains(structuredText(stored), secret) {
					t.Fatal("secret persisted")
				}
			}
			if strings.Contains(logs.String(), secret) || strings.Contains(structuredText(auth.payloads), secret) {
				t.Fatal("secret sent to log/policy")
			}
		}
	}
	secret := syntheticSecrets[0]
	if _, err := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: secret, OnSecret: "redact"})); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(structuredText(auth.payloads), secret) {
		t.Fatal("unredacted policy payload")
	}
	auth.fail = true
	before := len(f.repo.rows("sources"))
	if _, err := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: secret, OnSecret: "redact"})); err == nil || len(f.repo.rows("sources")) != before {
		t.Fatal("policy error did not fail closed")
	}
}
func TestInspectionMalformedKeyAndRestrictedEmbedding(t *testing.T) {
	f := newFixture(t)
	r, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "-----BEGIN PRIVATE KEY-----\nremaining-key", OnSecret: "redact"}))
	if e != nil || strings.Contains(r.Text, "remaining-key") {
		t.Fatal("partial private key was retained")
	}
	scope := scopeA
	scope.UserID = syntheticSecrets[0]
	if _, e = f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scope, Text: "safe"})); !errors.Is(e, ErrSecretDetected) {
		t.Fatal("secret scope accepted")
	}
	for _, a := range f.repo.rows("audit_events") {
		if strings.Contains(structuredText(a), scope.UserID) {
			t.Fatal("secret scope audited")
		}
	}
	rec := f.activeMemory("restricted content")
	rec.Sensitivity = "restricted"
	f.repo.store["memories"][recKey(rec.Scope, rec.ID)] = rec
	if e = f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrDenied) || len(f.emb.calls) != 0 {
		t.Fatal("restricted content embedded")
	}
}
