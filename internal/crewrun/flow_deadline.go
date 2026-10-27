package crewrun

import "errors"

var errFlowDeadline = errors.New("flow execution deadline exceeded")

func (e *Engine) flowExpired(state RunState) bool {
	return state.InvocationMode == "flow" && state.Deadline != nil && !e.now().Before(*state.Deadline)
}

func (e *Engine) flowNeedsAttention(run Run) bool {
	return run.State.InvocationMode == "flow" && (run.State.CancelRequested || e.flowExpired(run.State))
}

func (x *runExec) expireFlowLocked() bool {
	if x.flow == nil || !x.e.flowExpired(x.run.State) {
		return false
	}
	x.flow.cancel(errFlowDeadline)
	return true
}

func (x *runExec) expireFlow() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.expireFlowLocked()
}

func flowSameAttempt(n Node) bool {
	return flowChildNode(n) || n.Type == "ai" || n.Type == "ai_once" || n.Type == "expert" || n.Type == "loop" || n.Type == "notify"
}

func (f *flowExecution) stopOutcome(run Run) (string, string) {
	if run.State.CancelRequested {
		return "stopped", "사용자 요청으로 실행을 중단했어요"
	}
	if f.e.flowExpired(run.State) {
		return "failed", "time_limit: 정해 둔 실행 시간이 지났어요"
	}
	return "", ""
}
