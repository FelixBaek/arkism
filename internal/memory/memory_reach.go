package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Memory reach (ST-824 / FR-012): where an active memory is recalled inside its one Scope.
// personal: preferences, tone and reporting, recalled everywhere. general: work rules for any
// project. project: only when the caller's project key equals ReachProject. The key only narrows
// what one Scope returns; Scope, RLS and permissions stay the boundary (alternative A rejected).
const (
	ReachPersonal = "personal"
	ReachGeneral  = "general"
	ReachProject  = "project"

	reachOriginMigration = "migration"
	reachOriginHuman     = "human"
)

// memoryReach is the stored reach, or the default every memory had before ST-824:
// user memories are personal, the rest general. Unmigrated rows therefore recall as before.
func memoryReach(r Record) string {
	if r.Reach != "" {
		return r.Reach
	}
	if r.Kind == "user" {
		return ReachPersonal
	}
	return ReachGeneral
}

// reachVisible: a project memory shows only for its own project key; an empty key shows none.
func reachVisible(reach, reachProject, projectKey string) bool {
	return reach != ReachProject || projectKey != "" && reachProject == projectKey
}

func validProjectKey(key string) bool {
	return key == "" || validRunLabel(key, 256) && !containsSecret(key)
}

// MemoryReachInput changes one active memory's reach. Only a human principal may (ADR-0022).
type MemoryReachInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reach            string `json:"reach"`
	ReachProject     string `json:"reach_project,omitempty"`
}

func (s *Service) SetMemoryReach(ctx context.Context, token, id string, in MemoryReachInput) (Record, error) {
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.ExpectedRevision < 1 || !validRunLabel(id, 256) || containsSecret(id) {
		return Record{}, ErrInvalid
	}
	in.ReachProject = strings.ToLower(strings.TrimSpace(in.ReachProject)) // same form host.RemoteKey sends
	switch in.Reach {
	case ReachPersonal, ReachGeneral:
		if in.ReachProject != "" {
			return Record{}, ErrInvalid
		}
	case ReachProject:
		if in.ReachProject == "" || !validProjectKey(in.ReachProject) {
			return Record{}, ErrInvalid
		}
	default:
		return Record{}, ErrInvalid
	}
	return s.editLiveMemory(ctx, token, id, in.Scope, in.ExpectedRevision, "set_reach:memories", func(r *Record) error {
		r.Reach, r.ReachProject, r.ReachOrigin = in.Reach, in.ReachProject, reachOriginHuman
		return nil
	})
}

// editLiveMemory is the one human-only metadata edit of a live memory: it never
// changes text or approval, bumps the revision and writes one audit event.
// expected 0 skips the revision check (tag edits); errMemoryUnchanged skips the write.
func (s *Service) editLiveMemory(ctx context.Context, token, id string, scope Scope, expected int64, auditAction string, mutate func(*Record) error) (Record, error) {
	actor, e := s.authorize(ctx, token, "transition:memories", scope, id, "")
	if e != nil {
		return Record{}, e
	}
	if actor.Role != "human" {
		return Record{}, ErrDenied
	}
	if s.Repo == nil {
		return Record{}, ErrUnavailable
	}
	var out Record
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = Record{}
		r, e := tx.Get(ctx, colMemories, scope, id)
		if e != nil {
			return e
		}
		if r.PrivacyQuarantined || r.ContentErased {
			return ErrNotFound
		}
		if r.ConflictID != "" || expected > 0 && r.Revision != expected || !live(r) {
			return ErrConflict
		}
		if e = mutate(&r); errors.Is(e, errMemoryUnchanged) {
			out = r
			return nil
		} else if e != nil {
			return e
		}
		prev := r.Revision
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if e = tx.Replace(ctx, colMemories, r, prev); e != nil {
			return e
		}
		if e = audit(ctx, tx, scope, actor.PrincipalID, auditAction, id); e != nil {
			return e
		}
		out = r
		return queueMemoryEvent(ctx, tx, EventMemoryUpdated, colMemories, r)
	})
	if e != nil {
		return Record{}, e
	}
	return out, nil
}

var errMemoryUnchanged = errors.New("memory_unchanged")

// MigrateMemoryReach writes the default reach onto memories that have none (apply), or removes
// only what an earlier apply wrote (revert). It keeps each revision, so recall, history and
// revision-bound references are unchanged; running either direction twice changes nothing more.
// Offline administrator command (memoryd -memory-reach); never run as part of a deployment.
func (p *Postgres) MigrateMemoryReach(ctx context.Context, revert bool) (int, error) {
	rows, err := p.q().Query(ctx, `SELECT workspace_id, project_id, user_id, agent_id, id FROM memory_records WHERE collection=$1 ORDER BY workspace_id, project_id, id`, colMemories)
	if err != nil {
		return 0, pgErr(err)
	}
	type target struct {
		s  Scope
		id string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.s.WorkspaceID, &t.s.ProjectID, &t.s.UserID, &t.s.AgentID, &t.id); err != nil {
			rows.Close()
			return 0, pgErr(err)
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, pgErr(err)
	}
	changed := 0
	for _, t := range targets {
		err = p.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			r, e := tx.Get(ctx, colMemories, t.s, t.id)
			if errors.Is(e, ErrNotFound) || e == nil && r.ContentErased {
				return nil // deleted since the listing, or erased
			}
			if e != nil {
				return e
			}
			if revert {
				if r.ReachOrigin != reachOriginMigration {
					return nil
				}
				r.Reach, r.ReachProject, r.ReachOrigin = "", "", ""
			} else {
				if r.Reach != "" {
					return nil
				}
				r.Reach, r.ReachOrigin = memoryReach(r), reachOriginMigration
			}
			if e = tx.Replace(ctx, colMemories, r, r.Revision); e != nil {
				return e
			}
			changed++
			return nil
		})
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
