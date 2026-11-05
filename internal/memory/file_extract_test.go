package memory

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const extractKoreanSource = "# 작업 규칙\n배포 전에 항상 테스트를 실행한다.\n\n선택 상태에는 주황 강조선을 쓰지 않는다."

func extractAnswer(claims ...string) string { return `{"claims":[` + strings.Join(claims, ",") + `]}` }

const extractRuleClaim = `{"content":"선택 상태에 주황 강조선을 쓰지 않는다","kind":"user","semantic_kind":"preference","evidence_kind":"user_statement","observed_at":"2026-10-25","evidence_quote":"선택 상태에는 주황 강조선을 쓰지 않는다.","confidence":0.9}`

func extractRows(f *fixture) int {
	return len(f.repo.rows("sources")) + len(f.repo.rows("memory_candidates")) + len(f.repo.rows("jobs"))
}

func TestParseExtractedClaimsIsStrict(t *testing.T) {
	bad := []string{
		strings.Replace(extractRuleClaim, `"kind":"user"`, `"kind":"opinion"`, 1),
		strings.Replace(extractRuleClaim, `0.9}`, `0.9,"extra":1}`, 1),
		strings.Replace(extractRuleClaim, `0.9}`, `2}`, 1),
		strings.Replace(extractRuleClaim, `강조선을 쓰지 않는다."`, `강조선은 멋지다."`, 1), // quote not in the source
		strings.Replace(extractRuleClaim, `2026-10-25`, `2999-01-01`, 1),
		strings.Replace(extractRuleClaim, `"semantic_kind":"preference"`, `"semantic_kind":"wish"`, 1),
		strings.Replace(extractRuleClaim, `0.9}`, `0.9,"locator":"page:9"}`, 1),
		`"just a string"`,
	}
	claims, skipped, ok := parseExtractedClaims("```json\n"+extractAnswer(append([]string{extractRuleClaim}, bad...)...)+"\n```", extractKoreanSource, "chunk:1")
	if !ok || len(claims) != 1 || len(skipped) != len(bad) || claims[0].Locator != "chunk:1" || claims[0].ObservedAt != "2026-10-25" {
		t.Fatalf("claims %+v skipped %+v ok %v", claims, skipped, ok)
	}
	for _, answer := range []string{"not json", extractAnswer() + "{}", `{"claims":[],"note":"x"}`} {
		if _, _, ok := parseExtractedClaims(answer, extractKoreanSource, "chunk:1"); ok {
			t.Fatalf("accepted %q", answer)
		}
	}
}

func TestExtractPromptKeepsSourceLanguage(t *testing.T) {
	system, user := extractPrompt(extractKoreanSource)
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
	if _, err = f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: text}); !errors.Is(err, errModelNotConfigured) {
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
	if len(cands) != 1 || cands[0].Status != "candidate" || cands[0].Text != "선택 상태에 주황 강조선을 쓰지 않는다" || cands[0].MemoryContext == nil || cands[0].MemoryContext.ValidFrom != "2026-10-25" {
		t.Fatalf("candidates %+v", cands)
	}
	found := false
	for _, ref := range cands[0].Sources {
		found = found || ref.SourceID == out.SourceID && ref.Locator == "chunk:1"
	}
	if !found || len(f.repo.rows("memories")) != 0 {
		t.Fatalf("source refs %+v", cands[0].Sources)
	}
	if _, err = f.svc.ExtractFileMemories(testCtx, f.human, FileMemoriesInput{Scope: scopeA, Filename: "x.exe", ContentBase64: "AA=="}); !errors.Is(err, ErrInvalid) {
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
	ran, err := f.svc.RunFileExtractJob(testCtx, f.human, scopeA, out.JobID)
	if err != nil || ran.Status != JobStatusDone || ran.Job.Next != ran.Job.Total || ran.Job.Total < 3 || ran.Job.Calls != ran.Job.Total {
		t.Fatalf("run %+v %v", ran, err)
	}
	got, err := f.svc.GetFileExtractJob(testCtx, f.human, scopeA, out.JobID)
	stored, _ := f.repo.Get(testCtx, "jobs", scopeA, out.JobID)
	if err != nil || got.Status != JobStatusDone || len(stored.FileExtract.Chunks) != 0 {
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
