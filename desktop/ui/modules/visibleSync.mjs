// Read planning, publication and scheduling have separate responsibilities. A
// timer refreshes only the pages already opened, and never overwrites newer data.
export function createVisibleSync(deps) {
  const { state, memoryPaging, collectionPaging } = deps;
  let syncing = false, again = false;
  const pageKey = () => [state.view, state.memTab, state.skillTab, state.settingsTab].join("|");
  const blocked = () => !state.token || state.loading || document.hidden || state.pending.size > 0 ||
    !!document.querySelector("dialog[open], #detail [data-sync-dirty]") || (document.activeElement?.id !== "search" && !!document.activeElement?.matches("input, textarea, select, [contenteditable=true]"));
  const eligible = () => ["todo", "memories", "skills", "tools", "crew", "images", "design", "routing", "settings"].includes(state.view) &&
    !(state.view === "memories" && ["originals", "past"].includes(state.memTab)) && !(state.view === "settings" && state.settingsTab !== "models");
  const current = ticket => ticket.generation === state.connection.generation && ticket.page === pageKey() && ticket.data === state.data && !blocked();

  async function refreshMemories() {
    if (Date.now() - memoryPaging.lastMemoryUsage > 10 * 60000) void deps.loadUsage(state.connection.generation);
    if (memoryPaging.memoryPages.snapshot().loading) return state.data.memories;
    return memoryPaging.memoryPages.refresh(memoryPaging.memoryKey());
  }
  function plan(view, data) {
    const reads = [], add = (key, promise) => reads.push({ key, old: data[key], promise });
    const items = (path, extra = {}) => deps.request(`${path}?${deps.query(extra)}`).then(result => result.items || []);
    if (view === "settings") add("providers", deps.request(`/v1/providers?${deps.query()}`).then(result => result.providers));
    if (["todo", "memories"].includes(view)) {
      add("memories", refreshMemories());
      if (state.caps?.actions?.review_list) {
        for (const key of ["candidates", "conflicted"]) add(key, collectionPaging.read(key, true));
        add("maintenance", items("/v1/maintenance_proposals", { status: "open" }));
      }
    }
    if (["todo", "skills"].includes(view)) {
      for (const key of ["skills", "skillTrash"]) add(key, collectionPaging.read(key, true));
      add("skillUse", items("/v1/skills/usage", { days: 60 }).then(rows => new Map(rows.map(row => [row.skill_id, row]))));
    }
    if (["todo", "skills", "tools"].includes(view)) add("tools", deps.loadTools());
    const lists = { crew: ["crew", "/v1/crew"], images: ["prompts", "/v1/prompts"], design: ["designSystems", "/v1/design_systems"] };
    if (lists[view]) { const [key, path] = lists[view]; add(key, items(path)); }
    return reads;
  }
  function special(view) {
    if (view === "routing") return deps.routing.refresh();
    if (view !== "memories") return null;
    const panel = deps.panels()[state.memTab];
    if (panel) return panel.refresh();
    if (["digests", "merge"].includes(state.memTab)) deps.digests.refresh();
    return null;
  }
  async function refreshResults(ticket) {
    if (state.view !== "memories" || !memoryPaging.filteredMemories() || memoryPaging.memoryResults.snapshot().loading) return;
    const key = memoryPaging.memoryKey(true);
    await memoryPaging.memoryResults.refresh(key).catch(() => null);
    if (current(ticket) && key === memoryPaging.memoryKey(true)) deps.render(false);
  }
  async function sync(event) {
    if (syncing) { again ||= !!event; return; }
    if (blocked() || !eligible()) return;
    syncing = true;
    const view = state.view, start = performance.now();
    const ticket = { generation: state.connection.generation, page: pageKey(), data: state.data };
    try {
      const separate = special(view);
      if (separate) { await separate; return; }
      const reads = plan(view, ticket.data), results = await Promise.allSettled(reads.map(read => read.promise));
      if (!current(ticket)) return;
      if (publishRefresh(state, reads, results, deps.invalidateVersions)) { deps.render(); void memoryPaging.hydrateMaintenance(ticket.generation); }
      if (deps.catalogQuery?.visible()) await deps.catalogQuery.refreshVisible();
      else await refreshResults(ticket);
    } catch { /* retain current rows; a later scheduled/user refresh can retry */ }
    finally {
      window.ArkismPerf?.note(`sync.${view}`, performance.now() - start);
      syncing = false;
      if (again) { again = false; queueMicrotask(() => { void sync(); }); }
    }
  }
  sync.blocked = blocked;
  return sync;
}

const comparable = value => JSON.stringify(value instanceof Map ? [...value] : value, (key, item) => key === "_by" ? undefined : item);
export function publishRefresh(state, reads, results, invalidateVersions) {
  let changed = false;
  for (let i = 0; i < reads.length; i++) {
    const { key, old } = reads[i], result = results[i];
    if (result.status !== "fulfilled" || result.value == null || state.data[key] !== old || comparable(old) === comparable(result.value)) continue;
    if (key === "skills") {
      const previous = new Map((old || []).map(row => [row.id, row]));
      state.versions = new Map(result.value.filter(row => row.version_id === previous.get(row.id)?.version_id && row.updated_at === previous.get(row.id)?.updated_at && state.versions.has(row.version_id)).map(row => [row.version_id, state.versions.get(row.version_id)]));
      state.versionsDone = false; invalidateVersions();
    }
    state.data[key] = result.value; changed = true;
  }
  return changed;
}
