package memory

import (
	"bytes"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostgresHandoffTransferChildSkillErasureHTTP(t *testing.T) {
	for _, collection := range []string{"sources", colSkillVersions} {
		t.Run(collection, func(t *testing.T) {
			p := isolatedPostgres(t)
			if err := p.initTraceSchema(testCtx); err != nil {
				t.Fatal(err)
			}
			f := newFixture(t)
			f.svc.Repo = p
			body := "# Review\nVerify before accepting."
			imported, e := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "handoff-review", Path: "/synthetic/SKILL.md", Content: body, Analysis: detailedSkillAnalysis()})
			if e != nil {
				t.Fatal(e)
			}
			parent, e := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "handoff-parent", EventID: "first", Package: workCheckpointPackage()})
			if e != nil {
				t.Fatal(e)
			}
			for _, token := range []string{f.human, f.agent} {
				role := roleAgent
				if token == f.human {
					role = roleHuman
				}
				if e = p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: token, Role: role, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); e != nil {
					t.Fatal(e)
				}
			}
			app := restrictedPostgres(t, p)
			var appRole string
			if err := app.pool.QueryRow(testCtx, "SELECT current_user").Scan(&appRole); err != nil {
				t.Fatal(err)
			}
			if _, err := p.q().Exec(testCtx, "GRANT SELECT,INSERT,UPDATE,DELETE ON execution_traces,source_events,entities,relations TO "+pgx.Identifier{appRole}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			auth, e := NewPostgresAuthorizer(app)
			if e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(Handler(&Service{Repo: app, Auth: auth}))
			defer server.Close()
			client := &http.Client{Timeout: 5 * time.Second}
			call := func(token, method, path string, in, out any) int {
				t.Helper()
				raw, _ := json.Marshal(in)
				req, e := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				res, e := client.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				data, _ := io.ReadAll(res.Body)
				if out != nil && res.StatusCode < 300 {
					if e = json.Unmarshal(data, out); e != nil {
						t.Fatal(e)
					}
				}
				return res.StatusCode
			}
			in := HandoffTransferInput{Scope: scopeA, Work: Ref{Collection: colWorkCheckpoints, ID: parent.ID, Revision: parent.Revision}, Destination: "external:openai", Content: structuredText(map[string]string{"task": "Review work", "instructions": body}), Skills: []HandoffTransferSkill{{SkillID: "handoff-review", VersionID: imported.Record.ID, ContentHash: ContentHash(body)}}}
			var preflight HandoffTransferOutput
			if status := call(f.agent, "POST", "/v1/work-checkpoints/authorize-handoff", in, &preflight); status != 200 || len(preflight.SourceRefs) != 2 {
				t.Fatal("preflight", status, preflight)
			}
			pkg := workCheckpointPackage()
			pkg.Completed = []string{"Result derived from approved skill"}
			pkg.SourceRefs = preflight.SourceRefs
			var child WorkCheckpointSaveOutput
			if status := call(f.agent, "POST", "/v1/work-checkpoints/save", WorkCheckpointSaveInput{Scope: scopeA, ID: "handoff-child", EventID: "first", Package: pkg, ArchiveMessages: preflight.ArchiveMessages}, &child); status != 200 {
				t.Fatal("save", status)
			}
			if status := call(f.agent, "POST", "/v1/work-checkpoints/get", WorkCheckpointGetInput{Scope: scopeA, ID: child.ID}, nil); status != 200 {
				t.Fatal("initial get", status)
			}
			id, revision := imported.Record.ID, imported.Record.Revision
			if collection == "sources" {
				id = imported.Record.Sources[0].SourceID
				r, e := p.Get(testCtx, "sources", scopeA, id)
				if e != nil {
					t.Fatal(e)
				}
				revision = r.Revision
			}
			if collection == "sources" {
				status := call(f.human, "DELETE", "/v1/sources/"+id, ChangeInput{Scope: scopeA, ExpectedRevision: revision}, nil)
				if status != 200 && status != 202 {
					t.Fatal("source deletion", status)
				}
			} else {
				// Active skill versions cannot be deleted through generic CRUD.
				// Simulate the erasure worker removing canonical content, then
				// verify that the public checkpoint read fails immediately.
				if err := p.Delete(testCtx, colSkillVersions, scopeA, id, revision); err != nil {
					t.Fatal(err)
				}
			}
			status := call(f.agent, "POST", "/v1/work-checkpoints/get", WorkCheckpointGetInput{Scope: scopeA, ID: child.ID}, nil)
			if status != 403 && status != 404 && status != 409 {
				t.Fatal("deleted skill evidence still readable", status)
			}
		})
	}
}
