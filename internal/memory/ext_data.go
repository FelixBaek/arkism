package memory

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

// Extension data is a small key-value store each extension keeps per space. It
// lives in the generic records table (collection ext_data, id "{extension}:{key}",
// kind = extension), so backups, restores, row security and space deletion cover
// it like any other record. Only its own routes touch it (ownRoutesOnly):
// people and services read and change it over /v1/extensions/{extension}/data,
// and an extension reads and changes its own entries through a route or tool
// call that declared "data": true, always with the caller's permission.
const (
	colExtData               = "ext_data"
	extDataDefaultKeys       = opsPageMax
	extDataDefaultValueBytes = 4 << 10
	extDataKeysCap           = 5000
	extDataValueCap          = 256 << 10
	// extDataSnapshotMax bounds keys × value size: a data call carries every entry of the space.
	extDataSnapshotMax = 16 << 20
)

// ExtDataLimits are the per-space limits of one extension's store: keys and the
// size of one JSON value. Zero keeps the default (200 keys, 4 KiB).
type ExtDataLimits struct {
	MaxKeys    int
	ValueBytes int
}

func (l ExtDataLimits) maxKeys() int64  { return int64(cmp.Or(l.MaxKeys, extDataDefaultKeys)) }
func (l ExtDataLimits) valueBytes() int { return cmp.Or(l.ValueBytes, extDataDefaultValueBytes) }
func (l ExtDataLimits) requestBytes() int64 {
	return max(64<<10, 4*int64(l.valueBytes())+16<<10) // room for a few full values per change request
}

// ParseExtDataLimits reads ARKISM_EXT_DATA_MAX_KEYS (1–5000) and
// ARKISM_EXT_DATA_VALUE_BYTES (64 B–256 KiB); empty keeps the default. Keys
// times value size may not pass 16 MiB, the most one data call carries.
func ParseExtDataLimits(keys, valueBytes string) (ExtDataLimits, error) {
	var l ExtDataLimits
	var err error
	if keys != "" {
		if l.MaxKeys, err = strconv.Atoi(keys); err != nil || l.MaxKeys < 1 || l.MaxKeys > extDataKeysCap {
			return l, fmt.Errorf("ARKISM_EXT_DATA_MAX_KEYS: want 1-%d", extDataKeysCap)
		}
	}
	if valueBytes != "" {
		if l.ValueBytes, err = strconv.Atoi(valueBytes); err != nil || l.ValueBytes < 64 || l.ValueBytes > extDataValueCap {
			return l, fmt.Errorf("ARKISM_EXT_DATA_VALUE_BYTES: want 64-%d", extDataValueCap)
		}
	}
	if l.maxKeys()*int64(l.valueBytes()) > extDataSnapshotMax {
		return l, fmt.Errorf("ARKISM_EXT_DATA_MAX_KEYS × ARKISM_EXT_DATA_VALUE_BYTES may not pass %d bytes", extDataSnapshotMax)
	}
	return l, nil
}

var extDataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ExtDataEntry is the stored entry (Record.ExtData).
type ExtDataEntry struct {
	Extension string `json:"extension" bson:"extension"`
	Key       string `json:"key" bson:"key"`
	Value     string `json:"-" bson:"value"` // JSON text
}

// ExtDataItem is one entry as the API shows it.
type ExtDataItem struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Revision  int64           `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}

type ExtDataPage struct {
	Items      []ExtDataItem `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Authority  string        `json:"authority"`
}

// ExtDataOp is one change: put sets value, delete removes the key. ExpectedRevision 0 skips the revision check.
type ExtDataOp struct {
	Op               string          `json:"op"`
	Key              string          `json:"key"`
	Value            json.RawMessage `json:"value,omitempty"`
	ExpectedRevision int64           `json:"expected_revision,omitempty"`
}

func extDataID(extension, key string) string { return extension + ":" + key }

func extDataItem(r Record) ExtDataItem {
	return ExtDataItem{Key: r.ExtData.Key, Value: json.RawMessage(r.ExtData.Value), Revision: r.Revision, UpdatedAt: r.UpdatedAt, UpdatedBy: r.CreatedBy}
}

func validExtDataOp(op ExtDataOp, maxValue int) error {
	if !extDataKeyPattern.MatchString(op.Key) || op.ExpectedRevision < 0 || op.Op != "put" && op.Op != "delete" || op.Op == "delete" && len(op.Value) > 0 {
		return ErrInvalid
	}
	if op.Op == "delete" {
		return nil
	}
	if len(op.Value) == 0 || len(op.Value) > maxValue || !utf8.Valid(op.Value) || !json.Valid(op.Value) {
		return ErrInvalid
	}
	if containsSecret(string(op.Value)) || containsSecret(op.Key) {
		return ErrSecretDetected
	}
	if hasPrivateMarker(string(op.Value)) {
		return ErrInvalid
	}
	return nil
}

func (s *Service) extDataActor(ctx context.Context, token, action string, scope Scope, extension string, roles ...string) (Scope, Decision, error) {
	scope = spaceScope(scope)
	if !extNamePattern.MatchString(extension) {
		return scope, Decision{}, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, action, scope, "", roles...)
	return scope, d, err
}

// ListExtensionData pages an extension's entries in a space, newest first.
func (s *Service) ListExtensionData(ctx context.Context, token string, scope Scope, extension, cursor string, limit int) (ExtDataPage, error) {
	out := ExtDataPage{Items: []ExtDataItem{}, Authority: AuthorityDataOnly}
	scope, _, err := s.extDataActor(ctx, token, "read:"+colExtData, scope, extension, roleHuman, roleService, roleAgent)
	if err != nil {
		return out, err
	}
	rows, next, err := s.opsPage(ctx, OpsListInput{Scope: scope, Kind: extension, Cursor: cursor, Limit: limit}, colExtData)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		if r.ExtData != nil {
			out.Items = append(out.Items, extDataItem(r))
		}
	}
	// A name with no entries that no loaded extension answers to is a typo, not an empty store.
	if loaded, ok := s.Extensions.Endpoints.(interface{ Has(string) bool }); len(rows) == 0 && cursor == "" && (!ok || !loaded.Has(extension)) {
		return out, reasonError{ErrNotFound, fmt.Sprintf("extension: %q is not loaded and has no data in this space", extension)}
	}
	out.NextCursor = next
	return out, nil
}

// GetExtensionData reads one entry.
func (s *Service) GetExtensionData(ctx context.Context, token string, scope Scope, extension, key string) (ExtDataItem, error) {
	scope, _, err := s.extDataActor(ctx, token, "read:"+colExtData, scope, extension, roleHuman, roleService, roleAgent)
	if err != nil {
		return ExtDataItem{}, err
	}
	if !extDataKeyPattern.MatchString(key) {
		return ExtDataItem{}, ErrInvalid
	}
	r, err := s.Repo.Get(ctx, colExtData, scope, extDataID(extension, key))
	if err == nil && r.ExtData == nil {
		err = ErrNotFound
	}
	if err != nil {
		return ExtDataItem{}, err
	}
	return extDataItem(r), nil
}

// ChangeExtensionData applies changes to an extension's entries in a space, all or none.
func (s *Service) ChangeExtensionData(ctx context.Context, token string, scope Scope, extension string, ops []ExtDataOp) ([]ExtDataItem, error) {
	scope, d, err := s.extDataActor(ctx, token, "write:"+colExtData, scope, extension, roleHuman, roleService, roleAgent)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 || len(ops) > 32 {
		return nil, ErrInvalid
	}
	for _, op := range ops {
		if err := validExtDataOp(op, s.ExtData.valueBytes()); err != nil {
			return nil, err
		}
	}
	have, err := s.extDataCount(ctx, scope, extension)
	if err != nil {
		return nil, err
	}
	out := []ExtDataItem{}
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = out[:0]
		count := have
		for _, op := range ops {
			item, added, err := s.changeExtData(ctx, tx, scope, d, extension, op)
			if count += added; err == nil && count > s.ExtData.maxKeys() {
				err = errors.Join(ErrInvalid, errExtDataFull)
			}
			if err != nil {
				return err
			}
			if item != nil {
				out = append(out, *item)
			}
		}
		return nil
	})
	return out, err
}

// changeExtData applies one change and says how it changed the key count (+1, 0 or -1).
func (s *Service) changeExtData(ctx context.Context, tx Tx, scope Scope, d Decision, extension string, op ExtDataOp) (*ExtDataItem, int64, error) {
	id := extDataID(extension, op.Key)
	cur, err := tx.Get(ctx, colExtData, scope, id)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, 0, err
	}
	if op.ExpectedRevision > 0 && (!exists || cur.Revision != op.ExpectedRevision) {
		return nil, 0, ErrConflict
	}
	if err := audit(ctx, tx, scope, d.PrincipalID, op.Op+":"+colExtData, id); err != nil {
		return nil, 0, err
	}
	if op.Op == "delete" {
		if !exists {
			return nil, 0, ErrNotFound
		}
		return nil, -1, tx.Delete(ctx, colExtData, scope, id, cur.Revision)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: id, Scope: scope, Kind: extension, Status: "active", Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role,
		ExtData: &ExtDataEntry{Extension: extension, Key: op.Key, Value: string(op.Value)}}
	added := int64(1)
	if exists {
		r.CreatedAt, r.Revision, added = cur.CreatedAt, cur.Revision+1, 0
		err = tx.Replace(ctx, colExtData, r, cur.Revision)
	} else {
		err = tx.Insert(ctx, colExtData, r)
	}
	item := extDataItem(r)
	return &item, added, err
}

// extDataCount is how many keys an extension keeps in the space.
// shortcut: counted before the transaction, so two racing writers may together pass the cap by a few keys; a soft cap is enough here.
func (s *Service) extDataCount(ctx context.Context, scope Scope, extension string) (int64, error) {
	st, err := s.opsStore()
	if err != nil {
		return 0, err
	}
	rows, err := st.OpsCounts(ctx, opsCountQuery{Scope: scope, Collection: colExtData, By: "kind"})
	for _, c := range rows {
		if c.Key == extension {
			return c.Count, err
		}
	}
	return 0, err
}

var errExtDataFull = errors.New("extension data key limit reached for this space")

// extDataSnapshot is every entry an extension keeps in the space, for a call that declared "data": true.
func (s *Service) extDataSnapshot(ctx context.Context, token string, scope Scope, extension string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	cursor := ""
	for {
		page, err := s.ListExtensionData(ctx, token, scope, extension, cursor, opsPageMax)
		if err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			out[it.Key] = it.Value
		}
		// the key cap is soft (extDataCount), so stop at a page past it rather than read on
		if cursor = page.NextCursor; cursor == "" || int64(len(out)) > s.ExtData.maxKeys()+opsPageMax {
			return out, nil
		}
	}
}
