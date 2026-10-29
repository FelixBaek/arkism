package memory

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSkillVersionZipExport(t *testing.T) {
	f := newFixture(t)
	secret := SkillBundleFile{Path: "config/env.txt", Omitted: "secret", Size: 12, SHA256: ContentHash("API_KEY=abc\n")}
	bundle := bundleForTest(SkillBundleFile{Path: "scripts/run.py", Content: "print('한국어')\n"})
	bundle.Files = []SkillBundleFile{secret, bundle.Files[0]}
	bundle.SHA256 = skillBundleHash(bundle.Files)
	in := ImportSkillInput{Scope: scopeA, SkillID: "zip-review", Path: "/skills/zip-review/SKILL.md", Content: "# Review\nRead scripts/run.py.", Analysis: detailedSkillAnalysis(), Bundle: bundle}
	v, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(f.svc)
	get := func(token, extra string) *httptest.ResponseRecorder {
		q := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}.Encode() + extra
		r := httptest.NewRequest("GET", "/v1/skill_versions/"+v.Record.ID+"/zip?"+q, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get(f.human, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != `attachment; filename=zip-review.zip` {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	files := readTestZip(t, w.Body.Bytes())
	if len(files) != 3 || files["zip-review/SKILL.md"] != in.Content || files["zip-review/scripts/run.py"] != "print('한국어')\n" ||
		!strings.Contains(files["zip-review/OMITTED.txt"], "config/env.txt (secret)") || strings.Contains(files["zip-review/OMITTED.txt"], "API_KEY") {
		t.Fatal(files)
	}
	// The desktop app's bridge reads answers as JSON text: base64 carries the same bytes.
	b := get(f.human, "&format=json")
	var wrapped SkillZipExport
	if b.Code != 200 || json.Unmarshal(b.Body.Bytes(), &wrapped) != nil || wrapped.Filename != "zip-review.zip" {
		t.Fatal(b.Code, b.Body.String())
	}
	if raw, err := base64.StdEncoding.DecodeString(wrapped.ContentBase64); err != nil || !bytes.Equal(raw, w.Body.Bytes()) {
		t.Fatal("json bytes differ", err)
	}
	if bad := get(f.human, "&format=xml"); bad.Code != 400 {
		t.Fatal("unknown format", bad.Code)
	}
	if missing := get(f.human, "&x=1"); missing.Code != 400 {
		t.Fatal("unknown query key", missing.Code)
	}
}

func readTestZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(body)
	}
	return out
}
