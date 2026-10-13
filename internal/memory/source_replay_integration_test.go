//go:build integration && linux

package memory

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationSourceReplayChange(t *testing.T) {
	for _, file := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			for _, change := range []string{"text", "sensitivity", "expiry", "revision", "delete"} {
				name := "text/"
				if file {
					name = "file/"
				}
				if replay {
					name += "receipt/"
				} else {
					name += "digest/"
				}
				t.Run(name+change, func(t *testing.T) { exerciseSourceReplayChange(t, isolatedPostgres(t), file, replay, change) })
			}
		}
	}
}

func TestIntegrationSourceReplayDeletionFence(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "text"
		if file {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			repo := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.Repo = repo
			ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
			defer cancel()
			in := sourceInput(CreateInput{Scope: scopeA, Text: "final source race", IdempotencyKey: "race"})
			if file {
				f.svc.BlobDirectory = blobTestDir(t)
			}
			call := func() (Record, error) {
				if !file {
					return f.svc.Create(ctx, f.human, "sources", in)
				}
				out, err := f.svc.IngestFile(ctx, f.human, FileIngestInput{Source: in, Artifact: FileArtifactSpec{Kind: "tool_output", MIME: "text/plain"}}, []byte("fenced file"))
				return out.Source, err
			}
			src, err := call()
			if err != nil {
				t.Fatal(err)
			}
			race := &memoryFenceRaceRepo{Repository: repo, reached: make(chan struct{}), release: make(chan struct{})}
			// Install the barrier after registration/attachment, at the read policy.
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if a.Action == "read:sources" {
					f.svc.Repo = race
				}
				return nil
			}}
			type result struct {
				record Record
				err    error
			}
			done := make(chan result, 1)
			go func() { out, err := call(); done <- result{out, err} }()
			released := false
			defer func() {
				if !released {
					close(race.release)
				}
			}()
			select {
			case <-race.reached:
			case <-ctx.Done():
				t.Fatal("reuse never reached final fence")
			}
			deleter := &Service{Repo: repo, Auth: f.auth}
			if err := deleter.Delete(ctx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); err != nil {
				t.Fatal(err)
			}
			close(race.release)
			released = true
			select {
			case got := <-done:
				if got.err == nil || got.record.ID != "" {
					t.Fatal("deletion committed but stale source returned", got.err)
				}
			case <-ctx.Done():
				t.Fatal("reuse did not finish")
			}
		})
	}
}
