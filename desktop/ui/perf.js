// Background timing of heavy screens (the workflow canvas first). Each sample is kept in this browser only, the last
// 500 per kind, so a slow frame can be found later with ArkismPerf.summary() in the console.
// shortcut: local only, no upload; send the summary to the server when it has to be compared across PCs.
window.ArkismPerf = (() => {
  const KEY = "arkism.perf";
  const KEEP = 500;
  let data = {};
  try { data = JSON.parse(localStorage.getItem(KEY) || "{}") || {}; } catch { data = {}; }
  let saveTimer = null;
  const save = () => {
    saveTimer ||= setTimeout(() => {
      saveTimer = null;
      try { localStorage.setItem(KEY, JSON.stringify(data)); } catch { /* storage full or blocked: keep timing in memory */ }
    }, 2000);
  };
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
  const clear = () => { data = {}; save(); };
  return { note, summary, clear };
})();
