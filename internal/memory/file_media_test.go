package memory

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeVisionLLM reads images with canned text; it is a fakeWikiLLM otherwise.
type fakeVisionLLM struct {
	*fakeWikiLLM
	reads  []string
	images [][]byte
}

func (f *fakeVisionLLM) ImageInput() bool { return true }

func (f *fakeVisionLLM) CompleteImage(_ context.Context, system, user, mime string, image []byte, _ int) (string, ProviderUsage, error) {
	f.images = append(f.images, image)
	if len(f.images) > len(f.reads) {
		return "", ProviderUsage{}, ErrUnavailable
	}
	return f.reads[len(f.images)-1], ProviderUsage{Provider: wikiProvider, Model: "fake-wiki", Requests: 1, InputTokens: 900, OutputTokens: 40}, nil
}

func mediaFixture(t *testing.T, answers ...string) (*fixture, *fakeWikiLLM) {
	f, llm := wikiFixture(t, answers...)
	a := f.auth.actors[f.human]
	a.verbs["transfer"] = true
	f.auth.actors[f.human] = a
	return f, llm
}

func mediaUpload(name string, raw []byte) FileMemoriesInput {
	return FileMemoriesInput{Scope: scopeA, Filename: name, ContentBase64: base64.StdEncoding.EncodeToString(raw)}
}

func ledgerOps(f *fixture) []string {
	ops := []string{}
	for _, r := range f.repo.rows(colProviderUsage) {
		if r.Cost != nil {
			ops = append(ops, r.Cost.Provider+"/"+r.Cost.Operation)
		}
	}
	return ops
}

func TestFileMediaRefusedWithoutOptIn(t *testing.T) {
	f, llm := mediaFixture(t)
	for _, name := range []string{"a.png", "a.jpg", "a.webp", "a.mp3", "a.wav"} {
		if _, err := f.svc.ExtractFileMemories(testCtx, f.human, mediaUpload(name, []byte("bytes"))); !errors.Is(err, errUnsupportedFormat) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if code, status := errorResponse(fmt.Errorf("x: %w", errUnsupportedFormat)); status != 415 || code != "unsupported_media_type" {
		t.Fatal(code, status)
	}
	if len(llm.prompts) != 0 || len(ledgerOps(f)) != 0 {
		t.Fatal("a model was called")
	}
	in := mediaUpload("a.png", []byte("x"))
	in.MIME = "image/jpeg"
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mime mismatch: %v", err)
	}
}

func TestFileImageReadThenPrivacyFilteredToCandidates(t *testing.T) {
	quote := `"evidence_quote":"금요일 배포 금지"`
	claim := `{"content":"금요일에는 배포하지 않는다","kind":"decision","semantic_kind":"preference","evidence_kind":"inference",` + quote + `,"confidence":0.8}`
	f, llm := mediaFixture(t, extractAnswer(claim))
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"금요일 배포 금지\n<private>집 주소 서울</private>\napi_key=sk-abcdefghijklmnop1234"}}
	f.svc.Wiki = v
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, mediaUpload("board.png", []byte("PNGDATA")))
	if err != nil || out.CandidatesCreated != 1 || len(v.images) != 1 || string(v.images[0]) != "PNGDATA" {
		t.Fatalf("%+v %v", out, err)
	}
	if p := llm.prompts[0]; strings.Contains(p, "집 주소") || strings.Contains(p, "sk-abcdefghijklmnop1234") || !strings.Contains(p, "금요일 배포 금지") {
		t.Fatalf("unfiltered text reached extraction: %q", p)
	}
	if c := f.repo.rows("memory_candidates"); len(c) != 1 || c[0].Status != "candidate" {
		t.Fatalf("candidates %+v", c)
	}
	if ops := strings.Join(ledgerOps(f), ","); !strings.Contains(ops, "wiki_llm/wiki_file_vision") || !strings.Contains(ops, "wiki_llm/"+fileExtractOperation) {
		t.Fatalf("ledger %s", ops)
	}
}

func TestFileImageTransferDeniedSendsNothing(t *testing.T) {
	f, llm := mediaFixture(t)
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"x"}}
	f.svc.Wiki = v
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, a Authorization) (Decision, error) {
		d, e := f.auth.Authorize(ctx, token, a)
		if a.Action == "transfer:model_input" {
			d.ExternalTransfer = false
		}
		return d, e
	})
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, mediaUpload("a.jpg", []byte("JPG"))); !errors.Is(err, ErrDenied) || len(v.images) != 0 {
		t.Fatalf("%v %d", err, len(v.images))
	}
}

func scannedPDF(images ...string) []byte {
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>"}
	kids := []string{}
	for i, img := range images {
		page, image := 3+2*i, 4+2*i
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
		objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /Resources << /XObject << /Im0 %d 0 R >> >> >>", image),
			stream("/Type /XObject /Subtype /Image /Filter [/DCTDecode]", img))
	}
	objs = append(objs[:1], append([]string{"<< /Type /Pages /Kids [" + strings.Join(kids, " ") + "] >>"}, objs[1:]...)...)
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	for i, o := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	return []byte(b.String())
}

func TestScannedPDFPagesReadByVision(t *testing.T) {
	f, llm := mediaFixture(t, extractAnswer(), extractAnswer())
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, mediaUpload("scan.pdf", scannedPDF("JPEG1"))); !errors.Is(err, errPDFNoText) {
		t.Fatalf("without vision the scan has no text: %v", err)
	}
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"첫 쪽 글자", "둘째 쪽 글자"}}
	f.svc.Wiki = v
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, mediaUpload("scan.pdf", scannedPDF("JPEG1", "JPEG2")))
	if err != nil || len(v.images) != 2 || string(v.images[1]) != "JPEG2" || len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], "둘째 쪽 글자") {
		t.Fatalf("%+v %v %d %q", out, err, len(v.images), llm.prompts)
	}
	many := make([]string, fileVisionMaxPages+2)
	for i := range many {
		many[i] = fmt.Sprintf("J%d", i)
	}
	images, _ := pdfPageImages(scannedPDF(many...))
	if len(images) != len(many) || string(images[9]) != "J9" {
		t.Fatalf("page images %q", images)
	}
}

func TestFileAudioTranscribedThroughEndpoint(t *testing.T) {
	var gotModel, gotName, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		gotModel = r.FormValue("model")
		file, head, err := r.FormFile("file")
		if err == nil {
			data, _ := io.ReadAll(file)
			gotName = head.Filename + ":" + string(data)
		}
		_, _ = io.WriteString(w, `{"text":"회의 결론: 금요일 배포 금지","usage":{"type":"duration","seconds":3}}`)
	}))
	defer srv.Close()
	t.Setenv("MEMORY_TRANSCRIBE_ENDPOINT", srv.URL+"/v1/audio/transcriptions")
	t.Setenv("MEMORY_TRANSCRIBE_MODEL", "speech-1")
	t.Setenv("MEMORY_TRANSCRIBE_API_KEY", "k")
	tr, err := TranscriberFromEnv()
	if err != nil || tr == nil {
		t.Fatal(err)
	}
	f, llm := mediaFixture(t, extractAnswer())
	f.svc.Transcriber = tr
	if _, err = f.svc.ExtractFileMemories(testCtx, f.human, mediaUpload("팀 회의 녹음.mp3", []byte("MP3BYTES"))); err != nil {
		t.Fatal(err)
	}
	if gotModel != "speech-1" || gotName != "audio.mp3:MP3BYTES" || gotAuth != "Bearer k" || !strings.Contains(llm.prompts[0], "금요일 배포 금지") {
		t.Fatalf("%q %q %q %q", gotModel, gotName, gotAuth, llm.prompts)
	}
	if ops := strings.Join(ledgerOps(f), ","); !strings.Contains(ops, "transcription/wiki_file_transcribe") {
		t.Fatalf("ledger %s", ops)
	}
	t.Setenv("MEMORY_TRANSCRIBE_ENDPOINT", "http://example.com/x")
	if _, err = TranscriberFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain http endpoint: %v", err)
	}
}

// A dry run is a free preview: images, scanned PDFs and audio are not sent to a
// model; the answer says the file needs a real run.
func TestFileMediaDryRunCallsNoModel(t *testing.T) {
	f, llm := mediaFixture(t)
	v := &fakeVisionLLM{fakeWikiLLM: llm, reads: []string{"x", "y"}}
	f.svc.Wiki = v
	f.svc.Transcriber = &HTTPTranscriber{Endpoint: "https://127.0.0.1:1/never", ModelName: "speech-1", APIKey: "k"}
	for _, in := range []FileMemoriesInput{mediaUpload("a.png", []byte("PNG")), mediaUpload("a.mp3", []byte("MP3")), mediaUpload("scan.pdf", scannedPDF("JPEG1"))} {
		in.DryRun = true
		out, err := f.svc.ExtractFileMemories(testCtx, f.human, in)
		if err != nil || out.Status != JobStatusDone || len(out.Skipped) != 1 || out.Skipped[0].Reason != "dry_run_media" || len(out.Claims) != 0 {
			t.Fatalf("%s: %+v %v", in.Filename, out, err)
		}
	}
	if len(v.images) != 0 || len(llm.prompts) != 0 || len(ledgerOps(f)) != 0 || len(f.repo.rows("memory_candidates")) != 0 {
		t.Fatalf("a dry run spent a model: images %d prompts %d ledger %v", len(v.images), len(llm.prompts), ledgerOps(f))
	}
	f.svc.Transcriber = nil
	in := mediaUpload("a.mp3", []byte("MP3"))
	in.DryRun = true
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("a dry run still says audio is off: %v", err)
	}
}
