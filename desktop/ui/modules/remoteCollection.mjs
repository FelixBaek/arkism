// Shared cursor transport for management screens. Identity and filters are part
// of the key, so an old connection can never publish into a new scope.
export function createRemoteCollection({ state, read, createPager }) {
  const pager = createPager(async (key, page) => {
    const { filters } = JSON.parse(key);
    const result = await read(filters, page);
    if (!Number.isSafeInteger(result?.total) || result.total < 0) throw Error("catalog_page_invalid");
    return { ...result, metadata: { total: result.total, facets: result.facets || {} } };
  });
  const keyOf = filters => JSON.stringify({ server: state.server, scope: state.scope, generation: state.connection.generation, filters });
  function snapshot(filters) {
    const key = keyOf(filters), snap = pager.snapshot();
    if (snap.key !== key) return { items: [], total: null, cursor: "", ready: false, loading: false, error: "", facets: {} };
    return { ...snap, total: snap.metadata?.total ?? null, facets: snap.metadata?.facets || {} };
  }
  async function load(filters, more = false) {
    const key = keyOf(filters);
    try { await (more ? pager.snapshot().error ? pager.retry(key) : pager.load(key) : pager.refresh(key)); }
    catch (error) { if (error.message === "revision_conflict" && key === pager.snapshot().key) await pager.refresh(key); else throw error; }
    return key === keyOf(filters) && key === pager.snapshot().key ? snapshot(filters) : null;
  }
  return { snapshot, load, reset() { pager.reset(); } };
}
