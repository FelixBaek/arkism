package crewrun

import (
	"context"
	"slices"
	"strings"
)

// 진행 알림: a workflow's crew.notify sends a line to its channels when the run
// starts, a step ends, a person must decide, it fails or it ends. Sending uses
// the notify node's code (notify.json destinations); a failed send is logged
// and never touches the run. Discord has no sender in this daemon yet, so a
// discord channel is skipped.

type NotifyChannel struct {
	Kind    string `json:"kind"`    // discord | telegram | slack
	Channel string `json:"channel"` // notify.json destination name ("#ops" → "ops"); unknown → "default"
	When    string `json:"when"`    // events | approval | report
}

type CrewNotify struct {
	Channels []NotifyChannel `json:"channels,omitempty"`
	Moments  []string        `json:"moments,omitempty"` // start | step | approval | fail | end; empty = all but step
}

var defaultMoments = []string{"start", "approval", "fail", "end"}

type progressEvent struct{ moment, text string }

// progress queues a moment; saveLocked sends the queue once the change is stored.
func (x *runExec) progress(moment, text string) {
	n := x.item.Crew.Notify
	if n == nil || len(n.Channels) == 0 || x.run.State.Trigger == "test" { // 시험 실행 sends nothing outside
		return
	}
	moments := n.Moments
	if len(moments) == 0 {
		moments = defaultMoments
	}
	if slices.Contains(moments, moment) {
		x.events = append(x.events, progressEvent{moment, text})
	}
}

// wants says whether a channel takes a moment: events → every chosen moment,
// approval → only 확인 필요, report → only the end (with the whole summary).
func (c NotifyChannel) wants(moment string) bool {
	switch c.When {
	case "approval":
		return moment == "approval"
	case "report":
		return moment == "end"
	}
	return true
}

func (x *runExec) flushProgress() {
	if len(x.events) == 0 {
		return
	}
	evs, chans, name := x.events, x.item.Crew.Notify.Channels, x.item.Crew.Name
	x.events = nil
	e := x.e
	// Sends leave in order: each batch waits for the one before it.
	prev, done := x.sending, make(chan struct{})
	x.sending = done
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer close(done)
		if prev != nil {
			<-prev
		}
		ctx := context.Background()
		for _, ev := range evs {
			for _, c := range chans {
				if c.Kind == "discord" || !c.wants(ev.moment) {
					continue
				}
				text := ev.text
				if c.When != "report" {
					text = clip(text, 300)
				}
				msg := "[" + name + "] " + text
				to := strings.TrimLeft(strings.TrimSpace(c.Channel), "#")
				_, err := e.notify(ctx, map[string]string{"channel": c.Kind, "to": to}, msg)
				if err != nil && to != "" && to != "default" { // "#리서치" or "나에게" with no entry of its own: the default destination
					_, err = e.notify(ctx, map[string]string{"channel": c.Kind}, msg)
				}
				if err != nil {
					e.log().Warn("crew progress notice", "crew_id", x.item.ID, "channel", c.Kind, "error", err.Error())
				}
			}
		}
	}()
}

// stepNode: nodes whose end is worth a 단계마다 line (not the trigger roots or the end).
func stepNode(t string) bool {
	return t != "start" && t != "schedule" && t != "webhook" && t != "end"
}
