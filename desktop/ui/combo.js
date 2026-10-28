// App-styled drop-down lists in place of the web's own: a <select>'s option list and an <input list>'s autocomplete
// both open as one popover list (Figma 드롭다운) instead of the browser's white-box / autocomplete look.
// Works by delegation, so every select and datalist input in every screen gets it without touching the screens:
// the closed control keeps each screen's own styling; only the open list changes.
(() => {
  "use strict";
  let open = null; // { ctl, pop, items, at }
  const pop = document.createElement("div");
  pop.className = "combo-pop";
  pop.setAttribute("role", "listbox");
  pop.popover = "manual"; // top layer: shows above modal windows too
  let uid = 0;

  function optionsOf(ctl) {
    if (ctl.tagName === "SELECT") {
      // <optgroup> draws the same header row as data-group (its .logo element, else data-icon: the provider's logo); data-hint/data-tag as below.
      const rows = [];
      let og = null;
      for (const o of ctl.options) {
        if (o.hidden) continue;
        const g = o.parentElement.tagName === "OPTGROUP" ? o.parentElement : null;
        if (g && g !== og) rows.push({ head: g.label, icon: g.dataset.icon || g.label, logo: g.logo, badges: g.dataset.badges || "", disabled: true });
        og = g;
        rows.push({ value: o.value, label: o.label || o.textContent, hint: o.dataset.hint || "", tag: o.dataset.tag || "", badges: o.dataset.badges || "", disabled: o.disabled, on: o.selected });
      }
      return rows;
    }
    const dl = ctl.ownerDocument.getElementById(ctl.dataset.comboList || ""); // its own page: a popout window's too
    const q = ctl.dataset.comboTyped === "1" ? ctl.value.trim().toLowerCase() : "";
    const tab = open?.ctl === ctl ? open.tab : "";
    // data-group on options: a header row before each run of the same group (the options come in group order).
    const out = [];
    let group = "";
    for (const o of dl?.options || []) {
      const it = { value: o.value, hint: o.label && o.label !== o.value ? o.label : "", on: o.value === ctl.value, key: o.dataset.key || "", group: o.dataset.group || "", icon: o.dataset.icon || "", tag: o.dataset.tag || "", badges: o.dataset.badges || "" };
      if (tab && o.dataset.tab !== tab || q && !it.value.toLowerCase().includes(q) && !it.hint.toLowerCase().includes(q)) continue;
      const gk = `${it.group}\n${o.dataset.groupBadges || ""}`; // the same provider once per way it is connected
      if (it.group && gk !== group) out.push({ head: it.group, icon: o.dataset.groupIcon || it.group, badges: o.dataset.groupBadges || "", disabled: true });
      group = it.group ? gk : "";
      out.push(it);
    }
    return out;
  }
  // data-tabs on the datalist ("all" tab's label): tabs over the list, one per distinct option data-tab, in order.
  function tabsOf(ctl) {
    const dl = ctl.tagName === "SELECT" ? null : ctl.ownerDocument.getElementById(ctl.dataset.comboList || "");
    if (!dl?.dataset.tabs) return null;
    return [["", dl.dataset.tabs], ...[...new Set([...dl.options].map((o) => o.dataset.tab).filter(Boolean))].map((t) => [t, t])];
  }
  function place() {
    const r = open.ctl.getBoundingClientRect(), { innerWidth, innerHeight } = open.ctl.ownerDocument.defaultView; // the window it is in
    const below = innerHeight - r.bottom - 8, above = r.top - 8;
    const up = below < 180 && above > below;
    pop.style.minWidth = `${Math.round(r.width)}px`;
    pop.style.left = `${Math.round(Math.min(r.left, innerWidth - pop.offsetWidth - 8))}px`;
    pop.style.maxHeight = `${Math.round(Math.min(320, up ? above : below))}px`;
    pop.style.top = up ? `${Math.round(r.top - 4 - Math.min(pop.scrollHeight, 320, above))}px` : `${Math.round(r.bottom + 4)}px`;
  }
  // data-badges ("MAX,API"): how the model is connected, the same small marks as 설정 › 에이전트's rows (.ag-plan), after the name.
  const badges = (t, list) => { for (const b of (list || "").split(",").filter(Boolean)) { const s = document.createElement("span"); s.className = "ag-plan"; s.textContent = b; t.append(" ", s); } };
  function paint() {
    const bar = open.tabs && document.createElement("div");
    if (bar) {
      bar.className = "combo-tabs";
      bar.setAttribute("role", "tablist");
      for (const [t, label] of open.tabs) {
        const b = document.createElement("button");
        b.type = "button";
        b.className = `combo-tab${t === open.tab ? " on" : ""}`;
        b.setAttribute("role", "tab");
        b.setAttribute("aria-selected", String(t === open.tab));
        b.textContent = label;
        b.addEventListener("pointerdown", (e) => { e.preventDefault(); open.tab = t; open.items = optionsOf(open.ctl); open.at = -1; paint(); place(); });
        bar.append(b);
      }
    }
    const rows = open.items.map((o, i) => {
      if (o.head) {
        const h = document.createElement("div"); h.className = "combo-head"; h.setAttribute("role", "presentation");
        const m = o.logo ? o.logo.cloneNode(true) : window.ArkismBrand?.mark(o.icon); // the provider's logo before its name
        const t = document.createElement("span"); t.textContent = o.head; // one flex item, so the gap never splits the name
        badges(t, o.badges);
        h.append(...(m ? [m] : []), t);
        return h;
      }
      const b = document.createElement("div");
      b.className = `combo-opt${o.on ? " on" : ""}${i === open.at ? " at" : ""}${o.disabled ? " off" : ""}`;
      b.id = `combo-opt-${i}`;
      b.setAttribute("role", "option");
      b.setAttribute("aria-selected", String(!!o.on));
      const t = document.createElement("span");
      t.textContent = o.label ?? o.value;
      badges(t, o.badges);
      const m = o.icon && window.ArkismBrand?.mark(o.icon);
      if (m) b.append(m);
      b.append(t);
      if (o.hint) { const h = document.createElement("small"); h.textContent = o.hint; b.append(h); }
      if (o.tag) { const g = document.createElement("em"); g.className = "combo-tag"; g.textContent = o.tag; b.append(g); } // data-tag: a label at the right end
      b.addEventListener("pointerdown", (e) => { e.preventDefault(); if (!o.disabled) pick(o); });
      return b;
    });
    if (!rows.length) { const none = document.createElement("div"); none.className = "combo-none"; none.textContent = open.ctl.dataset.comboEmpty || "—"; rows.push(none); }
    pop.replaceChildren(...(bar ? [bar] : []), ...rows);
    open.ctl.setAttribute("aria-activedescendant", open.at >= 0 ? `combo-opt-${open.at}` : "");
    pop.querySelector(".at")?.scrollIntoView({ block: "nearest" });
  }
  function show(ctl) {
    const keep = open?.ctl === ctl ? open.tab : "";
    if (open?.ctl !== ctl) hide();
    const tabs = tabsOf(ctl);
    open = { ctl, tabs, tab: keep, items: [], at: -1 };
    const items = open.items = optionsOf(ctl);
    if (!items.length && !tabs && !ctl.dataset.comboEmpty) { hide(); return; }
    open.at = items.findIndex((o) => o.on);
    ctl.setAttribute("aria-expanded", "true");
    pop.id ||= `combo-pop-${++uid}`;
    ctl.setAttribute("aria-controls", pop.id);
    // Inside the open modal window, or the page under it is inert and a wheel over the list scrolls (and closes) the window.
    const host = ctl.closest("dialog[open]") || ctl.ownerDocument.body;
    if (pop.parentNode !== host) host.append(pop);
    if (pop.matches(":popover-open")) pop.hidePopover(); // re-show so it sits above a window opened since
    pop.showPopover();
    paint();
    place();
  }
  function hide() {
    if (!open) return;
    open.ctl.setAttribute("aria-expanded", "false");
    open.ctl.removeAttribute("aria-activedescendant");
    open = null;
    if (pop.matches(":popover-open")) pop.hidePopover();
  }
  function pick(o) {
    const ctl = open.ctl;
    hide();
    if (ctl.value === o.value && !o.key) return;
    ctl.value = o.value;
    if (o.key) ctl.dataset.comboKey = o.key;
    if (ctl.tagName !== "SELECT") ctl.dataset.comboTyped = "0";
    ctl.dispatchEvent(new Event("input", { bubbles: true }));
    ctl.dispatchEvent(new Event("change", { bubbles: true }));
  }
  // tag names, not instanceof: a node first touched in a popout window gets that window's classes
  const isSelect = (t) => t?.tagName === "SELECT" && !t.multiple && t.size <= 1 && !t.disabled;
  // An <input list> loses the attribute so the browser's own autocomplete never opens; the id moves to data-combo-list.
  function adopt(t) {
    if (t?.tagName !== "INPUT") return false;
    if (t.hasAttribute("list")) { t.dataset.comboList = t.getAttribute("list"); t.removeAttribute("list"); t.setAttribute("role", "combobox"); t.setAttribute("aria-autocomplete", "list"); t.setAttribute("aria-expanded", "false"); }
    return !!t.dataset.comboList && !t.disabled && !t.readOnly;
  }
  // On this page and on each popout window's (popup-drag.js), whose popups keep their selects and list inputs.
  const wire = (d) => {
    d.addEventListener("pointerdown", (e) => {
      const t = e.target;
      if (open && !pop.contains(t) && t !== open.ctl) hide();
      if (isSelect(t) && e.button === 0) { e.preventDefault(); t.focus(); if (open?.ctl === t) hide(); else show(t); return; }
      if (adopt(t)) { t.dataset.comboTyped = "0"; show(t); }
    }, true);
    d.addEventListener("focusin", (e) => { adopt(e.target); }, true);
    d.addEventListener("input", (e) => {
      const t = e.target;
      if (t?.tagName === "INPUT" && t.dataset.comboList && e.isTrusted) { t.dataset.comboTyped = "1"; delete t.dataset.comboKey; show(t); }
    }, true);
    d.addEventListener("keydown", (e) => {
      const t = e.target;
      if (!open || open.ctl !== t) {
        // Space/Enter/Alt+↓/F4 on a closed select, or ↓ in a list input, opens our list instead of the browser's.
        const opens = (isSelect(t) && (e.key === " " || e.key === "Enter" || e.key === "F4" || (e.altKey && e.key === "ArrowDown"))) || (adopt(t) && e.key === "ArrowDown");
        if (opens) { e.preventDefault(); show(t); }
        return;
      }
      const n = open.items.length;
      const step = (d) => { if (!n) return; let i = open.at; for (let k = 0; k < n; k++) { i = (i + d + n) % n; if (!open.items[i].disabled) break; } open.at = i; paint(); };
      if (e.key === "ArrowDown") { e.preventDefault(); step(1); }
      else if (e.key === "ArrowUp") { e.preventDefault(); step(-1); }
      else if (e.key === "Enter" && open.at >= 0) { e.preventDefault(); pick(open.items[open.at]); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); hide(); } // Esc closes the list, not the window under it
      else if (e.key === "Tab") hide();
    }, true);
    d.addEventListener("focusout", (e) => { if (open?.ctl === e.target) setTimeout(() => { if (open && open.ctl.ownerDocument.activeElement !== open.ctl) hide(); }, 0); }, true);
    d.defaultView.addEventListener("resize", hide);
    d.addEventListener("scroll", (e) => { if (open && !pop.contains(e.target)) hide(); }, true);
    d.addEventListener("close", hide, true); // a window closing takes its list with it
  };
  wire(document);
  addEventListener("arkism-popout", (e) => wire(e.detail));
  window.ArkismCombo = { open: (ctl) => { if (adopt(ctl)) { ctl.dataset.comboTyped = "0"; show(ctl); } } }; // a button that opens a list input's list
})();
