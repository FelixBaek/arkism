package installtxn

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Metadata checkpoints only the installer's own registration and shortcuts.
// Restore must reject unrelated concurrent changes rather than overwrite them.
// Its implementation owns the scope validation for the checkpoint it accepts.
type Metadata interface {
	Capture() ([]byte, error)
	Apply() error
	Verify() error
	Restore([]byte) error
	// Commit performs idempotent housekeeping after the durable commit record.
	// It must preserve changes the user made to current metadata after commit.
	Commit([]byte) error
}

// Request binds the live install path to a trusted payload and scoped native effects.
type Request struct {
	Live, Source string
	Expected     Manifest
	Metadata     Metadata
	// Stop must prove that processes using this payload have terminated before
	// returning nil. It must use verified process handles, not PID-only kills.
	Stop func(string) error
}

// Result distinguishes a durable commit from recovery/archive housekeeping.
type Result struct {
	Committed bool
	Recovered bool
	Archive   string
}

// Engine owns the ordering of payload and metadata publication. Fault injection
// is private to package tests; the shipping CLI must not accept fault flags.
type Engine struct{ checkpoint func(string) error }

func (e *Engine) point(name string) error {
	if e.checkpoint != nil {
		return e.checkpoint(name)
	}
	return nil
}
func validateRequest(r Request, l layout) error {
	if r.Metadata == nil || r.Stop == nil {
		return errors.New("install_effects_required")
	}
	if err := r.Expected.Validate(); err != nil {
		return err
	}
	source := filepath.Clean(r.Source)
	if source != r.Source || !filepath.IsAbs(source) {
		return errors.New("install_source_path_invalid")
	}
	lower := strings.ToLower(source)
	live := strings.ToLower(l.live)
	versions := strings.ToLower(l.versions)
	for _, path := range []string{live, versions} {
		if lower == path || strings.HasPrefix(lower, path+`\`) || strings.HasPrefix(path, lower+`\`) {
			return errors.New("install_source_overlaps_destination")
		}
	}
	return ordinary(source, true)
}

// Install stages a complete same-volume copy before stopping the old payload.
// A durable journal precedes each rename. The caller must inspect Committed on
// error: failure to archive a committed transaction must never trigger rollback.
func (e *Engine) Install(r Request) (result Result, err error) {
	l, err := paths(r.Live)
	if err != nil {
		return result, err
	}
	if err = validateRequest(r, l); err != nil {
		return result, err
	}
	release, err := openLayout(l)
	if err != nil {
		return result, err
	}
	defer release()
	recovered, err := recoverLocked(l, r.Metadata, r.Stop)
	if err != nil {
		return recovered, err
	}
	result.Recovered = recovered.Recovered
	j, err := begin(r, l)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil && !result.Committed {
			rollback, rollbackErr := recoverLocked(l, r.Metadata, r.Stop)
			result.Recovered = rollback.Recovered
			result.Archive = rollback.Archive
			err = errors.Join(err, rollbackErr)
		}
	}()
	if err = e.preparePayload(r, l, &j); err != nil {
		return result, err
	}
	if err = e.publish(r, l, &j); err != nil {
		return result, err
	}
	result.Committed = true
	if err = e.point("committed"); err != nil {
		return result, err
	}
	if err = r.Metadata.Commit(j.Metadata); err != nil {
		return result, err
	}
	result.Archive, err = archive(l, j)
	return result, err
}

// begin checkpoints metadata before any process is stopped or live file moved.
func begin(r Request, l layout) (journal, error) {
	if err := verify(r.Source, r.Expected); err != nil {
		return journal{}, err
	}
	metadata, err := r.Metadata.Capture()
	if err != nil {
		return journal{}, err
	}
	if len(metadata) > 1<<20 {
		return journal{}, errors.New("install_metadata_too_large")
	}
	hadOld, err := directoryExists(l.live)
	if err != nil {
		return journal{}, err
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return journal{}, err
	}
	j := journal{Format: 1, Live: l.live, SID: l.owner, ID: hex.EncodeToString(id), Phase: "staging", HadOld: hadOld, New: r.Expected.normalized(), Metadata: metadata}
	if err = os.Mkdir(l.pending, 0700); err != nil {
		return j, err
	}
	// An unjournaled workspace is retained for manual repair if initial storage fails.
	return j, save(l, j)
}
func (e *Engine) preparePayload(r Request, l layout, j *journal) error {
	if err := stage(r.Source, l.incoming, j.New); err != nil {
		return err
	}
	if err := e.point("staged"); err != nil {
		return err
	}
	if err := r.Stop(l.live); err != nil {
		return err
	}
	if j.HadOld {
		old, err := inventory(l.live, true)
		if err != nil {
			return err
		}
		j.Old = old
	}
	j.Phase = "prepared"
	if err := save(l, *j); err != nil {
		return err
	}
	return e.point("prepared")
}
func (e *Engine) publish(r Request, l layout, j *journal) error {
	j.Phase = "publishing"
	if err := save(l, *j); err != nil {
		return err
	}
	if j.HadOld {
		if err := move(l.live, l.previous); err != nil {
			return err
		}
	}
	if err := e.point("old_moved"); err != nil {
		return err
	}
	if err := move(l.incoming, l.live); err != nil {
		return err
	}
	if err := e.point("new_moved"); err != nil {
		return err
	}
	if err := verify(l.live, j.New); err != nil {
		return err
	}
	if err := r.Metadata.Apply(); err != nil {
		return err
	}
	if err := e.point("metadata_applied"); err != nil {
		return err
	}
	if err := r.Metadata.Verify(); err != nil {
		return err
	}
	j.Phase = "committed"
	return save(l, *j)
}

// Recover rolls back an interrupted publication or retains a durably committed
// new payload. It never guesses ownership from a directory name alone.
func (e *Engine) Recover(live string, metadata Metadata, stop func(string) error) (Result, error) {
	if metadata == nil || stop == nil {
		return Result{}, errors.New("install_metadata_required")
	}
	l, err := paths(live)
	if err != nil {
		return Result{}, err
	}
	release, err := openLayout(l)
	if err != nil {
		return Result{}, err
	}
	defer release()
	return recoverLocked(l, metadata, stop)
}
func recoverLocked(l layout, metadata Metadata, stop func(string) error) (result Result, err error) {
	exists, err := directoryExists(l.pending)
	if err != nil || !exists {
		return result, err
	}
	var j journal
	if err = readJSON(filepath.Join(l.pending, "journal.json"), &j); err != nil {
		return result, err
	}
	if err = j.validate(l); err != nil {
		return result, err
	}
	if j.Phase == "committed" {
		return recoverCommitted(l, j, metadata)
	}
	previous, err := directoryExists(l.previous)
	if err != nil {
		return result, err
	}
	failed, err := directoryExists(l.failed)
	if err != nil {
		return result, err
	}
	if j.Phase == "staging" || j.Phase == "prepared" {
		if previous || failed {
			return result, errors.New("install_prepublication_slots_changed")
		}
		if j.Phase == "prepared" && j.HadOld {
			if err = verifyBaseline(l.live, j.Old); err != nil {
				return result, err
			}
		}
	} else {
		if _, err = recoveryShape(l, j, previous, failed); err != nil {
			return result, err
		}
		if err = stop(l.live); err != nil {
			return result, fmt.Errorf("install_recovery_processes_not_stopped: %w", err)
		}
		if err = restorePayload(l, j, previous, failed); err != nil {
			return result, err
		}
		if err = metadata.Restore(j.Metadata); err != nil {
			return result, fmt.Errorf("install_metadata_recovery_failed: %w", err)
		}
	}
	result.Recovered = true
	result.Archive, err = archive(l, j)
	return result, err
}
func recoveryShape(l layout, j journal, previous, failed bool) (bool, error) {
	live, err := directoryExists(l.live)
	if err != nil {
		return false, err
	}
	if !j.HadOld && previous {
		return false, errors.New("install_unexpected_previous_payload")
	}
	if j.HadOld && !previous {
		// Also covers retry after files restored but metadata restoration failed.
		if !live {
			return false, errors.New("install_previous_payload_missing")
		}
		return live, verifyBaseline(l.live, j.Old)
	}
	if previous {
		if err = verifyBaseline(l.previous, j.Old); err != nil {
			return false, err
		}
	}
	if live {
		if failed {
			return false, errors.New("install_failed_slot_occupied")
		}
		if err = verify(l.live, j.New); err != nil {
			return false, err
		}
	}
	return live, nil
}
func restorePayload(l layout, j journal, previous, failed bool) error {
	live, err := recoveryShape(l, j, previous, failed)
	if err != nil {
		return err
	}
	if j.HadOld && !previous {
		return nil
	}
	if live {
		if err = move(l.live, l.failed); err != nil {
			return err
		}
	}
	if previous {
		return move(l.previous, l.live)
	}
	return nil
}

func recoverCommitted(l layout, j journal, metadata Metadata) (result Result, err error) {
	result.Committed = true
	if err = verify(l.live, j.New); err != nil {
		return result, err
	}
	if err = metadata.Commit(j.Metadata); err != nil {
		return result, err
	}
	result.Archive, err = archive(l, j)
	return result, err
}
