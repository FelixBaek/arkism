package memory

// Session archives: seven-day encrypted originals of multiple agents' sessions.
//
// 저장 모델 (확정 설계):
//   - 두 개의 private collection: session_archives(헤더/메타데이터)와
//     session_archive_chunks(1MiB 평plaintext 청크의 AES-256-GCM 암호문, base64).
//   - 원문(raw bytes)은 암호화된 청크 암호문으로만 존재한다. 청크 업로드 원문은
//     secret 검사도 policy authorize content로도 보내지 않는다(격리 원본).
//   - 검색 노출은 헤더의 safe projection(Text, 최대 64KiB, secret 발견 시 거부)뿐이다.
//   - 실제 사용 확인(use:session_archives)만 기한을 now+7d로 갱신한다. 검색 노출,
//     목록 조회, 재수집(동일 원문)은 기한을 늘리지 않는다. 기존 context_usage
//     검색기록은 절대 사용 근거로 쓰지 않는다.
//   - 만료는 헤더 상태를 expired로 바꾸고 청크 암호문을 지운다. 기억(memories)과
//     sources는 절대 바꾸거나 지우지 않으며, 원본은 SHA-256과 observed 시각으로만 남는다.
//
// 두 컬렉션은 private 경로로만 접근하며 일반 CRUD/기존 source 삭제와 분리한다.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	colSessionArchives      = "session_archives"
	colSessionArchiveChunks = "session_archive_chunks"

	// SessionArchiveTTL is the retention window: originals live seven days and
	// only a verified real use restarts the clock.
	SessionArchiveTTL = 7 * 24 * time.Hour
	// SessionArchiveChunkSize bounds one plaintext chunk.
	SessionArchiveChunkSize = 1 << 20
	// sessionArchiveMaxChunks bounds one header manifest (2 GiB raw ceiling).
	sessionArchiveMaxChunks = 2048
	// sessionArchiveMaxText bounds the safe search projection stored on the header.
	sessionArchiveMaxText = 64 << 10
	// sessionArchiveMaxLimit bounds list and expire page sizes.
	sessionArchiveMaxLimit = 100
	// HTTP body caps: chunk uploads decode at most 2 MiB of JSON, commits 1 MiB.
	sessionArchiveChunkBodyLimit  = 2 << 20
	sessionArchiveCommitBodyLimit = 1 << 20

	kindSessionArchive      = "session_archive"
	kindSessionArchiveChunk = "session_archive_chunk"
	kindSessionArchiveUsage = "session_archive_usage"

	sessionArchiveStateActive  = "active"
	sessionArchiveStateExpired = "expired"
)

// errSessionArchiveCiphertext maps both a malformed and an authenticated-failed
// ciphertext to the existing digest_mismatch contract; it never says which failed.
var errSessionArchiveCiphertext = fmt.Errorf("session_archive_ciphertext: %w", errBlobDigestMismatch)

// SessionArchiveData carries ciphertext and header metadata only. Every field is
// JSON-silent: the raw original and its ciphertext never appear in an API
// response, a log line or a memory record, whatever serializes a Record.
// Chunks use Cipher+ParentID; headers use the metadata fields. Expiry lives on
// Record.ExpiresAt.
type SessionArchiveData struct {
	Cipher     string     `json:"-" bson:"cipher,omitempty"`
	ParentID   string     `json:"-" bson:"parent_id,omitempty"`
	SessionID  string     `json:"-" bson:"session_id,omitempty"`
	Device     string     `json:"-" bson:"device,omitempty"`
	Origin     string     `json:"-" bson:"origin,omitempty"`
	Format     string     `json:"-" bson:"format,omitempty"`
	ObservedAt time.Time  `json:"-" bson:"observed_at,omitempty"`
	LastUsedAt *time.Time `json:"-" bson:"last_used_at,omitempty"`
	Chunks     []string   `json:"-" bson:"chunks,omitempty"`
	SHA256     string     `json:"-" bson:"sha256,omitempty"`
}

// SessionArchiveStore is the optional repository extension for session
// archives. It is deliberately not part of Repository: generic CRUD can never
// reach these collections, only these two scope-fenced queries may.
type SessionArchiveStore interface {
	// SessionArchiveRows returns one page of live headers (status active and
	// expiry in the future) in ascending ID order, optionally after a cursor ID
	// and filtered by a literal substring of the safe projection.
	SessionArchiveRows(ctx context.Context, scope Scope, after string, limit int, q string) ([]Record, error)
	// SessionArchiveExpiredRows returns up to limit active headers whose expiry has passed.
	SessionArchiveExpiredRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error)
	SessionArchiveOrphanRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error)
}

type SessionArchiveChunkInput struct {
	Scope    Scope  `json:"scope"`
	ParentID string `json:"parent_id"`
	SHA256   string `json:"sha256"`
	Data     string `json:"data"`
}

type SessionArchiveChunkOutput struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type SessionArchiveCommitInput struct {
	Scope Scope `json:"scope"`
	// ID is the importer's stable header identity: a hash of device+origin+
	// session+file. The service enforces the 64-hex
	// form; the derivation stays with the importer so appends of one growing
	// file keep targeting the same header.
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Device     string    `json:"device"`
	Origin     string    `json:"origin"`
	Format     string    `json:"format"`
	ObservedAt time.Time `json:"observed_at"`
	SHA256     string    `json:"sha256"`
	Chunks     []string  `json:"chunks"`
	Text       string    `json:"text"`
}

type SessionArchiveCommitOutput struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	ObservedAt time.Time  `json:"observed_at"`
	SHA256     string     `json:"sha256"`
	ChunkCount int        `json:"chunk_count"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Revision   int64      `json:"revision"`
}

type SessionArchiveListInput struct {
	Scope Scope  `json:"scope"`
	After string `json:"after"`
	Limit int    `json:"limit"`
	Query string `json:"q"`
}

type SessionArchiveListOutput struct {
	Items []SessionArchiveView `json:"items"`
	Next  string               `json:"next_cursor,omitempty"`
}

type SessionArchiveReadInput struct {
	Scope   Scope  `json:"scope"`
	ID      string `json:"id"`
	ChunkID string `json:"chunk_id"`
}

type SessionArchiveReadOutput struct {
	ID      string `json:"id"`
	ChunkID string `json:"chunk_id"`
	State   string `json:"state"`
	SHA256  string `json:"sha256,omitempty"`
	Data    string `json:"data,omitempty"`
}

type SessionArchiveGetInput struct {
	Scope Scope  `json:"scope"`
	ID    string `json:"id"`
}

// SessionArchiveView is the only session-archive shape an API response carries:
// metadata, the safe projection and the untrusted-data marker. Raw ciphertext
// is unreachable from here.
type SessionArchiveView struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	SessionID  string     `json:"session_id,omitempty"`
	Device     string     `json:"device,omitempty"`
	Origin     string     `json:"origin,omitempty"`
	Format     string     `json:"format,omitempty"`
	ObservedAt time.Time  `json:"observed_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	SHA256     string     `json:"sha256"`
	ChunkCount int        `json:"chunk_count"`
	Text       string     `json:"text,omitempty"`
	Authority  string     `json:"authority"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Revision   int64      `json:"revision"`
}

type SessionArchiveUsedInput struct {
	Scope    Scope  `json:"scope"`
	ID       string `json:"id"`
	MemoryID string `json:"memory_id"`
	EventID  string `json:"event_id"`
}

type SessionArchiveUsedOutput struct {
	ID        string     `json:"id"`
	MemoryID  string     `json:"memory_id"`
	ReceiptID string     `json:"receipt_id"`
	Extended  bool       `json:"extended"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type SessionArchiveExpireInput struct {
	Scope Scope `json:"scope"`
	Limit int   `json:"limit"`
}

type SessionArchiveExpireOutput struct {
	Expired []string `json:"expired"`
	Orphans int      `json:"orphans"`
}

func uniqueArchiveChunks(chunks []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range chunks {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) sessionArchiveNow() time.Time {
	if s.SessionArchiveNow != nil {
		return s.SessionArchiveNow().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func (s *Service) sessionArchiveAEAD() (cipher.AEAD, error) {
	if len(s.SessionArchiveKey) != 32 {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(s.SessionArchiveKey)
	if err != nil {
		return nil, ErrUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	return gcm, nil
}

// sessionArchiveAAD binds every ciphertext to one scope, one header and one
// chunk digest, so bytes cannot move between tenants, headers or slots.
func sessionArchiveAAD(scope Scope, parentID, chunkSHA string) []byte {
	return []byte("arkism/session-archive/v1\x00" + structuredText(scope) + "\x00" + parentID + "\x00" + chunkSHA)
}

func (s *Service) sealSessionArchiveChunk(scope Scope, parentID, chunkSHA string, plain []byte) (string, error) {
	gcm, err := s.sessionArchiveAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", ErrUnavailable
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, sessionArchiveAAD(scope, parentID, chunkSHA))), nil
}

func (s *Service) openSessionArchiveChunk(scope Scope, parentID, chunkSHA, sealed string) ([]byte, error) {
	gcm, err := s.sessionArchiveAEAD()
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < gcm.NonceSize() {
		return nil, errSessionArchiveCiphertext
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, sessionArchiveAAD(scope, parentID, chunkSHA))
	if err != nil {
		return nil, errSessionArchiveCiphertext
	}
	return plain, nil
}

// sessionArchiveChunkID derives the content address of one chunk: identical
// bytes inside one header and scope are stored once (idempotent uploads).
func sessionArchiveChunkID(scope Scope, parentID, chunkSHA string) string {
	return ContentHash(structuredText(scope) + "\n" + parentID + "\n" + chunkSHA)
}

func sessionArchiveReceiptID(scope Scope, principal, eventID, headerID, memoryID string) string {
	return ContentHash(strings.Join([]string{structuredText(scope), principal, eventID, headerID, memoryID}, "\n"))
}

func validSessionArchiveLabels(in SessionArchiveCommitInput) bool {
	return validRunLabel(in.SessionID, 256) && validRunLabel(in.Device, 256) && validRunLabel(in.Origin, 256) && validRunLabel(in.Format, 128)
}

func sessionArchiveStateOf(r Record, now time.Time) string {
	if r.PrivacyQuarantined || r.ContentErased || r.Status != statusActive || r.ExpiresAt == nil || !r.ExpiresAt.After(now) {
		return sessionArchiveStateExpired
	}
	return sessionArchiveStateActive
}

func sessionArchiveDueForExpiry(r Record, now time.Time) bool {
	return r.Status == statusActive && r.ExpiresAt != nil && !r.ExpiresAt.After(now)
}

func isSessionArchiveHeader(r Record) bool {
	return r.SessionArchive != nil && r.SessionArchive.ParentID == "" && r.SessionArchive.SHA256 != ""
}

func sessionArchiveView(r Record, state string) SessionArchiveView {
	d := r.SessionArchive
	text := r.Text
	if state != sessionArchiveStateActive {
		text = ""
	}
	return SessionArchiveView{
		ID: r.ID, State: state,
		SessionID: d.SessionID, Device: d.Device, Origin: d.Origin, Format: d.Format,
		ObservedAt: d.ObservedAt, LastUsedAt: d.LastUsedAt, SHA256: d.SHA256,
		ChunkCount: len(d.Chunks), Text: text, Authority: AuthorityDataOnly,
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision,
	}
}

// UploadSessionArchiveChunk stores one encrypted chunk. Identical bytes inside
// one header and scope are idempotent. The plaintext is never secret-checked
// and never sent to policy: it exists only as ciphertext at rest.
func (s *Service) UploadSessionArchiveChunk(ctx context.Context, token string, in SessionArchiveChunkInput) (SessionArchiveChunkOutput, error) {
	if s.Repo == nil {
		return SessionArchiveChunkOutput{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !sourceHashPattern.MatchString(in.ParentID) || !sourceHashPattern.MatchString(in.SHA256) {
		return SessionArchiveChunkOutput{}, ErrInvalid
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) == 0 || len(data) > SessionArchiveChunkSize {
		return SessionArchiveChunkOutput{}, ErrInvalid
	}
	if ContentHash(string(data)) != in.SHA256 {
		return SessionArchiveChunkOutput{}, errBlobDigestMismatch
	}
	d, e := s.authorize(ctx, token, "write:session_archives", in.Scope, in.ParentID, "")
	if e != nil {
		return SessionArchiveChunkOutput{}, e
	}
	if d.Role != "human" && d.Role != "service" {
		return SessionArchiveChunkOutput{}, ErrDenied
	}
	sealed, e := s.sealSessionArchiveChunk(in.Scope, in.ParentID, in.SHA256, data)
	if e != nil {
		return SessionArchiveChunkOutput{}, e
	}
	now := s.sessionArchiveNow()
	id := sessionArchiveChunkID(in.Scope, in.ParentID, in.SHA256)
	orphanExpiry := now.Add(SessionArchiveTTL)
	e = s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		if existing, err := tx.Get(ctx, colSessionArchiveChunks, in.Scope, id); err == nil {
			// Same bytes uploaded again: one record, same answer.
			if existing.ContentHash != in.SHA256 || !isSessionArchiveChunkFor(existing, in.ParentID) {
				return ErrConflict
			}
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := tx.Insert(ctx, colSessionArchiveChunks, Record{
			ID: id, Scope: in.Scope, Kind: kindSessionArchiveChunk, Status: statusActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			ContentHash: in.SHA256, ExpiresAt: &orphanExpiry,
			SessionArchive: &SessionArchiveData{Cipher: sealed, ParentID: in.ParentID},
		}); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colSessionArchiveChunks, id)
	})
	if e != nil {
		return SessionArchiveChunkOutput{}, e
	}
	return SessionArchiveChunkOutput{ID: id, SHA256: in.SHA256}, nil
}

func isSessionArchiveChunkFor(r Record, headerID string) bool {
	return r.SessionArchive != nil && r.SessionArchive.ParentID == headerID && r.SessionArchive.Cipher != ""
}

// verifySessionArchiveChunks checks every manifest chunk against this header
// and scope, decrypts the ordered concatenation and confirms the raw SHA-256.
// The plaintext exists only inside this call: it is never logged and never
// sent to policy or authorize.
func (s *Service) verifySessionArchiveChunks(ctx context.Context, tx Tx, scope Scope, headerID string, chunks []string, expectedSHA string) ([]string, error) {
	digest := sha256.New()
	var size int
	fenced := map[string]bool{}
	for _, id := range chunks {
		chunk, err := tx.Get(ctx, colSessionArchiveChunks, scope, id)
		if err != nil {
			return nil, err
		}
		if !isSessionArchiveChunkFor(chunk, headerID) || chunk.ContentHash == "" {
			return nil, ErrConflict
		}
		if sessionArchiveChunkID(scope, headerID, chunk.ContentHash) != id {
			return nil, ErrConflict
		}
		part, err := s.openSessionArchiveChunk(scope, headerID, chunk.ContentHash, chunk.SessionArchive.Cipher)
		if err != nil {
			return nil, err
		}
		size += len(part)
		if size > sessionArchiveMaxChunks*SessionArchiveChunkSize {
			return nil, ErrInvalid
		}
		_, _ = digest.Write(part)
		if !fenced[id] {
			previous := chunk.Revision
			chunk.Revision++
			if err := tx.Replace(ctx, colSessionArchiveChunks, chunk, previous); err != nil {
				return nil, err
			}
			fenced[id] = true
		}
	}
	if hex.EncodeToString(digest.Sum(nil)) != expectedSHA {
		return nil, errBlobDigestMismatch
	}
	return chunks, nil
}

// CommitSessionArchive writes or advances one header inside one transaction:
// revision CAS, manifest verification against the encrypted chunks, and audit.
// Re-collection of identical bytes never extends the TTL and never resurrects
// an expired header; a new observed time with new bytes appends while the old
// raw is still available.
func (s *Service) CommitSessionArchive(ctx context.Context, token string, in SessionArchiveCommitInput) (SessionArchiveCommitOutput, error) {
	if s.Repo == nil {
		return SessionArchiveCommitOutput{}, ErrUnavailable
	}
	now := s.sessionArchiveNow()
	if !validScope(in.Scope) || !sourceHashPattern.MatchString(in.ID) || !sourceHashPattern.MatchString(in.SHA256) || !validSessionArchiveLabels(in) {
		return SessionArchiveCommitOutput{}, ErrInvalid
	}
	if containsSecret(in.SessionID + "\n" + in.Device + "\n" + in.Origin + "\n" + in.Format) {
		return SessionArchiveCommitOutput{}, ErrSecretDetected
	}
	if in.ObservedAt.IsZero() || in.ObservedAt.After(now.Add(time.Minute)) || !storableTime(in.ObservedAt.UTC()) {
		return SessionArchiveCommitOutput{}, ErrInvalid
	}
	if len(in.Chunks) == 0 || len(in.Chunks) > sessionArchiveMaxChunks || len(in.Text) > sessionArchiveMaxText {
		return SessionArchiveCommitOutput{}, ErrInvalid
	}
	for _, c := range in.Chunks {
		if !sourceHashPattern.MatchString(c) {
			return SessionArchiveCommitOutput{}, ErrInvalid
		}
	}
	if containsSecret(in.Text) {
		return SessionArchiveCommitOutput{}, ErrSecretDetected
	}
	d, e := s.authorize(ctx, token, "write:"+colSessionArchives, in.Scope, in.ID, structuredText(in))
	if e != nil {
		return SessionArchiveCommitOutput{}, e
	}
	if d.Role != "human" && d.Role != "service" {
		return SessionArchiveCommitOutput{}, ErrDenied
	}
	observed := in.ObservedAt.UTC().Truncate(time.Millisecond)
	var out SessionArchiveCommitOutput
	e = s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		out = SessionArchiveCommitOutput{}
		header, err := tx.Get(ctx, colSessionArchives, in.Scope, in.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			manifest, verr := s.verifySessionArchiveChunks(ctx, tx, in.Scope, in.ID, in.Chunks, in.SHA256)
			if verr != nil {
				return verr
			}
			expires := observed.Add(SessionArchiveTTL)
			created := Record{
				ID: in.ID, Scope: in.Scope, Kind: kindSessionArchive, Status: statusActive, Revision: 1,
				Text: in.Text, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
				ExpiresAt: &expires,
				SessionArchive: &SessionArchiveData{
					SessionID: in.SessionID, Device: in.Device, Origin: in.Origin, Format: in.Format,
					ObservedAt: observed, Chunks: manifest, SHA256: in.SHA256,
				},
			}
			if err := tx.Insert(ctx, colSessionArchives, created); err != nil {
				return err
			}
			if err := audit(ctx, tx, in.Scope, d.PrincipalID, "create:"+colSessionArchives, in.ID); err != nil {
				return err
			}
			out = sessionArchiveCommitOutput(created, sessionArchiveStateActive)
			return nil
		case err != nil:
			return err
		}
		if !isSessionArchiveHeader(header) {
			return ErrConflict
		}
		if header.ContentErased {
			// Deleted by the user (세션 지우기): later collections of the file never revive it.
			out = sessionArchiveCommitOutput(header, sessionArchiveStateExpired)
			return nil
		}
		old := header.SessionArchive
		if old.SessionID != in.SessionID || old.Device != in.Device || old.Origin != in.Origin || old.Format != in.Format {
			return ErrConflict
		}
		if in.SHA256 == old.SHA256 {
			if sessionArchiveStateOf(header, now) != sessionArchiveStateActive {
				out = sessionArchiveCommitOutput(header, sessionArchiveStateExpired)
				return nil
			}
			// Re-collection of identical bytes: the manifest must still check out,
			// but the TTL never moves and an expired header stays expired.
			if _, verr := s.verifySessionArchiveChunks(ctx, tx, in.Scope, in.ID, in.Chunks, in.SHA256); verr != nil {
				return verr
			}
			out = sessionArchiveCommitOutput(header, sessionArchiveStateOf(header, now))
			return nil
		}
		if !observed.After(old.ObservedAt) {
			return ErrConflict
		}
		manifest, verr := s.verifySessionArchiveChunks(ctx, tx, in.Scope, in.ID, in.Chunks, in.SHA256)
		if verr != nil {
			return verr
		}
		base := observed
		if old.LastUsedAt != nil && old.LastUsedAt.After(base) {
			base = *old.LastUsedAt
		}
		expires := base.Add(SessionArchiveTTL)
		previous := header.Revision
		header.Revision++
		header.UpdatedAt = now
		header.ExpiresAt = &expires
		header.Text = in.Text
		header.Status = statusActive
		header.SessionArchive = &SessionArchiveData{
			SessionID: old.SessionID, Device: old.Device, Origin: old.Origin, Format: old.Format,
			ObservedAt: observed, LastUsedAt: old.LastUsedAt, Chunks: manifest, SHA256: in.SHA256,
		}
		if err := tx.Replace(ctx, colSessionArchives, header, previous); err != nil {
			return err
		}
		// Chunks the new manifest no longer references lose their ciphertext.
		// Other headers are unaffected: the header ID is part of every chunk ID.
		kept := make(map[string]bool, len(manifest))
		for _, c := range manifest {
			kept[c] = true
		}
		for _, c := range uniqueArchiveChunks(old.Chunks) {
			if kept[c] {
				continue
			}
			chunk, err := tx.Get(ctx, colSessionArchiveChunks, in.Scope, c)
			if err != nil {
				return err
			}
			if !isSessionArchiveChunkFor(chunk, in.ID) {
				return ErrConflict
			}
			if err := tx.Delete(ctx, colSessionArchiveChunks, in.Scope, c, chunk.Revision); err != nil {
				return err
			}
		}
		if err := audit(ctx, tx, in.Scope, d.PrincipalID, colSessionArchives+":append", in.ID); err != nil {
			return err
		}
		out = sessionArchiveCommitOutput(header, sessionArchiveStateActive)
		return nil
	})
	if e != nil {
		return SessionArchiveCommitOutput{}, e
	}
	return out, nil
}

func sessionArchiveCommitOutput(r Record, state string) SessionArchiveCommitOutput {
	return SessionArchiveCommitOutput{
		ID: r.ID, State: state, ObservedAt: r.SessionArchive.ObservedAt, SHA256: r.SessionArchive.SHA256,
		ChunkCount: len(r.SessionArchive.Chunks), ExpiresAt: r.ExpiresAt, Revision: r.Revision,
	}
}

// ListSessionArchives returns live headers as metadata plus the safe
// projection. Scope fencing happens in the repository; each row is
// re-authorized for the bound request actor.
func (s *Service) ListSessionArchives(ctx context.Context, token string, in SessionArchiveListInput) (SessionArchiveListOutput, error) {
	if s.Repo == nil {
		return SessionArchiveListOutput{}, ErrUnavailable
	}
	if !validScope(in.Scope) || in.Limit < 1 || in.Limit > sessionArchiveMaxLimit || len(in.Query) > 256 {
		return SessionArchiveListOutput{}, ErrInvalid
	}
	if in.After != "" && !sourceHashPattern.MatchString(in.After) {
		return SessionArchiveListOutput{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:"+colSessionArchives, in.Scope, "", "")
	if e != nil {
		return SessionArchiveListOutput{}, e
	}
	store, ok := s.Repo.(SessionArchiveStore)
	if !ok {
		return SessionArchiveListOutput{}, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, d)
	rows, e := store.SessionArchiveRows(ctx, in.Scope, in.After, in.Limit+1, in.Query)
	if e != nil {
		return SessionArchiveListOutput{}, e
	}
	now := s.sessionArchiveNow()
	out := SessionArchiveListOutput{Items: []SessionArchiveView{}}
	for _, r := range rows {
		if !isSessionArchiveHeader(r) || !sameScope(r.Scope, in.Scope) {
			return SessionArchiveListOutput{}, ErrUnavailable
		}
		// Defense in depth: the query already filters, the service re-checks with
		// its own clock so expiry boundaries stay exact.
		if sessionArchiveStateOf(r, now) != sessionArchiveStateActive {
			continue
		}
		row, e := s.authorize(ctx, token, "read:"+colSessionArchives, in.Scope, r.ID, "")
		if e != nil {
			return SessionArchiveListOutput{}, e
		}
		if row.PrincipalID != d.PrincipalID || row.Role != d.Role {
			return SessionArchiveListOutput{}, ErrDenied
		}
		out.Items = append(out.Items, sessionArchiveView(r, sessionArchiveStateActive))
		if len(out.Items) == in.Limit {
			break
		}
	}
	if len(rows) > in.Limit {
		if n := len(out.Items); n > 0 {
			out.Next = out.Items[n-1].ID
		} else {
			out.Next = rows[len(rows)-1].ID
		}
	}
	return out, nil
}

// ReadSessionArchiveChunk decrypts one manifest chunk for a human or service
// principal. Agents never see raw bytes. An expired header answers with
// state=expired and no body.
func (s *Service) ReadSessionArchiveChunk(ctx context.Context, token string, in SessionArchiveReadInput) (SessionArchiveReadOutput, error) {
	if s.Repo == nil {
		return SessionArchiveReadOutput{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !sourceHashPattern.MatchString(in.ID) || !sourceHashPattern.MatchString(in.ChunkID) {
		return SessionArchiveReadOutput{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:session_archive_raw", in.Scope, in.ID, "")
	if e != nil {
		return SessionArchiveReadOutput{}, e
	}
	if d.Role != "human" && d.Role != "service" {
		return SessionArchiveReadOutput{}, ErrDenied
	}
	header, e := s.Repo.Get(ctx, colSessionArchives, in.Scope, in.ID)
	if e != nil {
		return SessionArchiveReadOutput{}, e
	}
	if !isSessionArchiveHeader(header) {
		return SessionArchiveReadOutput{}, ErrConflict
	}
	if sessionArchiveStateOf(header, s.sessionArchiveNow()) != sessionArchiveStateActive {
		return SessionArchiveReadOutput{ID: in.ID, ChunkID: in.ChunkID, State: sessionArchiveStateExpired}, nil
	}
	if !slices.Contains(header.SessionArchive.Chunks, in.ChunkID) {
		return SessionArchiveReadOutput{}, ErrNotFound
	}
	chunk, e := s.Repo.Get(ctx, colSessionArchiveChunks, in.Scope, in.ChunkID)
	if e != nil {
		return SessionArchiveReadOutput{}, e
	}
	if !isSessionArchiveChunkFor(chunk, in.ID) || chunk.ContentHash == "" {
		return SessionArchiveReadOutput{}, ErrConflict
	}
	plain, e := s.openSessionArchiveChunk(in.Scope, in.ID, chunk.ContentHash, chunk.SessionArchive.Cipher)
	if e != nil {
		return SessionArchiveReadOutput{}, e
	}
	if e := s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.Get(ctx, colSessionArchives, in.Scope, in.ID)
		if err != nil {
			return err
		}
		if current.Revision != header.Revision || sessionArchiveStateOf(current, s.sessionArchiveNow()) != sessionArchiveStateActive {
			return ErrConflict
		}
		// A no-op revision fence serializes the read with expiry/commit.
		if err := tx.Replace(ctx, colSessionArchives, current, current.Revision); err != nil {
			return err
		}
		return audit(ctx, tx, in.Scope, d.PrincipalID, "read:session_archive_raw", in.ID)
	}); e != nil {
		return SessionArchiveReadOutput{}, e
	}
	return SessionArchiveReadOutput{
		ID: in.ID, ChunkID: in.ChunkID, State: sessionArchiveStateActive,
		SHA256: chunk.ContentHash, Data: base64.StdEncoding.EncodeToString(plain),
	}, nil
}

// GetSessionArchive returns metadata only; expired headers still answer, with
// state=expired and an empty projection.
func (s *Service) GetSessionArchive(ctx context.Context, token string, in SessionArchiveGetInput) (SessionArchiveView, error) {
	if s.Repo == nil {
		return SessionArchiveView{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !sourceHashPattern.MatchString(in.ID) {
		return SessionArchiveView{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:"+colSessionArchives, in.Scope, in.ID, "")
	if e != nil {
		return SessionArchiveView{}, e
	}
	r, e := s.Repo.Get(ctx, colSessionArchives, in.Scope, in.ID)
	if e != nil {
		return SessionArchiveView{}, e
	}
	if !isSessionArchiveHeader(r) {
		return SessionArchiveView{}, ErrConflict
	}
	state := sessionArchiveStateOf(r, s.sessionArchiveNow())
	if e := s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, d.PrincipalID, "read:"+colSessionArchives, in.ID)
	}); e != nil {
		return SessionArchiveView{}, e
	}
	return sessionArchiveView(r, state), nil
}

// SessionArchiveUsed records a verified real use and renews the retention to
// now+7d in the same transaction as the receipt. Search exposure never reaches
// here; the existing context_usage search records are never consulted.
func (s *Service) SessionArchiveUsed(ctx context.Context, token string, in SessionArchiveUsedInput) (SessionArchiveUsedOutput, error) {
	if s.Repo == nil {
		return SessionArchiveUsedOutput{}, ErrUnavailable
	}
	if !validScope(in.Scope) || !sourceHashPattern.MatchString(in.ID) || !validRunLabel(in.MemoryID, 256) || !validRunLabel(in.EventID, 128) {
		return SessionArchiveUsedOutput{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "use:"+colSessionArchives, in.Scope, in.ID, "")
	if e != nil {
		return SessionArchiveUsedOutput{}, e
	}
	if d.Role != "human" && d.Role != "service" && d.Role != "agent" {
		return SessionArchiveUsedOutput{}, ErrDenied
	}
	now := s.sessionArchiveNow()
	header, e := s.Repo.Get(ctx, colSessionArchives, in.Scope, in.ID)
	if e != nil {
		return SessionArchiveUsedOutput{}, e
	}
	if !isSessionArchiveHeader(header) {
		return SessionArchiveUsedOutput{}, ErrConflict
	}
	// An expired or already-expired raw is never revived by a late use report.
	if sessionArchiveStateOf(header, now) != sessionArchiveStateActive {
		return SessionArchiveUsedOutput{}, ErrConflict
	}
	memory, e := s.Repo.Get(ctx, colMemories, in.Scope, in.MemoryID)
	if e != nil {
		return SessionArchiveUsedOutput{}, e
	}
	// The use reporter must be the same principal that may read the memory.
	md, e := s.authorize(ctx, token, "read:"+colMemories, in.Scope, in.MemoryID, "")
	if e != nil {
		return SessionArchiveUsedOutput{}, e
	}
	if md.PrincipalID != d.PrincipalID || md.Role != d.Role {
		return SessionArchiveUsedOutput{}, ErrDenied
	}
	if !liveAt(memory, now) || !slices.Contains(memory.Tags, "session:"+header.SessionArchive.SessionID) {
		return SessionArchiveUsedOutput{}, ErrDenied
	}
	var out SessionArchiveUsedOutput
	e = s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		out = SessionArchiveUsedOutput{}
		current, err := tx.Get(ctx, colSessionArchives, in.Scope, in.ID)
		if err != nil {
			return err
		}
		if !isSessionArchiveHeader(current) || current.SessionArchive.SHA256 != header.SessionArchive.SHA256 {
			return ErrConflict
		}
		if sessionArchiveStateOf(current, now) != sessionArchiveStateActive {
			return ErrConflict
		}
		memoryNow, err := tx.Get(ctx, colMemories, in.Scope, in.MemoryID)
		if err != nil {
			return err
		}
		// Hold both revisions through receipt commit. A memory read alone under
		// Read Committed would let a concurrent quarantine invalidate this use.
		if err := tx.Replace(ctx, colMemories, memoryNow, memoryNow.Revision); err != nil {
			return err
		}
		if err := tx.Replace(ctx, colSessionArchives, current, current.Revision); err != nil {
			return err
		}
		for _, check := range []struct{ action, id string }{{"use:" + colSessionArchives, in.ID}, {"read:" + colMemories, in.MemoryID}} {
			who, err := s.authorize(ctx, token, check.action, in.Scope, check.id, "")
			if err != nil {
				return err
			}
			if who.PrincipalID != d.PrincipalID || who.Role != d.Role {
				return ErrDenied
			}
		}
		// Locks/policy calls may have waited across an expiry boundary. The
		// outer request timestamp must never resurrect an expired original.
		now := s.sessionArchiveNow()
		if sessionArchiveStateOf(current, now) != sessionArchiveStateActive {
			return ErrConflict
		}
		if !liveAt(memoryNow, now) || !slices.Contains(memoryNow.Tags, "session:"+current.SessionArchive.SessionID) {
			return ErrDenied
		}
		receiptID := sessionArchiveReceiptID(in.Scope, d.PrincipalID, in.EventID, in.ID, in.MemoryID)
		if _, err := tx.Get(ctx, colJobs, in.Scope, receiptID); err == nil {
			// Replay of the same receipt: already counted, never extends.
			out = SessionArchiveUsedOutput{ID: in.ID, MemoryID: in.MemoryID, ReceiptID: receiptID, Extended: false, ExpiresAt: current.ExpiresAt}
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		expires := now.Add(SessionArchiveTTL)
		if current.ExpiresAt != nil && current.ExpiresAt.After(expires) {
			expires = *current.ExpiresAt
		}
		lastUsed := now
		previous := current.Revision
		current.Revision++
		current.UpdatedAt = now
		current.ExpiresAt = &expires
		metadata := *current.SessionArchive
		current.SessionArchive = &metadata
		current.SessionArchive.LastUsedAt = &lastUsed
		if err := tx.Replace(ctx, colSessionArchives, current, previous); err != nil {
			return err
		}
		if err := tx.Insert(ctx, colJobs, Record{
			ID: receiptID, Scope: in.Scope, Kind: kindSessionArchiveUsage, Status: JobStatusDone, Revision: 1,
			CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
			Target: &Ref{Collection: colMemories, ID: in.MemoryID, Revision: memoryNow.Revision},
		}); err != nil {
			return err
		}
		if err := audit(ctx, tx, in.Scope, d.PrincipalID, "use:"+colSessionArchives, in.ID); err != nil {
			return err
		}
		out = SessionArchiveUsedOutput{ID: in.ID, MemoryID: in.MemoryID, ReceiptID: receiptID, Extended: true, ExpiresAt: &expires}
		return nil
	})
	if e != nil {
		return SessionArchiveUsedOutput{}, e
	}
	return out, nil
}

// ExpireSessionArchives expires due headers: manifest chunk ciphertexts are
// deleted, the header keeps only the raw's hash and observed time, and
// memories/sources are untouched. Concurrent use renewals are protected by the
// revision CAS inside the transaction.
func (s *Service) ExpireSessionArchives(ctx context.Context, token string, in SessionArchiveExpireInput) (SessionArchiveExpireOutput, error) {
	if s.Repo == nil {
		return SessionArchiveExpireOutput{}, ErrUnavailable
	}
	if in.Limit == 0 {
		in.Limit = sessionArchiveMaxLimit
	}
	if !validScope(in.Scope) || in.Limit < 1 || in.Limit > sessionArchiveMaxLimit {
		return SessionArchiveExpireOutput{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "expire:"+colSessionArchives, in.Scope, "", "")
	if e != nil {
		return SessionArchiveExpireOutput{}, e
	}
	if actor.Role != "human" && actor.Role != "service" {
		return SessionArchiveExpireOutput{}, ErrDenied
	}
	store, ok := s.Repo.(SessionArchiveStore)
	if !ok {
		return SessionArchiveExpireOutput{}, ErrUnavailable
	}
	now := s.sessionArchiveNow()
	rows, e := store.SessionArchiveExpiredRows(ctx, in.Scope, now, in.Limit)
	if e != nil {
		return SessionArchiveExpireOutput{}, e
	}
	expire := s.itemAuthorizer(ctx, token, "expire:"+colSessionArchives, in.Scope)
	for _, r := range rows {
		if r.Scope != in.Scope || !isSessionArchiveHeader(r) || !sessionArchiveDueForExpiry(r, now) {
			return SessionArchiveExpireOutput{}, ErrUnavailable
		}
		d, e := expire(r.ID, "")
		if e != nil {
			return SessionArchiveExpireOutput{}, e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return SessionArchiveExpireOutput{}, ErrDenied
		}
	}
	out := SessionArchiveExpireOutput{Expired: []string{}}
	e = s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Expired = []string{}
		for _, row := range rows {
			r, err := tx.Get(ctx, colSessionArchives, in.Scope, row.ID)
			if err != nil {
				return err
			}
			// A use renewal that landed after the query changed the revision: this
			// header is no longer ours to expire.
			if r.Revision != row.Revision || !sessionArchiveDueForExpiry(r, now) {
				return ErrConflict
			}
			for _, chunkID := range uniqueArchiveChunks(r.SessionArchive.Chunks) {
				chunk, err := tx.Get(ctx, colSessionArchiveChunks, in.Scope, chunkID)
				if err != nil {
					return err
				}
				if !isSessionArchiveChunkFor(chunk, r.ID) {
					return ErrConflict
				}
				if err := tx.Delete(ctx, colSessionArchiveChunks, in.Scope, chunkID, chunk.Revision); err != nil {
					return err
				}
			}
			previous := r.Revision
			r.Revision++
			r.UpdatedAt = now
			r.Status = sessionArchiveStateExpired
			metadata := *r.SessionArchive
			r.SessionArchive = &metadata
			r.SessionArchive.Chunks = nil
			r.Text = ""
			if err := tx.Replace(ctx, colSessionArchives, r, previous); err != nil {
				return err
			}
			if err := audit(ctx, tx, in.Scope, actor.PrincipalID, "expire:"+colSessionArchives, r.ID); err != nil {
				return err
			}
			out.Expired = append(out.Expired, r.ID)
		}
		return nil
	})
	if e != nil {
		return SessionArchiveExpireOutput{}, e
	}
	orphans, e := store.SessionArchiveOrphanRows(ctx, in.Scope, now, in.Limit)
	if e != nil {
		return out, e
	}
	for _, orphan := range orphans {
		if orphan.Scope != in.Scope || orphan.SessionArchive == nil {
			return out, ErrUnavailable
		}
		d, err := s.authorize(ctx, token, "expire:"+colSessionArchives, in.Scope, orphan.SessionArchive.ParentID, "")
		if err != nil {
			return out, err
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return out, ErrDenied
		}
		err = s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
			chunk, err := tx.Get(ctx, colSessionArchiveChunks, in.Scope, orphan.ID)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if chunk.Revision != orphan.Revision {
				return ErrConflict
			}
			header, err := tx.Get(ctx, colSessionArchives, in.Scope, chunk.SessionArchive.ParentID)
			if err == nil && header.SessionArchive != nil && slices.Contains(header.SessionArchive.Chunks, chunk.ID) {
				return nil
			}
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if err := tx.Delete(ctx, colSessionArchiveChunks, in.Scope, chunk.ID, chunk.Revision); err != nil {
				return err
			}
			return audit(ctx, tx, in.Scope, actor.PrincipalID, "expire:session_archive_orphan", chunk.ID)
		})
		if err != nil {
			return out, err
		}
		out.Orphans++
	}
	return out, nil
}

func (p *Postgres) SessionArchiveOrphanRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error) {
	if checkScope(scope) != nil || limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	w := `(doc->'expires_at'->>'$date')::timestamptz <= $6 AND NOT EXISTS (
SELECT 1 FROM memory_records h WHERE h.collection='session_archives'
AND h.workspace_id=memory_records.workspace_id AND h.project_id=memory_records.project_id
AND h.user_id=memory_records.user_id AND h.agent_id=memory_records.agent_id
AND h.id=memory_records.doc->'session_archive'->>'parent_id'
AND (h.doc->'session_archive'->'chunks') ? memory_records.id)`
	return p.selectRecords(ctx, colSessionArchiveChunks, scope, w, []any{now}, "id ASC", limit)
}

// Postgres implementations of the optional session-archive queries. Both reuse
// selectRecords, so the tenant fence stays in exactly one place.

var _ SessionArchiveStore = (*Postgres)(nil)

func (p *Postgres) SessionArchiveRows(ctx context.Context, scope Scope, after string, limit int, q string) ([]Record, error) {
	if checkScope(scope) != nil || limit < 1 || limit > sessionArchiveMaxLimit+1 || len(q) > 256 || after != "" && !sourceHashPattern.MatchString(after) {
		return nil, ErrInvalid
	}
	w := `status='active' AND (doc->'expires_at'->>'$date')::timestamptz > now()`
	args := []any{}
	if q != "" {
		w += ` AND strpos(COALESCE(doc->>'text',''),$6)>0`
		args = append(args, q)
	}
	if after != "" {
		w += fmt.Sprintf(` AND id>$%d`, len(args)+6)
		args = append(args, after)
	}
	return p.selectRecords(ctx, colSessionArchives, scope, w, args, "id ASC", limit)
}

func (p *Postgres) SessionArchiveExpiredRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error) {
	if checkScope(scope) != nil || now.IsZero() || limit < 1 || limit > sessionArchiveMaxLimit {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colSessionArchives, scope, `status='active' AND (doc->'expires_at'->>'$date')::timestamptz <= $6`, []any{now}, "id ASC", limit)
}

// sessionArchiveRoutes registers the six POST JSON endpoints. Chunk uploads
// decode with the dedicated 2 MiB cap; commits 1 MiB (2048 chunk IDs plus a
// 64 KiB escaped projection fit); everything else uses the default decode cap.
func sessionArchiveRoutes(mux routeRegistrar, s *Service) {
	sessionArchiveSearchRoutes(mux, s)
	mux.HandleFunc("POST /v1/session-archives/chunks", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveChunkInput
		if !decodeLimit(w, r, &in, sessionArchiveChunkBodyLimit) {
			return
		}
		out, e := s.UploadSessionArchiveChunk(r.Context(), bearer(r), in)
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("POST /v1/session-archives/commit", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveCommitInput
		if !decodeLimit(w, r, &in, sessionArchiveCommitBodyLimit) {
			return
		}
		out, e := s.CommitSessionArchive(r.Context(), bearer(r), in)
		if e != nil {
			respond(w, nil, e)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	})
	mux.HandleFunc("POST /v1/session-archives/list", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveListInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ListSessionArchives(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/session-archives/read", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveReadInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReadSessionArchiveChunk(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/session-archives/get", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveGetInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.GetSessionArchive(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/session-archives/used", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveUsedInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SessionArchiveUsed(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/session-archives/expire", func(w http.ResponseWriter, r *http.Request) {
		var in SessionArchiveExpireInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ExpireSessionArchives(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

func (s *Service) sessionArchiveTransaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if repo, ok := s.Repo.(interface {
		SessionArchiveTransaction(context.Context, func(context.Context, Tx) error) error
	}); ok {
		return repo.SessionArchiveTransaction(ctx, fn)
	}
	return s.Repo.Transaction(ctx, fn)
}
