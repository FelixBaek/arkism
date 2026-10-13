package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type filePolicy struct {
	Authorizer
	requests                       []Authorization
	unsafe, denyFile, changedActor bool
}

func (p *filePolicy) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	p.requests = append(p.requests, in)
	d, err := p.Authorizer.Authorize(ctx, token, in)
	if in.Content != "" {
		d.ContentSafe = !p.unsafe
	}
	if in.Action == "write:artifacts" {
		d.Allowed = !p.denyFile
		if p.changedActor {
			d.PrincipalID = "different-principal"
		}
	}
	return d, err
}

func fileRequest(t *testing.T, in FileIngestInput, content []byte, token string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(part).Encode(in); err != nil {
		t.Fatal(err)
	}
	part, err = w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="../../ignored.txt"`}, "Content-Type": {in.Artifact.MIME}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/sources/ingest", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func exerciseFileIngestHTTP(t *testing.T, repo Repository) {
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.BlobDirectory = blobTestDir(t)
	policy := &filePolicy{Authorizer: f.auth}
	f.svc.Auth = policy
	h := Handler(f.svc)
	in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA, IdempotencyKey: "http-file", Sensitivity: "public"}), Artifact: FileArtifactSpec{Kind: "tool_output", MIME: "application/json"}}
	body := []byte(`{"email":"person\u0040example.test","label":"original"}`)
	call := func(in FileIngestInput) FileIngestResult {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, fileRequest(t, in, body, f.human))
		if w.Code != http.StatusCreated {
			t.Fatalf("ingest: status=%d body=%s", w.Code, w.Body)
		}
		var out FileIngestResult
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := call(in)
	if first.Source.Text != "" || first.Source.Sensitivity != "private" || first.Source.Inspection == nil || !first.Source.Inspection.PersonalData || first.Source.Sources[0].Span != nil || first.Artifact.Digest != ContentHash(string(body)) {
		t.Fatal("file inspection/classification missing")
	}
	schemaBytes, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					MinLength int `json:"minLength"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if json.Unmarshal(schemaBytes, &schema) != nil {
		t.Fatal("invalid OpenAPI JSON")
	}
	textSchema, found := schema.Components.Schemas["Record"].Properties["text"]
	if !found || len(first.Source.Text) < textSchema.MinLength {
		t.Fatal("file-only response violates Record text schema")
	}
	baseProperties := schema.Components.Schemas["CreateInput"].Properties
	fileProperties := schema.Components.Schemas["FileSourceInput"].Properties
	if len(baseProperties) == 0 || len(baseProperties) != len(fileProperties) {
		t.Fatal("source input schemas have different fields")
	}
	for key := range baseProperties {
		if _, ok := fileProperties[key]; !ok {
			t.Fatal("file source schema missing field", key)
		}
	}
	if again := call(in); structuredText(first) != structuredText(again) {
		t.Fatal("HTTP replay changed response")
	}
	for _, action := range []string{"write:sources", "write:artifacts"} {
		found := false
		for _, req := range policy.requests {
			if req.Action != action || req.Content == "" {
				continue
			}
			var payload struct {
				Source   CreateInput        `json:"source"`
				Content  string             `json:"file_content"`
				Artifact FileArtifactResult `json:"artifact"`
			}
			if json.Unmarshal([]byte(req.Content), &payload) != nil || payload.Content != string(body) || payload.Source.Sensitivity != "private" || payload.Artifact.Digest != first.Artifact.Digest || payload.Artifact.Bytes != int64(len(body)) {
				t.Fatal("policy did not inspect complete exact payload")
			}
			found = true
		}
		if !found {
			t.Fatal("missing content policy", action)
		}
	}
	in.Source.Scope = scopeB
	other := call(in)
	if other.Source.ID == first.Source.ID || other.Artifact.ID == first.Artifact.ID {
		t.Fatal("cross-scope identity leak")
	}
	blob, err := repo.Get(testCtx, colBlobs, blobScope(), first.Artifact.Digest)
	if err != nil || blob.Blob == nil || blob.Blob.References != 2 {
		t.Fatal("file not shared", err)
	}
	saved, err := os.ReadFile(filepath.Join(f.svc.BlobDirectory, first.Artifact.Digest))
	if err != nil || !bytes.Equal(saved, body) {
		t.Fatal("file bytes changed", err)
	}
	artifact, err := repo.Get(testCtx, colArtifacts, scopeA, first.Artifact.ID)
	if err != nil || artifact.Sensitivity != "private" {
		t.Fatal("artifact classification", err)
	}
}

func TestFileIngestPolicyRequestLimit(t *testing.T) {
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	body := strings.Repeat("<", maxTextArtifactBytes-4) + "tail"
	calls := 0
	f.svc.Auth = authorizerFunc(func(_ context.Context, _ string, in Authorization) (Decision, error) {
		var payload struct {
			Content string `json:"file_content"`
		}
		if json.Unmarshal([]byte(in.Content), &payload) != nil || payload.Content != body {
			t.Error("policy received truncated/transformed file")
			return Decision{}, ErrUnavailable
		}
		calls++
		return Decision{Allowed: true, ContentSafe: true, Role: "human", PrincipalID: "policy-human", DecisionID: "synthetic", Scope: in.Scope, Context: in.Context}, nil
	})
	in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA, Text: strings.Repeat("x", 64000)}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}
	if _, err := f.svc.IngestFile(testCtx, "synthetic-caller", in, []byte(body)); err != nil {
		t.Fatal("maximum text upload policy round-trip", err)
	}
	if calls != 2 {
		t.Fatal("missing full policy decisions", calls)
	}
}

func TestFileIngestHTTP(t *testing.T) { exerciseFileIngestHTTP(t, newFakeRepo()) }

func TestFileIngestRefusesBeforeStorage(t *testing.T) {
	for _, name := range []string{"secret", "secret_redact", "policy_unsafe", "artifact_denied", "changed_actor", "agent", "oversize", "digest", "binary", "invalid_utf8", "scope", "secret_scope", "unset"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			dir := blobTestDir(t)
			f.svc.BlobDirectory = dir
			policy := &filePolicy{Authorizer: f.auth}
			f.svc.Auth = policy
			in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "tool_output", MIME: "text/plain"}}
			body, token, status := []byte("ordinary file"), f.human, 403
			switch name {
			case "secret", "secret_redact":
				body = []byte("password=synthetic-file-secret")
				status = 422
				if name == "secret_redact" {
					in.Source.OnSecret = "redact"
				}
			case "policy_unsafe":
				policy.unsafe = true
			case "artifact_denied":
				policy.denyFile = true
			case "changed_actor":
				policy.changedActor = true
			case "agent":
				token = f.agent
			case "oversize":
				body = bytes.Repeat([]byte("a"), maxTextArtifactBytes+1)
				status = 413
			case "digest":
				in.Artifact.Digest = strings.Repeat("0", 64)
				status = 422
			case "binary":
				in.Artifact.Kind = "pdf"
				in.Artifact.MIME = "application/pdf"
				body = []byte("%PDF-1.7")
				status = 400
			case "invalid_utf8":
				body = []byte{0xff}
				status = 400
			case "scope":
				in.Source.Scope = Scope{}
				status = 400
			case "secret_scope":
				in.Source.Scope.ProjectID = "password=synthetic"
				status = 422
			case "unset":
				f.svc.BlobDirectory = ""
				status = 503
			}
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, fileRequest(t, in, body, token))
			if w.Code != status {
				t.Fatalf("status %d want %d: %s", w.Code, status, w.Body)
			}
			for _, col := range []string{"sources", "jobs", colArtifacts, colBlobs} {
				if len(f.repo.rows(col)) != 0 {
					t.Fatal("failed upload wrote metadata", col)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed upload touched storage", err)
			}
			if strings.HasPrefix(name, "secret") {
				for _, req := range policy.requests {
					if req.Content != "" {
						t.Fatal("blocked secret sent to policy")
					}
				}
				if strings.Contains(structuredText(f.repo.rows("audit_events")), "synthetic") {
					t.Fatal("secret in audit")
				}
			}
		})
	}
}

func TestFileIngestMetadataRedactionAndRecovery(t *testing.T) {
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA, Summary: "password=synthetic", OnSecret: "redact"}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}
	out, err := f.svc.IngestFile(testCtx, f.human, in, []byte("original code"))
	if err != nil || !out.Source.Inspection.Redacted || strings.Contains(out.Source.Summary, "synthetic") {
		t.Fatal("metadata redaction", err)
	}
	w := httptest.NewRecorder()
	RecoveryHandler(f.svc).ServeHTTP(w, fileRequest(t, in, []byte("original code"), f.human))
	if w.Code == 201 {
		t.Fatal("recovery accepted new file")
	}
	if _, err = f.svc.IngestFile(testCtx, f.human, in, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty file accepted")
	}
}

func TestFileIngestChunkedEpilogueLimit(t *testing.T) {
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}
	r := fileRequest(t, in, []byte("code"), f.human)
	r.Body = io.NopCloser(io.MultiReader(r.Body, strings.NewReader(strings.Repeat("x", maxFileRequestBytes))))
	r.ContentLength = -1
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, r)
	if w.Code != 413 || len(f.repo.rows("sources")) != 0 {
		t.Fatal("chunked epilogue bypassed limit", w.Code)
	}
}

type slowFilePolicy struct {
	Authorizer
	reached chan struct{}
	once    sync.Once
}

func (p *slowFilePolicy) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	p.once.Do(func() { close(p.reached) })
	return p.Authorizer.Authorize(ctx, token, in)
}

func TestFileIngestSlowBody(t *testing.T) {
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	p := &slowFilePolicy{Authorizer: f.auth, reached: make(chan struct{})}
	f.svc.Auth = p
	server := httptest.NewServer(handler(f.svc, 800*time.Millisecond))
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}
	r := fileRequest(t, in, []byte("slow-file-body"), f.human)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(raw, []byte("slow-file-body"))
	if cut < 0 {
		t.Fatal("missing test body")
	}
	if _, err = fmt.Fprintf(conn, "POST /v1/sources/ingest HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nContent-Length: %d\r\nAuthorization: Bearer %s\r\n\r\n", r.Header.Get("Content-Type"), len(raw), f.human); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Write(raw[:cut]); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("preflight did not run")
	}
	// Reading a slow network body must not hold the shared storage lock.
	ctx, cancel := context.WithTimeout(testCtx, 200*time.Millisecond)
	defer cancel()
	if err = f.svc.CheckBlobStorage(ctx); err != nil {
		t.Fatal("slow upload held file lock", err)
	}
	second := fileRequest(t, in, []byte("code"), f.human)
	second.URL.Scheme = "http"
	second.URL.Host = server.Listener.Addr().String()
	second.RequestURI = ""
	resp, err := server.Client().Do(second)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal("concurrent upload was not bounded", resp.StatusCode)
	}
	resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal("slow upload did not receive deadline response", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 400 && resp.StatusCode != 503 {
		t.Fatal("unexpected timeout response", resp.StatusCode)
	}
	if len(f.repo.rows("sources")) != 0 {
		t.Fatal("slow upload stored a source")
	}
	// The timed-out reader must release the gate, not merely return an outer 503.
	deadline := time.Now().Add(time.Second)
	for {
		next := fileRequest(t, in, []byte("code"), f.human)
		next.URL.Scheme, next.URL.Host, next.RequestURI = "http", server.Listener.Addr().String(), ""
		resp, err = server.Client().Do(next)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == 201 {
			break
		}
		if resp.StatusCode != 503 || time.Now().After(deadline) {
			t.Fatal("upload gate not released", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
