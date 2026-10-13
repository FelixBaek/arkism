package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Old-session cold start: `arkism import-sessions` reads agent transcripts the host already keeps,
// asks a model for lesson/decision/skill CANDIDATES (each citing the message it came from) and
// sends them here. Every candidate goes through the ordinary capture path as an inferred claim, so
// secrets are blocked, duplicates merge and nothing becomes active before a person reviews it.
//
// One session is one capture event ("import:<session id>"), so a re-run of the same session is a
// duplicate event and stores nothing twice. dry_run reports what a run would store without writing.

const (
	importMaxSessions   = 16
	importMaxCandidates = 8
	importEventPrefix   = "import:"
)

var (
	importSessionID = regexp.MustCompile(`^is_[a-f0-9]{24}$`)
	importKinds     = map[string]string{"lesson": "lesson", "decision": "decision", "skill": "procedural"}
)

type SessionImportInput struct {
	Scope     Scope             `json:"scope"`
	DryRun    bool              `json:"dry_run"`
	Generator *Generator        `json:"generator"`
	Sessions  []ImportedSession `json:"sessions"`
}

// ImportedSession is one old conversation: a stable hash id (never the agent's own session id or
// path), the agent it came from, its original times, and how much of it the extractor read.
type ImportedSession struct {
	ID         string            `json:"id"`
	Origin     string            `json:"origin"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	EndedAt    *time.Time        `json:"ended_at"`
	Messages   int               `json:"messages"`
	Truncated  bool              `json:"truncated"`
	Candidates []ImportCandidate `json:"candidates"`
}

// ImportCandidate cites the message (its number in the session log) and an exact quote from it.
type ImportCandidate struct {
	Kind    string `json:"kind"` // lesson | decision | skill
	Subject string `json:"subject"`
	Text    string `json:"text"`
	Message int    `json:"message"`
	Quote   string `json:"quote"`
	Reason  string `json:"reason"`
}

type SessionImportOutcome struct {
	ID     string        `json:"id"`
	Status string        `json:"status"` // imported | would_import | already_imported | empty
	Items  []CaptureItem `json:"items"`
}

type SessionImportResult struct {
	DryRun    bool                   `json:"dry_run"`
	Sessions  []SessionImportOutcome `json:"sessions"`
	Counts    map[string]int         `json:"counts"`
	Authority string                 `json:"authority"`
}

func validImportInput(in SessionImportInput, now time.Time) bool {
	if !validScope(in.Scope) || !validGenerator(in.Generator) || len(in.Sessions) == 0 || len(in.Sessions) > importMaxSessions {
		return false
	}
	seen := map[string]bool{}
	for _, s := range in.Sessions {
		if !importSessionID.MatchString(s.ID) || seen[s.ID] || !originProductPattern.MatchString(s.Origin) || s.EndedAt == nil ||
			!storableTime(*s.EndedAt) || s.EndedAt.After(now) || s.StartedAt != nil && (!storableTime(*s.StartedAt) || s.StartedAt.After(*s.EndedAt)) ||
			s.Messages < 0 || len(s.Candidates) > importMaxCandidates {
			return false
		}
		seen[s.ID] = true
	}
	return true
}

// importClaim is the capture claim for one candidate; ok false means the candidate itself is unusable
// (unknown kind, no citation) and is reported as invalid without reaching storage.
func importClaim(c ImportCandidate) (CaptureClaim, bool) {
	kind, known := importKinds[c.Kind]
	quote := strings.Join(strings.Fields(c.Quote), " ")
	if !known || c.Message < 0 || quote == "" || strings.TrimSpace(c.Text) == "" {
		return CaptureClaim{}, false
	}
	subject := strings.TrimSpace(c.Subject)
	if subject == "" {
		subject = "general"
	}
	if c.Kind == "skill" {
		subject = "skill:" + subject
	}
	subject = cutUTF8(strings.Join(strings.Fields(subject), "-"), 128)
	reason := fmt.Sprintf("옛 세션 가져오기 후보(#%d \"%s\")", c.Message, cutUTF8(quote, 300))
	if r := strings.TrimSpace(c.Reason); r != "" {
		reason += ": " + r
	}
	return CaptureClaim{Subject: subject, Text: strings.TrimSpace(c.Text), Kind: kind, Basis: "inferred", Reason: cutUTF8(reason, 500)}, true
}

// cutUTF8 keeps at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func importReceipt(scope Scope, sessionID string) string {
	return "capture-event-" + ContentHash(structuredText(struct {
		Scope Scope  `json:"scope"`
		Event string `json:"event"`
	}{scope, importEventPrefix + sessionID}))
}

// ImportSessions stores (or, with dry_run, previews) the candidates of old sessions.
func (s *Service) ImportSessions(ctx context.Context, token string, in SessionImportInput) (SessionImportResult, error) {
	out := SessionImportResult{DryRun: in.DryRun, Sessions: []SessionImportOutcome{}, Counts: map[string]int{}, Authority: "data_only_untrusted"}
	if !validImportInput(in, time.Now().UTC()) {
		return out, ErrInvalid
	}
	if containsSecret(structuredText(in.Scope)) {
		return out, ErrSecretDetected
	}
	if _, err := s.authorize(ctx, token, "read:memory_candidates", in.Scope, "", ""); err != nil {
		return out, err
	}
	for _, sess := range in.Sessions {
		res := SessionImportOutcome{ID: sess.ID, Items: make([]CaptureItem, len(sess.Candidates))}
		var claims []CaptureClaim
		var slots []int
		for i, c := range sess.Candidates {
			claim, ok := importClaim(c)
			if !ok || !validCaptureClaim(claim) {
				res.Items[i] = CaptureItem{Status: "rejected", Reason: "invalid_claim"}
				continue
			}
			claims, slots = append(claims, claim), append(slots, i)
		}
		done, err := s.importDone(ctx, in.Scope, sess.ID)
		if err != nil {
			return out, err
		}
		switch {
		case done:
			res.Status = "already_imported"
			for _, i := range slots {
				res.Items[i] = CaptureItem{Status: "merged", Reason: "duplicate_event"}
			}
		case len(claims) == 0:
			res.Status = "empty"
		case in.DryRun:
			res.Status = "would_import"
			for k, claim := range claims {
				item, err := s.previewImportClaim(ctx, in.Scope, claim)
				if err != nil {
					return out, err
				}
				res.Items[slots[k]] = item
			}
		default:
			ended := sess.EndedAt.UTC().Truncate(time.Millisecond)
			captured, err := s.Capture(ctx, token, CaptureInput{Scope: in.Scope, Claims: claims, Generator: in.Generator,
				Event: CaptureEvent{ID: importEventPrefix + sess.ID, Type: "backfill", OriginProduct: sess.Origin, SessionID: sess.ID, OccurredAt: &ended}})
			if err != nil {
				return out, err
			}
			res.Status = "imported"
			for k, item := range captured.Items {
				res.Items[slots[k]] = item
			}
		}
		out.Counts["sessions_"+res.Status]++
		for _, item := range res.Items {
			out.Counts[item.Status]++
			if item.Reason == "secret_detected" {
				out.Counts["secret_detected"]++
			}
		}
		out.Sessions = append(out.Sessions, res)
	}
	return out, nil
}

// importDone reports whether a session's import event already left its capture receipt.
func (s *Service) importDone(ctx context.Context, scope Scope, sessionID string) (bool, error) {
	var seen Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		seen, e = tx.Get(ctx, "jobs", scope, importReceipt(scope, sessionID))
		return e
	})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil && seen.Kind == "capture_receipt", err
}

// previewImportClaim is what Capture would answer for one inferred claim, read-only.
func (s *Service) previewImportClaim(ctx context.Context, scope Scope, c CaptureClaim) (CaptureItem, error) {
	if containsSecret(structuredText(c)) {
		return CaptureItem{Status: "rejected", Reason: "secret_detected"}, nil
	}
	key := captureKey(scope, c.Kind, c.Text)
	item := CaptureItem{DedupeKey: key, Status: "candidate", Reason: "review_required"}
	var pointer Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		pointer, e = tx.Get(ctx, "jobs", scope, key)
		return e
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return item, err
	}
	if err == nil && pointer.Kind == "capture_pointer" && pointer.Target != nil {
		live, e := s.captureTargetLive(ctx, scope, pointer.Target.ID)
		if e != nil {
			return item, e
		}
		if live {
			item.Status, item.Reason, item.CandidateID = "merged", "duplicate_claim", pointer.Target.ID
			return item, nil
		}
	}
	if instructionLike.MatchString(structuredText(c)) {
		item.Reason = "review_required:instruction_like"
	}
	if !slices.Contains(captureKinds, c.Kind) {
		item.Status, item.Reason = "rejected", "invalid_claim"
	}
	return item, nil
}
