import { createFlowDeliveryRecovery } from "./flowDeliveryRecovery.mjs";

const STATUS = { running: "실행 중", waiting: "확인 대기", succeeded: "완료", failed: "실패", stopped: "멈춤", partial: "부분 완료", concluded: "완료", no_conclusion: "결론 없음", pending: "대기 중", stopping: "중단 요청됨" };
const DELIVERY = { waiting_run: "결과를 기다리는 중", pending: "전달 대기", sending: "전달 중", retry_wait: "전달 재시도 대기", unknown: "전달 여부 확인 필요", failed: "전달 실패", delivered: "전달됨" };

export function flowNeedsDecision(waiting) {
  return waiting?.kind === "approval" || waiting?.kind === "budget" || waiting?.kind === "child" && ["approval", "budget"].includes(waiting.decision_kind);
}

// All result bodies are rendered as text. Every async response is confined to
// the initiating page and connection; no private result survives a scope switch.
export function createFlowHistory({ state, el, button, request, query, notice, ago, go }) {
  const recovery = createFlowDeliveryRecovery({ state, el, button, request, notice });
  const views = globalThis.ArkismKeep.latest();
  const session = () => JSON.stringify([state.server, state.scope, state.token, state.connection?.generation]);
  const raw = (node) => { node.setAttribute("data-no-i18n", ""); return node; };
  const paragraph = (text) => el("p", "flow-history-note", text);
  function view(container) {
    const turn = views.begin(), key = session();
    return () => turn.live() && key === session() && container.isConnected;
  }
  function heading(box, title, refresh) {
    const row = el("div", "flow-history-head");
    row.append(el("h2", "", title), button("새로고침", "ghost", "arrow-clockwise", refresh));
    box.append(row);
  }
  function row(title, detail, action) {
    const pick = button("", "flow-history-row", null, action);
    pick.append(raw(el("strong", "", title)), raw(el("small", "", detail)));
    return pick;
  }
  function failure(box) {
    box.replaceChildren(paragraph("불러오지 못했어요. 연결과 접근 권한을 확인한 뒤 새로고침해 주세요."));
  }
  function detailShell(container, title, back, refresh) {
    const current = view(container);
    container.replaceChildren();
    const backButton = button("목록으로", "ghost", "arrow-left", back);
    container.append(backButton);
    heading(container, title, refresh);
    const body = el("section", "flow-history-detail");
    body.setAttribute("aria-live", "polite");
    body.append(paragraph("불러오는 중…"));
    container.append(body);
    backButton.focus();
    return { current, body };
  }
  // back: where 목록으로 returns (the workflow's 기록 list); child runs and meetings come back to this result
  async function openResult(id, container, back) {
    const again = () => void openResult(id, container, back);
    const { current, body } = detailShell(container, "실행 결과", back, again);
    try {
      const result = await request(`/v1/crew/runs/${encodeURIComponent(id)}/result?${query()}`);
      if (!current() || result.run_id !== id) return;
      body.replaceChildren(raw(paragraph(`${STATUS[result.status] || result.status} · ${id}`)));
      resultActions(body, result, again, current);
      const text = el("pre", "flow-result-text", result.result || "아직 최종 결과가 없어요.");
      body.append(raw(text));
      delivery(body, result.delivery?.delivery);
      recovery.render(body, result.delivery, again, current);
      const children = Object.values(result.children || {});
      if (children.length) body.append(el("h3", "", "함께 실행한 작업"));
      for (const child of children) body.append(row(child.kind === "meeting" ? "회의 결과" : "하위 실행", child.id, () => child.kind === "meeting" ? void openMeeting(child.id, container, again) : void openResult(child.id, container, again)));
    } catch { if (current()) failure(body); }
  }
  function resultActions(box, result, reload, current) {
    const actions = el("div", "flow-history-actions");
    const waiting = result.waiting;
    if (result.status === "waiting" && flowNeedsDecision(waiting)) {
      box.append(el("h3", "", "사람의 결정이 필요해요"), raw(paragraph(waiting.question || "다음 단계로 갈까요?")));
      actions.append(button("거절", "ghost", null, () => current() && void changeRun(result, "decision", { node_id: waiting.node_id, approve: false }, reload, current)), button("승인", "primary", null, () => current() && void changeRun(result, "decision", { node_id: waiting.node_id, approve: true }, reload, current)));
    }
    if (["running", "waiting"].includes(result.status) && result.invocation_mode === "flow") {
      const stop = button(result.cancel_requested ? "중단 요청됨" : "중단 요청", "ghost", "stop-circle", () => current() && void changeRun(result, "stop", {}, reload, current));
      stop.disabled = !!result.cancel_requested;
      actions.append(stop);
    }
    if (actions.childElementCount) box.append(actions);
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
  function delivery(box, value) {
    if (!value) return;
    box.append(el("h3", "", "대화로 전달"), paragraph(DELIVERY[value.status] || value.status));
    const origin = value.origin || {};
    if (/^\d{5,25}$/.test(origin.guild_id || "") && /^\d{5,25}$/.test(origin.thread_id || "")) {
      const link = el("a", "", "원래 대화 열기");
      link.href = `https://discord.com/channels/${origin.guild_id}/${origin.thread_id}`;
      link.target = "_blank"; link.rel = "noopener noreferrer"; box.append(link);
    }
    if (value.status === "unknown") box.append(paragraph("보냈는지 확인되지 않아 자동으로 다시 보내지 않아요. 원래 대화와 저장된 메시지 영수증을 먼저 확인해 주세요."));
    if (value.status === "failed") box.append(paragraph("권한이나 대화 상태를 확인해 주세요. 전달을 복구하려고 작업을 다시 실행하지 마세요."));
    if (value.status === "delivered") box.append(paragraph("서비스가 메시지 접수를 확인했어요. 사람이 읽었다는 뜻은 아니에요."));
    if (value.message_ids?.length) box.append(raw(paragraph(`확인한 메시지: ${value.message_ids.join(", ")}`)));
    if (value.error_code) box.append(raw(paragraph(value.error_code)));
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

