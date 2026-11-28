package memory

import (
	"context"
	"testing"
	"time"
)

type measuredSkillText struct{ usage ProviderUsage }

func (m measuredSkillText) Generator() Generator { return Generator{Model: m.usage.Model} }
func (m measuredSkillText) Complete(context.Context, string, string, int) (string, ProviderUsage, error) {
	return "draft", m.usage, nil
}

func TestLocalSkillChatKeepsMeasuredDraftUsage(t *testing.T) {
	for _, provider := range []string{"", "unrelated"} {
		u := ProviderUsage{Provider: provider, Model: "test-model", Requests: 1, InputTokens: 23, OutputTokens: 11, TotalTokens: 34}
		reply, err := completeSkillChat(context.Background(), LLMConfig{}, measuredSkillText{u}, chatRequest{provider: "skill_llm", timeout: time.Second})
		if provider == "" {
			u.Provider = "skill_llm"
		}
		if err != nil || !reply.complete || reply.usage != u || invalidSkillDraftUsage(reply.usage, u.Model) != (provider != "") {
			t.Fatalf("provider %q, measured usage %+v, error %v", provider, reply.usage, err)
		}
	}
}
