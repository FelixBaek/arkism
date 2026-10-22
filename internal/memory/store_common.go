package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	mathrand "math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	statusActive = "active"

	// VectorDimension is the stored embedding dimension (Gemini and TEI are both
	// configured for 1024). Embeddings of another dimension belong to another
	// space and are never searched in one request.
	VectorDimension = 1024

	// AuthorityDataOnly marks every hit as untrusted data, so a prompt builder
	// cannot mistake retrieved memory for instructions.
	AuthorityDataOnly = "data_only_untrusted"

	// Job lease vocabulary. A worker only takes pending or retry jobs that are
	// due, or running jobs whose lease ran out.
	JobStatusPending = "pending"
	JobStatusRetry   = "retry"
	JobStatusRunning = "running"
	JobStatusDone    = "done"
	JobStatusFailed  = "failed"

	colMemories      = "memories"
	colSkillVersions = "skill_versions"
	colSkills        = "skills"
	colJobs          = "jobs"
	colReflections   = "reflections"
	colProviderUsage = "provider_usage"

	defaultSearchLimit  = 20
	maxKeywordTokens    = 16
	maxDocumentKeywords = 512
	maxKeywordLength    = 256
	maxJobAttempts      = 5
	keywordTrim         = ".,;:!?()[]{}\"'`<>*"
	// keywordCallTrim is keywordTrim without parentheses, so a call marker
	// survives while the punctuation around it is dropped.
	keywordCallTrim = ".,;:!?[]{}\"'`<>*"

	// Search lane reasons. Fuse weights exact_keyword highest, and the reasons
	// are how a caller can tell which lane found a hit.
	reasonExactKeyword = "exact_keyword"
	reasonTextSearch   = "text_search"
	reasonVector       = "vector_search"
)

var (
	baseRetryDelay = 5 * time.Second
	maxRetryDelay  = 15 * time.Minute
)

// searchableCollections are the only collections a search may return. Skills,
// candidates, runs and audit rows stay out of retrieval.
var searchableCollections = []string{colMemories, colSkillVersions, colReflections}

// searchableLive is the decision made on the live document. skillParent is only
// set for skill_versions, and a non-empty space additionally requires the live
// embedding metadata to still describe the space that was searched.
func searchableLive(rec Record, collection string, space string, now time.Time, skillParent *Record, history ...bool) bool {
	if len(history) > 0 && history[0] {
		if collection != colMemories || !historySearchable(rec) {
			return false
		}
		rec.Status, rec.EffectiveAt, rec.ExpiresAt = statusActive, nil, nil
	}
	if collection == colReflections && !reflectionEligible(rec) {
		return false
	}
	if !liveAt(rec, now) {
		return false
	}
	if collection == colSkillVersions && !versionIsActive(rec, skillParent) {
		return false
	}
	if space == "" {
		return true
	}

	emb := rec.Embedding
	if emb == nil || emb.Space != space || emb.Dimension != VectorDimension {
		return false
	}
	if emb.ContentHash == "" {
		return false
	}

	// The stored hash must still describe the stored text, otherwise the vector
	// belongs to a revision that has since changed. The space string already
	// pins provider, model, revision and dimension.
	return emb.ContentHash == ContentHash(recordContent(rec))
}

// versionIsActive answers whether a version is still the one its skill points
// at. The parent owns the pointer, so a superseded version stops being
// retrievable even while its own row is unchanged.
func versionIsActive(version Record, parent *Record) bool {
	if parent == nil || parent.Status != statusActive {
		return false
	}

	return parent.ActiveVersion == version.ID
}

// checkReplaceRevision accepts the record a caller read (the CAS revision) or
// the record it is about to write (revision+1). A caller that is behind the
// stored document has a conflict; one that skips ahead has a bug.
func checkReplaceRevision(recRevision, revision int64) error {
	switch {
	case recRevision == revision || recRevision == revision+1:
		return nil
	case recRevision < revision:
		return ErrConflict
	default:
		return ErrInvalid
	}
}

// insertIfAbsent stores a record with a derived ID once. It reads first because
// a duplicate-key insert aborts a Postgres transaction.
func insertIfAbsent(ctx context.Context, tx Tx, col string, r Record) error {
	if _, err := tx.Get(ctx, col, r.Scope, r.ID); !errors.Is(err, ErrNotFound) {
		return err
	}
	return tx.Insert(ctx, col, r)
}

func sameScope(left, right Scope) bool {
	return left.WorkspaceID == right.WorkspaceID &&
		left.ProjectID == right.ProjectID &&
		left.UserID == right.UserID &&
		left.AgentID == right.AgentID
}

// Storage enforces the same scope names as the service, so direct callers
// (memoryd recall-baseline) cannot pass a path-like scope either.
func checkScope(scope Scope) error {
	if !validScope(scope) {
		return ErrInvalid
	}

	return nil
}

// retryDelay backs off exponentially and stops at a ceiling, so a failing
// dependency cannot turn into a hot loop across workers.
func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 16 {
		attempts = 16
	}
	delay := min(baseRetryDelay<<(attempts-1), maxRetryDelay)
	// Equal jitter keeps a positive floor and never exceeds the configured cap.
	return delay/2 + time.Duration(mathrand.Int64N(int64(delay-delay/2)+1))
}

func newLeaseToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf[:]), nil
}

// DocumentKeywords is the keyword list to store on a document: the caller's
// keywords, normalized and deduplicated, unioned with the identifiers, error
// codes, file names, model names and call sites found anywhere in the text and
// summary. A caller's list never hides what the text itself names, and a
// document may carry far more keywords than a query may carry tokens.
func DocumentKeywords(text, summary string, provided []string) []string {
	out := make([]string, 0, min(len(provided), maxDocumentKeywords)) // grows for text-found keywords
	seen := make(map[string]bool, cap(out))
	add := func(token string) bool {
		token = strings.TrimSpace(token)
		if token == "" || len(token) > maxKeywordLength {
			return false
		}
		key := strings.ToLower(token)
		if seen[key] {
			return false
		}
		seen[key] = true
		out = append(out, token)

		return len(out) >= maxDocumentKeywords
	}

	for _, keyword := range provided {
		if add(keyword) {
			return out
		}
	}
	// The whole text and summary are scanned, because an identifier can sit on
	// the last line; only the returned list is capped.
	for _, source := range []string{text, summary} {
		for _, raw := range strings.Fields(source) {
			if add(keywordFromToken(raw)) {
				return out
			}
		}
	}

	return out
}

// keywordFromToken returns the keyword a token carries, or "" when the token is
// prose. Identifiers carry a separator, a digit or an inner capital; a call site
// such as foo() keeps its place even when it is lower case.
func keywordFromToken(raw string) string {
	token := strings.Trim(raw, keywordTrim)
	if len(token) < 2 || len(token) > maxKeywordLength {
		return ""
	}

	callSite := strings.HasSuffix(strings.TrimRight(raw, keywordCallTrim), "()")
	var letters, digits, separators, innerCapital, previousLower bool
	for _, r := range token {
		switch {
		case unicode.IsLetter(r):
			letters = true
			if unicode.IsUpper(r) && previousLower {
				innerCapital = true
			}
			previousLower = unicode.IsLower(r)
		case unicode.IsDigit(r):
			digits = true
			previousLower = false
		default:
			separators = true
			previousLower = false
		}
	}
	if !letters || !(digits || separators || innerCapital || callSite) {
		return ""
	}

	return token
}

// tokenize splits a query into whole tokens and keeps identifiers intact:
// "internal/memory/atlas.go", "REQ-054" and "E11000" stay single tokens, while
// surrounding punctuation is trimmed.
func tokenize(query string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, maxKeywordTokens)
	for _, raw := range strings.Fields(query) {
		token := strings.ToLower(strings.Trim(raw, keywordTrim))
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
		if len(out) == maxKeywordTokens {
			break
		}
	}

	return out
}

// Preserve the original word lane and also address a caller-provided whole
// keyword. The extra literal uses the same byte limit and case normalization
// as stored keywords; punctuation and internal whitespace are significant.
func exactQueryKeywords(query string) []string {
	words := tokenize(query)
	whole := strings.TrimSpace(query)
	if whole != "" && len(whole) <= maxKeywordLength {
		whole = strings.ToLower(whole)
		if !slices.Contains(words, whole) {
			words = append(words, whole)
		}
	}
	return words
}

// searchHit is what a search lane returns. HitFromRecord supplies the memory
// origin, the untrusted-data marker and the fields an LLM may see; the lane
// becomes the reason so rank fusion can weight exact keyword matches and the
// caller can tell which index found the hit. Provider scores stop here.
func searchHit(rec Record, collection string, reason string) Hit {
	hit := HitFromRecord(collection, rec)
	hit.Reasons = []string{reason}

	return hit
}

// transientTransactionError marks a Postgres transaction failure (serialization
// or deadlock) that Postgres.Transaction retries. It never carries the raw
// error or server address.
type transientTransactionError struct{}

func (transientTransactionError) Error() string { return ErrUnavailable.Error() }
func (transientTransactionError) Unwrap() error { return ErrUnavailable }

// rlsRefusal marks a row FORCE RLS refused. Every caller still sees ErrUnavailable;
// only the denial audit path reads it as confirming the denial.
type rlsRefusal struct{}

func (rlsRefusal) Error() string { return ErrUnavailable.Error() }
func (rlsRefusal) Unwrap() error { return ErrUnavailable }
