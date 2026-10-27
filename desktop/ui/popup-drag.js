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
  function dress(d, kid) {
    const html = document.documentElement;
    for (const { name, value } of html.attributes) if (name !== "style") d.documentElement.setAttribute(name, value);
    d.documentElement.style.cssText = html.style.cssText; // a style attribute is blocked by the CSP (look.js sets --fs and zoom here); CSSOM is not
    d.documentElement.classList.add("popout");
    // the app's CSP (style-src 'self') blocks a <style> element; a constructed sheet is not inline markup, so it applies.
    // Only when every sheet could be read: mixing adopted sheets with cloned ones would change their cascade order.
    const nodes = [...document.querySelectorAll('link[rel="stylesheet"], style')], texts = nodes.map(textOf);
    const Sheet = d.defaultView?.CSSStyleSheet;
    if (Sheet && "adoptedStyleSheets" in d && texts.every((css) => css != null)) {
      d.head.replaceChildren();
      d.adoptedStyleSheets = texts.map((css, i) => { const s = new Sheet({ media: nodes[i].media || "" }); s.replaceSync(css); return s; });
    } else d.head.replaceChildren(...nodes.map((n) => Object.assign(n.cloneNode(true), n.href ? { href: n.href } : {})));
    d.body.className = document.body.className;
    // the icon sprite (<symbol id="i-…"> in index.html): a <use href="#i-…"> only finds symbols in its own document
    for (const svg of new Set([...document.querySelectorAll("svg symbol")].map((s) => s.closest("svg")))) d.body.prepend(svg.cloneNode(true));
    d.title = kid.title ? `${kid.title} · Arkism` : "Arkism";
    // Esc closes the top popup there the way it does here (the root is no longer modal, so no cancel of its own).
    // On the document, set again whenever the window swaps in a fresh page (a listener on the old one would be gone).
    d.addEventListener("keydown", (e) => { if (e.key === "Escape" && !e.defaultPrevented) { const top = [...d.querySelectorAll("dialog[open]")].pop(); if (top) { e.preventDefault(); dismiss(top); } } });
    // The window has no OS title bar (popout.rs): its popup's head moves it, and a double-click there maximizes it.
    d.addEventListener("mousedown", (e) => {
      const t = e.target;
      if (e.button !== 0 || !t.closest?.(HEAD) || t.closest(SKIP) || !t.closest("dialog.popout-root")) return;
      e.preventDefault();
      void tauri()?.invoke(e.detail === 2 ? "plugin:window|toggle_maximize" : "plugin:window|start_dragging", { label: kid.label }).catch(() => {});
    });
    window.dispatchEvent(new CustomEvent("arkism-popout", { detail: d })); // other scripts set up their listeners there (ArkismPopout below)
  }
  // A stylesheet this window already read goes over as text: a <link> fetched again in the fresh blank window can be
  // dropped while that window is still being set up, which left a popup unstyled in Chrome.
  function textOf(n) {
    let css;
    try { css = n.tagName === "STYLE" ? n.textContent : [...n.sheet.cssRules].map((r) => r.cssText).join("\n"); } catch { return null; } // not loaded yet, or another origin
    if (css == null || n.tagName === "LINK" && !css) return null;
    // a url() in the text was relative to the sheet's own address (fonts/…): pin it there, the child page has another
    return css.replace(/url\(\s*(["'])(.*?)\1\s*\)/g, (_, q, u) => `url(${q}${new URL(u, n.href || document.baseURI).href}${q})`);
  }
  function place(kid, dlg) {
    const d = docOf(kid.w);
    if (!d || dlg.ownerDocument === d && d.body.contains(dlg)) return;
    if (!d.documentElement.classList.contains("popout")) dress(d, kid);
    const from = dlg.ownerDocument, had = dlg.contains(from.activeElement) ? from.activeElement : null, modal = dlg.matches(":modal");
    d.body.append(d.adoptNode ? d.adoptNode(dlg) : dlg);
    if (dlg.tagName !== "DIALOG") return had?.focus(); // a menu or list that code put on the page
    const root = !kid.root?.isConnected || kid.root.ownerDocument !== d || !kid.root.open;
    // a confirm over the popup was modal here and the move dropped that: modal there too, so the popup under it waits
    if (modal && !root) { dlg.removeAttribute("open"); dlg.showModal(); }
    if (!dlg.open) dlg.setAttribute("open", "");
    if (root) { kid.root = dlg; dlg.classList.add("popout-root"); watch(kid, dlg); }
    dlg.classList.remove("popping");
    dlg.style.translate = "";
    had?.focus(); // the move (and showModal) took the focus away
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
    const kid = { w, root: null, label, title: dlg.dataset.popoutTitle };
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
  // For other scripts: the popout documents open now, and the one the user is in (else this one). A new one comes as
  // an "arkism-popout" event on this window (detail: its document), so a listener set on `document` goes there too.
  window.ArkismPopout = {
    docs: () => [...kids.values()].map((k) => docOf(k.w)).filter((d) => d?.documentElement.classList.contains("popout")),
    current: () => docOf(focusedKid()?.w) || document
  };

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
