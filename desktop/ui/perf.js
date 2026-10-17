// Background timing of the whole app: loading, refreshing and drawing each screen, the workflow canvas, and every
// "long task" (the browser busy for 50ms or more, which the user feels as a stall). Each sample is kept in this browser
// only, the last 500 per kind, and shows in 설정 → 고급 → 속도 기록 (or ArkismPerf.summary() in the console).
// shortcut: local only, no upload; send the summary to the server when it has to be compared across PCs.
window.ArkismPerf = (() => {
  const KEY = "arkism.perf";
  const KEEP = 500;
  let data = {};
  try { data = JSON.parse(localStorage.getItem(KEY) || "{}") || {}; } catch { data = {}; }
  let saveTimer = null;
  const flush = () => {
    clearTimeout(saveTimer); saveTimer = null;
    try { localStorage.setItem(KEY, JSON.stringify(data)); } catch { /* storage full or blocked: keep timing in memory */ }
  };
  const save = () => { saveTimer ||= setTimeout(flush, 2000); };
  // Closing the window within 2s of a sample would lose it.
  globalThis.addEventListener?.("pagehide", () => { if (saveTimer) flush(); });
  // note("canvas.edges", 3.2, 120): one sample, ms taken for work of the given size (nodes, edges, ...).
  function note(kind, ms, size = 0) {
    const list = (data[kind] ||= []);
    list.push([Math.round(ms * 10) / 10, size, Date.now()]);
    if (list.length > KEEP) list.splice(0, list.length - KEEP);
    save();
  }
  const pick = (sorted, q) => sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))];
  // Per kind: how many samples, median, 95th percentile, slowest, and the largest size seen.
  function summary() {
    const out = {};
    for (const [kind, list] of Object.entries(data)) {
      if (!list.length) continue;
      const ms = list.map((s) => s[0]).sort((a, b) => a - b);
      out[kind] = { n: ms.length, p50: pick(ms, 0.5), p95: pick(ms, 0.95), max: ms[ms.length - 1], size: Math.max(...list.map((s) => s[1])) };
    }
    return out;
  }
  // time("render.memories", () => draw(), rows.length): runs fn and notes how long it took; returns what fn returns.
  function time(kind, fn, size = 0) {
    const t0 = performance.now();
    try { return fn(); } finally { note(kind, performance.now() - t0, size); }
  }
  try {
    new PerformanceObserver((list) => list.getEntries().forEach((e) => note("longtask", e.duration))).observe({ type: "longtask", buffered: true });
  } catch { /* no long task timing in this webview */ }
  const clear = () => { data = {}; save(); };
  return { note, time, summary, clear };
})();
