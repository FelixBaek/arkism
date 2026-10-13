package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

func TestCommandLine(t *testing.T) {
	cfg := crewrun.FromEnv()
	cfg.Engine.HooksFile = filepath.Join(t.TempDir(), "crew-hooks.json")
	cfg.Engine.PublicURL = "http://127.0.0.1:8095"
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"nope"}, {"hook-url"}, {"run"}, {"run", "x", "--input", "{bad"}} {
		if code := run(context.Background(), args, &out, cfg); code != 2 {
			t.Fatal("usage error", args, code)
		}
	}
	if code := run(context.Background(), []string{"hook-url", "auto-1"}, &out, cfg); code != 0 {
		t.Fatal("hook-url", code)
	}
	first := out.String()
	if !strings.HasPrefix(first, "POST http://127.0.0.1:8095/hooks/auto-1\nX-Arkism-Hook-Token: ") {
		t.Fatal("hook-url output", first)
	}
	out.Reset()
	_ = run(context.Background(), []string{"hook-url", "auto-1"}, &out, cfg)
	if out.String() != first {
		t.Fatal("hook token changed on the second call")
	}
}
