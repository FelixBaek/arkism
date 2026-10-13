package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture mimics locomo10.json's shape; it is not taken from the dataset.
const fixture = `[{
  "sample_id": "conv-x",
  "conversation": {
    "speaker_a": "Ann", "speaker_b": "Bo",
    "session_2_date_time": "9:00 am on 3 June, 2023",
    "session_2": [{"speaker": "Bo", "dia_id": "D2:1", "text": "I adopted a cat."}],
    "session_1_date_time": "1:56 pm on 8 May, 2023",
    "session_1": [
      {"speaker": "Ann", "dia_id": "D1:1", "text": "I started pottery last week."},
      {"speaker": "Bo", "dia_id": "D1:2", "text": "Look!", "img_url": ["x"], "blip_caption": "a photo of a bowl"}
    ]
  },
  "qa": [
    {"question": "What did Ann start?", "answer": "pottery", "evidence": ["D1:1"], "category": 4},
    {"question": "When did Bo adopt a cat?", "answer": 2023, "evidence": ["D2:1"], "category": 2},
    {"question": "Trick?", "evidence": ["D1:1"], "category": 5, "adversarial_answer": "x"},
    {"question": "Both?", "answer": "pottery and a cat", "evidence": ["D1:1; D2:1"], "category": 1},
    {"question": "No evidence?", "answer": "y", "evidence": [], "category": 3}
  ]
}]`

func TestParseDataset(t *testing.T) {
	convs, err := ParseDataset([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 1 {
		t.Fatalf("got %d conversations", len(convs))
	}
	c := convs[0]
	if c.SampleID != "conv-x" || c.SpeakerA != "Ann" || c.SpeakerB != "Bo" {
		t.Fatalf("header: %+v", c)
	}
	if len(c.Sessions) != 2 || c.Sessions[0].N != 1 || c.Sessions[1].N != 2 {
		t.Fatalf("sessions not sorted: %+v", c.Sessions)
	}
	if got := c.Sessions[0].Date.Format("2006-01-02 15:04"); got != "2023-05-08 13:56" {
		t.Fatalf("date %s", got)
	}
	want := "Ann: I started pottery last week.\nBo: Look! [shares a photo: a photo of a bowl]"
	if got := SessionText(c.Sessions[0]); got != want {
		t.Fatalf("session text %q", got)
	}
	if c.QA[1].Answer != "2023" || c.QA[2].Answer != "" {
		t.Fatalf("answers: %q %q", c.QA[1].Answer, c.QA[2].Answer)
	}
	if ev := c.QA[3].EvidenceSessions(); len(ev) != 2 || ev[0] != 1 || ev[1] != 2 {
		t.Fatalf("evidence sessions %v", ev)
	}
}

func TestParseDatasetBadDate(t *testing.T) {
	bad := `[{"sample_id":"c","conversation":{"session_1_date_time":"yesterday","session_1":[]},"qa":[]}]`
	if _, err := ParseDataset([]byte(bad)); err == nil {
		t.Fatal("want date error")
	}
}

func TestSelectQuestions(t *testing.T) {
	convs, err := ParseDataset([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	qa := convs[0].QA
	idx := func(qs []QA) []int {
		var out []int
		for _, q := range qs {
			out = append(out, q.Index)
		}
		return out
	}
	// Category 5 is dropped; every session allowed.
	if got := idx(SelectQuestions(qa, 0, 0)); len(got) != 4 || got[0] != 0 || got[1] != 1 || got[2] != 3 || got[3] != 4 {
		t.Fatalf("all sessions: %v", got)
	}
	// Session 1 only: evidence must lie wholly in it; no evidence means unanswerable here.
	if got := idx(SelectQuestions(qa, 1, 0)); len(got) != 1 || got[0] != 0 {
		t.Fatalf("session 1: %v", got)
	}
	if got := idx(SelectQuestions(qa, 2, 2)); len(got) != 2 || got[1] != 1 {
		t.Fatalf("limit: %v", got)
	}
}

func TestParseJudge(t *testing.T) {
	cases := []struct {
		structured map[string]any
		text       string
		want       int
		err        bool
	}{
		{map[string]any{"label": "CORRECT"}, "", 1, false},
		{map[string]any{"label": "wrong"}, "CORRECT", 0, false},
		{nil, `{"reasoning": "same date", "label": "CORRECT"}`, 1, false},
		{nil, "```json\n{\"label\":\"WRONG\"}\n```", 0, false},
		{nil, "The answer is not CORRECT. Final: WRONG", 0, false},
		{nil, "INCORRECTLY phrased", 0, true},
		{map[string]any{"label": 3}, "", 0, true},
	}
	for i, c := range cases {
		got, err := ParseJudge(c.structured, c.text)
		if (err != nil) != c.err || (err == nil && got != c.want) {
			t.Errorf("case %d: got %d, %v", i, got, err)
		}
	}
}

func TestCheckpointResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	ck, err := openCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ck.add(Row{Type: "ingest", Key: ingestKey("c", 1), Conv: "c", Session: 1, Claims: 2}); err != nil {
		t.Fatal(err)
	}
	if err := ck.add(Row{Type: "qa", Key: qaKey("c", 7, "arkism"), Conv: "c", QIndex: 7, System: "arkism", Score: 1}); err != nil {
		t.Fatal(err)
	}
	// A kill mid-write leaves a torn line; it must not stop the resume.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"qa","key":"c|q8|ark`)
	f.Close()

	again, err := openCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.done(ingestKey("c", 1)); !ok {
		t.Fatal("ingest row lost")
	}
	if r, ok := again.done(qaKey("c", 7, "arkism")); !ok || r.Score != 1 {
		t.Fatalf("qa row lost: %+v", r)
	}
	if _, ok := again.done(qaKey("c", 8, "arkism")); ok {
		t.Fatal("torn row counted as done")
	}
	if _, ok := again.done(qaKey("c", 7, "no-memory")); ok {
		t.Fatal("other system counted as done")
	}
	if n := len(again.all()); n != 2 {
		t.Fatalf("rows %d", n)
	}
	// The report counts the resumed rows.
	rep := buildReport(config{}, again.all(), nil, reportTimes{}, 0)
	if rep.Sessions != 1 || rep.Claims != 2 || rep.Questions != 1 || rep.Summary["arkism"].Accuracy != 1 {
		t.Fatalf("report %+v", rep)
	}
}
