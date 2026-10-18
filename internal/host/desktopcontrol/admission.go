// Package desktopcontrol coordinates an explicitly opted-in desktop child's shutdown.
// A role-level idle/stop receipt must still be paired with native descendant ownership.
package desktopcontrol

import (
	"errors"
	"sync"
)

var (
	ErrPaused   = errors.New("desktop_admission_paused")
	ErrLease    = errors.New("desktop_control_lease_invalid")
	ErrStopping = errors.New("desktop_control_stopping")
)

// Scope names describe work kinds, never models, accounts or request contents.
type Scope uint8

const (
	Handler Scope = iota
	Round
	Pool
	Account
	scopeCount
)

// Default is process-local. Without an opted-in controller it stays open and does not
// change ordinary CLI behavior. All producers in an opted-in role share this gate.
var Default = &Gate{}

type Gate struct {
	mu       sync.Mutex
	active   [scopeCount]uint64
	lease    uint64
	stopping bool
}

// Enter retains activity before enqueue/WaitGroup.Add, including work not yet scheduled.
// The returned release is idempotent; the gate lock is never held across caller code.
func (g *Gate) Enter(scope Scope) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if scope >= scopeCount || g.lease != 0 || g.stopping || g.active[scope] == ^uint64(0) {
		return nil, ErrPaused
	}
	g.active[scope]++
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.active[scope]--; g.mu.Unlock() }) }, nil
}

func Enter(scope Scope) (func(), error) { return Default.Enter(scope) }

// Submit takes admission before a scheduler can publish pending work. The activity
// survives the submitting caller and is released only when the scheduled work returns.
func Submit(gate *Gate, scope Scope, schedule func(func()), work func()) bool {
	release, err := gate.Enter(scope)
	if err != nil {
		return false
	}
	schedule(func() { defer release(); work() })
	return true
}

// Pause closes admission and checks existing work under the same lock. Busy restores
// admission immediately; it does not cancel or wait for any ongoing work.
func (g *Gate) Pause(lease uint64) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if lease == 0 || g.lease != 0 || g.stopping {
		return false, ErrLease
	}
	g.lease = lease
	for _, count := range g.active {
		if count != 0 {
			g.lease = 0
			return false, nil
		}
	}
	return true, nil
}

func (g *Gate) Resume(lease uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return ErrStopping
	}
	if lease == 0 || lease != g.lease {
		return ErrLease
	}
	g.lease = 0
	return nil
}

func (g *Gate) Stop(lease uint64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return ErrStopping
	}
	if lease == 0 || lease != g.lease {
		return ErrLease
	}
	g.stopping = true
	return nil
}
