package host

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
)

// The injected comparator covers ASCII test keys only. Real Windows ordinal
// comparison is exercised separately in the Windows-native tests.
func atomicWritePublicationASCIIEqual(left, right []uint16) (bool, error) {
	if len(left) != len(right) {
		return false, nil
	}
	for index := range left {
		if left[index] > 127 || right[index] > 127 {
			return false, errors.New("test comparator accepts ASCII only")
		}
		if atomicWritePublicationASCIILower(left[index]) != atomicWritePublicationASCIILower(right[index]) {
			return false, nil
		}
	}
	return true, nil
}

func atomicWritePublicationASCIILower(value uint16) uint16 {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func atomicWritePublicationKey(value string) []uint16 { return utf16.Encode([]rune(value)) }

func atomicWritePublicationTable() *atomicWritePublications {
	return &atomicWritePublications{equal: atomicWritePublicationASCIIEqual}
}

func assertAtomicWritePublicationEmpty(t *testing.T, table *atomicWritePublications) {
	t.Helper()
	table.mu.Lock()
	defer table.mu.Unlock()
	if len(table.entries) != 0 {
		t.Fatal("publication references leaked")
	}
	for _, entry := range table.entries[:cap(table.entries)] {
		if entry != nil {
			t.Fatal("released destination retained its key")
		}
	}
}

func atomicWritePublicationResult(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("publication failed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publication did not finish")
	}
}

func TestAtomicWritePublicationSameKeyWaitsForOwner(t *testing.T) {
	table := atomicWritePublicationTable()
	owner, err := table.acquire(atomicWritePublicationKey("state"), time.Now(), time.Now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	waiting, published := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- table.run(atomicWritePublicationKey("STATE"), time.Now(), time.Now,
			func(permit <-chan struct{}, remaining time.Duration) bool {
				close(waiting)
				return waitAtomicWritePermit(permit, remaining)
			}, func() error { close(published); return nil })
	}()
	<-waiting
	select {
	case <-published:
		t.Fatal("same destination published before its owner released")
	default:
	}
	owner.release()
	atomicWritePublicationResult(t, result)
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationDifferentDestinationProceeds(t *testing.T) {
	table := atomicWritePublicationTable()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	blocked := make(chan error, 1)
	go func() {
		blocked <- table.run(atomicWritePublicationKey("held"), time.Now(), time.Now, waitAtomicWritePermit,
			func() error { close(entered); <-release; return nil })
	}()
	<-entered
	other := make(chan error, 1)
	go func() {
		other <- table.run(atomicWritePublicationKey("independent"), time.Now(), time.Now, waitAtomicWritePermit, func() error { return nil })
	}()
	atomicWritePublicationResult(t, other)
	finish()
	atomicWritePublicationResult(t, blocked)
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationWaitBudgetHasNoFakeErrno(t *testing.T) {
	table := atomicWritePublicationTable()
	start := time.Unix(1, 0)
	clock := start
	now := func() time.Time { return clock }
	key := atomicWritePublicationKey("state")
	owner, err := table.acquire(key, start, now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	published := false
	err = table.run(key, start, now, func(_ <-chan struct{}, remaining time.Duration) bool {
		if remaining != atomicWriteAdmissionBudget {
			t.Fatal("admission changed its independent wait budget")
		}
		clock = clock.Add(remaining)
		return false
	}, func() error { published = true; return nil })
	var diagnostic *atomicWriteAdmissionError
	var errno syscall.Errno
	if !errors.As(err, &diagnostic) || !errors.Is(err, errAtomicWriteAdmissionBudget) || errors.As(err, &errno) || published {
		t.Fatal("admission timeout lost its cause or invented an OS error")
	}
	if diagnostic.reason != "wait_budget" || diagnostic.elapsed != atomicWriteAdmissionBudget || err.Error() != "atomic_write_admission reason=wait_budget attempts=0 elapsed_ms=2000" {
		t.Fatal("admission diagnostics changed")
	}
	table.mu.Lock()
	refs := owner.entry.refs
	table.mu.Unlock()
	if refs != 1 {
		t.Fatal("timed-out waiter retained a reference")
	}
	owner.release()
	owner.release()
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationLatePermitIsReturned(t *testing.T) {
	for _, mode := range []string{"late_permit", "already_expired"} {
		t.Run(mode, func(t *testing.T) {
			table := atomicWritePublicationTable()
			start := time.Unix(1, 0)
			clock := start
			if mode == "already_expired" {
				clock = clock.Add(atomicWriteAdmissionBudget)
			}
			waits, publications := 0, 0
			err := table.run(atomicWritePublicationKey("state"), start, func() time.Time { return clock },
				func(permit <-chan struct{}, _ time.Duration) bool {
					waits++
					<-permit
					clock = start.Add(atomicWriteAdmissionBudget)
					return true
				}, func() error { publications++; return nil })
			if !errors.Is(err, errAtomicWriteAdmissionBudget) || publications != 0 {
				t.Fatal("expired admission published")
			}
			if mode == "already_expired" && waits != 0 {
				t.Fatal("expired admission waited again")
			}
			assertAtomicWritePublicationEmpty(t, table)
		})
	}
}

func atomicWritePublicationPanics(table *atomicWritePublications) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	_ = table.run(atomicWritePublicationKey("state"), time.Now(), time.Now, waitAtomicWritePermit,
		func() error { panic("fixed publication panic") })
	return false
}

func TestAtomicWritePublicationFailureAndPanicRelease(t *testing.T) {
	table := atomicWritePublicationTable()
	original := errors.New("fixed publication failure")
	err := table.run(atomicWritePublicationKey("state"), time.Now(), time.Now, waitAtomicWritePermit, func() error { return original })
	if err != original {
		t.Fatal("coordination changed the publication error")
	}
	assertAtomicWritePublicationEmpty(t, table)
	if !atomicWritePublicationPanics(table) {
		t.Fatal("publication panic was swallowed")
	}
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationComparisonFailureRetainsOwner(t *testing.T) {
	table := atomicWritePublicationTable()
	owner, err := table.acquire(atomicWritePublicationKey("state"), time.Now(), time.Now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	original := errors.New("fixed comparison failure")
	table.equal = func([]uint16, []uint16) (bool, error) { return false, original }
	err = table.run(atomicWritePublicationKey("state"), time.Now(), time.Now, waitAtomicWritePermit,
		func() error { t.Fatal("comparison failure published"); return nil })
	var diagnostic *atomicWriteAdmissionError
	if !errors.Is(err, original) || !errors.As(err, &diagnostic) || diagnostic.reason != "comparison_failed" || owner.entry.refs != 1 {
		t.Fatal("comparison failure altered owner or lost its cause")
	}
	owner.release()
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationKeysAndEntryIdentityAreReleased(t *testing.T) {
	table := atomicWritePublicationTable()
	key := atomicWritePublicationKey("state")
	owner, err := table.acquire(key, time.Now(), time.Now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	key[0] = 'x'
	if string(utf16.Decode(owner.entry.key)) != "state" {
		t.Fatal("registry retained a mutable caller key")
	}
	owner.release()
	newOwner, err := table.acquire(atomicWritePublicationKey("state"), time.Now(), time.Now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	defer newOwner.release()
	owner.release()
	if newOwner.entry == owner.entry || len(table.entries) != 1 || newOwner.entry.refs != 1 {
		t.Fatal("old lease released a new entry")
	}
	newOwner.release()
	for index := range 128 {
		err = table.run(atomicWritePublicationKey(fmt.Sprintf("state-%d", index)), time.Now(), time.Now, waitAtomicWritePermit, func() error { return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationAdmissionAndRetryBudgetsAreSeparate(t *testing.T) {
	table := atomicWritePublicationTable()
	start := time.Unix(1, 0)
	clock := start
	now := func() time.Time { return clock }
	key := atomicWritePublicationKey("state")
	owner, err := table.acquire(key, start, now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	sourcePath, destinationPath := atomicWriteRetryFixture(t)
	err = table.run(key, start, now, func(permit <-chan struct{}, _ time.Duration) bool {
		clock = clock.Add(499 * time.Millisecond)
		owner.release()
		<-permit
		return true
	}, func() error {
		return replaceAtomicWriteWindows(sourcePath, destinationPath, func(actualSource, actualDestination string) error {
			if actualSource != sourcePath || actualDestination != destinationPath {
				t.Fatal("coordination rewrote rename arguments")
			}
			return atomicWriteSharingViolation
		}, now, func(delay time.Duration) { clock = clock.Add(delay) })
	})
	var diagnostic *atomicWriteReplaceError
	if !errors.As(err, &diagnostic) || diagnostic.reason != "budget" || diagnostic.elapsed != atomicWriteRetryBudget || clock.Sub(start) != 999*time.Millisecond {
		t.Fatal("admission consumed or increased the OS retry budget")
	}
	assertAtomicWritePublicationEmpty(t, table)
}

func TestAtomicWritePublicationInvalidKeyDoesNotPublish(t *testing.T) {
	published := false
	err := coordinateAtomicWrite("invalid\x00key", func() error { published = true; return nil })
	var diagnostic *atomicWriteAdmissionError
	if !errors.Is(err, errAtomicWriteAdmissionKey) || !errors.As(err, &diagnostic) || diagnostic.reason != "key_invalid" || published {
		t.Fatal("invalid key reached publication")
	}
}
