package memory

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Work-state candidates (space setting work_state_candidates, off by default): a saved work
// checkpoint or handoff is where an agent says what it decided and where the work stands. With
// the setting on, that save also proposes those lines as decision candidates through the normal
// capture gate, so the secret check, dedupe and merge gate apply and only a person's review
// makes any of them a memory.

const workStateClaimsMax = 8

// workStateCandidates captures one fresh checkpoint save as decision candidates. Best effort:
// the checkpoint is already saved, so a failure here only logs.
func (s *Service) workStateCandidates(ctx context.Context, token string, in WorkCheckpointSaveInput) {
	cfg, err := s.spaceSettings(ctx, in.Scope)
	if err != nil || cfg.WorkStateCandidates == nil || !*cfg.WorkStateCandidates {
		return
	}
	claims := workStateClaims(in.Package)
	if len(claims) == 0 {
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	ev := CaptureEvent{ID: "work-" + in.ID + "-" + in.EventID, Type: workStateEventType(in.Package), OriginProduct: "work_checkpoint",
		SessionID: in.Package.SessionID, OccurredAt: &now}
	if !validRunLabel(ev.SessionID, 120) {
		ev.SessionID = ""
	}
	gen := &Generator{Model: "work_checkpoint", PromptVersion: "work-state-v1", PipelineVersion: "work-state-v1"}
	if _, err := s.Capture(ctx, token, CaptureInput{Scope: in.Scope, Event: ev, Claims: claims, Generator: gen}); err != nil {
		slog.Warn("work state candidates not captured", "checkpoint", in.ID, "error", err)
	}
}

// workStateEventType names the moment the checkpoint stands for.
func workStateEventType(p WorkCheckpointPackage) string {
	switch {
	case strings.Contains(p.Origin, "handoff"):
		return "handoff_completed"
	case p.Status == workCheckpointStatusCompleted:
		return "run_completed"
	}
	return "turn_completed"
}

// workStateClaims are the package's decisions (proposed: the agent made them, no person has
// confirmed them) and one line on where the work stands, at most workStateClaimsMax.
func workStateClaims(p WorkCheckpointPackage) []CaptureClaim {
	repo := strings.TrimSpace(p.Repository)
	if !validRunLabel(repo, 128) {
		repo = ""
	}
	claim := func(text, status, reason string) CaptureClaim {
		return CaptureClaim{Subject: "work_state", Repo: repo, Text: text, Kind: "decision", Basis: "assistant_result", DecisionStatus: status, Reason: reason}
	}
	var out []CaptureClaim
	for _, d := range p.Decisions {
		if d = strings.TrimSpace(d); d != "" && len(out) < workStateClaimsMax-1 {
			out = append(out, claim(d, "proposed", "작업 저장·인계에서 에이전트가 기록한 결정. 사람이 확인해야 기억이 됩니다."))
		}
	}
	if state := workStateLine(p); state != "" {
		out = append(out, claim(state, "", "작업 저장·인계에서 기록한 현재 상태. 사람이 확인해야 기억이 됩니다."))
	}
	return out
}

// workStateLine is "<title>: <status>" with the last completed step and the next step.
func workStateLine(p WorkCheckpointPackage) string {
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return ""
	}
	parts := []string{title + ": " + p.Status}
	if n := len(p.Completed); n > 0 {
		parts = append(parts, "done "+strings.TrimSpace(p.Completed[n-1]))
	}
	if len(p.NextSteps) > 0 {
		parts = append(parts, "next "+strings.TrimSpace(p.NextSteps[0]))
	}
	return runeCutString(strings.Join(parts, "; "), 1000)
}
