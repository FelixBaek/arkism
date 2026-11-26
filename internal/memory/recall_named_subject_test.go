package memory

import "testing"

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
