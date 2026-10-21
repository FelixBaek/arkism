package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/memory"
)

func TestGeneratePrivateTokenFileWithoutDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-token")
	getenv := func(string) string { t.Fatal("generator read environment or connected to database"); return "" }
	if err := runArgs([]string{"--generate-token-file", path}, getenv); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !memory.ValidPrincipalToken(strings.TrimSpace(string(raw))) {
		t.Fatal("invalid token file", err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("token file permissions", err)
	}
	if err := runArgs([]string{"--generate-token-file", path}, getenv); err == nil {
		t.Fatal("existing token overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(raw) != string(after) {
		t.Fatal("existing credential changed", err)
	}
}

func TestRejectAmbiguousTokenCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--revoke", "--rotate-token"}, {"--expires-in", "1s"}, {"--expires-in", "2161h"},
		{"--generate-token-file", "unused", "--principal", "ignored"}, {"--generate-token-file", "unused", "positional-secret"},
	} {
		if err := runArgs(args, func(string) string { t.Fatal("invalid arguments reached environment"); return "" }); err == nil {
			t.Fatal("ambiguous command accepted")
		}
	}
}
