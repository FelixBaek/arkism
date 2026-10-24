// Popups: drag the head to move one; drop it outside the app (or press Alt+Shift+O) to open it in its own window;
// a click outside a popup closes it the way Esc does, so a popup that guards unsaved edits still asks first.
(() => {
  const HEAD = ".dialog-head, .crew-popup-head, .ov-win-head, .ov-detail-head, .ov-recall-head, .keys-head";
  const SKIP = "button, a, input, select, textarea, label, summary, [contenteditable], [role=tab], [role=button], [draggable=true]";
  const tauri = () => window.__TAURI__?.core;

  // Esc's path: a "cancel" a guard can stop, then close.
  const dismiss = (dlg) => { if (dlg.dispatchEvent(new Event("cancel", { cancelable: true }))) dlg.close(); };

  // 새 창으로: the popup itself moves into a separate OS window opened with window.open (popout.rs builds it on this
  // window's browser environment), the way Obsidian pops out a pane. Its state, listeners and data stay as they are,
  // so nothing reloads. Only popups that can live alone (data-popout) go.
  const kids = new Map(); // popout window label → { w, root }
  let seq = Date.now() % 1e8;
  const docOf = (w) => { try { return w.closed ? null : w.document; } catch { return null; } };
  // A child's document as this one's: same stylesheets, theme and font attributes, marked popout (app.css).
  function dress(d) {
    const html = document.documentElement;
    for (const { name, value } of html.attributes) d.documentElement.setAttribute(name, value);
    d.documentElement.classList.add("popout");
    d.head.replaceChildren(...[...document.querySelectorAll('link[rel="stylesheet"], style')].map((n) => inline(d, n) || Object.assign(n.cloneNode(true), n.href ? { href: n.href } : {})));
    d.body.className = document.body.className;
  }
  // A stylesheet this window already read goes over as text: a <link> fetched again in the fresh blank window can be
  // dropped while that window is still being set up, which left a popup unstyled in Chrome.
  function inline(d, n) {
    let css;
    try { css = n.tagName === "LINK" && [...n.sheet.cssRules].map((r) => r.cssText).join("\n"); } catch { return null; } // not loaded yet, or another origin
    if (!css) return null;
    // a url() in the text was relative to the sheet's own address (fonts/…): pin it there, the child page has another
    css = css.replace(/url\(\s*(["'])(.*?)\1\s*\)/g, (_, q, u) => `url(${q}${new URL(u, n.href).href}${q})`);
    const s = d.createElement("style");
    if (n.media) s.media = n.media;
    s.textContent = css;
    return s;
  }
  function place(kid, dlg) {
    const d = docOf(kid.w);
    if (!d || dlg.ownerDocument === d && d.body.contains(dlg)) return;
    if (!d.documentElement.classList.contains("popout")) dress(d);
    d.body.append(d.adoptNode ? d.adoptNode(dlg) : dlg);
    if (dlg.tagName !== "DIALOG") return; // a menu or list that code put on the page
    if (!dlg.open) dlg.setAttribute("open", "");
    if (!kid.root?.isConnected || kid.root.ownerDocument !== d || !kid.root.open) { kid.root = dlg; dlg.classList.add("popout-root"); watch(kid, dlg); }
    dlg.classList.remove("popping");
    dlg.style.translate = "";
  }
  // The child's last popup closed (✕, 저장, Esc): its window goes. A popup that swaps itself for the next one
  // (기억 처리 ‹ ›) opens that one in the same tick, so look after it.
  const owner = new WeakMap(); // dialog → the window it is the root of now
  function watch(kid, dlg) {
    const known = owner.has(dlg);
    owner.set(dlg, kid);
    if (known) return;
    dlg.addEventListener("close", () => setTimeout(() => {
      const kid = owner.get(dlg), label = kid.label;
      if (kids.get(label) !== kid) return;
      const d = docOf(kid.w);
      if (d?.querySelector("dialog[open]")) return;
      kids.delete(label);
      void tauri()?.invoke("popout_destroy", { label }).catch(() => kid.w.close());
    }));
  }
  function tearOff(dlg, x, y, quiet) {
    if (!dlg.dataset.popout || !tauri() || dlg.classList.contains("popout-root")) return false;
    const r = dlg.getBoundingClientRect(), label = `popout-${++seq}`;
    const title = encodeURIComponent(dlg.dataset.popoutTitle || "");
    const w = window.open(`about:blank#${label}|${title}`, label, `popup,left=${Math.round(x)},top=${Math.round(y)},width=${Math.round(r.width)},height=${Math.round(r.height)}`);
    if (!w) { if (!quiet) window.ArkismNotice?.("새 창으로 열지 못했어요."); return false; }
    const kid = { w, root: null, label };
    kids.set(label, kid);
    place(kid, dlg);
    // The window may swap its first blank page for a fresh one while it is being built: move the popup again if so.
    let n = 0;
    const again = setInterval(() => {
      // a window whose page never got ready leaves the popup here: show it again and close that empty window
      if (++n > 60 || kids.get(label) !== kid || !(kid.root || dlg).open) {
        if (!kid.root) {
          dlg.classList.remove("popping");
          if (kids.get(label) === kid) { kids.delete(label); void tauri()?.invoke("popout_destroy", { label }).catch(() => w.close()); }
        }
        return clearInterval(again);
      }
      if (docOf(w)) place(kid, kid.root || dlg);
    }, 50);
    const d = docOf(w);
    if (d) {
      d.title = dlg.dataset.popoutTitle ? `${dlg.dataset.popoutTitle} · Arkism` : "Arkism";
      // Esc closes the top popup there the way it does here (a moved dialog is no longer modal, so no cancel of its own)
      w.addEventListener("keydown", (e) => { if (e.key === "Escape" && !e.defaultPrevented) { const top = [...docOf(w).querySelectorAll("dialog[open]")].pop(); if (top) { e.preventDefault(); dismiss(top); } } });
    }
    return true;
  }
  // The OS ✕ on a popout window (popout.rs): close its popup the way Esc does, so unsaved edits still ask first.
  void window.__TAURI__?.event?.listen?.("popout-close", (e) => {
    const kid = kids.get(e.payload), d = kid && docOf(kid.w);
    const top = d && [...d.querySelectorAll("dialog[open]")].pop();
    if (top) dismiss(top); else void tauri()?.invoke("popout_destroy", { label: e.payload });
  });
  // The window the user is in: a popup or menu that code opens from there (a confirm, the next stage) goes there too.
  const FLOAT = (n) => n.tagName === "DIALOG" || n.hasAttribute("popover") || /^(menu|listbox|tooltip)$/.test(n.getAttribute("role") || "") || /menu/.test(n.className || "");
  const focusedKid = () => [...kids.values()].find((k) => docOf(k.w)?.hasFocus());

  // 처음부터 창으로: a popup that can live alone opens in its own window at once, where it would have appeared here.
  new MutationObserver((list) => {
    for (const m of list) {
      const n = m.type === "attributes" ? m.target : null;
      const kid = focusedKid();
      if (kid) {
        for (const a of m.addedNodes || []) if (a.nodeType === 1 && a.parentNode === document.body && FLOAT(a)) place(kid, a);
        if (n instanceof HTMLDialogElement && n.open && n.ownerDocument === document) place(kid, n);
        continue;
      }
      if (!(n instanceof HTMLDialogElement) || !n.open || !n.dataset.popout || n.classList.contains("popping") || n.ownerDocument !== document) continue;
      n.classList.add("popping");
      requestAnimationFrame(() => { const r = n.getBoundingClientRect(); if (n.ownerDocument === document && !tearOff(n, window.screenX + r.left, window.screenY + r.top, true)) n.classList.remove("popping"); }); // no window: it just opens here
    }
  }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["open", "data-popout"] });

  document.addEventListener("pointerdown", (ev) => {
    if (ev.button !== 0) return;
    const head = ev.target.closest?.(HEAD);
    const dlg = head?.closest("dialog[open]");
    if (!dlg || dlg.classList.contains("popout-root") || ev.target.closest(SKIP)) return;
    // "-80px 0px" reads back as "-80px": a missing y is 0 (NaN here froze the popup after a sideways or top-clamped drag)
    const [x0 = 0, y0 = 0] = (dlg.style.translate || "0px").split(" ").map((v) => parseFloat(v) || 0);
    const sx = ev.clientX, sy = ev.clientY, r = dlg.getBoundingClientRect();
    const barH = () => document.getElementById("window-bar")?.hidden === false ? 32 : 0;
    const out = (e) => e.clientX < 0 || e.clientY < 0 || e.clientX > innerWidth || e.clientY > innerHeight;
    try { head.setPointerCapture(ev.pointerId); } catch { /* the pointer is already gone */ }
    const move = (e) => {
      dlg.classList.toggle("tear-off", !!dlg.dataset.popout && out(e));
      // keep at least 48px of the head on screen so it can always be grabbed again
      const dx = Math.min(innerWidth - 48 - r.left, Math.max(48 - r.right, e.clientX - sx));
      const dy = Math.min(innerHeight - 48 - r.top, Math.max(barH() - r.top, e.clientY - sy)); // never under the 창 버튼 strip
      dlg.style.translate = `${x0 + dx}px ${y0 + dy}px`;
    };
    const up = (e) => {
      head.removeEventListener("pointermove", move); head.removeEventListener("pointerup", up); head.removeEventListener("pointercancel", up);
      dlg.classList.remove("dragging", "tear-off");
      // A new, unsaved expert/team/automation has no id to reopen in another window yet: say so instead of doing nothing.
      if (e.type === "pointerup" && out(e) && !dlg.dataset.popout && tauri() && dlg.querySelector(".crew-popup-head")) window.ArkismNotice?.("저장한 뒤에 앱 밖 새 창으로 옮길 수 있어요.");
      if (e.type === "pointerup" && out(e) && !tearOff(dlg, e.screenX - (sx - r.left), e.screenY - (sy - r.top))) dlg.style.translate = `${x0}px ${y0}px`;
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

  // The keyboard's tear-off (Alt+Shift+O, named on the popup as aria-keyshortcuts): the top popup opens in its own
  // window where it sits now. e.code, since Option+Shift+O types "Ø" on a Mac; e.key for layouts with O elsewhere (Dvorak).
  document.addEventListener("keydown", (e) => {
    if ((e.code !== "KeyO" && e.key?.toLowerCase() !== "o") || e.repeat || !e.altKey || !e.shiftKey || e.ctrlKey || e.metaKey) return;
    const dlg = [...document.querySelectorAll("dialog[open]")].pop();
    if (!dlg || dlg.classList.contains("popout-root")) return;
    e.preventDefault();
    const say = (t) => window.ArkismNotice?.(t);
    if (!dlg.dataset.popout) return say(dlg.querySelector(".crew-popup-head") ? "저장한 뒤에 앱 밖 새 창으로 옮길 수 있어요." : "이 창은 새 창으로 열 수 없어요.");
    if (!tauri()) return say("새 창은 데스크톱 앱에서 열 수 있어요.");
    const r = dlg.getBoundingClientRect();
    tearOff(dlg, window.screenX + r.left, window.screenY + r.top);
  });

  // A reopened popup starts centered again.
  document.addEventListener("close", (ev) => { if (ev.target instanceof HTMLDialogElement) ev.target.style.translate = ""; }, true);
})();
