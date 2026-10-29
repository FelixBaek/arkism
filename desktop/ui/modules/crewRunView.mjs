// Owns run polling and history. Dependencies are explicit live ports, never a global scope.
export function createCrewRunView(deps) {


  // A step on an agent runner connection (Figma 1314:81577): nodes.<id>.backend holds names and ids only (ST-840).
  const RUNNER_STATE = { starting: "보내는 중", accepted: "받음", running: "실행 중", waiting_approval: "승인 대기", succeeded: "끝남", failed: "실패", cancel_requested: "취소 요청함", cancelled: "취소됨", unknown: "알 수 없음" };
  const NOT_APPLIED = { model: "고른 모델", instructions: "지시", tool_restriction: "도구 제한", memory: "기억" };
  function runnerLine(st) {
    const b = st.backend;
    if (!b?.backend) return "";
    const memory = (b.context || []).filter((c) => String(c).startsWith("memory:")).length;
    return [`외부 실행기 ${b.backend} · ${RUNNER_STATE[b.state] || b.state || ""}`, b.task_id && `작업 ${b.task_id}`, b.model && `모델 ${b.model}`,
      b.not_applied?.length && `못 쓴 것: ${b.not_applied.map((k) => NOT_APPLIED[k] || k).join(" / ")}`, memory && `기억 ${memory}개 보냄`, b.usage !== "reported" && "비용 모름"].filter(Boolean).join(" · ");
  }

  // ---------------------------------------------------------------- run view (03b)
  const polls = globalThis.ArkismKeep.latest();
  const sessionKey = () => JSON.stringify([deps.state.server, deps.state.scope, deps.state.token, deps.state.connection?.generation]);
  function stopPolling() {
    polls.cancel();
    if (deps.ed.timer) { clearTimeout(deps.ed.timer); deps.ed.timer = null; }
  }
  async function pollRun() {
    stopPolling();
    const epoch = polls.begin(), session = sessionKey(), record = deps.ed.rec;
    const key = deps.ed.run ? "run" : "test";
    const id = deps.ed[key]?.id;
    const current = () => epoch.live() && session === sessionKey() && record === deps.ed.rec && deps.ed[key]?.id === id;
    if (!id || !deps.ed.rec) return;
    try {
      const response = await deps.request(`/v1/crew/runs/${encodeURIComponent(id)}?${deps.query()}`);
      if (!current() || response.id !== id) return;
      deps.ed[key] = response;
      if (!deps.nodeDlg) deps.render(false);
    } catch { /* A short outage retries only in the same editor and connection. */ }
    if (!current()) return;
    const status = deps.ed[key]?.crew_run?.status;
    if (status === "running" || status === "waiting") deps.ed.timer = setTimeout(pollRun, 2500);
  }
  // 이번 실행 (198:2436 오른쪽): when and how it goes, the steps, why it went back, the workers, the budget.
  const RUN_STATE = { running: "실행기가 진행 중", waiting: "확인을 기다려요", succeeded: "끝났어요", failed: "멈췄어요", stopped: "멈췄어요" };

  const STEP_CLS = { running: "live", waiting: "live", failed: "fail", done: "", pending: "quiet", skipped: "quiet" };

  const hhmm = (t) => (t ? new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(t)) : "");

  const nodeDur = (st) => (st?.started_at && st.finished_at ? deps.spanText(new Date(st.finished_at) - new Date(st.started_at)) : "");

  function stepState(n, st) {
    const port = st.port ? deps.PORTS[n.type]?.find(([k]) => k === st.port)?.[1] || st.port : "";
    if (st.status === "done") return [port && port !== "통과" && port !== "승인" ? `${port}` : deps.NODE_LABEL.done, nodeDur(st)].filter(Boolean).join(" · ");
    if (st.status === "running") return "실행 중";
    if (st.status === "waiting") return n.type === "approval" ? "확인 대기" : "기다리는 중";
    if (st.status === "failed") return st.error ? `실패 · ${st.error.slice(0, 24)}` : "실패";
    return deps.NODE_LABEL[st.status] || st.status;
  }

  // The steps the run reached, in graph order (a node that ran twice shows once, with how often).
  const runSteps = (cr) => deps.levels(deps.ed.graph).flat().filter((n) => cr.nodes?.[n.id]);

  function failedNode(cr) {
    const id = Object.entries(cr.nodes || {}).find(([, st]) => st.status === "failed")?.[0];
    return id ? deps.nodeIn(id) : null;
  }

  // 확인 대기 · 실패: the amber card on top, with the answer buttons or the reason.
  function runAttn(run, after) {
    const cr = run.crew_run || {};
    if (cr.status === "waiting" && cr.waiting?.kind === "approval") {
      const node = deps.nodeIn(cr.waiting.node_id);
      const a = deps.el("div", "wf-attn crew-approval");
      const t = deps.el("div", "");
      t.append(deps.el("strong", "", node ? `${deps.nodeTitle(node)} · 사람 확인 대기` : "사람 확인 대기"), deps.el("p", "", cr.waiting.question || node?.config?.question || "다음 단계로 갈까요?"));
      const row = deps.el("div", "wf-attn-actions crew-approval-actions");
      row.append(deps.button("거절", "", null, async () => { await deps.decide(run, false); after(); }), deps.button("승인", "primary", null, async () => { await deps.decide(run, true); after(); }));
      a.append(t, row);
      return a;
    }
    if (cr.status === "failed") {
      const n = failedNode(cr);
      const a = deps.el("div", "wf-attn small");
      const t = deps.el("div", "");
      t.append(deps.el("strong", "", n ? `${deps.nodeTitle(n)} 단계가 실패했어요` : "실행이 멈췄어요"), deps.el("p", "", deps.readable(cr.summary) || cr.nodes?.[n?.id]?.error || "이유가 남지 않았어요."));
      a.append(t);
      return a;
    }
    return null;
  }

  function runPanel() {
    const box = deps.el("aside", "g-right run");
    const run = deps.ed.run;
    const cr = run.crew_run || {};
    const req = deps.runRequest(cr);
    const head = deps.el("div", "wf-run-head");
    head.append(deps.el("small", "", "이번 실행"), deps.el("strong", "", `${hhmm(run.created_at)} 시작 · ${RUN_STATE[cr.status] || cr.status || ""}`));
    if (req) head.append(deps.el("small", "", `“${req.slice(0, 60)}”`));
    box.append(head);
    const again = rerunOf();
    if (again) box.append(deps.el("h4", "", "다시 실행"), deps.kv([["시작한 곳", again.from || "—"], ["다시 쓰는 결과", `${again.reused}단계 (앞선 실행)`], ["설정", "실패했을 때와 같아요"]]),
      deps.el("h4", "", "끝나면"), deps.el("p", "meta", "새 결과가 실행 기록에 쌓이고, 실패 기록도 함께 남아요."));
    const attn = deps.ed.runList ? null : runAttn(run, pollRun); // the list view shows it above the table
    if (attn) box.append(attn);
    const steps = runSteps(cr);
    const work = steps.filter(deps.WORK);
    const h = deps.el("h4", "", "단계");
    h.append(deps.el("small", "", `${work.filter((n) => cr.nodes[n.id].status === "done").length} / ${work.length}`));
    box.append(h);
    const list = deps.el("div", "wf-run-steps");
    for (const n of steps) {
      const st = cr.nodes[n.id];
      const r = deps.el("div", `wf-run-step st-${st.status}`);
      r.append(deps.el("span", "", `${deps.nodeTitle(n)}${st.runs > 1 ? ` (${st.runs}회째)` : ""}`), deps.el("small", STEP_CLS[st.status] || "", stepState(n, st)));
      r.title = [st.error, runnerLine(st)].filter(Boolean).join("\n");
      list.append(r);
    }
    box.append(list);
    const loops = Object.entries(cr.loops || {}).filter(([, v]) => v > 0);
    for (const [k, v] of loops) {
      const [ft, port] = k.split(":");
      const [f, to] = ft.split(">");
      const e = deps.ed.graph.edges.find((x) => x.from === f && x.to === to && (x.port || "") === (port || ""));
      const card = deps.el("div", "wf-why");
      const t = deps.el("strong", "", "고른 길과 이유");
      t.append(deps.el("small", "", `되돌아감 ${v}${e ? `/${e.max}` : "번"}`));
      const portText = deps.PORTS[deps.nodeIn(f)?.type]?.find(([p]) => p === port)?.[1] || port;
      card.append(t, deps.el("p", "", `${deps.nodeTitle(deps.nodeIn(f) || { type: f })}${portText ? ` (${portText})` : ""} → ${deps.nodeTitle(deps.nodeIn(to) || { type: to })}로 ${v}번 되돌아갔어요. 성공한 노드 결과는 그대로 두고 그 칸부터 다시 돌아요.`));
      box.append(card);
    }
    for (const n of steps) {
      const out = cr.nodes?.[n.id]?.output;
      if (n.type !== "ai" || !out) continue;
      try {
        const v = JSON.parse(out);
        if (Array.isArray(v?.workers) && v.workers.length) {
          const wh = deps.el("h4", "", "협업 워커");
          wh.append(deps.el("small", "", `${deps.nodeTitle(n)} · ${v.workers.length}명`));
          box.append(wh);
          const wl = deps.el("div", "wf-run-steps");
          for (const w of v.workers) {
            const r = deps.el("div", "wf-run-step");
            r.title = w.task || "";
            r.append(deps.el("span", "", w.worker || "워커"), deps.el("small", w.ok ? "" : "fail", w.ok ? "완료" : "실패"));
            wl.append(r);
          }
          box.append(wl);
        }
      } catch { /* plain text output */ }
    }
    // 예산: time against limits.max_minutes and node runs against limits.max_steps (tokens are not counted per run).
    const lim = deps.ed.rec.crew.limits || {};
    const lastEnd = Math.max(0, ...Object.values(cr.nodes || {}).map((s) => (s.finished_at ? new Date(s.finished_at).getTime() : 0)));
    const live = cr.status === "running" || cr.status === "waiting";
    const spent = run.created_at ? (live ? Date.now() : lastEnd || Date.now()) - new Date(run.created_at).getTime() : 0;
    const used = Object.values(cr.nodes || {}).reduce((s, x) => s + (x.status === "pending" ? 0 : x.runs || 1), 0);
    const cards = deps.el("div", "wf-budget");
    const card = (label, value, of, pct) => {
      const c = deps.el("div", "wf-budget-card");
      const v = deps.el("strong", "", value);
      if (of) v.append(deps.el("small", "", ` / ${of}`));
      c.append(deps.el("small", "", label), v);
      if (pct !== null) c.append(deps.el("small", "", `${pct}% 사용`));
      cards.append(c);
    };
    card("시간", deps.spanText(spent), lim.max_minutes ? `${lim.max_minutes}분` : "", lim.max_minutes ? Math.min(100, Math.round(spent / 600 / lim.max_minutes)) : null);
    card("단계", String(used), lim.max_steps ? `${lim.max_steps}번` : "", lim.max_steps ? Math.min(100, Math.round((used / lim.max_steps) * 100)) : null);
    box.append(deps.el("h4", "", "예산"), cards);
    if (cr.summary && cr.status !== "failed") { box.append(deps.el("h4", "", "결과")); const pre = deps.el("pre", "try-answer"); pre.textContent = cr.summary; box.append(pre); }
    const acts = deps.el("div", "crew-actions wf-run-acts");
    if (cr.status === "running" || cr.status === "waiting") acts.append(deps.button("멈추기", "", "stop-circle", async () => { await deps.stopRun(deps.ed.run); void pollRun(); }));
    if (cr.status === "failed") acts.append(deps.button("실패한 곳부터 다시", "", null, retryRun));
    acts.append(deps.button("편집으로", "ghost", null, () => { stopPolling(); deps.ed.run = null; deps.render(false); }));
    box.append(acts);
    return box;
  }

  // 실패한 곳부터 다시 (198:6994): the done nodes keep their results, the rest runs again.
  // 여기부터 다시: from and every step after it run again; the past run stays as it was.
  async function retryRun(from) {
    let cr = deps.ed.run.crew_run || {};
    if (typeof from === "string") {
      const again = new Set([from]);
      for (let grew = true; grew; ) { grew = false; for (const e of deps.fwd(deps.ed.rec.crew.graph)) if (again.has(e.from) && !again.has(e.to)) { again.add(e.to); grew = true; } }
      cr = { ...cr, loops: {}, nodes: Object.fromEntries(Object.entries(cr.nodes || {}).filter(([id]) => !again.has(id))) };
    }
    const r = await deps.startRun(deps.ed.rec, deps.runRequest(cr), null, cr);
    if (!r) return;
    // 실패한 곳부터 재실행 (198:6994): the list of steps, where it started again and how many results it reuses.
    const work = deps.levels(deps.ed.graph).flat().filter(deps.WORK);
    const at = work.findIndex((n) => cr.nodes?.[n.id]?.status !== "done");
    deps.ed.rerun = { id: r.id, from: at < 0 ? "" : `${at + 1} · ${deps.nodeTitle(work[at])}`, reused: work.filter((n) => cr.nodes?.[n.id]?.status === "done").length };
    deps.ed.run = r;
    deps.ed.runList = true;
    deps.render(false);
    void pollRun();
  }

  const rerunOf = () => (deps.ed.rerun && deps.ed.run && deps.ed.rerun.id === deps.ed.run.id ? deps.ed.rerun : null);

  // 목록으로 보기 (198:5360 · 198:6908): the same run as a table of steps instead of the graph.
  function runList() {
    const run = deps.ed.run;
    const cr = run.crew_run || {};
    const page = deps.el("div", "wf-runlist");
    const attn = runAttn(run, pollRun);
    if (attn) {
      if (cr.status === "failed") attn.append(deps.button("실패한 곳부터 다시", "primary", null, retryRun));
      page.append(attn);
    }
    const again = rerunOf();
    page.append(deps.el("h4", "", again ? "현재 단계" : "실행 단계"));
    const table = deps.el("div", "wf-steptable");
    const headRow = deps.el("div", "wf-steprow head");
    headRow.append(deps.el("span", "", "단계"), deps.el("span", "", "결과"), deps.el("span", "", "시간"));
    table.append(headRow);
    runSteps(cr).filter(deps.WORK).forEach((n, i, all) => {
      const st = cr.nodes[n.id];
      const row = deps.el("div", `wf-steprow st-${st.status}`);
      const name = deps.el("span", "");
      name.append(deps.el("strong", "", `${i + 1} · ${deps.nodeTitle(n)}${st.runs > 1 ? ` · ${st.runs}회째` : ""}`));
      if (st.error) name.append(deps.el("small", "", st.error));
      else if (again && st.status === "done" && !st.started_at) name.append(deps.el("small", "", "앞선 실행 결과를 사용해요."));
      else if (again && st.status === "running") name.append(deps.el("small", "", "같은 설정으로 다시 실행하고 있어요."));
      else if (again && st.status === "pending" && i) name.append(deps.el("small", "", `${deps.nodeTitle(all[i - 1])}가 끝나면 이어서 실행해요.`));
      else if (st.status === "pending") name.append(deps.el("small", "", "앞 단계가 끝나면 실행해요."));
      if (runnerLine(st)) name.append(deps.el("small", "", runnerLine(st)));
      if (st.status === "done" && ["succeeded", "failed", "stopped"].includes(cr.status)) name.append(deps.button("여기부터 다시", "ghost", null, () => retryRun(n.id)));
      row.append(name, deps.el("span", `wf-badge small ${{ failed: "fail", waiting: "wait", running: "live", pending: "line", skipped: "line" }[st.status] || ""}`, st.status === "pending" ? "실행 전" : stepState(n, st).split(" · ")[0]), deps.el("span", "mono", nodeDur(st) || "—"));
      table.append(row);
    });
    page.append(table);
    const loops = Object.values(cr.loops || {}).reduce((a, b) => a + b, 0);
    if (loops) page.append(deps.el("p", "meta", `되돌아가는 선을 ${loops}번 따라가 앞 단계를 다시 돌았어요.`));
    if (again) page.append(deps.el("p", "meta", "새 결과가 도착하면 실행 기록에 표시돼요."));
    return page;
  }


  // 실행할 때 맡길 일: a small window instead of window.prompt, which the desktop webview may block.
  function askTask(rec) {
    return new Promise((resolve) => {
      const dlg = deps.el("dialog", "crew-dialog wf-dialog ask-dialog");
      const form = deps.el("form");
      form.method = "dialog";
      const q = deps.input("", { maxLength: 4000, placeholder: `$${rec.crew.handle}에게 맡길 일 (비워도 돼요)` });
      const body = deps.el("div", "dialog-body");
      body.append(deps.el("h2", "", `${rec.crew.name} 실행`), q);
      const foot = deps.el("div", "dialog-foot");
      const cancel = deps.button("취소", "ghost");
      cancel.type = "submit";
      cancel.value = "cancel";
      const ok = deps.button("실행", "primary", "play");
      ok.type = "submit";
      ok.value = "run";
      foot.append(cancel, ok);
      form.append(body, foot);
      let answer = null;
      form.addEventListener("submit", (e) => { if (e.submitter?.value === "run") answer = q.value.trim(); });
      dlg.append(form);
      dlg.addEventListener("close", () => { dlg.remove(); resolve(answer); });
      document.body.append(dlg);
      dlg.showModal();
      q.focus();
    });
  }

  async function runFromEditor() {
    const text = await askTask(deps.ed.rec);
    if (text === null) return;
    const r = await deps.startRun(deps.ed.rec, text);
    if (r) { deps.ed.test = r; deps.render(false); void pollRun(); }
  }

  return {
    get askTask() { return askTask; },
    get nodeDur() { return nodeDur; },
    get pollRun() { return pollRun; },
    get rerunOf() { return rerunOf; },
    get runFromEditor() { return runFromEditor; },
    get runList() { return runList; },
    // eslint-disable-next-line no-func-assign -- live port: another module may swap this function
    set runList(value) { runList = value; },
    get runPanel() { return runPanel; },
    get stepState() { return stepState; },
    get stopPolling() { return stopPolling; }
  };
}
