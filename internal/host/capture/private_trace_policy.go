package capture

import (
	"bufio"
	"os"
	"path/filepath"
)

// Behavior traces carry individually redacted records. They do not upload the
// raw transcript; raw archive uploads still skip an entire marked session.
func tracePolicySource(policy *archivePolicy, transcript string, cwd any) error {
	if err := policy.addPath(cwd, false); err != nil {
		return err
	}
	if transcript != "" {
		policy.paths[archivePathDir(transcript)] = struct{}{}
	}
	if err := evidencePolicyCheck(policy); err != nil {
		return err
	}
	if transcript == "" {
		return nil
	}
	info, err := os.Lstat(transcript)
	if os.IsNotExist(err) {
		return nil // Historical reference-only traces need no readable transcript.
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > archiveMaxBytes {
		return valueErr("session_trace_source_invalid")
	}
	if err := policy.inspect(transcript, "jsonl"); err != nil {
		return err
	}
	return evidencePolicyCheck(policy)
}

func tracePolicyRow(policy *archivePolicy, row map[string]any, inspected map[string]bool) error {
	if privatePayload([]any{row["origin"], row["session"], row["ts"], row["scope"], row["refs"], row["deliveries"], row["transcript"], row["cwd"], row["repository"]}) {
		return errLedgerInvalid
	}
	if err := policy.addPath(row["repository"], false); err != nil {
		return err
	}
	transcript, _ := row["transcript"].(string)
	if inspected[transcript] {
		if err := policy.addPath(row["cwd"], false); err != nil {
			return err
		}
		return evidencePolicyCheck(policy)
	}
	if err := tracePolicySource(policy, transcript, row["cwd"]); err != nil {
		return err
	}
	inspected[transcript] = true
	return nil
}

// Check before claiming, as a claim is itself a durable state transition.
func traceLedgerPolicy(path string) (*archivePolicy, error) {
	policy := newArchivePolicy()
	if privatePayload(sharedScope()) {
		return policy, errLedgerInvalid
	}
	if err := evidencePolicyCheck(policy); err != nil {
		return policy, err
	}
	f, err := os.Open(path)
	if err != nil {
		return policy, err
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	lines.Buffer(nil, 1<<20)
	inspected := map[string]bool{}
	for lines.Scan() {
		row, err := loadsMap(lines.Text())
		if err != nil {
			return policy, errLedgerInvalid
		}
		if err := tracePolicyRow(policy, row, inspected); err != nil {
			return policy, err
		}
	}
	if err := lines.Err(); err != nil {
		return policy, err
	}
	return policy, evidencePolicyCheck(policy)
}

// Metadata and behavior come from the same private, stable copy. Appending or
// changing the live transcript cannot introduce uninspected project metadata.
func traceBehaviorSnapshot(path string) (string, func(), error) {
	cleanup := func() {}
	if path == "" {
		return "", cleanup, nil
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", cleanup, nil
	}
	if err != nil {
		return "", cleanup, err
	}
	if !info.Mode().IsRegular() || info.Size() > archiveMaxBytes {
		return "", cleanup, valueErr("session_trace_source_invalid")
	}
	before, err := archiveSignature(path)
	if err != nil {
		return "", cleanup, err
	}
	dir, err := os.MkdirTemp("", "arkism-trace-inspect-")
	if err != nil {
		return "", cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	target := filepath.Join(dir, "session.jsonl")
	if err := copyFile(path, target); err != nil {
		cleanup()
		return "", func() {}, err
	}
	after, err := archiveSignature(path)
	if err != nil || !eq(before, after) {
		cleanup()
		return "", func() {}, valueErr("session_trace_source_changed")
	}
	return target, cleanup, nil
}
