package memory

// Files and text become memory CANDIDATES, never memories: the configured
// wiki model reads privacy-filtered text and proposes claims, and every claim
// goes through Capture (dedupe, secret and instruction checks) to the review
// queue a person decides. A dry run shows the claims and stores nothing.
//
// A file the request budget cannot cover runs as a job in the "jobs"
// collection (kind file_memory_extract); its chunks carry their own source
// references, so a job resumes from the next chunk after a budget stop.
//
// A document uploaded again (same scope, uploader and file name, or the same
// knowledge source) is compared chunk by chunk with its last upload: unchanged chunks
// are not sent to the model again, and chunks that disappeared are only
// reported with their candidate ids, for a person to retire if they wish.

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	fileExtractJobKind         = "file_memory_extract"
	fileExtractDocKind         = "file_memory_extract_doc"
	fileExtractPromptVersion   = "file-extract-v2"
	fileExtractPipelineVersion = "arkism-file-extract-v1"
	// The wiki_ prefix puts these calls under the wiki model's daily call cap.
	fileExtractOperation   = "wiki_memory_extract"
	fileExtractChunkRunes  = 6000
	fileExtractMaxChunks   = 24
	fileExtractSyncChunks  = 2
	fileExtractMaxClaims   = 32 // Capture takes at most 32 claims per event
	fileExtractMaxText     = 512 << 10
	fileExtractMaxPreview  = 64 << 10
	fileExtractSourceBytes = 60000 // under the 64000-byte source text limit
	fileExtractJobTokens   = 160000
	fileExtractJobLease    = 10 * time.Minute
	fileExtractQuoteRunes  = 110
	// extractChunksModeRunes keeps a chunks-mode piece within one candidate's 4000 bytes.
	extractChunksModeRunes = 1000
	extractChunksVersion   = "chunks-v1"
	extractContextMax      = 2000
	extractLabelsMax       = 20 // tags, metadata and entities of one item together
)

var (
	extractSemanticKinds = []string{"fact", "preference", "correction", "lesson", "failure_recovery"}
	extractEvidenceKinds = []string{"user_statement", "tool_observation", "agent_statement", "inference"}
)

// ExtractedClaim is one model-proposed claim; it is a proposal, never a memory.
type ExtractedClaim struct {
	Content       string  `json:"content"`
	Kind          string  `json:"kind"`
	SemanticKind  string  `json:"semantic_kind"`
	EvidenceKind  string  `json:"evidence_kind"`
	OccurredFrom  string  `json:"occurred_from,omitempty"`
	OccurredTo    string  `json:"occurred_to,omitempty"`
	EvidenceQuote string  `json:"evidence_quote"`
	Confidence    float64 `json:"confidence"`
	Locator       string  `json:"locator,omitempty"`
	// FactType maps the kind onto world and experience facts: episodic is
	// what the narrator or agent did or went through, every other kind is world.
	FactType string `json:"fact_type,omitempty"`
}

// ExtractedCause is a model-proposed "claim Cause led to claim Effect" between claims of one
// answer (indices into Claims). Stored, it is a causal link candidate a person reviews.
type ExtractedCause struct {
	Cause  int    `json:"cause"`
	Effect int    `json:"effect"`
	Reason string `json:"reason"`
}

// ExtractOptions are one item's own fields: they shape its prompt and label its candidates.
type ExtractOptions struct {
	Context   string            `json:"context,omitempty" bson:"context,omitempty"`
	Timestamp *time.Time        `json:"timestamp,omitempty" bson:"timestamp,omitempty"`
	Tags      []string          `json:"tags,omitempty" bson:"tags,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty" bson:"metadata,omitempty"`
	Entities  []string          `json:"entities,omitempty" bson:"entities,omitempty"`
	Strategy  string            `json:"strategy,omitempty" bson:"strategy,omitempty"`
}

// ExtractPromptView is the prompt extraction would send, rendered without a model call.
type ExtractPromptView struct {
	System        string          `json:"system"`
	User          string          `json:"user"`
	Settings      ExtractStrategy `json:"settings"`
	Strategy      string          `json:"strategy,omitempty"`
	Strategies    []string        `json:"strategies"`
	Timezone      string          `json:"timezone"`
	SkippedReason string          `json:"skipped_reason,omitempty"`
}

type ExtractSkip struct {
	Locator string `json:"locator,omitempty"`
	Reason  string `json:"reason"`
}

// ExtractPreviewInput is a dry run: strategy and overrides try settings without
// changing the space, and prompt_only renders the prompt with no model call.
type ExtractPreviewInput struct {
	Scope       Scope            `json:"scope"`
	Text        string           `json:"text"`
	SourceLabel string           `json:"source_label,omitempty"`
	Context     string           `json:"context,omitempty"`
	Timestamp   *time.Time       `json:"timestamp,omitempty"`
	Strategy    string           `json:"strategy,omitempty"`
	Overrides   *ExtractStrategy `json:"overrides,omitempty"`
	PromptOnly  bool             `json:"prompt_only,omitempty"`
}

type ExtractPreviewResult struct {
	Claims  []ExtractedClaim   `json:"claims"`
	Causes  []ExtractedCause   `json:"causes,omitempty"`
	Skipped []ExtractSkip      `json:"skipped"`
	Prompt  *ExtractPromptView `json:"prompt,omitempty"`
}

// FileMemoriesInput is one uploaded file; timestamp is the reference time its
// relative dates are read against (the upload time when unset).
type FileMemoriesInput struct {
	Scope         Scope      `json:"scope"`
	Filename      string     `json:"filename"`
	ContentBase64 string     `json:"content_base64"`
	MIME          string     `json:"mime,omitempty"`
	DryRun        bool       `json:"dry_run"`
	Context       string     `json:"context,omitempty"`
	Timestamp     *time.Time `json:"timestamp,omitempty"`
	Tags          []string   `json:"tags,omitempty"`
	Strategy      string     `json:"strategy,omitempty"`
	// Parsers names the readers to try, in order; empty uses the server's order.
	Parsers []string `json:"parsers,omitempty"`
	// Blocks is text with images in place, sent instead of content_base64.
	Blocks []FileBlock `json:"blocks,omitempty"`
}

// IngestInput is text to extract memory candidates from (POST /v1/memories/ingest).
// Async queues every item as a job and answers at once (poll the operations routes).
type IngestInput struct {
	Scope Scope        `json:"scope"`
	Items []IngestItem `json:"items"`
	Async bool         `json:"async,omitempty"`
}

// IngestItem is one text. Items with the same document_id are one document: sent
// again, only changed chunks are extracted (update_mode replace, the default) or the
// text is added after the document's earlier chunks (append). Without a document_id
// the text itself names the document, so the same text sent twice is extracted once.
type IngestItem struct {
	Content    string            `json:"content"`
	Context    string            `json:"context,omitempty"`
	Timestamp  *time.Time        `json:"timestamp,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Entities   []string          `json:"entities,omitempty"`
	DocumentID string            `json:"document_id,omitempty"`
	UpdateMode string            `json:"update_mode,omitempty"`
	Strategy   string            `json:"strategy,omitempty"`
	// Blocks is the content as ordered text and image blocks, instead of content.
	Blocks []IngestBlock `json:"blocks,omitempty"`
}

// IngestBlock is one piece of an item's content in order: text, or an image (png, jpeg
// or webp) the server model reads into text. Image blocks need the vision opt-in
// (MEMORY_FILE_VISION); without it the item fails as an unsupported format.
type IngestBlock struct {
	Type          string `json:"type"`
	Text          string `json:"text,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	MIME          string `json:"mime,omitempty"`
}

// IngestResult has one result per item, in order; a failed item has status failed and error.
type IngestResult struct {
	Items []FileMemoriesResult `json:"items"`
}

type KnowledgeExtractInput struct {
	Scope  Scope `json:"scope"`
	DryRun bool  `json:"dry_run"`
}

type FileMemoriesResult struct {
	SourceID          string            `json:"source_id,omitempty"`
	SourceIDs         []string          `json:"source_ids,omitempty"`
	JobID             string            `json:"job_id,omitempty"`
	DocumentID        string            `json:"document_id,omitempty"`
	Status            string            `json:"status"`
	CandidatesCreated int               `json:"candidates_created"`
	Claims            []ExtractedClaim  `json:"claims,omitempty"`
	Causes            []ExtractedCause  `json:"causes,omitempty"`
	Skipped           []ExtractSkip     `json:"skipped"`
	Delta             *FileExtractDelta `json:"delta,omitempty"`
	// Error is a failed ingest item's error code (POST /v1/memories/ingest).
	Error string `json:"error,omitempty"`
}

// FileExtractDelta compares an upload with the last upload of the same document.
type FileExtractDelta struct {
	ChunksTotal     int                  `json:"chunks_total" bson:"chunks_total"`
	ChunksUnchanged int                  `json:"chunks_unchanged" bson:"chunks_unchanged"`
	ChunksChanged   int                  `json:"chunks_changed" bson:"chunks_changed"`
	ChunksRemoved   int                  `json:"chunks_removed" bson:"chunks_removed"`
	RemovedSources  []FileExtractRemoved `json:"removed_sources" bson:"removed_sources"`
}

// FileExtractRemoved is a chunk of the last upload that is gone now; its
// candidates (an approved candidate keeps its id as a memory) stay as they are.
type FileExtractRemoved struct {
	Locator      string   `json:"locator" bson:"locator"`
	Source       Ref      `json:"source" bson:"source"`
	CandidateIDs []string `json:"candidate_ids" bson:"candidate_ids"`
}

// FileExtractChunk is one model request's worth of text and where it came from.
type FileExtractChunk struct {
	Locator    string   `json:"locator" bson:"locator"`
	Source     Ref      `json:"source" bson:"source"`
	Text       string   `json:"-" bson:"text,omitempty"`
	Hash       string   `json:"-" bson:"hash,omitempty"`
	Candidates []string `json:"-" bson:"candidates,omitempty"`
	// Round names a reprocess run, so its capture events are new rather than retries.
	Round string `json:"-" bson:"round,omitempty"`
}

type FileExtractJob struct {
	Label             string             `json:"label" bson:"label"`
	Sources           []string           `json:"sources" bson:"sources"`
	Chunks            []FileExtractChunk `json:"-" bson:"chunks"`
	Kept              []FileExtractChunk `json:"-" bson:"kept,omitempty"` // unchanged chunks carried from the last upload
	Document          string             `json:"document,omitempty" bson:"document,omitempty"`
	Previous          string             `json:"previous,omitempty" bson:"previous,omitempty"` // the document's job before this upload
	Delta             FileExtractDelta   `json:"delta" bson:"delta"`
	Total             int                `json:"total" bson:"total"`
	Next              int                `json:"next" bson:"next"`
	CandidatesCreated int                `json:"candidates_created" bson:"candidates_created"`
	Skipped           []ExtractSkip      `json:"skipped" bson:"skipped"`
	Calls             int                `json:"calls" bson:"calls"`
	Tokens            int64              `json:"tokens" bson:"tokens"`
	Error             string             `json:"error,omitempty" bson:"error,omitempty"`
	RequestedBy       string             `json:"requested_by" bson:"requested_by"`
	// Settings, Options and Zone are fixed when the document is queued: the resolved
	// extraction settings, the item's own fields and the zone its dates are read in.
	Settings ExtractStrategy `json:"settings" bson:"settings"`
	Options  ExtractOptions  `json:"options" bson:"options"`
	Zone     string          `json:"zone,omitempty" bson:"zone,omitempty"`
	// Batch is the provider batch the remaining chunks were sent as (file_extract_batch.go).
	Batch *ExtractBatch `json:"batch,omitempty" bson:"batch,omitempty"`
	// Ref is the document reference (file:<name>, knowledge:<source id>, doc:<id> or
	// text:<hash>); Tags are the document's reviewer tags when the upload was made, given
	// to its new candidates.
	Ref  string   `json:"-" bson:"ref,omitempty"`
	Tags []string `json:"tags,omitempty" bson:"tags,omitempty"`
}

// zone is the job's time zone; the server's for a job queued before zones were stored.
func (j *FileExtractJob) zone() *time.Location {
	if z, err := time.LoadLocation(j.Zone); j.Zone != "" && err == nil {
		return z
	}
	return usageZone()
}

// refTime is when the item's text was written: its timestamp, else now.
func (j *FileExtractJob) refTime() time.Time {
	if j.Options.Timestamp != nil {
		return j.Options.Timestamp.In(j.zone())
	}
	return time.Now().In(j.zone())
}

type FileExtractJobView struct {
	ID        string         `json:"id"`
	Revision  int64          `json:"revision"`
	Status    string         `json:"status"`
	Job       FileExtractJob `json:"job"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// extractPrompt is the system and user message for one chunk under plan.
func extractPrompt(text string, plan *FileExtractJob) (string, string) {
	system := "You extract durable memory claims from one document for a person to review; nothing you return is saved without that review. " + wikiDataRule +
		" Extract preferences, prohibitions (things never to do), decisions, lessons and facts that stay useful later; skip small talk and one-off details." +
		" Write content and evidence_quote in the same language as the source and never translate: Korean stays Korean." +
		` Return only one JSON object: {"claims":[{"content": string, "kind": "user"|"decision"|"procedural"|"lesson"|"episodic"|"semantic", "semantic_kind": "fact"|"preference"|"correction"|"lesson"|"failure_recovery", "evidence_kind": "user_statement"|"tool_observation"|"agent_statement"|"inference", "occurred_from": string, "occurred_to": string, "evidence_quote": string, "confidence": number}]}.` +
		" evidence_quote is an exact excerpt of the source, at most 300 characters. occurred_from and occurred_to are when the event the claim describes happened, as the source states it (YYYY-MM-DD or YYYY-MM; the same value twice for one day), or empty strings when the source gives no date; read relative dates such as yesterday against the reference time. confidence is between 0 and 1." +
		" kind episodic is something the narrator or agent did or went through (an experience); facts about the world, people and projects take the other kinds." +
		" At most 32 claims; answer {\"claims\":[]} when nothing qualifies." + extractCausalRule(plan.Settings) + spaceExtractionGuidance(plan.Settings)
	user := "Reference time: " + plan.refTime().Format(time.RFC3339) + " (" + plan.zone().String() + ")\n"
	if plan.Options.Context != "" {
		user += wikiFence("SOURCE_CONTEXT", plan.Options.Context) + "\n"
	}
	return system, user + wikiFence("SOURCE_DATA", text)
}

// extractCausalRule asks for causes between the answer's own claims when the settings turn that on.
func extractCausalRule(st ExtractStrategy) string {
	if !st.causal() {
		return ""
	}
	return ` Also return "causes": [{"cause": number, "effect": number, "reason": string}] next to "claims": cause and effect are 0-based positions in your claims list, only where the source states that what one claim describes led to the other; reason is one short sentence. Omit causes or return [] when none does.`
}

func extractSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

func validExtractedClaim(c ExtractedClaim, source string) bool {
	content := strings.TrimSpace(c.Content)
	if content == "" || len(content) > 4000 || !utf8.ValidString(content) || strings.ContainsRune(content, 0) || c.Locator != "" ||
		!slices.Contains(captureKinds, c.Kind) || !slices.Contains(extractSemanticKinds, c.SemanticKind) || !slices.Contains(extractEvidenceKinds, c.EvidenceKind) ||
		math.IsNaN(c.Confidence) || c.Confidence < 0 || c.Confidence > 1 || len(c.OccurredFrom) > 64 || len(c.OccurredTo) > 64 {
		return false
	}
	quote := extractSpaces(c.EvidenceQuote)
	// A quote the source does not contain is the model's own wording, not evidence.
	if quote == "" || len(c.EvidenceQuote) > 1200 || !strings.Contains(extractSpaces(source), quote) {
		return false
	}
	return true
}

// parseExtractedClaims keeps the well-formed claims of a strict JSON answer and
// reports every malformed one; false means the answer itself was not JSON. Dates
// are checked against ref, the time the source was written.
func parseExtractedClaims(answer, source, locator string, ref time.Time) (ExtractPreviewResult, bool) {
	var raw struct {
		Claims []json.RawMessage `json:"claims"`
		Causes []ExtractedCause  `json:"causes"`
	}
	if !decodeModelJSON(answer, &raw) {
		return ExtractPreviewResult{}, false
	}
	claims, skipped := []ExtractedClaim{}, []ExtractSkip{}
	kept := map[int]int{} // answer position -> position in claims
	for i, item := range raw.Claims {
		if i >= fileExtractMaxClaims {
			skipped = append(skipped, ExtractSkip{Locator: locator, Reason: "too_many_claims"})
			break
		}
		var c ExtractedClaim
		if !decodeModelJSON(string(item), &c) || !validExtractedClaim(c, source) {
			skipped = append(skipped, ExtractSkip{Locator: locator, Reason: "malformed_claim"})
			continue
		}
		c.Content, c.Locator, c.FactType = strings.TrimSpace(c.Content), locator, extractFactType(c.Kind)
		if (c.OccurredFrom != "" || c.OccurredTo != "") && resolveOccurred(c.OccurredFrom, c.OccurredTo, "", ref) == nil {
			c.OccurredFrom, c.OccurredTo = "", "" // an impossible or far-future date is not shown or stored
		}
		kept[i] = len(claims)
		claims = append(claims, c)
	}
	return ExtractPreviewResult{Claims: claims, Causes: keptCauses(raw.Causes, kept, locator, &skipped), Skipped: skipped}, true
}

func extractFactType(kind string) string {
	if kind == "episodic" {
		return "experience"
	}
	return "world"
}

// keptCauses renumbers the causes between kept claims; one naming a dropped claim,
// itself, or with a bad reason is skipped.
func keptCauses(raw []ExtractedCause, kept map[int]int, locator string, skipped *[]ExtractSkip) []ExtractedCause {
	out := []ExtractedCause{}
	for i, c := range raw {
		cause, okC := kept[c.Cause]
		effect, okE := kept[c.Effect]
		c.Reason = strings.TrimSpace(c.Reason)
		if i >= causalDraftsMax || !okC || !okE || cause == effect || !validCausalReason(c.Reason) {
			*skipped = append(*skipped, ExtractSkip{Locator: locator, Reason: "malformed_cause"})
			continue
		}
		out = append(out, ExtractedCause{Cause: cause, Effect: effect, Reason: c.Reason})
	}
	return out
}

// extractClean masks <private> spans and secret-looking text before any model
// call or storage; the count says how many redactions were made.
func extractClean(text string) (string, int) {
	n := 0
	masked := redactSessionPrivate(text)
	if masked != text {
		n++
	}
	return redactCode(masked, &n), n
}

// extractChunkRunes is the chunk size of st; a chunks-mode piece must fit one candidate.
func extractChunkRunes(st ExtractStrategy) int {
	size := cmp.Or(st.ChunkSize, fileExtractChunkRunes)
	if st.Mode == spaceExtractChunks {
		size = min(size, extractChunksModeRunes)
	}
	return size
}

// extractChunks packs each page into chunks of size runes; paged text gets page locators.
func extractChunks(pages []string, paged bool, size int) []FileExtractChunk {
	out := []FileExtractChunk{}
	for p, page := range pages {
		for c, piece := range knowledgePackParagraphs(knowledgeParagraphs(strings.Split(page, "\n"), false), size) {
			locator := fmt.Sprintf("chunk:%d", c+1)
			if paged {
				locator = fmt.Sprintf("page:%d/chunk:%d", p+1, c+1)
			}
			out = append(out, FileExtractChunk{Locator: locator, Text: piece, Hash: ContentHash(extractSpaces(piece))})
		}
	}
	return out
}

// extractActor is a person with the scope's permission, returned with the space's
// settings. An agent (agentOK: uploads, text ingest and their job runs) qualifies only
// where the space's MCP tool list names add_document: extraction spends the model
// budget, so an empty list (every tool) does not let agents in. Whether a model is
// needed depends on the mode (extractPlan).
func (s *Service) extractActor(ctx context.Context, token, action string, scope Scope, agentOK bool) (Decision, SpaceConfig, error) {
	roles := []string{roleHuman}
	if agentOK {
		roles = append(roles, roleAgent)
	}
	d, err := s.wikiAuthorize(ctx, token, action, scope, "", "", roles...)
	if err != nil {
		return d, SpaceConfig{}, err
	}
	settings, err := s.spaceSettings(ctx, scope)
	if err == nil && d.Role == roleAgent && (!slices.Contains(settings.MCPTools, agentUploadTool) || !s.mcpToolAllowed(nil, agentUploadTool)) {
		return Decision{}, SpaceConfig{}, ErrDenied
	}
	if err == nil && !settings.ExtractionEnabled {
		err = ErrExtractionDisabled
	}
	return d, settings, err
}

// extractPlan is how one item is extracted: the space's settings under the item's strategy
// and a dry run's overrides, the zone its dates are read in, and the item's own fields.
func (s *Service) extractPlan(settings SpaceConfig, opts ExtractOptions, override *ExtractStrategy) (FileExtractJob, error) {
	st, err := settings.extractStrategy(opts.Strategy, override)
	if err != nil {
		return FileExtractJob{}, err
	}
	if st.Mode != spaceExtractChunks && s.Wiki == nil {
		return FileExtractJob{}, ErrModelNotConfigured
	}
	return FileExtractJob{Settings: st, Options: opts, Zone: settings.zone().String()}, nil
}

// agentUploadTool is the MCP tool a space must list to let agents upload files for extraction.
const agentUploadTool = "add_document"

// extractTransfer checks that the chunk may go to the model for the person running the extraction.
func (s *Service) extractTransfer(ctx context.Context, token string, d Decision, scope Scope, ch FileExtractChunk) error {
	t, err := s.authorizeAt(ctx, token, "generate:memory_candidates", scope, "", ch.Text, wikiDestination)
	if err != nil {
		return err
	}
	if t.PrincipalID != d.PrincipalID || !t.ExternalTransfer || !t.ContentSafe {
		return ErrDenied
	}
	return nil
}

// extractChunk sends one privacy-filtered chunk to the model under the budget gate;
// in chunks mode the chunk itself is the claim and no model is called.
func (s *Service) extractChunk(ctx context.Context, token string, d Decision, scope Scope, budget *WikiJob, plan *FileExtractJob, ch FileExtractChunk) (ExtractPreviewResult, error) {
	if plan.Settings.Mode == spaceExtractChunks {
		return chunkClaims(ch), nil
	}
	if s.Wiki == nil {
		return ExtractPreviewResult{}, ErrModelNotConfigured
	}
	if err := s.extractTransfer(ctx, token, d, scope, ch); err != nil {
		return ExtractPreviewResult{}, err
	}
	system, user := extractPrompt(ch.Text, plan)
	answer, err := s.wikiCall(ctx, scope, budget, fileExtractOperation, system, user)
	if errors.Is(err, errWikiBudget) {
		return ExtractPreviewResult{}, ErrModelBudget
	}
	if err != nil {
		return ExtractPreviewResult{}, err
	}
	return readExtractAnswer(answer, plan, ch), nil
}

// readExtractAnswer reads one chunk's model answer; causes are kept only where the plan asks for them.
func readExtractAnswer(answer string, plan *FileExtractJob, ch FileExtractChunk) ExtractPreviewResult {
	res, ok := parseExtractedClaims(answer, ch.Text, ch.Locator, plan.refTime())
	if !ok {
		return ExtractPreviewResult{Skipped: []ExtractSkip{{Locator: ch.Locator, Reason: "malformed_answer"}}}
	}
	if !plan.Settings.causal() {
		res.Causes = nil
	}
	return res
}

// chunkClaims is a chunks-mode chunk as one claim, in the source's own words.
func chunkClaims(ch FileExtractChunk) ExtractPreviewResult {
	text := strings.TrimSpace(ch.Text)
	return ExtractPreviewResult{Claims: []ExtractedClaim{{Content: text, Kind: "semantic", SemanticKind: "fact", EvidenceKind: "inference", FactType: "world",
		EvidenceQuote: runeCutString(extractSpaces(text), fileExtractQuoteRunes), Confidence: 1, Locator: ch.Locator}}, Skipped: []ExtractSkip{}}
}

// previewChunks runs the first chunks a request may pay for and stores nothing.
func (s *Service) previewChunks(ctx context.Context, token string, d Decision, scope Scope, plan *FileExtractJob, chunks []FileExtractChunk, out *FileMemoriesResult) error {
	budget := &WikiJob{MaxTotalTokens: fileExtractJobTokens}
	for i, ch := range chunks {
		if i >= fileExtractSyncChunks {
			out.Skipped = append(out.Skipped, ExtractSkip{Locator: ch.Locator, Reason: "dry_run_limit"})
			continue
		}
		res, err := s.extractChunk(ctx, token, d, scope, budget, plan, ch)
		if err != nil {
			return err
		}
		for _, c := range res.Causes { // positions in the whole result, not the chunk
			out.Causes = append(out.Causes, ExtractedCause{Cause: c.Cause + len(out.Claims), Effect: c.Effect + len(out.Claims), Reason: c.Reason})
		}
		out.Claims, out.Skipped = append(out.Claims, res.Claims...), append(out.Skipped, res.Skipped...)
	}
	return nil
}

// ExtractPreview proposes claims from pasted text under the space's settings, a
// named strategy and overrides (a dry run of settings not yet saved). Nothing is
// stored; only the model call is recorded in the usage ledger. With prompt_only it
// renders the prompt for the first chunk (or a placeholder) and calls no model.
func (s *Service) ExtractPreview(ctx context.Context, token string, in ExtractPreviewInput) (ExtractPreviewResult, error) {
	opts := ExtractOptions{Context: in.Context, Timestamp: in.Timestamp, Strategy: in.Strategy}
	if !validScope(in.Scope) || !in.PromptOnly && strings.TrimSpace(in.Text) == "" || len(in.Text) > fileExtractMaxPreview || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) ||
		in.SourceLabel != "" && (!validRunLabel(in.SourceLabel, 200) || containsSecret(in.SourceLabel)) || !validExtractOptions(opts, time.Now()) {
		return ExtractPreviewResult{}, ErrInvalid
	}
	d, settings, err := s.extractActor(ctx, token, "generate:memory_candidates", in.Scope, false)
	if err != nil {
		return ExtractPreviewResult{}, err
	}
	st, err := settings.extractStrategy(in.Strategy, in.Overrides)
	if err != nil {
		return ExtractPreviewResult{}, err
	}
	plan := &FileExtractJob{Settings: st, Options: opts, Zone: settings.zone().String()}
	if err = s.defendItem(ctx, in.Scope, "generate:memory_candidates", &plan.Options, &in.Text); err != nil {
		return ExtractPreviewResult{}, err
	}
	text, redacted := extractClean(in.Text)
	chunks := extractChunks([]string{text}, false, extractChunkRunes(st))
	if in.PromptOnly {
		return ExtractPreviewResult{Claims: []ExtractedClaim{}, Skipped: extractRedactionNote(redacted), Prompt: extractPromptView(settings, in.Strategy, plan, chunks)}, nil
	}
	if st.Mode != spaceExtractChunks && s.Wiki == nil {
		return ExtractPreviewResult{}, ErrModelNotConfigured
	}
	out := FileMemoriesResult{Skipped: extractRedactionNote(redacted)}
	if err = s.previewChunks(ctx, token, d, in.Scope, plan, chunks, &out); err != nil {
		return ExtractPreviewResult{}, err
	}
	return ExtractPreviewResult{Claims: append([]ExtractedClaim{}, out.Claims...), Causes: out.Causes, Skipped: out.Skipped}, nil
}

// defendItem runs the space's defense rules over an item's texts and context, then
// the privacy filter over the context (chunking filters the texts).
func (s *Service) defendItem(ctx context.Context, scope Scope, action string, opts *ExtractOptions, texts ...*string) error {
	if err := s.defendSpace(ctx, "", defenseGuard{action: action}, scope, append(texts, &opts.Context)...); err != nil {
		return err
	}
	opts.Context, _ = extractClean(opts.Context)
	return nil
}

// extractPromptView renders what extraction under plan would send, with no model call.
func extractPromptView(settings SpaceConfig, strategy string, plan *FileExtractJob, chunks []FileExtractChunk) *ExtractPromptView {
	text := "(the item's text)"
	if len(chunks) > 0 {
		text = chunks[0].Text
	}
	system, user := extractPrompt(text, plan)
	out := &ExtractPromptView{System: system, User: user, Settings: plan.Settings, Strategy: cmp.Or(strategy, settings.ExtractionDefaultStrategy),
		Strategies: slices.Sorted(maps.Keys(settings.ExtractionStrategies)), Timezone: plan.Zone}
	if plan.Settings.Mode == spaceExtractChunks {
		out.SkippedReason = "chunks_mode_calls_no_model"
	}
	return out
}

// validExtractOptions checks an item's own fields; context and text are cleaned later.
func validExtractOptions(o ExtractOptions, now time.Time) bool {
	return utf8.ValidString(o.Context) && utf8.RuneCountInString(o.Context) <= extractContextMax && !strings.ContainsRune(o.Context, 0) &&
		(o.Timestamp == nil || storableTime(*o.Timestamp) && !o.Timestamp.After(now)) &&
		(o.Strategy == "" || spaceStrategyName.MatchString(o.Strategy)) && validClaimLabels(o.Tags, o.Metadata, o.Entities)
}

func extractRedactionNote(n int) []ExtractSkip {
	if n == 0 {
		return []ExtractSkip{}
	}
	return []ExtractSkip{{Reason: fmt.Sprintf("privacy_redacted:%d", n)}}
}

// fileUpload decodes an uploaded file within the file size limit.
// decodeUpload decodes a base64 file no larger than the per-file limit.
func decodeUpload(content string, limit int64) ([]byte, error) {
	if int64(len(content)) > int64(base64.StdEncoding.EncodedLen(int(limit))) {
		return nil, errBlobTooLarge
	}
	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil || len(raw) == 0 {
		return nil, ErrInvalid
	}
	if int64(len(raw)) > limit {
		return nil, errBlobTooLarge
	}
	return raw, nil
}

// uploadFile checks one JSON upload's name, type and size.
func (s *Service) uploadFile(in FileMemoriesInput) (uploadedFile, error) {
	mime, err := s.uploadMIME(in.Filename, in.MIME)
	if err != nil {
		return uploadedFile{}, err
	}
	f := uploadedFile{Scope: in.Scope, Name: in.Filename, MIME: mime, Parsers: in.Parsers, DryRun: in.DryRun}
	if len(in.Blocks) > 0 {
		if in.ContentBase64 != "" {
			return uploadedFile{}, ErrInvalid
		}
		f.Blocks, err = decodeBlocks(in.Blocks, s.Files.maxBytes())
		return f, err
	}
	f.Raw, err = decodeUpload(in.ContentBase64, s.Files.maxBytes())
	return f, err
}

func pagePointers(pages []string) []*string {
	out := make([]*string, len(pages))
	for i := range pages {
		out[i] = &pages[i]
	}
	return out
}

// extractFileChunks applies the privacy filter page by page and enforces size limits;
// smaller chunks allow more of them, so the text a document may hold stays the same.
func extractFileChunks(pages []string, paged bool, size int) ([]FileExtractChunk, int, error) {
	redacted, total := 0, 0
	for i := range pages {
		var n int
		pages[i], n = extractClean(pages[i])
		redacted += n
		total += len(pages[i])
	}
	chunks := extractChunks(pages, paged, size)
	if total > fileExtractMaxText || len(chunks) > fileExtractMaxChunks*fileExtractChunkRunes/size {
		return nil, 0, errBlobTooLarge
	}
	if len(chunks) == 0 {
		return nil, 0, ErrInvalid
	}
	return chunks, redacted, nil
}

// ExtractFileMemories turns one uploaded file into review candidates.
func (s *Service) ExtractFileMemories(ctx context.Context, token string, in FileMemoriesInput) (FileMemoriesResult, error) {
	if !validScope(in.Scope) || !validUploadName(in.Filename) {
		return FileMemoriesResult{}, ErrInvalid
	}
	opts := ExtractOptions{Context: in.Context, Timestamp: in.Timestamp, Tags: in.Tags, Strategy: in.Strategy}
	if !validExtractOptions(opts, time.Now()) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, settings, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope, true)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	plan, err := s.extractPlan(settings, opts, nil)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	f, err := s.uploadFile(in)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	return s.extractUploaded(ctx, token, d, plan, f)
}

// validUploadName is a plain file name: no folders, no control characters, no secret.
func validUploadName(name string) bool {
	return validRunLabel(name, 200) && !strings.ContainsAny(name, "/\\") && !containsSecret(name)
}

// extractUploaded reads one checked upload and proposes candidates from it.
func (s *Service) extractUploaded(ctx context.Context, token string, d Decision, plan FileExtractJob, f uploadedFile) (FileMemoriesResult, error) {
	pages, paged, notes, err := s.fileMemoryPages(ctx, token, d, f)
	if err == nil {
		err = s.defendItem(ctx, f.Scope, "write:memory_candidates", &plan.Options, pagePointers(pages)...)
	}
	if err != nil {
		return FileMemoriesResult{}, err
	}
	if f.DryRun && len(pages) == 0 {
		return FileMemoriesResult{Status: JobStatusDone, Skipped: notes}, nil
	}
	chunks, redacted, err := extractFileChunks(pages, paged, extractChunkRunes(plan.Settings))
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out := FileMemoriesResult{Skipped: append(notes, extractRedactionNote(redacted)...)}
	if f.DryRun {
		out.Status = JobStatusDone
		return out, s.previewChunks(ctx, token, d, f.Scope, &plan, chunks, &out)
	}
	plan.Label = f.Name
	out, err = s.extractStored(ctx, token, d, f.Scope, plan, "file:"+f.Name, "", chunks, out, fileExtractSyncChunks, false)
	if err == nil {
		s.keepOriginal(ctx, d, f, &out, redacted)
	}
	return out, err
}

// ingestMaxItems bounds one ingest request; every item may become its own job.
const (
	ingestMaxItems  = 16
	ingestMaxBlocks = 64
)

// ingestRequestBytes bounds one ingest request: its texts and image blocks as JSON.
const ingestRequestBytes = 6 << 20

// Ingest extracts memory candidates from text items, each its own document and job.
// The whole request (shapes, strategies, model) is checked first, so a malformed item
// stores nothing. After that each item stands alone: its result says done, pending or
// failed (with the error code), and a failed item never undoes the items before it. The
// request fails only when every item failed before anything was stored. Async queues
// every item and answers at once (the HTTP route starts the queued jobs).
func (s *Service) Ingest(ctx context.Context, token string, in IngestInput) (IngestResult, error) {
	if !validScope(in.Scope) || len(in.Items) == 0 || len(in.Items) > ingestMaxItems || slices.ContainsFunc(in.Items, func(it IngestItem) bool { return !validIngestItem(it) }) ||
		ingestImages(in.Items) > s.Files.visionPages() {
		return IngestResult{}, ErrInvalid
	}
	d, settings, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope, true)
	if err != nil {
		return IngestResult{}, err
	}
	for _, it := range in.Items { // an unknown strategy or a missing model refuses the whole request
		if _, err = s.extractPlan(settings, ingestOptions(it), nil); err != nil {
			return IngestResult{}, err
		}
	}
	sync := fileExtractSyncChunks
	if in.Async {
		sync = 0
	}
	out, firstErr, stored, failed := IngestResult{Items: make([]FileMemoriesResult, len(in.Items))}, error(nil), false, 0
	for i, it := range in.Items {
		res, err := s.ingestItem(ctx, token, d, in.Scope, settings, it, sync)
		if err != nil {
			code, _ := errorResponse(err)
			res.Status, res.Error, firstErr, failed = JobStatusFailed, code, cmp.Or(firstErr, err), failed+1
		}
		stored = stored || res.JobID != ""
		out.Items[i] = res
	}
	if failed == len(in.Items) && !stored {
		return IngestResult{}, firstErr
	}
	return out, nil
}

// ingestImages counts the image blocks of a request; each is one vision model call.
func ingestImages(items []IngestItem) int {
	n := 0
	for _, it := range items {
		for _, b := range it.Blocks {
			if b.Type == "image" {
				n++
			}
		}
	}
	return n
}

func validIngestItem(it IngestItem) bool {
	content := strings.TrimSpace(it.Content) != "" && len(it.Blocks) == 0 || it.Content == "" && len(it.Blocks) > 0 && len(it.Blocks) <= ingestMaxBlocks && !slices.ContainsFunc(it.Blocks, func(b IngestBlock) bool { return !validIngestBlock(b) })
	return content && len(it.Content) <= fileExtractMaxText && utf8.ValidString(it.Content) && !strings.ContainsRune(it.Content, 0) &&
		(it.DocumentID == "" || validRunLabel(it.DocumentID, 200) && !containsSecret(it.DocumentID)) && slices.Contains([]string{"", "replace", "append"}, it.UpdateMode) &&
		validExtractOptions(ingestOptions(it), time.Now())
}

func ingestOptions(it IngestItem) ExtractOptions {
	return ExtractOptions{Context: it.Context, Timestamp: it.Timestamp, Tags: it.Tags, Metadata: it.Metadata, Entities: it.Entities, Strategy: it.Strategy}
}

// validIngestBlock: a text block carries text only, an image block a supported image type.
func validIngestBlock(b IngestBlock) bool {
	switch b.Type {
	case "text":
		return b.ContentBase64 == "" && b.MIME == "" && strings.TrimSpace(b.Text) != "" && len(b.Text) <= fileExtractMaxText && utf8.ValidString(b.Text) && !strings.ContainsRune(b.Text, 0)
	case "image":
		return b.Text == "" && b.ContentBase64 != "" && strings.HasPrefix(b.MIME, "image/") && slices.Contains(slices.Collect(maps.Values(mediaFormats)), b.MIME)
	}
	return false
}

// ingestItem plans, defends, filters, chunks and stores one item.
func (s *Service) ingestItem(ctx context.Context, token string, d Decision, scope Scope, settings SpaceConfig, it IngestItem, sync int) (FileMemoriesResult, error) {
	plan, err := s.extractPlan(settings, ingestOptions(it), nil)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	text, docRef := it.Content, "text:"+ContentHash(it.Content)
	if len(it.Blocks) > 0 {
		docRef = "text:" + ContentHash(structuredText(it.Blocks))
		if text, err = s.ingestBlocksText(ctx, token, d, scope, it.Blocks); err != nil {
			return FileMemoriesResult{}, err
		}
	}
	if err = s.defendItem(ctx, scope, "write:memory_candidates", &plan.Options, &text); err != nil {
		return FileMemoriesResult{}, err
	}
	chunks, redacted, err := extractFileChunks([]string{text}, false, extractChunkRunes(plan.Settings))
	if err != nil {
		return FileMemoriesResult{}, err
	}
	// Without a document id the content names the document, so the same content is extracted once.
	plan.Label = "ingest"
	if it.DocumentID != "" {
		docRef, plan.Label = "doc:"+it.DocumentID, it.DocumentID
	}
	return s.extractStored(ctx, token, d, scope, plan, docRef, "", chunks, FileMemoriesResult{Skipped: extractRedactionNote(redacted)}, sync, it.UpdateMode == "append")
}

// storeExtractSources stores the filtered text as source evidence, in parts
// small enough for a source record, and points every chunk at its part.
func (s *Service) storeExtractSources(ctx context.Context, token string, scope Scope, label string, chunks []FileExtractChunk) error {
	now := time.Now().UTC()
	for start := 0; start < len(chunks); {
		end, size := start, 0
		var text strings.Builder
		for end < len(chunks) && (end == start || size+len(chunks[end].Text)+64 <= fileExtractSourceBytes) {
			part := "[" + chunks[end].Locator + "]\n" + chunks[end].Text + "\n\n"
			text.WriteString(part)
			size += len(part)
			end++
		}
		src, err := s.Create(ctx, token, "sources", CreateInput{Scope: scope, Kind: "document", Text: strings.TrimSpace(text.String()),
			OriginProduct: "arkism", SourceRef: "file:" + label, CollectedAt: &now})
		if err != nil {
			return err
		}
		for i := start; i < end; i++ {
			chunks[i].Source = Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}
		}
		start = end
	}
	return nil
}

func extractSourceIDs(chunks []FileExtractChunk) []string {
	ids := []string{}
	for _, ch := range chunks {
		if !slices.Contains(ids, ch.Source.ID) {
			ids = append(ids, ch.Source.ID)
		}
	}
	return ids
}

// extractStored extracts the chunks that changed since the document's last
// upload: up to sync of them now, the rest as a queued job. Chunks without a source
// yet (an uploaded file, ingested text) get one; unchanged chunks keep their old
// source. With appendText the chunks follow the document's earlier ones instead of
// replacing them. A reprocess round extracts every chunk again.
func (s *Service) extractStored(ctx context.Context, token string, d Decision, scope Scope, plan FileExtractJob, docRef, round string, chunks []FileExtractChunk, out FileMemoriesResult, sync int, appendText bool) (FileMemoriesResult, error) {
	// The uploader is part of the identity: another person's file of the same
	// name is a different document, and its chunks and candidates stay theirs.
	docID := extractDocID(scope, d.PrincipalID, docRef)
	prev, err := s.extractDocState(ctx, scope, docID)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	if appendText {
		chunks = appendExtractChunks(prev, chunks)
	}
	if round != "" { // a reprocess round extracts every chunk again under new event ids
		prev = nil
		for i := range chunks {
			chunks[i].Round = round
		}
	}
	fresh, kept, delta := diffExtractChunks(prev, chunks)
	if len(fresh) > 0 && fresh[0].Source.ID == "" {
		if err = s.storeExtractSources(ctx, token, scope, plan.Label, fresh); err != nil {
			return FileMemoriesResult{}, err
		}
	}
	plan.Document, plan.Ref, plan.Chunks, plan.Kept, plan.Delta = docID, docRef, fresh, kept, delta
	return s.launchExtract(ctx, token, d, scope, plan, out, sync)
}

// launchExtract records the planned document job (document, chunks and delta filled
// in) and runs it at once when it has at most sync chunks; a longer one is queued.
func (s *Service) launchExtract(ctx context.Context, token string, d Decision, scope Scope, plan FileExtractJob, out FileMemoriesResult, sync int) (FileMemoriesResult, error) {
	fresh := plan.Chunks
	out.DocumentID = plan.Document
	out.SourceIDs = extractSourceIDs(append(slices.Clone(plan.Kept), fresh...))
	out.SourceID, out.Delta = out.SourceIDs[0], &plan.Delta
	plan.Total, plan.Skipped = len(fresh), out.Skipped
	job, err := s.createExtractJob(ctx, d, scope, plan)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out.JobID, out.Status = job.ID, job.Status
	if len(fresh) == 0 || len(fresh) > sync {
		return out, nil
	}
	r, ok, err := s.claimExtractJob(ctx, d, scope, job.ID)
	if err != nil || !ok {
		return out, err
	}
	r, runErr, err := s.runExtractJob(ctx, token, d, r)
	if runErr != nil { // the job stays failed at its chunk; its run route resumes it
		out.Status = r.Status
		return out, runErr
	}
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out.Status, out.CandidatesCreated, out.Skipped = r.Status, r.FileExtract.CandidatesCreated, r.FileExtract.Skipped
	return out, nil
}

// extractEventID is the capture event of one chunk; a reprocess round makes it new.
func extractEventID(ch FileExtractChunk) string {
	id := "file-extract:" + ch.Source.ID + ":" + ch.Locator + ":" + ch.Hash[:min(16, len(ch.Hash))]
	if ch.Round != "" {
		id += ":" + ch.Round
	}
	return id
}

// appendExtractChunks puts the new text after the document's earlier chunks, numbered on.
func appendExtractChunks(prev, next []FileExtractChunk) []FileExtractChunk {
	out := slices.Clone(prev)
	for i, ch := range next {
		ch.Locator = fmt.Sprintf("chunk:%d", len(prev)+i+1)
		out = append(out, ch)
	}
	return out
}

func extractDocID(scope Scope, principal, docRef string) string {
	return "file-extract-doc-" + ContentHash(structuredText(scope)+"\x00"+principal+"\x00"+docRef)
}

// diffExtractChunks splits an upload by chunk hash against the chunks the
// last upload already extracted: fresh ones need the model, kept ones carry
// their old source and candidates, and the delta lists what disappeared.
func diffExtractChunks(prev, next []FileExtractChunk) ([]FileExtractChunk, []FileExtractChunk, FileExtractDelta) {
	old := map[string]FileExtractChunk{}
	for _, ch := range prev {
		if _, ok := old[ch.Hash]; !ok {
			old[ch.Hash] = ch
		}
	}
	seen := map[string]bool{}
	fresh, kept := []FileExtractChunk{}, []FileExtractChunk{}
	for _, ch := range next {
		seen[ch.Hash] = true
		if p, ok := old[ch.Hash]; ok {
			ch.Text, ch.Source, ch.Candidates = "", p.Source, p.Candidates
			kept = append(kept, ch)
			continue
		}
		fresh = append(fresh, ch)
	}
	delta := FileExtractDelta{ChunksTotal: len(next), ChunksUnchanged: len(kept), ChunksChanged: len(fresh), RemovedSources: []FileExtractRemoved{}}
	for _, ch := range prev {
		if !seen[ch.Hash] {
			seen[ch.Hash] = true
			delta.RemovedSources = append(delta.RemovedSources, FileExtractRemoved{Locator: ch.Locator, Source: ch.Source, CandidateIDs: append([]string{}, ch.Candidates...)})
		}
	}
	delta.ChunksRemoved = len(delta.RemovedSources)
	return fresh, kept, delta
}

// extractAndCapture returns the ids of the candidates the chunk produced or merged into,
// and stores the answer's causes (when the plan asks for them) as causal link candidates.
func (s *Service) extractAndCapture(ctx context.Context, token string, d Decision, scope Scope, budget *WikiJob, plan *FileExtractJob, ch FileExtractChunk) ([]string, []ExtractSkip, error) {
	// The chunk's model calls name its source document and the candidates they produced.
	ctx, note := withLLMNote(ctx, ch.Source.ID, nil)
	res, err := s.extractChunk(ctx, token, d, scope, budget, plan, ch)
	if err != nil {
		return nil, nil, err
	}
	ids, skipped, err := s.captureChunk(ctx, token, d, scope, plan, ch, res)
	if err == nil {
		s.attachLLMCreated(ctx, scope, note, ids)
	}
	return ids, skipped, err
}

// captureChunk stores one chunk's read answer: its claims as candidates, its causes as causal link candidates.
func (s *Service) captureChunk(ctx context.Context, token string, d Decision, scope Scope, plan *FileExtractJob, ch FileExtractChunk, res ExtractPreviewResult) ([]string, []ExtractSkip, error) {
	ids, more, err := s.captureExtracted(ctx, token, scope, plan, ch, res.Claims)
	if err == nil && len(res.Causes) > 0 {
		err = s.storeExtractCauses(ctx, d, scope, ids, res.Causes)
	}
	return slices.DeleteFunc(ids, func(id string) bool { return id == "" }), append(res.Skipped, more...), err
}

// captureExtracted hands claims to Capture, which dedupes and screens them and
// only ever creates candidates; one event per chunk makes a retry a no-op. The ids
// are aligned with claims ("" where none was created). The item's timestamp is the
// event time, its labels go on every claim.
func (s *Service) captureExtracted(ctx context.Context, token string, scope Scope, plan *FileExtractJob, ch FileExtractChunk, claims []ExtractedClaim) ([]string, []ExtractSkip, error) {
	if len(claims) == 0 {
		return nil, nil, nil
	}
	gen := Generator{Model: "none", PromptVersion: extractChunksVersion, PipelineVersion: fileExtractPipelineVersion}
	if plan.Settings.Mode != spaceExtractChunks {
		gen = s.Wiki.Generator()
		gen.PromptVersion, gen.PipelineVersion = fileExtractPromptVersion, fileExtractPipelineVersion
	}
	at, o := plan.refTime().UTC(), plan.Options
	in := CaptureInput{Scope: scope, Generator: &gen, zone: plan.zone(),
		Event: CaptureEvent{ID: extractEventID(ch), Type: "backfill", OriginProduct: "arkism", OccurredAt: &at}}
	for _, c := range claims {
		confidence := c.Confidence
		in.Claims = append(in.Claims, CaptureClaim{Subject: "file-extract", Text: c.Content, Kind: c.Kind, Basis: "inferred", Confidence: &confidence,
			Reason:     ch.Locator + ": " + runeCutString(extractSpaces(c.EvidenceQuote), fileExtractQuoteRunes),
			SourceRefs: []Ref{ch.Source}, locator: ch.Locator, labels: plan.Tags, OccurredFrom: c.OccurredFrom, OccurredTo: c.OccurredTo,
			Tags: o.Tags, Metadata: o.Metadata, Entities: entityNameInputs(o.Entities),
			// The document was read by a model, so the evidence is inference whatever the model labelled it.
			MemoryContext: &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: c.SemanticKind}})
	}
	res, err := s.Capture(ctx, token, in)
	if err != nil {
		return nil, nil, err
	}
	ids, skipped := make([]string, len(claims)), []ExtractSkip{}
	for i, item := range res.Items {
		ids[i] = item.CandidateID
		if item.CandidateID == "" {
			skipped = append(skipped, ExtractSkip{Locator: ch.Locator, Reason: item.Status + ":" + item.Reason})
		}
	}
	return ids, skipped, nil
}

// storeExtractCauses records causes between one answer's claims as causal link
// candidates between their candidates (ids aligned with the claims). A candidate keeps
// its id as a memory, so the link is reviewable once a person approved both claims;
// a pair that already has a row is left as it is.
func (s *Service) storeExtractCauses(ctx context.Context, d Decision, scope Scope, ids []string, causes []ExtractedCause) error {
	now := time.Now().UTC()
	for _, c := range causes {
		cause, effect := ids[c.Cause], ids[c.Effect]
		if cause == "" || effect == "" || cause == effect {
			continue
		}
		err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			a, e := tx.Get(ctx, "memory_candidates", scope, cause)
			if e != nil {
				return e
			}
			b, e := tx.Get(ctx, "memory_candidates", scope, effect)
			if e != nil {
				return e
			}
			r := causalRecord(d, a, b, c.Reason, "candidate", now)
			if _, e = tx.Get(ctx, colMemoryRelations, scope, r.ID); !errors.Is(e, ErrNotFound) {
				return e
			}
			if e = tx.Insert(ctx, colMemoryRelations, r); e != nil {
				return e
			}
			return audit(ctx, tx, scope, d.PrincipalID, "propose:causal_link", r.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
