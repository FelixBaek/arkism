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
    if (ctl.tagName === "SELECT") return [...ctl.options].filter((o) => !o.hidden).map((o) => ({ value: o.value, label: o.label || o.textContent, disabled: o.disabled, on: o.selected }));
    const dl = document.getElementById(ctl.dataset.comboList || "");
    const q = ctl.dataset.comboTyped === "1" ? ctl.value.trim().toLowerCase() : "";
    return [...(dl?.options || [])].map((o) => ({ value: o.value, hint: o.label && o.label !== o.value ? o.label : "", on: o.value === ctl.value }))
      .filter((o) => !q || o.value.toLowerCase().includes(q) || o.hint.toLowerCase().includes(q));
  }
  function place() {
    const r = open.ctl.getBoundingClientRect();
    const below = innerHeight - r.bottom - 8, above = r.top - 8;
    const up = below < 180 && above > below;
    pop.style.minWidth = `${Math.round(r.width)}px`;
    pop.style.left = `${Math.round(Math.min(r.left, innerWidth - pop.offsetWidth - 8))}px`;
    pop.style.maxHeight = `${Math.round(Math.min(320, up ? above : below))}px`;
    pop.style.top = up ? `${Math.round(r.top - 4 - Math.min(pop.scrollHeight, 320, above))}px` : `${Math.round(r.bottom + 4)}px`;
  }
  function paint() {
    pop.replaceChildren(...open.items.map((o, i) => {
      const b = document.createElement("div");
      b.className = `combo-opt${o.on ? " on" : ""}${i === open.at ? " at" : ""}${o.disabled ? " off" : ""}`;
      b.id = `combo-opt-${i}`;
      b.setAttribute("role", "option");
      b.setAttribute("aria-selected", String(!!o.on));
      const t = document.createElement("span");
      t.textContent = o.label ?? o.value;
      b.append(t);
      if (o.hint) { const h = document.createElement("small"); h.textContent = o.hint; b.append(h); }
      b.addEventListener("pointerdown", (e) => { e.preventDefault(); if (!o.disabled) pick(o); });
      return b;
    }));
    open.ctl.setAttribute("aria-activedescendant", open.at >= 0 ? `combo-opt-${open.at}` : "");
    pop.querySelector(".at")?.scrollIntoView({ block: "nearest" });
  }
  function show(ctl) {
    const items = optionsOf(ctl);
    if (!items.length) { hide(); return; }
    if (open?.ctl !== ctl) hide();
    open = { ctl, items, at: items.findIndex((o) => o.on) };
    ctl.setAttribute("aria-expanded", "true");
    pop.id ||= `combo-pop-${++uid}`;
    ctl.setAttribute("aria-controls", pop.id);
    if (!pop.isConnected) document.body.append(pop);
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
    if (ctl.value === o.value) return;
    ctl.value = o.value;
    if (ctl.tagName !== "SELECT") ctl.dataset.comboTyped = "0";
    ctl.dispatchEvent(new Event("input", { bubbles: true }));
    ctl.dispatchEvent(new Event("change", { bubbles: true }));
  }
  const isSelect = (t) => t instanceof HTMLSelectElement && !t.multiple && t.size <= 1 && !t.disabled;
  // An <input list> loses the attribute so the browser's own autocomplete never opens; the id moves to data-combo-list.
  function adopt(t) {
    if (!(t instanceof HTMLInputElement)) return false;
    if (t.hasAttribute("list")) { t.dataset.comboList = t.getAttribute("list"); t.removeAttribute("list"); t.setAttribute("role", "combobox"); t.setAttribute("aria-autocomplete", "list"); t.setAttribute("aria-expanded", "false"); }
    return !!t.dataset.comboList && !t.disabled && !t.readOnly;
  }
  document.addEventListener("pointerdown", (e) => {
    const t = e.target;
    if (open && !pop.contains(t) && t !== open.ctl) hide();
    if (isSelect(t) && e.button === 0) { e.preventDefault(); t.focus(); if (open?.ctl === t) hide(); else show(t); return; }
    if (adopt(t)) { t.dataset.comboTyped = "0"; show(t); }
  }, true);
  document.addEventListener("focusin", (e) => { adopt(e.target); }, true);
  document.addEventListener("input", (e) => {
    const t = e.target;
    if (t instanceof HTMLInputElement && t.dataset.comboList && e.isTrusted) { t.dataset.comboTyped = "1"; show(t); }
  }, true);
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (!open || open.ctl !== t) {
      // Space/Enter/Alt+↓/F4 on a closed select, or ↓ in a list input, opens our list instead of the browser's.
      if (isSelect(t) && (e.key === " " || e.key === "Enter" || e.key === "F4" || (e.altKey && e.key === "ArrowDown"))) { e.preventDefault(); show(t); }
      else if (adopt(t) && e.key === "ArrowDown") { e.preventDefault(); show(t); }
      return;
    }
    const n = open.items.length;
    const step = (d) => { let i = open.at; for (let k = 0; k < n; k++) { i = (i + d + n) % n; if (!open.items[i].disabled) break; } open.at = i; paint(); };
    if (e.key === "ArrowDown") { e.preventDefault(); step(1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); step(-1); }
    else if (e.key === "Enter" && open.at >= 0) { e.preventDefault(); pick(open.items[open.at]); }
    else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); hide(); } // Esc closes the list, not the window under it
    else if (e.key === "Tab") hide();
  }, true);
  document.addEventListener("focusout", (e) => { if (open?.ctl === e.target) setTimeout(() => { if (open && document.activeElement !== open.ctl) hide(); }, 0); }, true);
  addEventListener("resize", hide);
  document.addEventListener("scroll", (e) => { if (open && !pop.contains(e.target)) hide(); }, true);
  document.addEventListener("close", hide, true); // a window closing takes its list with it
})();
