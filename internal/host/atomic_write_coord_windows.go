package host

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const atomicWriteAdmissionBudget = 2 * time.Second

var errAtomicWriteAdmissionBudget = errors.New("atomic_write_admission_budget")
var errAtomicWriteAdmissionKey = errors.New("atomic_write_admission_key_invalid")

// This table coordinates this process's normalized destination spellings only.
// It is not a file-identity/security fence: 8.3, SUBST, junctions and namespace
// aliases may still have different keys. No path key is logged.
var atomicWriteDestinations = atomicWritePublications{equal: atomicWriteOrdinalEqual}

type atomicWritePublication struct {
	key    []uint16
	permit chan struct{}
	refs   int
}

type atomicWritePublications struct {
	mu      sync.Mutex
	entries []*atomicWritePublication
	equal   func([]uint16, []uint16) (bool, error)
}

type atomicWritePublicationLease struct {
	table *atomicWritePublications
	entry *atomicWritePublication
	held  bool
	once  sync.Once
}

type atomicWriteAdmissionError struct {
	cause   error
	reason  string
	elapsed time.Duration
}

func (err *atomicWriteAdmissionError) Error() string {
	return fmt.Sprintf("atomic_write_admission reason=%s attempts=0 elapsed_ms=%d", err.reason, err.elapsed.Milliseconds())
}

func (err *atomicWriteAdmissionError) Unwrap() error { return err.cause }

func atomicWriteAdmissionFailure(cause error, reason string, start, end time.Time) error {
	return &atomicWriteAdmissionError{cause: cause, reason: reason, elapsed: max(end.Sub(start), 0)}
}

func atomicWriteDestinationKey(path string) ([]uint16, error) {
	if strings.ContainsRune(path, 0) {
		return nil, errAtomicWriteAdmissionKey
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	return utf16.Encode([]rune(absolute)), nil
}

func coordinateAtomicWrite(path string, publish func() error) error {
	start := time.Now()
	key, err := atomicWriteDestinationKey(path)
	if err != nil {
		return atomicWriteAdmissionFailure(err, "key_invalid", start, time.Now())
	}
	// Resolve the system API before the bookkeeping mutex can be taken. DLL
	// loading and all filesystem/path work stay outside that critical section.
	if err := atomicWriteOrdinalReady(); err != nil {
		return atomicWriteAdmissionFailure(err, "comparison_unavailable", start, time.Now())
	}
	return atomicWriteDestinations.run(key, start, time.Now, waitAtomicWritePermit, publish)
}

func (table *atomicWritePublications) reference(key []uint16) (*atomicWritePublication, error) {
	table.mu.Lock()
	defer table.mu.Unlock()
	for _, entry := range table.entries {
		equal, err := table.equal(key, entry.key)
		if err != nil {
			return nil, err
		}
		if equal {
			entry.refs++
			return entry, nil
		}
	}
	entry := &atomicWritePublication{key: append([]uint16(nil), key...), permit: make(chan struct{}, 1), refs: 1}
	entry.permit <- struct{}{}
	table.entries = append(table.entries, entry)
	return entry, nil
}

func (table *atomicWritePublications) unreference(entry *atomicWritePublication) {
	table.mu.Lock()
	defer table.mu.Unlock()
	entry.refs--
	if entry.refs != 0 {
		return
	}
	for index, current := range table.entries {
		if current == entry {
			copy(table.entries[index:], table.entries[index+1:])
			last := len(table.entries) - 1
			table.entries[last] = nil
			table.entries = table.entries[:last]
			return
		}
	}
}

func (lease *atomicWritePublicationLease) release() {
	lease.once.Do(func() {
		if lease.held {
			lease.entry.permit <- struct{}{}
		}
		lease.table.unreference(lease.entry)
	})
}

func waitAtomicWritePermit(permit <-chan struct{}, remaining time.Duration) bool {
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-permit:
		return true
	case <-timer.C:
		return false
	}
}

func (table *atomicWritePublications) acquire(key []uint16, start time.Time, now func() time.Time, wait func(<-chan struct{}, time.Duration) bool) (*atomicWritePublicationLease, error) {
	entry, err := table.reference(key)
	if err != nil {
		return nil, atomicWriteAdmissionFailure(err, "comparison_failed", start, now())
	}
	lease := &atomicWritePublicationLease{table: table, entry: entry}
	transferred := false
	defer func() {
		if !transferred {
			lease.release()
		}
	}()
	deadline := start.Add(atomicWriteAdmissionBudget)
	remaining := deadline.Sub(now())
	if remaining <= 0 || !wait(entry.permit, remaining) {
		return nil, atomicWriteAdmissionFailure(errAtomicWriteAdmissionBudget, "wait_budget", start, now())
	}
	lease.held = true
	if !now().Before(deadline) {
		return nil, atomicWriteAdmissionFailure(errAtomicWriteAdmissionBudget, "wait_budget", start, now())
	}
	transferred = true
	return lease, nil
}

func (table *atomicWritePublications) run(key []uint16, start time.Time, now func() time.Time, wait func(<-chan struct{}, time.Duration) bool, publish func() error) error {
	lease, err := table.acquire(key, start, now, wait)
	if err != nil {
		return err
	}
	defer lease.release()
	// No bookkeeping mutex is held while waiting, publishing or retrying.
	return publish()
}
