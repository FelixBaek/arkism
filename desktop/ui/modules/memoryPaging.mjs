// Owns memory cursors and refresh. Dependencies are explicit live ports, never a global scope.
export function createMemoryPaging(deps) {

  // Only requested pages live here. Counts and local sorting describe loaded rows.
  const memoryKey = (filtered = false) => JSON.stringify({ server: deps.state.server, scope: deps.state.scope, generation: deps.generation,
    q: filtered && deps.state.query ? (deps.$("#search").value.trim() || deps.state.query) : "", kind: filtered && deps.state.kind !== "all" ? deps.state.kind : "" });

  const legacyMemorySearch = new Set();

  const readMemoryPage = async (key, page) => {
    const { server, scope, q, kind } = JSON.parse(key);
    const params = { ...scope, status: "active", ...(q ? { q, ...(!legacyMemorySearch.has(server) ? { ignore_case: "true" } : {}) } : {}), ...(kind ? { kind } : {}), limit: page.limit, ...(page.cursor ? { cursor: page.cursor } : {}) };
    const read = () => deps.request(`/v1/memories?${new URLSearchParams(params)}`);
    try { return await read(); }
    catch (error) {
      if (!params.ignore_case || error.message !== "invalid_request") throw error;
      // Older servers have a strict query allowlist. Preserve their literal
      // text search rather than making a client upgrade require a server upgrade.
      delete params.ignore_case;
      const result = await read();
      legacyMemorySearch.add(server);
      return result;
    }
  };

  const memoryPages = window.ArkismMemoryPages.create(readMemoryPage);

  const memoryResults = window.ArkismMemoryPages.create(readMemoryPage);

  const maintenanceRecords = new Map();

  let lastMemoryUsage = 0;

  const partialMemories = () => !!memoryPages.snapshot().cursor;

  const memoryCount = () => `${deps.state.data.memories.length}${partialMemories() ? "+" : ""}`;

  const filteredMemories = () => deps.state.memTab === "active" && (!!deps.state.query || deps.state.kind !== "all");

  const currentMemoryPages = () => filteredMemories() ? memoryResults : memoryPages;

  function ensureMemoryResults() {
    if (!filteredMemories()) return;
    const key = memoryKey(true), snap = memoryResults.snapshot();
    if (snap.key === key) return;
    void memoryResults.load(key).then(() => { if (deps.state.view === "memories" && memoryKey(true) === key) deps.render(false); }, () => { if (deps.state.view === "memories" && memoryKey(true) === key) deps.render(false); });
  }

  async function fetchMoreMemories() {
    const pager = currentMemoryPages(), key = memoryKey(filteredMemories()), g = deps.generation;
    try {
      const items = await (pager.snapshot().error ? pager.retry(key) : pager.load(key));
      if (g === deps.generation && pager === memoryPages) deps.state.data.memories = items;
    } catch (_) { /* retain the previous page and offer retry in the footer */ }
  }

  async function hydrateMaintenance(g) {
    const refs = (deps.state.data.maintenance || []).flatMap(p => [p.maintenance?.keep, p.maintenance?.retire, p.maintenance?.memory]).filter(Boolean);
    const cached = new Map(deps.state.data.memories.map(m => [m.id, m]));
    const queue = [...new Map(refs.filter(ref => !cached.has(ref.id) || (ref.revision != null && cached.get(ref.id).revision !== ref.revision)).map(ref => [ref.id, ref])).values()];
    await Promise.all(Array.from({ length: Math.min(4, queue.length) }, async () => {
      while (queue.length && g === deps.generation) {
        const ref = queue.shift();
        const record = await deps.request(`/v1/memories/${encodeURIComponent(ref.id)}?${deps.query()}`).catch(() => null);
        if (g !== deps.generation) return;
        if (record?.id === ref.id && (ref.revision == null || record.revision === ref.revision)) maintenanceRecords.set(ref.id, record);
        else maintenanceRecords.delete(ref.id);
      }
    }));
    if (g === deps.generation && refs.length) deps.render(false);
  }

  return {
    get currentMemoryPages() { return currentMemoryPages; },
    get ensureMemoryResults() { return ensureMemoryResults; },
    get fetchMoreMemories() { return fetchMoreMemories; },
    get filteredMemories() { return filteredMemories; },
    get hydrateMaintenance() { return hydrateMaintenance; },
    get lastMemoryUsage() { return lastMemoryUsage; },
    set lastMemoryUsage(value) { lastMemoryUsage = value; },
    get legacyMemorySearch() { return legacyMemorySearch; },
    get maintenanceRecords() { return maintenanceRecords; },
    get memoryCount() { return memoryCount; },
    get memoryKey() { return memoryKey; },
    get memoryPages() { return memoryPages; },
    get memoryResults() { return memoryResults; },
    get partialMemories() { return partialMemories; }
  };
}
