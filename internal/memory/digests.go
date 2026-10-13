package memory

// Digests: short documents derived only from approved memories.
//
//   - topic_digests: up to TopicDigestCap per scope. Each has a title, a body
//     (≤ 1500 characters) and the member memories it was written from, with
//     their revisions. Heat is the members' injection and usage counts.
//   - user_digests: one short summary of the person (≤ 2000 characters) from
//     approved user memories. A cheap judge decides whether newly approved
//     memories are worth a new version (conflict / new_rule / minor / none).
//
// A model only ever proposes: every new or changed digest is a candidate with a
// diff against the version it would replace, and only a person makes it active.
// Text is checked for secrets before it is stored. A digest whose member
// memory is no longer approved and active stops being served and its body is
// cleared on the next worker sweep, so an erased memory leaves no copy here.
// Served digests are data, never instructions (AuthorityDataOnly).

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	colTopicDigests = "topic_digests"
	colUserDigests  = "user_digests"

	digestKindTopic = "topic"
	digestKindUser  = "user"
	digestKindState = "state"

	digestCandidate  = "candidate"
	digestActive     = statusActive
	digestRejected   = "rejected"
	digestSuperseded = "superseded"
	digestStale      = "stale"
	digestState      = "state"

	topicDigestBodyMax = 1500
	userDigestBodyMax  = 2000
	digestTitleMax     = 120
	digestMembersMax   = 64
	digestJudgedMax    = 2000
	digestListLimit    = 500
)

// TopicDigestCap is the most active topic digests one scope keeps (MEMORY_TOPIC_DIGEST_MAX).
// At the cap a new topic is not proposed; the two coldest similar digests are proposed for merging instead.
var TopicDigestCap = 15

// UserDigestMinorThreshold is how many "minor" judge verdicts add up to one regeneration.
var UserDigestMinorThreshold = 5

var (
	digestJudgeVerdicts = []string{"conflict", "new_rule", "minor", "none"}
	digestDecisions     = map[string]string{"approve": digestActive, "reject": digestRejected}
	// errDigestChanged: a member memory or the replaced digest changed after the proposal.
	errDigestChanged = errors.New("digest: basis changed")
	// errDigestCap: the scope already has TopicDigestCap active topic digests.
	errDigestCap = fmt.Errorf("%w: topic digest cap", ErrConflict)
)

// DigestData is the digest part of a record. Body is Record.Text.
type DigestData struct {
	Title string `json:"title,omitempty" bson:"title,omitempty"`
	// Members are the approved memories the body was written from.
	Members []Ref `json:"members,omitempty" bson:"members,omitempty"`
	// Replaces are the active digests an approval supersedes: none (new), one (update) or two (merge).
	Replaces []Ref  `json:"replaces,omitempty" bson:"replaces,omitempty"`
	Proposal string `json:"proposal,omitempty" bson:"proposal,omitempty"`
	Judge    string `json:"judge,omitempty" bson:"judge,omitempty"`
	// State record only: memories the judge has seen, minor verdicts since the
	// last candidate, the verdict waiting for a candidate, and regenerations a person asked for.
	Judged     []Ref    `json:"judged,omitempty" bson:"judged,omitempty"`
	Minor      int      `json:"minor,omitempty" bson:"minor,omitempty"`
	Pending    string   `json:"pending,omitempty" bson:"pending,omitempty"`
	Regenerate []string `json:"regenerate,omitempty" bson:"regenerate,omitempty"`
}

type DigestHeat struct {
	Injected int `json:"injected"`
	Used     int `json:"used"`
	Score    int `json:"score"`
}

type DigestDiffLine struct {
	Op   string `json:"op"` // "=", "-", "+"
	Text string `json:"text"`
}

// DigestDiff compares a candidate with the active version(s) it would replace.
type DigestDiff struct {
	Base           []Ref            `json:"base"`
	TitleBefore    string           `json:"title_before,omitempty"`
	TitleAfter     string           `json:"title_after,omitempty"`
	Lines          []DigestDiffLine `json:"lines"`
	MembersAdded   []Ref            `json:"members_added"`
	MembersRemoved []Ref            `json:"members_removed"`
}

// DigestView is what the API serves. Body is empty when the digest is not fresh.
type DigestView struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Revision  int64           `json:"revision"`
	Title     string          `json:"title,omitempty"`
	Body      string          `json:"body"`
	Members   []Ref           `json:"members"`
	Replaces  []Ref           `json:"replaces,omitempty"`
	Proposal  string          `json:"proposal,omitempty"`
	Judge     string          `json:"judge,omitempty"`
	Tags      []string        `json:"tags,omitempty"`
	Heat      DigestHeat      `json:"heat"`
	Fresh     bool            `json:"fresh"`
	Outdated  bool            `json:"outdated,omitempty"`
	Diff      *DigestDiff     `json:"diff,omitempty"`
	Generator *Generator      `json:"generator,omitempty"`
	Review    *ReviewDecision `json:"review,omitempty"`
	CreatedBy string          `json:"created_by,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Authority string          `json:"authority"`
}

type DigestPage struct {
	Items     []DigestView `json:"items"`
	Cap       int          `json:"cap,omitempty"`
	Active    int          `json:"active"`
	Authority string       `json:"authority"`
}

type DigestProposalInput struct {
	Scope     Scope      `json:"scope"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title,omitempty"`
	Body      string     `json:"body"`
	Members   []Ref      `json:"members"`
	Replaces  []Ref      `json:"replaces,omitempty"`
	Generator *Generator `json:"generator,omitempty"`
	// Usage is the worker's model call that wrote this proposal (ledger only).
	Usage *ModelCallUsage `json:"usage,omitempty"`
}

type DigestDecisionInput struct {
	Scope            Scope  `json:"scope"`
	Kind             string `json:"kind"`
	ExpectedRevision int64  `json:"expected_revision"`
	Decision         string `json:"decision"`
	Reason           string `json:"reason,omitempty"`
}

type DigestRegenerateInput struct {
	Scope Scope  `json:"scope"`
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
}

type DigestJudgeInput struct {
	Scope   Scope  `json:"scope"`
	Judged  []Ref  `json:"judged"`
	Verdict string `json:"verdict"`
	// Usage is the judge model call behind the verdict (ledger only).
	Usage *ModelCallUsage `json:"usage,omitempty"`
}

type DigestJudgeResult struct {
	Regenerate bool   `json:"regenerate"`
	Pending    string `json:"pending,omitempty"`
	Minor      int    `json:"minor"`
}

func digestCollection(kind string) (string, bool) {
	switch kind {
	case digestKindTopic:
		return colTopicDigests, true
	case digestKindUser:
		return colUserDigests, true
	}
	return "", false
}

func digestCap() int {
	if TopicDigestCap < 1 {
		return 1
	}
	return min(TopicDigestCap, 100)
}

func digestStateID(col string, scope Scope) string {
	prefix := "tds_"
	if col == colUserDigests {
		prefix = "uds_"
	}
	return prefix + ContentHash(structuredText(scope))[:32]
}

func validDigestRefs(refs []Ref, collection string, max int) bool {
	if len(refs) > max {
		return false
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if r.Collection != collection || !validRunLabel(r.ID, 256) || r.Revision < 1 || seen[r.ID] {
			return false
		}
		seen[r.ID] = true
	}
	return true
}

// eligibleMember is an approved, active memory outside any conflict.
func eligibleMember(m Record, kind string) bool {
	return live(m) && verifiedMemory(m) && m.ConflictID == "" && !m.ContentErased && (kind == "" || m.Kind == kind)
}

// memberKind is the memory kind a digest of this kind may be built from ("" = any approved memory).
func memberKind(kind string) string {
	if kind == digestKindUser {
		return digestKindUser
	}
	return ""
}

func (s *Service) digestAuthorize(ctx context.Context, token, verb, col string, scope Scope, id string) (Decision, error) {
	if !validScope(scope) {
		return Decision{}, ErrInvalid
	}
	return s.authorize(ctx, token, verb+":"+col, scope, id, "")
}

// digestHeat is the members' injected and used counts from the host's last memory-use report.
func (s *Service) digestHeatTable(ctx context.Context, scope Scope) map[string]MemoryUseStat {
	out := map[string]MemoryUseStat{}
	doc, err := agentToolsDoc(ctx, s.Repo, scope)
	if err != nil || doc.AgentTools == nil {
		return out
	}
	use := doc.AgentTools.MemoryUse30
	if use == nil {
		use = doc.AgentTools.MemoryUse
	}
	if use == nil {
		return out
	}
	for _, it := range use.Items {
		out[it.ID] = it
	}
	return out
}

func heatOf(members []Ref, table map[string]MemoryUseStat) DigestHeat {
	h := DigestHeat{}
	for _, m := range members {
		h.Injected += table[m.ID].Injected
		h.Used += table[m.ID].Used
	}
	h.Score = h.Injected + h.Used
	return h
}

// freshness is (all members still eligible, some member has a newer revision).
func freshness(ctx context.Context, tx Tx, scope Scope, kind string, members []Ref) (bool, bool) {
	if len(members) == 0 {
		return false, false
	}
	outdated := false
	for _, ref := range members {
		m, err := tx.Get(ctx, colMemories, scope, ref.ID)
		if err != nil || !eligibleMember(m, memberKind(kind)) {
			return false, false
		}
		outdated = outdated || m.Revision != ref.Revision
	}
	return true, outdated
}

func (s *Service) digestView(ctx context.Context, r Record, heat map[string]MemoryUseStat) DigestView {
	d := DigestData{}
	if r.Digest != nil {
		d = *r.Digest
	}
	v := DigestView{ID: r.ID, Kind: r.Kind, Status: r.Status, Revision: r.Revision, Title: d.Title, Members: refsOrEmpty(d.Members), Replaces: d.Replaces,
		Proposal: d.Proposal, Judge: d.Judge, Tags: r.Tags, Heat: heatOf(d.Members, heat), Generator: r.Generator, Review: r.Review,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Authority: AuthorityDataOnly}
	if r.Status == digestActive || r.Status == digestCandidate {
		v.Fresh, v.Outdated = freshness(ctx, s.Repo, r.Scope, r.Kind, d.Members)
	}
	if v.Fresh {
		v.Body = r.Text
	}
	if r.Status == digestCandidate && v.Fresh {
		v.Diff = s.digestDiff(ctx, r)
	}
	return v
}

func refsOrEmpty(refs []Ref) []Ref {
	if refs == nil {
		return []Ref{}
	}
	return refs
}

// digestDiff compares a candidate with the active versions it replaces (none for a new digest).
func (s *Service) digestDiff(ctx context.Context, r Record) *DigestDiff {
	col, _ := digestCollection(r.Kind)
	before, titles, members := []string{}, []string{}, []Ref{}
	for _, ref := range r.Digest.Replaces {
		base, err := s.Repo.Get(ctx, col, r.Scope, ref.ID)
		if err != nil || base.Digest == nil {
			continue
		}
		before = append(before, base.Text)
		if base.Digest.Title != "" {
			titles = append(titles, base.Digest.Title)
		}
		members = append(members, base.Digest.Members...)
	}
	out := &DigestDiff{Base: refsOrEmpty(r.Digest.Replaces), TitleBefore: strings.Join(titles, " + "), TitleAfter: r.Digest.Title,
		Lines: diffLines(strings.Join(before, "\n"), r.Text), MembersAdded: []Ref{}, MembersRemoved: []Ref{}}
	key := func(x Ref) string { return x.ID + "@" + itoa64(x.Revision) }
	old, now := map[string]bool{}, map[string]bool{}
	for _, m := range members {
		old[key(m)] = true
	}
	for _, m := range r.Digest.Members {
		now[key(m)] = true
		if !old[key(m)] {
			out.MembersAdded = append(out.MembersAdded, m)
		}
	}
	for _, m := range members {
		if !now[key(m)] {
			out.MembersRemoved = append(out.MembersRemoved, m)
		}
	}
	return out
}

// ListDigests lists one kind in one status (default active), hottest first.
func (s *Service) ListDigests(ctx context.Context, token string, scope Scope, kind, status string) (DigestPage, error) {
	col, ok := digestCollection(kind)
	if status == "" {
		status = digestActive
	}
	if !ok || !slices.Contains([]string{digestActive, digestCandidate, digestRejected, digestSuperseded, digestStale}, status) {
		return DigestPage{}, ErrInvalid
	}
	if _, err := s.digestAuthorize(ctx, token, "read", col, scope, ""); err != nil {
		return DigestPage{}, err
	}
	rows, err := s.Repo.ListRecordsByStatus(ctx, scope, col, status, digestListLimit)
	if err != nil {
		return DigestPage{}, err
	}
	heat := s.digestHeatTable(ctx, scope)
	out := DigestPage{Items: []DigestView{}, Authority: AuthorityDataOnly}
	for _, r := range rows {
		if r.Digest != nil && r.Kind == kind && r.Scope == scope {
			out.Items = append(out.Items, s.digestView(ctx, r, heat))
		}
	}
	slices.SortStableFunc(out.Items, func(a, b DigestView) int { return b.Heat.Score - a.Heat.Score })
	if status == digestActive {
		out.Active = len(out.Items)
	} else if active, e := s.Repo.ListRecordsByStatus(ctx, scope, col, digestActive, digestListLimit); e == nil {
		out.Active = len(active)
	}
	if kind == digestKindTopic {
		out.Cap = digestCap()
	}
	return out, nil
}

func (s *Service) GetDigest(ctx context.Context, token string, scope Scope, kind, id string) (DigestView, error) {
	col, ok := digestCollection(kind)
	if !ok || !validRunLabel(id, 256) {
		return DigestView{}, ErrInvalid
	}
	if _, err := s.digestAuthorize(ctx, token, "read", col, scope, id); err != nil {
		return DigestView{}, err
	}
	r, err := s.Repo.Get(ctx, col, scope, id)
	if err != nil {
		return DigestView{}, err
	}
	if r.Digest == nil || r.Kind != kind || r.PrivacyQuarantined {
		return DigestView{}, ErrNotFound
	}
	return s.digestView(ctx, r, s.digestHeatTable(ctx, scope)), nil
}

func validDigestText(kind, title, body string) bool {
	max := topicDigestBodyMax
	if kind == digestKindUser {
		max = userDigestBodyMax
	}
	n := utf8.RuneCountInString(body)
	t := utf8.RuneCountInString(title)
	if !utf8.ValidString(body) || !utf8.ValidString(title) || strings.TrimSpace(body) == "" || n > max || t > digestTitleMax || strings.ContainsRune(title, '\n') {
		return false
	}
	if kind == digestKindTopic {
		return strings.TrimSpace(title) != ""
	}
	return true
}

// ProposeDigest stores a candidate written by a worker. It never becomes active
// here: only DecideDigest by a person does that. Every member must be an
// approved active memory at the named revision; the replaced digests must be
// active at theirs. A topic digest is new only below the cap; a user digest
// needs a judge verdict (or a person's request) waiting for it.
func (s *Service) ProposeDigest(ctx context.Context, token string, in DigestProposalInput) (DigestView, error) {
	col, ok := digestCollection(in.Kind)
	in.Title, in.Body = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if !ok || !validScope(in.Scope) || !validDigestText(in.Kind, in.Title, in.Body) || len(in.Members) == 0 || !validDigestRefs(in.Members, colMemories, digestMembersMax) ||
		!validDigestRefs(in.Replaces, col, 2) || in.Kind == digestKindUser && len(in.Replaces) > 1 || in.Generator != nil && !validGenerator(in.Generator) {
		return DigestView{}, ErrInvalid
	}
	d, err := s.digestAuthorize(ctx, token, "write", col, in.Scope, "")
	if err != nil {
		return DigestView{}, err
	}
	s.recordModelCall(ctx, in.Scope, "digest_"+in.Kind, in.Usage)
	if containsSecret(in.Title) || containsSecret(in.Body) || in.Generator != nil && containsSecret(structuredText(in.Generator)) {
		// Never store or audit the rejected text.
		_ = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return audit(ctx, tx, in.Scope, d.PrincipalID, "inspection:blocked:"+inspectionVersion, "")
		})
		return DigestView{}, ErrSecretDetected
	}
	for _, m := range in.Members {
		if _, err := s.authorize(ctx, token, "read:memories", in.Scope, m.ID, ""); err != nil {
			return DigestView{}, err
		}
	}
	var out Record
	err = s.Repo.Transaction(s.digestCtx(ctx), func(ctx context.Context, tx Tx) error {
		sources, err := digestMembers(ctx, tx, in.Scope, in.Kind, in.Members)
		if err != nil {
			return err
		}
		proposal, err := digestReplaces(ctx, tx, col, in)
		if err != nil {
			return err
		}
		var state Record
		if in.Kind == digestKindUser {
			if state, err = digestStateRecord(ctx, tx, col, in.Scope); err != nil {
				return err
			}
			if state.Digest.Pending == "" {
				return ErrConflict // the judge has not asked for a new version
			}
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		tags := []string{}
		if instructionLike.MatchString(in.Title + "\n" + in.Body) {
			tags = append(tags, "review:instruction_like")
		}
		judge := ""
		if in.Kind == digestKindUser {
			judge = state.Digest.Pending
		}
		out = Record{ID: "dg_" + ID(), Scope: in.Scope, Kind: in.Kind, Status: digestCandidate, Revision: 1, Text: in.Body, Summary: in.Title,
			Sources: sources, Tags: tags, Generator: in.Generator, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			VerificationStatus: VerificationCandidate,
			Digest:             &DigestData{Title: in.Title, Members: in.Members, Replaces: in.Replaces, Proposal: proposal, Judge: judge}}
		if err := tx.Insert(ctx, col, out); err != nil {
			return err
		}
		if in.Kind == digestKindUser {
			st := *state.Digest
			st.Pending, st.Minor = "", 0
			st.Regenerate = slices.DeleteFunc(st.Regenerate, func(x string) bool { return x == digestKindUser })
			if err := saveDigestState(ctx, tx, col, state, st, now); err != nil {
				return err
			}
		} else if err := clearTopicRequest(ctx, tx, in.Scope, in.Replaces, now); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+col, out.ID)
	})
	if err != nil {
		return DigestView{}, err
	}
	return s.digestView(ctx, out, s.digestHeatTable(ctx, in.Scope)), nil
}

// digestMembers checks every member and returns the union of their sources (at most 64).
func digestMembers(ctx context.Context, tx Tx, scope Scope, kind string, members []Ref) ([]SourceRef, error) {
	sources, seen := []SourceRef{}, map[string]bool{}
	for _, ref := range members {
		m, err := tx.Get(ctx, colMemories, scope, ref.ID)
		if errors.Is(err, ErrNotFound) {
			return nil, ErrConflict
		}
		if err != nil {
			return nil, err
		}
		if !eligibleMember(m, memberKind(kind)) || m.Revision != ref.Revision {
			return nil, ErrConflict
		}
		if len(m.Sources) > 0 {
			if err := checkSources(ctx, tx, m); err != nil {
				return nil, ErrConflict
			}
		}
		for _, src := range m.Sources {
			key := src.SourceID + "\x00" + src.Locator
			if !seen[key] && len(sources) < 64 {
				seen[key] = true
				sources = append(sources, src)
			}
		}
	}
	return sources, nil
}

// digestReplaces checks the replaced digests and the cap, and names the proposal.
func digestReplaces(ctx context.Context, tx Tx, col string, in DigestProposalInput) (string, error) {
	for _, ref := range in.Replaces {
		base, err := tx.Get(ctx, col, in.Scope, ref.ID)
		if errors.Is(err, ErrNotFound) {
			return "", ErrConflict
		}
		if err != nil {
			return "", err
		}
		if base.Digest == nil || base.Kind != in.Kind || base.Status != digestActive || base.Revision != ref.Revision {
			return "", ErrConflict
		}
	}
	active, err := activeDigests(ctx, tx, col, in.Scope)
	if err != nil {
		return "", err
	}
	switch {
	case in.Kind == digestKindUser:
		// One summary: a new version must name the active one it replaces.
		if len(active) > 0 && len(in.Replaces) == 0 {
			return "", ErrConflict
		}
		if len(in.Replaces) == 1 {
			return "update", nil
		}
		return "new", nil
	case len(in.Replaces) == 2:
		return "merge", nil
	case len(in.Replaces) == 1:
		return "update", nil
	case len(active) >= digestCap():
		return "", errDigestCap // at the cap: merge first
	}
	return "new", nil
}

type digestLister interface {
	Get(context.Context, string, Scope, string) (Record, error)
}

type statusLister interface {
	ListRecordsByStatus(context.Context, Scope, string, string, int) ([]Record, error)
}

// digestRepo is the repository read when a transaction view cannot list (in-memory test store).
type digestRepoKey struct{}

// activeDigests lists inside the transaction when the store can (Postgres), else from the repository.
func activeDigests(ctx context.Context, tx Tx, col string, scope Scope) ([]Record, error) {
	if lister, ok := tx.(statusLister); ok {
		return lister.ListRecordsByStatus(ctx, scope, col, digestActive, digestListLimit)
	}
	if lister, ok := ctx.Value(digestRepoKey{}).(statusLister); ok {
		return lister.ListRecordsByStatus(ctx, scope, col, digestActive, digestListLimit)
	}
	return nil, ErrUnavailable
}

func (s *Service) digestCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, digestRepoKey{}, s.Repo)
}

// digestStateRecord is the scope's state record, created on first use (not stored until saved).
func digestStateRecord(ctx context.Context, tx digestLister, col string, scope Scope) (Record, error) {
	r, err := tx.Get(ctx, col, scope, digestStateID(col, scope))
	if errors.Is(err, ErrNotFound) {
		return Record{ID: digestStateID(col, scope), Scope: scope, Kind: digestKindState, Status: digestState, Digest: &DigestData{}}, nil
	}
	if err != nil {
		return Record{}, err
	}
	if r.Digest == nil {
		r.Digest = &DigestData{}
	}
	return r, nil
}

func saveDigestState(ctx context.Context, tx Tx, col string, prior Record, st DigestData, now time.Time) error {
	r := prior
	r.Digest, r.UpdatedAt = &st, now
	if prior.Revision == 0 {
		r.Revision, r.CreatedAt, r.Text = 1, now, "digest state"
		return tx.Insert(ctx, col, r)
	}
	r.Revision = prior.Revision + 1
	return tx.Replace(ctx, col, r, prior.Revision)
}

// clearTopicRequest drops a person's regeneration request once a candidate for that digest exists.
func clearTopicRequest(ctx context.Context, tx Tx, scope Scope, replaces []Ref, now time.Time) error {
	if len(replaces) == 0 {
		return nil
	}
	state, err := digestStateRecord(ctx, tx, colTopicDigests, scope)
	if err != nil || state.Revision == 0 {
		return err
	}
	st := *state.Digest
	before := len(st.Regenerate)
	st.Regenerate = slices.DeleteFunc(slices.Clone(st.Regenerate), func(x string) bool {
		return slices.ContainsFunc(replaces, func(r Ref) bool { return r.ID == x })
	})
	if len(st.Regenerate) == before {
		return nil
	}
	return saveDigestState(ctx, tx, colTopicDigests, state, st, now)
}

// DecideDigest is a person's answer to a candidate: approve makes it active and
// supersedes what it replaces, reject leaves everything as it was. A candidate
// whose basis changed goes stale instead of active.
func (s *Service) DecideDigest(ctx context.Context, token, id string, in DigestDecisionInput) (DigestView, error) {
	col, ok := digestCollection(in.Kind)
	status, known := digestDecisions[in.Decision]
	if !ok || !known || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || utf8.RuneCountInString(in.Reason) > 500 || containsSecret(in.Reason) {
		return DigestView{}, ErrInvalid
	}
	d, err := s.digestAuthorize(ctx, token, "write", col, in.Scope, id)
	if err != nil {
		return DigestView{}, err
	}
	if d.Role != roleHuman {
		return DigestView{}, ErrDenied
	}
	var out Record
	err = s.Repo.Transaction(s.digestCtx(ctx), func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, in.Scope, id)
		if err != nil {
			return err
		}
		if r.Digest == nil || r.Kind != in.Kind || r.Status != digestCandidate || r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		if status == digestActive {
			if err := approveDigestTx(ctx, tx, col, r, now); err != nil {
				return err
			}
			r.ApprovedBy, r.VerificationStatus = d.PrincipalID, VerificationVerified
		}
		prev := r.Revision
		r.Status, r.Revision, r.UpdatedAt = status, r.Revision+1, now
		r.Review = &ReviewDecision{Action: in.Decision, By: d.PrincipalID, At: now, Reason: in.Reason}
		if err := tx.Replace(ctx, col, r, prev); err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, status+":"+col, r.ID)
	})
	if errors.Is(err, errDigestChanged) {
		_ = s.markDigestStale(ctx, col, in.Scope, id, in.ExpectedRevision)
		return DigestView{}, ErrConflict
	}
	if err != nil {
		return DigestView{}, err
	}
	return s.digestView(ctx, out, s.digestHeatTable(ctx, in.Scope)), nil
}

// approveDigestTx rechecks the basis and supersedes the replaced digests.
func approveDigestTx(ctx context.Context, tx Tx, col string, r Record, now time.Time) error {
	if _, err := digestMembers(ctx, tx, r.Scope, r.Kind, r.Digest.Members); err != nil {
		return errors.Join(errDigestChanged, err)
	}
	in := DigestProposalInput{Scope: r.Scope, Kind: r.Kind, Replaces: r.Digest.Replaces}
	if _, err := digestReplaces(ctx, tx, col, in); errors.Is(err, errDigestCap) {
		return err // still a good candidate: approve after a merge
	} else if err != nil {
		return errors.Join(errDigestChanged, err)
	}
	for _, ref := range r.Digest.Replaces {
		base, err := tx.Get(ctx, col, r.Scope, ref.ID)
		if err != nil {
			return err
		}
		prev := base.Revision
		base.Status, base.Revision, base.UpdatedAt = digestSuperseded, base.Revision+1, now
		base.SupersededBy = &Ref{Collection: col, ID: r.ID, Revision: r.Revision + 1}
		if err := tx.Replace(ctx, col, base, prev); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) markDigestStale(ctx context.Context, col string, scope Scope, id string, revision int64) error {
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, scope, id)
		if err != nil || r.Revision != revision || r.Status != digestCandidate {
			return err
		}
		prev := r.Revision
		r.Status, r.Revision, r.UpdatedAt, r.Text = digestStale, r.Revision+1, time.Now().UTC().Truncate(time.Millisecond), ""
		return tx.Replace(ctx, col, r, prev)
	})
}

// RegenerateDigest records a person's request for a new version; the next
// worker run writes it as a candidate (never active on its own).
func (s *Service) RegenerateDigest(ctx context.Context, token string, in DigestRegenerateInput) (DigestJudgeResult, error) {
	col, ok := digestCollection(in.Kind)
	if !ok || in.Kind == digestKindTopic && !validRunLabel(in.ID, 256) || in.Kind == digestKindUser && in.ID != "" {
		return DigestJudgeResult{}, ErrInvalid
	}
	d, err := s.digestAuthorize(ctx, token, "write", col, in.Scope, in.ID)
	if err != nil {
		return DigestJudgeResult{}, err
	}
	if d.Role != roleHuman {
		return DigestJudgeResult{}, ErrDenied
	}
	var out DigestJudgeResult
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		target := digestKindUser
		if in.Kind == digestKindTopic {
			r, err := tx.Get(ctx, col, in.Scope, in.ID)
			if err != nil {
				return err
			}
			if r.Digest == nil || r.Kind != digestKindTopic || r.Status != digestActive {
				return ErrConflict
			}
			target = in.ID
		}
		state, err := digestStateRecord(ctx, tx, col, in.Scope)
		if err != nil {
			return err
		}
		st := *state.Digest
		if !slices.Contains(st.Regenerate, target) {
			st.Regenerate = append(slices.Clone(st.Regenerate), target)
		}
		if in.Kind == digestKindUser && st.Pending == "" {
			st.Pending = "requested"
		}
		out = DigestJudgeResult{Regenerate: true, Pending: st.Pending, Minor: st.Minor}
		if err := saveDigestState(ctx, tx, col, state, st, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "regenerate:"+col, in.ID)
	})
	return out, err
}

// JudgeUserDigest records the cheap judge's verdict on newly approved user
// memories. conflict and new_rule ask for a candidate now; minor verdicts add
// up to one; none asks for nothing. The judged memories are not asked about again.
func (s *Service) JudgeUserDigest(ctx context.Context, token string, in DigestJudgeInput) (DigestJudgeResult, error) {
	if !validScope(in.Scope) || !slices.Contains(digestJudgeVerdicts, in.Verdict) || len(in.Judged) == 0 || !validDigestRefs(in.Judged, colMemories, 200) {
		return DigestJudgeResult{}, ErrInvalid
	}
	d, err := s.digestAuthorize(ctx, token, "write", colUserDigests, in.Scope, "")
	if err != nil {
		return DigestJudgeResult{}, err
	}
	s.recordModelCall(ctx, in.Scope, "digest_judge", in.Usage)
	var out DigestJudgeResult
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, ref := range in.Judged {
			m, err := tx.Get(ctx, colMemories, in.Scope, ref.ID)
			if err != nil || !eligibleMember(m, digestKindUser) || m.Revision != ref.Revision {
				return ErrConflict
			}
		}
		state, err := digestStateRecord(ctx, tx, colUserDigests, in.Scope)
		if err != nil {
			return err
		}
		st := *state.Digest
		st.Judged = mergeJudged(st.Judged, in.Judged)
		switch in.Verdict {
		case "conflict", "new_rule":
			st.Pending = in.Verdict
		case "minor":
			st.Minor++
			if st.Minor >= max(1, UserDigestMinorThreshold) && st.Pending == "" {
				st.Pending = "minor"
			}
		}
		out = DigestJudgeResult{Regenerate: st.Pending != "", Pending: st.Pending, Minor: st.Minor}
		if err := saveDigestState(ctx, tx, colUserDigests, state, st, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "judge:"+colUserDigests+":"+in.Verdict, state.ID)
	})
	return out, err
}

func mergeJudged(prior, add []Ref) []Ref {
	out := slices.DeleteFunc(slices.Clone(prior), func(p Ref) bool {
		return slices.ContainsFunc(add, func(a Ref) bool { return a.ID == p.ID })
	})
	out = append(out, add...)
	return out[max(0, len(out)-digestJudgedMax):]
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
