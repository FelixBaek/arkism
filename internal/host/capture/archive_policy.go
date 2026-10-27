package capture

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errArchivePolicySkipped = errors.New("archive_capture_skipped")

// errArchiveInspectionLimit: a record the policy cannot inspect whole (above
// archiveMetadataRecordBytes, archiveMetadataPaths or archiveMetadataDepth).
const errArchiveInspectionLimit = valueErr("archive_metadata_inspection_limit")

const (
	archiveMetadataRecordBytes = 8 << 20
	archiveMetadataPaths       = 4096
	archiveMetadataDepth       = 64
)

// An archive may have come from a database or synchronized inbox. Its temporary
// snapshot location never replaces the source and recorded project locations.
// The policy is read again before every request, including transport retries.
type archivePolicy struct{ paths map[string]struct{} }

func newArchivePolicy(sourcePaths ...string) *archivePolicy {
	p := &archivePolicy{paths: map[string]struct{}{}}
	for _, path := range sourcePaths {
		if path != "" {
			p.paths[archivePathDir(path)] = struct{}{}
		}
	}
	return p
}

func (p *archivePolicy) check() error {
	settings := readCaptureSettings()
	if captureSkippedWithSettings("", settings) {
		return errArchivePolicySkipped
	}
	for path := range p.paths {
		if captureSkippedWithSettings(path, settings) {
			return errArchivePolicySkipped
		}
	}
	return nil
}

func (c *collector) archiveCaptureSkipped(sourcePaths ...string) bool {
	if newArchivePolicy(sourcePaths...).check() != nil {
		c.counts.inc("capture_skipped")
		return true
	}
	return false
}

func (c *collector) archivePost(policy *archivePolicy, endpoint string, payload map[string]any) (map[string]any, error) {
	if err := policy.check(); err != nil {
		return nil, err
	}
	if client, ok := c.client.(archiveClient); ok {
		client.beforePost = policy.check
		return client.post(endpoint, payload)
	}
	return c.client.post(endpoint, payload)
}

func (p *archivePolicy) inspect(path, format string) error {
	if format == "sqlite_snapshot" {
		return inspectSQLiteArchive(path, p)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		record, done, err := nextArchiveMetadata(r)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if record != nil {
			if err := p.walk(record, 0); err != nil {
				return err
			}
		}
	}
}

func nextArchiveMetadata(r *bufio.Reader) (record any, done bool, err error) {
	line, err := readLine(r, archiveMetadataRecordBytes+1)
	if err != nil {
		return nil, false, err
	}
	if len(line) == 0 {
		return nil, true, nil
	}
	trimmed := bytes.TrimPrefix(bytes.TrimSpace(line), []byte("\xef\xbb\xbf"))
	structured := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	if len(line) > archiveMetadataRecordBytes {
		if structured {
			return nil, false, errArchiveInspectionLimit
		}
		if err := skipArchiveUnstructuredLine(r, line); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if !structured {
		return nil, false, nil
	}
	record, err = readArchiveMetadataRecord(r, trimmed)
	return record, false, err
}

func skipArchiveUnstructuredLine(r *bufio.Reader, line []byte) error {
	// Non-JSON records have no supported project metadata. archiveSnapshot still
	// inspects every raw byte for private markers, including these long records.
	for len(line) > 0 && line[len(line)-1] != '\n' {
		var err error
		line, err = readLine(r, archiveMetadataRecordBytes+1)
		if err != nil {
			return err
		}
	}
	return nil
}

func archivePathKey(key string) (path, file bool) {
	switch strings.ToLower(key) {
	case "cwd", "directory", "worktree", "worktree_path", "working_directory", "workingdirectory", "project_path", "projectpath", "project_root", "projectroot", "root_directory", "working_dir", "project_dir", "projectdirectory", "workspace_path", "workspacepath", "workspace_root", "workspaceroot", "root_path", "rootpath":
		return true, false
	case "file_path", "filepath":
		return true, true
	}
	return false, false
}

func (p *archivePolicy) addPath(value any, file bool) error {
	if list, ok := value.([]any); ok {
		for _, v := range list {
			if err := p.addPath(v, file); err != nil {
				return err
			}
		}
		return nil
	}
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	if len(s) > 32<<10 || strings.ContainsRune(s, 0) {
		return valueErr("archive_metadata_path_invalid")
	}
	if file {
		s = archivePathDir(s)
	}
	p.paths[s] = struct{}{}
	if len(p.paths) > archiveMetadataPaths {
		return errArchiveInspectionLimit
	}
	return nil
}

func (p *archivePolicy) walk(value any, depth int) error {
	if depth > archiveMetadataDepth {
		return errArchiveInspectionLimit
	}
	switch v := value.(type) {
	case map[string]any:
		return p.walkObject(v, depth)

	case []any:
		for _, child := range v {
			if err := p.walk(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		return p.walkJSONText(v, depth)
	}
	return nil
}

func (p *archivePolicy) walkObject(value map[string]any, depth int) error {
	for key, child := range value {
		if isPath, file := archivePathKey(key); isPath {
			if err := p.addPath(child, file); err != nil {
				return err
			}
		}
		if err := p.walk(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (p *archivePolicy) walkJSONText(value string, depth int) error {
	// Zcode message.data and AgentsView metadata may be nested JSON strings.
	text := strings.TrimSpace(value)
	if len(text) <= 1 || (text[0] != '{' && text[0] != '[') {
		return nil
	}
	if len(text) > archiveMetadataRecordBytes {
		return errArchiveInspectionLimit
	}
	var child any
	if json.Unmarshal([]byte(text), &child) == nil {
		return p.walk(child, depth+1)
	}
	return nil
}

// Synchronized exports can retain Windows paths on another operating system.
func archivePathDir(path string) string {
	if strings.Contains(path, `\`) && (len(path) > 1 && path[1] == ':' || strings.HasPrefix(path, `\\`)) {
		path = strings.ReplaceAll(path, `\`, "/")
	}
	parent := filepath.Dir(path)
	if len(parent) == 2 && parent[1] == ':' {
		return parent + "/"
	}
	if strings.HasPrefix(path, "//") && !strings.HasPrefix(parent, "//") {
		return "/" + parent
	}
	return parent
}

// Support both JSONL and pretty-printed JSON exports. A partial record is bounded
// before another line is read, so malformed input cannot grow an unbounded buffer.
func readArchiveMetadataRecord(r *bufio.Reader, record []byte) (any, error) {
	for {
		decoder := json.NewDecoder(bytes.NewReader(record))
		var value any
		err := decoder.Decode(&value)
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				return nil, valueErr("archive_metadata_inspection_failed")
			}
			return value, nil
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, valueErr("archive_metadata_inspection_failed")
		}
		if len(record) >= archiveMetadataRecordBytes {
			return nil, errArchiveInspectionLimit
		}
		line, readErr := readLine(r, archiveMetadataRecordBytes-len(record)+1)
		if readErr != nil {
			return nil, readErr
		}
		if len(line) == 0 {
			return nil, valueErr("archive_metadata_inspection_failed")
		}
		if len(record)+len(line)+1 > archiveMetadataRecordBytes {
			return nil, errArchiveInspectionLimit
		}
		record = append(record, '\n')
		record = append(record, line...)
	}
}
