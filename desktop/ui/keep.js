// @ts-check
// Two shared guards for the screens. latest(): a reply that comes back after a newer request for the same key
// (or after cancel) is dropped, so an older answer never overwrites a newer choice. view(): a redraw that rebuilds
// a box keeps the field being typed in (text, caret, focus), open <details> and the scroll of the boxes it names.
(() => {
  function latest() {
    const turns = new Map();
    const stop = (key) => { const turn = turns.get(key); if (turn) { turns.delete(key); turn.abort(); } };
    return {
      // begin(key) supersedes the key's earlier turn; turn.live() stays true until a newer begin or a cancel.
      // turn.signal aborts the superseded fetch where the transport supports it; live() is the guard either way.
      begin(key = "") {
        stop(key);
        const controller = typeof AbortController === "function" ? new AbortController() : null;
        const turn = { signal: controller?.signal, live: () => turns.get(key) === turn, abort: () => controller?.abort() };
        turns.set(key, turn);
        return turn;
      },
      // cancel(key) drops one key's turn; cancel() drops every key (a reset, leaving the screen).
      cancel(key) { if (key === undefined) for (const k of [...turns.keys()]) stop(k); else stop(key); }
    };
  }

  // What names a control across a rebuild: its label, name, id or placeholder; a <details> by its summary.
  function name(node) {
    if (node.tagName === "DETAILS") return node.querySelector("summary")?.textContent || "";
    return node.getAttribute("aria-label") || node.getAttribute("name") || node.id || node.getAttribute("placeholder") || "";
  }
  function mark(root, node) {
    const tag = node.tagName, label = name(node);
    const same = [...root.querySelectorAll(tag.toLowerCase())].filter((n) => name(n) === label);
    return { tag, label, at: same.indexOf(node) };
  }
  function find(root, m) {
    return [...root.querySelectorAll(m.tag.toLowerCase())].filter((n) => name(n) === m.label)[m.at] || null;
  }
  // view(root, ...boxes): call before rebuilding root; the returned restore() puts the state back afterwards.
  // boxes are the scroll containers to hold still: ones that outlive the redraw (root itself when it scrolls).
  function view(root, ...boxes) {
    const doc = root.ownerDocument || document, active = doc.activeElement;
    // only a field being typed in or picked from; a pressed button is not put back (its row may be gone on purpose)
    const inside = !!active && active !== root && root.contains(active) && /^(INPUT|TEXTAREA|SELECT)$/.test(active.tagName);
    const focus = inside ? mark(root, active) : null;
    const field = inside && active.tagName !== "SELECT" ? { value: active.value, start: active.selectionStart, end: active.selectionEnd } : null;
    const open = [...root.querySelectorAll("details")].filter((d) => d.open).map((d) => mark(root, d));
    const scrolls = boxes.filter(Boolean).map((box) => [box, box.scrollTop]);
    return () => {
      for (const m of open) { const d = find(root, m); if (d) d.open = true; }
      for (const [box, top] of scrolls) if (box.scrollTop !== top) box.scrollTop = top;
      const now = doc.activeElement; // still on a live control (the redraw kept it, or focus moved on): leave it
      if (!focus || (now && now !== doc.body && now.isConnected !== false)) return;
      const next = find(root, focus);
      if (!next) return;
      // a reply that redrew the box must not take back what is being typed
      if (field && next.value !== field.value) next.value = field.value;
      next.focus?.({ preventScroll: true });
      try { if (field && field.start != null) next.setSelectionRange?.(field.start, field.end); } catch { /* a number or date field has no caret */ }
    };
  }

  const api = { latest, view };
  globalThis.ArkismKeep = api;
  if (typeof module !== "undefined") module.exports = api;
})();
