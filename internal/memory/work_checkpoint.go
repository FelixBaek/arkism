package memory

// 프로그램 공통 작업 상태.
//
// 같은 사용자 scope에서 에이전트 프로그램이 바뀌어도 작업 ID로 이어받는다.
// 기존 synthetic consultation/handoff는 수정·재사용하지 않고, 전용 private
// collection인 work_checkpoints와 work_checkpoint_receipts만 사용한다. 두
// 컬렉션은 일반 CRUD의 knownCollection에서 제외되어 전용 엔드포인트로만
// 접근한다. 이 기능은 장기 기억 승인·개인정보 삭제·외부 실행의 권한을
// 부여하지 않으며, 저장된 package는 언제나 data_only_untrusted 자료다.
//
// 저장 규칙:
//   - save는 scope/id/event_id로 영수증을 먼저 본다. 같은 요청(payload 포함)
//     재전송은 당시 revision을 답한다. 같은 event_id에 다른 요청은 409다.
//   - CAS(expected_revision)와 receipt 저장은 한 트랜잭션이다. 충돌·감사
//     실패는 모두 rollback된다.
//   - receipt에는 본문이 없다(request hash·대상 id·revision만). save 응답은
//     {id,revision,authority} 메타데이터뿐이다.
//   - source_refs는 실제 scope·revision·읽기 권한과 live/content_erased를
//     매번 다시 확인한다.
//
// 작업 단위 삭제도 개인정보 삭제 API의 work_checkpoint 대상으로 처리한다.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	colWorkCheckpoints        = "work_checkpoints"
	colWorkCheckpointReceipts = "work_checkpoint_receipts"

	kindWorkCheckpoint        = "work_checkpoint"
	kindWorkCheckpointReceipt = "work_checkpoint_receipt"

	// workCheckpointDefaultLimit / workCheckpointMaxLimit: list page bounds.
	workCheckpointDefaultLimit = 20
	workCheckpointMaxLimit     = 100
	// workCheckpointMaxArray bounds every package array.
	workCheckpointMaxArray = 100
	// workCheckpointMaxString bounds one package string value in bytes.
	workCheckpointMaxString = 8192
	// workCheckpointMaxJSON bounds the marshaled package (총 JSON 64KiB).
	workCheckpointMaxJSON = 64 << 10
	// workCheckpointBodyLimit is the save HTTP decode cap: the 64 KiB package
	// plus room for the scope/id/event_id envelope.
	workCheckpointBodyLimit = workCheckpointMaxJSON + 8<<10

	workCheckpointStatusInProgress = "in_progress"
	workCheckpointStatusBlocked    = "blocked"
	workCheckpointStatusCompleted  = "completed"
)

// workCheckpointIDPattern: 클라이언트가 만든 영숫자/._- 1~128자 (id와 event_id 공통).
var workCheckpointIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// WorkCheckpoint is the Record payload for work checkpoints and their
// idempotency receipts. Every field is JSON-silent: packages reach API
// responses only through the dedicated /v1/work-checkpoints endpoints, never
// through generic Record serialization, logs or audit rows. Checkpoint rows
// carry Package; receipt rows carry only RequestHash plus the target id and
// the revision recorded at save time (원본 본문 보관 금지).
type WorkCheckpoint struct {
	ArchiveMessages []ArchiveMessageRef    `json:"-" bson:"archive_messages,omitempty"`
	Package         *WorkCheckpointPackage `json:"-" bson:"package,omitempty"`
	// Receipt-only fields (request hash/id/revision, 본문 없음).
	RequestHash     string `json:"-" bson:"request_hash,omitempty"`
	ReceiptTargetID string `json:"-" bson:"receipt_target_id,omitempty"`
	ReceiptRevision int64  `json:"-" bson:"receipt_revision,omitempty"`
}

// WorkCheckpointPackage is caller-reported work state. repository는 식별
// 정보일 뿐 서버 파일 접근은 없다. status가 completed여도 그것은 호출자
// 보고이며 검증 완료 인증이 아니다.
type WorkCheckpointPackage struct {
	Title       string                `json:"title" bson:"title"`
	Goal        string                `json:"goal" bson:"goal"`
	Repository  string                `json:"repository" bson:"repository"`
	Branch      string                `json:"branch,omitempty" bson:"branch,omitempty"`
	Head        string                `json:"head,omitempty" bson:"head,omitempty"`
	Status      string                `json:"status" bson:"status"`
	Origin      string                `json:"origin" bson:"origin"`
	SessionID   string                `json:"session_id,omitempty" bson:"session_id,omitempty"`
	Decisions   []string              `json:"decisions,omitempty" bson:"decisions,omitempty"`
	Completed   []string              `json:"completed,omitempty" bson:"completed,omitempty"`
	NextSteps   []string              `json:"next_steps,omitempty" bson:"next_steps,omitempty"`
	Constraints []string              `json:"constraints,omitempty" bson:"constraints,omitempty"`
	Files       []WorkCheckpointFile  `json:"files,omitempty" bson:"files,omitempty"`
	Checks      []WorkCheckpointCheck `json:"checks,omitempty" bson:"checks,omitempty"`
	SourceRefs  []Ref                 `json:"source_refs,omitempty" bson:"source_refs,omitempty"`
}

type WorkCheckpointFile struct {
	Path   string `json:"path" bson:"path"`
	SHA256 string `json:"sha256" bson:"sha256"`
}

type WorkCheckpointCheck struct {
	Command string `json:"command" bson:"command"`
	Outcome string `json:"outcome" bson:"outcome"`
	Summary string `json:"summary,omitempty" bson:"summary,omitempty"`
}

type WorkCheckpointSaveInput struct {
	ArchiveMessages  []ArchiveMessageRef   `json:"archive_messages,omitempty"`
	Scope            Scope                 `json:"scope"`
	ID               string                `json:"id"`
	EventID          string                `json:"event_id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Package          WorkCheckpointPackage `json:"package"`
}

// WorkCheckpointSaveOutput is metadata only: an old replayed response must
// never mix the current package with the revision recorded then.
type WorkCheckpointSaveOutput struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	Authority string `json:"authority"`
}

type WorkCheckpointGetInput struct {
	Scope Scope  `json:"scope"`
	ID    string `json:"id"`
}

type WorkCheckpointView struct {
	ArchiveMessages []ArchiveMessageRef   `json:"archive_messages,omitempty"`
	ID              string                `json:"id"`
	Revision        int64                 `json:"revision"`
	Package         WorkCheckpointPackage `json:"package"`
	UpdatedAt       time.Time             `json:"updated_at"`
	Authority       string                `json:"authority"`
}

type WorkCheckpointListInput struct {
	Scope Scope  `json:"scope"`
	After string `json:"after"`
	Limit int    `json:"limit"`
}

type WorkCheckpointListItem struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Origin    string    `json:"origin"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WorkCheckpointListOutput struct {
	Items     []WorkCheckpointListItem `json:"items"`
	Next      string                   `json:"next_cursor,omitempty"`
	Authority string                   `json:"authority"`
}

// WorkCheckpointStore is the optional repository extension for listing
// checkpoints. Like SessionArchiveStore it is deliberately not part of
// Repository: generic CRUD can never reach these collections.
type WorkCheckpointStore interface {
	// WorkCheckpointRows returns up to limit checkpoint rows of one scope in
	// ascending ID order, optionally strictly after a cursor ID.
	WorkCheckpointRows(ctx context.Context, scope Scope, after string, limit int) ([]Record, error)
}

func validWorkCheckpointString(s string, maxN int) bool {
	return utf8.ValidString(s) && len(s) > 0 && len(s) <= maxN && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validWorkCheckpointOutcome(outcome string) bool {
	return outcome == "passed" || outcome == "failed" || outcome == "not_run"
}

// validWorkCheckpointRef mirrors the relation gate: a known live-referencable
// collection, never jobs/audit/private collections.
func validWorkCheckpointRef(ref Ref) bool {
	return knownCollection(ref.Collection) && ref.Collection != colJobs && ref.Collection != "audit_events" && validRunLabel(ref.ID, 256) && ref.Revision >= 1
}

// validateWorkCheckpointPackage enforces the field contract and runs the full
// secret scan over the whole package. It returns the normalized package
// (status defaults to in_progress).
func validateWorkCheckpointPackage(in WorkCheckpointPackage) (WorkCheckpointPackage, error) {
	p := in
	if !validWorkCheckpointString(p.Title, workCheckpointMaxString) ||
		!validWorkCheckpointString(p.Goal, workCheckpointMaxString) ||
		!validWorkCheckpointString(p.Repository, workCheckpointMaxString) ||
		!validWorkCheckpointString(p.Origin, workCheckpointMaxString) {
		return WorkCheckpointPackage{}, ErrInvalid
	}
	if p.Status == "" {
		p.Status = workCheckpointStatusInProgress
	}
	switch p.Status {
	case workCheckpointStatusInProgress, workCheckpointStatusBlocked, workCheckpointStatusCompleted:
	default:
		return WorkCheckpointPackage{}, ErrInvalid
	}
	if !validWorkCheckpointNotes(p) || !validWorkCheckpointEvidence(p) {
		return WorkCheckpointPackage{}, ErrInvalid
	}
	if encoded, err := json.Marshal(p); err != nil || len(encoded) > workCheckpointMaxJSON {
		return WorkCheckpointPackage{}, ErrInvalid
	}
	if containsSecret(structuredText(p)) {
		return WorkCheckpointPackage{}, ErrSecretDetected
	}
	return p, nil
}

// validWorkCheckpointNotes checks the optional strings and the string lists.
func validWorkCheckpointNotes(p WorkCheckpointPackage) bool {
	for _, s := range []string{p.Branch, p.Head, p.SessionID} {
		if s != "" && !validWorkCheckpointString(s, workCheckpointMaxString) {
			return false
		}
	}
	for _, list := range [][]string{p.Decisions, p.Completed, p.NextSteps, p.Constraints} {
		if len(list) > workCheckpointMaxArray {
			return false
		}
		for _, s := range list {
			if !validWorkCheckpointString(s, workCheckpointMaxString) {
				return false
			}
		}
	}
	return true
}

// validWorkCheckpointEvidence checks files, checks and source references.
func validWorkCheckpointEvidence(p WorkCheckpointPackage) bool {
	if len(p.Files) > workCheckpointMaxArray || len(p.Checks) > workCheckpointMaxArray || len(p.SourceRefs) > workCheckpointMaxArray {
		return false
	}
	for _, f := range p.Files {
		if !validWorkCheckpointString(f.Path, workCheckpointMaxString) || !sourceHashPattern.MatchString(f.SHA256) {
			return false
		}
	}
	for _, c := range p.Checks {
		if !validWorkCheckpointString(c.Command, workCheckpointMaxString) || !validWorkCheckpointOutcome(c.Outcome) ||
			c.Summary != "" && !validWorkCheckpointString(c.Summary, workCheckpointMaxString) {
			return false
		}
	}
	for _, ref := range p.SourceRefs {
		if !validWorkCheckpointRef(ref) {
			return false
		}
	}
	return true
}

func isWorkCheckpoint(r Record) bool {
	return r.Kind == kindWorkCheckpoint && r.WorkCheckpoint != nil && r.WorkCheckpoint.Package != nil
}

func isWorkCheckpointReceipt(r Record) bool {
	return r.Kind == kindWorkCheckpointReceipt && r.WorkCheckpoint != nil && r.WorkCheckpoint.RequestHash != "" &&
		r.WorkCheckpoint.ReceiptTargetID != "" && r.WorkCheckpoint.ReceiptRevision >= 1 && r.WorkCheckpoint.Package == nil
}

// workCheckpointReceiptID derives the idempotency receipt identity from the
// checkpoint identity, the caller's event and the principal. The body never
// enters the receipt, only its hash.
func workCheckpointReceiptID(scope Scope, checkpointID, eventID, principal string) string {
	return ContentHash(strings.Join([]string{structuredText(scope), checkpointID, eventID, principal}, "\n"))
}

func workCheckpointListItem(r Record) WorkCheckpointListItem {
	p := r.WorkCheckpoint.Package
	return WorkCheckpointListItem{ID: r.ID, Revision: r.Revision, Title: p.Title, Status: p.Status, Origin: p.Origin, UpdatedAt: r.UpdatedAt}
}

// authorizeWorkCheckpointSourceRefs checks read permission for every source
// reference before the save transaction, like parent checks in create. Inside
// the transaction only existence, revision and liveness are re-checked.
func (s *Service) authorizeWorkCheckpointSourceRefs(ctx context.Context, token string, scope Scope, refs []Ref, actor Decision) error {
	for _, ref := range refs {
		d, err := s.authorize(ctx, token, "read:"+ref.Collection, scope, ref.ID, "")
		if err != nil {
			return err
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return ErrDenied
		}
	}
	return nil
}

// validateWorkCheckpointSourceRefs re-checks every source reference inside the
// save transaction: same scope, exact revision, live and not content-erased.
func validateWorkCheckpointSourceRefs(ctx context.Context, tx Tx, scope Scope, refs []Ref) error {
	for _, ref := range refs {
		target, err := tx.Get(ctx, ref.Collection, scope, ref.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err != nil || target.Revision != ref.Revision || target.PrivacyQuarantined || !live(target) {
			return ErrConflict
		}
		if target.ContentErased {
			return ErrDenied
		}
		if err := tx.FenceSearchRead(ctx, scope, ref.Collection, ref.ID, ref.Revision); err != nil {
			return err
		}
	}
	return nil
}

// SaveWorkCheckpoint stores or advances one work checkpoint. Same
// scope/id/event_id plus identical payload replays the recorded result;
// the same event_id with a different request is a conflict. CAS, receipt and
// audit live in one transaction and roll back together.
func (s *Service) SaveWorkCheckpoint(ctx context.Context, token string, in WorkCheckpointSaveInput) (WorkCheckpointSaveOutput, error) {
	if s.Repo == nil {
		return WorkCheckpointSaveOutput{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !workCheckpointIDPattern.MatchString(in.ID) || !workCheckpointIDPattern.MatchString(in.EventID) || in.ExpectedRevision < 0 {
		return WorkCheckpointSaveOutput{}, ErrInvalid
	}
	if containsSecret(structuredText(in.Scope)) {
		return WorkCheckpointSaveOutput{}, ErrSecretDetected
	}
	pkg, err := validateWorkCheckpointPackage(in.Package)
	if err != nil {
		return WorkCheckpointSaveOutput{}, err
	}
	// Policy sees the sanitized package; the receipt keeps only its hash.
	in.Package = pkg
	requestHash := ContentHash(structuredText(in))
	d, e := s.authorize(ctx, token, "write:"+colWorkCheckpoints, in.Scope, in.ID, structuredText(pkg))
	if e != nil {
		return WorkCheckpointSaveOutput{}, e
	}
	// human/service/agent may write; nobody gains approval or deletion power.
	if d.Role != roleHuman && d.Role != roleService && d.Role != roleAgent {
		return WorkCheckpointSaveOutput{}, ErrDenied
	}
	// Every nested authorization must answer for the same principal (actor
	// 주체 재검사); source-ref read permission is checked before the store
	// transaction, which re-checks existence, revision and liveness.
	ctx = bindRequestActor(ctx, d)
	if err := s.authorizeWorkCheckpointSourceRefs(ctx, token, in.Scope, pkg.SourceRefs, d); err != nil {
		return WorkCheckpointSaveOutput{}, err
	}
	receiptID := workCheckpointReceiptID(in.Scope, in.ID, in.EventID, d.PrincipalID)
	// A committed exact retry needs no raw reread: it can only return metadata.
	prior, receiptErr := s.Repo.Get(ctx, colWorkCheckpointReceipts, in.Scope, receiptID)
	replayOnly := receiptErr == nil
	if receiptErr != nil && !errors.Is(receiptErr, ErrNotFound) {
		return WorkCheckpointSaveOutput{}, receiptErr
	}
	var archiveHeaders []Record
	var archiveRefs []ArchiveMessageRef
	if !replayOnly {
		archiveHeaders, archiveRefs, err = s.workCheckpointArchives(ctx, token, in, d)
		if err != nil {
			return WorkCheckpointSaveOutput{}, err
		}
	} else if !workCheckpointReceiptMatches(prior, d.PrincipalID, requestHash) {
		return WorkCheckpointSaveOutput{}, ErrConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	var out WorkCheckpointSaveOutput
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = WorkCheckpointSaveOutput{}
		if err := fenceWorkSubject(ctx, tx, in.Scope); err != nil {
			return err
		}
		previous, err := tx.Get(ctx, colWorkCheckpointReceipts, in.Scope, receiptID)
		switch {
		case err == nil:
			// Same event replay: only an identical payload returns the revision
			// recorded then; anything else is a conflicting request.
			if !workCheckpointReceiptMatches(previous, d.PrincipalID, requestHash) || previous.WorkCheckpoint.ReceiptTargetID != in.ID {
				return ErrConflict
			}
			out = WorkCheckpointSaveOutput{ID: in.ID, Revision: previous.WorkCheckpoint.ReceiptRevision, Authority: AuthorityDataOnly}
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if replayOnly {
			return ErrConflict
		}
		current, err := tx.Get(ctx, colWorkCheckpoints, in.Scope, in.ID)
		record, created, err := nextWorkCheckpointRecord(current, err, in, &pkg, now, d)
		if err != nil {
			return err
		}
		if err := s.fenceSourceArchives(ctx, tx, archiveHeaders); err != nil {
			return err
		}
		existing := []ArchiveMessageRef(nil)
		if !created {
			existing = current.WorkCheckpoint.ArchiveMessages
		}
		refs, err := mergeWorkArchiveMessages(existing, archiveRefs)
		if err != nil {
			return err
		}
		record.WorkCheckpoint.ArchiveMessages = refs
		if err := validateWorkCheckpointSourceRefs(ctx, tx, in.Scope, pkg.SourceRefs); err != nil {
			return err
		}
		if created {
			if err := tx.Insert(ctx, colWorkCheckpoints, record); err != nil {
				return err
			}
		} else if err := tx.Replace(ctx, colWorkCheckpoints, record, in.ExpectedRevision); err != nil {
			return err
		}
		if err := tx.Insert(ctx, colWorkCheckpointReceipts, Record{
			ID: receiptID, Scope: in.Scope, Kind: kindWorkCheckpointReceipt, Status: JobStatusDone, Revision: 1,
			CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			Target:         &Ref{Collection: colWorkCheckpoints, ID: in.ID, Revision: record.Revision},
			WorkCheckpoint: &WorkCheckpoint{RequestHash: requestHash, ReceiptTargetID: in.ID, ReceiptRevision: record.Revision},
		}); err != nil {
			return err
		}
		action := "update:" + colWorkCheckpoints
		if created {
			action = "create:" + colWorkCheckpoints
		}
		if err := audit(ctx, tx, in.Scope, d.PrincipalID, action, in.ID); err != nil {
			return err
		}
		out = WorkCheckpointSaveOutput{ID: in.ID, Revision: record.Revision, Authority: AuthorityDataOnly}
		return nil
	})
	if e != nil {
		return WorkCheckpointSaveOutput{}, e
	}
	return out, nil
}

// workCheckpointArchives validates the cited archive messages and that the
// caller may read every archive they come from.
func (s *Service) workCheckpointArchives(ctx context.Context, token string, in WorkCheckpointSaveInput, d Decision) ([]Record, []ArchiveMessageRef, error) {
	archiveHeaders, archiveRefs, err := s.validateArchiveMessageRefs(ctx, token, in.Scope, in.ArchiveMessages, "", "")
	if err != nil {
		return nil, nil, err
	}
	for i, ref := range archiveRefs {
		if ref.SHA256 != in.ArchiveMessages[i].SHA256 {
			return nil, nil, ErrConflict
		}
	}
	for _, h := range archiveHeaders {
		who, err := s.authorize(ctx, token, "read:"+colSessionArchives, in.Scope, h.ID, "")
		if err != nil {
			return nil, nil, err
		}
		if who.PrincipalID != d.PrincipalID || who.Role != d.Role {
			return nil, nil, ErrDenied
		}
	}
	return archiveHeaders, archiveRefs, nil
}

// workCheckpointReceiptMatches reports whether r is a usable receipt recorded
// by principal for the same request.
func workCheckpointReceiptMatches(r Record, principal, requestHash string) bool {
	return !r.PrivacyQuarantined && !r.ContentErased && isWorkCheckpointReceipt(r) && r.CreatedBy == principal && r.WorkCheckpoint.RequestHash == requestHash
}

// nextWorkCheckpointRecord builds the record to store from the current row
// (or its absence), enforcing the expected revision.
func nextWorkCheckpointRecord(current Record, err error, in WorkCheckpointSaveInput, pkg *WorkCheckpointPackage, now time.Time, d Decision) (Record, bool, error) {
	switch {
	case errors.Is(err, ErrNotFound):
		if in.ExpectedRevision != 0 {
			return Record{}, false, ErrConflict
		}
		return Record{ID: in.ID, Scope: in.Scope, Kind: kindWorkCheckpoint, Status: statusActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			WorkCheckpoint: &WorkCheckpoint{Package: pkg}}, true, nil
	case err != nil:
		return Record{}, false, err
	}
	if !isWorkCheckpoint(current) || !live(current) || current.ContentErased {
		return Record{}, false, ErrConflict
	}
	if current.Revision != in.ExpectedRevision {
		return Record{}, false, ErrConflict
	}
	record := current
	record.Revision = current.Revision + 1
	record.UpdatedAt = now
	record.WorkCheckpoint = &WorkCheckpoint{Package: pkg}
	return record, false, nil
}

// GetWorkCheckpoint returns the package as untrusted data and audits the read
// without any body.
func (s *Service) GetWorkCheckpoint(ctx context.Context, token string, in WorkCheckpointGetInput) (WorkCheckpointView, error) {
	if s.Repo == nil {
		return WorkCheckpointView{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !workCheckpointIDPattern.MatchString(in.ID) {
		return WorkCheckpointView{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:"+colWorkCheckpoints, in.Scope, in.ID, "")
	if e != nil {
		return WorkCheckpointView{}, e
	}
	r, e := s.Repo.Get(ctx, colWorkCheckpoints, in.Scope, in.ID)
	if e != nil {
		return WorkCheckpointView{}, e
	}
	if !live(r) || r.ContentErased {
		return WorkCheckpointView{}, ErrNotFound
	}
	if !isWorkCheckpoint(r) {
		return WorkCheckpointView{}, ErrConflict
	}
	ctx = bindRequestActor(ctx, d)
	if e := s.authorizeWorkCheckpointSourceRefs(ctx, token, in.Scope, r.WorkCheckpoint.Package.SourceRefs, d); e != nil {
		return WorkCheckpointView{}, e
	}
	if e := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.Get(ctx, colWorkCheckpoints, in.Scope, in.ID)
		if err != nil {
			return err
		}
		if !isWorkCheckpoint(current) || !live(current) || current.ContentErased || current.Revision != r.Revision {
			return ErrConflict
		}
		if err := validateWorkCheckpointSourceRefs(ctx, tx, in.Scope, current.WorkCheckpoint.Package.SourceRefs); err != nil {
			return err
		}
		if err := fenceWorkCheckpoint(ctx, tx, in.Scope, current.ID, current.Revision); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "read:"+colWorkCheckpoints, in.ID)
	}); e != nil {
		return WorkCheckpointView{}, e
	}
	return WorkCheckpointView{ArchiveMessages: r.WorkCheckpoint.ArchiveMessages, ID: r.ID, Revision: r.Revision, Package: *r.WorkCheckpoint.Package, UpdatedAt: r.UpdatedAt, Authority: AuthorityDataOnly}, nil
}

// ListWorkCheckpoints returns one ID-ordered page of checkpoint metadata.
// Scope fencing stays in the repository; every returned row is re-authorized
// for the bound request actor.
func (s *Service) ListWorkCheckpoints(ctx context.Context, token string, in WorkCheckpointListInput) (WorkCheckpointListOutput, error) {
	if s.Repo == nil {
		return WorkCheckpointListOutput{}, ErrUnavailable
	}
	if in.Limit == 0 {
		in.Limit = workCheckpointDefaultLimit
	}
	if !validScope(in.Scope) || in.Limit < 1 || in.Limit > workCheckpointMaxLimit || in.After != "" && !workCheckpointIDPattern.MatchString(in.After) {
		return WorkCheckpointListOutput{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:"+colWorkCheckpoints, in.Scope, "", "")
	if e != nil {
		return WorkCheckpointListOutput{}, e
	}
	store, ok := s.Repo.(WorkCheckpointStore)
	if !ok {
		return WorkCheckpointListOutput{}, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, d)
	rows, e := store.WorkCheckpointRows(ctx, in.Scope, in.After, in.Limit+1)
	if e != nil {
		return WorkCheckpointListOutput{}, e
	}
	out := WorkCheckpointListOutput{Items: []WorkCheckpointListItem{}, Authority: AuthorityDataOnly}
	for _, r := range rows {
		if e := s.checkWorkCheckpointListRow(ctx, token, in.Scope, d, r); e != nil {
			return WorkCheckpointListOutput{}, e
		}
		out.Items = append(out.Items, workCheckpointListItem(r))
		if len(out.Items) == in.Limit {
			break
		}
	}
	if e := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, item := range out.Items {
			current, err := tx.Get(ctx, colWorkCheckpoints, in.Scope, item.ID)
			if err != nil {
				return err
			}
			if !isWorkCheckpoint(current) || !live(current) || current.ContentErased || current.Revision != item.Revision {
				return ErrConflict
			}
			if err := validateWorkCheckpointSourceRefs(ctx, tx, in.Scope, current.WorkCheckpoint.Package.SourceRefs); err != nil {
				return err
			}
			if err := fenceWorkCheckpoint(ctx, tx, in.Scope, current.ID, current.Revision); err != nil {
				return err
			}
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "read:work_checkpoints:list", "")
	}); e != nil {
		return WorkCheckpointListOutput{}, e
	}
	if len(rows) > in.Limit && len(out.Items) > 0 {
		out.Next = out.Items[len(out.Items)-1].ID
	}
	return out, nil
}

// checkWorkCheckpointListRow re-authorizes one listed row for the bound actor.
func (s *Service) checkWorkCheckpointListRow(ctx context.Context, token string, scope Scope, d Decision, r Record) error {
	if !sameScope(r.Scope, scope) {
		return ErrUnavailable
	}
	if !isWorkCheckpoint(r) {
		return ErrConflict
	}
	row, e := s.authorize(ctx, token, "read:"+colWorkCheckpoints, scope, r.ID, "")
	if e != nil {
		return e
	}
	if row.PrincipalID != d.PrincipalID || row.Role != d.Role {
		return ErrDenied
	}
	if !live(r) || r.ContentErased {
		return ErrConflict
	}
	return s.authorizeWorkCheckpointSourceRefs(ctx, token, scope, r.WorkCheckpoint.Package.SourceRefs, d)
}

var _ WorkCheckpointStore = (*Postgres)(nil)

// WorkCheckpointRows reuses selectRecords, so the tenant fence stays in
// exactly one place. Receipts live in their own collection and are never
// listed here.
func (p *Postgres) WorkCheckpointRows(ctx context.Context, scope Scope, after string, limit int) ([]Record, error) {
	if checkScope(scope) != nil || limit < 1 || limit > workCheckpointMaxLimit+1 || after != "" && !workCheckpointIDPattern.MatchString(after) {
		return nil, ErrInvalid
	}
	w, args := "status='active'", []any{}
	if after != "" {
		w += fmt.Sprintf(` AND id>$%d`, len(args)+6)
		args = append(args, after)
	}
	return p.selectRecords(ctx, colWorkCheckpoints, scope, w, args, "id ASC", limit)
}

// workCheckpointRoutes registers the three POST JSON endpoints. Save decodes
// with the dedicated 68 KiB cap (64 KiB package + envelope); get/list use the
// default decode cap.
func workCheckpointRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/work-checkpoints/save", func(w http.ResponseWriter, r *http.Request) {
		var in WorkCheckpointSaveInput
		if !decodeLimit(w, r, &in, workCheckpointBodyLimit) {
			return
		}
		out, e := s.SaveWorkCheckpoint(r.Context(), bearer(r), in)
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/work-checkpoints/get", func(w http.ResponseWriter, r *http.Request) {
		var in WorkCheckpointGetInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.GetWorkCheckpoint(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/work-checkpoints/list", func(w http.ResponseWriter, r *http.Request) {
		var in WorkCheckpointListInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ListWorkCheckpoints(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

// Fence a read without changing the public revision.
func (p *Postgres) FenceWorkCheckpoint(ctx context.Context, scope Scope, id string, revision int64) error {
	return p.fence(ctx, scope, colWorkCheckpoints, id, revision, "search")
}
func fenceWorkCheckpoint(ctx context.Context, tx Tx, scope Scope, id string, revision int64) error {
	store, ok := tx.(interface {
		FenceWorkCheckpoint(context.Context, Scope, string, int64) error
	})
	if !ok {
		return ErrUnavailable
	}
	return store.FenceWorkCheckpoint(ctx, scope, id, revision)
}

// Preserve earlier immutable provenance when append/reconstruction moved its ref.
func mergeWorkArchiveMessages(existing, incoming []ArchiveMessageRef) ([]ArchiveMessageRef, error) {
	out := append([]ArchiveMessageRef(nil), existing...)
	for _, ref := range incoming {
		found := false
		for _, old := range out {
			if sameSourceArchiveMessages([]ArchiveMessageRef{old}, []ArchiveMessageRef{ref}) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, ref)
		}
	}
	if len(out) > 100 {
		return nil, ErrInvalid
	}
	return out, nil
}
