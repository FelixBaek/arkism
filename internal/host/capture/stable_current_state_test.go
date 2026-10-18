package capture

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// A later SessionStart is a new disclosure. Previously delivered context cannot
// be recalled from the model, but a withdrawn memory must not be sent again.
func TestStableReplayDoesNotRediscloseRemovedMemory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("HOME", root)
	t.Setenv("ARKISM_WORKSPACE", "w")
	t.Setenv("ARKISM_PROJECT", "p")
	t.Setenv("ARKISM_USER", "u")
	t.Setenv("ARKISM_AGENT", "")
	t.Setenv("ARKISM_WORK_AGENT", "")
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_REPO", "")
	t.Setenv("ARKISM_SESSION_PROCESSOR", "0")
	t.Setenv("ARKISM_MEMORY_OUTCOME_SPOOL", "")
	standing := stableFake(t)
	oldScope := capScope
	capScope = host.Scope
	t.Cleanup(func() { capScope = oldScope })
	item := memoryHit("current-state-rule", 7, "")
	item["kind"], item["text"] = "user", "synthetic withdrawn standing rule"
	item["status"], item["approved_by"] = "active", "reviewer"
	*standing = []any{item}
	part, delivery, err := stableBlock("current-state-session", root)
	if err != nil || !strings.Contains(part.Full, item["text"].(string)) {
		t.Fatal("initial approved synthetic rule was not delivered", err)
	}
	markStableShown("current-state-session", part.Full, delivery, root)
	*standing = nil // Current authorized view no longer contains this memory.
	again, _, _ := stableBlock("current-state-session", root)
	if strings.Contains(again.Full, item["text"].(string)) || strings.Contains(again.Meta, "current-state-rule") {
		t.Fatal("new SessionStart redisclosed a memory removed from the current authorized view")
	}
}

// These mutate the synthetic current view after the real renderer saved a block.
// A new disclosure must be refused without minting fresh evidence over old bytes.
func TestStableReplayRejectsChangedCurrentSnapshot(t *testing.T) {
	for _, mode := range []string{"revision", "body_same_revision", "source_same_revision", "quarantine", "approval", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			standing := stableFake(t)
			item := memoryHit("synthetic-stable-rule", 7, "")
			item["kind"], item["text"] = "user", "synthetic old approved text"
			*standing = []any{item}
			first, delivery, e := stableBlock("state", root)
			if e != nil {
				t.Fatal(e)
			}
			markStableShown("state", first.Full, delivery, root)
			original, _ := sessionStore().Stable("state")
			switch mode {
			case "revision":
				item["revision"] = 8
				item["text"] = "synthetic revised text"
			case "body_same_revision":
				item["text"] = "synthetic privacy correction"
			case "source_same_revision":
				item["source_state"] = "synthetic corrected source without memory revision"
			case "quarantine":
				item["privacy_quarantined"] = true
			case "approval":
				item["approved_by"] = ""
			case "superseded":
				item["status"] = "superseded"
			}
			for attempt := 0; attempt < 2; attempt++ {
				next, _, e := stableBlock("state", root)
				if e == nil || next.Full != "" || next.Meta != "" {
					t.Fatal("stale snapshot was redisclosed", mode, e)
				}
				saved, _ := sessionStore().Stable("state")
				if saved.BlockHash != original.BlockHash || saved.Fingerprint != original.Fingerprint || saved.Refs[0].Fingerprint != original.Refs[0].Fingerprint {
					t.Fatal("old body was rebound to current evidence")
				}
			}
		})
	}
}

func TestStableReplayRejectsRehashedCacheTampering(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	stableFake(t)
	first, delivery, e := stableBlock("tamper", root)
	if e != nil {
		t.Fatal(e)
	}
	markStableShown("tamper", first.Full, delivery, root)
	b, _ := sessionStore().Stable("tamper")
	b.Full += "\nsynthetic injected stale body"
	b.BlockHash = stableBlockHash(b)
	// Updating a public checksum cannot update the server's integrity binding.
	if e = sessionStore().ClearStable("tamper"); e != nil {
		t.Fatal(e)
	}
	if _, e = sessionStore().SaveStable("tamper", b); e != nil {
		t.Fatal(e)
	}
	next, _, e := stableBlock("tamper", root)
	if e == nil || next.Full != "" {
		t.Fatal("a rehashed tampered body bypassed the original seal")
	}
}

func TestStableReplayValidationOutageHasNoOldTextFallback(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	stableFake(t)
	first, delivery, e := stableBlock("outage", root)
	if e != nil {
		t.Fatal(e)
	}
	markStableShown("outage", first.Full, delivery, root)
	original, _ := sessionStore().Stable("outage")
	post := capPost
	calls := 0
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			calls++
			if d <= 0 || d > 2*time.Second {
				t.Fatal("unbounded call")
			}
			return nil, errTimeout
		}
		return post(path, body, d)
	}
	next, _, e := stableBlock("outage", root)
	if e == nil || next.Full != "" || calls != 1 {
		t.Fatal("validation outage replayed old data", e, calls)
	}
	saved, _ := sessionStore().Stable("outage")
	if saved.Fingerprint != original.Fingerprint {
		t.Fatal("outage silently replaced snapshot")
	}
	capPost = post
	unchanged, _, e := stableBlock("outage", root)
	if e != nil || unchanged.Full != first.Full {
		t.Fatal("unchanged current cache could not recover", e)
	}
}

func TestStableReplayRejectsCurrentDigestChange(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	stableFake(t)
	get := capGet
	digest := testDigest
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/digests/user/context?") {
			return map[string]any{"authority": "data_only_untrusted", "text": digest}, nil
		}
		return get(path, d)
	}
	first, delivery, e := stableBlock("digest", root)
	if e != nil {
		t.Fatal(e)
	}
	markStableShown("digest", first.Full, delivery, root)
	digest = "" // approval withdrawn; new SessionStart cannot repeat the old summary.
	next, _, e := stableBlock("digest", root)
	if e == nil || next.Full != "" {
		t.Fatal("withdrawn digest was replayed")
	}
}

func TestStableReplayUsesOneSharedDeadlineWithoutSerialMemoryReads(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	standing := stableFake(t)
	*standing = nil
	for i := 0; i < 50; i++ {
		item := memoryHit(fmt.Sprint("synthetic-", i), 1, "")
		item["kind"] = "user"
		*standing = append(*standing, item)
	}
	first, delivery, e := stableBlock("batch", root)
	if e != nil {
		t.Fatal(e)
	}
	markStableShown("batch", first.Full, delivery, root)
	get, post := capGet, capPost
	var previous time.Duration = 2 * time.Second
	memoryReads, batches := 0, 0
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories") {
			memoryReads++
		}
		if d <= 0 || d > previous {
			t.Fatal("preview reset the common deadline", d, previous)
		}
		previous = d
		time.Sleep(3 * time.Millisecond)
		return get(path, d)
	}
	capPost = func(path string, body any, d time.Duration) (map[string]any, error) {
		if path == "/v1/context/validate" {
			batches++
			if d <= 0 || d > previous {
				t.Fatal("batch reset the common deadline")
			}
		}
		return post(path, body, d)
	}
	next, _, e := stableBlock("batch", root)
	if e != nil || next.Full != first.Full || memoryReads != 0 || batches != 1 {
		t.Fatal("replay should validate one batch with no individual memory reads", e, memoryReads, batches)
	}
}

func TestStableLocalDedupRejectsChangedReferenceMetadata(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	stableFake(t)
	first, delivery, e := stableBlock("ref-tamper", root)
	if e != nil {
		t.Fatal(e)
	}
	markStableShown("ref-tamper", first.Full, delivery, root)
	b, _ := sessionStore().Stable("ref-tamper")
	b.Refs[0].ID = "synthetic-unrelated-memory"
	b.Refs[0].Revision = 99
	if e = sessionStore().ClearStable("ref-tamper"); e != nil {
		t.Fatal(e)
	}
	if _, e = sessionStore().SaveStable("ref-tamper", b); e != nil {
		t.Fatal(e)
	}
	if ids := currentStableMemoryIDs("ref-tamper"); len(ids) != 0 {
		t.Fatal("changed metadata suppressed a fresh authorized search hit")
	}
}
