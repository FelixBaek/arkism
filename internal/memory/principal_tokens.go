package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const MaxPrincipalTokenTTL = 90 * 24 * time.Hour

// NewPrincipalToken generates 256 random bits, encoded without padding. Token
// validation can reject obvious weak inputs; it cannot prove how a caller
// generated a supplied secret. Use this generator rather than a password.
func NewPrincipalToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if !ValidPrincipalToken(token) {
		return "", ErrUnavailable
	}
	return token, nil
}

// ValidPrincipalToken accepts 32-byte secrets in canonical base64url or hex.
// Repeated-byte and short-alphabet inputs are rejected as obvious weak tokens.
func ValidPrincipalToken(token string) bool {
	var raw []byte
	var err error
	switch len(token) {
	case 43:
		raw, err = base64.RawURLEncoding.Strict().DecodeString(token)
		if err == nil && base64.RawURLEncoding.EncodeToString(raw) != token {
			return false
		}
	case 64:
		raw, err = hex.DecodeString(token)
	default:
		return false
	}
	if err != nil || len(raw) != 32 {
		return false
	}
	seen := map[byte]bool{}
	for _, b := range raw {
		seen[b] = true
	}
	return len(seen) >= 16
}

func principalTokenTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		ttl = MaxPrincipalTokenTTL
	}
	if ttl < time.Minute || ttl > MaxPrincipalTokenTTL {
		return 0, ErrInvalid
	}
	return ttl, nil
}

func lockPrincipal(ctx context.Context, q pgQuery, id string) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('arkism.principal:' || $1,0))`, id)
	return err
}

func insertPrincipalToken(ctx context.Context, q pgQuery, id string, digest, previous []byte, ttl time.Duration) error {
	_, err := q.Exec(ctx, `INSERT INTO principal_token_history(token_sha256,principal_id,issued_at,expires_at,rotated_from)
VALUES($1,$2,statement_timestamp(),statement_timestamp()+$3::double precision*interval '1 second',$4)`, digest, id, ttl.Seconds(), previous)
	return pgErr(err)
}

// provisionPrincipalToken never silently rotates or extends an existing token.
// Updating a project's grants cannot prolong an expired credential.
func provisionPrincipalToken(ctx context.Context, q pgQuery, grant PrincipalGrant, digest []byte, ttl time.Duration) error {
	if err := lockPrincipal(ctx, q, grant.PrincipalID); err != nil {
		return err
	}
	var old []byte
	err := q.QueryRow(ctx, `SELECT token_sha256 FROM principals WHERE id=$1 FOR UPDATE`, grant.PrincipalID).Scan(&old)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if !bytes.Equal(old, digest) {
			return ErrConflict
		}
		_, err = q.Exec(ctx, `UPDATE principals SET role=$2,active=true WHERE id=$1`, grant.PrincipalID, grant.Role)
		return pgErr(err)
	}
	if _, err = q.Exec(ctx, `INSERT INTO principals(id,role,token_sha256,active) VALUES($1,$2,$3,true)`, grant.PrincipalID, grant.Role, digest); err != nil {
		return pgErr(err)
	}
	return insertPrincipalToken(ctx, q, grant.PrincipalID, digest, nil, ttl)
}

type PrincipalTokenRotation struct {
	PrincipalID   string
	PreviousToken string
	Token         string
	TTL           time.Duration
}

// RotatePrincipalToken is an offline init-role operation. It atomically changes
// the current digest and records its predecessor; grants and role are retained.
// A previous token may be expired, but cannot belong to a revoked principal.
func (p *Postgres) RotatePrincipalToken(ctx context.Context, in PrincipalTokenRotation) error {
	ttl, err := principalTokenTTL(in.TTL)
	if err != nil || !validRunLabel(in.PrincipalID, 256) || !ValidPrincipalToken(in.Token) || in.PreviousToken == "" || len(in.PreviousToken) > 4096 || in.Token == in.PreviousToken {
		return ErrInvalid
	}
	old := sha256.Sum256([]byte(in.PreviousToken))
	next := sha256.Sum256([]byte(in.Token))
	return p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		q := tx.(*Postgres).q()
		if err := lockPrincipal(ctx, q, in.PrincipalID); err != nil {
			return err
		}
		var digest []byte
		var active bool
		err := q.QueryRow(ctx, `SELECT token_sha256,active FROM principals WHERE id=$1 FOR UPDATE`, in.PrincipalID).Scan(&digest, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return pgErr(err)
		}
		if !active {
			return ErrDenied
		}
		if bytes.Equal(digest, next[:]) {
			// Resume after a client saved its pending token and the database
			// committed, but the client crashed before recording completion.
			var replay bool
			err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principal_token_history WHERE token_sha256=$1 AND principal_id=$2 AND rotated_from=$3)`, next[:], in.PrincipalID, old[:]).Scan(&replay)
			if err != nil {
				return pgErr(err)
			}
			if replay {
				return nil
			}
			return ErrConflict
		}
		if !bytes.Equal(digest, old[:]) {
			return ErrConflict
		}
		if err = insertPrincipalToken(ctx, q, in.PrincipalID, next[:], old[:], ttl); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `UPDATE principals SET token_sha256=$2 WHERE id=$1`, in.PrincipalID, next[:])
		return pgErr(err)
	})
}

type PrincipalTokenStatus struct {
	PrincipalID   string     `json:"principal_id"`
	IssuedAt      *time.Time `json:"issued_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	Active        bool       `json:"active"`
	RotateBefore  time.Time  `json:"rotate_before"`
	NeedsRotation bool       `json:"needs_rotation"`
}

// PrincipalTokenStatus requires the init connection and the current secret. It
// exposes lifecycle metadata, never a digest or raw token. Legacy credentials
// request rotation immediately; newly issued ones have a 30-day renewal margin.
func (p *Postgres) PrincipalTokenStatus(ctx context.Context, id, token string) (PrincipalTokenStatus, error) {
	out := PrincipalTokenStatus{PrincipalID: id}
	if !validRunLabel(id, 256) || token == "" || len(token) > 4096 {
		return out, ErrInvalid
	}
	digest := sha256.Sum256([]byte(token))
	err := p.q().QueryRow(ctx, `SELECT t.issued_at,t.expires_at,p.active,t.expires_at-interval '720 hours',
t.issued_at IS NULL OR t.expires_at<=statement_timestamp()+interval '720 hours'
FROM principals p JOIN principal_token_history t ON t.token_sha256=p.token_sha256 AND t.principal_id=p.id
WHERE p.id=$1 AND p.token_sha256=$2`, id, digest[:]).Scan(&out.IssuedAt, &out.ExpiresAt, &out.Active, &out.RotateBefore, &out.NeedsRotation)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	return out, pgErr(err)
}
