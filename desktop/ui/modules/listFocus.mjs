// Async page completion can redraw controls while someone types. Keep the
// current input/selection; a completed request must not move focus to its footer.
export function retainListFocus(document) {
  const active = document.activeElement;
  const row = active?.closest("#table .row[data-id]");
  if (row) {
    const id = row.dataset.id, at = [row, ...row.querySelectorAll("button,a[href],input,select")].indexOf(active);
    const restore = () => {
      if (document.activeElement !== document.body) return;
      const next = [...document.querySelectorAll("#table .row[data-id]")].find(node => node.dataset.id === id);
      if (!next) return false;
      ([next, ...next.querySelectorAll("button,a[href],input,select")][at] || next).focus({ preventScroll: true });
      return true;
    };
    return () => { if (restore() === false) requestAnimationFrame(() => requestAnimationFrame(restore)); };
  }
  if (!active?.closest("#controls, #table .v4s-bar") || !active.matches("input,select")) return () => {};
  const selector = active.matches("input") ? "input" : "select";
  const label = active.getAttribute("aria-label"), start = active.selectionStart, end = active.selectionEnd;
  return () => {
    if (document.activeElement !== document.body) return;
    const replacement = [...document.querySelectorAll(`#controls ${selector}, #table .v4s-bar ${selector}`)].find(node => node.getAttribute("aria-label") === label);
    replacement?.focus({ preventScroll: true });
    if (replacement?.matches("input") && start !== null) replacement.setSelectionRange(start, end);
  };
}
