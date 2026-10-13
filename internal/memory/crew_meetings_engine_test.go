package memory

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

// The real meeting driver against this server: every progress write it makes
// must pass the server's rules (lease, turns, notes, statuses), and the
// conclusion must reach capture. run-node is a shell script.
func TestMeetingDriverAgainstTheServer(t *testing.T) {
	f := newMeetingFixture(t)
	f.auth.actors[tokEngine] = actor("crew-engine", "service", true, "read", "write")
	srv := httptest.NewServer(Handler(f.svc))
	defer srv.Close()
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	_ = os.WriteFile(tok, []byte(tokEngine), 0o600)
	runner := filepath.Join(dir, "run-node")
	script := `#!/bin/sh
in=$(cat)
case "$in" in
  *"JSON 하나로만"*) out='{\"summary\":\"머지한다\",\"decisions\":[\"PR 12를 머지한다\"],\"todos\":[],\"open_questions\":[]}' ;;
  *STANCE*) out='좋아요\nSTANCE: approve' ;;
  *) out='의견입니다' ;;
esac
printf '{"ok":true,"output":"%s","model":"opus 5.5","effort":"medium","cli":"fake","duration_ms":1,"usage":{"input_tokens":100,"output_tokens":20,"cost_usd":0.001,"estimated":true}}\n' "$out"
`
	if err := os.WriteFile(runner, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &crewrun.Client{BaseURL: srv.URL, TokenFile: tok, Scope: crewrun.Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID}}
	e := &crewrun.Engine{Store: client, Meetings: client, Runner: []string{runner}, DefaultModel: "opus 5.5"}
	m := f.meeting(f.human)
	if _, err := f.svc.NoteCrewMeeting(testCtx, f.human, m.ID, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: 1, Text: "보안도"}); err != nil {
		t.Fatal(err)
	}
	if err := e.PollMeetings(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	got, _ := f.svc.GetCrewMeeting(testCtx, f.human, scopeA, m.ID)
	cm := got.CrewMeeting
	if cm.Status != "concluded" || cm.Conclusion == nil || cm.Saved == nil || len(cm.Saved.CandidateIDs) != 1 || cm.TeamSnapshot == nil {
		t.Fatalf("%+v", cm)
	}
	// brief, the note, 2 rounds of 2 (the team ends on approval only when all reviewers approve: round 1)
	if cm.Turns[1].Kind != "human" || cm.Turns[len(cm.Turns)-1].Kind != "conclusion" || cm.EndedReason == "" {
		t.Fatal(cm.Turns, cm.EndedReason)
	}
	cand := f.repo.rows("memory_candidates")
	if len(cand) != 1 {
		t.Fatal("candidates", len(cand))
	}
}
