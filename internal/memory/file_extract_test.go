package memory

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const extractKoreanSource = "# 작업 규칙\n배포 전에 항상 테스트를 실행한다.\n\n선택 상태에는 주황 강조선을 쓰지 않는다."

func extractAnswer(claims ...string) string { return `{"claims":[` + strings.Join(claims, ",") + `]}` }

const extractRuleClaim = `{"content":"선택 상태에 주황 강조선을 쓰지 않는다","kind":"user","semantic_kind":"preference","evidence_kind":"user_statement","occurred_from":"2026-10-25","occurred_to":"2026-10-25","evidence_quote":"선택 상태에는 주황 강조선을 쓰지 않는다.","confidence":0.9}`

func extractRows(f *fixture) int {
	return len(f.repo.rows("sources")) + len(f.repo.rows("memory_candidates")) + len(f.repo.rows("jobs"))
}

func TestParseExtractedClaimsIsStrict(t *testing.T) {
	bad := []string{
		strings.Replace(extractRuleClaim, `"kind":"user"`, `"kind":"opinion"`, 1),
		strings.Replace(extractRuleClaim, `0.9}`, `0.9,"extra":1}`, 1),
		strings.Replace(extractRuleClaim, `0.9}`, `2}`, 1),
		strings.Replace(extractRuleClaim, `강조선을 쓰지 않는다."`, `강조선은 멋지다."`, 1), // quote not in the source
		strings.Replace(extractRuleClaim, `"semantic_kind":"preference"`, `"semantic_kind":"wish"`, 1),
		strings.Replace(extractRuleClaim, `0.9}`, `0.9,"locator":"page:9"}`, 1),
		`"just a string"`,
	}
	claims, skipped, ok := parseExtractedClaims("```json\n"+extractAnswer(append([]string{extractRuleClaim}, bad...)...)+"\n```", extractKoreanSource, "chunk:1")
	if !ok || len(claims) != 1 || len(skipped) != len(bad) || claims[0].Locator != "chunk:1" || claims[0].OccurredFrom != "2026-10-25" {
		t.Fatalf("claims %+v skipped %+v ok %v", claims, skipped, ok)
	}
	// A far-future or reversed event date is dropped; the claim itself stays.
	for _, dates := range [][2]string{{"2999-01-01", "2999-01-01"}, {"2026-10-25", "2026-10-01"}, {"someday", ""}} {
		claim := strings.Replace(extractRuleClaim, `"occurred_from":"2026-10-25","occurred_to":"2026-10-25"`, `"occurred_from":"`+dates[0]+`","occurred_to":"`+dates[1]+`"`, 1)
		claims, _, _ := parseExtractedClaims(extractAnswer(claim), extractKoreanSource, "chunk:1")
		if len(claims) != 1 || claims[0].OccurredFrom != "" || claims[0].OccurredTo != "" {
			t.Fatalf("%v kept: %+v", dates, claims)
		}
	}
	for _, answer := range []string{"not json", extractAnswer() + "{}", `{"claims":[],"note":"x"}`} {
		if _, _, ok := parseExtractedClaims(answer, extractKoreanSource, "chunk:1"); ok {
			t.Fatalf("accepted %q", answer)
		}
	}
}

func TestExtractPromptKeepsSourceLanguage(t *testing.T) {
	system, user := extractPrompt(extractKoreanSource, "")
	if !strings.Contains(system, "Korean stays Korean") || !strings.Contains(system, "never translate") || !strings.Contains(system, wikiDataRule) {
		t.Fatalf("system prompt: %s", system)
	}
	for _, word := range []string{"preferences", "prohibitions", "decisions", "lessons", "facts"} {
		if !strings.Contains(system, word) {
			t.Fatalf("prompt misses %s", word)
		}
	}
	if strings.Count(user, "<<<SOURCE_DATA>>>") != 1 || !strings.Contains(user, "주황 강조선") {
		t.Fatalf("user prompt: %s", user)
	}
}

func TestExtractPreviewFiltersPrivacyAndStoresNothing(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim))
	text := extractKoreanSource + "\n\n<private>집 비밀번호 메모</private>\n\napi_key=sk-abcdefghijklmnop1234"
	before := extractRows(f)
	out, err := f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: text, SourceLabel: "회의록"})
	if err != nil || len(out.Claims) != 1 || out.Claims[0].Content != "선택 상태에 주황 강조선을 쓰지 않는다" || len(out.Skipped) != 1 {
		t.Fatalf("preview %+v %v", out, err)
	}
	if len(llm.prompts) != 1 || strings.Contains(llm.prompts[0], "집 비밀번호") || strings.Contains(llm.prompts[0], "sk-abcdefghijklmnop1234") || !strings.Contains(llm.prompts[0], sessionPrivateMark) {
		t.Fatalf("private text reached the model: %v", llm.prompts)
	}
	if extractRows(f) != before {
		t.Fatal("preview stored rows")
	}
	if _, err = f.svc.ExtractPreview(testCtx, f.agent, ExtractPreviewInput{Scope: scopeA, Text: text}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent preview: %v", err)
	}
	f.svc.Wiki = nil
	if _, err = f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: text}); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("no model: %v", err)
	}
}

func TestFileMemoriesDryRunAndCapture(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer(extractRuleClaim))
	in := FileMemoriesInput{Scope: scopeA, Filename: "rules.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte(extractKoreanSource)), DryRun: true}
	dry, err := f.svc.ExtractFileMemories(testCtx, f.human, in)
	if err != nil || len(dry.Claims) != 1 || dry.SourceID != "" || dry.CandidatesCreated != 0 || extractRows(f) != 0 {
		t.Fatalf("dry run %+v %v rows %d", dry, err, extractRows(f))
	}
	in.DryRun = false
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, in)
	if err != nil || out.CandidatesCreated != 1 || out.SourceID == "" || out.Status != JobStatusDone || len(llm.prompts) != 2 {
		t.Fatalf("extract %+v %v", out, err)
	}
	cands := f.repo.rows("memory_candidates")
	if len(cands) != 1 || cands[0].Status != "candidate" || cands[0].Text != "선택 상태에 주황 강조선을 쓰지 않는다" || cands[0].MemoryContext == nil || cands[0].MemoryContext.ValidFrom != "" ||
		cands[0].Occurred == nil || !cands[0].Occurred.From.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, usageZone())) || !cands[0].Occurred.To.Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, usageZone())) {
		t.Fatalf("candidates %+v", cands)
	}
	found := false
	for _, ref := range cands[0].Sources {
		found = found || ref.SourceID == out.SourceID && ref.Locator == "chunk:1"
	}
	if !found || len(f.repo.rows("memories")) != 0 {
		t.Fatalf("source refs %+v", cands[0].Sources)
	}
	if _, err = f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "x.exe", ContentBase64: "AA=="}); !errors.Is(err, errUnsupportedFormat) {
		t.Fatalf("unsupported type: %v", err)
	}
}

func TestFileMemoriesLargeFileRunsAsJob(t *testing.T) {
	f, _ := wikiFixture(t, extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer())
	para := strings.Repeat("배포 전에 테스트를 실행한다. ", 200)
	text := strings.Join([]string{para, para, para, para, para}, "\n\n")
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "long.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte(text))})
	if err != nil || out.JobID == "" || out.Status != JobStatusPending {
		t.Fatalf("queue %+v %v", out, err)
	}
	f.auth.actors[f.human2].verbs["generate"] = true
	if _, err = f.svc.RunFileExtractJob(testCtx, f.human2, scopeA, out.JobID); !errors.Is(err, ErrDenied) {
		t.Fatalf("another person ran the job: %v", err)
	}
	ran, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, out.JobID)
	if err != nil || ran.Status != JobStatusDone || ran.Job.Next != ran.Job.Total || ran.Job.Total < 3 || ran.Job.Calls != ran.Job.Total {
		t.Fatalf("run %+v %v", ran, err)
	}
	got, err := f.svc.GetFileExtractJob(testCtx, f.human, scopeA, out.JobID)
	stored, _ := f.repo.Get(testCtx, "jobs", scopeA, out.JobID)
	if err != nil || got.Status != JobStatusDone || slices.ContainsFunc(stored.FileExtract.Chunks, func(c FileExtractChunk) bool { return c.Text != "" }) {
		t.Fatalf("get %+v %v", got, err)
	}
}

func TestKnowledgeSourceExtractMemories(t *testing.T) {
	quote := `"evidence_quote":"Bleed 3 mm."`
	claim := `{"content":"Bleed is 3 mm","kind":"semantic","semantic_kind":"fact","evidence_kind":"inference",` + quote + `,"confidence":0.7}`
	f, _ := wikiFixture(t, extractAnswer(claim))
	src := wikiSource(t, f, "print", knowledgeFixtureInput().Content)
	out, err := f.svc.ExtractKnowledgeMemories(testCtx, f.human, src.ID, KnowledgeExtractInput{Scope: src.Scope})
	if err != nil || out.CandidatesCreated != 1 || out.SourceID != src.SourceEventID {
		t.Fatalf("knowledge extract %+v %v", out, err)
	}
}

// tinyPDF writes a two-page PDF: a plain Latin page and a Flate-compressed
// page whose Type0 font maps codes to Korean through a ToUnicode CMap.
func tinyPDF(t *testing.T, extra string) []byte {
	t.Helper()
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, _ = w.Write([]byte("BT /F2 12 Tf 72 700 Td <00010002> Tj 0 -14 Td [<0001> -400 <0002>] TJ ET"))
	_ = w.Close()
	cmap := "/CIDInit /ProcSet findresource begin\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n" +
		"1 beginbfchar <0001> <D55C> endbfchar\n1 beginbfrange <0002> <0002> <AD6D> endbfrange\nendcmap"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 /Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /Contents 7 0 R >>",
		"<< /Type /Page /Parent 2 0 R /Contents 8 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /Dotum /ToUnicode 9 0 R >>",
		stream("", "BT /F1 12 Tf 72 700 Td (Hello \\(PDF\\)) Tj T* (second line) Tj ET"),
		stream("/Filter /FlateDecode", z.String()),
		stream("", cmap),
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	for i, o := range objs {
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	b.WriteString("trailer\n<< /Root 1 0 R " + extra + ">>\n%%EOF\n")
	return []byte(b.String())
}

func stream(filter, data string) string {
	return fmt.Sprintf("<< /Length %d %s >>\nstream\n%s\nendstream", len(data), filter, data)
}

func TestPDFPagesText(t *testing.T) {
	pages, err := pdfPagesText(tinyPDF(t, ""))
	if err != nil || len(pages) != 2 || pages[0] != "Hello (PDF)\nsecond line" || pages[1] != "한국\n한 국" {
		t.Fatalf("pages %q %v", pages, err)
	}
	if _, err = pdfPagesText(tinyPDF(t, "/Encrypt 10 0 R ")); !errors.Is(err, errPDFNoText) {
		t.Fatalf("encrypted: %v", err)
	}
	scanned := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] >>\nendobj\n3 0 obj\n<< /Type /Page >>\nendobj\n")
	if _, err = pdfPagesText(scanned); !errors.Is(err, errPDFNoText) {
		t.Fatalf("scanned: %v", err)
	}
	if _, err = pdfPagesText([]byte("not a pdf")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not pdf: %v", err)
	}
}

func TestFileMemoriesReadsPDFPages(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(), extractAnswer())
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "doc.pdf", MIME: "application/pdf", ContentBase64: base64.StdEncoding.EncodeToString(tinyPDF(t, "")), DryRun: true})
	if err != nil || len(llm.prompts) != 2 || !strings.Contains(llm.prompts[1], "한국") || extractRows(f) != 0 {
		t.Fatalf("pdf %+v %v %v", out, err, llm.prompts)
	}
}

func TestFileExtractJobLeavesStoppedJobAlone(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(), extractAnswer(), extractAnswer())
	para := strings.Repeat("배포 전에 테스트를 실행한다. ", 200)
	text := strings.Join([]string{para, para, para, para, para}, "\n\n")
	out, err := f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "long.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte(text))})
	if err != nil || out.JobID == "" {
		t.Fatalf("queue %+v %v", out, err)
	}
	job, _ := f.repo.Get(testCtx, "jobs", scopeA, out.JobID)
	job.Status = "cancelled" // an operator stop this package does not know
	if err = f.repo.Replace(testCtx, "jobs", job, job.Revision); err != nil {
		t.Fatal(err)
	}
	ran, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, out.JobID)
	if err != nil || ran.Status != "cancelled" || len(llm.prompts) != 0 {
		t.Fatalf("stopped job ran %+v %v %d", ran, err, len(llm.prompts))
	}
}

func TestDiffExtractChunks(t *testing.T) {
	ch := func(loc, hash, src string, cands ...string) FileExtractChunk {
		return FileExtractChunk{Locator: loc, Hash: hash, Text: "t-" + hash, Source: Ref{Collection: "sources", ID: src, Revision: 1}, Candidates: cands}
	}
	prev := []FileExtractChunk{ch("chunk:1", "a", "s1", "c1"), ch("chunk:2", "b", "s1", "c2", "c3"), ch("chunk:3", "c", "s1")}
	next := []FileExtractChunk{ch("chunk:1", "b", ""), ch("chunk:2", "d", ""), ch("chunk:3", "a", "")}
	fresh, kept, delta := diffExtractChunks(prev, next)
	if len(fresh) != 1 || fresh[0].Hash != "d" || fresh[0].Text != "t-d" || len(kept) != 2 || kept[0].Text != "" || kept[0].Source.ID != "s1" || !slices.Equal(kept[0].Candidates, []string{"c2", "c3"}) || kept[1].Locator != "chunk:3" {
		t.Fatalf("fresh %+v kept %+v", fresh, kept)
	}
	if delta.ChunksTotal != 3 || delta.ChunksUnchanged != 2 || delta.ChunksChanged != 1 || delta.ChunksRemoved != 1 || delta.RemovedSources[0].Locator != "chunk:3" || len(delta.RemovedSources[0].CandidateIDs) != 0 {
		t.Fatalf("delta %+v", delta)
	}
	fresh, kept, delta = diffExtractChunks(nil, next)
	if len(fresh) != 3 || len(kept) != 0 || delta.ChunksChanged != 3 || delta.RemovedSources == nil {
		t.Fatalf("first upload %+v", delta)
	}
}

func extractUpload(t *testing.T, f *fixture, paras ...string) FileMemoriesResult {
	t.Helper()
	return extractUploadAs(t, f, f.human, paras...)
}

func extractUploadAs(t *testing.T, f *fixture, token string, paras ...string) FileMemoriesResult {
	t.Helper()
	out, err := f.svc.ExtractFileMemories(testCtx, token, FileMemoriesInput{Scope: scopeA, Filename: "rules.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte(strings.Join(paras, "\n\n")))})
	if err != nil || out.Delta == nil || out.JobID == "" {
		t.Fatalf("upload %+v %v", out, err)
	}
	return out
}

func TestFileExtractReuploadOnlyExtractsChanges(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer(), extractAnswer())
	filler := strings.Repeat(" 배포 전에 테스트를 실행한다.", 200) // one paragraph fills most of a chunk
	p1, p2, p3 := "선택 상태에는 주황 강조선을 쓰지 않는다."+filler, "둘째 문단."+filler, "셋째 문단."+filler
	first := extractUpload(t, f, p1, p2)
	if first.Status != JobStatusDone || first.CandidatesCreated != 1 || len(llm.prompts) != 2 || first.Delta.ChunksTotal != 2 || first.Delta.ChunksChanged != 2 {
		t.Fatalf("first %+v %+v", first, first.Delta)
	}
	cands := f.repo.rows("memory_candidates")
	same := extractUpload(t, f, p1, p2)
	if len(llm.prompts) != 2 || same.CandidatesCreated != 0 || same.Status != JobStatusDone || same.JobID == first.JobID || same.Delta.ChunksUnchanged != 2 || same.Delta.ChunksChanged != 0 || same.Delta.ChunksRemoved != 0 {
		t.Fatalf("unchanged upload called the model: %d %+v %+v", len(llm.prompts), same, same.Delta)
	}
	changed := extractUpload(t, f, p3, p2)
	d := changed.Delta
	if len(llm.prompts) != 3 || !strings.Contains(llm.prompts[2], "셋째 문단") || d.ChunksUnchanged != 1 || d.ChunksChanged != 1 || d.ChunksRemoved != 1 ||
		d.RemovedSources[0].Locator != "chunk:1" || !slices.Equal(d.RemovedSources[0].CandidateIDs, []string{cands[0].ID}) {
		t.Fatalf("changed upload %d %+v %+v", len(llm.prompts), changed, d)
	}
	if left := f.repo.rows("memory_candidates"); len(left) != 1 || left[0].Status != "candidate" {
		t.Fatalf("a removed chunk touched its candidate: %+v", left)
	}
}

func TestFileExtractReuploadAfterCancelMakesNewJob(t *testing.T) {
	f, llm := wikiFixture(t)
	para := strings.Repeat("배포 전에 테스트를 실행한다. ", 200)
	paras := []string{para, para, para, para, para}
	out := extractUpload(t, f, paras...)
	job, _ := f.repo.Get(testCtx, "jobs", scopeA, out.JobID)
	job.Status = "cancelled"
	if err := f.repo.Replace(testCtx, "jobs", job, job.Revision); err != nil {
		t.Fatal(err)
	}
	again := extractUpload(t, f, paras...)
	if again.JobID == out.JobID || again.Status != JobStatusPending || again.Delta.ChunksChanged != again.Delta.ChunksTotal || len(llm.prompts) != 0 {
		t.Fatalf("re-upload after cancel %+v %+v", again, again.Delta)
	}
}

func TestFileExtractReuploadIsPerUploader(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim), extractAnswer())
	a := f.auth.actors[f.human2]
	a.verbs["generate"] = true
	f.auth.actors[f.human2] = a
	para := "선택 상태에는 주황 강조선을 쓰지 않는다." + strings.Repeat(" 배포 전에 테스트를 실행한다.", 200)
	extractUpload(t, f, para)
	other := extractUploadAs(t, f, f.human2, "다른 사람의 문서."+strings.Repeat(" 다른 내용.", 300))
	if other.Delta.ChunksChanged != other.Delta.ChunksTotal || other.Delta.ChunksRemoved != 0 || len(llm.prompts) != 2 {
		t.Fatalf("another uploader saw the first upload: %d %+v", len(llm.prompts), other.Delta)
	}
}

// cancelOnCallLLM cancels the job through the operations route during its first model call.
type cancelOnCallLLM struct {
	*fakeWikiLLM
	cancel func()
}

func (c *cancelOnCallLLM) Complete(ctx context.Context, system, user string, max int) (string, ProviderUsage, error) {
	if len(c.prompts) == 0 {
		c.cancel()
	}
	return c.fakeWikiLLM.Complete(ctx, system, user, max)
}

func TestFileExtractCancelStopsTheRunningJob(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer(), extractAnswer())
	f.auth.actors[f.human].verbs["ops"] = true
	para := strings.Repeat("배포 전에 테스트를 실행한다. ", 200)
	paras := []string{para, para, para, para, para}
	out := extractUpload(t, f, paras...)
	if out.Status != JobStatusPending {
		t.Fatalf("expected a queued job %+v", out)
	}
	f.svc.Wiki = &cancelOnCallLLM{fakeWikiLLM: llm, cancel: func() {
		if _, err := f.svc.CancelOperation(testCtx, f.human, out.JobID, OperationChangeInput{Scope: scopeA}); err != nil {
			t.Error(err)
		}
	}}
	if _, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, out.JobID); !errors.Is(err, ErrConflict) {
		t.Fatalf("run after cancel: %v", err)
	}
	job, _ := f.repo.Get(testCtx, "jobs", scopeA, out.JobID)
	if len(llm.prompts) != 1 || job.Status != JobStatusCancelled {
		t.Fatalf("model calls %d, status %s", len(llm.prompts), job.Status)
	}
	if v, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, out.JobID); err != nil || v.Status != JobStatusCancelled || len(llm.prompts) != 1 {
		t.Fatalf("cancelled job ran again %+v %v", v, err)
	}
	if _, err := f.svc.RetryOperation(testCtx, f.human, out.JobID, OperationChangeInput{Scope: scopeA}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("operator retry of an extraction: %v", err)
	}
	if again := extractUpload(t, f, paras...); again.JobID == out.JobID {
		t.Fatal("re-upload reused the cancelled job")
	}
}
