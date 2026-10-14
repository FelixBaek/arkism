package memory

import (
	"strings"
	"testing"
)

func TestValidAgentInfos(t *testing.T) {
	ok := AgentInfo{ID: "claude", Name: "Claude", SkillsDirs: []string{"~/.claude/skills", "/opt/skills"}, ChatsDir: "~/.claude/projects", Skills: 3}
	if !validAgentInfos(nil) || !validAgentInfos([]AgentInfo{ok}) {
		t.Fatal("valid list refused")
	}
	if !validAgentInfos([]AgentInfo{{ID: "a", Name: "가나다", Skills: 0}}) {
		t.Fatal("chats dir is optional")
	}
	bad := map[string]func(a *AgentInfo){
		"uppercase id":       func(a *AgentInfo) { a.ID = "Claude" },
		"digit-first id":     func(a *AgentInfo) { a.ID = "1claude" },
		"long id":            func(a *AgentInfo) { a.ID = "a" + strings.Repeat("b", 40) },
		"blank name":         func(a *AgentInfo) { a.Name = "  " },
		"long name":          func(a *AgentInfo) { a.Name = strings.Repeat("이", 41) },
		"too many dirs":      func(a *AgentInfo) { a.SkillsDirs = []string{"/a", "/b", "/c", "/d", "/e"} },
		"relative chats":     func(a *AgentInfo) { a.ChatsDir = "projects" },
		"negative skills":    func(a *AgentInfo) { a.Skills = -1 },
		"empty skills dir":   func(a *AgentInfo) { a.SkillsDirs = []string{""} },
		"dotdot skills dir":  func(a *AgentInfo) { a.SkillsDirs = []string{"~/../etc"} },
		"control char dir":   func(a *AgentInfo) { a.SkillsDirs = []string{"/opt/\nskills"} },
		"over-long dir":      func(a *AgentInfo) { a.SkillsDirs = []string{"/" + strings.Repeat("d", 200)} },
		"relative skill dir": func(a *AgentInfo) { a.SkillsDirs = []string{"skills"} },
	}
	for name, mutate := range bad {
		a := ok
		a.SkillsDirs = append([]string(nil), ok.SkillsDirs...)
		mutate(&a)
		if validAgentInfos([]AgentInfo{ok, a}) {
			t.Errorf("%s accepted", name)
		}
	}
	if validAgentInfos(make([]AgentInfo, 41)) {
		t.Fatal("more than 40 agents accepted")
	}
}
