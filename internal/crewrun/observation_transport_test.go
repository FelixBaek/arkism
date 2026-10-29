package crewrun

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func transportTestParent(t *testing.T) AttemptObservation {
	t.Helper()
	parent := observerTestRecord(t, observerTestContext(), "main", "transport", "reserved", 1, 0)
	parent.UsageRole = "transport_aggregate"
	parent.LeafCoverage = "unknown"
	return observerReseal(t, parent)
}
func transportTestLeaf(t *testing.T, parent AttemptObservation, index int, cost float64, basis string) (AttemptObservation, ObservationParentLinkEvidence) {
	t.Helper()
	identity := parent.Identity
	identity.AttemptID = ""
	identity.IdentityBasis = "observed_host_leaf"
	identity.CanonicalAttemptID = nil
	identity.AttemptRef = nil
	identity.ParentAttemptID = &parent.Identity.AttemptID
	identity.CallKey = "host-" + strings.Repeat("x", index+1)
	usage := UnknownUsageObservation()
	if basis != "unknown" {
		usage.CostUSD = &cost
	}
	usage.CostBasis = basis
	zero := int64(0)
	if basis != "unknown" {
		usage.InputTokens = &zero
		usage.OutputTokens = &zero
	}
	usage.InputBasis = basis
	usage.OutputBasis = basis
	usage.UsageBasis = basis
	record, err := SealAttemptObservation(AttemptObservation{ContractID: CrewObservationContractID, Identity: identity, Task: parent.Task, Status: "failed", DispatchState: "dispatched", Usage: usage, ErrorCode: "failed", ObservationRevision: 1, UsageRole: "provider_leaf", LeafCoverage: "not_applicable"})
	if err != nil {
		t.Fatal(err)
	}
	run := parent.Identity.Run
	proof, err := SealObservationParentLink(ObservationParentLinkEvidence{ContractID: CrewObservationContractID, Scope: run.Scope, Task: parent.Task, RootRunCollection: *run.RootRunCollection, RootRunID: *run.RootRunID, ParentAttemptID: parent.Identity.AttemptID, ChildAttemptID: record.Identity.AttemptID, ParentRunRef: run.RunRef, ChildRunRef: run.RunRef, Relationship: "host_leaf", SourceSHA256: parent.RecordSHA256})
	if err != nil {
		t.Fatal(err)
	}
	return record, proof
}
func transportCompleted(t *testing.T, basis string, cost float64) (*ObservationTransport, AttemptObservation) {
	t.Helper()
	parent := transportTestParent(t)
	transport, err := NewObservationTransport(parent)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := OpenObservationJournalWriter(transport.Request())
	if err != nil {
		t.Fatal(err)
	}
	record, proof := transportTestLeaf(t, parent, 0, cost, basis)
	if err = writer.Begin(record.Identity.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = writer.Record(record, proof); err != nil {
		t.Fatal(err)
	}
	if err = writer.CloseBoundary(); err != nil {
		t.Fatal(err)
	}
	return transport, record
}
func TestObservationTransportClosedRoundtripAndPrivateCleanup(t *testing.T) {
	transport, record := transportCompleted(t, "provider_reported", .5)
	request := transport.Request()
	info, err := os.Stat(request.JournalDir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("directory not private")
	}
	raw, _ := json.Marshal(request)
	decoded, err := DecodeObservationTransportRequest(raw)
	if err != nil || decoded.Parent.RecordSHA256 != request.Parent.RecordSHA256 {
		t.Fatal(err)
	}
	read, err := transport.Read()
	if err != nil || read.Coverage != "complete" || len(read.Records) != 1 || read.Records[0].RecordSHA256 != record.RecordSHA256 || read.Aggregate.CostUSD == nil || *read.Aggregate.CostUSD != .5 {
		t.Fatalf("read %+v %v", read, err)
	}
	for _, entry := range read.Records {
		raw, _ := json.Marshal(entry)
		if bytes.Contains(raw, []byte("prompt")) {
			t.Fatal("body in journal")
		}
	}
	if err = transport.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(request.JournalDir); !os.IsNotExist(err) {
		t.Fatal("own directory remains", err)
	}
}
func TestObservationTransportEstimatedZeroStaysEstimated(t *testing.T) {
	transport, _ := transportCompleted(t, "estimated", 0)
	defer func() { _ = transport.Close() }()
	read, err := transport.Read()
	if err != nil {
		t.Fatal(err)
	}
	if read.Aggregate.CostBasis != "estimated" || read.Aggregate.InputBasis != "estimated" || read.Aggregate.UsageBasis != "estimated" {
		t.Fatalf("estimate promoted %+v", read.Aggregate)
	}
}
func TestObservationTransportDuplicateBeginMarkerIsMalformed(t *testing.T) {
	transport, record := transportCompleted(t, "provider_reported", .5)
	defer func() { _ = transport.Close() }()
	path := filepath.Join(transport.Request().JournalDir, observationIDName("begin-", record.Identity.AttemptID))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"attempt_id":`), []byte(`"attempt_id":"ignored","attempt_id":`), 1)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	read, err := transport.Read()
	if err != nil || len(read.Records) != 1 || read.Coverage != "partial" {
		t.Fatalf("malformed input:coverage=%s records=%d issues=%v err=%v", read.Coverage, len(read.Records), read.Issues, err)
	}
}
func TestObservationTransportChildHelper(t *testing.T) {
	if os.Getenv("ARKISM_OBSERVATION_TEST_CHILD") != "1" {
		return
	}
	raw, err := os.ReadFile(os.Getenv("ARKISM_OBSERVATION_TEST_REQUEST"))
	if err != nil {
		os.Exit(91)
	}
	req, err := DecodeObservationTransportRequest(raw)
	if err != nil {
		os.Exit(92)
	}
	writer, err := OpenObservationJournalWriter(req)
	if err != nil {
		os.Exit(93)
	}
	record, proof := transportTestLeaf(t, req.Parent, 0, .75, "provider_reported")
	if writer.Begin(record.Identity.AttemptID) != nil || writer.Record(record, proof) != nil {
		os.Exit(94)
	}
	next, _ := transportTestLeaf(t, req.Parent, 1, 0, "unknown")
	if writer.Begin(next.Identity.AttemptID) != nil {
		os.Exit(95)
	}
	os.Exit(23)
}
func TestObservationTransportProcessDeathKeepsEarlierFailedCharge(t *testing.T) {
	transport, err := NewObservationTransport(transportTestParent(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transport.Close() }()
	request := filepath.Join(t.TempDir(), "request.json")
	raw, _ := json.Marshal(transport.Request())
	if err = os.WriteFile(request, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestObservationTransportChildHelper$")
	command.Env = []string{"ARKISM_OBSERVATION_TEST_CHILD=1", "ARKISM_OBSERVATION_TEST_REQUEST=" + request}
	if err = command.Run(); err == nil {
		t.Fatal("child did not die")
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 23 {
		t.Fatalf("unexpected helper failure %v", err)
	}
	read, err := transport.Read()
	if err != nil || read.Coverage != "partial" || len(read.Records) != 1 || read.Records[0].Usage.CostUSD == nil || *read.Records[0].Usage.CostUSD != .75 {
		t.Fatalf("earlier paid failure lost %+v %v", read, err)
	}
}
func TestObservationTransportForeignReplacementIsPreserved(t *testing.T) {
	transport, err := NewObservationTransport(transportTestParent(t))
	if err != nil {
		t.Fatal(err)
	}
	path := transport.Request().JournalDir
	original := path + "-old"
	if err = os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(original) }()
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(path, "foreign")
	if err = os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(path) }()
	if err = transport.Close(); err == nil {
		t.Fatal("replacement accepted")
	}
	if raw, e := os.ReadFile(sentinel); e != nil || string(raw) != "keep" {
		t.Fatal("foreign replacement removed")
	}
}
func TestObservationTransportMalformedOrSymlinkEntryKeepsValidRows(t *testing.T) {
	transport, _ := transportCompleted(t, "provider_reported", .5)
	defer func() { _ = transport.Close() }()
	path := transport.Request().JournalDir
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(path, "record-link.json")); err != nil {
		t.Fatal(err)
	}
	read, err := transport.Read()
	if err != nil || read.Coverage != "partial" || len(read.Records) != 1 {
		t.Fatal("valid charge discarded", err)
	}
	if err = transport.Close(); err == nil {
		t.Fatal("foreign symlink silently deleted")
	}
	if raw, e := os.ReadFile(foreign); e != nil || string(raw) != "keep" {
		t.Fatal("foreign target changed")
	}
	_ = os.Remove(filepath.Join(path, "record-link.json"))
	_ = os.Remove(path)
}

func TestObservationTransportLocalBoundaryKeepsOutsideAncestor(t *testing.T) {
	parent := transportTestParent(t)
	ancestor := "crw2-attempt-" + strings.Repeat("c", 64)
	parent.Identity.ParentAttemptID = &ancestor
	parent = observerReseal(t, parent)
	transport, err := NewObservationTransport(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transport.Close() }()
	writer, err := OpenObservationJournalWriter(transport.Request())
	if err != nil {
		t.Fatal(err)
	}
	child, proof := transportTestLeaf(t, parent, 0, .4, "provider_reported")
	if err = writer.Begin(child.Identity.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = writer.Record(child, proof); err != nil {
		t.Fatal(err)
	}
	if err = writer.CloseBoundary(); err != nil {
		t.Fatal(err)
	}
	read, err := transport.Read()
	if err != nil || read.Coverage != "complete" || len(read.Records) != 1 {
		t.Fatalf("local boundary rejected outside ancestor: %s %v %v", read.Coverage, read.Issues, err)
	}
	actual := transport.Request().Parent
	if actual.RecordSHA256 != parent.RecordSHA256 || actual.Identity.AttemptID != parent.Identity.AttemptID || actual.Identity.ParentAttemptID == nil || *actual.Identity.ParentAttemptID != ancestor {
		t.Fatal("outer identity changed")
	}
	collector := NewAttemptObserver(nil)
	collector.Observe(parent)
	collector.Observe(child)
	if _, err = QualifyObservationLedger(collector.Snapshot(), read.Evidence); err == nil {
		t.Fatal("local boundary pretended outside ancestry verified")
	}
}

func TestObservationTransportStrictRequestAndOwnership(t *testing.T) {
	transport, _ := transportCompleted(t, "provider_reported", .2)
	defer func() { _ = transport.Close() }()
	raw, _ := json.Marshal(transport.Request())
	for _, bad := range [][]byte{append([]byte(`{"protocol":"duplicate",`), raw[1:]...), bytes.Replace(raw, []byte(`"protocol":`), []byte(`"unknown":`), 1), bytes.Replace(raw, []byte(`"journal_dir":`), []byte(`"journal_dir":null,"duplicate":`), 1)} {
		if _, err := DecodeObservationTransportRequest(bad); err == nil {
			t.Fatal("invalid wire request accepted")
		}
	}
	request := transport.Request()
	request.Parent.Task.TaskID = "foreign"
	request.Parent = observerReseal(t, request.Parent)
	if writer, err := OpenObservationJournalWriter(request); err == nil {
		_ = writer.CloseBoundary()
		t.Fatal("foreign parent reused journal")
	}
	request = transport.Request()
	alias := filepath.Join(t.TempDir(), "arkism-observation-alias")
	if err := os.Symlink(request.JournalDir, alias); err != nil {
		t.Fatal(err)
	}
	request.JournalDir = alias
	if writer, err := OpenObservationJournalWriter(request); err == nil {
		_ = writer.CloseBoundary()
		t.Fatal("symlink journal accepted")
	}
}
func TestObservationTransportExhaustionRetainsEarlierCharges(t *testing.T) {
	parent := transportTestParent(t)
	transport, err := NewObservationTransport(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transport.Close() }()
	writer, err := OpenObservationJournalWriter(transport.Request())
	if err != nil {
		t.Fatal(err)
	}
	child, proof := transportTestLeaf(t, parent, 0, .7, "provider_reported")
	if err = writer.Begin(child.Identity.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = writer.Record(child, proof); err != nil {
		t.Fatal(err)
	}
	writer.journal.bytes = observationJournalTotalBytes
	later, _ := transportTestLeaf(t, parent, 1, 0, "unknown")
	if err = writer.Begin(later.Identity.AttemptID); err == nil {
		t.Fatal("journal exhaustion ignored")
	}
	if err = writer.CloseBoundary(); err == nil {
		t.Fatal("exhaustion claimed complete")
	}
	read, err := transport.Read()
	if err != nil || len(read.Records) != 1 || read.Records[0].Usage.CostUSD == nil || *read.Records[0].Usage.CostUSD != .7 || read.Coverage != "partial" {
		t.Fatal("exhaustion discarded known failed charge", err)
	}
}
func TestObservationTransportBadProofKeepsPaidRecord(t *testing.T) {
	parent := transportTestParent(t)
	transport, err := NewObservationTransport(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transport.Close() }()
	writer, err := OpenObservationJournalWriter(transport.Request())
	if err != nil {
		t.Fatal(err)
	}
	child, proof := transportTestLeaf(t, parent, 0, .9, "provider_reported")
	if err = writer.Begin(child.Identity.AttemptID); err != nil {
		t.Fatal(err)
	}
	proof.ParentRunRef.ID = "foreign"
	if err = writer.Record(child, proof); err == nil {
		t.Fatal("foreign proof accepted")
	}
	_ = writer.CloseBoundary()
	read, err := transport.Read()
	if err != nil || len(read.Records) != 1 || read.Records[0].Usage.CostUSD == nil || *read.Records[0].Usage.CostUSD != .9 || read.Coverage != "partial" {
		t.Fatal("proof failure discarded known charge", err)
	}
}

func TestObservationTransportExactDispatchedChildBoundary(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AttemptObservation)
	}{
		{"run_revision", func(r *AttemptObservation) { r.Identity.Run.RunRef.Revision++ }},
		{"node", func(r *AttemptObservation) { r.Identity.NodeID = "other-node" }},
		{"node_attempt", func(r *AttemptObservation) { r.Identity.NodeAttempt++ }},
		{"unknown_to_known_result_revision", func(r *AttemptObservation) { v := int64(9); r.Identity.Run.ResultRevision = &v }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := transportTestParent(t)
			child, proof := transportTestLeaf(t, parent, 1, .4, "provider_reported")
			c.mutate(&child)
			child = observerReseal(t, child)
			proof.ChildAttemptID = child.Identity.AttemptID
			proof.ChildRunRef = child.Identity.Run.RunRef
			proof.RecordSHA256 = ""
			var err error
			proof, err = SealObservationParentLink(proof)
			if err != nil {
				t.Fatal(err)
			}
			transport, err := NewObservationTransport(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = transport.Close() }()
			writer, err := OpenObservationJournalWriter(transport.Request())
			if err != nil {
				t.Fatal(err)
			}
			if err = writer.Begin(child.Identity.AttemptID); err != nil {
				t.Fatal(err)
			}
			if err = writer.Record(child, proof); err == nil {
				t.Error("writer accepted foreign dispatched child")
			}
			_ = writer.CloseBoundary()
			completed, valid := transportCompleted(t, "provider_reported", .2)
			defer func() { _ = completed.Close() }()
			raw, _ := json.Marshal(child)
			if err = os.WriteFile(filepath.Join(completed.Request().JournalDir, observationRecordName(child)), raw, 0600); err != nil {
				t.Fatal(err)
			}
			read, err := completed.Read()
			if err != nil || read.Coverage != "partial" || len(read.Records) != 1 || read.Records[0].RecordSHA256 != valid.RecordSHA256 {
				t.Fatal("reader accepted foreign child or discarded valid charge", err)
			}
		})
	}
}

func TestObservationTransportExactKnownResultSnapshot(t *testing.T) {
	for _, kind := range []string{"matching", "missing", "different"} {
		t.Run(kind, func(t *testing.T) {
			parent := transportTestParent(t)
			value := int64(7)
			parent.Identity.Run.ResultRevision = &value
			parent = observerReseal(t, parent)
			child, proof := transportTestLeaf(t, parent, 0, .3, "provider_reported")
			switch kind {
			case "missing":
				child.Identity.Run.ResultRevision = nil
			case "different":
				v := int64(8)
				child.Identity.Run.ResultRevision = &v
			}
			child = observerReseal(t, child)
			transport, err := NewObservationTransport(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = transport.Close() }()
			writer, err := OpenObservationJournalWriter(transport.Request())
			if err != nil {
				t.Fatal(err)
			}
			if err = writer.Begin(child.Identity.AttemptID); err != nil {
				t.Fatal(err)
			}
			err = writer.Record(child, proof)
			if kind == "matching" {
				if err != nil {
					t.Fatal("matching result snapshot rejected", err)
				}
			} else if err == nil {
				t.Fatal("unobserved result revision accepted")
			}
			_ = writer.CloseBoundary()
			if kind != "matching" {
				raw, _ := json.Marshal(child)
				if err = os.WriteFile(filepath.Join(transport.Request().JournalDir, observationRecordName(child)), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			read, err := transport.Read()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "matching" {
				if read.Coverage != "complete" || len(read.Records) != 1 {
					t.Fatal("matching snapshot not complete")
				}
			} else if read.Coverage != "partial" || len(read.Records) != 0 {
				t.Fatal("reader accepted unobserved result revision")
			}
		})
	}
}
