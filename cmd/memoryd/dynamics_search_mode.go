package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
)

// daemonSearchMode is the SYS-06 search mode file. Only off and shadow exist:
// shadow keeps returning the baseline and records a separate diagnostic. A
// "dynamic" serving mode is refused until a serving policy is accepted.
type daemonSearchMode struct {
	Protocol       string `json:"protocol"`
	Mode           string `json:"mode"`
	RegistryPath   string `json:"registry_path"`
	RegistrySHA256 string `json:"registry_sha256"`
	Policy         string `json:"policy"`
	NativeVersion  string `json:"native_version"`
	Dimension      string `json:"dimension"`
	DeadlineNS     int64  `json:"deadline_ns"`
	MaxSteps       int64  `json:"max_steps"`
	Capacity       int    `json:"capacity"`
}

// loadDaemonSearchMode returns nil for no file or mode off. A search mode file
// belongs to the HTTP API only, never to a worker or repair run.
func loadDaemonSearchMode(path string, background bool) (*memory.NativeSearchShadowConfig, error) {
	if path == "" {
		return nil, nil
	}
	if background {
		return nil, memory.ErrInvalid
	}
	raw, err := readDaemonDynamicsFile(path)
	if err != nil || !dyn.ValidJSONUnicode(raw) {
		return nil, memory.ErrInvalid
	}
	var m daemonSearchMode
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Protocol != "sys06-search-mode-v1" {
		return nil, memory.ErrInvalid
	}
	switch m.Mode {
	case "off":
		if m != (daemonSearchMode{Protocol: m.Protocol, Mode: "off"}) {
			return nil, memory.ErrInvalid // an off file carries no shadow settings to misread later
		}
		return nil, nil
	case "shadow":
	default:
		return nil, memory.ErrInvalid
	}
	// Join would clean away "..", so the path must be local before it is joined.
	if !filepath.IsLocal(m.RegistryPath) {
		return nil, memory.ErrInvalid
	}
	registryRaw, err := readDaemonDynamicsFile(filepath.Join(filepath.Dir(path), m.RegistryPath))
	if err != nil || memory.ContentHash(string(registryRaw)) != m.RegistrySHA256 || !dyn.ValidJSONUnicode(registryRaw) {
		return nil, memory.ErrInvalid
	}
	var registry dyn.PolicyRegistryV2
	if json.Unmarshal(registryRaw, &registry) != nil {
		return nil, memory.ErrInvalid
	}
	cfg := memory.NativeSearchShadowConfig{Mode: "shadow", Registry: registry, Policy: dyn.PolicyID(m.Policy), NativeVersion: m.NativeVersion, Dimension: m.Dimension, Deadline: time.Duration(m.DeadlineNS), MaxSteps: m.MaxSteps, Capacity: m.Capacity}
	if _, err = memory.NewNativeSearchShadowRuntime(cfg); err != nil || m.NativeVersion == "" || m.Dimension == "" {
		return nil, memory.ErrInvalid
	}
	return &cfg, nil
}
