package capture

import (
	"os"
	"path/filepath"

	"github.com/FelixBaek/arkism/internal/host"
)

// analysisRun is what one drain did with the spool, kept in analysis.json so `agents sync` can report it
// (issue #449): without it a run that read every session and found nothing looks the same as a broken one.
type analysisRun struct {
	jobs, done, candidates, waiting, dropped, parked, failed int
	reasons                                                  map[string]int
}

// tally is the current drain's run; nil outside drain.
var tally *analysisRun

func (r *analysisRun) note(field *int, code string) {
	*field++
	if code != "" {
		r.reasons[code]++
	}
}

// count adds one settled job to the current drain's run.
func count(kind, code string) {
	if tally == nil {
		return
	}
	switch kind {
	case "done":
		tally.done++
	case "waiting":
		tally.note(&tally.waiting, code)
	case "dropped":
		tally.note(&tally.dropped, code)
	case "parked":
		tally.note(&tally.parked, code)
	default:
		tally.note(&tally.failed, code)
	}
}

func analysisPath() string { return filepath.Join(capState, "analysis.json") }

// saveAnalysisRun writes the run when it touched a job or failed; an idle run keeps the last informative one.
// last_done_at survives every write: it is what tells the app processing moved on.
func saveAnalysisRun(r *analysisRun, runErr error) error {
	if r.jobs == 0 && runErr == nil {
		return nil
	}
	lastDone, lastCandidates := any(nil), any(nil)
	if raw, err := os.ReadFile(analysisPath()); err == nil {
		if v, err := loadsOrdered(raw); err == nil {
			if o, ok := v.(host.Obj); ok {
				lastDone, _ = o.Get("last_done_at")
				lastCandidates, _ = o.Get("last_done_candidates")
			}
		}
	}
	now := host.NowISO()
	if r.done > 0 {
		lastDone, lastCandidates = now, r.candidates
	}
	reason, top := "", 0
	for code, n := range r.reasons {
		if n > top || n == top && code < reason {
			reason, top = code, n
		}
	}
	errText := ""
	if runErr != nil {
		errText = head(errorCode(runErr), 80)
	}
	out := host.Obj{{Key: "at", Value: now}, {Key: "jobs", Value: r.jobs}, {Key: "done", Value: r.done}, {Key: "candidates", Value: r.candidates},
		{Key: "waiting", Value: r.waiting}, {Key: "dropped", Value: r.dropped}, {Key: "parked", Value: r.parked}, {Key: "failed", Value: r.failed},
		{Key: "reason", Value: head(reason, 80)}, {Key: "error", Value: errText}, {Key: "last_done_at", Value: lastDone}, {Key: "last_done_candidates", Value: lastCandidates}}
	return atomicWrite(analysisPath(), dumps(out))
}
