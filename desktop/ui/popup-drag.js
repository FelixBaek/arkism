// Every popup moves by dragging its head. A dialog keeps its spot until it closes; it cannot leave the app window
// (shortcut: popups are <dialog>s inside one webview — moving one outside needs its own Tauri window, not done).
(() => {
  const HEAD = ".dialog-head, .crew-popup-head, .ov-win-head, .ov-detail-head, .ov-recall-head, .git-dialog-head, .keys-head";
  const SKIP = "button, a, input, select, textarea, label, summary, [contenteditable], [role=tab], [role=button], [draggable=true]";
  document.addEventListener("pointerdown", (ev) => {
    if (ev.button !== 0) return;
    const head = ev.target.closest?.(HEAD);
    const dlg = head?.closest("dialog[open]");
    if (!dlg || ev.target.closest(SKIP)) return;
    const [x0, y0] = (dlg.style.translate || "0px 0px").split(" ").map((v) => parseFloat(v) || 0);
    const sx = ev.clientX, sy = ev.clientY, r = dlg.getBoundingClientRect();
    const move = (e) => {
      // keep at least 48px of the head on screen so it can always be grabbed again
      const dx = Math.min(innerWidth - 48 - r.left, Math.max(48 - r.right, e.clientX - sx));
      const dy = Math.min(innerHeight - 48 - r.top, Math.max(-r.top, e.clientY - sy));
      dlg.style.translate = `${x0 + dx}px ${y0 + dy}px`;
    };
    const up = () => { removeEventListener("pointermove", move); removeEventListener("pointerup", up); removeEventListener("pointercancel", up); dlg.classList.remove("dragging"); };
    dlg.classList.add("dragging");
    addEventListener("pointermove", move);
    addEventListener("pointerup", up);
    addEventListener("pointercancel", up);
    ev.preventDefault();
  });
  // A reopened popup starts centered again.
  document.addEventListener("close", (ev) => { if (ev.target instanceof HTMLDialogElement) ev.target.style.translate = ""; }, true);
})();
