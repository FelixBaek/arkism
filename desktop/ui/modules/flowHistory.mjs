import { createFlowDeliveryRecovery } from "./flowDeliveryRecovery.mjs";

const STATUS = { running: "실행 중", waiting: "확인 대기", succeeded: "완료", failed: "실패", stopped: "멈춤", partial: "부분 완료", concluded: "완료", no_conclusion: "결론 없음", pending: "대기 중", stopping: "중단 요청됨" };
const STAGES = { evidence_ready: "개선 근거 준비", comparison_required: "독립 비교 필요", approval_required: "사람 승인 필요", trial_ready: "제한 시험 준비", trial_running: "제한 시험 중", trial_comparison_required: "시험 결과 비교 필요", apply_ready: "적용 검토 필요", monitoring: "적용 후 확인 중", recovery_unavailable: "복구 확인 필요", comparison_failed: "비교에서 실패", trial_failed: "시험에서 실패", rolled_back: "이전 버전으로 복구됨" };
const NEXT = { evidence_ready: "저장한 근거와 비교 계획으로 개선 후보를 생성해요.", comparison_required: "독립 평가자가 기존 버전과 비교한 결과가 필요해요.", approval_required: "비교 결과를 검토한 뒤 사람이 시험을 승인해요.", trial_ready: "허용된 실행 환경과 기한으로 제한 시험을 시작해요.", trial_running: "정책이 허용한 서비스가 실제 시험 결과를 수집해요.", trial_comparison_required: "실제 시험 근거를 독립적으로 비교해요.", apply_ready: "적용 권한과 평가 근거를 다시 확인해요.", monitoring: "다음 실행의 효과를 확인하고 필요하면 복구해요.", recovery_unavailable: "복구할 버전과 현재 권한을 확인해요." };
const DELIVERY = { waiting_run: "결과를 기다리는 중", pending: "전달 대기", sending: "전달 중", retry_wait: "전달 재시도 대기", unknown: "전달 여부 확인 필요", failed: "전달 실패", delivered: "전달됨" };

export function flowNeedsDecision(waiting) {
  return waiting?.kind === "approval" || waiting?.kind === "budget" || waiting?.kind === "child" && ["approval", "budget"].includes(waiting.decision_kind);
}

// All result bodies are rendered as text. Every async response is confined to
// the initiating page and connection; no private result survives a scope switch.
export function createFlowHistory({ state, el, button, request, query, notice, byId, ago, go }) {
  const recovery = createFlowDeliveryRecovery({ state, el, button, request, notice });
  let generation = 0;
  const session = () => JSON.stringify([state.server, state.scope, state.token, state.connection?.generation]);
  const raw = (node) => { node.setAttribute("data-no-i18n", ""); return node; };
  const paragraph = (text) => el("p", "flow-history-note", text);
  function view(container) {
    const epoch = ++generation, key = session();
    return () => epoch === generation && key === session() && state.view === "crew" && container.isConnected;
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
  async function render(kind, container) {
    const current = view(container);
    container.replaceChildren();
    container.className = "wf-main flow-history";
    heading(container, kind === "results" ? "결과함" : "개선 흐름", () => void render(kind, container));
    container.append(paragraph(kind === "results" ? "최근 200개 실행을 확인해요. 실행 완료와 대화로의 전달 상태는 따로 표시해요." : "실행 근거에서 시작한 스킬 개선을 확인해요. 표시된 다음 단계는 실행 권한을 뜻하지 않아요."));
    const list = el("div", "flow-history-list");
    list.setAttribute("aria-live", "polite");
    list.append(paragraph("불러오는 중…"));
    container.append(list);
    const path = kind === "results" ? "/v1/crew/runs" : "/v1/flow/improvements";
    const load = async (after = "") => {
      try {
        const page = await request(`${path}?${query({ limit: kind === "results" ? 200 : 50, ...(after ? { after } : {}) })}`);
        if (!current()) return;
        if (!after) list.replaceChildren();
        for (const rec of page.items || []) {
        if (kind === "results") {
          const run = rec.crew_run || {}, name = byId(run.crew_id)?.crew.name || "워크플로우";
          list.append(row(name, `${STATUS[run.status] || run.status} · ${ago(rec.created_at)} · ${rec.id}`, () => void openResult(rec.id, container)));
        } else {
          const improvement = rec.flow_improvement || {};
          list.append(row(rec.skill_id || "스킬 개선", `${STAGES[improvement.stage] || improvement.stage} · ${rec.id}`, () => void openImprovement(rec.id, container)));
        }
        }
        if (!(page.items || []).length && !after && !page.next_cursor) list.append(paragraph(kind === "results" ? "아직 실행 결과가 없어요." : "아직 연결된 개선 흐름이 없어요."));
        if (page.next_cursor) {
          const more = button("더 보기", "ghost", null, async () => { more.disabled = true; await load(page.next_cursor); more.remove(); });
          list.append(more);
        }
      } catch { if (current()) failure(list); }
    };
    await load();
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
  async function openResult(id, container) {
    const { current, body } = detailShell(container, "실행 결과", () => void render("results", container), () => void openResult(id, container));
    try {
      const result = await request(`/v1/crew/runs/${encodeURIComponent(id)}/result?${query()}`);
      if (!current() || result.run_id !== id) return;
      body.replaceChildren(raw(paragraph(`${STATUS[result.status] || result.status} · ${id}`)));
      resultActions(body, result, () => void openResult(id, container), current);
      const text = el("pre", "flow-result-text", result.result || "아직 최종 결과가 없어요.");
      body.append(raw(text));
      delivery(body, result.delivery?.delivery);
      recovery.render(body, result.delivery, () => void openResult(id, container), current);
      const children = Object.values(result.children || {});
      if (children.length) body.append(el("h3", "", "함께 실행한 작업"));
      for (const child of children) body.append(row(child.kind === "meeting" ? "회의 결과" : "하위 실행", child.id, () => child.kind === "meeting" ? void openMeeting(child.id, container, id) : void openResult(child.id, container)));
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
  async function openMeeting(id, container, parent) {
    const { current, body } = detailShell(container, "회의 결과", () => void openResult(parent, container), () => void openMeeting(id, container, parent));
    try {
      const rec = await request(`/v1/crew/meetings/${encodeURIComponent(id)}?${query()}`);
      if (!current() || rec.id !== id) return;
      const meeting = rec.crew_meeting || {}, conclusion = meeting.conclusion || {};
      body.replaceChildren(raw(paragraph(`${STATUS[meeting.status] || meeting.status} · ${id}`)), raw(el("pre", "flow-result-text", [conclusion.summary, ...(conclusion.decisions || []), ...(conclusion.todos || []), ...(conclusion.open_questions || [])].filter(Boolean).join("\n") || "아직 결론이 없어요.")), button("전체 회의 기록", "ghost", null, () => go("meetings")));
    } catch { if (current()) failure(body); }
  }
  async function openImprovement(id, container) {
    const { current, body } = detailShell(container, "개선 흐름", () => void render("improvements", container), () => void openImprovement(id, container));
    try {
      const rec = await request(`/v1/flow/improvements/${encodeURIComponent(id)}?${query()}`);
      if (!current() || rec.id !== id) return;
      const value = rec.improvement || {};
      body.replaceChildren(paragraph(STAGES[value.stage] || value.stage), raw(paragraph(id)));
      if (NEXT[value.stage]) body.append(el("h3", "", "다음에 필요한 작업"), paragraph(NEXT[value.stage]));
      if (value.pending) body.append(paragraph("처리 중인 작업의 결과를 확인해야 해요. 확인 없이 같은 작업을 다시 시작하지 마세요."), raw(paragraph(`${value.pending.action} · ${value.pending.state}`)));
      body.append(paragraph("후보 생성·독립 비교·시험·적용에는 각각 필요한 근거와 권한이 있어야 해요. 이 화면을 여는 것만으로 승인하거나 적용하지 않아요."));
      for (const [key, label] of [["source_run", "근거 실행"], ["candidate", "개선 후보"], ["comparison", "독립 비교"], ["trial", "제한 시험"], ["trial_comparison", "시험 비교"]]) {
        const ref = value[key];
        if (!ref?.id) continue;
        if (key === "source_run") body.append(row(label, `${ref.id} · v${ref.revision}`, () => void openResult(ref.id, container)));
        else body.append(el("h3", "", label), raw(paragraph(`${ref.id} · v${ref.revision}`)));
      }
      if (value.receipts?.length) {
        body.append(el("h3", "", "처리 기록"));
        for (const receipt of value.receipts) body.append(raw(paragraph([receipt.action, receipt.state, receipt.error_code].filter(Boolean).join(" · "))));
      }
    } catch { if (current()) failure(body); }
  }
  return { render, openResult, reset: () => { generation++; } };
}

