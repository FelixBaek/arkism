package subscription

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

func TestDesktopControlAccountProbeCannotEscapePausedAdmission(t *testing.T) {
	a := newAccounts(t)
	identity = func(string, string) map[string]any { return map[string]any{"status": "connected"} }
	var queued func()
	submit = func(work func()) { queued = work }
	fetchUsage = func(string, string, string, float64) map[string]any { return usage(42, "", unixNow(), unixNow()+3600) }
	t.Cleanup(func() {
		if queued != nil {
			queued()
		}
		_ = desktopcontrol.Default.Resume(11)
		_ = desktopcontrol.Default.Resume(12)
	})
	if idle, err := desktopcontrol.Default.Pause(11); err != nil || !idle {
		t.Fatal("test gate not idle")
	}
	if _, err := Account(a.profile, "inert", a.state, true); err != nil {
		t.Fatal(err)
	}
	if queued != nil {
		t.Fatal("paused inventory scheduled a usage probe")
	}
	if desktopcontrol.Default.Resume(11) != nil {
		t.Fatal("resume failed")
	}
	if _, err := Account(a.profile, "inert", a.state, true); err != nil {
		t.Fatal(err)
	}
	if queued == nil {
		t.Fatal("rejected probe left a stuck busy flag")
	}
	if idle, err := desktopcontrol.Default.Pause(12); err != nil || idle {
		t.Fatal("queued but not started probe was missed")
	}
	queued()
	queued = nil
	if idle, err := desktopcontrol.Default.Pause(12); err != nil || !idle {
		t.Fatal("completed probe retained admission")
	}
}
