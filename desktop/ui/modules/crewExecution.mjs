// Owns run commands. Dependencies are explicit live ports, never a global scope.
export function createCrewExecution(deps) {


  // ---------------------------------------------------------------- runs
  // 실행: a run the host engine picks up within ten seconds. The editor opens the run view (03b) on it.
  async function startRun(rec, text, only, nodesFrom, until) {
    const g = rec.crew.graph;
    if (!deps.stepCount(g)) { deps.notice("아직 할 일이 없어요. 편집에서 노드를 넣어 주세요."); return null; }
    const nodes = Object.fromEntries((g.nodes || []).map((n) => [n.id, { status: "pending" }]));
    let loops;
    if (nodesFrom) {
      for (const [id, st] of Object.entries(nodesFrom.nodes || {})) if (nodes[id] && st.status === "done") nodes[id] = { status: "done", output: st.output, port: st.port, runs: st.runs };
      loops = nodesFrom.loops;
    }
    // 여기까지: what the chosen step does not need is marked skipped, so the run stops after it.
    if (until) { const need = new Set([until, ...deps.upstream(g, until).map((n) => n.id)]); for (const id of Object.keys(nodes)) if (!need.has(id)) nodes[id] = { status: "skipped" }; }
    const run = { crew_id: rec.id, crew_revision: rec.revision, trigger: "manual", status: "running", input: deps.runInput(text, only), nodes, ...(only ? { only } : {}), ...(loops ? { loops } : {}) };
    try {
      const r = await deps.request("/v1/crew/runs", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, run }) });
      deps.notice(only ? "이 노드만 실행을 맡겼어요. 최근 실행에서 볼 수 있어요." : until ? "여기까지 실행을 맡겼어요. 서버 실행기가 10초 안에 시작해요." : "실행을 맡겼어요. 서버 실행기가 10초 안에 시작해요.");
      return r;
    } catch (e) {
      if (nodesFrom) return startRun(rec, text, only);
      deps.notice(`실행하지 못했어요 (${e.message}).`);
      return null;
    }
  }

  const RUN_LABEL = { succeeded: "완료", failed: "실패", waiting: "확인 대기", running: "실행 중", stopped: "멈춤" };

  const NODE_LABEL = { done: "완료", running: "실행 중", waiting: "기다리는 중", failed: "실패", skipped: "건너뜀", pending: "기다리는 중" };

  async function decide(run, approve) {
    const cr = run.crew_run;
    try {
      await deps.request(`/v1/crew/runs/${encodeURIComponent(run.id)}/decision`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: run.revision, node_id: cr.waiting.node_id, approve }) });
      deps.notice(approve ? "승인했어요. 다음 단계로 넘어가요." : "거절했어요. 그 갈래는 멈춰요.");
    } catch (e) {
      deps.notice(e.message === "revision_conflict" ? "실행 상태가 바뀌었어요. 다시 열어 주세요." : `보내지 못했어요 (${e.message}).`);
    }
  }

  async function stopRun(run) {
    try {
      await deps.request(`/v1/crew/runs/${encodeURIComponent(run.id)}/progress`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: run.revision, run: { ...run.crew_run, status: "stopped", waiting: undefined } }) });
      deps.notice("멈췄어요. 돌던 단계는 끝까지 가고 다음 단계는 시작하지 않아요.");
    } catch (e) {
      deps.notice(`멈추지 못했어요 (${e.message}).`);
    }
  }

  return {
    get decide() { return decide; },
    get NODE_LABEL() { return NODE_LABEL; },
    get RUN_LABEL() { return RUN_LABEL; },
    get startRun() { return startRun; },
    get stopRun() { return stopRun; }
  };
}
