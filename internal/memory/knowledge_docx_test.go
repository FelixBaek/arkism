//go:build linux

package memory

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func testDOCX(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestKnowledgeDOCXIngestAndDefaultSearch(t *testing.T) {
	f := handoffFixture(t)
	for _, token := range []string{f.human, f.work} {
		a := f.auth.actors[token]
		a.verbs["retrieve"], a.verbs["read"] = true, true
		f.auth.actors[token] = a
	}
	in := knowledgeFixtureInput()
	in.Synthetic = false
	in.SourceKey, in.Filename, in.MIMEType, in.Content = "manual", "manual.docx", knowledgeDOCXMIME, ""
	in.ContentBase64 = testDOCX(t, `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>배송 안내</w:t></w:r></w:p><w:p><w:r><w:t>주문 뒤 사흘 안에 보내요.</w:t></w:r></w:p>`)
	ing, err := f.svc.IngestKnowledge(testCtx, f.human, in)
	if err != nil || len(ing.Chunks) != 1 || ing.Chunks[0].Knowledge.SectionPath != "배송 안내" {
		t.Fatal(err, ing.Chunks)
	}
	// Text and a Word file together, or a broken file, are refused.
	bad := in
	bad.Content = "x"
	if _, err := f.svc.IngestKnowledge(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	bad = in
	bad.SourceKey, bad.ContentBase64 = "broken", base64.StdEncoding.EncodeToString([]byte("not a zip"))
	if _, err := f.svc.IngestKnowledge(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	// Reviewed and shared with every agent: a search without a profile finds it.
	head, err := f.svc.SetKnowledgeState(testCtx, f.human, colKnowledgeSources, ing.Source.ID, KnowledgeStateInput{Scope: in.Scope, ExpectedRevision: ing.Source.Revision, Status: "active", TrustLevel: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.CreateKnowledgeBinding(testCtx, f.human, KnowledgeBindingInput{Scope: in.Scope, BindingKey: head.ID + ":default", KnowledgeSourceID: head.ID, SpecialistProfileID: knowledgeDefaultProfile, SpecialistProfileVersionID: "v1", SkillID: "default.v1", TrustRequirement: "reviewed", AccessMode: "cite_required"})
	if err != nil {
		t.Fatal(err)
	}
	b = knowledgeActivate(t, f, b)
	got, err := f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: in.Scope, Query: "배송"})
	if err != nil || len(got.BindingIDs) != 1 || got.BindingIDs[0] != b.ID || got.Receipt == nil || len(got.Receipt.Context.Items) != 1 {
		t.Fatal(err, got)
	}
}
