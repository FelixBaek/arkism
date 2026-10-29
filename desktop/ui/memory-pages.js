// @ts-check
// Bounded cursor transport; rendering stays in the existing memory screen.
(function (root) {
  "use strict";
  function create(read, size = 50) {
    const loads = globalThis.ArkismKeep.latest(); // reset() drops a page read still on its way
    let key = "", items = [], cursor = "", ready = false, pending = null, error = "", failedRefresh = false, pageCount = 0, history = new Set(), metadata = null;
    const snapshot = () => ({ key, items, cursor, ready, loading: !!pending, error, metadata });
    function reset(next = "") { loads.cancel(); key = next; items = []; cursor = ""; ready = false; pending = null; error = ""; failedRefresh = false; pageCount = 0; history = new Set(); metadata = null; }
    function load(next, refresh = false) {
      if (next !== key) reset(next);
      if (pending) return pending;
      if (ready && !cursor && !refresh) return Promise.resolve(items);
      const epoch = loads.begin(), initial = refresh || !ready;
      const pages = refresh ? Math.max(1, pageCount) : 1;
      const task = (async () => {
        let after = initial ? "" : cursor, merged = initial ? [] : [...items];
        const seen = new Set(merged.map(row => row.id)), visited = initial ? new Set() : new Set(history);
        let readCount = initial ? 0 : pageCount, nextMetadata = metadata;
        for (let n = 0; n < pages; n++) {
          const page = await read(next, { limit: size, cursor: after });
          if (!epoch.live()) return items;
          readCount++;
          if (!page || page.authority !== "data_only_untrusted" || !Array.isArray(page.items) || page.items.length > size || typeof page.next_cursor !== "string" || page.next_cursor.length > 2048) throw new Error("memory_page_invalid");
          for (const row of page.items) {
            if (!row || typeof row.id !== "string" || !row.id) throw new Error("memory_page_invalid");
            if (!seen.has(row.id)) { seen.add(row.id); merged.push(row); }
          }
          const tail = page.next_cursor;
          if (tail && (tail === after || visited.has(tail))) throw new Error("memory_cursor_repeated");
          visited.add(tail); after = tail; nextMetadata = page.metadata ?? null;
          if (!after) break;
        }
        if (epoch.live()) { items = merged; cursor = after; ready = true; error = ""; failedRefresh = false; pageCount = readCount; history = visited; metadata = nextMetadata; }
        return items;
      })();
      pending = task;
      task.then(() => { if (epoch.live()) pending = null; }, e => { if (epoch.live()) { pending = null; error = String(e.message || e); failedRefresh = refresh; } });
      return task;
    }
    return { snapshot, reset, load, retry: key => load(key, failedRefresh), refresh: key => load(key, true) };
  }
  if (typeof window === "undefined" && typeof module === "object" && module.exports) module.exports = { create };
  else root.ArkismMemoryPages = { create };
})(typeof window === "object" ? window : globalThis);
