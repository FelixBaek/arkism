package crewrun

// Optional local cross-process journal. It transports accounting/refs only;
// hashes do not authenticate a producer or prove exhaustive provider billing.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const observationJournalEntries = 8192
const observationJournalFileBytes = 128 << 10
const observationJournalTotalBytes = 32 << 20

type ObservationTransportRequest struct {
	Protocol   string             `json:"protocol"`
	Parent     AttemptObservation `json:"parent"`
	JournalDir string             `json:"journal_dir"`
}
type ObservationTransportRead struct {
	Records   []AttemptObservation
	Evidence  ObservationEvidence
	Issues    []string
	Coverage  string
	Aggregate UsageObservation
}
type observationOwner struct {
	Protocol     string `json:"protocol"`
	ParentSHA256 string `json:"parent_sha256"`
}
type observationJournal struct {
	mu        sync.Mutex
	path      string
	root      *os.Root
	dirInfo   fs.FileInfo
	ownerInfo fs.FileInfo
	parent    AttemptObservation
	owned     map[string]fs.FileInfo
	bytes     int64
	sequence  int64
	closed    bool
}
type ObservationTransport struct{ journal *observationJournal }

// ObservationJournalWriter is used through the host package's explicit opt-in API.
type ObservationJournalWriter struct {
	journal    *observationJournal
	begun      map[string]bool
	latest     map[string]AttemptObservation
	versions   map[string]map[int64]AttemptObservation
	incomplete bool
	closed     bool
}

func observationTransportError(code string) error { return observationError(code, "$.observation") }
func observationMarshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, observationTransportError("journal_encoding")
	}
	return append(raw, '\n'), nil
}
func validateTransportParent(req ObservationTransportRequest) error {
	if req.Protocol != CrewObservationContractID {
		return observationTransportError("unsupported_version")
	}
	if err := ValidateAttemptObservation(req.Parent); err != nil {
		return err
	}
	if req.Parent.Status != "reserved" || req.Parent.DispatchState != "not_dispatched" || req.Parent.UsageRole != "transport_aggregate" {
		return observationTransportError("invalid_transport_parent")
	}
	return nil
}

// DecodeObservationTransportRequest is a closed wire envelope, not a new
// canonical schema type. Default product JSON is never extended implicitly.
func DecodeObservationTransportRequest(raw []byte) (ObservationTransportRequest, error) {
	var req ObservationTransportRequest
	if err := checkObservationJSONUnicode(raw); err != nil {
		return req, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkObservationJSON(decoder, 0); err != nil {
		return req, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return req, observationTransportError("invalid_json")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return req, observationTransportError("invalid_transport_shape")
	}
	for _, key := range []string{"protocol", "parent", "journal_dir"} {
		if _, ok := fields[key]; !ok {
			return req, observationTransportError("invalid_transport_shape")
		}
	}
	if json.Unmarshal(fields["protocol"], &req.Protocol) != nil || json.Unmarshal(fields["journal_dir"], &req.JournalDir) != nil {
		return req, observationTransportError("invalid_type")
	}
	parent, err := DecodeAttemptObservation(fields["parent"])
	if err != nil {
		return req, err
	}
	req.Parent = parent
	return req, validateTransportParent(req)
}
func NewObservationTransport(parent AttemptObservation) (*ObservationTransport, error) {
	req := ObservationTransportRequest{Protocol: CrewObservationContractID, Parent: parent}
	if err := validateTransportParent(req); err != nil {
		return nil, err
	}
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return nil, observationTransportError("journal_create")
	}
	path, err := os.MkdirTemp(base, "arkism-observation-")
	if err != nil {
		return nil, observationTransportError("journal_create")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, observationTransportError("journal_create")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, observationTransportError("journal_create")
	}
	journal := &observationJournal{path: path, root: root, dirInfo: info, parent: cloneObservation(parent), owned: map[string]fs.FileInfo{}}
	raw, _ := observationMarshal(observationOwner{CrewObservationContractID, parent.RecordSHA256})
	if err = journal.publish("owner.json", raw); err != nil {
		_ = journal.cleanup()
		return nil, err
	}
	journal.ownerInfo = journal.owned["owner.json"]
	return &ObservationTransport{journal: journal}, nil
}
func (t *ObservationTransport) Request() ObservationTransportRequest {
	if t == nil || t.journal == nil {
		return ObservationTransportRequest{}
	}
	return ObservationTransportRequest{Protocol: CrewObservationContractID, Parent: cloneObservation(t.journal.parent), JournalDir: t.journal.path}
}
func privateObservationPath(path string) (fs.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(filepath.Base(path), "arkism-observation-") {
		return nil, observationTransportError("journal_path")
	}
	current := path
	for {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, observationTransportError("journal_path")
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, observationTransportError("journal_permissions")
	}
	return info, nil
}
func openObservationJournal(req ObservationTransportRequest) (*observationJournal, error) {
	if err := validateTransportParent(req); err != nil {
		return nil, err
	}
	info, err := privateObservationPath(req.JournalDir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(req.JournalDir)
	if err != nil {
		return nil, observationTransportError("journal_open")
	}
	journal := &observationJournal{path: req.JournalDir, root: root, dirInfo: info, parent: cloneObservation(req.Parent), owned: map[string]fs.FileInfo{}}
	raw, ownerInfo, err := journal.read("owner.json")
	expected, _ := observationMarshal(observationOwner{req.Protocol, req.Parent.RecordSHA256})
	if err == nil && !bytes.Equal(raw, expected) {
		err = observationTransportError("journal_owner")
	}

	if err != nil {
		_ = root.Close()
		return nil, err
	}
	journal.ownerInfo = ownerInfo
	return journal, nil
}
func (j *observationJournal) unchanged() error {
	if j.closed {
		return observationTransportError("journal_closed")
	}
	info, err := os.Lstat(j.path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, j.dirInfo) {
		return observationTransportError("journal_replaced")
	}
	if j.ownerInfo != nil {
		expected, _ := observationMarshal(observationOwner{CrewObservationContractID, j.parent.RecordSHA256})
		raw, _, readErr := j.read("owner.json")
		if readErr != nil || !bytes.Equal(raw, expected) {
			return observationTransportError("journal_replaced")
		}
		owner, e := j.root.Lstat("owner.json")
		if e != nil || !os.SameFile(owner, j.ownerInfo) {
			return observationTransportError("journal_replaced")
		}
	}
	return nil
}
func (j *observationJournal) read(name string) ([]byte, fs.FileInfo, error) {
	if filepath.Base(name) != name {
		return nil, nil, observationTransportError("journal_path")
	}
	info, err := j.root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > observationJournalFileBytes {
		return nil, nil, observationTransportError("journal_file")
	}
	file, err := j.root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		_ = file.Close()
		return nil, nil, observationTransportError("journal_replaced")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, observationJournalFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > observationJournalFileBytes {
		return nil, nil, observationTransportError("journal_read")
	}
	return raw, info, nil
}
func (j *observationJournal) publish(name string, raw []byte) error {
	if err := j.unchanged(); err != nil {
		return err
	}
	if filepath.Base(name) != name || len(raw) > observationJournalFileBytes {
		return observationTransportError("journal_limit")
	}
	if prior, _, err := j.read(name); err == nil {
		if bytes.Equal(prior, raw) {
			return nil
		}
		return observationTransportError("journal_conflict")
	}
	if len(j.owned) >= observationJournalEntries || j.bytes+int64(len(raw)) > observationJournalTotalBytes {
		return observationTransportError("journal_limit")
	}
	j.sequence++
	temporary := fmt.Sprintf(".write-%d-%d", os.Getpid(), j.sequence)
	file, err := j.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return observationTransportError("journal_write")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return observationTransportError("journal_write")
	}
	written, writeErr := file.Write(raw)
	if written != len(raw) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		j.removeOwned(temporary, info)
		return observationTransportError("journal_write")
	}
	if err = j.root.Link(temporary, name); err != nil {
		j.removeOwned(temporary, info)
		return observationTransportError("journal_write")
	}
	j.removeOwned(temporary, info)
	j.owned[name] = info
	j.bytes += int64(len(raw))
	return nil
}
func (j *observationJournal) removeOwned(name string, info fs.FileInfo) {
	current, err := j.root.Lstat(name)
	if err == nil && os.SameFile(current, info) {
		_ = j.root.Remove(name)
	}
}
func (j *observationJournal) cleanup() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	if err := j.unchanged(); err != nil {
		j.closed = true
		_ = j.root.Close()
		return err
	}
	for name, info := range j.owned {
		j.removeOwned(name, info)
	}
	j.closed = true
	closeErr := j.root.Close()
	info, err := os.Lstat(j.path)
	if err != nil || !os.SameFile(info, j.dirInfo) {
		return observationTransportError("journal_replaced")
	}
	if err = os.Remove(j.path); err != nil {
		return observationTransportError("journal_cleanup")
	}
	return closeErr
}
func (t *ObservationTransport) Close() error {
	if t == nil || t.journal == nil {
		return nil
	}
	return t.journal.cleanup()
}
func observationRecordName(record AttemptObservation) string {
	return "record-" + strings.TrimPrefix(record.Identity.AttemptID, "crw2-attempt-") + "-" + strconv.FormatInt(record.ObservationRevision, 10) + ".json"
}
func observationIDName(prefix, id string) string {
	return prefix + strings.TrimPrefix(id, "crw2-attempt-") + ".json"
}
func observationBoundary(parent AttemptObservation) string {
	return "host-" + parent.Identity.AttemptID
}
func observationChildMatches(parent, child AttemptObservation) bool {
	id := child.Identity
	return id.ParentAttemptID != nil && *id.ParentAttemptID == parent.Identity.AttemptID &&
		id.IdentityBasis == "observed_host_leaf" && id.CanonicalAttemptID == nil && id.AttemptRef == nil &&
		child.Task == parent.Task && child.UsageRole == "provider_leaf" &&
		id.Run.RunRef == parent.Identity.Run.RunRef && id.NodeID == parent.Identity.NodeID && id.NodeAttempt == parent.Identity.NodeAttempt &&
		sameObservationRun(id.Run, parent.Identity.Run) && sameObservationResultSnapshot(id.Run.ResultRevision, parent.Identity.Run.ResultRevision)
}

func OpenObservationJournalWriter(req ObservationTransportRequest) (*ObservationJournalWriter, error) {
	journal, err := openObservationJournal(req)
	if err != nil {
		return nil, err
	}
	lock, err := journal.root.OpenFile("writer.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = journal.root.Close()
		return nil, observationTransportError("journal_writer_conflict")
	}
	lockInfo, statErr := lock.Stat()
	_, writeErr := lock.Write([]byte(req.Parent.RecordSHA256 + "\n"))
	closeErr := lock.Close()
	if statErr != nil || writeErr != nil || closeErr != nil {
		_ = journal.root.Close()
		return nil, observationTransportError("journal_write")
	}
	journal.owned["writer.lock"] = lockInfo
	writer := &ObservationJournalWriter{journal: journal, begun: map[string]bool{}, latest: map[string]AttemptObservation{}, versions: map[string]map[int64]AttemptObservation{}}
	opened, err := writer.sender("open", []string{})
	if err != nil {
		writer.MarkIncomplete()
		return writer, err
	}
	raw, err := observationMarshal(opened)
	if err == nil {
		err = journal.publish("opened.json", raw)
	}
	if err != nil {
		writer.MarkIncomplete()
		return writer, err
	}
	return writer, nil
}
func (w *ObservationJournalWriter) issueLocked() {
	w.incomplete = true
	raw := []byte("{\"code\":\"host_observation_incomplete\"}\n")
	_ = w.journal.publish("issue.json", raw)
}
func (w *ObservationJournalWriter) MarkIncomplete() {
	if w == nil || w.journal == nil {
		return
	}
	w.journal.mu.Lock()
	defer w.journal.mu.Unlock()
	w.issueLocked()
}
func (w *ObservationJournalWriter) Begin(id string) error {
	if w == nil || w.journal == nil {
		return observationTransportError("journal_closed")
	}
	w.journal.mu.Lock()
	defer w.journal.mu.Unlock()
	if w.closed || !observationAttemptHash.MatchString(id) {
		w.issueLocked()
		return observationTransportError("journal_begin")
	}
	if w.begun[id] {
		w.issueLocked()
		return observationTransportError("journal_duplicate_begin")
	}
	raw, err := observationMarshal(map[string]string{"attempt_id": id, "parent_attempt_id": w.journal.parent.Identity.AttemptID})
	if err == nil {
		err = w.journal.publish(observationIDName("begin-", id), raw)
	}
	if err != nil {
		w.issueLocked()
		return err
	}
	w.begun[id] = true
	return nil
}
func (w *ObservationJournalWriter) Record(record AttemptObservation, proof ObservationParentLinkEvidence) error {
	if w == nil || w.journal == nil {
		return observationTransportError("journal_closed")
	}
	w.journal.mu.Lock()
	defer w.journal.mu.Unlock()
	if w.closed || !w.begun[record.Identity.AttemptID] || !observationChildMatches(w.journal.parent, record) {
		w.issueLocked()
		return observationTransportError("journal_record_binding")
	}
	if err := ValidateAttemptObservation(record); err != nil {
		w.issueLocked()
		return err
	}
	versions := w.versions[record.Identity.AttemptID]
	if prior, ok := versions[record.ObservationRevision]; ok {
		if prior.RecordSHA256 == record.RecordSHA256 {
			return nil
		}
		w.issueLocked()
		return observationTransportError("journal_conflict")
	}
	if !validObservationTransition(versions, record) {
		w.issueLocked()
		return observationTransportError("journal_observation_invalid")
	}
	raw, err := observationMarshal(record)
	if err == nil {
		err = w.journal.publish(observationRecordName(record), raw)
	}
	if err != nil {
		w.issueLocked()
		return err
	}
	// Preserve a valid charged record even if its separate proof cannot be published.
	proofRaw, err := observationMarshal(proof)
	if err == nil {
		err = qualifyObservationParent(record, w.journal.parent, proofRaw, observationBytesSHA(proofRaw))
	}
	if err == nil {
		err = w.journal.publish(observationIDName("proof-", record.Identity.AttemptID), proofRaw)
	}
	if err != nil {
		w.issueLocked()
		return err
	}
	if versions == nil {
		versions = map[int64]AttemptObservation{}
		w.versions[record.Identity.AttemptID] = versions
	}
	versions[record.ObservationRevision] = cloneObservation(record)
	if prior, ok := w.latest[record.Identity.AttemptID]; !ok || record.ObservationRevision > prior.ObservationRevision {
		w.latest[record.Identity.AttemptID] = cloneObservation(record)
	}
	return nil
}
func (w *ObservationJournalWriter) sender(state string, members []string) (ObservationSenderJournal, error) {
	parent := w.journal.parent
	run := parent.Identity.Run
	owner, _ := observationMarshal(observationOwner{CrewObservationContractID, parent.RecordSHA256})
	var closing *string
	if state == "closed" {
		raw, _ := observationMarshal(members)
		digest := observationBytesSHA(raw)
		closing = &digest
	}
	return SealObservationSenderJournal(ObservationSenderJournal{ContractID: CrewObservationContractID, Scope: run.Scope, Task: parent.Task, RootRunCollection: *run.RootRunCollection, RootRunID: *run.RootRunID, ParentAttemptID: parent.Identity.AttemptID, SenderBoundaryID: observationBoundary(parent), State: state, MemberAttemptIDs: members, OpeningEventSHA256: observationBytesSHA(owner), ClosingEventSHA256: closing})
}
func (w *ObservationJournalWriter) CloseBoundary() error {
	if w == nil || w.journal == nil {
		return nil
	}
	w.journal.mu.Lock()
	defer w.journal.mu.Unlock()
	if w.closed {
		return nil
	}
	defer func() { w.closed = true; _ = w.journal.root.Close(); w.journal.closed = true }()
	members := []string{}
	if len(w.begun) == 0 {
		w.issueLocked()
	}
	for id := range w.begun {
		record, ok := w.latest[id]
		if !ok || record.Status == "reserved" {
			w.issueLocked()
			continue
		}
		if record.DispatchState != "not_dispatched" {
			members = append(members, id)
		}
	}
	sort.Strings(members)
	if w.incomplete {
		return observationTransportError("journal_incomplete")
	}
	journal, err := w.sender("closed", members)
	if err != nil {
		w.issueLocked()
		return err
	}
	raw, err := observationMarshal(journal)
	if err == nil {
		err = w.journal.publish("closed.json", raw)
	}
	if err != nil {
		w.issueLocked()
		return err
	}
	membership, err := ObservationMembershipHash(members)
	if err != nil {
		w.issueLocked()
		return err
	}
	parent := w.journal.parent
	run := parent.Identity.Run
	closure, err := SealObservationDispatchClosure(ObservationDispatchClosure{ContractID: CrewObservationContractID, Scope: run.Scope, Task: parent.Task, RootRunCollection: *run.RootRunCollection, RootRunID: *run.RootRunID, ParentAttemptID: parent.Identity.AttemptID, SenderBoundaryID: observationBoundary(parent), MemberAttemptIDs: members, MembershipSHA256: membership, SenderJournalSHA256: observationBytesSHA(raw)})
	if err != nil {
		w.issueLocked()
		return err
	}
	closedRaw, err := observationMarshal(closure)
	if err == nil {
		err = w.journal.publish("closure.json", closedRaw)
	}
	if err != nil {
		w.issueLocked()
	}
	return err
}
func appendObservationIssue(issues []string, code string) []string {
	if !observationContains(issues, code) {
		issues = append(issues, code)
	}
	return issues
}
func (t *ObservationTransport) Read() (ObservationTransportRead, error) {
	result := ObservationTransportRead{Records: []AttemptObservation{}, Evidence: ObservationEvidence{ParentLinks: map[string][]byte{}, ParentSHA256: map[string]string{}, SenderJournals: map[string][]byte{}, SenderSHA256: map[string]string{}}, Issues: []string{}, Coverage: "unknown", Aggregate: UnknownUsageObservation()}
	if t == nil || t.journal == nil {
		return result, observationTransportError("journal_closed")
	}
	j := t.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.unchanged(); err != nil {
		return result, err
	}
	entries, err := fs.ReadDir(j.root.FS(), ".")
	if err != nil {
		return result, observationTransportError("journal_read")
	}
	if len(entries) > observationJournalEntries {
		result.Issues = appendObservationIssue(result.Issues, "journal_limit")
		entries = entries[:observationJournalEntries]
	}
	var total int64
	begun := map[string]bool{}
	records := map[string]AttemptObservation{}
	for _, entry := range entries {
		name := entry.Name()
		raw, info, readErr := j.read(name)
		if readErr != nil {
			result.Issues = appendObservationIssue(result.Issues, "journal_file_invalid")
			continue
		}
		total += int64(len(raw))
		if total > observationJournalTotalBytes {
			result.Issues = appendObservationIssue(result.Issues, "journal_limit")
			break
		}
		accepted := readObservationEntry(&result, name, raw, j.parent, begun, records)
		if accepted {
			j.owned[name] = info
		}
	}
	collector := NewAttemptObserver(nil)
	sort.Slice(result.Records, func(i, k int) bool {
		a, b := result.Records[i], result.Records[k]
		if a.Identity.AttemptID == b.Identity.AttemptID {
			return a.ObservationRevision < b.ObservationRevision
		}
		return a.Identity.AttemptID < b.Identity.AttemptID
	})
	for _, record := range result.Records {
		collector.Observe(record)
	}
	normalized := collector.Snapshot()
	result.Records = normalized.Records
	records = map[string]AttemptObservation{}
	for _, record := range normalized.Latest {
		records[record.Identity.AttemptID] = record
	}
	for _, issue := range normalized.Issues {
		if observationContains([]string{"invalid_observation", "observation_conflict", "observation_transition", "canonical_attempt_conflict"}, issue) {
			result.Issues = appendObservationIssue(result.Issues, "journal_observation_invalid")
		}
	}

	for id := range begun {
		if _, ok := records[id]; !ok {
			result.Issues = appendObservationIssue(result.Issues, "journal_record_missing")
		}
	}
	finishObservationRead(&result, j.parent, begun, records)
	sort.Strings(result.Issues)
	return result, nil
}
func readObservationEntry(result *ObservationTransportRead, name string, raw []byte, parent AttemptObservation, begun map[string]bool, records map[string]AttemptObservation) bool {
	switch {
	case name == "owner.json":
		return true
	case name == "writer.lock":
		if string(raw) == parent.RecordSHA256+"\n" {
			return true
		}
	case name == "issue.json":
		result.Issues = appendObservationIssue(result.Issues, "host_observation_incomplete")
		return bytes.Equal(raw, []byte("{\"code\":\"host_observation_incomplete\"}\n"))
	case strings.HasPrefix(name, "begin-"):
		var item map[string]string
		_, markerErr := observationWireObject(raw, []string{"attempt_id", "parent_attempt_id"})
		if markerErr == nil && json.Unmarshal(raw, &item) == nil && len(item) == 2 && item["parent_attempt_id"] == parent.Identity.AttemptID && observationAttemptHash.MatchString(item["attempt_id"]) && name == observationIDName("begin-", item["attempt_id"]) {
			begun[item["attempt_id"]] = true
			return true
		}
	case strings.HasPrefix(name, "record-"):
		return readObservationRecord(result, name, raw, parent, records)
	case strings.HasPrefix(name, "proof-"):
		proof, err := DecodeObservationParentLink(raw)
		if err == nil && proof.ParentAttemptID == parent.Identity.AttemptID && name == observationIDName("proof-", proof.ChildAttemptID) {
			result.Evidence.ParentLinks[proof.ChildAttemptID] = raw
			result.Evidence.ParentSHA256[proof.ChildAttemptID] = observationBytesSHA(raw)
			return true
		}
	case name == "opened.json", name == "closed.json", name == "closure.json":
		return readObservationBoundary(result, name, raw, parent)
	}
	result.Issues = appendObservationIssue(result.Issues, "journal_entry_invalid")
	return false
}
func finishObservationRead(result *ObservationTransportRead, parent AttemptObservation, begun map[string]bool, records map[string]AttemptObservation) {
	collector := NewAttemptObserver(nil)
	for _, record := range result.Records {
		collector.Observe(record)
	}
	for id := range records {
		if !begun[id] {
			result.Issues = appendObservationIssue(result.Issues, "journal_begin_missing")
		}
	}
	if len(result.Evidence.Closures) != 1 {
		result.Issues = appendObservationIssue(result.Issues, "journal_boundary_unclosed")
	}
	if len(result.Issues) > 0 {
		result.Coverage = "partial"
		return
	}
	// This verifies only the sender's local boundary. The immutable outer
	// reservation can reference an ancestor outside this journal; global ancestry
	// and later descendants remain the caller's ledger qualification obligation.
	for id, record := range records {
		if err := qualifyObservationParent(record, parent, result.Evidence.ParentLinks[id], result.Evidence.ParentSHA256[id]); err != nil {
			result.Issues = appendObservationIssue(result.Issues, "journal_evidence_invalid")
			result.Coverage = "partial"
			return
		}
	}
	if _, err := qualifyObservationClosure(parent, result.Evidence.Closures[0], records, result.Evidence); err != nil {
		result.Issues = appendObservationIssue(result.Issues, "journal_evidence_invalid")
		result.Coverage = "partial"
		return
	}
	result.Coverage = "complete"
	result.Aggregate = aggregateObservationUsage(collector.Snapshot().Totals, records)
}
func aggregateObservationUsage(totals ObserverTotals, records map[string]AttemptObservation) UsageObservation {
	result := UnknownUsageObservation()
	for _, component := range []struct {
		total     ComponentTotal
		value     **int64
		basis     *string
		component string
	}{{totals.Input, &result.InputTokens, &result.InputBasis, "input"}, {totals.Output, &result.OutputTokens, &result.OutputBasis, "output"}, {totals.CacheRead, &result.CacheReadTokens, &result.CacheReadBasis, "cache_read"}, {totals.CacheWrite, &result.CacheWriteTokens, &result.CacheWriteBasis, "cache_write"}, {totals.Reasoning, &result.ReasoningTokens, &result.ReasoningBasis, "reasoning"}} {
		if component.total.Value != nil {
			value := *component.total.Value
			*component.value = &value
			*component.basis = observationAggregateBasis(records, component.component)
		}
	}
	result.UsageBasis = usageOverall(result.InputBasis, result.OutputBasis)
	if totals.Cost.Value != nil {
		value := *totals.Cost.Value
		result.CostUSD = &value
		result.CostBasis = observationAggregateBasis(records, "cost")
	}
	return result
}

func observationWireObject(raw []byte, keys []string) (map[string]json.RawMessage, error) {
	if err := checkObservationJSONUnicode(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkObservationJSON(decoder, 0); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, observationTransportError("invalid_json")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return nil, observationTransportError("invalid_transport_shape")
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return nil, observationTransportError("invalid_transport_shape")
		}
	}
	return fields, nil
}
func observationAggregateBasis(records map[string]AttemptObservation, component string) string {
	for _, record := range records {
		if record.DispatchState == "not_dispatched" {
			continue
		}
		u := record.Usage
		basis := map[string]string{"input": u.InputBasis, "output": u.OutputBasis, "cache_read": u.CacheReadBasis, "cache_write": u.CacheWriteBasis, "reasoning": u.ReasoningBasis, "cost": u.CostBasis}[component]
		if basis == "estimated" {
			return "estimated"
		}
	}
	return "provider_reported"
}

func readObservationRecord(result *ObservationTransportRead, name string, raw []byte, parent AttemptObservation, records map[string]AttemptObservation) bool {
	record, err := DecodeAttemptObservation(raw)
	if err == nil && observationChildMatches(parent, record) && name == observationRecordName(record) {
		result.Records = append(result.Records, record)
		id := record.Identity.AttemptID
		if prior, ok := records[id]; !ok || record.ObservationRevision > prior.ObservationRevision {
			records[id] = record
		}
		return true
	}

	result.Issues = appendObservationIssue(result.Issues, "journal_entry_invalid")
	return false
}
func readObservationBoundary(result *ObservationTransportRead, name string, raw []byte, parent AttemptObservation) bool {
	switch name {
	case "opened.json":
		journal, err := DecodeObservationSenderJournal(raw)
		if err == nil && journal.ParentAttemptID == parent.Identity.AttemptID && journal.State == "open" {
			return true
		}
	case "closed.json":
		journal, err := DecodeObservationSenderJournal(raw)
		if err == nil && journal.ParentAttemptID == parent.Identity.AttemptID && journal.State == "closed" {
			result.Evidence.SenderJournals[journal.SenderBoundaryID] = raw
			result.Evidence.SenderSHA256[journal.SenderBoundaryID] = observationBytesSHA(raw)
			return true
		}
	case "closure.json":
		closure, err := DecodeObservationDispatchClosure(raw)
		if err == nil && closure.ParentAttemptID == parent.Identity.AttemptID {
			result.Evidence.Closures = append(result.Evidence.Closures, closure)
			return true
		}

	}
	result.Issues = appendObservationIssue(result.Issues, "journal_entry_invalid")
	return false
}

func sameObservationResultSnapshot(child, parent *int64) bool {
	if child == nil || parent == nil {
		return child == nil && parent == nil
	}
	return *child == *parent
}
