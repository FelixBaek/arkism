package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func claimEvidenceUser(text string) map[string]any {
	return map[string]any{"text": text, "archive_ref": map[string]any{"archive_id": "original"}}
}

func TestCaptureClaimEvidenceExactMessage(t *testing.T) {
	const original = "Remember that the selector must use Prod."
	for _, tc := range []struct {
		name, quote string
		users       []any
		explicit    bool
	}{
		{"exact", original, []any{claimEvidenceUser(original)}, true},
		{"substring", "selector must use Prod", []any{claimEvidenceUser(original)}, true},
		{"case_changed", strings.ToLower(original), []any{claimEvidenceUser(original)}, false},
		{"space_collapsed", "Remember that the selector must use Prod.", []any{claimEvidenceUser("Remember  that the selector must use Prod.")}, false},
		{"tab_changed", "selector must use Prod", []any{claimEvidenceUser("Remember that the selector\tmust use Prod.")}, false},
		{"unicode_space_changed", "selector must use Prod", []any{claimEvidenceUser("Remember that the selector\u00a0must use Prod.")}, false},
		{"cross_message", "Prod.\nRemember", []any{claimEvidenceUser(original), claimEvidenceUser("Remember the timeout is 20 seconds.")}, false},
		{"multiline_same_message", "Prod.\nRemember", []any{claimEvidenceUser(original + "\nRemember the timeout is 20 seconds.")}, true},
		{"quote_not_trimmed", " " + original, []any{claimEvidenceUser(original)}, false},
		{"blank_quote", "\t\n ", []any{claimEvidenceUser(original)}, false},
		{"empty_users_authoritative", original, []any{}, false},
		{"aggregate_is_not_evidence", original, []any{claimEvidenceUser("A different user message.")}, false},
		{"ordered_prepared_user", original, []any{host.Obj{{Key: "text", Value: original}, {Key: "archive_ref", Value: map[string]any{"archive_id": "original"}}}}, true},
		{"explicit_user_role", original, []any{map[string]any{"role": "user", "text": original, "archive_ref": map[string]any{"archive_id": "original"}}}, true},
		{"assistant_role", original, []any{map[string]any{"role": "assistant", "text": original, "archive_ref": map[string]any{"archive_id": "original"}}}, false},
		{"null_role", original, []any{map[string]any{"role": nil, "text": original, "archive_ref": map[string]any{"archive_id": "original"}}}, false},
		{"non_string_text", "7", []any{map[string]any{"text": 7, "archive_ref": map[string]any{"archive_id": "original"}}}, false},
		{"korean_exact", "대소문자를 유지해", []any{claimEvidenceUser("앞으로 대소문자를 유지해.")}, true},
		{"korean_space_changed", "대소문자를 유지해", []any{claimEvidenceUser("앞으로 대소문자를  유지해.")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := buildClaims([]any{map[string]any{"text": "A generated claim still needs human review.", "quote": tc.quote, "risk": "low"}}, original, nil, tc.users)
			if err != nil || len(claims) != 1 {
				t.Fatal(err, claims)
			}
			claim := claims[0].(map[string]any)
			wantBasis, wantRisk, wantArchives := "inferred", "medium", 0
			if tc.explicit {
				wantBasis, wantRisk, wantArchives = "explicit_user", "low", 1
			}
			archives, _ := claim["archive_messages"].([]any)
			if claim["basis"] != wantBasis || claim["risk"] != wantRisk || len(archives) != wantArchives {
				t.Fatalf("basis=%v risk=%v archive count=%d; want %s/%s/%d", claim["basis"], claim["risk"], len(archives), wantBasis, wantRisk, wantArchives)
			}
			if claim["text"] != "A generated claim still needs human review." {
				t.Fatal("quote validation rewrote the generated claim")
			}
		})
	}
}

func TestCaptureClaimEvidenceNilUsersLegacy(t *testing.T) {
	for _, tc := range []struct {
		name, text, quote, basis string
	}{
		{"exact", "Keep Prod exactly.", "Prod", "explicit_user"},
		{"case_changed", "Keep Prod exactly.", "prod", "inferred"},
		{"whitespace_changed", "Keep  Prod exactly.", "Keep Prod", "inferred"},
		{"literal_multiline", "Keep Prod.\nAlways.", "Prod.\nAlways", "explicit_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := buildClaims([]any{map[string]any{"text": "Keep the original spelling.", "quote": tc.quote}}, tc.text, nil, nil)
			if err != nil || len(claims) != 1 || claims[0].(map[string]any)["basis"] != tc.basis {
				t.Fatal(err, claims)
			}
			if _, ok := claims[0].(map[string]any)["archive_messages"]; ok {
				t.Fatal("legacy aggregate invented an archive identity")
			}
		})
	}
}

func TestCaptureClaimEvidenceDuplicateQuoteDoesNotGuessArchive(t *testing.T) {
	const quote = "Keep Prod exactly."
	users := []any{claimEvidenceUser(quote), claimEvidenceUser(quote)}
	users[1].(map[string]any)["archive_ref"] = map[string]any{"archive_id": "second"}
	claims, err := buildClaims([]any{map[string]any{"text": "Keep Prod.", "quote": quote, "risk": "low"}}, quote, nil, users)
	if err != nil || len(claims) != 1 {
		t.Fatal(err, claims)
	}
	claim := claims[0].(map[string]any)
	if claim["basis"] != "explicit_user" || claim["risk"] != "low" || claim["archive_messages"] != nil {
		t.Fatal("ambiguous source was guessed", claim)
	}
}

func TestCaptureClaimEvidencePreparedUnlinkedUserRemainsExplicit(t *testing.T) {
	const quote = "앞으로 이름의 대소문자를 유지해."
	// Missing archive metadata does not invalidate an exact user quotation.
	users, err := evidencePrepareUsers(nil, []any{map[string]any{"text": quote}}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := buildClaims([]any{map[string]any{"text": "이름의 대소문자를 유지한다.", "quote": quote, "risk": "low"}}, quote, nil, users)
	if err != nil || len(claims) != 1 {
		t.Fatal(err, claims)
	}
	claim := claims[0].(map[string]any)
	if claim["basis"] != "explicit_user" || claim["risk"] != "low" || claim["archive_messages"] != nil {
		t.Fatal("exact unlinked source changed", claim)
	}
}

func TestCaptureClaimEvidencePreparedTranscriptKeepsUserBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	rows := []map[string]any{
		{"type": "user", "message": map[string]any{"role": "user", "content": "Remember the profile is Prod."}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Remember the profile is Stage."}}}},
		{"type": "user", "message": map[string]any{"role": "user", "content": "Remember the timeout is 20 seconds."}},
	}
	var raw strings.Builder
	for _, row := range rows {
		raw.WriteString(host.MustDumps(row, host.DumpOpts{}) + "\n")
	}
	if err := os.WriteFile(path, []byte(raw.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	users, _, _, _, err := readNew(path, 0, nil)
	if err != nil || len(users) != 2 {
		t.Fatal(err, users)
	}
	linked, err := evidencePrepareUsers(nil, users, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	quotes := []string{"profile is Prod.", "profile is Stage.", "Prod.\nRemember"}
	inputs := make([]any, len(quotes))
	for i, quote := range quotes {
		inputs[i] = map[string]any{"text": "A lasting preference.", "quote": quote, "risk": "low"}
	}
	combined := field(users[0], "text").(string) + "\n" + field(users[1], "text").(string)
	claims, err := buildClaims(inputs, combined, nil, linked)
	if err != nil || len(claims) != 3 {
		t.Fatal(err, claims)
	}
	for i, want := range []string{"explicit_user", "inferred", "inferred"} {
		if claims[i].(map[string]any)["basis"] != want {
			t.Fatalf("claim %d basis=%v; want %s", i, claims[i].(map[string]any)["basis"], want)
		}
	}
}

func TestCaptureClaimEvidenceBenchmarkAggregateLegacy(t *testing.T) {
	capFake(t)
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		return []any{
			map[string]any{"text": "Keep Prod.", "quote": "Prod", "risk": "low"},
			map[string]any{"text": "Keep prod.", "quote": "prod", "risk": "low"},
		}, nil
	}
	result, err := ExtractClaims("Always keep Prod exactly.", "2026-01-01", nil, "", "synthetic-legacy")
	if err != nil || len(result.Claims) != 2 {
		t.Fatal(err, result.Claims)
	}
	for i, want := range []string{"explicit_user", "inferred"} {
		claim := result.Claims[i].(map[string]any)
		if claim["basis"] != want || claim["archive_messages"] != nil {
			t.Fatalf("legacy claim %d basis=%v archive=%v", i, claim["basis"], claim["archive_messages"])
		}
	}
}
