package memory

import "testing"

func TestSkillRouteKeepsTheRivalThatAlsoFit(t *testing.T) {
	cands := []SkillCandidate{{ID: "a", Name: "pdf-read"}, {ID: "b", Name: "pdf-tools"}, {ID: "c", Name: "docx"}, {ID: "d", Name: "unsure"}}
	out := RouteResult{SelectedID: "a", Provider: "clef-flash", Judgments: []SkillJudgment{
		{ID: "a", Suitability: 3, Confidence: 0.9}, {ID: "b", Suitability: 2, Confidence: 0.8},
		{ID: "c", Suitability: 1, Confidence: 1}, {ID: "d", Suitability: 3, Confidence: 0.4}}}
	sr := newSkillRoute(out, cands, "")
	if sr.Rival != "b" || sr.RivalName != "pdf-tools" || sr.SelectedName != "pdf-read" {
		t.Fatalf("rival = %q %q, selected name %q", sr.Rival, sr.RivalName, sr.SelectedName)
	}
	// Nothing else fit: no rival. A withheld pick keeps the judge's best guess and still looks for a rival.
	out.Judgments = out.Judgments[:1]
	if sr := newSkillRoute(out, cands, ""); sr.Rival != "" {
		t.Fatalf("rival without a second fit: %q", sr.Rival)
	}
	out = RouteResult{Provider: "clef-flash", Degraded: []string{"jev_uncertain"}, Judgments: []SkillJudgment{
		{ID: "a", Suitability: 2, Confidence: 0.6}, {ID: "b", Suitability: 3, Confidence: 0.9}}}
	if sr := newSkillRoute(out, cands, ""); sr.Selected != "b" || sr.Rival != "a" {
		t.Fatalf("withheld: selected %q rival %q", sr.Selected, sr.Rival)
	}
}
