// Server-wide management filtering and ordering. The client retains only opened
// result pages; query/scope changes discard late replies and old cursors.
const paths = { memories: "/v1/memories/query", candidates: "/v1/memory_candidates/query", conflicted: "/v1/memory_candidates/query", skills: "/v1/skills/query", skillTrash: "/v1/skills/query" };
export function createCatalogQuery({ state, request, createPager, preferences, render }) {
  const entries = new Map(); let picked = null;
  const identity = () => JSON.stringify([state.server, state.scope, state.connection.generation]);
  function spec(name) {
    const base = { scope: state.scope, q: state.query || "" };
    if (["skills", "skillTrash"].includes(name)) return { ...base, state: name === "skillTrash" ? "trash" : state.skillTab, sort: state.skillSort, direction: state.skillDir || 0, model: state.skillModel, category: state.skillCat, categories: preferences().categories.map(({ id, rules }) => ({ id, rules })), agents: preferences().agents };
    return { ...base, state: name === "candidates" ? "candidate" : name === "conflicted" ? "conflicted" : ["idle", "provenance"].includes(state.memTab) ? state.memTab : "active", kind: state.kind === "all" ? "" : state.kind, repository: state.repo || "", sort: name === "candidates" ? "risk" : name === "conflicted" ? "recent" : state.sort, kept_ids: preferences().kept, collect_unused: preferences().collectUnused };
  }
  const key = name => JSON.stringify({ identity: identity(), body: spec(name) });
  function entry(name) {
    if (!entries.has(name)) {
      const value = {};
      value.pager = createPager(async (serialized, page) => {
        const { body } = JSON.parse(serialized);
        const result = await request(paths[name], { method: "POST", body: JSON.stringify({ ...body, limit: page.limit, ...(page.cursor ? { cursor: page.cursor } : {}) }) });
        if (!Number.isSafeInteger(result?.total) || result.total < 0 || !result.facets || typeof result.next_cursor !== "string") throw Error("catalog_page_invalid");
        const pageResult = { ...result, metadata: { total: result.total, facets: result.facets } };
        return name === "skillTrash" ? { ...pageResult, items: result.items.map(row => ({ ...row, status: "deprecated" })) } : pageResult;
      });
      entries.set(name, value);
    }
    return entries.get(name);
  }
  function snapshot(name) {
    const value = entry(name), serialized = key(name), snap = value.pager.snapshot();
    if (snap.key !== serialized) return { key: serialized, items: [], cursor: "", ready: false, loading: false, error: "", total: null, facets: {} };
    const meta = snap.metadata;
    return { ...snap, total: meta?.total ?? null, facets: meta?.facets || {} };
  }
  const currentName = () => state.view === "skills" ? state.skillTab === "trash" ? "skillTrash" : "skills" : ({ candidate: "candidates", conflicted: "conflicted" }[state.memTab] || "memories");
  const visible = () => state.view === "skills" && !state.skillPage || state.view === "memories" && ["active", "idle", "provenance", "candidate", "conflicted"].includes(state.memTab);
  async function read(name, refresh = false) {
    const value = entry(name), serialized = key(name);
    try { await (refresh ? value.pager.refresh(serialized) : snapshot(name).error ? value.pager.retry(serialized) : value.pager.load(serialized)); }
    catch (error) {
      // A changed snapshot invalidates an offset. Re-read the opened window
      // atomically, rather than repeatedly retrying the old offset forever.
      if (error.message === "revision_conflict") await value.pager.refresh(serialized);
      else throw error;
    }
    if (key(name) === serialized && visible() && currentName() === name) { if (name === "skills" || name === "skillTrash") state.versionsDone = false; render(false); }
  }
  function ensure(name = currentName()) {
    if (snapshot(name).ready || snapshot(name).loading || snapshot(name).error) return;
    const serialized = key(name);
    void read(name).catch(() => { if (key(name) === serialized && visible()) render(false); });
  }
  function record(id) {
    for (const [name, value] of [...entries].sort(([a],[b]) => Number(b === currentName()) - Number(a === currentName()))) {
      const snap = value.pager.snapshot();
      if (!snap.key || JSON.parse(snap.key).identity !== identity()) continue;
      const item = snap.items.find(row => (["skills", "skillTrash"].includes(name) ? `skill:${row.id}` : row.id) === id);
      if (item) return item;
    }
    return picked?.identity === identity() && picked.id === id ? picked.record : null;
  }
  return { snapshot, ensure, record, currentName, visible,
    more: name => read(name),
    refreshVisible: () => visible() ? read(currentName(), true) : Promise.resolve(),
    pin(id, item = record(id)) { if (item) picked = { id, record: item, identity: identity() }; }, // item: a record opened from elsewhere, maybe past the loaded pages
    reset() { for (const value of entries.values()) value.pager.reset(); entries.clear(); picked = null; }
  };
}
