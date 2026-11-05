package memory

// Files and text become memory CANDIDATES, never memories: the configured
// wiki model reads privacy-filtered text and proposes claims, and every claim
// goes through Capture (dedupe, secret and instruction checks) to the review
// queue a person decides. A dry run shows the claims and stores nothing.
//
// A file the request budget cannot cover runs as a job in the "jobs"
// collection (kind file_memory_extract); its chunks carry their own source
// references, so a job resumes from the next chunk after a budget stop.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	fileExtractJobKind         = "file_memory_extract"
	fileExtractPromptVersion   = "file-extract-v1"
	fileExtractPipelineVersion = "arkism-file-extract-v1"
	// The wiki_ prefix puts these calls under the wiki model's daily call cap.
	fileExtractOperation   = "wiki_memory_extract"
	fileExtractChunkRunes  = 6000
	fileExtractMaxChunks   = 24
	fileExtractSyncChunks  = 2
	fileExtractMaxClaims   = 32 // Capture takes at most 32 claims per event
	fileExtractMaxFile     = 4 << 20
	fileExtractMaxText     = 512 << 10
	fileExtractMaxPreview  = 64 << 10
	fileExtractSourceBytes = 60000 // under the 64000-byte source text limit
	fileExtractJobTokens   = 160000
	fileExtractJobLease    = 10 * time.Minute
	fileExtractQuoteRunes  = 110
)

var (
	extractSemanticKinds = []string{"fact", "preference", "correction", "lesson", "failure_recovery"}
	extractEvidenceKinds = []string{"user_statement", "tool_observation", "agent_statement", "inference"}
	extractDateLayouts   = []string{time.RFC3339, time.DateOnly, "2006-01"}
)

// ExtractedClaim is one model-proposed claim; it is a proposal, never a memory.
type ExtractedClaim struct {
	Content       string  `json:"content"`
	Kind          string  `json:"kind"`
	SemanticKind  string  `json:"semantic_kind"`
	EvidenceKind  string  `json:"evidence_kind"`
	ObservedAt    string  `json:"observed_at,omitempty"`
	EvidenceQuote string  `json:"evidence_quote"`
	Confidence    float64 `json:"confidence"`
	Locator       string  `json:"locator,omitempty"`
}

type ExtractSkip struct {
	Locator string `json:"locator,omitempty"`
	Reason  string `json:"reason"`
}

type ExtractPreviewInput struct {
	Scope       Scope  `json:"scope"`
	Text        string `json:"text"`
	SourceLabel string `json:"source_label,omitempty"`
}

type ExtractPreviewResult struct {
	Claims  []ExtractedClaim `json:"claims"`
	Skipped []ExtractSkip    `json:"skipped"`
}

type FileMemoriesInput struct {
	Scope         Scope  `json:"scope"`
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
	MIME          string `json:"mime,omitempty"`
	DryRun        bool   `json:"dry_run"`
}

type KnowledgeExtractInput struct {
	Scope  Scope `json:"scope"`
	DryRun bool  `json:"dry_run"`
}

type FileMemoriesResult struct {
	SourceID          string           `json:"source_id,omitempty"`
	SourceIDs         []string         `json:"source_ids,omitempty"`
	JobID             string           `json:"job_id,omitempty"`
	Status            string           `json:"status"`
	CandidatesCreated int              `json:"candidates_created"`
	Claims            []ExtractedClaim `json:"claims,omitempty"`
	Skipped           []ExtractSkip    `json:"skipped"`
}

// FileExtractChunk is one model request's worth of text and where it came from.
type FileExtractChunk struct {
	Locator string `json:"locator" bson:"locator"`
	Source  Ref    `json:"source" bson:"source"`
	Text    string `json:"-" bson:"text"`
}

type FileExtractJob struct {
	Label             string             `json:"label" bson:"label"`
	Sources           []string           `json:"sources" bson:"sources"`
	Chunks            []FileExtractChunk `json:"-" bson:"chunks"`
	Total             int                `json:"total" bson:"total"`
	Next              int                `json:"next" bson:"next"`
	CandidatesCreated int                `json:"candidates_created" bson:"candidates_created"`
	Skipped           []ExtractSkip      `json:"skipped" bson:"skipped"`
	Calls             int                `json:"calls" bson:"calls"`
	Tokens            int64              `json:"tokens" bson:"tokens"`
	Error             string             `json:"error,omitempty" bson:"error,omitempty"`
	RequestedBy       string             `json:"requested_by" bson:"requested_by"`
}

type FileExtractJobView struct {
	ID        string         `json:"id"`
	Revision  int64          `json:"revision"`
	Status    string         `json:"status"`
	Job       FileExtractJob `json:"job"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func extractPrompt(text string) (string, string) {
	system := "You extract durable memory claims from one document for a person to review; nothing you return is saved without that review. " + wikiDataRule +
		" Extract preferences, prohibitions (things never to do), decisions, lessons and facts that stay useful later; skip small talk and one-off details." +
		" Write content and evidence_quote in the same language as the source and never translate: Korean stays Korean." +
		` Return only one JSON object: {"claims":[{"content": string, "kind": "user"|"decision"|"procedural"|"lesson"|"episodic"|"semantic", "semantic_kind": "fact"|"preference"|"correction"|"lesson"|"failure_recovery", "evidence_kind": "user_statement"|"tool_observation"|"agent_statement"|"inference", "observed_at": string, "evidence_quote": string, "confidence": number}]}.` +
		" evidence_quote is an exact excerpt of the source, at most 300 characters. observed_at is the event date the source states (YYYY-MM-DD, YYYY-MM or RFC 3339), or an empty string. confidence is between 0 and 1. At most 32 claims; answer {\"claims\":[]} when nothing qualifies."
	return system, wikiFence("SOURCE_DATA", text)
}

func extractSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

func validExtractedClaim(c ExtractedClaim, source string, now time.Time) bool {
	content := strings.TrimSpace(c.Content)
	if content == "" || len(content) > 4000 || !utf8.ValidString(content) || strings.ContainsRune(content, 0) || c.Locator != "" ||
		!slices.Contains(captureKinds, c.Kind) || !slices.Contains(extractSemanticKinds, c.SemanticKind) || !slices.Contains(extractEvidenceKinds, c.EvidenceKind) ||
		math.IsNaN(c.Confidence) || c.Confidence < 0 || c.Confidence > 1 || len(c.ObservedAt) > 64 {
		return false
	}
	quote := extractSpaces(c.EvidenceQuote)
	// A quote the source does not contain is the model's own wording, not evidence.
	if quote == "" || len(c.EvidenceQuote) > 1200 || !strings.Contains(extractSpaces(source), quote) {
		return false
	}
	return c.ObservedAt == "" || slices.ContainsFunc(extractDateLayouts, func(layout string) bool {
		t, err := time.Parse(layout, c.ObservedAt)
		return err == nil && storableTime(t) && !t.After(now)
	})
}

// parseExtractedClaims keeps the well-formed claims of a strict JSON answer and
// reports every malformed one; false means the answer itself was not JSON.
func parseExtractedClaims(answer, source, locator string) ([]ExtractedClaim, []ExtractSkip, bool) {
	var raw struct {
		Claims []json.RawMessage `json:"claims"`
	}
	if !decodeModelJSON(answer, &raw) {
		return nil, nil, false
	}
	claims, skipped := []ExtractedClaim{}, []ExtractSkip{}
	now := time.Now().UTC()
	for i, item := range raw.Claims {
		if i >= fileExtractMaxClaims {
			skipped = append(skipped, ExtractSkip{Locator: locator, Reason: "too_many_claims"})
			break
		}
		var c ExtractedClaim
		if !decodeModelJSON(string(item), &c) || !validExtractedClaim(c, source, now) {
			skipped = append(skipped, ExtractSkip{Locator: locator, Reason: "malformed_claim"})
			continue
		}
		c.Content, c.Locator = strings.TrimSpace(c.Content), locator
		claims = append(claims, c)
	}
	return claims, skipped, true
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

// extractChunks packs each page into model-sized chunks; paged text gets page locators.
func extractChunks(pages []string, paged bool) []FileExtractChunk {
	out := []FileExtractChunk{}
	for p, page := range pages {
		for c, piece := range knowledgePackParagraphs(knowledgeParagraphs(strings.Split(page, "\n"), false), fileExtractChunkRunes) {
			locator := fmt.Sprintf("chunk:%d", c+1)
			if paged {
				locator = fmt.Sprintf("page:%d/chunk:%d", p+1, c+1)
			}
			out = append(out, FileExtractChunk{Locator: locator, Text: piece})
		}
	}
	return out
}

// extractActor is a person with the scope's permission and a configured model.
func (s *Service) extractActor(ctx context.Context, token, action string, scope Scope) (Decision, error) {
	d, err := s.wikiAuthorize(ctx, token, action, scope, "", "", roleHuman)
	if err != nil {
		return d, err
	}
	if s.Wiki == nil {
		return d, ErrModelNotConfigured
	}
	return d, nil
}

// extractChunk sends one privacy-filtered chunk to the model under the budget gate.
func (s *Service) extractChunk(ctx context.Context, token string, d Decision, scope Scope, budget *WikiJob, ch FileExtractChunk) ([]ExtractedClaim, []ExtractSkip, error) {
	t, err := s.authorizeAt(ctx, token, "generate:memory_candidates", scope, "", ch.Text, "external:wiki-llm")
	if err != nil {
		return nil, nil, err
	}
	if t.PrincipalID != d.PrincipalID || !t.ExternalTransfer || !t.ContentSafe {
		return nil, nil, ErrDenied
	}
	system, user := extractPrompt(ch.Text)
	answer, err := s.wikiCall(ctx, scope, budget, fileExtractOperation, system, user)
	if errors.Is(err, errWikiBudget) {
		return nil, nil, ErrModelBudget
	}
	if err != nil {
		return nil, nil, err
	}
	claims, skipped, ok := parseExtractedClaims(answer, ch.Text, ch.Locator)
	if !ok {
		return nil, []ExtractSkip{{Locator: ch.Locator, Reason: "malformed_answer"}}, nil
	}
	return claims, skipped, nil
}

// previewChunks runs the first chunks a request may pay for and stores nothing.
func (s *Service) previewChunks(ctx context.Context, token string, d Decision, scope Scope, chunks []FileExtractChunk, out *FileMemoriesResult) error {
	budget := &WikiJob{MaxTotalTokens: fileExtractJobTokens}
	for i, ch := range chunks {
		if i >= fileExtractSyncChunks {
			out.Skipped = append(out.Skipped, ExtractSkip{Locator: ch.Locator, Reason: "dry_run_limit"})
			continue
		}
		claims, skipped, err := s.extractChunk(ctx, token, d, scope, budget, ch)
		if err != nil {
			return err
		}
		out.Claims, out.Skipped = append(out.Claims, claims...), append(out.Skipped, skipped...)
	}
	return nil
}

// ExtractPreview proposes claims from pasted text. Nothing is stored; only the
// model call is recorded in the usage ledger.
func (s *Service) ExtractPreview(ctx context.Context, token string, in ExtractPreviewInput) (ExtractPreviewResult, error) {
	if !validScope(in.Scope) || strings.TrimSpace(in.Text) == "" || len(in.Text) > fileExtractMaxPreview || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) ||
		in.SourceLabel != "" && (!validRunLabel(in.SourceLabel, 200) || containsSecret(in.SourceLabel)) {
		return ExtractPreviewResult{}, ErrInvalid
	}
	d, err := s.extractActor(ctx, token, "generate:memory_candidates", in.Scope)
	if err != nil {
		return ExtractPreviewResult{}, err
	}
	text, redacted := extractClean(in.Text)
	out := FileMemoriesResult{Skipped: extractRedactionNote(redacted)}
	if err = s.previewChunks(ctx, token, d, in.Scope, extractChunks([]string{text}, false), &out); err != nil {
		return ExtractPreviewResult{}, err
	}
	return ExtractPreviewResult{Claims: append([]ExtractedClaim{}, out.Claims...), Skipped: out.Skipped}, nil
}

func extractRedactionNote(n int) []ExtractSkip {
	if n == 0 {
		return []ExtractSkip{}
	}
	return []ExtractSkip{{Reason: fmt.Sprintf("privacy_redacted:%d", n)}}
}

// extractFilePages reads a txt, md, json, docx or pdf file into page texts.
func extractFilePages(in FileMemoriesInput) ([]string, bool, error) {
	ext := strings.ToLower(path.Ext(in.Filename))
	mime := knowledgeFileMIME[ext]
	if ext == ".pdf" {
		mime = "application/pdf"
	}
	if mime == "" || in.MIME != "" && in.MIME != mime {
		return nil, false, ErrInvalid
	}
	if len(in.ContentBase64) > base64.StdEncoding.EncodedLen(fileExtractMaxFile) {
		return nil, false, errBlobTooLarge
	}
	if ext == ".docx" {
		text, err := knowledgeDOCXText(in.ContentBase64)
		return []string{text}, false, err
	}
	raw, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if err != nil || len(raw) == 0 {
		return nil, false, ErrInvalid
	}
	if ext == ".pdf" {
		pages, err := pdfPagesText(raw)
		return pages, true, err
	}
	if !utf8.Valid(raw) || slices.Contains(raw, 0) {
		return nil, false, ErrInvalid
	}
	text, failed := normalizeKnowledgeContent(string(raw), ext)
	if failed {
		return nil, false, ErrInvalid
	}
	return []string{text}, false, nil
}

// extractFileChunks applies the privacy filter page by page and enforces size limits.
func extractFileChunks(pages []string, paged bool) ([]FileExtractChunk, int, error) {
	redacted, total := 0, 0
	for i := range pages {
		var n int
		pages[i], n = extractClean(pages[i])
		redacted += n
		total += len(pages[i])
	}
	chunks := extractChunks(pages, paged)
	if total > fileExtractMaxText || len(chunks) > fileExtractMaxChunks {
		return nil, 0, errBlobTooLarge
	}
	if len(chunks) == 0 {
		return nil, 0, ErrInvalid
	}
	return chunks, redacted, nil
}

// ExtractFileMemories turns one uploaded file into review candidates.
func (s *Service) ExtractFileMemories(ctx context.Context, token string, in FileMemoriesInput) (FileMemoriesResult, error) {
	if !validScope(in.Scope) || !validRunLabel(in.Filename, 200) || strings.ContainsAny(in.Filename, "/\\") || containsSecret(in.Filename) {
		return FileMemoriesResult{}, ErrInvalid
	}
	d, err := s.extractActor(ctx, token, "write:memory_candidates", in.Scope)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	pages, paged, err := extractFilePages(in)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	chunks, redacted, err := extractFileChunks(pages, paged)
	if err != nil {
		return FileMemoriesResult{}, err
	}
	out := FileMemoriesResult{Skipped: extractRedactionNote(redacted)}
	if in.DryRun {
		out.Status = JobStatusDone
		return out, s.previewChunks(ctx, token, d, in.Scope, chunks, &out)
	}
	if err = s.storeExtractSources(ctx, token, in.Scope, in.Filename, chunks); err != nil {
		return FileMemoriesResult{}, err
	}
	return s.extractStored(ctx, token, d, in.Scope, in.Filename, chunks, out)
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

// extractStored runs a small file now and queues a larger one as a job.
func (s *Service) extractStored(ctx context.Context, token string, d Decision, scope Scope, label string, chunks []FileExtractChunk, out FileMemoriesResult) (FileMemoriesResult, error) {
	out.SourceIDs = extractSourceIDs(chunks)
	out.SourceID = out.SourceIDs[0]
	if len(chunks) > fileExtractSyncChunks {
		job, err := s.createExtractJob(ctx, d, scope, label, chunks, out.Skipped)
		if err != nil {
			return FileMemoriesResult{}, err
		}
		out.JobID, out.Status = job.ID, job.Status
		return out, nil
	}
	budget := &WikiJob{MaxTotalTokens: fileExtractJobTokens}
	for _, ch := range chunks {
		created, skipped, err := s.extractAndCapture(ctx, token, d, scope, budget, ch)
		if err != nil {
			return FileMemoriesResult{}, err
		}
		out.CandidatesCreated += created
		out.Skipped = append(out.Skipped, skipped...)
	}
	out.Status = JobStatusDone
	return out, nil
}

func (s *Service) extractAndCapture(ctx context.Context, token string, d Decision, scope Scope, budget *WikiJob, ch FileExtractChunk) (int, []ExtractSkip, error) {
	claims, skipped, err := s.extractChunk(ctx, token, d, scope, budget, ch)
	if err != nil {
		return 0, nil, err
	}
	created, more, err := s.captureExtracted(ctx, token, scope, ch, claims)
	return created, append(skipped, more...), err
}

// captureExtracted hands claims to Capture, which dedupes and screens them and
// only ever creates candidates; one event per chunk makes a retry a no-op.
func (s *Service) captureExtracted(ctx context.Context, token string, scope Scope, ch FileExtractChunk, claims []ExtractedClaim) (int, []ExtractSkip, error) {
	if len(claims) == 0 {
		return 0, nil, nil
	}
	gen := s.Wiki.Generator()
	gen.PromptVersion, gen.PipelineVersion = fileExtractPromptVersion, fileExtractPipelineVersion
	now := time.Now().UTC()
	in := CaptureInput{Scope: scope, Generator: &gen,
		Event: CaptureEvent{ID: "file-extract:" + ch.Source.ID + ":" + ch.Locator, Type: "backfill", OriginProduct: "arkism", OccurredAt: &now}}
	for _, c := range claims {
		confidence := c.Confidence
		in.Claims = append(in.Claims, CaptureClaim{Subject: "file-extract", Text: c.Content, Kind: c.Kind, Basis: "inferred", Confidence: &confidence,
			Reason:     ch.Locator + ": " + runeCutString(extractSpaces(c.EvidenceQuote), fileExtractQuoteRunes),
			SourceRefs: []Ref{ch.Source}, locator: ch.Locator,
			// The document was read by a model, so the evidence is inference whatever the model labelled it.
			MemoryContext: &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: c.SemanticKind, ValidFrom: c.ObservedAt}})
	}
	res, err := s.Capture(ctx, token, in)
	if err != nil {
		return 0, nil, err
	}
	created, skipped := 0, []ExtractSkip{}
	for _, item := range res.Items {
		if item.CandidateID != "" {
			created++
			continue
		}
		skipped = append(skipped, ExtractSkip{Locator: ch.Locator, Reason: item.Status + ":" + item.Reason})
	}
	return created, skipped, nil
}
