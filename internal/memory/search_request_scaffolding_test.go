package memory

import (
	"slices"
	"testing"
)

func TestKoreanAnswerRequestTopicCoverage(t *testing.T) {
	for _, q := range []string{"한글로 답해줘", "한글로 답해 주세요!", "한글로 답변해주세요", "한글로 응답해주십시오"} {
		if got := searchQueryTerms(q); !slices.Equal(got, []string{"한글"}) {
			t.Errorf("%s: %v", q, got)
		}
		h := Hit{Text: "한글 사용을 선호한다", Reasons: []string{reasonTextSearch}}
		if share, ok := keywordTermShare(SearchInput{Query: q}, searchQueryTerms(q), false, h); !ok || share != 1 {
			t.Errorf("topic-only match credit for %q: %v %v", q, share, ok)
		}
	}
	if got := searchQueryTerms("백업 복원 답변해줘"); !slices.Equal(got, searchQueryTerms("백업 복원")) {
		t.Fatalf("lost a topic: %v", got)
	}
	for _, q := range []string{"답해줘", "어떻게 답해줘", "그렇게 답해줘 라고 말했다", "한글로 '답해줘'", "한글로 답하지 마", "답해 주세요"} {
		if got := koreanAnswerTopic(q); got != q {
			t.Errorf("changed literal or non-request %q to %q", q, got)
		}
	}
}
