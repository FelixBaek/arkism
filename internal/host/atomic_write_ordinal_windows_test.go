package host

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicWriteOrdinalWindowsCaseComparison(t *testing.T) {
	if err := atomicWriteOrdinalReady(); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"State", "STATE"}, {"기억-É", "기억-é"}} {
		equal, err := atomicWriteOrdinalEqual(atomicWritePublicationKey(pair[0]), atomicWritePublicationKey(pair[1]))
		if err != nil || !equal {
			t.Fatal("Windows ordinal case comparison did not coalesce equivalent keys", err)
		}
	}
	equal, err := atomicWriteOrdinalEqual(atomicWritePublicationKey("state-a"), atomicWritePublicationKey("state-b"))
	if err != nil || equal {
		t.Fatal("Windows ordinal comparison merged different keys", err)
	}
	if _, err := atomicWriteOrdinalEqual(nil, atomicWritePublicationKey("state")); !errors.Is(err, errAtomicWriteAdmissionKey) {
		t.Fatal("empty key was accepted")
	}
}

func TestAtomicWriteOrdinalWindowsNormalizedDestination(t *testing.T) {
	if err := atomicWriteOrdinalReady(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	first, err := atomicWriteDestinationKey(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := atomicWriteDestinationKey(dir + `\child\..\STATE.json`)
	if err != nil {
		t.Fatal(err)
	}
	table := &atomicWritePublications{equal: atomicWriteOrdinalEqual}
	owner, err := table.acquire(first, time.Now(), time.Now, waitAtomicWritePermit)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	entry, err := table.reference(second)
	if err != nil {
		t.Fatal(err)
	}
	if entry != owner.entry || entry.refs != 2 {
		t.Fatal("normalized Windows spellings did not share admission")
	}
	table.unreference(entry)
	owner.release()
	assertAtomicWritePublicationEmpty(t, table)
}
