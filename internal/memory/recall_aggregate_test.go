package memory

import (
	"strings"
	"testing"
)

func TestAggregateIntent(t *testing.T) {
	for q, want := range map[string]bool{
		"How many times did they hike together?":     true,
		"Which cities has she been to?":              true,
		"What books has he read?":                    true,
		"Where has she camped?":                      true,
		"What kind of art does she make?":            true,
		"What are the names of his dogs?":            true,
		"지난달에 몇 번 운동했어?":                             true,
		"list the incidents we had":                  true,
		"여행을 어디어디 다녀왔어?":                             true,
		"고객사 목록":                                     true,
		"읽은 책들은 뭐야?":                                 true,
		"The whole list is long, when did it start?": false,
		"고객사 계약 만료일이 언제야?":                           false,
		"What is her favorite color?":                false,
		"When did she move to Boston?":               false,
		"Who is their favorite basketball player?":   false,
	} {
		if got := aggregateIntent(q); got != want {
			t.Errorf("aggregateIntent(%q) = %v, want %v", q, got, want)
		}
	}
	if conversationLaneLimit("Which cities has she been to?") != 2*conversationLaneHits || conversationLaneLimit("When did she move?") != conversationLaneHits {
		t.Fatal("aggregate questions get twice the conversation lane, others keep it")
	}
	list := SearchInput{Query: "What books has he read?", Limit: 10, AggregateItems: true}
	if aggregateItemLimit(list) != 20 || aggregateItemLimit(SearchInput{Query: list.Query, Limit: 40, AggregateItems: true}) != 50 ||
		aggregateItemLimit(SearchInput{Query: "When did she move?", Limit: 10, AggregateItems: true}) != 10 {
		t.Fatal("with the opt-in, aggregate questions get twice the item limit up to 50, others keep it")
	}
	if aggregateItemLimit(SearchInput{Query: list.Query, Limit: 10}) != 10 {
		t.Fatal("without aggregate_items the limit stays strict")
	}
}

// A list question keeps the vector lane's tail: only the absolute ceiling bounds it. Skills keep
// their own nearest-few rule either way.
func TestListingVectorCutIsAbsoluteOnly(t *testing.T) {
	if got := pgVectorCut("w", 7, 0.5, false, true); got != "w AND embedding <=> $7::vector <= 0.5" {
		t.Fatal(got)
	}
	for _, skills := range []bool{false, true} {
		if got := pgVectorCut("w", 7, 0.5, skills, skills); !strings.Contains(got, "nearest") {
			t.Fatal(skills, got)
		}
	}
}
