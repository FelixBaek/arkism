package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
)

// Memory maintenance proposals: a nightly job points at two active
// memories that say the same thing, or where a newer one contradicts an older
// one, and proposes keeping one. Nothing changes until a person decides; the
// decision runs the ordinary conflict path (mark, then supersede), so the
// retired memory stays as history. A proposal holds references and reason codes
// only, never memory text, so erasing a memory leaves no copy of it here.
//
// Each pair of memories has one proposal record, whose id comes from the pair,
// so "asked before?" is a single read in the same transaction as the write.
//
// ST-814: a signal proposal names one memory whose stored trust signal
// (ST-813) shows it often fails: a rule often broken or a lesson that often
// fails. It carries the signal's counts as the AI's evidence. Deciding it never
// changes the memory; a person who agrees fixes or retires it the usual way.
const colMaintenance = "maintenance_proposals"

const (
	maintenanceOpen      = "open"
	maintenanceStale     = "stale"
	maintenanceListLimit = 500
)

var (
	maintenanceTypes   = []string{"duplicate", "contradiction"}
	maintenanceReasons = []string{"same_meaning", "newer_replaces_older", "more_specific"}
	// signal proposal type -> the memory kinds it may name
	signalTypes          = map[string][]string{"rule_often_violated": {"user", "procedural", "decision"}, "lesson_often_failing": {"lesson"}}
	maintenanceDecisions = map[string]string{"accept": "accepted", "swap": "swapped", "dismiss": "dismissed", "forget_both": "forgotten", "forget_keep": "forgotten", "forget_retire": "forgotten", "forget": "forgotten"}
)

type MaintenanceProposal struct {
	Type   string `json:"type"`
	Keep   Ref    `json:"keep"`
	Retire Ref    `json:"retire"`
	// Memory and Evidence are set only on a signal proposal (ST-814).
	Memory    *Ref                 `json:"memory,omitempty"`
	Evidence  *MaintenanceEvidence `json:"evidence,omitempty"`
	Reason    string               `json:"reason"`
	Decision  string               `json:"decision,omitempty"`
	DecidedBy string               `json:"decided_by,omitempty"`
	DecidedAt *time.Time           `json:"decided_at,omitempty"`
}

// MaintenanceEvidence is the stored signal's counts when the proposal was made.
type MaintenanceEvidence struct {
	Samples  int     `json:"samples"`
	Passed   int     `json:"passed"`
	PassRate float64 `json:"pass_rate"`
}

type MaintenanceInput struct {
	Scope    Scope               `json:"scope"`
	Proposal MaintenanceProposal `json:"proposal"`
}

type MaintenanceDecision struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Decision         string `json:"decision"`
	// ReasonCode (required for a forget decision) and Reason are stored on each removed memory.
	ReasonCode string `json:"reason_code,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type MaintenancePage struct {
	Items []Record `json:"items"`
}

func validMaintenance(p MaintenanceProposal) bool {
	ref := func(r Ref) bool { return r.Collection == colMemories && validRunLabel(r.ID, 256) && r.Revision > 0 }
	if p.Decision != "" || p.DecidedBy != "" || p.DecidedAt != nil {
		return false
	}
	if _, ok := signalTypes[p.Type]; ok {
		e := p.Evidence
		return p.Reason == "low_pass_rate" && p.Memory != nil && ref(*p.Memory) && p.Keep == (Ref{}) && p.Retire == (Ref{}) &&
			e != nil && e.Samples > 0 && e.Passed >= 0 && e.Passed <= e.Samples
	}
	return p.Memory == nil && p.Evidence == nil && slices.Contains(maintenanceTypes, p.Type) && slices.Contains(maintenanceReasons, p.Reason) && ref(p.Keep) && ref(p.Retire) &&
		p.Keep.ID != p.Retire.ID
}

func isSignalProposal(p MaintenanceProposal) bool { return p.Memory != nil }

// proposalRefs is the memories a proposal names: one for a signal proposal, else the pair.
func proposalRefs(p MaintenanceProposal) []Ref {
	if isSignalProposal(p) {
		return []Ref{*p.Memory}
	}
	return []Ref{p.Keep, p.Retire}
}

// proposalID is one record per pair, or per memory for a signal proposal (NUL is no valid id).
func proposalID(scope Scope, p MaintenanceProposal) string {
	if isSignalProposal(p) {
		return pairID(scope, "\x00signal", p.Memory.ID)
	}
	return pairID(scope, p.Keep.ID, p.Retire.ID)
}

// errMemoryChanged: a memory in the proposal changed after it was made.
var errMemoryChanged = errors.New("maintenance: memory changed")

// pairID is the one proposal id for two memories in a scope, in either order.
func pairID(scope Scope, a, b string) string {
	if b < a {
		a, b = b, a
	}
	h := sha256.Sum256([]byte(scope.WorkspaceID + "\x00" + scope.ProjectID + "\x00" + scope.UserID + "\x00" + scope.AgentID + "\x00" + a + "\x00" + b))
	return "mp_" + hex.EncodeToString(h[:16])
}

func sameRevisions(a, b MaintenanceProposal) bool {
	if isSignalProposal(a) || isSignalProposal(b) {
		return isSignalProposal(a) && isSignalProposal(b) && a.Memory.Revision == b.Memory.Revision
	}
	rev := map[string]int64{a.Keep.ID: a.Keep.Revision, a.Retire.ID: a.Retire.Revision}
	return rev[b.Keep.ID] == b.Keep.Revision && rev[b.Retire.ID] == b.Retire.Revision
}

// ListMaintenance returns proposals in the given status (default open), newest first.
func (s *Service) ListMaintenance(ctx context.Context, token string, scope Scope, status string) (MaintenancePage, error) {
	if status == "" {
		status = maintenanceOpen
	}
	if !validScope(scope) || !validRunLabel(status, 32) {
		return MaintenancePage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colMaintenance, scope, "", ""); err != nil {
		return MaintenancePage{}, err
	}
	rows, err := s.Repo.ListRecordsByStatus(ctx, scope, colMaintenance, status, maintenanceListLimit)
	if err != nil {
		return MaintenancePage{}, err
	}
	out := MaintenancePage{Items: []Record{}}
	for _, r := range rows {
		if r.Maintenance != nil {
			out.Items = append(out.Items, r)
		}
	}
	return out, nil
}

// ProposeMaintenance stores one proposal. Both memories must be active and
// approved at the given revisions. The same pair is never proposed twice: an
// earlier proposal for it (open or decided, in either order) comes back with
// Duplicate set, so a pair a person kept as two is not asked about again. An
// open proposal for older revisions, or a stale one, is replaced in place.
func (s *Service) ProposeMaintenance(ctx context.Context, token string, in MaintenanceInput) (Record, error) {
	if !validScope(in.Scope) || !validMaintenance(in.Proposal) {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colMaintenance, in.Scope, "", "")
	if err != nil {
		return Record{}, err
	}
	// The evidence is checked against stored signals, so the proposer must be able to read them.
	if isSignalProposal(in.Proposal) {
		if _, err := s.authorize(ctx, token, "read:usage", in.Scope, "", ""); err != nil {
			return Record{}, err
		}
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		id := proposalID(in.Scope, in.Proposal)
		prior, err := tx.Get(ctx, colMaintenance, in.Scope, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		found := err == nil
		if found && prior.Maintenance != nil && (prior.Status != maintenanceOpen && prior.Status != maintenanceStale || prior.Status == maintenanceOpen && sameRevisions(*prior.Maintenance, in.Proposal)) {
			out = prior
			out.Duplicate = true
			return nil
		}
		if err := checkProposalMemories(ctx, tx, in); err != nil {
			return err
		}
		p := in.Proposal
		if isSignalProposal(p) {
			if err := checkSignalEvidence(ctx, tx, in.Scope, &p); err != nil {
				return err
			}
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		out = Record{ID: id, Scope: in.Scope, Kind: p.Type, Status: maintenanceOpen, Revision: 1, Text: p.Reason, Maintenance: &p,
			CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role}
		if found {
			out.Revision, out.CreatedAt = prior.Revision+1, prior.CreatedAt
			err = tx.Replace(ctx, colMaintenance, out, prior.Revision)
		} else {
			err = tx.Insert(ctx, colMaintenance, out)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colMaintenance, out.ID)
	})
	return out, err
}

// checkProposalMemories: both memories are live, approved, outside any
// conflict and at the revisions the proposal names.
func checkProposalMemories(ctx context.Context, tx Tx, in MaintenanceInput) error {
	for _, ref := range proposalRefs(in.Proposal) {
		m, err := tx.Get(ctx, colMemories, in.Scope, ref.ID)
		if err != nil {
			return err
		}
		if m.Revision != ref.Revision || !live(m) || !verifiedMemory(m) || m.ConflictID != "" {
			return ErrConflict
		}
	}
	return nil
}

// checkSignalEvidence: the memory's kind fits the proposal type and the
// evidence is the stored signal's counts, so the AI cannot invent its evidence.
// The pass rate is recomputed here from those counts.
func checkSignalEvidence(ctx context.Context, tx Tx, scope Scope, p *MaintenanceProposal) error {
	m, err := tx.Get(ctx, colMemories, scope, p.Memory.ID)
	if err != nil {
		return err
	}
	if !slices.Contains(signalTypes[p.Type], m.Kind) {
		return ErrInvalid
	}
	r, err := tx.Get(ctx, colJobs, scope, memorySignalID(scope, p.Memory.ID))
	if errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	sig := r.MemorySignal
	if r.Kind != memorySignalKind || sig == nil || sig.Memory.ID != p.Memory.ID || sig.Samples != p.Evidence.Samples || sig.Passed != p.Evidence.Passed {
		return ErrConflict
	}
	e := *p.Evidence
	e.PassRate = float64(e.Passed) / float64(e.Samples)
	p.Evidence = &e
	return nil
}

// DecideMaintenance is a person's answer: accept (keep stays, the other becomes
// history), swap (the other way round), dismiss (both stay) or a forget decision:
// forget_keep, forget_retire or forget_both on a pair, forget on a signal proposal.
// A forgotten memory becomes history (rejected) with the person's reason, never
// deleted. The memory change and the decision commit together. A memory that changed since the
// proposal makes it stale instead.
func (s *Service) DecideMaintenance(ctx context.Context, token, id string, in MaintenanceDecision) (Record, error) {
	status, ok := maintenanceDecisions[in.Decision]
	forgets := strings.HasPrefix(in.Decision, "forget")
	if !ok || !validScope(in.Scope) || !validRunLabel(id, 256) || !validDecisionInput(in, forgets) {
		return Record{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "write:"+colMaintenance, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if d.Role != roleHuman {
		return Record{}, ErrDenied
	}
	seen, err := s.Repo.Get(ctx, colMaintenance, in.Scope, id)
	if err != nil {
		return Record{}, err
	}
	if seen.Revision != in.ExpectedRevision || seen.Status != maintenanceOpen || seen.Maintenance == nil {
		return Record{}, ErrConflict
	}
	signal := isSignalProposal(*seen.Maintenance)
	if !decisionFits(*seen.Maintenance, in.Decision, signal) {
		return Record{}, ErrInvalid
	}
	var mark *ConflictInput
	var forget []Ref
	var removal *Removal
	if forgets {
		if forget, err = s.forgettable(ctx, token, in.Scope, *seen.Maintenance, in.Decision, d.PrincipalID); err != nil {
			return Record{}, err
		}
		removal = &Removal{Code: in.ReasonCode, Note: in.Reason, By: d.PrincipalID}
	} else if in.Decision != "dismiss" && !signal {
		// A signal proposal changes no memory either way: accept records that a person agrees.
		m := supersedeInput(in.Scope, seen, in.Decision)
		// The same permission the conflict path asks for: a human who may change both memories.
		if _, err := s.conflictPrincipal(ctx, token, m); err != nil {
			return Record{}, err
		}
		mark = &m
	}
	out, err := s.decideTx(ctx, in.Scope, seen, mark, forget, removal, status, in.Decision, d.PrincipalID)
	if errors.Is(err, errMemoryChanged) {
		if _, e := s.closeMaintenance(ctx, in.Scope, seen, maintenanceStale, "", d.PrincipalID); e != nil && !errors.Is(e, ErrConflict) {
			return Record{}, e
		}
		return Record{}, ErrConflict
	}
	return out, err
}

// validDecisionInput: a forget decision carries a known reason code, every other decision none.
func validDecisionInput(in MaintenanceDecision, forgets bool) bool {
	return in.ExpectedRevision >= 1 && validRemoval(in.ReasonCode, in.Reason) && forgets == (in.ReasonCode != "")
}

// forgettable is the memories a forget decision removes, once the same person may reject each of them.
func (s *Service) forgettable(ctx context.Context, token string, scope Scope, p MaintenanceProposal, decision, principal string) ([]Ref, error) {
	refs := forgetRefs(p, decision)
	for _, ref := range refs {
		who, err := s.authorize(ctx, token, "transition:"+colMemories, scope, ref.ID, "")
		if err != nil {
			return nil, err
		}
		if who.Role != roleHuman || who.PrincipalID != principal {
			return nil, ErrDenied
		}
	}
	return refs, nil
}

// decisionFits says whether a decision applies to the proposal: a signal has one memory to
// forget and nothing to swap; a pair can forget either side or both.
func decisionFits(p MaintenanceProposal, decision string, signal bool) bool {
	switch decision {
	case "swap", "forget_keep", "forget_retire", "forget_both":
		return !signal
	case "forget":
		return signal
	}
	return true
}

// forgetRefs is the memories a forget decision removes.
func forgetRefs(p MaintenanceProposal, decision string) []Ref {
	switch decision {
	case "forget_keep":
		return []Ref{p.Keep}
	case "forget_retire":
		return []Ref{p.Retire}
	case "forget_both":
		return []Ref{p.Keep, p.Retire}
	case "forget":
		return []Ref{*p.Memory}
	}
	return nil
}

// decideTx changes the memories (when mark is set) and closes the proposal in one transaction.
func (s *Service) decideTx(ctx context.Context, scope Scope, seen Record, mark *ConflictInput, forget []Ref, removal *Removal, status, decision, principal string) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if forget != nil {
			for _, ref := range forget {
				if err := forgetMemoryTx(ctx, tx, scope, ref, *removal, principal); err != nil {
					return err
				}
			}
		} else if err := changeMemories(ctx, tx, scope, seen, mark, principal); err != nil {
			return err
		}
		var err error
		out, err = closeMaintenanceTx(ctx, tx, scope, seen, status, decision, principal)
		return err
	})
	return out, err
}

// changeMemories supersedes one memory (mark set) or, for dismiss, only checks
// that both are still as proposed: keeping two changed memories as two would
// silence a pair nobody has looked at.
func changeMemories(ctx context.Context, tx Tx, scope Scope, seen Record, mark *ConflictInput, principal string) error {
	if mark != nil {
		return supersedeInTx(ctx, tx, *mark, principal)
	}
	err := checkProposalMemories(ctx, tx, MaintenanceInput{Scope: scope, Proposal: *seen.Maintenance})
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return errors.Join(errMemoryChanged, err)
	}
	return err
}

// forgetMemoryTx moves one proposed memory, still as proposed, to rejected with the person's reason:
// the same status change as POST /v1/memories/{id}/transition, so it leaves recall and stays in history.
func forgetMemoryTx(ctx context.Context, tx Tx, scope Scope, ref Ref, removal Removal, principal string) error {
	r, err := tx.Get(ctx, colMemories, scope, ref.ID)
	if errors.Is(err, ErrNotFound) {
		return errors.Join(errMemoryChanged, err)
	}
	if err != nil {
		return err
	}
	if r.Revision != ref.Revision || r.Status != "active" || r.ConflictID != "" || r.PrivacyQuarantined || r.ContentErased {
		return errors.Join(errMemoryChanged, ErrConflict)
	}
	now := time.Now().UTC()
	removal.At = now
	r.Status, r.Revision, r.UpdatedAt, r.Removal = "rejected", ref.Revision+1, now, &removal
	if err := tx.Replace(ctx, colMemories, r, ref.Revision); err != nil {
		return err
	}
	return audit(ctx, tx, scope, principal, "transition:memories:rejected", r.ID)
}

// supersedeInput is the conflict mark for a decision: the memory to retire
// first, the one to keep second.
func supersedeInput(scope Scope, r Record, decision string) ConflictInput {
	keep, retire := r.Maintenance.Keep, r.Maintenance.Retire
	if decision == "swap" {
		keep, retire = retire, keep
	}
	return ConflictInput{Scope: scope, Memories: []Ref{retire, keep}, Action: "mark", Hold: "both", Reason: "기억 정리 제안 " + r.ID}
}

// supersedeInTx marks the pair as a conflict and resolves it with mark.Resolution
// (the second superseding the first, or both rejected), in the caller's transaction: the conflict path's own
// history and audit, with no moment where both memories sit conflicted.
func supersedeInTx(ctx context.Context, tx Tx, mark ConflictInput, principal string) error {
	resolution := mark.Resolution
	if resolution == "" {
		resolution = "second_supersedes_first"
	}
	mark.Resolution = ""
	marked, err := applyConflict(ctx, tx, mark, principal, -1)
	if err == nil {
		resolve := ConflictInput{Scope: mark.Scope, Memories: marked.Relation.Relations, Action: "resolve", Reason: mark.Reason,
			ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: resolution}
		_, err = applyConflict(ctx, tx, resolve, principal, resolutionWinner[resolution])
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return errors.Join(errMemoryChanged, err)
	}
	return err
}

func (s *Service) closeMaintenance(ctx context.Context, scope Scope, seen Record, status, decision, principal string) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = closeMaintenanceTx(ctx, tx, scope, seen, status, decision, principal)
		return err
	})
	return out, err
}

func closeMaintenanceTx(ctx context.Context, tx Tx, scope Scope, seen Record, status, decision, principal string) (Record, error) {
	r, err := tx.Get(ctx, colMaintenance, scope, seen.ID)
	if err != nil {
		return Record{}, err
	}
	if r.Revision != seen.Revision || r.Status != maintenanceOpen || r.Maintenance == nil {
		return Record{}, ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := *r.Maintenance
	p.Decision, p.DecidedBy, p.DecidedAt = decision, principal, &now
	prev := r.Revision
	r.Maintenance, r.Status, r.Revision, r.UpdatedAt = &p, status, r.Revision+1, now
	if err := tx.Replace(ctx, colMaintenance, r, prev); err != nil {
		return Record{}, err
	}
	// The code is the proposal's own reason (same_meaning, ...), so a dismiss can be counted by what was proposed.
	if decision != "" {
		if err := recordDecision(ctx, tx, scope, principal, "dreaming_"+decision, r.ID, p.Reason, ""); err != nil {
			return Record{}, err
		}
	}
	return r, audit(ctx, tx, scope, principal, status+":"+colMaintenance, r.ID)
}
