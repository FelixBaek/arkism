package main

import (
	"context"
	"errors"
	"github.com/FelixBaek/arkism/internal/memory"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalCodexConfiguration(t *testing.T) {
	svc := new(memory.Service)
	conf := memory.LLMConfig{}
	for _, key := range []string{"MEMORY_LOCAL_TERRA_SKILLS", "MEMORY_LOCAL_TERRA_TEXT"} {
		t.Setenv(key, "")
	}
	if err := configureTerraText(svc, conf, false); err != nil || svc.Analyzer != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMORY_LOCAL_TERRA_TEXT", "on")
	if configureTerraText(svc, conf, false) == nil || configureTerraText(svc, conf, true) != nil {
		t.Fatal("invalid flag")
	}
	t.Setenv("MEMORY_LOCAL_TERRA_TEXT", "1")
	t.Setenv("MEMORY_LOCAL_TERRA_SKILLS", "1")
	t.Setenv("MEMORY_LOCAL_STANDALONE", "false")
	if configureTerraText(svc, conf, false) == nil {
		t.Fatal("accepted outside local mode")
	}
	t.Setenv("MEMORY_LOCAL_STANDALONE", "true")
	t.Setenv("ARKISM_CODEX_BIN", filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(configureTerraText(svc, conf, false), memory.ErrModelNotConfigured) {
		t.Fatal("missing executable")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_CODEX_BIN", bin)
	if err := configureTerraText(svc, conf, false); err != nil || svc.Wiki == nil || svc.ReflectReread == nil || svc.Analyzer == nil || svc.Drafter == nil {
		t.Fatal("configured model not attached", err)
	}
	if svc.Wiki.Generator().Model != terraTextModel {
		t.Fatal("wrong model")
	}
}

func TestLocalCodexExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub is a POSIX executable")
	}
	bin := filepath.Join(t.TempDir(), "codex")
	stub := "#!/bin/sh\nset -eu\nwhile [ \"$1\" != '-o' ]; do shift; done\nprintf '%s' '{\"text\":\"ok\"}' > \"$2\"\nprintf '%s' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":2,\"cached_input_tokens\":1}}'\n"
	if err := os.WriteFile(bin, []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	m := terraText{executable: bin, vision: true}
	text, u, err := m.Complete(t.Context(), "format", "synthetic", 10)
	if err != nil || text != "ok" || u.TotalTokens != 10 {
		t.Fatalf("%q %#v %v", text, u, err)
	}
	if !m.ImageInput() {
		t.Fatal("image input disabled")
	}
	img := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	text, _, err = m.CompleteImage(t.Context(), "format", "synthetic", "image/png", img, 10)
	if err != nil || text != "ok" {
		t.Fatal(err)
	}
	if _, _, err = m.Complete(t.Context(), "format", "data", 1); err == nil {
		t.Fatal("model exceeded budget")
	}
	for _, mime := range []string{"image/jpeg", "image/webp", "text/plain"} {
		if _, _, err = m.CompleteImage(t.Context(), "s", "d", mime, img, 10); err == nil {
			t.Fatal(mime)
		}
	}
	m.vision = false
	if _, _, err = m.CompleteImage(t.Context(), "s", "d", "image/png", img, 10); err == nil {
		t.Fatal("disabled vision")
	}
}
func TestLocalCodexUsage(t *testing.T) {
	b := new(terraEventLog)
	if _, err := b.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readTerraTextUsage(b); ok {
		t.Fatal("invalid event counted")
	}
	if _, err := b.Write([]byte(strings.Repeat("x", (4<<20)+1))); err == nil {
		t.Fatal("oversized events accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := (terraText{}).Complete(ctx, "s", "d", 5); err == nil {
		t.Fatal("accepted missing executable")
	}
}
