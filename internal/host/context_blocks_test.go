package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lines(prefix string, n, width int) string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, "- "+prefix+strings.Repeat("가", width))
	}
	return strings.Join(out, "\n")
}

// Over budget a block goes full -> meta -> stub, and every form is made of whole lines.
func TestAssemblePartsDegradesInThreeSteps(t *testing.T) {
	work, _ := RenderHookContext(contextState(), 8000)
	full := lines("[user] 긴 기억 ", 40, 200)
	meta := lines("[user] (id: m) ", 40, 0)
	got, err := AssembleParts(Part{Full: full, Meta: meta}, Part{Full: work}, Part{}, ContextMaxChars)
	if err != nil || Len(got) > ContextMaxChars || !strings.HasPrefix(got, meta+"\n\n") || strings.Contains(got, "긴 기억") {
		t.Fatalf("meta step: err %v len %d %.80q", err, Len(got), got)
	}
	tooBig := lines("[user] (id: m) ", 400, 0)
	got, _ = AssembleParts(Part{Full: full, Meta: tooBig}, Part{Full: work}, Part{}, ContextMaxChars)
	if !strings.HasPrefix(got, MemoryOmitted) || !strings.Contains(got, "recall") {
		t.Fatalf("stub step: %.80q", got)
	}
	// Everything fits: full form, nothing degraded.
	got, _ = AssembleParts(Part{Full: "m", Meta: "meta"}, Part{Full: "w"}, Part{Full: "c", Meta: "cm"}, ContextMaxChars)
	if got != "m\n\nw\n\nc" {
		t.Fatal(got)
	}
}

func crewText(n int) string {
	out := []string{"Arkism 워크플로우 정의(자료):", crewOpen}
	for i := 0; i < n; i++ {
		out = append(out, "[$리서치 — 워크플로우] 리서치 팀: 조사", "- 단계:", "  1. "+strings.Repeat("긴 단계 ", 60)+"(에이전트)")
	}
	out = append(out, crewClose, "Arkism 호출 안내:", "- $리서치 안내: run_workflow")
	return strings.Join(out, "\n")
}

func TestCrewMetaKeepsWholeFirstLinesOnly(t *testing.T) {
	text := crewText(20)
	meta := CrewMeta(text)
	if meta == "" || strings.Contains(meta, "긴 단계") || strings.Count(meta, "[$리서치 — 워크플로우] 리서치 팀: 조사") != 20 ||
		!strings.Contains(meta, crewClose) || !strings.HasPrefix(meta, "Arkism 워크플로우 정의(자료):\n"+crewOpen) {
		t.Fatal(meta)
	}
	if CrewMeta("plain text") != "" || CrewMeta(crewOpen+"\nno close") != "" {
		t.Fatal("non-crew text has a meta form")
	}
	work, _ := RenderHookContext(contextState(), 8000)
	got, _ := AssembleParts(Part{}, Part{Full: work}, Part{Full: text, Meta: meta}, ContextMaxChars)
	if !strings.Contains(got, meta) || strings.Contains(got, CrewOmitted) {
		t.Fatal(got)
	}
}

// The stable block's form depends on its own size only, never on how big the work block is.
func TestStablePartIgnoresTheOtherBlocks(t *testing.T) {
	big, _ := RenderHookContext(contextState(), 8000)
	small := Part{Full: "작은 작업"}
	for _, stable := range []Part{
		{Full: lines("[user] 규칙 ", 10, 50), Meta: "meta", Stable: true},  // fits its share
		{Full: lines("[user] 규칙 ", 40, 200), Meta: "meta", Stable: true}, // over its share
	} {
		a, _ := AssembleParts(stable, Part{Full: big}, Part{}, ContextMaxChars)
		b, _ := AssembleParts(stable, small, Part{}, ContextMaxChars)
		if strings.SplitN(a, "\n\n", 2)[0] != strings.SplitN(b, "\n\n", 2)[0] {
			t.Fatalf("stable prefix moved with the work block:\n%.60q\n%.60q", a, b)
		}
	}
}

func TestSessionStorePinsAndStableAreCreateOnly(t *testing.T) {
	s := SessionStore{Root: t.TempDir()}
	first, err := s.Pin("sid", SkillPin{SkillID: "agent/x", VersionID: "v1"})
	if err != nil || first.VersionID != "v1" {
		t.Fatal(first, err)
	}
	if kept, err := s.Pin("sid", SkillPin{SkillID: "agent/x", VersionID: "v2", Version: 2}); err != nil || kept.VersionID != "v1" {
		t.Fatal("a later pin replaced the first", kept, err)
	}
	if p, ok := s.SkillPin("other", "agent/x"); ok {
		t.Fatal("pin leaked to another session", p)
	}
	if _, err := s.Pin("sid", SkillPin{SkillID: "agent/y"}); err == nil {
		t.Fatal("empty pin accepted")
	}
	b, err := s.SaveStable("sid", StableBlock{Full: "one", IDs: []string{"a"}})
	if err != nil || b.Full != "one" {
		t.Fatal(b, err)
	}
	if b, _ = s.SaveStable("sid", StableBlock{Full: "two"}); b.Full != "one" {
		t.Fatal("stable block replaced", b)
	}
	// A day later the session rebuilds; pins go with it.
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(filepath.Join(s.dir("sid"), "stable.json"), old, old)
	s.Expire("sid", SessionStateTTL, time.Now())
	if _, ok := s.Stable("sid"); ok {
		t.Fatal("expired block kept")
	}
	if _, ok := s.SkillPin("sid", "agent/x"); ok {
		t.Fatal("expired pin kept")
	}
}

func TestAgentSessionFollowsTheHookLink(t *testing.T) {
	if _, _, ok := procParent(os.Getpid()); !ok {
		t.Skip("no /proc")
	}
	t.Setenv("ARKISM_SESSION_ID", "")
	s := SessionStore{Root: t.TempDir()}
	if s.AgentSession() != "" {
		t.Fatal("session without a link")
	}
	if err := s.LinkAgent("session-a"); err != nil {
		t.Fatal(err)
	}
	if got := s.AgentSession(); got != "session-a" {
		t.Fatal(got)
	}
	s.LinkAgent("session-b") // /clear: same agent process, new session
	if got := s.AgentSession(); got != "session-b" {
		t.Fatal(got)
	}
	t.Setenv("ARKISM_SESSION_ID", "explicit")
	if s.AgentSession() != "explicit" {
		t.Fatal("env override ignored")
	}
}
