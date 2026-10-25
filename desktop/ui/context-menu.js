// Right-click: the webview's own menu (뒤로, 공유, 인쇄…) never shows. A small menu shows only where it helps:
// text boxes (잘라내기·복사·붙여넣기·모두 선택), selected text (복사), links (링크 복사), and any element that offers
// its own items with el.ctxItems = () => [{ label, run }].
(() => {
  let menu = null;
  const close = () => { menu?.remove(); menu = null; };
  const notice = (t) => window.ArkismNotice?.(t);
  // the clipboard of the window the menu opened in: a popout's own document has the focus; a missing API rejects too
  const copy = (t, w) => new Promise((ok) => ok(w.navigator.clipboard.writeText(t))).then(() => notice("복사했어요."), () => notice("복사하지 못했어요."));

  function show(x, y, items, from) {
    close();
    const m = document.createElement("div");
    m.className = "cat-menu ctx-menu";
    m.setAttribute("role", "menu");
    for (const it of items) {
      const b = document.createElement("button");
      b.type = "button"; b.className = "cat-menu-item"; b.textContent = it.label;
      b.setAttribute("role", "menuitem");
      b.disabled = !!it.off;
      b.addEventListener("click", () => { close(); it.run(); });
      m.append(b);
    }
    // inside an open dialog, so the menu sits on the same top layer and is not inert
    (from.closest?.("dialog[open]") || from.ownerDocument.body).append(m);
    // a moved dialog (translate) shifts position: fixed, so measure and correct; then keep it on screen
    m.style.left = `${x}px`; m.style.top = `${y}px`;
    let r = m.getBoundingClientRect();
    const { innerWidth, innerHeight } = from.ownerDocument.defaultView; // the window it opened in (a popout's too)
    const tx = Math.max(8, Math.min(x, innerWidth - r.width - 8)), ty = Math.max(8, Math.min(y, innerHeight - r.height - 8));
    m.style.left = `${x + tx - r.left}px`; m.style.top = `${y + ty - r.top}px`;
    menu = m;
    m.querySelector("button:not(:disabled)")?.focus();
  }

  // Text box: the field and its selection are put back before each command, since the menu took the focus.
  function fieldItems(f) {
    const doc = f.ownerDocument, getSelection = () => doc.getSelection();
    const box = "selectionStart" in f && f.selectionStart !== null;
    const range = !box && getSelection().rangeCount ? getSelection().getRangeAt(0).cloneRange() : null;
    const s = box ? f.selectionStart : 0, e = box ? f.selectionEnd : 0;
    const picked = box ? s !== e : !!range && !range.collapsed;
    const back = () => { f.focus(); if (box) f.setSelectionRange(s, e); else if (range) { getSelection().removeAllRanges(); getSelection().addRange(range); } };
    const ro = f.readOnly || f.disabled;
    return [
      { label: "잘라내기", off: !picked || ro, run: () => { back(); doc.execCommand("cut"); } },
      { label: "복사", off: !picked, run: () => { back(); doc.execCommand("copy"); } },
      { label: "붙여넣기", off: ro, run: () => doc.defaultView.navigator.clipboard.readText().then((t) => { back(); doc.execCommand("insertText", false, t); }, () => notice("Ctrl+V로 붙여넣어 주세요.")) },
      { label: "모두 선택", run: () => { f.focus(); if (box) f.select(); else doc.execCommand("selectAll"); } },
    ];
  }

  // On this page and on each popout window's (popup-drag.js).
  const wire = (d) => {
    d.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      const t = e.target.nodeType === 1 ? e.target : e.target.parentElement; // not instanceof: a popout window's node may have its classes
      if (!t) return;
      // the menu key (or Shift+F10) fires at 0,0: open by the focused element instead
      let x = e.clientX, y = e.clientY;
      if (!x && !y) { const r = t.getBoundingClientRect(); x = r.left + 8; y = r.bottom; }
      const field = t.closest("input:not([type=checkbox]):not([type=radio]):not([type=range]):not([type=color]), textarea, [contenteditable]:not([contenteditable=false])");
      const sel = String(d.getSelection());
      let host = t; while (host && !host.ctxItems) host = host.parentElement;
      const link = t.closest("a[href]");
      const items = field ? fieldItems(field)
        : sel.trim() ? [{ label: "복사", run: () => copy(sel, d.defaultView) }]
        : host ? host.ctxItems()
        : link && /^https?:/.test(link.href) ? [{ label: "링크 복사", run: () => copy(link.href, d.defaultView) }]
        : [];
      if (items.length) show(x, y, items, t);
    });
    d.addEventListener("pointerdown", (e) => { if (menu && !menu.contains(e.target)) close(); }, true);
    d.addEventListener("keydown", (e) => {
      if (!menu) return;
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); return; }
      const step = { ArrowDown: 1, ArrowUp: -1 }[e.key];
      if (!step) return;
      e.preventDefault();
      const bs = [...menu.querySelectorAll("button:not(:disabled)")], i = bs.indexOf(d.activeElement);
      bs[(i + step + bs.length) % bs.length]?.focus();
    }, true);
    d.defaultView.addEventListener("blur", close);
    d.defaultView.addEventListener("resize", close);
    d.addEventListener("scroll", close, true);
  };
  wire(document);
  addEventListener("arkism-popout", (e) => wire(e.detail));
})();
