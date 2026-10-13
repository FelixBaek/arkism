package memory

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// 잘못된 인코딩·예산과 JSON에 담을 수 없는 숫자는 결과를 발행하지 않는다.
func TestContextBudgetInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name, encoding string
		budget         int
		trust          float64
		want           error
	}{
		{"unknown encoding", "unknown", 64000, 1, ErrInvalid},
		{"empty encoding", "", 64000, 1, ErrInvalid},
		{"zero budget", "o200k_base", 0, 1, ErrInvalid},
		{"minimum integer budget", "o200k_base", math.MinInt, 1, ErrInvalid},
		{"NaN trust", "o200k_base", 64000, math.NaN(), ErrUnavailable},
		{"infinite trust", "o200k_base", 64000, math.Inf(1), ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ContextBundle{Items: []Hit{{ID: "keep", Text: "한국어 문서", Trust: tc.trust}}}
			if err := packContext(testCtx, &out, tc.budget, tc.encoding); !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			if out.ContextData != "" || out.TokensUsed != 0 || out.TokenEncoding != "" || len(out.Items) != 1 || out.Items[0].ID != "keep" {
				t.Fatalf("failed packing published or removed data: %+v", out)
			}
		})
	}
}

// 실행 길이 제한은 글자 수가 아닌 UTF-8 바이트이며 공백/비공백 전환 때 초기화된다.
func TestContextTokenRunUTF8Boundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"empty", "", true},
		{"spaces at limit", strings.Repeat(" ", 4096), true},
		{"spaces over limit", strings.Repeat(" ", 4097), false},
		{"emoji at limit", strings.Repeat("😀", 1024), true},
		{"emoji over limit", strings.Repeat("😀", 1025), false},
		{"Korean at limit", strings.Repeat("가", 1365) + "x", true},
		{"Korean over limit", strings.Repeat("가", 1366), false},
		{"Unicode whitespace at limit", strings.Repeat("\u2003", 1365), true},
		{"Unicode whitespace over limit", strings.Repeat("\u2003", 1366), false},
		{"space separates runs", strings.Repeat("a", 4096) + " " + strings.Repeat("b", 4096), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundedTokenRuns(tc.text); got != tc.want {
				t.Fatalf("boundedTokenRuns = %v, want %v", got, tc.want)
			}
		})
	}
}
