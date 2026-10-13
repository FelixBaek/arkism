package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func draftFixture() (SkillDraftInput, SkillDraft) {
	in := SkillDraftInput{SkillID: "review", Path: "/skills/review/SKILL.md"}
	for i := 0; i < 3; i++ {
		in.Lessons = append(in.Lessons, SkillDraftLesson{TaskType: "review", Lesson: "완료 전에 결과를 확인한다", VerificationMethod: "검사 결과 확인"})
	}
	a := *detailedSkillAnalysis()
	a.Tools, a.APIs, a.Permissions, a.SecretNames, a.Dependencies, a.IrreversibleActions = nil, nil, nil, nil, nil, nil
	a.Reads, a.Writes, a.ExternalTransfer = false, false, false
	return in, SkillDraft{Content: "# 결과 확인\n제공된 검사 결과로 완료 여부를 판단한다. 실패하면 변경 제안을 철회한다.", Analysis: a}
}

func draftResponse(content string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 15, "total_tokens": 27}}
}

func TestChatSkillDraft(t *testing.T) {
	in, answer := draftFixture()
	answer.Analysis.SkillID, answer.Analysis.SourcePath = "invented", "/invented"
	answer.Analysis.EvaluationStats = map[string]float64{"runs": 100, "passed": 100}
	var sent SkillDraftInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
			Model    string
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "draft-model" || len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" || !strings.Contains(req.Messages[0].Content, "untrusted data") {
			t.Error("invalid data boundary")
		}
		if !strings.Contains(req.Messages[0].Content, `"evaluation_stats":{}`) {
			t.Error("ambiguous evaluation statistics schema")
		}
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing credential")
		}
		if json.Unmarshal([]byte(req.Messages[1].Content), &sent) != nil {
			t.Error("invalid input")
		}
		_ = json.NewEncoder(w).Encode(draftResponse(structuredText(answer)))
	}))
	defer server.Close()
	drafter := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
	for _, baseline := range []bool{false, true} {
		if baseline {
			in.BaselineContent = "# Review\nCheck results."
			a := answer.Analysis
			a.SkillID, a.ContentHash = in.SkillID, ContentHash(in.BaselineContent)
			in.BaselineAnalysis = &a
		}
		got, usage, err := drafter.Draft(testCtx, in)
		if err != nil || got.Content != answer.Content || got.Analysis.SkillID != in.SkillID || got.Analysis.SourcePath != in.Path || got.Analysis.ContentHash != ContentHash(got.Content) || got.Analysis.EvaluationStats["runs"] != 0 || usage.TotalTokens != 27 {
			t.Fatalf("draft: %+v %+v %v", got, usage, err)
		}
		if len(sent.Lessons) != 3 || sent.Lessons[0].Lesson != in.Lessons[0].Lesson || sent.BaselineContent != in.BaselineContent {
			t.Fatal("lost input")
		}
		if baseline && (sent.BaselineAnalysis.EvaluationStats != nil || sent.BaselineAnalysis.ContentHash != "" || in.BaselineAnalysis.EvaluationStats["runs"] != 100) {
			t.Fatal("baseline identity/stats leaked or caller mutated")
		}
	}
	if g := drafter.Generator(); !validGenerator(&g) || g.PromptVersion != "skill-draft-v2" {
		t.Fatal("provenance")
	}
}

func TestChatSkillDraftRejectsBeforeTransfer(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	for _, kind := range []string{"secret", "oversized", "no_lessons", "empty_lesson", "baseline_missing", "baseline_hash", "model", "path"} {
		t.Run(kind, func(t *testing.T) {
			in, answer := draftFixture()
			d := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
			switch kind {
			case "secret":
				in.Lessons[0].Lesson = "Bearer abcdefghijklmnop"
			case "oversized":
				in.Lessons[0].Lesson = strings.Repeat("x", 64001)
			case "no_lessons":
				in.Lessons = nil
			case "empty_lesson":
				in.Lessons[0].VerificationMethod = ""
			case "baseline_missing":
				in.BaselineContent = "orphan"
			case "baseline_hash":
				in.BaselineContent, in.BaselineAnalysis = "changed", &answer.Analysis
			case "model":
				d.Config.Model = "Jev-1.13.0"
			case "path":
				in.Path = ""
			}
			if _, _, err := d.Draft(testCtx, in); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	if requests != 0 {
		t.Fatal("invalid material sent", requests)
	}
}

func TestChatSkillDraftInvalidOutputPreservesUsage(t *testing.T) {
	in, answer := draftFixture()
	for _, kind := range []string{"missing", "null", "unknown", "duplicate", "nested_duplicate", "trailing", "type", "stats_array", "empty", "secret", "truncated", "usage", "overflow", "provider_error"} {
		t.Run(kind, func(t *testing.T) {
			raw := structuredText(answer)
			switch kind {
			case "missing":
				raw = strings.Replace(raw, `"writes":false,`, "", 1)
			case "null":
				raw = strings.Replace(raw, `"writes":false`, `"writes":null`, 1)
			case "unknown":
				raw = strings.Replace(raw, `"writes":false`, `"extra":false`, 1)
			case "duplicate":
				raw = `{"content":"hidden",` + raw[1:]
			case "nested_duplicate":
				raw = strings.Replace(raw, `"writes":false`, `"writes":true,"writes":false`, 1)
			case "trailing":
				raw += `{}`
			case "stats_array":
				raw = strings.Replace(raw, `"evaluation_stats":null`, `"evaluation_stats":[]`, 1)
			case "type":
				raw = strings.Replace(raw, `"writes":false`, `"writes":"false"`, 1)
			case "empty":
				answerCopy := answer
				answerCopy.Content = " "
				raw = structuredText(answerCopy)
			case "secret":
				answerCopy := answer
				answerCopy.Content = "Bearer abcdefghijklmnop"
				raw = structuredText(answerCopy)
			}
			response := draftResponse(raw)
			if kind == "truncated" {
				response["choices"].([]any)[0].(map[string]any)["finish_reason"] = "length"
			}
			if kind == "usage" {
				delete(response, "usage")
			}
			if kind == "overflow" {
				response["usage"] = map[string]any{"prompt_tokens": int64(9223372036854775807), "completion_tokens": 1, "total_tokens": 1}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "provider_error" {
					w.WriteHeader(500)
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			d := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
			got, usage, err := d.Draft(testCtx, in)
			if err == nil || got.Content != "" {
				t.Fatal("invalid response accepted", got)
			}
			if kind != "usage" && kind != "overflow" && kind != "provider_error" && usage.TotalTokens != 27 {
				t.Fatal("billed usage lost")
			}
		})
	}
}

func TestChatSkillDraftCapabilityCeiling(t *testing.T) {
	for _, kind := range []string{"tools", "apis", "permissions", "secrets", "dependencies", "irreversible", "reads", "writes", "transfer"} {
		t.Run(kind, func(t *testing.T) {
			in, answer := draftFixture()
			switch kind {
			case "tools":
				answer.Analysis.Tools = []string{"shell"}
			case "apis":
				answer.Analysis.APIs = []string{"remote"}
			case "permissions":
				answer.Analysis.Permissions = []string{"admin"}
			case "secrets":
				answer.Analysis.SecretNames = []string{"CREDENTIAL"}
			case "dependencies":
				answer.Analysis.Dependencies = []string{"new-package"}
			case "irreversible":
				answer.Analysis.IrreversibleActions = []string{"delete"}
			case "reads":
				answer.Analysis.Reads = true
			case "writes":
				answer.Analysis.Writes = true
			case "transfer":
				answer.Analysis.ExternalTransfer = true
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(draftResponse(structuredText(answer)))
			}))
			defer server.Close()
			d := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
			if _, usage, err := d.Draft(testCtx, in); !errors.Is(err, ErrDenied) || usage.TotalTokens != 27 {
				t.Fatal("new capability accepted", err)
			}
			a := answer.Analysis
			in.BaselineContent = "existing permitted procedure"
			a.SkillID, a.ContentHash = in.SkillID, ContentHash(in.BaselineContent)
			in.BaselineAnalysis = &a
			if _, _, err := d.Draft(testCtx, in); err != nil {
				t.Fatal("existing capability rejected", err)
			}
		})
	}
}

func TestChatSkillDraftToProposal(t *testing.T) {
	f := newFixture(t)
	proposal := proposalFixture(t, f)
	in, answer := draftFixture()
	in.SkillID, in.Path = proposal.Skill.SkillID, proposal.Skill.Path
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(draftResponse(structuredText(answer)))
	}))
	defer server.Close()
	d := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
	got, _, err := d.Draft(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Generator()
	proposal.Skill.Content, proposal.Skill.Analysis, proposal.Generator = got.Content, &got.Analysis, &g
	result, err := f.svc.ProposeSkill(testCtx, f.agent, proposal)
	if err != nil || result.Record.Status != "candidate" || result.Skill.ActiveVersion != "" || result.Record.Generator.Model != "draft-model" {
		t.Fatal("draft not stored as candidate", err)
	}
}
