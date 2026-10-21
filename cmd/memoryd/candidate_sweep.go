package main

import (
	"context"
	"github.com/FelixBaek/arkism/internal/memory"
	"os"
	"strconv"
	"strings"
	"time"
)

// Candidate expiry is scoped and opt-in. It never prunes approved memories.
func candidateSweepConfig() (memory.Scope, int, error) {
	raw, scope := os.Getenv("MEMORY_CANDIDATE_EXPIRY_DAYS"), os.Getenv("MEMORY_CANDIDATE_EXPIRY_SCOPE")
	if raw == "" && scope == "" {
		return memory.Scope{}, 0, nil
	}
	days, err := strconv.Atoi(raw)
	parts := strings.Split(scope, "/")
	if err != nil || days < 1 || days > 3650 || len(parts) != 4 || parts[0] == "" || parts[1] == "" {
		return memory.Scope{}, 0, memory.ErrInvalid
	}
	return memory.Scope{WorkspaceID: parts[0], ProjectID: parts[1], UserID: parts[2], AgentID: parts[3]}, days, nil
}

func sweepCandidates(ctx context.Context, s *memory.Service, token string, scope memory.Scope, days int) error {
	if days == 0 {
		return nil
	}
	_, err := s.ExpireCandidates(ctx, token, memory.ExpireCandidatesInput{Scope: scope, Before: time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), Limit: 100})
	return err
}
