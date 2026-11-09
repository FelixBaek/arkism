//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMentalModelDeltaRefresh(t *testing.T) {
	f, llm, ms := obsFixture(t)
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshManual, Strategy: mmStrategyDelta}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "x", Query: "y", Refresh: MentalModelRefresh{Mode: mmRefreshManual, Strategy: "partial"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown strategy: %v", err)
	}
	answer := func(text string, ids ...string) string {
		raw, _ := json.Marshal(mentalModelAnswer{Summary: text, Citations: ids})
		return string(raw)
	}
	llm.answers = []string{answer("Staging needs the flag.", ms[0].ID)}
	// No approved text yet: a delta model rewrites in full.
	v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil || v.Draft == nil || v.Draft.Action != "refresh" {
		t.Fatalf("%+v %v", v, err)
	}
	v, err = f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove})
	if err != nil || v.Active == nil {
		t.Fatalf("%+v %v", v, err)
	}
	// Nothing changed after the approval: nothing to send.
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no new memories: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	fresh := f.activeMemory("deploy to staging requires the staging flag and a second reviewer")
	llm.answers = append(llm.answers, answer("Staging needs the flag and a second reviewer.", ms[0].ID, fresh.ID))
	v, err = f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil || v.Draft == nil || v.Draft.Action != "delta_refresh" || v.Active == nil {
		t.Fatalf("%+v %v", v, err)
	}
	prompt := llm.prompts[len(llm.prompts)-1]
	newData := prompt[strings.Index(prompt, "NEW_DATA"):]
	if !strings.Contains(prompt, "Staging needs the flag.") || !strings.Contains(newData, fresh.ID) || strings.Contains(newData, ms[1].ID) || strings.Contains(prompt, ms[3].ID) {
		t.Fatalf("delta prompt must carry the approved text, its citations and only new memories:\n%s", prompt)
	}
	if len(v.DraftDiff) == 0 {
		t.Fatal("a delta draft is shown as a diff of the approved text")
	}
	// A cited memory that may no longer leave the host: the approved text is not sent; full rewrite.
	if _, err := f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewReject}); err != nil {
		t.Fatal(err)
	}
	f.editRecord(colMemories, ms[0].ID, func(r *Record) { r.Sensitivity = "restricted" })
	llm.answers = append(llm.answers, answer("Staging needs a second reviewer.", fresh.ID))
	v, err = f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil || v.Draft == nil || v.Draft.Action != "refresh" {
		t.Fatalf("%+v %v", v, err)
	}
	if prompt := llm.prompts[len(llm.prompts)-1]; strings.Contains(prompt, "APPROVED") || strings.Contains(prompt, ms[0].ID) {
		t.Fatalf("a non-transferable citation must keep the approved text and the record from the model:\n%s", prompt)
	}
}
