package capture

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateReviewPreservesSafeBatchAndNextRun(t *testing.T) {
	for _, encoding := range []string{"escaped", "unfinished"} {
		for _, scenario := range []string{"candidate_text", "old_text", "related_text", "candidate_id", "conflict_id", "related_id", "relation_id"} {
			t.Run(encoding+"/"+scenario, func(t *testing.T) {
				capFake(t)
				marked := `before \u003cprivate>synthetic-private\u003c/private> after`
				if encoding == "unfinished" {
					marked = "before <private synthetic-private"
				}
				bad := map[string]any{"id": "bad", "revision": 1, "kind": "decision", "text": "ordinary candidate", "tags": []any{"basis:explicit_user"}, "created_by": "extractor", "status": "candidate"}
				safe := map[string]any{"id": "safe", "revision": 1, "kind": "decision", "text": "Use <PrivateRoute> and answer in Korean", "tags": []any{"basis:explicit_user"}, "created_by": "extractor", "status": "candidate"}
				old := map[string]any{"id": "old", "kind": "decision", "text": "ordinary old memory"}
				related := map[string]any{"id": "related", "kind": "decision", "text": "ordinary related memory"}
				switch scenario {
				case "candidate_text":
					bad["text"] = marked
				case "old_text":
					bad["conflict_id"] = "relation"
					old["text"] = marked
				case "related_text":
					related["text"] = marked
				case "candidate_id":
					bad["id"] = marked
				case "conflict_id":
					bad["conflict_id"] = marked
				case "related_id":
					related["id"] = marked
				case "relation_id":
					bad["conflict_id"] = "relation"
				}
				originalText := bad["text"]
				var classified []string
				reads, searches, models := 0, 0, 0
				capGet = func(path string, _ time.Duration) (map[string]any, error) {
					reads++
					if privatePayload(path) {
						t.Fatal("private identity reached read URL")
					}
					if strings.HasPrefix(path, "/v1/memory_candidates?") {
						if strings.Contains(path, "status=conflicted") {
							return map[string]any{"items": []any{}}, nil
						}
						return map[string]any{"items": []any{bad, safe}}, nil
					}
					if strings.HasPrefix(path, "/v1/memory_relations/relation?") {
						id := any("old")
						if scenario == "relation_id" {
							id = marked
						}
						return map[string]any{"relations": []any{map[string]any{"collection": "memories", "id": id}}}, nil
					}
					if strings.HasPrefix(path, "/v1/memories/old?") {
						return old, nil
					}
					t.Fatalf("unexpected read: %s", path)
					return nil, nil
				}
				reviewSearch = func(query string) ([]any, error) {
					searches++
					if privatePayload(query) || strings.Contains(query, "synthetic-private") {
						t.Fatal("private text reached search")
					}
					return []any{related}, nil
				}
				llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) {
					models++
					if !strings.Contains(user, "ITEM safe ") || !strings.Contains(user, "PrivateRoute") {
						t.Fatal("safe context was lost before the model")
					}
					if privatePayload(user) || strings.Contains(user, "synthetic-private") {
						t.Fatal("private item/old/related text reached model")
					}
					var decisions []any
					for _, c := range []map[string]any{bad, safe} {
						if strings.Contains(user, "ITEM "+str(c["id"])+" ") {
							decisions = append(decisions, map[string]any{"id": c["id"], "risk": "low", "risk_reasons": []any{}, "dup_hint": ""})
						}
					}
					return decisions, nil
				}
				revPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
					if privatePayload([]any{path, body}) {
						t.Fatal("private data reached classification transport")
					}
					if !strings.HasSuffix(path, "/classify") {
						t.Fatal("reviewer performed a decision action")
					}
					for _, c := range []map[string]any{bad, safe} {
						if path == fmt.Sprintf("/v1/memory_candidates/%s/classify", c["id"]) {
							classified = append(classified, str(c["id"]))
							rev, _ := num(field(body, "expected_revision"))
							c["revision"] = rev + 1
							c["classification"] = map[string]any{"risk": field(body, "risk"), "by": "reviewer"}
							return c, nil
						}
					}
					t.Fatal("unknown classify identity")
					return nil, nil
				}
				if err := aiReview(false, io.Discard); err != nil {
					t.Fatalf("one private candidate blocked safe batch: %v", err)
				}
				if len(classified) == 0 || !reviewed(safe) || models != 1 {
					t.Fatalf("safe candidate not classified: classified=%v models=%d", classified, models)
				}
				if scenario == "candidate_text" {
					if !reviewed(bad) || autosaveGet(bad["classification"], "risk") != "high" {
						t.Fatal("private candidate was not persistently flagged for human review")
					}
				}
				if bad["text"] != originalText || bad["status"] != "candidate" || safe["status"] != "candidate" {
					t.Fatal("risk classification changed text/status")
				}
				priorCount := len(classified)
				priorModels := models
				if err := aiReview(false, io.Discard); err != nil {
					t.Fatalf("next run blocked: %v", err)
				}
				if len(classified) != priorCount || models != priorModels {
					t.Fatalf("next run selected already reviewed candidates: classified=%v models=%d reads=%d searches=%d", classified, models, reads, searches)
				}
			})
		}
	}
}

func TestPrivateReviewLegacyOutboxStopsWithoutChangingPreparedBytes(t *testing.T) {
	for _, key := range []string{"risk_reasons", "dup_hint", "scope"} {
		t.Run(key, func(t *testing.T) {
			capFake(t)
			body := map[string]any{"scope": map[string]any{}, "expected_revision": 1, "risk": "high"}
			marked := `before \u003cprivate>synthetic-old-reason\u003c/private> after`
			switch key {
			case "risk_reasons":
				body[key] = []any{marked}
			case "scope":
				body[key] = map[string]any{"workspace_id": marked}
			default:
				body[key] = marked
			}
			// Simulate a prepared write saved before the private boundary existed.
			requests := []map[string]any{{"path": "/v1/memory_candidates/legacy/classify", "body": body}}
			_, err := dispatchReviews(requests, func(string, any) (map[string]any, error) {
				return nil, errors.New("connection refused")
			})
			if err == nil {
				t.Fatal("legacy fixture did not retain a failed delivery")
			}
			files, err := filepath.Glob(filepath.Join(reviewOutboxDir(), "batch-*.json"))
			if err != nil || len(files) != 1 {
				t.Fatal("legacy prepared write missing", files, err)
			}
			before, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			timeNow = func() float64 { return float64(time.Now().Add(2*time.Hour).UnixNano()) / 1e9 }
			posts := 0
			revPost = func(string, any, time.Duration) (map[string]any, error) {
				posts++
				return nil, nil
			}
			capGet = func(string, time.Duration) (map[string]any, error) {
				t.Fatal("unsafe prepared write did not stop before new classification")
				return nil, nil
			}
			llm = func(string, string, string, time.Duration, string, string) (any, error) {
				t.Fatal("unsafe prepared write reached a model")
				return nil, nil
			}
			if _, err := replayReviews(sendClassify); !errors.Is(err, errReviewInvalid) {
				t.Fatal("unsafe replay was not held for manual recovery", err)
			}
			if err := aiReview(false, io.Discard); !errors.Is(err, errReviewInvalid) {
				t.Fatal("unsafe prepared write did not fail closed on the next run", err)
			}
			after, err := os.ReadFile(files[0])
			if err != nil || string(after) != string(before) || posts != 0 {
				t.Fatal("unsafe replay changed prepared bytes, retry state or transport", err, posts)
			}
		})
	}
}
