// 설정 › 일반 › 알림 (Figma 198:5962 카드/알림): desktop notifications for memories waiting for review, finished
// workflow runs and a lost connection. app.js calls check() on its 10 s tick and the server is read every 30 s; the first
// read only learns what is already there, and nothing is sent while the window is in front (the screen shows it then).
window.ArkismAlerts = (() => {
  "use strict";
  const KEY = "arkism.alerts";
  const DEF = { review: true, done: true, error: true, sound: true };
  let prefs = { ...DEF };
  try { prefs = { ...DEF, ...JSON.parse(localStorage.getItem(KEY) || "{}") }; } catch (_) { /* defaults */ }
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(prefs)); } catch (_) { /* per-viewer convenience only */ } };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  let seen = null; // { owner, review, error (null = unknown), runs: Set of finished run ids, runsReady }
  let readAt = 0;
  let busy = false;

  async function check({ state, request, query, tauri, away = () => document.hidden || !document.hasFocus() }) {
    if (!tauri || busy || !state.token || Date.now() - readAt < 30000) return;
    busy = true;
    readAt = Date.now();
    const owner = JSON.stringify([state.server, state.token, state.scope]);
    if (seen && seen.owner !== owner) seen = null; // another server, token or scope: its first read is a new baseline
    try {
      // The window's own lists stop refreshing while it is hidden (visibleSync), so the review count and the
      // connection are read here. Runs are read only while 작업 완료 알림 is on.
      const [counted, error, runs] = await Promise.all([
        state.caps?.actions?.review_list ? request("/v1/memory_candidates/query", { method: "POST", body: JSON.stringify({ scope: state.scope, state: "candidate", limit: 1 }) })
          .then((r) => (Number.isSafeInteger(r?.total) ? r.total : null), () => null) : null,
        request("/readyz").then(() => false, (e) => (e?.code === "api_context_changed" ? null : true)),
        prefs.done ? request(`/v1/crew/runs?${query({ limit: 20 })}`).then((r) => r.items || [], () => null) : null
      ]);
      // An older server has no count query; a failed count read stays null so seen.review is kept.
      const review = state.caps?.actions?.review_list ? counted : (state.candidateTotal ?? state.data?.candidates?.length ?? null);
      const finished = (runs || []).filter((x) => ["succeeded", "failed"].includes(x.crew_run?.status));
      if (!seen) { seen = { owner, review, error, runs: new Set(finished.map((x) => x.id)), runsReady: !!runs }; return; }
      const out = [];
      if (prefs.review && review != null && seen.review != null && review > seen.review) out.push([tr("검토할 기억이 생겼어요"), tr(`승인을 기다리는 기억이 ${review}개 있어요.`)]);
      if (prefs.error && error && seen.error === false) out.push([tr("Arkism 연결이 끊겼어요"), state.loadError?.text || tr("연결을 확인해 주세요.")]);
      if (runs) {
        const name = (id) => (state.data?.crew || []).find((r) => r.id === id)?.crew?.name || tr("워크플로우");
        const fresh = seen.runsReady; // the first list read is a baseline: runs that finished before it are not news
        seen.runsReady = true;
        for (const x of finished) {
          if (seen.runs.has(x.id)) continue;
          seen.runs.add(x.id);
          if (fresh && prefs.done) out.push([x.crew_run.status === "succeeded" ? tr("작업이 끝났어요") : tr("작업이 실패했어요"), name(x.crew_run.crew_id)]);
        }
      }
      if (review != null) seen.review = review;
      if (error != null) seen.error = error;
      if (!away()) return;
      for (const [title, body] of out.slice(0, 3)) await tauri.invoke("notify", { title, body, sound: !!prefs.sound }).catch(() => {});
    } finally { busy = false; }
  }
  return { check, get: () => ({ ...prefs }), set: (patch) => {
    prefs = { ...prefs, ...patch };
    save();
    if (patch.done === false && seen) seen.runsReady = false; // runs are not read while off, so the next list is a baseline again
  } };
})();
