package memory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

const nativeBridgeContractSHA = "52aa77d1448e54185a0b9c50265fa5a62aae7cecae9e96b5acdc6fbbc38aeac2"

const nativeBridgeProfile = "arkism-native-shadow-p1-v1"
const nativeBridgeAContract = "82936159380c3d34720ea80822db0b8c10c3242dba8bb493876c7a8895bad5f0"

// Private candidate input is never a wire record, authorization or source reader.
type nativeBridgeInput struct {
	Scope                         Scope
	Lanes                         [][]Hit
	History                       map[string]Record
	Limit, MaxChars, BudgetTokens int
	TokenEncoding                 string
	Now                           time.Time
	ClockDomain                   string
	Registry                      dyn.PolicyRegistryV2
	Policy                        dyn.PolicyID
	Published                     []nativeBridgeValue
	MaxSteps                      int64
}

type nativeBridgeMaterial struct {
	Origin     string `json:"origin"`
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	TextSHA    string `json:"text_sha256"`
	SummarySHA string `json:"summary_sha256"`
	GuardSHA   string `json:"native_guard_sha256"`
}

type nativeBridgeValue struct {
	Scope                                Scope
	Target                               nativeBridgeMaterial
	Kind                                 string
	ConfigSHA                            string
	DynamicsSHA                          string
	LineageSHA                           string
	SourceScope                          Scope
	SourceID                             string
	SourceRevision                       int64
	SourceGuardSHA                       string
	ClockDomain                          string
	ObservedAt, AvailableAt, PublishedAt time.Time
	Current                              *bool
	Status                               string
	Value                                *float64
	ActivatedAt                          *time.Time
}

type nativeBridgeBinding struct {
	ProfileContractSHA string                `json:"profile_contract_sha256"`
	Protocol           string                `json:"protocol"`
	Profile            string                `json:"profile"`
	AContractSHA       string                `json:"a_contract_sha256"`
	ACandidateSetSHA   *string               `json:"a_candidate_set_sha256"`
	ASnapshotSHA       *string               `json:"a_snapshot_sha256"`
	Basis              string                `json:"basis"`
	CandidateInputSHA  string                `json:"candidate_input_sha256"`
	SourceInputSHA     string                `json:"source_input_sha256"`
	Scope              nativeBridgeWireScope `json:"scope"`
	Policy             string                `json:"source_policy_id"`
	Version            string                `json:"source_policy_version"`
	RegistrySHA        string                `json:"source_registry_sha256"`
	RankerSHA          string                `json:"source_ranker_config_sha256"`
	Reference          string                `json:"reference_namespace"`
	ClockDomain        string                `json:"clock_domain"`
	Cutoff             nativeBridgeClock     `json:"cutoff"`
	Limit              int                   `json:"limit"`
	MaxChars           int                   `json:"max_chars"`
	BudgetTokens       int                   `json:"budget_tokens"`
	TokenEncoding      string                `json:"token_encoding"`
}

type nativeBridgeWireScope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	UserID      string `json:"user_id"`
	AgentID     string `json:"agent_id"`
}

func nativeBridgeWireScopeOf(s Scope) nativeBridgeWireScope {
	return nativeBridgeWireScope(s)
}

type nativeBridgeClock struct {
	State  string `json:"state"`
	UnixNS *int64 `json:"unix_ns"`
}

type nativeBridgeRow struct {
	ScoreKind string               `json:"ranking_score_kind"`
	Material  nativeBridgeMaterial `json:"material"`
	Position  int                  `json:"position"`
	ScoreBits string               `json:"ranking_score_bits"`
}

type nativeBridgeEmitted struct {
	HistoryExpiresAt *nativeBridgeClock   `json:"history_expires_at"`
	Material         nativeBridgeMaterial `json:"preclip_material"`
	Position         int                  `json:"position"`
	ScoreBits        string               `json:"rendered_reference_score_bits"`
	TextSHA          string               `json:"emitted_text_sha256"`
	SummarySHA       string               `json:"emitted_summary_sha256"`
	GuardSHA         string               `json:"emitted_native_guard_sha256"`
	History          bool                 `json:"history"`
}

type nativeBridgeResult struct {
	Protocol       string                 `json:"protocol"`
	Binding        *nativeBridgeBinding   `json:"binding"`
	Status         string                 `json:"status"`
	Reason         string                 `json:"reason"`
	Ranked         []nativeBridgeRow      `json:"ranked"`
	Prepack        []nativeBridgeMaterial `json:"prepack"`
	Emitted        []nativeBridgeEmitted  `json:"emitted"`
	ContextDataSHA *string                `json:"context_data_sha256"`
	TokensUsed     *int                   `json:"tokens_used"`
	TokenEncoding  *string                `json:"token_encoding"`
	Steps          int64                  `json:"local_steps"`
	ResultSHA      string                 `json:"result_sha256"`
	served         ContextBundle          // unexported: never hashed or recorded, only served in mode "on"
}

type nativeBridgeWork struct {
	ctx         context.Context
	used, limit int64
}

func (w *nativeBridgeWork) step() string {
	if w.ctx.Err() != nil {
		return "timeout"
	}
	if w.used >= w.limit {
		return "work_limit"
	}
	w.used++
	return ""
}

func nativeBridgeClockOf(t time.Time) nativeBridgeClock {
	if t.IsZero() {
		return nativeBridgeClock{State: "native_zero"}
	}
	n := t.UnixNano()
	if !time.Unix(0, n).Equal(t) {
		return nativeBridgeClock{State: "outside_int64_ns"}
	}
	return nativeBridgeClock{State: "known", UnixNS: &n}
}
func nativeBridgeHash(name string, value any) string {
	// Only concrete closed candidate structs/slices are passed; no caller schema.
	raw, e := json.Marshal(value)
	if e != nil {
		return ""
	}
	return ContentHash("sys-native-bridge-candidate-v1\x00" + name + "\x00" + string(raw))
}
func nativeBridgeMaterialOf(h Hit) nativeBridgeMaterial {
	return nativeBridgeMaterial{h.Origin, h.Collection, h.ID, h.Revision, ContentHash(h.Text), ContentHash(h.Summary), searchHitHash(h)}
}
func nativeBridgeKey(h Hit) string { return h.Origin + ":" + h.Collection + ":" + h.ID }
func nativeBridgeSHA(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func nativeBridgeBits(v float64) string {
	var b [8]byte
	n := math.Float64bits(v)
	for i := 7; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	return hex.EncodeToString(b[:])
}

// runNativeBridgeCandidate is pure numeric ranking + native packing on owned data.
// It never calls Search/Auth/Repo/fences, and never returns the private body clone.
func runNativeBridgeCandidate(ctx context.Context, in nativeBridgeInput) nativeBridgeResult {
	out := nativeBridgeResult{Protocol: "sys-native-bridge-result-v1", Status: "failed", Reason: "invalid_input"}
	work := nativeBridgeWork{ctx: ctx, limit: in.MaxSteps}
	finish := func(reason string) nativeBridgeResult {
		out.Reason = reason
		out.Steps = work.used
		if reason == "none" {
			out.Status = "selected"
		} else {
			out.Ranked = nil
			out.Prepack = nil
			out.Emitted = nil
			out.ContextDataSHA = nil
			out.TokensUsed = nil
			out.TokenEncoding = nil
		}
		out.ResultSHA = ""
		out.ResultSHA = nativeBridgeHash("Result", out)
		return out
	}
	entry, reason := nativeBridgePrepare(ctx, in)
	if reason != "" {
		return finish(reason)
	}
	base, commit, reason := nativeBridgeBase(in, &work)
	if reason != "" {
		return finish(reason)
	}
	if nativeBridgeHash("PublishedFixtureInput", in.Published) == "" {
		return finish("invalid_source")
	}
	out.Binding = &nativeBridgeBinding{ProfileContractSHA: nativeBridgeContractSHA, Protocol: "sys-native-bridge-binding-v1", Profile: nativeBridgeProfile, AContractSHA: nativeBridgeAContract, Basis: "private_declared_fixture", CandidateInputSHA: commit, SourceInputSHA: nativeBridgeHash("PublishedFixtureInput", in.Published), Scope: nativeBridgeWireScopeOf(in.Scope), Policy: string(entry.PolicyID), Version: entry.Version, RegistrySHA: in.Registry.RegistrySHA256, RankerSHA: entry.Ranker.ConfigSHA256, Reference: "arkism-native-shadow-rrf-reference-v1", ClockDomain: in.ClockDomain, Cutoff: nativeBridgeClockOf(in.Now), Limit: in.Limit, MaxChars: in.MaxChars, BudgetTokens: in.BudgetTokens, TokenEncoding: in.TokenEncoding}
	renderScores := map[string]float64{}
	for _, row := range base {
		renderScores[nativeBridgeKey(row.Hit)] = row.Score
	}
	ordered, reason := nativeBridgeRank(base, in, *entry, &work)
	if reason != "" {
		return finish(reason)
	}
	out.Ranked = make([]nativeBridgeRow, len(ordered))
	for i, h := range ordered {
		out.Ranked[i] = nativeBridgeRow{func() string {
			if entry.PolicyID == "B0" {
				return "native_rrf_reference"
			}
			return "outer_fusion"
		}(), nativeBridgeMaterialOf(h.Hit), i + 1, nativeBridgeBits(h.Score)}
	}
	ordered = ordered[:min(in.Limit, len(ordered))]
	out.Prepack = make([]nativeBridgeMaterial, len(ordered))
	bundle := ContextBundle{Authority: AuthorityDataOnly, Items: []Hit{}}
	for i, h := range ordered {
		h.Hit.Score = renderScores[nativeBridgeKey(h.Hit)]
		h.Hit.Lanes = hitLanes(h.Hit.Reasons)
		out.Prepack[i] = nativeBridgeMaterialOf(h.Hit)
		bundle.Items = append(bundle.Items, h.Hit)
	}
	boundSearchHits(&bundle, in.MaxChars, in.History)
	if in.BudgetTokens > 0 {
		if err := packContext(ctx, &bundle, in.BudgetTokens, in.TokenEncoding); err != nil {
			return finish("packing_error")
		}
	}
	out.Emitted = make([]nativeBridgeEmitted, 0, len(bundle.Items)) // history rows are appended after the items
	for i, h := range bundle.Items {
		out.Emitted = append(out.Emitted, nativeBridgeEmission(h, i+1, false, out.Prepack, nil))
	}
	for i, h := range bundle.History {
		out.Emitted = append(out.Emitted, nativeBridgeEmission(h.Hit, i+1, true, out.Prepack, h.ExpiresAt))
	}
	if in.BudgetTokens > 0 {
		hash := ContentHash(bundle.ContextData)
		out.ContextDataSHA = &hash
		out.TokensUsed = &bundle.TokensUsed
		out.TokenEncoding = &bundle.TokenEncoding
	}
	out.served = bundle
	return finish("none")
}

func nativeBridgeBudgetValid(in nativeBridgeInput) bool {
	return utf8.ValidString(in.Scope.WorkspaceID) && utf8.ValidString(in.Scope.ProjectID) && utf8.ValidString(in.Scope.UserID) && utf8.ValidString(in.Scope.AgentID) && in.Limit >= 1 && in.Limit <= 50 && in.MaxChars >= 1 && in.MaxChars <= 64000 && in.BudgetTokens >= 0 && in.BudgetTokens <= 64000 && ((in.BudgetTokens == 0 && in.TokenEncoding == "") || (in.BudgetTokens > 0 && (in.TokenEncoding == "cl100k_base" || in.TokenEncoding == "o200k_base")))
}
func nativeBridgeBase(in nativeBridgeInput, work *nativeBridgeWork) ([]ScoredHit, string, string) {
	if in.Lanes == nil || len(in.Lanes) > 4 {
		return nil, "", "invalid_input"
	}
	lanes := make([][]Hit, len(in.Lanes))
	guards := map[string]nativeBridgeMaterial{}

	projections := make([][]nativeBridgeLaneRow, len(lanes))
	for i, lane := range in.Lanes {
		var reason string
		lanes[i], projections[i], reason = nativeBridgeLane(in, lane, guards, work)
		if reason != "" {
			return nil, "", reason
		}
	}
	if len(guards) > 200 {
		return nil, "", "invalid_input"
	}
	// Full C pure reference; original live baseline and observed R are not executed here.
	base := FuseScored(lanes, len(guards))
	return base, nativeBridgeHash("CandidateLanes", projections), ""
}
func nativeBridgeEmission(h Hit, position int, history bool, pre []nativeBridgeMaterial, expires *time.Time) nativeBridgeEmitted {
	var material nativeBridgeMaterial
	for _, m := range pre {
		if m.Origin == h.Origin && m.Collection == h.Collection && m.ID == h.ID && m.Revision == h.Revision {
			material = m
		}
	}
	var clock *nativeBridgeClock
	if history {
		observed := nativeBridgeClock{State: "absent"}
		if expires != nil {
			observed = nativeBridgeClockOf(*expires)
		}
		clock = &observed
	}
	return nativeBridgeEmitted{clock, material, position, nativeBridgeBits(h.Score), ContentHash(h.Text), ContentHash(h.Summary), searchHitHash(h), history}
}

func nativeBridgeRank(base []ScoredHit, in nativeBridgeInput, entry dyn.PolicyEntry, work *nativeBridgeWork) ([]ScoredHit, string) {
	if entry.PolicyID == "B0" {
		return base, ""
	}
	if len(in.History) > 0 {
		return nil, "unsupported_context"
	}
	baseValues := make([]float64, len(base))
	for i, h := range base {
		baseValues[i] = h.Score
	}
	baseDense := nativeBridgeDense(baseValues)
	if reason := nativeBridgeApplyScores(base, baseDense, in, entry, work); reason != "" {
		return nil, reason
	}
	zeroAux := true
	for _, lane := range entry.Ranker.Lanes {
		if lane.Kind != "relevance" && lane.Weight != 0 {
			zeroAux = false
		}
	}
	if !zeroAux {
		sort.SliceStable(base, func(i, j int) bool {
			a, b := base[i], base[j]
			if a.Score != b.Score {
				return a.Score > b.Score
			}
			if a.Trust != b.Trust {
				return a.Trust > b.Trust
			}
			if !a.UpdatedAt.Equal(b.UpdatedAt) {
				return a.UpdatedAt.After(b.UpdatedAt)
			}
			return nativeBridgeKey(a.Hit) < nativeBridgeKey(b.Hit)
		})
	}
	return base, ""
}
func nativeBridgeDense(values []float64) []int64 {
	ordered := append([]float64{}, values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] > ordered[j] })
	ranks := map[float64]int64{}
	for _, value := range ordered {
		if _, ok := ranks[value]; !ok {
			ranks[value] = int64(len(ranks) + 1)
		}
	}
	result := make([]int64, len(values))
	for i, value := range values {
		result[i] = ranks[value]
	}
	return result
}
func nativeBridgeAux(h Hit, kind string, in nativeBridgeInput, entry dyn.PolicyEntry, work *nativeBridgeWork) (float64, string) {
	if kind == "recency" {
		return nativeBridgeRecency(h, in, entry)
	}
	selected, reason := nativeBridgePublished(h, kind, in, entry, work)
	if reason != "" {
		return 0, reason
	}
	if entry.PolicyID == "V3-P1-FD" {
		return nativeBridgeForgetting(selected, in, entry)
	}
	if selected.Value == nil {
		return 0, "invalid_source"
	}
	return *selected.Value, ""
}
func nativeBridgePublished(h Hit, kind string, in nativeBridgeInput, entry dyn.PolicyEntry, work *nativeBridgeWork) (*nativeBridgeValue, string) {
	missing := "missing_state"
	if kind == "importance" {
		missing = "missing_feature"
	}
	if len(in.Published) > 100000 {
		return nil, "invalid_source"
	}
	var selected *nativeBridgeValue
	lineage := ""
	ambiguous := false
	for i := range in.Published {
		if reason := work.step(); reason != "" {
			return nil, reason
		}
		v := &in.Published[i]
		if v.Target != nativeBridgeMaterialOf(h) || v.Kind != kind {
			continue
		}
		future, reason := nativeBridgeValidateSource(v, in, entry)
		if reason != "" {
			return nil, reason
		}
		if future {
			continue
		}
		if lineage != "" && lineage != v.LineageSHA {
			return nil, missing
		}
		lineage = v.LineageSHA
		if nativeBridgeNewerSource(v, selected) {
			selected = v
			ambiguous = false
		} else if v.AvailableAt.Equal(selected.AvailableAt) && v.PublishedAt.Equal(selected.PublishedAt) {
			ambiguous = true
		}
	}
	if !nativeBridgeSelectionKnown(selected, ambiguous) {
		return nil, missing
	}
	return selected, ""
}

func nativeBridgePrepare(ctx context.Context, in nativeBridgeInput) (*dyn.PolicyEntry, string) {
	if ctx == nil {
		return nil, "invalid_input"
	}
	if !nativeBridgeBudgetValid(in) || !validScope(in.Scope) || in.MaxSteps < 1 || !utf8.ValidString(in.ClockDomain) || len(in.ClockDomain) > 256 {
		return nil, "invalid_input"
	}
	if ctx.Err() != nil {
		return nil, "timeout"
	}
	if err := dyn.ValidateTyped("PolicyRegistryV2", in.Registry); err != nil {
		return nil, "invalid_registry"
	}
	var entry *dyn.PolicyEntry
	for i := range in.Registry.Entries {
		if in.Registry.Entries[i].PolicyID == in.Policy {
			entry = &in.Registry.Entries[i]
		}
	}
	if entry == nil {
		return nil, "unsupported_policy"
	}
	if entry.PolicyID != "B0" && entry.Version != "p1-zeroaux-order-v1" {
		return nil, "unsupported_version"
	}
	return entry, ""
}

type nativeBridgeLaneRow struct {
	Ordinal    int                  `json:"ordinal"`
	Material   nativeBridgeMaterial `json:"material"`
	Exact      bool                 `json:"exact_keyword"`
	ReasonsSHA string               `json:"reasons_sha256"`
}

func nativeBridgeLane(in nativeBridgeInput, lane []Hit, guards map[string]nativeBridgeMaterial, work *nativeBridgeWork) ([]Hit, []nativeBridgeLaneRow, string) {
	if lane == nil || len(lane) > in.Limit {
		return nil, nil, "invalid_input"
	}
	clones := make([]Hit, 0, len(lane))
	projections := []nativeBridgeLaneRow{}
	for j, h := range lane {
		if reason := work.step(); reason != "" {
			return nil, nil, reason
		}
		if !nativeBridgeInputHitValid(h, in.Scope) {
			return nil, nil, "input_conflict"
		}
		m := nativeBridgeMaterialOf(h)
		key := nativeBridgeKey(h)
		if prior, exists := guards[key]; exists && prior != m {
			return nil, nil, "input_conflict"
		}
		guards[key] = m
		raw, e := json.Marshal(h)
		if e != nil {
			return nil, nil, "invalid_input"
		}
		var clone Hit
		if e = json.Unmarshal(raw, &clone); e != nil {
			return nil, nil, "invalid_input"
		}
		clones = append(clones, clone)
		exact := nativeBridgeExact(h.Reasons)
		projections = append(projections, nativeBridgeLaneRow{j + 1, m, exact, ContentHash(structuredText(h.Reasons))})
	}
	return clones, projections, ""
}

func nativeBridgeExact(reasons []string) bool {
	exact := false
	for _, r := range reasons {
		exact = exact || r == "exact_keyword"
	}
	return exact
}

func nativeBridgeInputHitValid(h Hit, scope Scope) bool {
	return !(h.Scope != scope || h.Revision < 1 || h.ID == "" || len(h.ID) > 256 || !utf8.ValidString(h.ID) || h.Origin == "" || len(h.Origin) > 256 || !utf8.ValidString(h.Origin) || h.Collection == "" || len(h.Collection) > 256 || !utf8.ValidString(h.Collection) || !utf8.ValidString(h.Text) || !utf8.ValidString(h.Summary) || math.IsNaN(h.Trust) || math.IsInf(h.Trust, 0) || h.Authority != AuthorityDataOnly)
}

func nativeBridgeApplyScores(base []ScoredHit, baseDense []int64, in nativeBridgeInput, entry dyn.PolicyEntry, work *nativeBridgeWork) string {
	allValues := make([][]float64, len(entry.Ranker.Lanes))
	for laneIndex, lane := range entry.Ranker.Lanes {
		if lane.Kind == "relevance" {
			continue
		}
		values, reason := nativeBridgeAuxValues(base, lane.Kind, in, entry, work)
		if reason != "" {
			return reason
		}
		allValues[laneIndex] = values
	}
	auxDense := make([][]int64, len(allValues))
	for i, v := range allValues {
		if v != nil {
			auxDense[i] = nativeBridgeDense(v)
		}
	}
	for i := range base {
		if reason := work.step(); reason != "" {
			return reason
		}
		lanes := make([]dyn.NativeNumericLanePrototype, len(entry.Ranker.Lanes))
		for j, lane := range entry.Ranker.Lanes {
			dense := baseDense[i]
			if j > 0 {
				dense = auxDense[j][i]
			}
			lanes[j] = dyn.NativeNumericLanePrototype{Kind: lane.Kind, Weight: lane.Weight, DenseRank: dense}
		}
		score, err := dyn.NativeFusionScorePrototype(lanes)
		if err != nil {
			return "nonfinite_score"
		}
		base[i].Score = score
	}
	return ""
}

func nativeBridgeAuxValues(base []ScoredHit, kind string, in nativeBridgeInput, entry dyn.PolicyEntry, work *nativeBridgeWork) ([]float64, string) {
	values := make([]float64, len(base))
	for i, h := range base {
		if reason := work.step(); reason != "" {
			return nil, reason
		}
		value, reason := nativeBridgeAux(h.Hit, kind, in, entry, work)
		if reason != "" {
			return nil, reason
		}
		values[i] = value
	}
	return values, ""
}

func nativeBridgeRecency(h Hit, in nativeBridgeInput, entry dyn.PolicyEntry) (float64, string) {
	now, updated := nativeBridgeClockOf(in.Now), nativeBridgeClockOf(h.UpdatedAt)
	if now.UnixNS == nil || updated.UnixNS == nil || in.ClockDomain == "" {
		return 0, "source_unavailable"
	}
	if *updated.UnixNS > *now.UnixNS {
		return 0, "future_event"
	}
	// Difference must fit int64, without overflowing before validation.
	if *updated.UnixNS < 0 && *now.UnixNS > math.MaxInt64+*updated.UnixNS {
		return 0, "source_unavailable"
	}
	value, err := dyn.NativeDecayPrototype(1, *now.UnixNS-*updated.UnixNS, entry.Ranker.RecencyTauNS)
	if err != nil {
		return 0, "invalid_source"
	}
	return value, ""
}

func nativeBridgeForgetting(selected *nativeBridgeValue, in nativeBridgeInput, entry dyn.PolicyEntry) (float64, string) {
	if selected.ActivatedAt == nil || entry.Dynamics == nil {
		return 0, "missing_state"
	}
	now, activation := nativeBridgeClockOf(in.Now), nativeBridgeClockOf(*selected.ActivatedAt)
	if now.UnixNS == nil || activation.UnixNS == nil || *activation.UnixNS > *now.UnixNS || (*activation.UnixNS < 0 && *now.UnixNS > math.MaxInt64+*activation.UnixNS) {
		return 0, "invalid_source"
	}
	value, err := dyn.NativeDecayPrototype(entry.Dynamics.Initial, *now.UnixNS-*activation.UnixNS, entry.Dynamics.TauNS)
	if err != nil {
		return 0, "invalid_source"
	}
	return value, ""
}

func nativeBridgeValidateSource(v *nativeBridgeValue, in nativeBridgeInput, entry dyn.PolicyEntry) (bool, string) {
	if !nativeBridgeSourceContextValid(v, in, entry) {
		return false, "invalid_source"
	}
	if entry.Dynamics != nil && v.DynamicsSHA != entry.Dynamics.ConfigSHA256 {
		return false, "invalid_source"
	}
	observed, available, published, now := nativeBridgeClockOf(v.ObservedAt), nativeBridgeClockOf(v.AvailableAt), nativeBridgeClockOf(v.PublishedAt), nativeBridgeClockOf(in.Now)
	if observed.UnixNS == nil || available.UnixNS == nil || published.UnixNS == nil || now.UnixNS == nil || *observed.UnixNS > *available.UnixNS || *available.UnixNS > *published.UnixNS {
		return false, "invalid_source"
	}
	if v.Value != nil && (math.IsNaN(*v.Value) || math.IsInf(*v.Value, 0) || *v.Value < 0 || *v.Value > 1) {
		return false, "invalid_source"
	}
	if *published.UnixNS > *now.UnixNS {
		return true, ""
	}
	return false, ""
}

func nativeBridgeSelectionKnown(selected *nativeBridgeValue, ambiguous bool) bool {
	return !(selected == nil || ambiguous || selected.Status != "present" || selected.Current == nil || !*selected.Current)
}

func nativeBridgeNewerSource(v, selected *nativeBridgeValue) bool {
	return selected == nil || v.AvailableAt.After(selected.AvailableAt) || (v.AvailableAt.Equal(selected.AvailableAt) && v.PublishedAt.After(selected.PublishedAt))
}

func nativeBridgeSourceContextValid(v *nativeBridgeValue, in nativeBridgeInput, entry dyn.PolicyEntry) bool {
	return !(v.Scope != in.Scope || v.SourceScope != in.Scope || v.ConfigSHA != entry.Ranker.ConfigSHA256 || !nativeBridgeSHA(v.LineageSHA) || v.SourceID == "" || v.SourceRevision < 1 || !nativeBridgeSHA(v.SourceGuardSHA) || v.ClockDomain != in.ClockDomain || v.ClockDomain == "")
}
