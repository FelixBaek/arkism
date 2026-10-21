package memory

// Real-PostgreSQL work-checkpoint tests. They run
// against a disposable loopback database from isolatedPostgres and skip when
// POSTGRES_TEST_DSN is not configured.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func workCheckpointPGInput(scope Scope, id, event string, revision int64, pkg WorkCheckpointPackage) WorkCheckpointSaveInput {
	return WorkCheckpointSaveInput{Scope: scope, ID: id, EventID: event, ExpectedRevision: revision, Package: pkg}
}

// boundContext binds the caller token for the store's forced RLS, exactly
// like the HTTP boundary does with the bearer token.
func boundContext(token string) context.Context {
	return ContextWithPostgresToken(testCtx, token)
}

func TestPostgresWorkCheckpointRoundtripAndIdempotency(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "checkpoint evidence source"}))
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}

	// one program saves, another recovers by the same ID.
	out, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "wp.roundtrip_1", "evt-1", 0, pkg))
	if err != nil {
		t.Fatal("save:", err)
	}
	if out.ID != "wp.roundtrip_1" || out.Revision != 1 || out.Authority != AuthorityDataOnly {
		t.Fatalf("save output = %+v", out)
	}
	view, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "wp.roundtrip_1"})
	if err != nil {
		t.Fatal("get:", err)
	}
	normalized, _ := validateWorkCheckpointPackage(pkg)
	if structuredText(view.Package) != structuredText(normalized) {
		t.Fatalf("package roundtrip mismatch:\n got %+v\nwant %+v", view.Package, pkg)
	}
	if view.UpdatedAt.IsZero() || view.Authority != AuthorityDataOnly {
		t.Fatalf("view = %+v", view)
	}

	// retry replays the recorded revision.
	replay, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "wp.roundtrip_1", "evt-1", 0, pkg))
	if err != nil || replay.Revision != 1 {
		t.Fatalf("replay = %+v %v", replay, err)
	}
	changed := pkg
	changed.Status = "blocked"
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "wp.roundtrip_1", "evt-1", 1, changed)); !errors.Is(err, ErrConflict) {
		t.Fatalf("same event different payload = %v", err)
	}
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.human, workCheckpointPGInput(scopeA, "wp.roundtrip_1", "evt-2", 0, pkg)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision = %v", err)
	}
	updated := pkg
	updated.Status = "completed"
	if out, err := f.svc.SaveWorkCheckpoint(testCtx, f.human, workCheckpointPGInput(scopeA, "wp.roundtrip_1", "evt-2", 1, updated)); err != nil || out.Revision != 2 {
		t.Fatalf("update = %+v %v", out, err)
	}
	old, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "wp.roundtrip_1", "evt-1", 0, pkg))
	if err != nil || old.Revision != 1 {
		t.Fatalf("late replay = %+v %v", old, err)
	}
	view, err = f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "wp.roundtrip_1"})
	if err != nil || view.Revision != 2 || view.Package.Status != "completed" {
		t.Fatalf("get after update = %+v %v", view, err)
	}

	// Receipt rows carry hash/id/revision only; the package body lives in no
	// receipt payload.
	receipts := pgCollection(t, p, colWorkCheckpointReceipts)
	if len(receipts) != 2 {
		t.Fatalf("receipts = %d", len(receipts))
	}
	for _, r := range receipts {
		if r.WorkCheckpoint == nil || r.WorkCheckpoint.Package != nil || r.WorkCheckpoint.RequestHash == "" ||
			r.WorkCheckpoint.ReceiptTargetID != "wp.roundtrip_1" || r.WorkCheckpoint.ReceiptRevision < 1 || r.WorkCheckpoint.ReceiptRevision > 2 {
			t.Fatalf("receipt shape = %+v", r.WorkCheckpoint)
		}
		if strings.Contains(structuredText(r.WorkCheckpoint), pkg.Title) {
			t.Fatal("receipt carries package body")
		}
	}
	var receiptDoc []byte
	if err := p.pool.QueryRow(testCtx, `SELECT doc FROM memory_records WHERE collection=$1 LIMIT 1`, colWorkCheckpointReceipts).Scan(&receiptDoc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(receiptDoc), "package") || strings.Contains(string(receiptDoc), "atlas") {
		t.Fatalf("receipt doc carries body: %s", receiptDoc)
	}
	// Audit rows name actions and ids only.
	for _, a := range pgCollection(t, p, "audit_events") {
		if strings.Contains(structuredText(a), pkg.Title) || strings.Contains(structuredText(a), "go test ./...") {
			t.Fatalf("audit carries body: %s", structuredText(a))
		}
	}
}

func TestPostgresWorkCheckpointPermissionsScopeAndRefs(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	// Run as the RLS-restricted application role, like production.
	app := restrictedPostgres(t, p)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Repo, f.svc.Auth = app, auth
	grant := func(principal, role, token string, actions ...string) {
		t.Helper()
		if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: principal, Role: role, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}); err != nil {
			t.Fatal(err)
		}
	}
	codex := testPrincipalToken("codex-checkpoint-tok")
	grant("codex-wp", "agent", codex, "db:read", "db:write", "write:work_checkpoints", "read:work_checkpoints", "read:sources", "write:sources")
	claude := testPrincipalToken("claude-checkpoint-tok")
	grant("claude-wp", "agent", claude, "db:read", "db:write", "write:work_checkpoints", "read:work_checkpoints")
	src, err := f.svc.Create(boundContext(codex), codex, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "checkpoint evidence source"}))
	if err != nil {
		t.Fatal("create source:", err)
	}

	// Missing read permission on the referenced source refuses the save
	// (claude-wp holds no read:sources).
	withRef := workCheckpointPackage()
	withRef.Title = "refs required"
	withRef.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	if _, err := f.svc.SaveWorkCheckpoint(boundContext(claude), claude, workCheckpointPGInput(scopeA, "wp-refs", "evt-perm", 0, withRef)); !errors.Is(err, ErrDenied) {
		t.Fatalf("ref without read permission = %v", err)
	}
	// Granting read makes the same request succeed.
	grant("claude-wp", "agent", claude, "db:read", "db:write", "write:work_checkpoints", "read:work_checkpoints", "read:sources")
	if out, err := f.svc.SaveWorkCheckpoint(boundContext(claude), claude, workCheckpointPGInput(scopeA, "wp-refs", "evt-perm", 0, withRef)); err != nil || out.Revision != 1 {
		t.Fatalf("save with refs = %+v %v", out, err)
	}
	// 권한 변경 재검사: revoking read:sources refuses the next save with a ref.
	revoked := withRef
	revoked.Title = "refs revoked"
	if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "claude-wp", Role: "agent", Token: claude, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "write:work_checkpoints", "read:work_checkpoints"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SaveWorkCheckpoint(boundContext(claude), claude, workCheckpointPGInput(scopeA, "wp-refs", "evt-perm2", 1, revoked)); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked ref permission = %v", err)
	}
	// Revocation also removes access to the derived work state.
	view, err := f.svc.GetWorkCheckpoint(boundContext(claude), claude, WorkCheckpointGetInput{Scope: scopeA, ID: "wp-refs"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("get after revocation = %+v %v", view, err)
	}

	// Secrets are refused and leave nothing behind.
	secret := workCheckpointPackage()
	secret.Title = "leak"
	secret.Goal = "password: hunter2secret"
	if _, err := f.svc.SaveWorkCheckpoint(boundContext(codex), codex, workCheckpointPGInput(scopeA, "wp-secret", "evt-secret", 0, secret)); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("secret = %v", err)
	}
	if _, err := app.Get(boundContext(codex), colWorkCheckpoints, scopeA, "wp-secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("secret checkpoint stored: %v", err)
	}
	if rows, err := app.WorkCheckpointRows(boundContext(codex), scopeA, "", 100); err != nil || len(rows) != 1 {
		t.Fatalf("rows after secret = %d %v", len(rows), err)
	}

	// Foreign scope is denied by policy and hidden by RLS independently.
	foreign := Scope{WorkspaceID: "other-org", ProjectID: "other-project"}
	request := Authorization{Action: "read:" + colWorkCheckpoints, Scope: foreign}
	request.Context = NewContextScope(foreign, request.Action, "")
	decision, err := auth.Authorize(boundContext(claude), claude, request)
	if err != nil || decision.Allowed || !verifiedDenial(request, decision) {
		t.Fatalf("foreign authorization = %+v %v", decision, err)
	}
	// Existing fail-closed policy: the restricted caller cannot persist the
	// required denial audit in a foreign scope, so the service reports unavailable.
	if err := f.svc.auditPolicyDenial(boundContext(claude), request, decision); err == nil {
		t.Fatal("foreign audit unexpectedly writable")
	}
	if view, err := f.svc.GetWorkCheckpoint(boundContext(claude), claude, WorkCheckpointGetInput{Scope: foreign, ID: "wp-refs"}); !errors.Is(err, ErrUnavailable) || view.ID != "" {
		t.Fatalf("foreign get = %+v %v", view, err)
	}
	if page, err := f.svc.ListWorkCheckpoints(boundContext(claude), claude, WorkCheckpointListInput{Scope: foreign}); !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
		t.Fatalf("foreign list = %+v %v", page, err)
	}
	if rows, err := app.WorkCheckpointRows(boundContext(claude), foreign, "", 100); err != nil || len(rows) != 0 {
		t.Fatalf("foreign RLS rows = %d %v", len(rows), err)
	}

	// Deleted and erased references are re-checked at save time.
	missing := withRef
	missing.SourceRefs = []Ref{{Collection: "sources", ID: "gone-source", Revision: 1}}
	if _, err := f.svc.SaveWorkCheckpoint(boundContext(codex), codex, workCheckpointPGInput(scopeA, "wp-refs2", "evt-missing", 0, missing)); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing ref = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.Insert(testCtx, "sources", Record{ID: "erased-checkpoint-src", Scope: scopeA, Kind: "note", Status: statusActive, Revision: 2,
		ContentErased: true, CreatedAt: now, UpdatedAt: now, CreatedBy: "codex-wp", CreatedByType: roleAgent}); err != nil {
		t.Fatal(err)
	}
	erased := withRef
	erased.SourceRefs = []Ref{{Collection: "sources", ID: "erased-checkpoint-src", Revision: 2}}
	if _, err := f.svc.SaveWorkCheckpoint(boundContext(codex), codex, workCheckpointPGInput(scopeA, "wp-refs2", "evt-erased", 0, erased)); !errors.Is(err, ErrDenied) {
		t.Fatalf("erased ref = %v", err)
	}

	// Work checkpoint rows live in memory_records and inherit the forced
	// tenant RLS without any new table, migration or policy.
	var bound, unknown int
	if err := app.q().QueryRow(ContextWithPostgresToken(testCtx, codex), `SELECT count(*) FROM memory_records WHERE collection=$1`, colWorkCheckpoints).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if err := app.q().QueryRow(ContextWithPostgresToken(testCtx, "no-such-principal-token"), `SELECT count(*) FROM memory_records WHERE collection=$1`, colWorkCheckpoints).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if bound != 1 || unknown != 0 {
		t.Fatalf("RLS counts: bound=%d unknown=%d", bound, unknown)
	}
}

func TestPostgresWorkCheckpointListPagination(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	pkg := workCheckpointPackage()
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("wp-page-%d", i)
		if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, id, "evt-"+id, 0, pkg)); err != nil {
			t.Fatal("save:", err)
		}
	}
	seen := []string{}
	after := ""
	for page := 0; ; page++ {
		out, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, After: after, Limit: 3})
		if err != nil {
			t.Fatal("list:", err)
		}
		if out.Authority != AuthorityDataOnly {
			t.Fatalf("authority = %q", out.Authority)
		}
		for _, item := range out.Items {
			if item.ID != fmt.Sprintf("wp-page-%d", len(seen)+1) {
				t.Fatalf("page order = %+v", out.Items)
			}
			if item.Title != pkg.Title || item.Origin != pkg.Origin || item.Revision != 1 || item.UpdatedAt.IsZero() {
				t.Fatalf("item fields = %+v", item)
			}
			seen = append(seen, item.ID)
		}
		if len(out.Items) == 0 && out.Next != "" {
			t.Fatal("empty page with cursor")
		}
		if out.Next == "" {
			break
		}
		if page > 10 {
			t.Fatal("pagination did not end")
		}
		after = out.Next
	}
	if len(seen) != 7 {
		t.Fatalf("seen = %v", seen)
	}
	// Exact tail boundary: the last ID as cursor yields an empty page.
	out, err := f.svc.ListWorkCheckpoints(testCtx, f.human, WorkCheckpointListInput{Scope: scopeA, After: seen[len(seen)-1], Limit: 3})
	if err != nil || len(out.Items) != 0 || out.Next != "" {
		t.Fatalf("tail = %+v %v", out, err)
	}
}

func TestPostgresWorkCheckpointConcurrentCAS(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	const workers = 6
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(workers)
	errs := make([]error, workers)
	revs := make([]int64, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wait.Done()
			<-start
			pkg := workCheckpointPackage()
			pkg.Title = fmt.Sprintf("동시 작업 %d", i)
			pkg.Decisions = []string{fmt.Sprintf("worker-%d", i)}
			out, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "wp-race", fmt.Sprintf("evt-race-%d", i), 0, pkg))
			errs[i], revs[i] = err, out.Revision
		}(i)
	}
	close(start)
	wait.Wait()
	successes := 0
	for i, err := range errs {
		switch {
		case err == nil:
			successes++
			if revs[i] != 1 {
				t.Fatalf("worker %d revision = %d", i, revs[i])
			}
		case errors.Is(err, ErrConflict):
		default:
			t.Fatalf("worker %d unexpected error = %v", i, err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent CAS successes = %d (%v)", successes, errs)
	}
	view, err := f.svc.GetWorkCheckpoint(testCtx, f.human, WorkCheckpointGetInput{Scope: scopeA, ID: "wp-race"})
	if err != nil || view.Revision != 1 {
		t.Fatalf("post-race get = %+v %v", view, err)
	}
	if receipts := pgCollection(t, p, colWorkCheckpointReceipts); len(receipts) != 1 {
		t.Fatalf("receipts after race = %d", len(receipts))
	}
	auditCounts := map[string]int{}
	for _, row := range pgCollection(t, p, "audit_events") {
		auditCounts[row.Kind]++
	}
	if len(auditCounts) != 2 || auditCounts["create:"+colWorkCheckpoints] != 1 || auditCounts["read:"+colWorkCheckpoints] != 1 {
		t.Fatalf("audits after race = %v", auditCounts)
	}
	// The winner's request replayed afterwards stays idempotent; the losers'
	// events stay conflicting.
	for i := 0; i < workers; i++ {
		pkg := workCheckpointPackage()
		pkg.Title = fmt.Sprintf("동시 작업 %d", i)
		pkg.Decisions = []string{fmt.Sprintf("worker-%d", i)}
		out, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "wp-race", fmt.Sprintf("evt-race-%d", i), 0, pkg))
		if err != nil && !errors.Is(err, ErrConflict) {
			t.Fatalf("replay %d = %v", i, err)
		}
		if err == nil && out.Revision != 1 {
			t.Fatalf("replay %d revision = %d", i, out.Revision)
		}
	}
}
