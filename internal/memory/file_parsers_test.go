package memory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type hostedFunc func(ctx context.Context, ext, mime string, raw []byte) (string, error)

func (h hostedFunc) Parse(ctx context.Context, ext, mime string, raw []byte) (string, error) {
	return h(ctx, ext, mime, raw)
}

func TestFileParserOrderAndAllowlist(t *testing.T) {
	f := FileSettings{Hosted: map[string]HostedParser{"upload_job": hostedFunc(nil)}}
	order, err := f.parserOrder(nil)
	if err != nil || !slices.Equal(order, []string{"builtin", "converter", "vision", "transcribe", "upload_job"}) {
		t.Fatal(order, err)
	}
	if got, err := f.parserOrder([]string{"upload_job", "builtin"}); err != nil || got[0] != "upload_job" {
		t.Fatal(got, err)
	}
	f.Allowed = []string{"builtin"}
	for _, bad := range [][]string{{"vision"}, {"nope"}, {"builtin", "builtin"}} {
		if _, err := f.parserOrder(bad); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
	t.Setenv("MEMORY_FILE_MAX_BYTES", "1048576")
	t.Setenv("MEMORY_FILE_PARSERS", "vision,builtin")
	env, err := FileSettingsFromEnv()
	if err != nil || env.maxBytes() != 1<<20 || env.batchFiles() != fileBatchFilesDefault || !slices.Equal(env.Parsers, []string{"vision", "builtin"}) {
		t.Fatal(env, err)
	}
	for name, value := range map[string]string{"MEMORY_FILE_MAX_BYTES": "999999999999", "MEMORY_FILE_PARSERS": "builtin,remote", "MEMORY_FILE_BATCH_MAX_FILES": "0"} {
		t.Setenv(name, value)
		if _, err := FileSettingsFromEnv(); !errors.Is(err, ErrInvalid) {
			t.Fatal(name, err)
		}
		t.Setenv(name, "")
	}
}

func TestFileSizeLimitIsConfigurable(t *testing.T) {
	f, _ := mediaFixture(t)
	in := mediaUpload("a.txt", []byte("0123456789A"))
	in.DryRun = true
	f.svc.Files.MaxBytes = 10
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); !errors.Is(err, errBlobTooLarge) {
		t.Fatal(err)
	}
	if f.svc.Files.requestBytes() != int64(base64.StdEncoding.EncodedLen(10))+fileRequestJSONOverhead {
		t.Fatal(f.svc.Files.requestBytes())
	}
}

// A request may put an external reader first; a file it cannot read falls to
// the next reader, and nothing leaves without the transfer check.
func TestHostedParserChain(t *testing.T) {
	f, llm := mediaFixture(t, extractAnswer(), extractAnswer())
	var calls []string
	f.svc.Files.Hosted = map[string]HostedParser{
		"upload_job": hostedFunc(func(_ context.Context, ext, mime string, raw []byte) (string, error) {
			calls = append(calls, ext+" "+mime+" "+string(raw))
			if ext == ".txt" {
				return "", errUnsupportedFormat
			}
			return "외부에서 읽은 본문", nil
		}),
	}
	in := mediaUpload("notes.rtf", []byte("{\\rtf1 x}"))
	in.Parsers = []string{"upload_job"}
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); err != nil || len(calls) != 1 || calls[0] != ".rtf application/octet-stream {\\rtf1 x}" {
		t.Fatal(err, calls)
	}
	if !strings.Contains(llm.prompts[0], "외부에서 읽은 본문") {
		t.Fatal(llm.prompts)
	}
	// The external reader cannot read this one, so the built-in reader does.
	fallback := mediaUpload("a.txt", []byte("내장 읽기"))
	fallback.Parsers = []string{"upload_job", "builtin"}
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, fallback); err != nil || len(calls) != 2 || !strings.Contains(llm.prompts[1], "내장 읽기") {
		t.Fatal(err, calls)
	}
	// A dry run never sends the file out, so the chain stops before the external reader.
	fallback.DryRun = true
	if out, err := f.svc.ExtractFileMemories(testCtx, f.human, fallback); err != nil || len(out.Skipped) != 1 || out.Skipped[0].Reason != "dry_run_media" || len(calls) != 2 {
		t.Fatal(out, err, calls)
	}
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, a Authorization) (Decision, error) {
		d, e := f.auth.Authorize(ctx, token, a)
		if a.Action == "transfer:model_input" {
			d.ExternalTransfer = false
		}
		return d, e
	})
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); !errors.Is(err, ErrDenied) || len(calls) != 2 {
		t.Fatal(err, calls)
	}
}

func TestUploadJobParserProtocol(t *testing.T) {
	hostedPollInterval = time.Millisecond
	t.Cleanup(func() { hostedPollInterval = 2 * time.Second })
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/upload":
			file, header, err := r.FormFile("file")
			raw, _ := io.ReadAll(file)
			if err != nil || header.Filename != "document.pdf" || string(raw) != "PDF" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":"job-1"}`))
		case r.URL.Path == "/v1/job-1" && r.URL.Query().Get("expand") == "markdown_full":
			_, _ = w.Write([]byte(`{"markdown_full":"# 제목\n본문"}`))
		case r.URL.Path == "/v1/job-1":
			status := "PENDING"
			if polls.Add(1) > 1 {
				status = "COMPLETED"
			}
			_, _ = w.Write([]byte(`{"job":{"status":"` + status + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &UploadJobParser{Base: srv.URL + "/v1", APIKey: "k"}
	text, err := p.Parse(testCtx, ".pdf", "application/pdf", []byte("PDF"))
	if err != nil || text != "# 제목\n본문" || polls.Load() != 2 {
		t.Fatal(text, err, polls.Load())
	}
	if _, err := p.Parse(testCtx, ".zip", "application/zip", []byte("ZIP")); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("400 is unsupported: %v", err)
	}
}

func TestPresignedJobParserProtocol(t *testing.T) {
	hostedPollInterval = time.Millisecond
	t.Cleanup(func() { hostedPollInterval = 2 * time.Second })
	var uploaded string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signed" {
			if r.Method != http.MethodPut || r.Header.Get("Authorization") != "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			raw, _ := io.ReadAll(r.Body)
			uploaded = string(raw)
			return
		}
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/org/o1/files":
			if body["name"] != "document.png" || body["contentType"] != "image/png" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_, _ = w.Write([]byte(`{"fileId":"f1","uploadUrl":"` + srv.URL + `/signed"}`))
		case "/org/o1/extraction":
			_, _ = w.Write([]byte(`{"extractionId":"e1"}`))
		case "/org/o1/extraction/e1":
			_, _ = w.Write([]byte(`{"ready":true,"data":{"success":true,"text":"그림 속 글"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &PresignedJobParser{Base: srv.URL, Org: "o1", Token: "t"}
	text, err := p.Parse(testCtx, ".png", "image/png", []byte("PNG"))
	if err != nil || text != "그림 속 글" || uploaded != "PNG" {
		t.Fatal(text, err, uploaded)
	}
	if _, err := p.Parse(testCtx, ".gif", "image/gif", []byte("GIF")); !errors.Is(err, errUnsupportedFormat) {
		t.Fatal(err)
	}
}

// A knowledge upload of an image is read by the opted-in vision model.
func TestKnowledgeImageReadByVision(t *testing.T) {
	f, llm := mediaFixture(t)
	in := knowledgeFixtureInput()
	in.Synthetic = false
	in.SourceKey, in.Filename, in.MIMEType, in.Content = "board", "board.png", "image/png", ""
	in.ContentBase64 = base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	if _, err := f.svc.IngestKnowledge(testCtx, f.human, in); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("without vision an image is unsupported: %v", err)
	}
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"화이트보드: 금요일 배포 금지"}}
	f.svc.Wiki = v
	got, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil || len(got.Chunks) != 1 || !strings.Contains(got.Chunks[0].Text, "금요일 배포 금지") || len(v.images) != 1 {
		t.Fatal(err, got.Chunks)
	}
}
