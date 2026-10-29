package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type contextReaderTransport func(*http.Request) (*http.Response, error)

func (f contextReaderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func contextReaderRecords() (map[string]any, map[string]any) {
	scope := map[string]any{"workspace_id": "w", "project_id": "p"}
	source := map[string]any{"id": "source", "revision": int64(7), "scope": scope, "kind": "text", "status": "active", "text": "원본 😀\n", "summary": "ignored summary", "sources": []any{}, "content_hash": strings.Repeat("a", 64)}
	policy := map[string]any{"id": "team", "revision": int64(8), "scope": scope, "kind": "crew", "status": "active", "text": "", "crew": map[string]any{"type": "team", "name": "display", "members": []any{map[string]any{"agent_id": "speaker", "role": "worker", "order": 0}}, "context": []any{map[string]any{"collection": "sources", "id": "source", "share": "team", "title": "display"}}}}
	return source, policy
}
func contextReaderFixture(t *testing.T, source, policy map[string]any) (*Client, context.Context, *[]string) {
	t.Helper()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	client := &Client{BaseURL: "https://fixture.invalid", TokenFile: token, Scope: Scope{WorkspaceID: "w", ProjectID: "p"}}
	client.HTTP = &http.Client{Transport: contextReaderTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		if r.Method != "GET" || r.URL.Query().Get("workspace_id") != "w" || r.URL.Query().Get("project_id") != "p" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Fatal("unscoped or changed read")
		}
		value := source
		if r.URL.Path == "/v1/crew/team" {
			value = policy
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}, Request: r}, nil
	})}
	run := ObservationRun{Scope: ObservationScope(client.Scope), RunRef: ObservationRef{Collection: "crew_meetings", ID: "meeting", Revision: 9}, Definition: ObservationDefinition{Ref: ObservationRef{Collection: "crew", ID: "team", Revision: 3}, SourceType: "team"}}
	ctx := withContextReceiptActor(WithAttemptContext(context.Background(), AttemptContext{Run: run, NodeID: "never-parse-me", NodeAttempt: 1}), "speaker")
	return client, ctx, &calls
}
func contextReaderGet(t *testing.T, c *Client, ctx context.Context) (ContextReceiptRead, error) {
	t.Helper()
	return c.ContextReceipt(ctx, TeamContext{Collection: "sources", ID: "source", Share: "shared"})
}
func TestContextReaderNativeBodyAndLivePolicy(t *testing.T) {
	source, policy := contextReaderRecords()
	c, ctx, calls := contextReaderFixture(t, source, policy)
	got, err := contextReaderGet(t, c, ctx)
	want, _ := ContextTextSHA256(source["text"].(string))
	if err != nil || got.Text != source["text"] || got.Observation.ContentSHA256 == nil || *got.Observation.ContentSHA256 != want {
		t.Fatal("selected body or digest changed", err)
	}
	if got.Facts.Lifecycle != "active" || got.Facts.PolicyState != "confirmed" || got.Facts.Sharing != "team" || got.Observation.Sharing != "team" || got.Facts.PolicyRef == nil || got.Facts.PolicyRef.Revision != 8 || got.Facts.GuardSHA256 == want || len(*calls) != 2 {
		t.Fatal("native/current policy unavailable", got.Facts)
	}
	if got.Observation.AvailableAtNS != nil || got.Observation.AvailabilitySeq != nil {
		t.Fatal("invented availability from record timestamps")
	}
	if err = ValidateSourceObservation(got.Observation); err != nil {
		t.Fatal(err)
	}
}
func TestContextReaderNativeOmittedFalseAndDisplayOnlyCAS(t *testing.T) {
	source, policy := contextReaderRecords()
	c, ctx, _ := contextReaderFixture(t, source, policy)
	first, err := contextReaderGet(t, c, ctx)
	if err != nil {
		t.Fatal(err)
	}
	source["content_erased"] = false
	source["privacy_quarantined"] = false
	source["revision"] = int64(8)
	source["title"] = "new display"
	source["updated_at"] = "2099-01-01T00:00:00Z"
	policy["revision"] = int64(9)
	policy["updated_at"] = "2099-01-01T00:00:00Z"
	team := policy["crew"].(map[string]any)
	team["name"] = "new display"
	team["context"].([]any)[0].(map[string]any)["title"] = "new label"
	second, err := contextReaderGet(t, c, ctx)
	if err != nil || second.Facts.GuardSHA256 != first.Facts.GuardSHA256 || *second.Observation.ContentSHA256 != *first.Observation.ContentSHA256 {
		t.Fatal("display/omitempty change changed premise", err)
	}
}
func TestContextReaderSemanticNativeAndPolicyGuardChanges(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{"native content hash", func(s, p map[string]any) { s["content_hash"] = strings.Repeat("b", 64) }},
		{"source provenance", func(s, p map[string]any) {
			s["sources"] = []any{map[string]any{"source_id": "origin", "locator": "part-2"}}
		}},
		{"memory premise", func(s, p map[string]any) {
			s["memory_context"] = map[string]any{"semantic_kind": "fact", "applicability": "new"}
		}},
		{"team focus", func(s, p map[string]any) {
			p["crew"].(map[string]any)["members"].([]any)[0].(map[string]any)["focus"] = "changed"
		}},
		{"live share", func(s, p map[string]any) {
			p["crew"].(map[string]any)["context"].([]any)[0].(map[string]any)["share"] = "shared"
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, policy := contextReaderRecords()
			c, ctx, _ := contextReaderFixture(t, source, policy)
			first, err := contextReaderGet(t, c, ctx)
			if err != nil {
				t.Fatal(err)
			}
			test.change(source, policy)
			second, err := contextReaderGet(t, c, ctx)
			if err != nil || first.Facts.GuardSHA256 == second.Facts.GuardSHA256 {
				t.Fatal("semantic native change hidden", err)
			}
		})
	}
}
func TestContextReaderHistoryCannotBecomeCurrent(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"inactive", func(s map[string]any) { s["status"] = "archived" }},
		{"not effective", func(s map[string]any) { s["effective_at"] = "2099-01-01T00:00:00Z" }},
		{"expired", func(s map[string]any) { s["expires_at"] = "2000-01-01T00:00:00Z" }},
		{"erased", func(s map[string]any) { s["content_erased"] = true }},
		{"quarantined", func(s map[string]any) { s["privacy_quarantined"] = true }},
		{"missing status", func(s map[string]any) { delete(s, "status") }},
		{"invalid flag", func(s map[string]any) { s["content_erased"] = "false" }},
		{"invalid time", func(s map[string]any) { s["expires_at"] = "not-a-time" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, policy := contextReaderRecords()
			test.change(source)
			c, ctx, _ := contextReaderFixture(t, source, policy)
			got, err := contextReaderGet(t, c, ctx)
			if err != nil || got.Text != source["text"] || got.Facts.Lifecycle == "active" {
				t.Fatal("history became usable or body lost", err)
			}
		})
	}
}
func TestContextReaderMissingMetadataPreservesObservedText(t *testing.T) {
	source, policy := contextReaderRecords()
	for _, key := range []string{"id", "revision", "scope"} {
		delete(source, key)
	}
	source["text"] = ""
	source["summary"] = "summary fallback"
	c, ctx, _ := contextReaderFixture(t, source, policy)
	got, err := contextReaderGet(t, c, ctx)
	if err != nil || got.Text != "summary fallback" || got.Observation.ProvenanceStatus == "pinned" || got.Observation.Revision != nil {
		t.Fatal("missing metadata fabricated a pin", err)
	}
}
func TestContextReaderExactBindings(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"id", func(s map[string]any) { s["id"] = "foreign" }},
		{"scope", func(s map[string]any) { s["scope"] = map[string]any{"workspace_id": "other", "project_id": "p"} }},
		{"collection", func(s map[string]any) { s["collection"] = "memories" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, policy := contextReaderRecords()
			test.change(source)
			c, ctx, _ := contextReaderFixture(t, source, policy)
			if _, err := contextReaderGet(t, c, ctx); err == nil {
				t.Fatal("foreign record rebound to request")
			}
		})
	}
}
func TestContextReaderCASIsStrictAndNeverFabricated(t *testing.T) {
	for _, value := range []any{nil, true, json.Number("7.0"), -1, "7"} {
		source, policy := contextReaderRecords()
		source["revision"] = value
		c, ctx, _ := contextReaderFixture(t, source, policy)
		got, err := contextReaderGet(t, c, ctx)
		if err == nil && got.Observation.Revision != nil {
			t.Fatal("invalid CAS pinned")
		}
	}
}
func TestContextReaderPolicyUnknownAndActorRemovalPreservesSource(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"removed actor", func(p map[string]any) { p["crew"].(map[string]any)["members"] = []any{} }},
		{"removed source", func(p map[string]any) { p["crew"].(map[string]any)["context"] = []any{} }},
		{"bad share", func(p map[string]any) {
			p["crew"].(map[string]any)["context"].([]any)[0].(map[string]any)["share"] = "unknown"
		}},
		{"old policy", func(p map[string]any) { p["revision"] = int64(2) }},
		{"foreign policy scope", func(p map[string]any) { p["scope"] = map[string]any{"workspace_id": "foreign", "project_id": "p"} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source, policy := contextReaderRecords()
			test.change(policy)
			c, ctx, _ := contextReaderFixture(t, source, policy)
			got, err := contextReaderGet(t, c, ctx)
			if err != nil || got.Text != source["text"] || got.Facts.PolicyState == "confirmed" {
				t.Fatal("cached share or membership trusted", err)
			}
		})
	}
}
func TestContextReaderNoActorOrActualTeamDoesNotGuess(t *testing.T) {
	source, policy := contextReaderRecords()
	c, _, calls := contextReaderFixture(t, source, policy)
	got, err := contextReaderGet(t, c, context.Background())
	if err != nil || got.Facts.PolicyState == "confirmed" || len(*calls) != 1 {
		t.Fatal("invented actor/team policy", err)
	}
}
func TestContextReaderGenericCoherentCollections(t *testing.T) {
	for _, collection := range []string{"skills", "skill_versions", "prompts", "design_systems", "crew"} {
		source, policy := contextReaderRecords()
		c, ctx, _ := contextReaderFixture(t, source, policy)
		got, err := c.ContextReceipt(ctx, TeamContext{Collection: collection, ID: "source"})
		if err != nil || got.Facts.Lifecycle != "active" {
			t.Fatal("coherent generic Record disabled", collection, err)
		}
	}
}
func TestContextReaderHTTPAndStrictJSON(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{{"duplicate", `{"text":"x","text":"y"}`, 200}, {"invalid", `{"text":`, 200}, {"invalid utf8", "{\"text\":\"\xff\"}", 200}, {"source403", `{"code":"permission_denied"}`, 403}, {"knowledge unsupported", `{"code":"invalid_request"}`, 400}} {
		t.Run(test.name, func(t *testing.T) {
			source, policy := contextReaderRecords()
			c, ctx, _ := contextReaderFixture(t, source, policy)
			c.HTTP = &http.Client{Transport: contextReaderTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: http.Header{}, Request: r}, nil
			})}
			if _, err := contextReaderGet(t, c, ctx); err == nil {
				t.Fatal("invalid/unsupported source read accepted")
			}
		})
	}
	source, policy := contextReaderRecords()
	c, ctx, _ := contextReaderFixture(t, source, policy)
	c.TokenFile = filepath.Join(t.TempDir(), "missing")
	if _, err := contextReaderGet(t, c, ctx); err == nil {
		t.Fatal("missing token bypassed")
	}
}
func TestContextReaderPolicyHTTPFailureIsSeparate(t *testing.T) {
	source, policy := contextReaderRecords()
	c, ctx, _ := contextReaderFixture(t, source, policy)
	transport := c.HTTP.Transport
	c.HTTP.Transport = contextReaderTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/crew/team" {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"code":"permission_denied"}`)), Header: http.Header{}, Request: r}, nil
		}
		return transport.RoundTrip(r)
	})
	got, err := contextReaderGet(t, c, ctx)
	if err != nil || got.Text != source["text"] || got.Facts.PolicyState != "denied" || got.Observation.ReadAuthorization != "confirmed" {
		t.Fatal("policy error misreported as historical source revocation", err)
	}
	if got.Facts.CheckedAtNS == nil || *got.Facts.CheckedAtNS > time.Now().UnixNano() {
		t.Fatal("invalid observed check time")
	}
}

func TestContextReaderMissingKindOrInvalidNativeHashIsUnknown(t *testing.T) {
	for _, kind := range []string{"missing kind", "invalid native hash"} {
		t.Run(kind, func(t *testing.T) {
			source, policy := contextReaderRecords()
			if kind == "missing kind" {
				delete(source, "kind")
			} else {
				source["content_hash"] = true
			}
			c, ctx, _ := contextReaderFixture(t, source, policy)
			got, err := contextReaderGet(t, c, ctx)
			if err != nil || got.Text != source["text"] || got.Facts.Lifecycle == "active" {
				t.Fatal("incoherent native metadata became current", err)
			}
		})
	}
}
func TestContextReaderNativeMemoryApprovalAndNoStaleMark(t *testing.T) {
	for _, kind := range []string{"approved", "legacy", "missing approver", "whitespace approver", "new review metadata", "stale", "superseded"} {
		t.Run(kind, func(t *testing.T) {
			source, policy := contextReaderRecords()
			source["approved_by"] = "human"
			source["verification_status"] = "verified"
			review := map[string]any{"receipt_id": "receipt", "actor_role": "human", "channel": "explicit_review_api", "by": "human", "action": "approve"}
			source["review"] = review
			switch kind {
			case "legacy":
				delete(source, "review")
			case "new review metadata":
				review["actor_role"] = "agent"
			case "missing approver":
				delete(source, "approved_by")
			case "whitespace approver":
				source["approved_by"] = "  \t "
			case "stale":
				source["stale_mark"] = map[string]any{"memory": "old"}
			case "superseded":
				source["superseded_by"] = map[string]any{"collection": "memories", "id": "next", "revision": 2}
			}
			c, ctx, _ := contextReaderFixture(t, source, policy)
			got, err := c.ContextReceipt(ctx, TeamContext{Collection: "memories", ID: "source"})
			if err != nil {
				t.Fatal(err)
			}
			if (got.Facts.Lifecycle == "active") != (kind == "approved" || kind == "legacy" || kind == "new review metadata") {
				t.Fatal("human/stale memory boundary changed", kind, got.Facts.Lifecycle)
			}
		})
	}
}

func TestContextReaderNoBodyDoesNotBypassForeignBinding(t *testing.T) {
	source, policy := contextReaderRecords()
	delete(source, "text")
	delete(source, "summary")
	source["id"] = "foreign"
	c, ctx, _ := contextReaderFixture(t, source, policy)
	if _, err := contextReaderGet(t, c, ctx); err == nil {
		t.Fatal("bodyless foreign response accepted")
	}
}
func TestContextReaderTransportErrorDoesNotExposeRawDetails(t *testing.T) {
	source, policy := contextReaderRecords()
	c, ctx, _ := contextReaderFixture(t, source, policy)
	c.HTTP.Transport = contextReaderTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic-secret-raw-details") })
	_, err := contextReaderGet(t, c, ctx)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("raw transport detail exposed")
	}
}
