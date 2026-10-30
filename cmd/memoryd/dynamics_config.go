package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/FelixBaek/arkism/internal/memory"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

// A plan pins both files once. Loading does not execute the offline workload or
// replace its full A validator; only the runtime's consumed bindings are checked.
type daemonDynamicsPlan struct {
	raw    []byte
	config memory.DynamicsLoadConfig
}

func loadDaemonDynamicsPlan(runtimePath, loadPath string) (*daemonDynamicsPlan, error) {
	if runtimePath == "" && loadPath == "" {
		return nil, nil
	}
	if runtimePath == "" || loadPath == "" {
		return nil, memory.ErrInvalid
	}
	raw, err := readDaemonDynamicsFile(runtimePath)
	if err != nil {
		return nil, memory.ErrInvalid
	}
	if _, err = memory.NewDynamicsLoadRuntime(raw); err != nil {
		return nil, memory.ErrInvalid
	}
	var config memory.DynamicsLoadConfig
	if json.Unmarshal(raw, &config) != nil {
		return nil, memory.ErrInvalid
	}
	load, err := readDaemonDynamicsFile(loadPath)
	// The token decoder replaces broken Unicode silently, so refuse it before projecting the bindings.
	if err != nil || !dyn.ValidJSONUnicode(load) || memory.ContentHash(string(load)) != config.LoadConfigSHA256 {
		return nil, memory.ErrInvalid
	}
	projection, err := daemonDynamicsJSONObject(load)
	if err != nil || !daemonDynamicsLoadMatches(projection, config) {
		return nil, memory.ErrInvalid
	}
	return &daemonDynamicsPlan{raw: bytes.Clone(raw), config: config}, nil
}

func daemonDynamicsLoadMatches(p map[string]any, c memory.DynamicsLoadConfig) bool {
	// All top-level A keys must be present with their canonical spelling. The
	// actual pilot still validates schedule/fault semantics with the pinned A tool.
	keys := strings.Fields("protocol run_id seed mode scenario cache start_ns duration_ns arrival_schedule fault_schedule queue_admission_limit batch_limit concurrency_limit retry_limit deadline_ns allowed_freshness_ns quality_criterion_sha256 model_sha256 corpus_sha256")
	if len(p) != len(keys) || p["protocol"] != "sys05-load-config-v1" || p["mode"] != c.Mode {
		return false
	}
	for _, key := range keys {
		if _, ok := p[key]; !ok {
			return false
		}
	}
	l := c.Limits
	for key, want := range map[string]int64{"queue_admission_limit": l.QueueCountLimit, "batch_limit": l.BatchLimit, "concurrency_limit": l.ConcurrencyLimit, "retry_limit": l.RetryLimit, "deadline_ns": l.DeadlineNS, "allowed_freshness_ns": l.AllowedFreshnessNS} {
		number, ok := p[key].(json.Number)
		if !ok {
			return false
		}
		got, err := number.Int64()
		if err != nil || got != want {
			return false
		}
	}
	return true
}

func daemonDynamicsJSONObject(raw []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := daemonDynamicsJSONValue(d, 0)
	if err != nil {
		return nil, memory.ErrInvalid
	}
	p, ok := v.(map[string]any)
	if !ok || d.Decode(new(any)) != io.EOF {
		return nil, memory.ErrInvalid
	}
	return p, nil
}

func daemonDynamicsJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, memory.ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return nil, memory.ErrInvalid
	}
	switch token {
	case json.Delim('{'):
		return daemonDynamicsJSONObjectBody(d, depth)
	case json.Delim('['):
		return daemonDynamicsJSONArrayBody(d, depth)
	default:
		return token, nil
	}
}

func daemonDynamicsJSONObjectBody(d *json.Decoder, depth int) (any, error) {
	result := map[string]any{}
	for d.More() {
		keyToken, e := d.Token()
		key, ok := keyToken.(string)
		if e != nil || !ok || !daemonDynamicsJSONKey(key) {
			return nil, memory.ErrInvalid
		}
		if _, exists := result[key]; exists {
			return nil, memory.ErrInvalid
		}
		value, e := daemonDynamicsJSONValue(d, depth+1)
		if e != nil {
			return nil, e
		}
		result[key] = value
	}
	return result, daemonDynamicsJSONEnd(d, '}')
}

func daemonDynamicsJSONArrayBody(d *json.Decoder, depth int) (any, error) {
	var result []any
	for d.More() {
		v, e := daemonDynamicsJSONValue(d, depth+1)
		if e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, daemonDynamicsJSONEnd(d, ']')
}

func daemonDynamicsJSONEnd(d *json.Decoder, want json.Delim) error {
	if end, e := d.Token(); e != nil || end != want {
		return memory.ErrInvalid
	}
	return nil
}

func daemonDynamicsJSONKey(key string) bool {
	if key == "" {
		return false
	}
	for _, c := range key {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Walk pinned roots and compare each opened inode with its non-symlink entry.
// Nonblocking opens refuse FIFO substitution without hanging startup.
func readDaemonDynamicsFile(path string) ([]byte, error) {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." || strings.ContainsRune(part, 0) {
			return nil, memory.ErrInvalid
		}
	}
	abs, err := filepath.Abs(path)
	volume := filepath.VolumeName(abs)
	if err != nil || strings.HasPrefix(volume, `\\`) {
		return nil, memory.ErrInvalid
	}
	rootPath := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(abs, rootPath), string(filepath.Separator))
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, memory.ErrInvalid
	}
	defer func() { _ = root.Close() }()
	for _, part := range parts[:len(parts)-1] {
		before, e := root.Lstat(part)
		if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			return nil, memory.ErrInvalid
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			return nil, memory.ErrInvalid
		}
		opened, e := next.Stat(".")
		current, afterErr := root.Lstat(part)
		if e != nil || afterErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(opened, current) {
			_ = next.Close()
			return nil, memory.ErrInvalid
		}
		_ = root.Close()
		root = next
	}
	return readDaemonDynamicsLeaf(root, parts[len(parts)-1])
}

func readDaemonDynamicsLeaf(root *os.Root, leaf string) ([]byte, error) {
	const maxBytes = 64 << 20 // Existing A parser bound, not a scheduler default.
	before, err := root.Lstat(leaf)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxBytes {
		return nil, memory.ErrInvalid
	}
	f, err := root.OpenFile(leaf, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, memory.ErrInvalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, memory.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	after, statErr := f.Stat()
	current, pathErr := root.Lstat(leaf)
	if err != nil || statErr != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || int64(len(raw)) != after.Size() || len(raw) > maxBytes {
		return nil, memory.ErrInvalid
	}
	return raw, nil
}
