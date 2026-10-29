// Server cursors for the other management lists. Never walk unopened pages.
const COLLECTIONS = {
  candidates: ["/v1/memory_candidates", { status: "candidate" }],
  conflicted: ["/v1/memory_candidates", { status: "conflicted" }],
  skills: ["/v1/skills", {}],
  skillTrash: ["/v1/skills/trash", {}]
};

// A count read from the server, so a failed or partial read never shows as 0 or as the whole (#465).
// rows: the loaded rows or their number, undefined while the read runs, null when it failed; more: further pages wait;
// total: the server's own count, which wins whenever it is a number. n is exact or null; min is what is known to be there.
/** @param {unknown[] | number | null | undefined} rows @param {{ more?: boolean, total?: number | null }} [opts] */
export function serverCount(rows, { more = false, total = null } = {}) {
  if (Number.isSafeInteger(total)) return { state: "loaded", n: total, min: total, text: String(total) };
  if (rows === undefined) return { state: "loading", n: null, min: 0, text: "…" };
  if (rows === null) return { state: "failed", n: null, min: 0, text: "—" };
  const k = typeof rows === "number" ? rows : rows.length;
  return more ? { state: "partial", n: null, min: k, text: `${k}+` } : { state: "loaded", n: k, min: k, text: String(k) };
}

export function createCollectionPaging({ state, request, createPager }) {
  const identity = () => JSON.stringify([state.server, state.scope, state.connection.generation]);
  const pagers = new Map(Object.entries(COLLECTIONS).map(([name, [path, extra]]) => [name, createPager(async (key, page) => {
    const [, scope] = JSON.parse(key);
    const result = await request(`${path}?${new URLSearchParams({ ...scope, ...extra, limit: page.limit, ...(page.cursor ? { cursor: page.cursor } : {}) })}`);
    return name === "skillTrash" ? { ...result, items: result.items?.map(row => ({ ...row, status: "deprecated" })) } : result;
  })]));
  const snapshot = name => {
    const pager = pagers.get(name), snap = pager.snapshot();
    return snap.key === identity() ? snap : { items: [], cursor: "", ready: false, loading: false, error: "" };
  };
  async function read(name, refresh = false) {
    const pager = pagers.get(name), key = identity();
    const items = await (refresh ? pager.refresh(key) : pager.load(key));
    if (key !== identity()) throw new Error("api_context_changed");
    return items;
  }
  async function more(name) {
    const pager = pagers.get(name), key = identity();
    const items = await (snapshot(name).error ? pager.retry(key) : pager.load(key));
    if (key === identity()) {
      state.data[name] = items;
      if (name === "skills") state.versionsDone = false;
    }
    return items;
  }
  return { snapshot, read, more, reset() { for (const pager of pagers.values()) pager.reset(); } };
}

// One footer for local display limits and remote cursors, even when filters hide
// every loaded row. Focusing its replacement keeps Enter/retry usable.
export function collectionFooter({ document, name, snapshot, total, shown, reveal, fetch, render, isCurrent }) {
  const snap = snapshot();
  if (shown >= total && !snap.cursor && !snap.error && !snap.loading) return null;
  const foot = document.createElement("div"); foot.className = "mem-more collection-more"; foot.dataset.collection = name;
  const status = document.createElement("span"); status.setAttribute("role", "status");
  status.textContent = snap.cursor ? `불러온 ${total}개 중 ${shown}개 표시 · 다음 목록 있음` : `${total}개 중 ${shown}개 표시`;
  const button = document.createElement("button"); button.className = "link-btn"; button.type = "button";
  button.textContent = snap.error ? "다시 불러오기" : snap.loading ? "불러오는 중…" : "더 보기";
  button.disabled = snap.loading;
  button.addEventListener("click", async () => {
    const focused = document.activeElement === button;
    if (shown < total) { reveal(); render(); }
    else {
      const pending = fetch(); render();
      try { await pending; } catch { /* the pager retains rows and exposes retry */ }
      if (!isCurrent()) return;
      render();
    }
    if (focused && isCurrent() && (document.activeElement === document.body || document.activeElement?.closest(".collection-more"))) {
      const next = document.querySelector(`[data-collection="${name}"] button`) || document.querySelector("#table [data-id]:last-of-type") || document.querySelector("#search");
      next?.focus({ preventScroll: true });
    }
  });
  foot.append(status, " · ", button);
  return foot;
}
