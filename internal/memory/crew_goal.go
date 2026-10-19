package memory

import "slices"

// CrewGoal is what a workflow is for (자동화 상세 › 목표): the job, when it counts as done, and what it leaves.
type CrewGoal struct {
	Task   string `json:"task,omitempty"`
	Done   string `json:"done,omitempty"`
	Output string `json:"output,omitempty"`
}

// CrewNotifyChannel is one place a workflow reports progress to. The bot token stays in 설정 › 메신저.
type CrewNotifyChannel struct {
	Kind    string `json:"kind"`    // discord | telegram | slack
	Channel string `json:"channel"` // channel name or "나에게"
	When    string `json:"when"`    // events | approval | report
}

// CrewNotify is 진행 알림: the channels and the moments (start, step, approval, fail, end) that send.
type CrewNotify struct {
	Channels []CrewNotifyChannel `json:"channels,omitempty"`
	Moments  []string            `json:"moments,omitempty"`
}

var (
	crewNotifyKinds   = []string{"discord", "telegram", "slack"}
	crewNotifyWhen    = []string{"events", "approval", "report"}
	crewNotifyMoments = []string{"start", "step", "approval", "fail", "end"}
)

func validCrewGoal(g *CrewGoal) bool {
	return g == nil || runes(g.Task) <= 300 && runes(g.Done) <= 300 && runes(g.Output) <= 300
}

func validCrewNotify(n *CrewNotify) bool {
	if n == nil {
		return true
	}
	if len(n.Channels) > 10 || len(n.Moments) > len(crewNotifyMoments) {
		return false
	}
	for _, c := range n.Channels {
		if !slices.Contains(crewNotifyKinds, c.Kind) || !slices.Contains(crewNotifyWhen, c.When) || runes(c.Channel) > 80 {
			return false
		}
	}
	for i, m := range n.Moments {
		if !slices.Contains(crewNotifyMoments, m) || slices.Index(n.Moments, m) != i {
			return false
		}
	}
	return true
}

// Only a workflow carries a goal and progress notices.
func validCrewGoalNotify(c Crew) bool {
	if c.Type != "workflow" {
		return c.Goal == nil && c.Notify == nil
	}
	return validCrewGoal(c.Goal) && validCrewNotify(c.Notify)
}
