//go:build linux

package memory

import (
	"bytes"
	"strings"
	"testing"
)

// A binary attachment larger than one bundle line travels as pieces and comes back byte for byte;
// one over the configured limit is reported as too_large, never dropped silently.
func TestSpaceBundleCarriesLargeBinaryAttachments(t *testing.T) {
	f := handoffFixture(t)
	f.svc.Files.BundleBytes = 700 << 10
	old := spaceFilePieceBytes
	spaceFilePieceBytes = 256 << 10
	t.Cleanup(func() { spaceFilePieceBytes = old })
	human := Decision{Allowed: true, Scope: scopeA, PrincipalID: "human-1", Role: roleHuman}
	attach := func(raw []byte) {
		src, err := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "scanned contract"}))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.svc.attachOriginal(testCtx, human, scopeA, Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}, "pdf", "application/pdf", raw); err != nil {
			t.Fatal(err)
		}
	}
	big := bytes.Repeat([]byte{0x25, 0x50, 0x44, 0x46, 0x00, 0xff}, (600<<10)/6)
	attach(big)
	attach(make([]byte, 1<<20))

	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Parts: []string{spaceSectionAttachments}})
	if strings.Count(bundle, `"parts":3`) != 3 || !strings.Contains(bundle, `"reason":"too_large"`) {
		t.Fatalf("pieces or too_large skip missing: %d pieces", strings.Count(bundle, `"parts":3`))
	}
	report := importBundle(t, f, scopeB, bundle, "")
	arts := scopeRecords(f, colArtifacts, scopeB)
	if report.Imported != 1 || len(arts) != 1 || arts[0].Artifact.Digest != ContentHash(string(big)) || arts[0].Artifact.MIME != "application/pdf" {
		t.Fatalf("binary attachment not restored: %+v %+v", report, arts)
	}
	if again := importBundle(t, f, scopeB, bundle, ""); len(scopeRecords(f, colArtifacts, scopeB)) != 1 || again.Imported+again.Merged != 1 {
		t.Fatalf("second import duplicated the attachment: %+v", again)
	}

	// A bundle missing a middle piece reports the file as incomplete.
	lines := strings.Split(strings.TrimSpace(bundle), "\n")
	cut := []string{}
	for _, l := range lines {
		if !strings.Contains(l, `"part":2`) {
			cut = append(cut, l)
		}
	}
	imp := &spaceImporter{s: f.svc, token: f.human, d: human, scope: scopeB}
	for _, l := range cut[1 : len(cut)-1] {
		line, err := decodeSpaceLine([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		if err = imp.add(testCtx, line); err != nil {
			t.Fatal(err)
		}
	}
	imp.dropPiece(spaceSectionAttachments)
	if !hasSkip(imp.report, spaceSectionAttachments, "incomplete_file") {
		t.Fatalf("missing piece not reported: %+v", imp.report)
	}
}

// Extracted documents travel with their chunk text and come back as a done
// document with new evidence sources, without a model call; importing twice merges.
func TestSpaceBundleCarriesExtractedDocuments(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim))
	out := extractUpload(t, f, "첫 규칙 문단입니다.", "둘째 문단입니다.")
	if _, err := f.svc.EditDocumentTags(testCtx, f.human, out.DocumentID, DocumentTagsInput{Scope: scopeA, Add: []string{"규칙"}}); err != nil {
		t.Fatal(err)
	}
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Parts: []string{spaceSectionDocuments}})
	for _, want := range []string{`"section":"documents"`, "첫 규칙 문단입니다.", `"ref":"file:rules.txt"`, `"tags":["규칙"]`} {
		if !strings.Contains(bundle, want) {
			t.Fatalf("bundle misses %q:\n%s", want, bundle)
		}
	}
	calls := len(llm.prompts)
	report := importBundle(t, f, scopeB, bundle, "")
	if report.Imported != 1 || len(llm.prompts) != calls {
		t.Fatalf("document import %+v (model calls %d -> %d)", report, calls, len(llm.prompts))
	}
	var doc Record
	for _, r := range scopeRecords(f, "jobs", scopeB) {
		if r.Kind == fileExtractDocKind {
			doc = r
		}
	}
	if doc.Target == nil || len(doc.Tags) != 1 || doc.CreatedBy != "human-1" {
		t.Fatalf("restored document %+v", doc)
	}
	job, err := f.svc.extractJob(testCtx, f.repo, scopeB, doc.Target.ID)
	if err != nil || job.Status != JobStatusDone {
		t.Fatalf("restored job %+v %v", job, err)
	}
	chunks, err := f.svc.storedChunks(testCtx, scopeB, job.FileExtract.Kept)
	if err != nil || len(chunks) == 0 || !strings.Contains(chunks[0].Text, "첫 규칙 문단입니다.") {
		t.Fatalf("restored chunks %+v %v", chunks, err)
	}
	if again := importBundle(t, f, scopeB, bundle, ""); again.Merged != 1 || again.Imported != 0 {
		t.Fatalf("second import %+v", again)
	}

	// An agent's export carries no documents.
	job2, err := f.svc.ExportSpace(testCtx, f.agent, scopeA, SpaceExportInput{Parts: []string{spaceSectionDocuments}})
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scopeA, job2.ID)
	if b, err := downloadBundle(f, f.agent, scopeA, job2.ID); err != nil || strings.Contains(b, "첫 규칙") || !strings.Contains(b, `"reason":"not_human"`) {
		t.Fatalf("agent export %v:\n%s", err, b)
	}
}

func hasSkip(r SpaceReport, section, reason string) bool {
	for _, s := range r.Skipped {
		if s.Section == section && s.Reason == reason {
			return true
		}
	}
	return false
}
