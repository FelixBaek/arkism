package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
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
const colMaintenance = "maintenance_proposals"

const (
	maintenanceOpen      = "open"
	maintenanceStale     = "stale"
	maintenanceListLimit = 500
)

var (
	maintenanceTypes     = []string{"duplicate", "contradiction"}
	maintenanceReasons   = []string{"same_meaning", "newer_replaces_older", "more_specific"}
	maintenanceDecisions = map[string]string{"accept": "accepted", "swap": "swapped", "dismiss": "dismissed"}
)

type MaintenanceProposal struct {
	Type      string     `json:"type"`
	Keep      Ref        `json:"keep"`
	Retire    Ref        `json:"retire"`
	Reason    string     `json:"reason"`
	Decision  string     `json:"decision,omitempty"`
	DecidedBy string     `json:"decided_by,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

type MaintenanceInput struct {
	Scope    Scope               `json:"scope"`
	Proposal MaintenanceProposal `json:"proposal"`
}

type MaintenanceDecision struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Decision         string `json:"decision"`
}

type MaintenancePage struct {
	Items []Record `json:"items"`
}

func validMaintenance(p MaintenanceProposal) bool {
	ref := func(r Ref) bool { return r.Collection == colMemories && validRunLabel(r.ID, 256) && r.Revision > 0 }
	return slices.Contains(maintenanceTypes, p.Type) && slices.Contains(maintenanceReasons, p.Reason) && ref(p.Keep) && ref(p.Retire) &&
		p.Keep.ID != p.Retire.ID && p.Decision == "" && p.DecidedBy == "" && p.DecidedAt == nil
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
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		id := pairID(in.Scope, in.Proposal.Keep.ID, in.Proposal.Retire.ID)
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
		now := time.Now().UTC().Truncate(time.Millisecond)
		p := in.Proposal
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
	for _, ref := range []Ref{in.Proposal.Keep, in.Proposal.Retire} {
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

// DecideMaintenance is a person's answer: accept (keep stays, the other becomes
// history), swap (the other way round) or dismiss (both stay). The memory
// change and the decision commit together. A memory that changed since the
// proposal makes it stale instead.
func (s *Service) DecideMaintenance(ctx context.Context, token, id string, in MaintenanceDecision) (Record, error) {
	status, ok := maintenanceDecisions[in.Decision]
	if !ok || !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 {
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
	var mark *ConflictInput
	if in.Decision != "dismiss" {
		m := supersedeInput(in.Scope, seen, in.Decision)
		// The same permission the conflict path asks for: a human who may change both memories.
		if _, err := s.conflictPrincipal(ctx, token, m); err != nil {
			return Record{}, err
		}
		mark = &m
	}
	out, err := s.decideTx(ctx, in.Scope, seen, mark, status, in.Decision, d.PrincipalID)
	if errors.Is(err, errMemoryChanged) {
		if _, e := s.closeMaintenance(ctx, in.Scope, seen, maintenanceStale, "", d.PrincipalID); e != nil && !errors.Is(e, ErrConflict) {
			return Record{}, e
		}
		return Record{}, ErrConflict
	}
	return out, err
}

// decideTx changes the memories (when mark is set) and closes the proposal in one transaction.
func (s *Service) decideTx(ctx context.Context, scope Scope, seen Record, mark *ConflictInput, status, decision, principal string) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := changeMemories(ctx, tx, scope, seen, mark, principal); err != nil {
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

// supersedeInput is the conflict mark for a decision: the memory to retire
// first, the one to keep second.
func supersedeInput(scope Scope, r Record, decision string) ConflictInput {
	keep, retire := r.Maintenance.Keep, r.Maintenance.Retire
	if decision == "swap" {
		keep, retire = retire, keep
	}
	return ConflictInput{Scope: scope, Memories: []Ref{retire, keep}, Action: "mark", Hold: "both", Reason: "기억 정리 제안 " + r.ID}
}

// supersedeInTx marks the pair as a conflict and resolves it with the second
// superseding the first, in the caller's transaction: the conflict path's own
// history and audit, with no moment where both memories sit conflicted.
func supersedeInTx(ctx context.Context, tx Tx, mark ConflictInput, principal string) error {
	marked, err := applyConflict(ctx, tx, mark, principal, -1)
	if err == nil {
		resolve := ConflictInput{Scope: mark.Scope, Memories: marked.Relation.Relations, Action: "resolve", Reason: mark.Reason,
			ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: "second_supersedes_first"}
		_, err = applyConflict(ctx, tx, resolve, principal, 1)
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
	return r, audit(ctx, tx, scope, principal, status+":"+colMaintenance, r.ID)
}
