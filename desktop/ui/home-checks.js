// 홈 확인 줄 · 공통 상태 화면 (Figma 385:30743 · 523:35767 · 198:3554).
// - stalledItem: 기억 처리 멈춤 경고. 판정은 overview.js stages()의 stalled 와 같은 식(대화는 계속 끝나는데 6시간 넘게 새 후보가 없음).
// - knowledgeRow: 메뉴별 확인할 것의 "지식" 줄 (위키 초안 · 출처 바뀜 · 검토 대기 자료).
// - ArkismState: 빈 화면 · 오류 · 권한 공통 부품 (panel · denied).
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
      for (const [label, fn, kind] of actions) { const b = el("button", kind === "ghost" ? "ghost" : "", label); b.type = "button"; b.addEventListener("click", fn); row.append(b); }
      box.append(row);
    }
    return box;
  }
  // The server's error codes that need a different account or server, in the design's words.
  const DENIED = { access_denied: ["관리 계정이 필요해요", "계정 확인"] };
  const denied = (code) => DENIED[code] || null;
  return { panel, denied };
})();

window.ArkismHomeChecks = (ctx) => {
  "use strict";
  const { state, request, query, go, ago, render } = ctx;
  const HOUR = 3600000;
  const time = (v) => { const t = new Date(v || 0).getTime(); return Number.isNaN(t) ? 0 : t; };
  const live = () => state.view === "todo" && !state.selected && !state.todoAll;

  // ---- 기억 처리 멈춤 (same rule as overview.js stages())
  let S = { g: -1, sessions: null };
  function load(generation) {
    if (S.g === generation) return;
    S = { g: generation, sessions: null };
    const g = generation;
    request(`/v1/usage/sessions?${query({ range: "7d", limit: 300 })}`).then((d) => (Array.isArray(d?.items) ? d.items : []), () => [])
      .then((items) => { if (S.g !== g) return; S.sessions = items; if (live()) render(false); });
  }
  function stall(sessions, extracted, now = Date.now()) {
    const ends = sessions.map((s) => time(s.ended_at || s.started_at)).filter(Boolean);
    const lastSession = Math.max(0, ...ends), lastExtract = Math.max(0, ...extracted);
    const pending = lastExtract ? ends.filter((t) => t > lastExtract) : ends;
    const since = pending.length ? Math.min(...pending) : lastExtract;
    const stalled = ends.length > 0 && since > 0 && lastSession - since > 6 * HOUR && now - since > 6 * HOUR;
    return { stalled, hours: Math.floor((now - since) / HOUR), lastExtract, pending: pending.length };
  }
  function stalledItem(generation) {
    load(generation);
    if (!S.sessions) return null;
    const d = state.data;
    const extracted = [...d.candidates, ...d.conflicted, ...d.memories].map((m) => time(m.created_at)).filter(Boolean);
    const r = stall(S.sessions, extracted);
    if (!r.stalled) return null;
    return { id: "stalled", n: 1, icon: "warning-circle", label: "기억 처리 멈춤", title: `기억 추출이 ${r.hours}시간째 멈췄어요`, warn: true,
      sub: `${r.lastExtract ? `마지막 추출 ${ago(r.lastExtract)}` : "추출 기록 없음"} · 멈춘 뒤 끝난 대화 ${r.pending}건`, action: "처리 흐름 보기",
      go: ["overview"], preview: [`${r.hours}시간째 멈춤`] };
  }

  // ---- 지식 (위키 · 자료)
  let K = { g: -1, pages: undefined, sources: undefined };
  function knowledgeRow(generation) {
    if (K.g !== generation) {
      K = { g: generation, pages: undefined, sources: undefined };
      const g = generation, get = (p, extra) => request(`${p}?${query(extra)}`).then((r) => (Array.isArray(r?.items) ? r.items : null), () => null);
      Promise.all([get("/v1/wiki/pages", { state: "all" }), get("/v1/knowledge/sources", { limit: 500 })]).then(([pages, sources]) => {
        if (K.g !== g) return; K.pages = pages; K.sources = sources; if (live()) render(false);
      });
    }
    const pages = (K.pages || []).filter((p) => p.review_state !== "deleted");
    const drafts = pages.filter((p) => p.review_state === "draft").length;
    const stale = pages.filter((p) => p.review_state !== "draft" && p.stale_sources?.length).length;
    const wait = (K.sources || []).filter((s) => s.lifecycle_status === "draft" || (s.lifecycle_status !== "failed" && s.trust_level !== "reviewed")).length;
    const parts = [{ label: `위키 초안 ${drafts}`, n: drafts }, { label: `출처 바뀜 ${stale}`, n: stale }, { label: `검토 대기 자료 ${wait}`, n: wait }].filter((p) => p.n);
    return ["TOOLS", "지식", parts, ["knowledge"]];
  }
  return { stalledItem, knowledgeRow };
};
