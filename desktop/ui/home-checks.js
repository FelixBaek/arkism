// @ts-check
// 홈 확인 줄 · 공통 상태 화면 (Figma 385:30743 · 523:35767 · 198:3554).
// - stalledItem: 기억 처리 멈춤 경고. 판정은 overview.js stages()의 stalled 와 같은 식(대화는 계속 끝나는데 6시간 넘게 새 후보가 없음).
// - knowledgeRow: 메뉴별 확인할 것의 "지식" 줄 (위키 초안 · 출처 바뀜 · 검토 대기 자료).
// - ArkismState: 빈 화면 · 오류 · 권한 공통 부품 (panel · of).
window.ArkismState = (() => {
  "use strict";
  const el = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; };
  // actions: [[label, fn, "ghost"?]]; note: amber one-liner under the text
  function panel({ title, text, note, actions = [] }) {
    const box = el("section", "st-panel");
    box.append(el("strong", "", title));
    if (text) box.append(el("p", "", text));
    if (note) box.append(el("p", "st-note", note));
    if (actions.length) {
      const row = el("div", "st-actions");
      for (const [label, fn, kind] of actions) { const b = el("button", kind === "ghost" ? "btn ghost" : "btn", label); b.type = "button"; b.addEventListener("click", fn); row.append(b); }
      box.append(row);
    }
    return box;
  }
  // 198:3554 states in the design's words. of(kind, on, over): an action shows only when `on` has its handler;
  // over replaces title/text/note, and over.slow adds the kind's slow note (AI 대기).
  const KINDS = {
    noResults: { title: "검색 결과 없음", text: "조건에 맞는 항목이 없어요.", actions: [["검색 지우기", "clear"], ["필터 초기화", "reset", "ghost"]] },
    loadFailed: { title: "불러오지 못했어요", note: "연결을 확인하고 다시 시도해 주세요.", actions: [["다시 시도", "retry"]] },
    changed: { title: "다른 곳에서 내용이 바뀌었어요", text: "작성 중인 내용은 유지했어요. 최신 내용을 확인해 주세요.", actions: [["최신 내용 보기", "latest"], ["입력 복사", "copy", "ghost"]] },
    desktopOnly: { title: "데스크톱에서 사용하세요", text: "Git 작업과 비교 실행은 Arkism 데스크톱 앱에서 열 수 있어요." },
    serverUpdate: { title: "서버 업데이트가 필요해요", text: "이 서버는 아직 이 기능을 지원하지 않아요.", actions: [["다시 확인", "recheck"]] },
    aiWait: { title: "AI 도움을 기다리는 중", text: "요청을 처리하고 있어요.", slow: "너무 오래 걸려요. 잠시 후 다시 시도해 주세요." },
    denied: { title: "관리 계정이 필요해요", text: "이 작업은 관리 계정에서 사용할 수 있어요.", actions: [["계정 확인", "account"]] },
  };
  function of(kind, on = {}, over = {}) {
    const k = KINDS[kind];
    return panel({ title: over.title || k.title, text: over.text || k.text, note: over.note || (over.slow ? k.slow : k.note),
      actions: (k.actions || []).filter(([, key]) => on[key]).map(([label, key, cls]) => [label, on[key], cls]) });
  }
  // A stall's length in words: hours up to 48, then whole days (#190).
  const stallFor = (hours) => {
    if (hours < 48) return { span: `${hours}시간`, stopped: `${hours}시간째 멈춤`, sentence: `기억 추출이 ${hours}시간째 멈췄어요` };
    const days = Math.floor(hours / 24);
    return { span: `${days}일`, stopped: `${days}일째 멈춤`, sentence: `기억 추출이 ${days}일째 멈췄어요` };
  };
  return { panel, of, stallFor };
})();

window.ArkismHomeChecks = (ctx) => {
  "use strict";
  const { state, request, query, ago, render } = ctx;
  const HOUR = 3600000;
  const time = (v) => { const t = new Date(v || 0).getTime(); return Number.isNaN(t) ? 0 : t; };
  const live = () => state.view === "todo" && !state.selected && !state.todoAll;

  // ---- 기억 처리 멈춤 (same rule as overview.js stages())
  // Refetched at most once a minute: the home only re-renders on data changes, so a timer keeps the warning current.
  let S = { g: -1, sessions: null, at: 0 }, timer = 0;
  function load(generation, force = false) {
    if (!force && S.g === generation && Date.now() - S.at < 60000) return;
    if (S.g !== generation) S = { g: generation, sessions: null, at: 0 };
    S.at = Date.now();
    const g = generation;
    const got = (items) => { if (S.g !== g) return; S.sessions = items; if (live()) render(false); };
    request(`/v1/usage/sessions?${query({ range: "7d", limit: 300 })}`).then((d) => got(Array.isArray(d?.items) ? d.items : []), () => got([]));
  }
  function stall(sessions, lastExtract, now = Date.now()) {
    const ends = sessions.map((s) => time(s.ended_at || s.started_at)).filter(Boolean);
    const lastSession = Math.max(0, ...ends);
    const pending = lastExtract ? ends.filter((t) => t > lastExtract) : ends;
    const since = pending.length ? Math.min(...pending) : lastExtract;
    const stalled = ends.length > 0 && since > 0 && lastSession - since > 6 * HOUR && now - since > 6 * HOUR;
    return { stalled, hours: Math.floor((now - since) / HOUR), lastExtract, pending: pending.length };
  }
  function stalledItem(generation) {
    timer ||= setInterval(() => { if (live() && S.g >= 0) load(S.g, true); }, 60000); // forced: a tick just short of 60s after the last fetch must not skip a whole minute
    load(generation);
    // a list that did not load cannot prove a stall (#187)
    if (!S.sessions || state.loadError || state.listErrors?.candidates || state.listErrors?.conflicted) return null;
    const d = state.data;
    let lastExtract = 0;
    for (const list of [d.candidates, d.conflicted, d.memories]) for (const m of list) lastExtract = Math.max(lastExtract, time(m.created_at));
    const r = stall(S.sessions, lastExtract);
    if (!r.stalled) return null;
    const words = window.ArkismState.stallFor(r.hours);
    return { id: "stalled", n: 1, icon: "warning-circle", label: "기억 처리 멈춤", title: words.sentence, warn: true,
      sub: `${r.lastExtract ? `마지막 추출 ${ago(r.lastExtract)}` : "추출 기록 없음"} · 멈춘 뒤 끝난 대화 ${r.pending}건`, action: "처리 흐름 보기",
      go: ["overview"], preview: [words.stopped] };
  }

  // ---- 지식 (위키 · 자료)
  const SOURCE_LIMIT = 500;
  let K = { g: -1, pages: undefined, sources: undefined };
  function knowledgeRow(generation) {
    if (K.g !== generation) {
      K = { g: generation, pages: undefined, sources: undefined };
      const g = generation, get = (p, extra) => request(`${p}?${query(extra)}`).then((r) => (Array.isArray(r?.items) ? r.items : null), () => null);
      Promise.all([get("/v1/wiki/pages", { state: "all" }), get("/v1/knowledge/sources", { limit: SOURCE_LIMIT })]).then(([pages, sources]) => {
        if (K.g !== g) return; K.pages = pages; K.sources = sources; if (live()) render(false);
      }, () => {});
    }
    const pages = (K.pages || []).filter((p) => p.review_state !== "deleted");
    const drafts = pages.filter((p) => p.review_state === "draft").length;
    const stale = pages.filter((p) => p.review_state !== "draft" && p.stale_sources?.length).length;
    // the source list has no cursor or total: a full page of SOURCE_LIMIT may have more behind it, so its count reads "N+" (#465)
    const wait = ctx.count((K.sources || []).filter((s) => s.lifecycle_status === "draft" || (s.lifecycle_status !== "failed" && s.trust_level !== "reviewed")), { more: (K.sources || []).length >= SOURCE_LIMIT });
    const parts = [{ label: `위키 초안 ${drafts}`, n: drafts }, { label: `출처 바뀜 ${stale}`, n: stale }, { label: `검토 대기 자료 ${wait.text}`, n: wait.min }].filter((p) => p.n);
    return ["TOOLS", "지식", parts, ["knowledge"]];
  }
  return { stalledItem, knowledgeRow };
};
