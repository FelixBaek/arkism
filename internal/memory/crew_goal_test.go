package memory

import "testing"

func TestValidCrewGoalNotify(t *testing.T) {
	ok := Crew{Type: "workflow", Goal: &CrewGoal{Task: "a"}, Notify: &CrewNotify{Channels: []CrewNotifyChannel{{Kind: "discord", Channel: "#a", When: "events"}}, Moments: []string{"start", "end"}}}
	if !validCrewGoalNotify(ok) {
		t.Fatal("valid goal and notify rejected")
	}
	for name, c := range map[string]Crew{
		"agent goal":   {Type: "agent", Goal: &CrewGoal{Task: "a"}},
		"bad kind":     {Type: "workflow", Notify: &CrewNotify{Channels: []CrewNotifyChannel{{Kind: "sms", When: "events"}}}},
		"dup moment":   {Type: "workflow", Notify: &CrewNotify{Moments: []string{"end", "end"}}},
		"unknown when": {Type: "workflow", Notify: &CrewNotify{Channels: []CrewNotifyChannel{{Kind: "slack", When: "x"}}}},
	} {
		if validCrewGoalNotify(c) {
			t.Fatal(name, "accepted")
		}
	}
}
