// Popups: drag the head to move one; drop it outside the app (or press 새 창으로) to open it in its own window;
// a click outside a popup closes it the way Esc does, so a popup that guards unsaved edits still asks first.
(() => {
  const HEAD = ".dialog-head, .crew-popup-head, .ov-win-head, .ov-detail-head, .ov-recall-head, .git-dialog-head, .keys-head";
  const SKIP = "button, a, input, select, textarea, label, summary, [contenteditable], [role=tab], [role=button], [draggable=true]";
  const tauri = () => window.__TAURI__?.core;

  // Esc's path: a "cancel" a guard can stop, then close.
  const dismiss = (dlg) => { if (dlg.dispatchEvent(new Event("cancel", { cancelable: true }))) dlg.close(); };

  // Opens the popup in a separate window at screen point (x, y) and closes it here. Only popups that say how to reopen
  // themselves (data-popout) can go; unsaved edits stop it the same way closing would.
  async function tearOff(dlg, x, y) {
    const recipe = dlg.dataset.popout;
    const session = window.ArkismPopoutSession?.();
    if (!recipe || !session || !tauri()) return false;
    if (!dlg.dispatchEvent(new Event("cancel", { cancelable: true }))) return false;
    const r = dlg.getBoundingClientRect();
    try {
      await tauri().invoke("popout_open", { payload: JSON.stringify({ recipe: JSON.parse(recipe), session }), title: dlg.dataset.popoutTitle || "", x: Math.round(x), y: Math.round(y), width: Math.round(r.width), height: Math.round(r.height) });
    } catch (err) {
      window.ArkismNotice?.(`새 창으로 열지 못했어요 (${err?.message || err}).`);
      return false;
    }
    dlg.close();
    return true;
  }

  document.addEventListener("pointerdown", (ev) => {
    if (ev.button !== 0) return;
    const head = ev.target.closest?.(HEAD);
    const dlg = head?.closest("dialog[open]");
    if (!dlg || dlg.classList.contains("popout-root") || ev.target.closest(SKIP)) return;
    const [x0, y0] = (dlg.style.translate || "0px 0px").split(" ").map((v) => parseFloat(v) || 0);
    const sx = ev.clientX, sy = ev.clientY, r = dlg.getBoundingClientRect();
    const out = (e) => e.clientX < 0 || e.clientY < 0 || e.clientX > innerWidth || e.clientY > innerHeight;
    try { head.setPointerCapture(ev.pointerId); } catch { /* the pointer is already gone */ }
    const move = (e) => {
      dlg.classList.toggle("tear-off", !!dlg.dataset.popout && out(e));
      // keep at least 48px of the head on screen so it can always be grabbed again
      const dx = Math.min(innerWidth - 48 - r.left, Math.max(48 - r.right, e.clientX - sx));
      const dy = Math.min(innerHeight - 48 - r.top, Math.max(-r.top, e.clientY - sy));
      dlg.style.translate = `${x0 + dx}px ${y0 + dy}px`;
    };
    const up = (e) => {
      head.removeEventListener("pointermove", move); head.removeEventListener("pointerup", up); head.removeEventListener("pointercancel", up);
      dlg.classList.remove("dragging", "tear-off");
      // A new, unsaved expert/team/automation has no id to reopen in another window yet: say so instead of doing nothing.
      if (e.type === "pointerup" && out(e) && !dlg.dataset.popout && tauri() && dlg.querySelector(".crew-popup-head")) window.ArkismNotice?.("저장한 뒤에 앱 밖 새 창으로 옮길 수 있어요.");
      if (e.type === "pointerup" && out(e)) void tearOff(dlg, e.screenX - (sx - r.left), e.screenY - (sy - r.top)).then((gone) => { if (!gone) dlg.style.translate = `${x0}px ${y0}px`; });
    };
    dlg.classList.add("dragging");
    head.addEventListener("pointermove", move);
    head.addEventListener("pointerup", up);
    head.addEventListener("pointercancel", up);
    ev.preventDefault();
  });

  // 바깥 클릭: both the press and the release land on the backdrop (a drag that ends outside is not a click).
  let pressedOutside = null;
  const outside = (dlg, e) => { const r = dlg.getBoundingClientRect(); return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom; };
  document.addEventListener("pointerdown", (e) => { pressedOutside = e.target instanceof HTMLDialogElement && e.target.open && outside(e.target, e) ? e.target : null; }, true);
  document.addEventListener("click", (e) => {
    const dlg = pressedOutside;
    pressedOutside = null;
    if (dlg && e.target === dlg && outside(dlg, e) && !dlg.classList.contains("popout-root")) dismiss(dlg);
  }, true);

  // 새 창으로: the same tear-off from a button (keyboard, or when dragging out is awkward). Desktop app only.
  new MutationObserver(() => {
    if (document.documentElement.classList.contains("popout") || !tauri()?.invoke) return;
    for (const dlg of document.querySelectorAll("dialog[data-popout]:not([data-popout-btn])")) {
      const head = dlg.querySelector(HEAD);
      if (!head) continue;
      dlg.dataset.popoutBtn = "1";
      const b = document.createElement("button");
      b.type = "button";
      b.className = "ghost icon-btn popout-btn";
      b.setAttribute("aria-label", "새 창으로 열기");
      b.title = b.getAttribute("aria-label");
      b.innerHTML = '<svg viewBox="0 0 256 256" width="16" height="16" aria-hidden="true"><path fill="currentColor" d="M224 104a8 8 0 0 1-16 0V59.3l-66.3 66.4a8 8 0 0 1-11.4-11.4L196.7 48H152a8 8 0 0 1 0-16h64a8 8 0 0 1 8 8Zm-40 24a8 8 0 0 0-8 8v72H48V80h72a8 8 0 0 0 0-16H48a16 16 0 0 0-16 16v128a16 16 0 0 0 16 16h128a16 16 0 0 0 16-16v-72a8 8 0 0 0-8-8Z"/></svg>';
      b.addEventListener("click", () => { const r = dlg.getBoundingClientRect(); void tearOff(dlg, window.screenX + r.left + 40, window.screenY + r.top + 40); });
      const close = head.querySelector(".crew-popup-close");
      close ? close.before(b) : head.append(b);
    }
  }).observe(document.documentElement, { childList: true, subtree: true });

  // A reopened popup starts centered again.
  document.addEventListener("close", (ev) => { if (ev.target instanceof HTMLDialogElement) ev.target.style.translate = ""; }, true);
})();
