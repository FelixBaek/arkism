package main

import (
	"github.com/FelixBaek/arkism/internal/memory"
	"strings"
	"testing"
)

func TestLocalCaptureRuntimeID(t *testing.T) {
	good := strings.Repeat("af", 32)
	for _, c := range []struct {
		mode, id string
		valid    bool
	}{{"", "", true}, {"true", good, true}, {"", good, false}, {"true", "xx", false}, {"true", "aa", false}, {"true", strings.ToUpper(good), false}} {
		svc := new(memory.Service)
		err := setCaptureRuntimeID(svc, c.mode, c.id)
		if (err == nil) != c.valid || (c.valid && svc.CaptureRuntimeID != c.id) || (!c.valid && svc.CaptureRuntimeID != "") {
			t.Fatal("unexpected capture identity validation")
		}
	}
}
