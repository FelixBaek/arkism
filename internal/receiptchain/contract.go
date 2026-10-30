// Package receiptchain records separate context transport boundaries. Records
// carry no authority and do not qualify observations for memory learning.
package receiptchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const Protocol = "sys-native-context-receipt-chain-v1"

var ErrInvalid = errors.New("receipt_chain_invalid")
var ErrConflict = errors.New("receipt_chain_conflict")
var ErrIncomplete = errors.New("receipt_chain_incomplete")

type Scope struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	UserID      string `json:"user_id"`
	AgentID     string `json:"agent_id"`
}
type Ref struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
}
type Source struct {
	Scope            Scope   `json:"scope"`
	Ref              Ref     `json:"ref"`
	ContentSHA256    *string `json:"content_sha256"`
	ProvenanceSHA256 *string `json:"provenance_sha256"`
	Form             string  `json:"form"`
}
type Record struct {
	Protocol           string   `json:"protocol"`
	CycleID            string   `json:"cycle_id"`
	Scope              Scope    `json:"scope"`
	ReaderSHA256       string   `json:"reader_sha256"`
	Run                *Ref     `json:"run"`
	Path               string   `json:"path"`
	Sequence           int64    `json:"sequence"`
	EventID            string   `json:"event_id"`
	Stage              string   `json:"stage"`
	Parents            []string `json:"parents"`
	ContextID          *string  `json:"context_id"`
	SourceRelation     string   `json:"source_relation"`
	Sources            []Source `json:"sources"`
	BytesSHA256        *string  `json:"bytes_sha256"`
	ObservedAtNS       *int64   `json:"observed_at_ns"`
	Status             string   `json:"status"`
	Reason             string   `json:"reason"`
	ActualUse          *bool    `json:"actual_use"`
	TaskSuccess        *bool    `json:"task_success"`
	CausalContribution *bool    `json:"causal_contribution"`
	RecordSHA256       string   `json:"record_sha256"`
}

func Hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func hashValue(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", ErrInvalid
	}
	return Hash(append([]byte(Protocol+"\x00"), raw...)), nil
}
func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
func label(s string, maximum int) bool {
	return s != "" && len(s) <= maximum && utf8.ValidString(s) && strings.TrimSpace(s) == s
}
func ValidScope(s Scope) bool {
	return label(s.WorkspaceID, 256) && label(s.ProjectID, 256) && len(s.UserID) <= 256 && len(s.AgentID) <= 256 && utf8.ValidString(s.UserID) && utf8.ValidString(s.AgentID)
}
func validRef(r Ref) bool   { return label(r.Collection, 64) && label(r.ID, 256) && r.Revision > 0 }
func ptrSHA(p *string) bool { return p == nil || validSHA(*p) }
func validRecordIdentity(r Record) error {
	if r.Protocol != Protocol || !label(r.CycleID, 128) || !ValidScope(r.Scope) || !validSHA(r.ReaderSHA256) || !label(r.Path, 80) || !label(r.EventID, 128) || r.Sequence < 1 || r.Sequence > 256 || r.Run != nil && !validRef(*r.Run) {
		return ErrInvalid
	}
	return nil
}
func validRecordHeader(r Record) error {
	if e := validRecordIdentity(r); e != nil {
		return e
	}
	if !ptrSHA(r.BytesSHA256) || !label(r.Status, 32) || !reasonKnown(r.Reason) || !pathKnown(r.Path) || r.ActualUse != nil || r.TaskSuccess != nil || r.CausalContribution != nil {
		return ErrInvalid
	}
	if r.Status != "observed" && r.Status != "unknown" && r.Status != "failed" {
		return ErrInvalid
	}
	return nil
}
func validRecord(r Record) error {
	if e := validRecordHeader(r); e != nil {
		return e
	}
	if !relationKnown(r.SourceRelation) || !stageKnown(r.Stage) || r.Parents == nil || len(r.Parents) > 256 || r.Sources == nil || len(r.Sources) > 200 {
		return ErrInvalid
	}
	for _, p := range r.Parents {
		if !validSHA(p) {
			return ErrInvalid
		}
	}
	if e := validRecordSources(r); e != nil {
		return e
	}
	if r.ContextID != nil && !label(*r.ContextID, 256) {
		return ErrInvalid
	}
	return nil
}
func stageKnown(s string) bool {
	switch s {
	case "server_return", "final_render", "transport_accepted", "reader_bytes_acknowledged", "source_read", "answer_observed", "prior_path":
		return true
	}
	return false
}
func Seal(r Record) (Record, error) {
	r.RecordSHA256 = ""
	if e := validRecord(r); e != nil {
		return Record{}, e
	}
	r = clone(r)
	h, e := hashValue(r)
	r.RecordSHA256 = h
	return r, e
}
func Validate(r Record) error {
	sealed, e := Seal(r)
	if e != nil {
		return e
	}
	if r.RecordSHA256 != sealed.RecordSHA256 {
		return ErrInvalid
	}
	return nil
}

// Decode rejects duplicate, unknown and omitted fields, including nested data.
func Decode(raw []byte) (Record, error) {
	if len(raw) > 131072 || !validJSONUnicode(raw) {
		return Record{}, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := scanValue(d, 0); e != nil {
		return Record{}, e
	}
	if _, e := d.Token(); e != io.EOF {
		return Record{}, ErrInvalid
	}
	var r Record
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&r); e != nil {
		return Record{}, ErrInvalid
	}
	canonical, e := json.Marshal(r)
	if e != nil {
		return Record{}, ErrInvalid
	}
	var given, expected any
	if json.Unmarshal(raw, &given) != nil || json.Unmarshal(canonical, &expected) != nil {
		return Record{}, ErrInvalid
	}
	if !sameShape(given, expected) {
		return Record{}, ErrInvalid
	}
	return r, Validate(r)
}
func sameShape(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		return ok && sameObjectShape(left, right)
	case []any:
		right, ok := b.([]any)
		return ok && sameArrayShape(left, right)
	}
	return scalarShape(a, b)
}
func sameObjectShape(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, exists := b[k]
		if !exists || !sameShape(v, w) {
			return false
		}
	}
	return true
}
func sameArrayShape(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if !sameShape(v, b[i]) {
			return false
		}
	}
	return true
}

func scalarShape(a, b any) bool {
	switch a.(type) {
	case nil:
		return b == nil
	case string:
		_, ok := b.(string)
		return ok
	case bool:
		_, ok := b.(bool)
		return ok
	case float64:
		_, ok := b.(float64)
		return ok
	case map[string]any, []any:
		return true
	}
	return false
}
func scanValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return ErrInvalid
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return scanObject(d, depth)
	case '[':
		return scanArray(d, depth)
	default:
		return ErrInvalid
	}
}

func reasonKnown(s string) bool {
	switch s {
	case "none", "unknown", "source_unknown", "prior_path", "partial_write", "server_receipt_missing", "storage_failed", "readback_failed", "spool_failed", "reader_unknown", "identity_unknown", "source_read_unknown":
		return true
	}
	return false
}
func pathKnown(s string) bool {
	switch s {
	case "prompt", "stable", "crew_runner_input", "reader_direct", "readback", "spool":
		return true
	}
	return false
}

func relationKnown(s string) bool {
	switch s {
	case "returned", "included", "read", "lineage_only", "unknown":
		return true
	}
	return false
}

func validRecordSources(r Record) error {
	for _, s := range r.Sources {
		if s.Scope != r.Scope || !validRef(s.Ref) || !ptrSHA(s.ContentSHA256) || !ptrSHA(s.ProvenanceSHA256) || (s.Form != "full" && s.Form != "meta" && s.Form != "unknown") {
			return ErrInvalid
		}
	}

	return nil
}

func scanObject(d *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return ErrInvalid
		}
		s, ok := k.(string)
		if !ok || seen[s] {
			return ErrInvalid
		}
		seen[s] = true
		if e = scanValue(d, depth+1); e != nil {
			return e
		}
	}
	end, e := d.Token()
	if e != nil || end != json.Delim('}') {
		return ErrInvalid
	}
	return nil
}
func scanArray(d *json.Decoder, depth int) error {
	for d.More() {
		if e := scanValue(d, depth+1); e != nil {
			return e
		}
	}
	end, e := d.Token()
	if e != nil || end != json.Delim(']') {
		return ErrInvalid
	}
	return nil
}
