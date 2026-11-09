//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func seoulDay(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, usageZone()) }

func TestResolveOccurredReadsDatesAgainstTheSourceTime(t *testing.T) {
	ref := time.Date(2026, 11, 2, 15, 0, 0, 0, usageZone()) // a Monday
	for _, c := range []struct {
		from, to, text string
		want           [2]time.Time
		phrase         string
	}{
		{"", "", "어제 배포했다", [2]time.Time{seoulDay(2026, 11, 1), seoulDay(2026, 11, 2)}, "어제"},
		{"", "", "We deployed it yesterday.", [2]time.Time{seoulDay(2026, 11, 1), seoulDay(2026, 11, 2)}, "yesterday"},
		{"", "", "지난주에 장애가 났다", [2]time.Time{seoulDay(2026, 10, 26), seoulDay(2026, 11, 2)}, "지난주"},
		{"", "", "3 days ago the cache broke", [2]time.Time{seoulDay(2026, 10, 30), seoulDay(2026, 10, 31)}, "3 days ago"},
		// dates the extractor gave win over the text, to is inclusive
		{"2026-10-25", "2026-10-27", "어제 정리했다", [2]time.Time{seoulDay(2026, 10, 25), seoulDay(2026, 10, 28)}, ""},
		{"2026-09", "", "", [2]time.Time{seoulDay(2026, 9, 1), seoulDay(2026, 10, 1)}, ""},
		{"", "2026-10-31T10:00:00+09:00", "", [2]time.Time{seoulDay(2026, 10, 31), seoulDay(2026, 11, 1)}, ""},
		// a reversed or far-future pair is not trusted: the text decides
		{"2026-10-27", "2026-10-25", "어제 배포했다", [2]time.Time{seoulDay(2026, 11, 1), seoulDay(2026, 11, 2)}, "어제"},
		{"2999-01-01", "2999-01-02", "오늘 배포했다", [2]time.Time{seoulDay(2026, 11, 2), seoulDay(2026, 11, 3)}, "오늘"},
	} {
		got := resolveOccurred(c.from, c.to, c.text, ref)
		if got == nil || !got.From.Equal(c.want[0]) || !got.To.Equal(c.want[1]) || got.Phrase != c.phrase {
			t.Fatalf("%q %q %q: %+v", c.from, c.to, c.text, got)
		}
	}
	for _, c := range [][3]string{{"", "", "항상 한국어로 답한다"}, {"2999-01-01", "", ""}, {"someday", "", "no date here"}} {
		if got := resolveOccurred(c[0], c[1], c[2], ref); got != nil {
			t.Fatalf("%q: %+v", c, got)
		}
	}
	if validOccurred(&RecallTimeWindow{From: seoulDay(2026, 11, 2), To: seoulDay(2026, 11, 2)}, ref) || validOccurred(&RecallTimeWindow{}, ref) {
		t.Fatal("an empty span is not a valid event time")
	}
}

func TestCaptureStoresTheEventTimeAndTheReviewerDecidesIt(t *testing.T) {
	f := newFixture(t)
	in := captureInput(scopeA, "evt-occurred", explicit("어제 Demo 결제 서버를 배포했다."), explicit("Demo 결제 화면은 2026-10-25에 바꿨다."))
	at := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC) // 10:00 in Seoul
	in.Event.OccurredAt = &at
	in.Claims[1].OccurredFrom, in.Claims[1].OccurredTo = "2026-10-25", "2026-10-26"
	items := f.capture(f.human, in)
	var cands []Record
	for _, item := range items {
		cands = append(cands, f.get(f.human, "memory_candidates", scopeA, item.CandidateID))
	}
	if o := cands[0].Occurred; o == nil || !o.From.Equal(seoulDay(2026, 11, 1)) || !o.To.Equal(seoulDay(2026, 11, 2)) || o.Phrase != "어제" {
		t.Fatalf("relative date: %+v", o)
	}
	if o := cands[1].Occurred; o == nil || !o.From.Equal(seoulDay(2026, 10, 25)) || !o.To.Equal(seoulDay(2026, 10, 27)) {
		t.Fatalf("given dates: %+v", o)
	}
	review := func(c Record, o *RecallTimeWindow, action string) (Record, error) {
		return f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: action, Occurred: o, Reason: map[bool]string{true: "no"}[action == "reject"]})
	}
	for _, bad := range []struct {
		o      *RecallTimeWindow
		action string
	}{{&RecallTimeWindow{From: seoulDay(2026, 10, 3), To: seoulDay(2026, 10, 2)}, "approve"}, {&RecallTimeWindow{From: seoulDay(2999, 1, 1), To: seoulDay(2999, 1, 2)}, "approve"}, {&RecallTimeWindow{}, "reject"}} {
		if _, err := review(cands[0], bad.o, bad.action); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v %s: %v", bad.o, bad.action, err)
		}
	}
	// the reviewer moves the first date and clears the second; approval stays the person's act
	fixed := &RecallTimeWindow{From: seoulDay(2026, 10, 31), To: seoulDay(2026, 11, 1)}
	m, err := review(cands[0], fixed, "approve")
	if err != nil || m.Occurred == nil || !m.Occurred.From.Equal(fixed.From) || m.Occurred.Phrase != "" {
		t.Fatal(m.Occurred, err)
	}
	if m, err = review(cands[1], &RecallTimeWindow{}, "approve"); err != nil || m.Occurred != nil {
		t.Fatal(m.Occurred, err)
	}
}

func TestRecallTimeWindowFindsMemoriesByEventTime(t *testing.T) {
	f, now := recallFixture(t)
	old := now.AddDate(0, -3, 0).UTC()
	put := func(id, text string, from, to time.Time) {
		r := Record{ID: id, Scope: scopeA, Kind: "episodic", Text: text, Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(f.source()), CreatedAt: old, UpdatedAt: old,
			Occurred: &RecallTimeWindow{From: from, To: to}}
		if err := f.repo.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	// recorded months ago, but it happened last week (Sunday to Tuesday, across the week boundary)
	put("outage", "결제 장애 정리", seoulDay(2026, 10, 25), seoulDay(2026, 10, 28))
	put("trip", "출장 기록", seoulDay(2026, 9, 1), seoulDay(2026, 9, 3))
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "지난주에 무슨 일", Explain: true})
	if err != nil || !slices.Contains(ids(out.Items), "outage") || slices.Contains(ids(out.Items), "trip") {
		t.Fatal(ids(out.Items), err)
	}
	// a window outside the span finds nothing; one inside it finds the span
	hits, err := f.repo.SearchMemoriesInWindow(testCtx, SearchInput{Scope: scopeA, Limit: 10}, seoulDay(2026, 9, 1), seoulDay(2026, 9, 2))
	if err != nil || !slices.Equal(ids(hits), []string{"trip"}) {
		t.Fatal(ids(hits), err)
	}
}

func TestRecallWindowSQLReadsTheStoredEventSpan(t *testing.T) {
	_, doc, err := encodeRecord(Record{ID: "m", Text: "x", Occurred: &RecallTimeWindow{From: seoulDay(2026, 11, 1), To: seoulDay(2026, 11, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Occurred map[string]any `json:"occurred"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"from", "to"} {
		if v, _ := d.Occurred[k].(map[string]any); v["$date"] == nil || !strings.Contains(recallWindowSQL, "doc->'occurred'->'"+k+"'->>'$date'") {
			t.Fatalf("%s: stored %v", k, d.Occurred)
		}
	}
}

func TestFileClaimsTakeOnlyTheExtractorsDates(t *testing.T) {
	at := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	in := CaptureInput{Event: CaptureEvent{OccurredAt: &at}}
	session, _, _ := captureClaimKeys(in, CaptureClaim{Text: "어제 배포했다"}, scopeA)
	file, _, _ := captureClaimKeys(in, CaptureClaim{Text: "어제 배포했다", locator: "chunk:1"}, scopeA)
	dated, _, _ := captureClaimKeys(in, CaptureClaim{Text: "어제 배포했다", locator: "chunk:1", OccurredFrom: "2024-03-05"}, scopeA)
	// a file is read later than it was written, so its relative words say nothing about the read day
	if session.occurred == nil || file.occurred != nil || dated.occurred == nil || !dated.occurred.From.Equal(seoulDay(2024, 3, 5)) {
		t.Fatal(session.occurred, file.occurred, dated.occurred)
	}
}

func TestObservationSpansTheEventDaysOfItsCitedNotes(t *testing.T) {
	m := func(id string, o *RecallTimeWindow) Record { return Record{ID: id, Occurred: o} }
	members := []Record{
		m("a", &RecallTimeWindow{Phrase: "어제", From: seoulDay(2026, 10, 25), To: seoulDay(2026, 10, 26)}),
		m("b", &RecallTimeWindow{From: seoulDay(2026, 10, 20), To: seoulDay(2026, 10, 22)}),
		m("c", nil),
		m("x", &RecallTimeWindow{From: seoulDay(2026, 1, 1), To: seoulDay(2026, 1, 2)}), // not cited
	}
	cite := func(ids ...string) []Ref {
		out := []Ref{}
		for _, id := range ids {
			out = append(out, Ref{Collection: colMemories, ID: id, Revision: 1})
		}
		return out
	}
	got := citedOccurred(members, cite("a", "b", "c"))
	if got == nil || !got.From.Equal(seoulDay(2026, 10, 20)) || !got.To.Equal(seoulDay(2026, 10, 26)) || got.Phrase != "" {
		t.Fatal(got)
	}
	if citedOccurred(members, cite("c")) != nil {
		t.Fatal("undated notes give an undated observation")
	}
}
