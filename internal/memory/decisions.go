package memory

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A decision is one choice a person or the AI made that later questions count: a skill sent to the
// trash, a merge proposal dismissed, a candidate approved after an edit. Each is one audit_events row
// with kind "decision:<kind>", a structured reason code and, when someone gave one, their own words.
// Migration 0005 keeps a per-day count of these rows in decision_daily_counts, so audit retention can
// still delete the rows (and the free-text reasons in them) without losing the counts.
const (
	decisionKindPrefix = "decision:"
	decisionReasonMax  = 500
	decisionSummaryMax = 366
)

// A kind and a reason code are short lowercase tokens; anything else in a code is counted as "other"
// so a stray value never blocks the person's action.
var decisionTokenRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// recordDecision writes one counted decision in the caller's transaction.
func recordDecision(ctx context.Context, tx Tx, scope Scope, actor, kind, id, code, reason string) error {
	if !decisionTokenRe.MatchString(kind) {
		return ErrInvalid
	}
	if code != "" && !decisionTokenRe.MatchString(code) {
		code = "other"
	}
	reason = strings.TrimSpace(reason)
	if r := []rune(reason); len(r) > decisionReasonMax {
		reason = string(r[:decisionReasonMax])
	}
	if containsSecret(reason) { // the decision still counts; words carrying a secret are not kept
		reason = ""
	}
	if containsSecret(structuredText(scope)) || containsSecret(actor) || containsSecret(id) || len(id) > 512 {
		return ErrSecretDetected
	}
	now := time.Now().UTC()
	return tx.Insert(ctx, "audit_events", Record{Audit: &AuditMetadata{RequestID: auditRequestID(ctx), Decision: "allow", ReasonCode: code, Reason: reason},
		ID: ID(), Scope: scope, Kind: decisionKindPrefix + kind, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: actor, Target: &Ref{ID: id}})
}

// decidedBy is the reason code for a decision a service made on its own rather than a person.
func decidedBy(d Decision) string {
	if d.Role != roleHuman {
		return "automatic"
	}
	return ""
}

// recordTransitionDecision counts a rejected skill version (an AI-drafted improvement apart from the
// rest) and a person's candidate review, so edit-then-approve can be told from a plain approve.
func recordTransitionDecision(ctx context.Context, tx Tx, col, status string, r Record, review *CandidateReviewInput, d Decision) error {
	switch {
	case review != nil:
		return recordDecision(ctx, tx, r.Scope, d.PrincipalID, "candidate_"+review.Action, r.ID, "", review.Reason)
	case col == colSkillVersions && status == "rejected":
		kind := "skill_version_rejected"
		if r.Kind == proposedSkillKind || r.Generator != nil {
			kind = "skill_improvement_rejected"
		}
		return recordDecision(ctx, tx, r.Scope, d.PrincipalID, kind, r.ID, decidedBy(d), "")
	}
	return nil
}

// DecisionCount is one day's count for one decision kind and reason code.
type DecisionCount struct {
	Day        time.Time
	Kind       string
	ReasonCode string
	Count      int64
}

type decisionCounter interface {
	DecisionDailyCounts(ctx context.Context, scope Scope, from time.Time) ([]DecisionCount, error)
}

type DecisionSummaryRow struct {
	Period     string `json:"period"` // first day of the period, YYYY-MM-DD (UTC)
	Kind       string `json:"kind"`
	ReasonCode string `json:"reason_code"`
	Count      int64  `json:"count"`
}

type DecisionSummary struct {
	Authority string               `json:"authority"`
	Period    string               `json:"period"`
	From      string               `json:"from"`
	Rows      []DecisionSummaryRow `json:"rows"`
}

// periodStart is the first day of the day, ISO week (Monday) or month that holds d.
func periodStart(d time.Time, period string) time.Time {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "week":
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	case "month":
		return d.AddDate(0, 0, 1-d.Day())
	}
	return d
}

// DecisionSummary counts the scope's decisions of the last days days by kind, reason code and period.
// Only a person reads it: the counts describe what people decided.
func (s *Service) DecisionSummary(ctx context.Context, token string, scope Scope, period string, days int) (DecisionSummary, error) {
	out := DecisionSummary{Authority: AuthorityDataOnly, Period: period, Rows: []DecisionSummaryRow{}}
	if out.Period == "" {
		out.Period = "week"
	}
	if days == 0 {
		days = 30
	}
	if !validScope(scope) || (out.Period != "day" && out.Period != "week" && out.Period != "month") || days < 1 || days > decisionSummaryMax {
		return out, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:routing", scope, "", "")
	if err != nil {
		return out, err
	}
	if d.Role != roleHuman {
		return out, ErrDenied
	}
	counter, ok := s.Repo.(decisionCounter)
	if !ok {
		return out, ErrUnavailable
	}
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1-days)
	out.From = from.Format(time.DateOnly)
	counts, err := counter.DecisionDailyCounts(ctx, Scope{WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID}, from)
	if err != nil {
		return out, err
	}
	at := map[[3]string]int{}
	for _, c := range counts {
		key := [3]string{periodStart(c.Day, out.Period).Format(time.DateOnly), strings.TrimPrefix(c.Kind, decisionKindPrefix), c.ReasonCode}
		if i, seen := at[key]; seen {
			out.Rows[i].Count += c.Count
			continue
		}
		at[key] = len(out.Rows)
		out.Rows = append(out.Rows, DecisionSummaryRow{Period: key[0], Kind: key[1], ReasonCode: key[2], Count: c.Count})
	}
	return out, nil
}

// Decisions only the desktop sees: whether the person followed the AI's advice.
var clientDecisionKinds = map[string]bool{"ai_help_accepted": true, "ai_help_discarded": true}

type DecisionReport struct {
	Scope    Scope  `json:"scope"`
	Kind     string `json:"kind"`
	TargetID string `json:"target_id"`
}

// ReportDecision records a decision the server cannot see, under the permission that asked for the AI help.
func (s *Service) ReportDecision(ctx context.Context, token string, in DecisionReport) error {
	if !validScope(in.Scope) || !clientDecisionKinds[in.Kind] || !validRunLabel(in.TargetID, 256) {
		return ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:crew", in.Scope, in.TargetID, "")
	if err != nil {
		return err
	}
	if d.Role != roleHuman {
		return ErrDenied
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return recordDecision(ctx, tx, in.Scope, d.PrincipalID, in.Kind, in.TargetID, "", "")
	})
}

func decisionRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/decisions/summary", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		days := 0
		if v := q.Get("days"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			days = n
		}
		v, e := s.DecisionSummary(r.Context(), bearer(r), scopeQuery(r), q.Get("period"), days)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /v1/decisions", func(w http.ResponseWriter, r *http.Request) {
		var in DecisionReport
		if !decode(w, r, &in) {
			return
		}
		respond(w, map[string]bool{"recorded": true}, s.ReportDecision(r.Context(), bearer(r), in))
	})
}

// DecisionDailyCounts reads the per-day counts under the caller's row-level security.
func (p *Postgres) DecisionDailyCounts(ctx context.Context, scope Scope, from time.Time) ([]DecisionCount, error) {
	if !validScope(scope) {
		return nil, ErrInvalid
	}
	rows, err := p.q().Query(ctx, `SELECT day,kind,reason_code,count FROM decision_daily_counts
WHERE workspace_id=$1 AND project_id=$2 AND day>=$3::date ORDER BY day,kind,reason_code`, scope.WorkspaceID, scope.ProjectID, from.Format(time.DateOnly))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	out := []DecisionCount{}
	for rows.Next() {
		var c DecisionCount
		if err := rows.Scan(&c.Day, &c.Kind, &c.ReasonCode, &c.Count); err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
