// 창 버튼: the window has no native title bar (decorations: false), so the top 32px strip drags the window
// (double-click maximizes, Tauri's drag region) and holds minimize / maximize / close. Desktop app only.
(() => {
  const win = window.__TAURI__?.window?.getCurrentWindow?.();
  const bar = document.getElementById("window-bar");
  if (!win || !bar) return; // browser preview: no window to move, keep the strip hidden
  bar.hidden = false;
  const max = bar.querySelector('[data-win="max"]');
  const label = (on) => { max.setAttribute("aria-label", on ? "이전 크기로" : "최대화"); max.title = max.getAttribute("aria-label"); max.classList.toggle("on", on); };
  const sync = () => void win.isMaximized().then(label).catch(() => {});
  bar.querySelector('[data-win="min"]').addEventListener("click", () => void win.minimize());
  max.addEventListener("click", () => void win.toggleMaximize().then(sync));
  bar.querySelector('[data-win="close"]').addEventListener("click", () => {
    // A separate popup window closes the way its popup does, so unsaved edits still ask first (app.js startPopout).
    const root = document.querySelector("dialog.popout-root[open]");
    if (root) { if (root.dispatchEvent(new Event("cancel", { cancelable: true }))) root.close(); return; }
    void win.close();
  });
  void win.onResized(sync);
  sync();
  // An open modal is drawn above everything else; one that covers the top-right corner (a page dialog, a popout
  // window's popup) takes the strip inside so the buttons stay on top and clickable; other modals hide it.
  const place = () => {
    const top = [...document.querySelectorAll("dialog[open]")].filter((d) => d.matches(":modal")).pop();
    const r = top?.getBoundingClientRect();
    const host = r && r.top <= 0 && r.right >= innerWidth - 1 ? top : document.body;
    if (bar.parentElement !== host) host.prepend(bar);
    // any other modal makes the strip inert (a click would only hit its backdrop), so it is hidden until it closes
    bar.classList.toggle("under-modal", !!top && host === document.body);
  };
  new MutationObserver(place).observe(document.body, { subtree: true, attributes: true, attributeFilter: ["open"] });
  addEventListener("resize", place);
})();
