// 설정 › 일반 › 알림 (Figma 198:5962 카드/알림): desktop notifications for memories waiting for review, finished
// workflow runs and a lost connection. app.js calls check() on its 10 s tick; the first check only learns what is
// already there, and nothing is sent while the window is in front (the screen shows it then).
window.ArkismAlerts = (() => {
  "use strict";
  const KEY = "arkism.alerts";
  const DEF = { review: true, done: true, error: true, sound: true };
  let prefs = { ...DEF };
  try { prefs = { ...DEF, ...JSON.parse(localStorage.getItem(KEY) || "{}") }; } catch (_) { /* defaults */ }
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(prefs)); } catch (_) { /* per-viewer convenience only */ } };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  let seen = null; // { review, runs: Set of finished run ids, error }
  let runsAt = 0;
  let busy = false;

  async function check({ state, request, query, tauri, away = () => document.hidden || !document.hasFocus() }) {
    if (!tauri || busy) return;
    busy = true;
    try {
      const review = state.candidateTotal ?? state.data?.candidates?.length ?? 0;
      const error = !!state.loadError;
      let runs = null;
      // Runs are read every 30 s, only while 작업 완료 알림 is on.
      if (prefs.done && Date.now() - runsAt > 30000) {
        runsAt = Date.now();
        runs = await request(`/v1/crew/runs?${query({ limit: 20 })}`).then((r) => r.items || [], () => null);
      }
      const finished = (runs || []).filter((x) => ["succeeded", "failed"].includes(x.crew_run?.status));
      if (!seen) { seen = { review, error, runs: new Set(finished.map((x) => x.id)) }; return; }
      const out = [];
      if (prefs.review && review > seen.review) out.push([tr("검토할 기억이 생겼어요"), tr(`승인을 기다리는 기억이 ${review}개 있어요.`)]);
      if (prefs.error && error && !seen.error) out.push([tr("Arkism 연결이 끊겼어요"), state.loadError.text || tr("연결을 확인해 주세요.")]);
      if (runs) {
        const name = (id) => (state.data?.crew || []).find((r) => r.id === id)?.crew?.name || tr("워크플로우");
        for (const x of finished) {
          if (seen.runs.has(x.id)) continue;
          seen.runs.add(x.id);
          if (prefs.done) out.push([x.crew_run.status === "succeeded" ? tr("작업이 끝났어요") : tr("작업이 실패했어요"), name(x.crew_run.crew_id)]);
        }
      }
      seen.review = review; seen.error = error;
      if (!away()) return;
      for (const [title, body] of out.slice(0, 3)) await tauri.invoke("notify", { title, body, sound: !!prefs.sound }).catch(() => {});
    } finally { busy = false; }
  }
  return { check, get: () => ({ ...prefs }), set: (patch) => { prefs = { ...prefs, ...patch }; save(); } };
})();
