package crewrun

import (
	"context"
	"errors"
	"testing"
)

func TestGoHoldsAdmissionWhileDriving(t *testing.T) {
	// Refused admission (the desktop is updating) leaves the run for the next poll: nothing is driven.
	e := &Engine{Admit: func() (func(), error) { return nil, errors.New("paused") }}
	e.Go(context.Background(), "run-1") // a nil Store would panic if Drive ran
	e.Wait()

	released := 0
	e = &Engine{Store: newFakeStore(), Admit: func() (func(), error) { return func() { released++ }, nil }}
	e.Go(context.Background(), "missing")
	e.Wait()
	if released != 1 {
		t.Fatalf("admission released %d times, want 1", released)
	}
}
