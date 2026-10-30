import { createFlowDeliveryRecovery } from "./flowDeliveryRecovery.mjs";

const STATUS = { running: "실행 중", waiting: "확인 대기", succeeded: "완료", failed: "실패", stopped: "멈춤", partial: "부분 완료", concluded: "완료", no_conclusion: "결론 없음", pending: "대기 중", stopping: "중단 요청됨" };
const DELIVERY = { waiting_run: "결과를 기다리는 중", pending: "전달 대기", sending: "전달 중", retry_wait: "전달 재시도 대기", unknown: "전달 여부 확인 필요", failed: "전달 실패", delivered: "전달됨" };

export function flowNeedsDecision(waiting) {
  return waiting?.kind === "approval" || waiting?.kind === "budget" || waiting?.kind === "child" && ["approval", "budget"].includes(waiting.decision_kind);
}

// All result bodies are rendered as text. Every async response is confined to
// the initiating page and connection; no private result survives a scope switch.
export function createFlowHistory({ state, el, button, request, query, notice, ago, go, span = () => "" }) {
  const recovery = createFlowDeliveryRecovery({ state, el, button, request, notice });
  const views = globalThis.ArkismKeep.latest();
  const session = () => JSON.stringify([state.server, state.scope, state.token, state.connection?.generation]);
  const raw = (node) => { node.setAttribute("data-no-i18n", ""); return node; };
  const paragraph = (text) => el("p", "flow-history-note", text);
  const pad = (n) => String(n).padStart(2, "0");
  const stamp = (t) => { const d = new Date(t); return Number.isNaN(d.getTime()) ? "" : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`; };
  const mark = (ok, bad) => el("span", `flow-mark${ok ? " ok" : bad ? " bad" : ""}`, ok ? "✓" : bad ? "!" : "○");
  function view(container) {
    const turn = views.begin(), key = session();
    return () => turn.live() && key === session() && container.isConnected;
  }
  function heading(box, title, refresh, lead = [], extra = []) {
    const row = el("div", "flow-history-head");
    const h = el("h2", "", title);
    row.append(...lead, h, el("span", "grow"), ...extra, button("새로고침", "ghost", "arrow-clockwise", refresh));
    box.append(row);
    return h;
  }
  function row(title, detail, action) {
    const pick = button("", "flow-history-row", null, action);
    pick.append(raw(el("strong", "", title)), raw(el("small", "", detail)));
    return pick;
  }
  function failure(box) {
    box.replaceChildren(paragraph("불러오지 못했어요. 연결과 접근 권한을 확인한 뒤 새로고침해 주세요."));
  }
  function detailShell(container, title, back, refresh, extra = []) {
    const current = view(container);
    container.replaceChildren();
    const backButton = button("목록으로", "ghost", "arrow-left", back);
    const h = heading(container, title, refresh, [backButton], extra);
    const body = el("section", "flow-history-detail");
    body.setAttribute("aria-live", "polite");
    body.append(paragraph("불러오는 중…"));
    container.append(body);
    backButton.focus();
    return { current, body, h };
  }
  // 1480:7861: a section with its title on the left and what it holds on the right
  function section(box, title, hint) {
    const head = el("div", "flow-sec-head");
    head.append(el("h3", "", title), el("small", "", hint));
    box.append(head);
  }
  // back: where 목록으로 returns (the workflow's 기록 list); child runs and meetings come back to this result.
  // info (from 기록): when·kind for the title, steps() for 단계 보기, nodeName(id) for the step a run stopped at or a child ran from.
  async function openResult(id, container, back, info = {}) {
    const again = () => void openResult(id, container, back, info);
    const extra = info.steps ? [button("단계 보기", "ghost", "tree-structure", () => info.steps())] : [];
    const title = ["실행 결과", info.when, info.kind].filter(Boolean).join(" · ");
    const { current, body, h } = detailShell(container, title, back, again, extra);
    try {
      const result = await request(`/v1/crew/runs/${encodeURIComponent(id)}/result?${query()}`);
      if (!current()) return;
      // A result for another run (an older server, or a mix-up) must not leave the page saying 불러오는 중 forever.
      if (result.run_id !== id) { failure(body); return; }
      if (!["succeeded", "concluded"].includes(result.status)) h.textContent = `${title} · ${STATUS[result.status] || result.status}`;
      body.replaceChildren();
      resultActions(body, result, again, current, info);
      section(body, "최종 결과", "마지막 단계가 낸 글이에요");
      body.append(raw(el("pre", "flow-result-text", result.result || "아직 최종 결과가 없어요.")));
      delivery(body, result.delivery?.delivery);
      recovery.render(body, result.delivery, again, current);
      const children = Object.values(result.children || {});
      if (children.length) section(body, "하위 실행 · 회의 결과", "눌러서 그 실행의 결과를 열어요");
      const list = el("div", "flow-lines");
      for (const child of children) {
        const meeting = child.kind === "meeting";
        const pick = button("", "flow-line", null, () => meeting ? void openMeeting(child.id, container, again) : void openResult(child.id, container, again));
        pick.append(...childLine(child, meeting, info.nodeName));
        list.append(pick);
      }
      if (children.length) body.append(list);
    } catch { if (current()) failure(body); }
  }
  // 1480:7861 row: mark · start time · kind · name … duration · tokens (rounds for a meeting) · 열기 →
  function childLine(child, meeting, nodeName) {
    const ok = ["succeeded", "concluded"].includes(child.status), bad = ["failed", "stopped", "no_conclusion"].includes(child.status);
    const busy = child.status && !child.finished_at;
    const name = el("strong", "");
    name.append(raw(el("span", "", child.name || nodeName?.(child.node_id) || child.id)));
    if (meeting && child.status === "concluded") name.append(el("span", "", " · 결론 남"));
    const took = child.started_at && child.finished_at ? span(new Date(child.finished_at) - new Date(child.started_at)) : "";
    const tokens = child.tokens ? `${child.tokens >= 1000 ? `${Math.round(child.tokens / 1000)}k` : child.tokens} 토큰` : "";
    const end = [busy ? STATUS[child.status] || child.status : "", ...(meeting ? [child.round ? `라운드 ${child.round}` : ""] : [took, tokens]), "열기 →"].filter(Boolean).join(" · ");
    return [mark(ok, bad), raw(el("small", "", stamp(child.started_at))), el("small", "", meeting ? "회의 결과" : "하위 실행"), name, el("span", "grow"), el("small", "", end)];
  }
  // 확인해 주세요 (1480:7861): where it stopped and what it asks, with 중단 요청 · 거절 · 승인 in one row.
  // info.steps(id) opens the run on the graph with that step picked: amber where it waits, red where it failed.
  function resultActions(box, result, reload, current, { nodeName, steps, failed } = {}) {
    const actions = el("div", "flow-history-actions");
    const onGraph = (id) => (steps ? [button("그래프에서 보기", "ghost small", "tree-structure", () => steps(id))] : []);
    const waiting = result.waiting;
    const deciding = result.status === "waiting" && flowNeedsDecision(waiting);
    if (["running", "waiting"].includes(result.status) && result.invocation_mode === "flow") {
      const stop = button(result.cancel_requested ? "중단 요청됨" : "중단 요청", "ghost", "stop-circle", () => current() && void changeRun(result, "stop", {}, reload, current));
      stop.disabled = !!result.cancel_requested;
      actions.append(stop);
    }
    if (deciding) {
      const step = nodeName?.(waiting.node_id);
      const banner = el("div", "flow-decide");
      const text = el("div", "");
      text.append(raw(el("strong", "", step ? `확인해 주세요 · ${step} 단계에서 멈췄어요` : "확인해 주세요 · 사람의 결정이 필요해요")), raw(paragraph(waiting.question || "다음 단계로 갈까요?")), ...onGraph(waiting.node_id));
      actions.append(button("거절", "", null, () => current() && void changeRun(result, "decision", { node_id: waiting.node_id, approve: false }, reload, current)), button("승인", "primary", null, () => current() && void changeRun(result, "decision", { node_id: waiting.node_id, approve: true }, reload, current)));
      banner.append(text, actions);
      box.append(banner);
    } else if (actions.childElementCount) box.append(actions);
    const broke = result.status === "failed" && failed && nodeName?.(failed);
    if (broke) {
      const banner = el("div", "flow-decide err");
      const text = el("div", "");
      text.append(raw(el("strong", "", `${broke} 단계가 실패했어요`)));
      banner.append(text, ...onGraph(failed));
      box.append(banner);
    }
  }
  async function changeRun(result, action, fields, reload, current) {
    if (!Number.isInteger(result.revision) || result.revision < 1) { notice("최신 실행을 다시 열어 주세요."); return; }
    const key = session();
    try {
      await request(`/v1/crew/runs/${encodeURIComponent(result.run_id)}/${action}`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: result.revision, ...fields }) });
      if (key !== session() || !current()) return;
      notice(action === "stop" ? "중단을 요청했어요. 실제 종료 상태를 확인해 주세요." : "결정을 보냈어요. 실행 상태를 다시 확인해요.");
      reload();
    } catch { if (key === session() && current()) notice("보내지 못했어요. 상태가 바뀌었거나 권한이 없을 수 있어요. 다시 열어 확인해 주세요."); }
  }
  // 전달 상태: one line per place the result went: mark, channel, target, status.
  function delivery(box, value) {
    if (!value) return;
    section(box, "전달 상태", "결과를 보낸 곳과 성공 여부");
    const line = el("div", "flow-line");
    const ok = value.status === "delivered", bad = ["failed", "unknown"].includes(value.status);
    const origin = value.origin || {};
    const target = /^\d{5,25}$/.test(origin.guild_id || "") && /^\d{5,25}$/.test(origin.thread_id || "") ? el("a", "", "원래 대화 열기") : el("strong", "", "원래 대화");
    if (target.tagName === "A") { target.href = `https://discord.com/channels/${origin.guild_id}/${origin.thread_id}`; target.target = "_blank"; target.rel = "noopener noreferrer"; }
    const end = [DELIVERY[value.status] || value.status, value.error_code, ...(value.message_ids || [])].filter(Boolean).join(" · ");
    line.append(mark(ok, bad), el("small", "", "대화로 전달"), target, el("span", "grow"), raw(el("small", "", end)));
    const list = el("div", "flow-lines");
    list.append(line);
    box.append(list);
    if (value.status === "unknown") box.append(paragraph("보냈는지 확인되지 않아 자동으로 다시 보내지 않아요. 원래 대화와 저장된 메시지 영수증을 먼저 확인해 주세요."));
    if (value.status === "failed") box.append(paragraph("권한이나 대화 상태를 확인해 주세요. 전달을 복구하려고 작업을 다시 실행하지 마세요."));
  }
  async function openMeeting(id, container, back) {
    const { current, body } = detailShell(container, "회의 결과", back, () => void openMeeting(id, container, back));
    try {
      const rec = await request(`/v1/crew/meetings/${encodeURIComponent(id)}?${query()}`);
      if (!current() || rec.id !== id) return;
      const meeting = rec.crew_meeting || {}, conclusion = meeting.conclusion || {};
      body.replaceChildren(raw(paragraph(`${STATUS[meeting.status] || meeting.status} · ${id}`)), raw(el("pre", "flow-result-text", [conclusion.summary, ...(conclusion.decisions || []), ...(conclusion.todos || []), ...(conclusion.open_questions || [])].filter(Boolean).join("\n") || "아직 결론이 없어요.")), button("전체 회의 기록", "ghost", null, () => go("meetings")));
    } catch { if (current()) failure(body); }
  }
  // An agent's or team's own FLOW runs (called by @이름) for its 기록: each row opens its result in place.
  async function runs(crewId, container) {
    const current = view(container);
    container.replaceChildren(paragraph("불러오는 중…"));
    try {
      const page = await request(`/v1/crew/runs?${query({ crew_id: crewId, limit: 30 })}`);
      if (!current()) return;
      const items = (page.items || []).filter((rec) => rec.crew_run?.invocation_mode === "flow");
      container.replaceChildren(...items.map((rec) => row(`${STATUS[rec.crew_run.status] || rec.crew_run.status} · ${ago(rec.created_at)}`, rec.id, () => void openResult(rec.id, container, () => void runs(crewId, container)))));
      if (!items.length) container.append(paragraph("아직 @이름으로 부른 실행이 없어요."));
    } catch { if (current()) failure(container); }
  }
  return { openResult, runs, reset: () => { views.cancel(); } };
}

