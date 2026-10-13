package capture

import (
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestMessageDayUsesTheLastMessageDate(t *testing.T) {
	cued := []any{map[string]any{"at": "2026-09-01T10:00:00Z"}, map[string]any{"at": "2026-09-02T23:30:00.123Z"}}
	if got := messageDay(cued); got != "2026-09-02" {
		t.Fatal(got)
	}
	if got := messageDay([]any{map[string]any{"at": "not a time"}}); got != host.NowISO()[:10] {
		t.Fatal(got)
	}
}
