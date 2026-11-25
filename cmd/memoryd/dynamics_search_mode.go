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

// daemonSearchMode is the SYS-06 search mode file. off and shadow keep returning
// the baseline; shadow records a separate diagnostic. "on" serves only the
// DYN-02/03 fast/slow ranking and needs a fast_slow block. Explicit "dynamic"
// serves the configured registered scalar or fast/slow policy; absent stays off.
type daemonSearchMode struct {
	Protocol       string `json:"protocol"`
	Mode           string `json:"mode"`
	CandidateOnly  bool   `json:"candidate_only,omitempty"`
	RegistryPath   string `json:"registry_path"`
	RegistrySHA256 string `json:"registry_sha256"`
	Policy         string `json:"policy"`
	NativeVersion  string `json:"native_version"`
	Dimension      string `json:"dimension"`
	DeadlineNS     int64  `json:"deadline_ns"`
	MaxSteps       int64  `json:"max_steps"`
	Capacity       int    `json:"capacity"`
	// FastSlow is optional in shadow and required in on.
	FastSlow *daemonSearchFastSlow `json:"fast_slow"`
}

type daemonSearchFastSlow struct {
	SlowDynamics dyn.DynamicsConfigV2 `json:"slow_dynamics"`
	FastWeight   float64              `json:"fast_weight"`
}

// The read-only diagnostic accepts the reviewed B1 registry as published.
const candidateShadowRegistrySHA256 = "78e30b4b53616920ff38ba30bf9584bb89c4fbdc799990c16ec813d5deb4a2c4"

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
	case "shadow", "on", "dynamic":
	default:
		return nil, memory.ErrInvalid
	}
	if !m.validCandidateOnly() {
		return nil, memory.ErrInvalid
	}
	return m.runtimeConfig(filepath.Dir(path))
}

func (m daemonSearchMode) validCandidateOnly() bool {
	return !m.CandidateOnly || (m.Mode == "shadow" && m.Policy == "B1" && m.FastSlow == nil && m.RegistrySHA256 == candidateShadowRegistrySHA256)
}

func (m daemonSearchMode) runtimeConfig(directory string) (*memory.NativeSearchShadowConfig, error) {
	// Join would clean away "..", so the path must be local before it is joined.
	if !filepath.IsLocal(m.RegistryPath) {
		return nil, memory.ErrInvalid
	}
	registryRaw, err := readDaemonDynamicsFile(filepath.Join(directory, m.RegistryPath))
	if err != nil || memory.ContentHash(string(registryRaw)) != m.RegistrySHA256 || !dyn.ValidJSONUnicode(registryRaw) {
		return nil, memory.ErrInvalid
	}
	var registry dyn.PolicyRegistryV2
	if json.Unmarshal(registryRaw, &registry) != nil {
		return nil, memory.ErrInvalid
	}
	cfg := memory.NativeSearchShadowConfig{Mode: m.Mode, Registry: registry, Policy: dyn.PolicyID(m.Policy), NativeVersion: m.NativeVersion, Dimension: m.Dimension, Deadline: time.Duration(m.DeadlineNS), MaxSteps: m.MaxSteps, Capacity: m.Capacity}
	if m.FastSlow != nil {
		cfg.FastSlow = &memory.NativeSearchFastSlow{Slow: m.FastSlow.SlowDynamics, FastWeight: m.FastSlow.FastWeight}
	}
	if _, err = memory.NewNativeSearchShadowRuntime(cfg); err != nil || m.NativeVersion == "" || m.Dimension == "" {
		return nil, memory.ErrInvalid
	}
	cfg.CandidateOnly = m.CandidateOnly
	return &cfg, nil
}
