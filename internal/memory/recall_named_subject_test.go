package memory

import (
	"slices"
	"testing"
)

func TestMixedSubjectRequiresTopicEvidence(t *testing.T) {
	hit := Hit{Text: "The River Finch roadmap label is Driftwood."}
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"River Finch 로드맵의 라벨을 알려줘", true},
		{"River Finch 회의실 온도를 알려줘", false},
		{"River   Finch 회의실 온도를 알려줘", false},
		{"River Finch 말고 Meadow 일정을 알려줘", false},
		{"Which label did River Finch use?", false},
	} {
		name := mixedQueryName(tc.query)
		got := name != "" && mixedNameMatch(name, hit.Text) && mixedTopicMatch(name, tc.query, hit)
		if got != tc.want {
			t.Errorf("%q: matched %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestPostgresMixedSubjectRecall(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	roadmap := f.activeMemory("The River Finch roadmap label is Driftwood.")
	meeting := f.activeMemory("River Finch meets on Tuesday.")
	f.activeMemory("The Amber Lake roadmap label is Clover.")
	for _, tc := range []struct {
		query  string
		expect bool
	}{
		{"River Finch 로드맵의 라벨을 알려줘", true},
		{"River   Finch 회의실 온도를 알려줘", false},
		{"River Finch 말고 Amber Lake 로드맵을 알려줘", false},
	} {
		out := f.search(f.human, SearchInput{Scope: scopeA, Query: tc.query, Purpose: "answer", Limit: 5})
		named := []string{}
		for _, hit := range out.Items {
			if slices.Contains(hit.Reasons, reasonNamedSubject) {
				named = append(named, hit.ID)
			}
		}
		if tc.expect {
			if len(out.Items) == 0 || out.Items[0].ID != roadmap.ID || !slices.Equal(named, []string{roadmap.ID}) {
				t.Errorf("%q: unexpected named results %v; expected roadmap first", tc.query, named)
			}
		} else if len(named) > 0 {
			t.Errorf("%q: unexpected named results %v", tc.query, named)
		}
		if slices.Contains(named, meeting.ID) {
			t.Fatal("a subject match without topic evidence was promoted")
		}
	}
}
