package memory

// Semantic retrieval is a derived lane over the deterministic contract. Scope,
// binding, lifecycle, version and trust filters run first and produce the only
// IDs the vector lane may rank; embeddings are regenerable derived data that
// live on the chunk record, so canonical RLS, fences and privacy erasure
// already cover them.
import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"sort"
	"syscall"
	"time"
)

const (
	knowledgeModeDeterministic = "deterministic"
	knowledgeModeSemantic      = "semantic"
	knowledgeModeFusion        = "fusion"
	knowledgeCandidateLimit    = 2048
	knowledgeRRFConstant       = 60.0
	// Fallback policies. The named contract combinations are:
	// semantic_strict          mode=semantic + strict
	// semantic_fallback_allowed mode=semantic + fallback_allowed (default)
	// fusion_strict            mode=fusion + strict
	knowledgeFallbackAllowed = "fallback_allowed"
	knowledgeFallbackStrict  = "strict"
	// The TEI CPU backend forces max_batch_requests=4 and panics when a client
	// batch overflows its bounded queue, so the job batch stays at that ceiling.
	// Exported as KnowledgeIndexBatch for the runtime config validation.
	knowledgeIndexBatch = 4
)

// KnowledgeIndexBatch is the embedding job's queue batch size. It shares the
// TEI CPU backend ceiling with the client batch and the server batch.
const KnowledgeIndexBatch = knowledgeIndexBatch

func validKnowledgeMode(mode string) bool {
	return mode == knowledgeModeDeterministic || mode == knowledgeModeSemantic || mode == knowledgeModeFusion
}

func validKnowledgeFallback(policy string) bool {
	return policy == knowledgeFallbackAllowed || policy == knowledgeFallbackStrict
}

func knowledgeAdapterVersion(mode string) string {
	switch mode {
	case knowledgeModeSemantic:
		return "tei-pgvector-cosine-v1"
	case knowledgeModeFusion:
		return "rrf-" + knowledgeFTSAdapter + "-pgvector-v1"
	}
	return knowledgeFTSAdapter
}

// embedderFailureKind maps an embedder error to one fixed, content-free
// reason code so retrieval logs and index failures distinguish transport
// outage classes instead of collapsing into one bucket.
func embedderFailureKind(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return "embedder_denied"
	case errors.Is(err, ErrInvalid):
		return "embedder_rejected_input"
	case errors.Is(err, context.DeadlineExceeded):
		return "embedder_timeout"
	default:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return "embedder_timeout"
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return "embedder_connect_failed"
		}
		return "embedder_unavailable"
	}
}

func knowledgeFallbackReason(err error) string {
	reason := embedderFailureKind(err)
	if reason == "embedder_rejected_input" {
		reason = "embedder_rejected_query"
	}
	return reason
}

// embedKnowledgeQuery keeps the provider call outside every transaction.
func (s *Service) embedKnowledgeQuery(ctx context.Context, d Decision, query string) (*Embedding, error) {
	if s.Embedder == nil {
		return nil, ErrUnavailable
	}
	if !embedderTransferAllowed(s.Embedder, d) {
		return nil, ErrDenied
	}
	out, err := s.Embedder.Embed(ctx, []string{query}, embedInputQuery)
	if err != nil {
		return nil, err
	}
	if len(out) != 1 || len(out[0].Vector) != VectorDimension || out[0].Space == "" {
		return nil, ErrUnavailable
	}
	return &out[0], nil
}

// embedderTransferAllowed: loopback TEI keeps text on the host; any other
// embedder sends it to a provider and needs the decision's external transfer.
func embedderTransferAllowed(e Embedder, d Decision) bool {
	if _, local := e.(*TEIEmbedder); local {
		return true
	}
	return d.ExternalTransfer
}

func knowledgeChunkEmbedded(chunk Record, space string) bool {
	e := chunk.Embedding
	return e != nil && e.Space == space && e.ContentHash == chunk.ContentHash && e.Dimension == VectorDimension && len(e.Vector) == VectorDimension
}

// knowledgeCandidateExclusion reports why a filtered chunk cannot enter the
// ranking lanes of this mode. Fusion keeps a chunk that either lane can rank.
func knowledgeCandidateExclusion(mode string, matched int, embedded bool) string {
	switch mode {
	case knowledgeModeSemantic:
		if !embedded {
			return "embedding_unavailable"
		}
	case knowledgeModeFusion:
		if matched == 0 && !embedded {
			return "no_text_match"
		}
	default:
		if matched == 0 {
			return "no_text_match"
		}
	}
	return ""
}

type knowledgeCandidate struct {
	b, h, doc, chunk Record
	overlap          int // distinct query terms the full-text lane matched
	textScore        float64
	embedded         bool
	distance         *float64
	fusion           *float64
}

func knowledgeDeterministicOrder(candidates []knowledgeCandidate) []knowledgeCandidate {
	out := slices.Clone(candidates)
	sort.Slice(out, func(i, j int) bool {
		if out[i].overlap != out[j].overlap {
			return out[i].overlap > out[j].overlap
		}
		if out[i].textScore != out[j].textScore {
			return out[i].textScore > out[j].textScore
		}
		if out[i].chunk.ID != out[j].chunk.ID {
			return out[i].chunk.ID < out[j].chunk.ID
		}
		return out[i].b.ID < out[j].b.ID
	})
	return out
}

// knowledgeRank orders already-authorized candidates. The vector lane receives
// an explicit ID allowlist, so similarity can only reorder what the filters
// allowed, never add to it. The second return value is how many candidates the
// vector lane actually ranked.
func knowledgeRank(ctx context.Context, tx Tx, scope Scope, mode string, candidates []knowledgeCandidate, embedding *Embedding) ([]knowledgeCandidate, int, error) {
	deterministic := knowledgeDeterministicOrder(candidates)
	if mode == knowledgeModeDeterministic {
		return deterministic, 0, nil
	}
	ids := []string{}
	byID := map[string]knowledgeCandidate{}
	for _, item := range candidates {
		if item.embedded && !slices.Contains(ids, item.chunk.ID) {
			ids = append(ids, item.chunk.ID)
		}
		byID[item.chunk.ID] = item
	}
	sort.Strings(ids)
	ranks := []VectorRank{}
	if len(ids) > 0 {
		out, err := tx.RankVectors(ctx, scope, colKnowledgeChunks, ids, embedding.Vector, embedding.Space, len(ids))
		if err != nil {
			return nil, 0, err
		}
		ranks = out
	}
	semantic := make([]knowledgeCandidate, 0, len(ranks))
	position := map[string]int{}
	for i, rank := range ranks {
		item, ok := byID[rank.ID]
		if !ok {
			return nil, 0, ErrConflict
		}
		distance := rank.Distance
		item.distance = &distance
		semantic = append(semantic, item)
		position[rank.ID] = i + 1
	}
	if mode == knowledgeModeSemantic {
		return semantic, len(ranks), nil
	}
	// Reciprocal rank fusion keeps the two lanes comparable without mixing a
	// token count and a cosine distance into one score.
	fused := map[string]float64{}
	for i, item := range deterministic {
		if item.overlap > 0 {
			fused[item.chunk.ID] += 1 / (knowledgeRRFConstant + float64(i+1))
		}
	}
	for id, rank := range position {
		fused[id] += 1 / (knowledgeRRFConstant + float64(rank))
	}
	out := make([]knowledgeCandidate, 0, len(fused))
	for _, item := range deterministic {
		score, ok := fused[item.chunk.ID]
		if !ok {
			continue
		}
		value := score
		item.fusion = &value
		if rank, ok := position[item.chunk.ID]; ok {
			distance := ranks[rank-1].Distance
			item.distance = &distance
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if *out[i].fusion != *out[j].fusion {
			return *out[i].fusion > *out[j].fusion
		}
		return out[i].chunk.ID < out[j].chunk.ID
	})
	return out, len(ranks), nil
}

func knowledgeRankEvidence(evidence *KnowledgeEvidence, mode string, item knowledgeCandidate, rank int) {
	if mode == knowledgeModeDeterministic {
		return
	}
	evidence.Rank, evidence.VectorDistance, evidence.FusionScore = rank, item.distance, item.fusion
}

func knowledgeSelection(mode string, item knowledgeCandidate, rank int) KnowledgeSelection {
	out := KnowledgeSelection{BindingID: item.b.ID, ChunkID: item.chunk.ID, ChunkRevision: item.chunk.Revision, ContentHash: item.chunk.ContentHash}
	if mode != knowledgeModeDeterministic {
		out.Rank, out.VectorDistance, out.FusionScore = rank, item.distance, item.fusion
	}
	return out
}

func knowledgeLogMode(log *KnowledgeRetrievalLog, requested, mode, policy, fallback string, embedding *Embedding) {
	if requested == knowledgeModeDeterministic && fallback == "" {
		return
	}
	log.RetrievalMode, log.RequestedMode, log.ModeFallback = mode, requested, fallback
	log.FallbackPolicy, log.FallbackUsed = policy, fallback != ""
	if embedding != nil {
		log.EmbeddingModel, log.EmbeddingRevision = embedding.Model, embedding.Revision
		log.EmbeddingSpaceID, log.EmbeddingDimension = embedding.Space, embedding.Dimension
	}
}

type KnowledgeIndexInput struct {
	Scope             Scope  `json:"scope"`
	KnowledgeSourceID string `json:"knowledge_source_id"`
}

type KnowledgeIndexResult struct {
	Scope              Scope     `json:"scope"`
	KnowledgeSourceID  string    `json:"knowledge_source_id"`
	DocumentID         string    `json:"document_id"`
	SourceVersion      string    `json:"source_version"`
	ChunkerVersion     string    `json:"chunker_version"`
	EmbeddingSpaceID   string    `json:"embedding_space_id"`
	EmbeddingModel     string    `json:"embedding_model"`
	EmbeddingRevision  string    `json:"embedding_revision"`
	EmbeddingDimension int       `json:"embedding_dimension"`
	Embedded           int       `json:"embedded"`
	Reused             int       `json:"reused"`
	Failed             int       `json:"failed"`
	IndexedAt          time.Time `json:"indexed_at"`
	ChunkIDs           []string  `json:"chunk_ids"`
}

type knowledgeIndexTarget struct {
	id       string
	revision int64
	hash     string
	text     string
}

// IndexKnowledgeEmbeddings embeds the current document's chunks. Content hash
// plus embedding space is the idempotency key: an unchanged chunk in the same
// space is never re-embedded, and a new source version brings new chunk rows
// that have no vector until they are indexed.
func (s *Service) IndexKnowledgeEmbeddings(ctx context.Context, token string, in KnowledgeIndexInput) (KnowledgeIndexResult, error) {
	var out KnowledgeIndexResult
	if !validRunLabel(in.KnowledgeSourceID, 256) {
		return out, ErrInvalid
	}
	d, err := s.knowledgeAuthorization(ctx, token, "write:"+colKnowledgeChunks, in.Scope, in.KnowledgeSourceID)
	if err != nil {
		return out, err
	}
	if s.Embedder == nil {
		return out, ErrUnavailable
	}
	if !embedderTransferAllowed(s.Embedder, d) {
		return out, ErrDenied
	}
	space := s.Embedder.Space()
	var targets []knowledgeIndexTarget
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = KnowledgeIndexResult{Scope: in.Scope, KnowledgeSourceID: in.KnowledgeSourceID, EmbeddingSpaceID: space, EmbeddingDimension: VectorDimension, ChunkIDs: []string{}}
		targets = nil
		h, err := tx.Get(ctx, colKnowledgeSources, in.Scope, in.KnowledgeSourceID)
		if err != nil {
			return err
		}
		if err = knowledgeLive(ctx, tx, h); err != nil {
			return err
		}
		if err = knowledgeSourceFence(ctx, tx, h); err != nil {
			return err
		}
		if h.Knowledge.LifecycleStatus == "failed" {
			return ErrInvalid
		}
		doc, err := tx.Get(ctx, colKnowledgeDocuments, in.Scope, h.Knowledge.DocumentID)
		if err != nil {
			return err
		}
		if err = knowledgeLive(ctx, tx, doc); err != nil {
			return err
		}
		if doc.Knowledge.SourceVersion != h.Knowledge.SourceVersion || doc.SourceEventID != h.SourceEventID {
			return ErrConflict
		}
		out.DocumentID, out.SourceVersion, out.ChunkerVersion = doc.ID, doc.Knowledge.SourceVersion, doc.Knowledge.ChunkerVersion
		for _, chunkID := range doc.Knowledge.ChunkIDs {
			chunk, err := tx.Get(ctx, colKnowledgeChunks, in.Scope, chunkID)
			if err != nil {
				return err
			}
			if err = knowledgeLive(ctx, tx, chunk); err != nil {
				return err
			}
			if chunk.Knowledge.DocumentID != doc.ID || chunk.SourceEventID != h.SourceEventID || ContentHash(chunk.Text) != chunk.ContentHash {
				return ErrConflict
			}
			out.ChunkIDs = append(out.ChunkIDs, chunk.ID)
			if knowledgeChunkEmbedded(chunk, space) {
				out.Reused++
				continue
			}
			targets = append(targets, knowledgeIndexTarget{id: chunk.ID, revision: chunk.Revision, hash: chunk.ContentHash, text: chunk.Text})
		}
		return nil
	})
	if err != nil || len(targets) == 0 {
		return out, err
	}
	// The provider call happens between transactions; the write below revalidates
	// every revision and content hash before it stores a vector.
	vectors := map[string]Embedding{}
	for start := 0; start < len(targets); start += knowledgeIndexBatch {
		batch := targets[start:min(start+knowledgeIndexBatch, len(targets))]
		texts := make([]string, len(batch))
		for i, target := range batch {
			texts[i] = target.text
		}
		embeddings, embedErr := s.Embedder.Embed(ctx, texts, embedInputDocument)
		if embedErr == nil && len(embeddings) != len(batch) {
			embedErr = ErrUnavailable
		}
		if embedErr != nil {
			// Durable partial-batch outcome: vectors from completed batches are
			// written (idempotently reused by a retry), only the remainder is
			// marked failed. The embedder error stays the job's error.
			written, failure := s.recordKnowledgeIndexFailure(ctx, in.Scope, d.PrincipalID, targets, vectors, embedErr)
			out.Embedded, out.Failed = written, len(targets)-written
			return out, errors.Join(failure, embedErr)
		}
		for i, target := range batch {
			if embeddings[i].Space != space || len(embeddings[i].Vector) != VectorDimension {
				// Same partial-batch contract as an embedder error: keep earlier batches.
				written, failure := s.recordKnowledgeIndexFailure(ctx, in.Scope, d.PrincipalID, targets, vectors, ErrUnavailable)
				out.Embedded, out.Failed = written, len(targets)-written
				return out, errors.Join(failure, ErrUnavailable)
			}
			embeddings[i].ContentHash = target.hash
			embeddings[i].Dimension = VectorDimension
			vectors[target.id] = embeddings[i]
		}
	}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Embedded = 0
		for _, target := range targets {
			chunk, err := tx.Get(ctx, colKnowledgeChunks, in.Scope, target.id)
			if err != nil {
				return err
			}
			if err = knowledgeLive(ctx, tx, chunk); err != nil {
				return err
			}
			if chunk.Revision != target.revision || chunk.ContentHash != target.hash {
				return ErrConflict
			}
			embedding := vectors[target.id]
			knowledge := *chunk.Knowledge
			knowledge.IndexStatus = "indexed"
			knowledge.IndexError = ""
			chunk.Knowledge = &knowledge
			chunk.Embedding = &embedding
			old := chunk.Revision
			chunk.Revision++
			chunk.UpdatedAt = time.Now().UTC()
			if err = tx.Replace(ctx, colKnowledgeChunks, chunk, old); err != nil {
				return err
			}
			out.Embedded++
			out.IndexedAt = embedding.EmbeddedAt
			out.EmbeddingModel, out.EmbeddingRevision = embedding.Model, embedding.Revision
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "knowledge:indexed", in.KnowledgeSourceID)
	})
	return out, err
}

// recordKnowledgeIndexFailure keeps a durable, content-free failure marker on
// every chunk the batch did not embed and commits the vectors that earlier
// batches already produced, so a retry only re-embeds the failed remainder. A
// chunk whose source was deleted or whose revision moved while the embedder
// call was in flight is left untouched: the job is cancelled for it and the
// next index run re-evaluates it against the live revision.
func (s *Service) recordKnowledgeIndexFailure(ctx context.Context, scope Scope, principal string, targets []knowledgeIndexTarget, done map[string]Embedding, cause error) (int, error) {
	reason := embedderFailureKind(cause)
	written := 0
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		written = 0 // a transient retry reruns this closure
		failed := 0
		for _, target := range targets {
			chunk, err := tx.Get(ctx, colKnowledgeChunks, scope, target.id)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return err
			}
			if err = knowledgeLive(ctx, tx, chunk); err != nil {
				continue
			}
			if chunk.Revision != target.revision || chunk.ContentHash != target.hash {
				continue
			}
			knowledge := *chunk.Knowledge
			if embedding, ok := done[target.id]; ok {
				knowledge.IndexStatus, knowledge.IndexError = "indexed", ""
				chunk.Knowledge, chunk.Embedding = &knowledge, &embedding
				written++
			} else {
				knowledge.IndexStatus, knowledge.IndexError = "failed", reason
				chunk.Knowledge = &knowledge
				failed++
			}
			old := chunk.Revision
			chunk.Revision++
			chunk.UpdatedAt = time.Now().UTC()
			if err = tx.Replace(ctx, colKnowledgeChunks, chunk, old); err != nil {
				return err
			}
		}
		if failed > 0 {
			if err := audit(ctx, tx, scope, principal, "knowledge:index_failed", reason); err != nil {
				return err
			}
		}
		if written > 0 {
			if err := audit(ctx, tx, scope, principal, "knowledge:indexed", reason); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

func (s *Service) registerKnowledgeSemanticHTTP(mux routeRegistrar) {
	mux.HandleFunc("POST /v1/knowledge/index", func(w http.ResponseWriter, r *http.Request) {
		var in KnowledgeIndexInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.IndexKnowledgeEmbeddings(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
