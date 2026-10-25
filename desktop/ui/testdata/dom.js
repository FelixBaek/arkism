// A small DOM stand-in for UI behavior tests (no npm dependencies): elements, a CSS selector subset,
// event capture/bubbling, dialogs, popovers, an HTML parser for index.html and timers you can flush.
"use strict";
const fs = require("fs");
const path = require("path");

const VOID = new Set(["area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"]);
const RAW = new Set(["script", "style"]);

class Event {
  constructor(type, init = {}) {
    this.type = type; this.bubbles = !!init.bubbles; this.cancelable = !!init.cancelable;
    this.defaultPrevented = false; this.target = null; this.currentTarget = null;
    this._stop = false; this._stopNow = false;
    Object.assign(this, init);
    this.timeStamp = Date.now();
  }
  preventDefault() { if (this.cancelable !== false) this.defaultPrevented = true; }
  stopPropagation() { this._stop = true; }
  stopImmediatePropagation() { this._stop = this._stopNow = true; }
  composedPath() { const p = []; for (let n = this.target; n; n = n.parentNode) p.push(n); p.push(globalThis.window); return p; }
}
class CustomEvent extends Event { constructor(type, init = {}) { super(type, init); this.detail = init.detail ?? null; } }

class Target {
  constructor() { this._ls = []; }
  addEventListener(type, fn, opts) {
    if (!fn) return;
    const capture = typeof opts === "boolean" ? opts : !!opts?.capture;
    if (this._ls.some((l) => l.type === type && l.fn === fn && l.capture === capture)) return;
    this._ls.push({ type, fn, capture, once: !!opts?.once });
    opts?.signal?.addEventListener?.("abort", () => this.removeEventListener(type, fn, opts));
  }
  removeEventListener(type, fn, opts) {
    const capture = typeof opts === "boolean" ? opts : !!opts?.capture;
    this._ls = this._ls.filter((l) => !(l.type === type && l.fn === fn && l.capture === capture));
  }
  _fire(e, phase) {
    for (const l of [...this._ls]) {
      if (l.type !== e.type) continue;
      if (phase === "capture" && !l.capture) continue;
      if (phase === "bubble" && l.capture) continue;
      if (l.once) this.removeEventListener(l.type, l.fn, l.capture);
      e.currentTarget = this;
      typeof l.fn === "function" ? l.fn.call(this, e) : l.fn.handleEvent(e);
      if (e._stopNow) break;
    }
  }
  dispatchEvent(e) {
    e.target = this;
    const chain = [];
    for (let n = this.parentNode; n; n = n.parentNode) chain.push(n);
    if (this instanceof Node && this.ownerDocument && chain.at(-1) === this.ownerDocument && globalThis.window?._ls) chain.push(globalThis.window);
    for (const n of chain.slice().reverse()) { n._fire(e, "capture"); if (e._stop) return !e.defaultPrevented; }
    this._fire(e, "target");
    if (e.bubbles) for (const n of chain) { if (e._stop) break; n._fire(e, "bubble"); }
    if (!e.defaultPrevented && e.type === "click") this._activate?.(e);
    return !e.defaultPrevented;
  }
}

class Node extends Target {
  constructor(doc) { super(); this.ownerDocument = doc; this.parentNode = null; this.childNodes = []; }
  get parentElement() { return this.parentNode instanceof Element ? this.parentNode : null; }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes.at(-1) || null; }
  get nextSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) + 1] || null : null; }
  get previousSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) - 1] || null : null; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === this.ownerDocument; }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) { this.replaceChildren(); if (v !== "" && v != null) this.appendChild(this.ownerDocument.createTextNode(String(v))); }
  get nodeValue() { return null; }
  contains(n) { for (; n; n = n.parentNode) if (n === this) return true; return false; }
  _nodes(items) {
    const out = [];
    for (const it of items) {
      if (it instanceof Fragment) { out.push(...it.childNodes); it.childNodes = []; }
      else out.push(it instanceof Node ? it : this.ownerDocument.createTextNode(String(it)));
    }
    return out;
  }
  _insert(nodes, index) {
    for (const n of nodes) {
      if (n.parentNode) { const i = n.parentNode.childNodes.indexOf(n); if (n.parentNode === this && i < index) index--; n.parentNode.childNodes.splice(i, 1); n.parentNode._changed?.("childList", n.parentNode, undefined, undefined, [], [n]); }
      n.parentNode = this;
      this.childNodes.splice(index++, 0, n);
    }
    this._changed?.("childList", this, undefined, undefined, nodes);
  }
  appendChild(n) { this._insert(this._nodes([n]), this.childNodes.length); return n; }
  insertBefore(n, ref) { const i = ref ? this.childNodes.indexOf(ref) : -1; this._insert(this._nodes([n]), i < 0 ? this.childNodes.length : i); return n; }
  removeChild(n) { const i = this.childNodes.indexOf(n); if (i >= 0) { this.childNodes.splice(i, 1); n.parentNode = null; this._changed?.("childList", this, undefined, undefined, [], [n]); } return n; }
  replaceChild(n, old) { this.insertBefore(n, old); return this.removeChild(old); }
  append(...items) { this._insert(this._nodes(items), this.childNodes.length); }
  prepend(...items) { this._insert(this._nodes(items), 0); }
  replaceChildren(...items) { const nodes = this._nodes(items); for (const c of this.childNodes) c.parentNode = null; this.childNodes = []; this._insert(nodes, 0); }
  remove() { this.parentNode?.removeChild(this); }
  before(...items) { const p = this.parentNode; if (p) p._insert(p._nodes(items), p.childNodes.indexOf(this)); }
  after(...items) { const p = this.parentNode; if (p) p._insert(p._nodes(items), p.childNodes.indexOf(this) + 1); }
  replaceWith(...items) { const p = this.parentNode; if (!p) return; const nodes = p._nodes(items).filter((n) => n !== this); p._insert(nodes, p.childNodes.indexOf(this)); this.remove(); }
  cloneNode(deep) {
    let c;
    if (this instanceof Text) c = this.ownerDocument.createTextNode(this.data);
    else if (this instanceof Fragment) c = this.ownerDocument.createDocumentFragment();
    else { c = this.ownerDocument.createElement(this.localName); c._attrs = new Map(this._attrs); }
    if (deep) for (const k of this.childNodes) c.appendChild(k.cloneNode(true));
    return c;
  }
}
class Text extends Node {
  constructor(doc, data) { super(doc); this.data = data; this.nodeType = 3; this.nodeName = "#text"; }
  get textContent() { return this.data; }
  set textContent(v) { this.data = String(v); this.parentNode?._changed?.("characterData", this); }
  get nodeValue() { return this.data; }
  set nodeValue(v) { this.textContent = v; }
  get wholeText() { return this.data; }
}
class Comment extends Node { constructor(doc, data) { super(doc); this.data = data; this.nodeType = 8; } get textContent() { return ""; } }
class Fragment extends Node {
  constructor(doc) { super(doc); this.nodeType = 11; }
  querySelector(s) { return query(this, s, true); }
  querySelectorAll(s) { return query(this, s, false); }
  get children() { return this.childNodes.filter((c) => c instanceof Element); }
}

const toDataKey = (a) => a.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
const toDataAttr = (k) => "data-" + k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
function datasetOf(el) {
  return new Proxy({}, {
    get: (_, k) => typeof k === "string" ? el.getAttribute(toDataAttr(k)) ?? undefined : undefined,
    set: (_, k, v) => { el.setAttribute(toDataAttr(k), String(v)); return true; },
    deleteProperty: (_, k) => { el.removeAttribute(toDataAttr(k)); return true; },
    has: (_, k) => el.hasAttribute(toDataAttr(k)),
    ownKeys: () => [...el._attrs.keys()].filter((a) => a.startsWith("data-")).map(toDataKey),
    getOwnPropertyDescriptor: (_, k) => el.hasAttribute(toDataAttr(k)) ? { enumerable: true, configurable: true, value: el.getAttribute(toDataAttr(k)) } : undefined
  });
}
function styleOf(el) {
  const props = new Map();
  const write = () => { el._attrs.set("style", [...props].map(([k, v]) => `${k}: ${v}`).join("; ")); };
  const css = (k) => k.startsWith("--") ? k : k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase());
  const api = {
    setProperty: (k, v) => { v === "" || v == null ? props.delete(k) : props.set(k, String(v)); write(); },
    getPropertyValue: (k) => props.get(k) ?? "",
    removeProperty: (k) => { const v = props.get(k) ?? ""; props.delete(k); write(); return v; },
    _load: (text) => { props.clear(); for (const part of String(text).split(";")) { const i = part.indexOf(":"); if (i > 0) props.set(part.slice(0, i).trim(), part.slice(i + 1).trim()); } }
  };
  return new Proxy(api, {
    get: (t, k) => k in t ? t[k] : k === "cssText" ? el._attrs.get("style") || "" : typeof k === "string" ? props.get(css(k)) ?? "" : undefined,
    set: (t, k, v) => { if (k === "cssText") t._load(v); else t.setProperty(css(k), v); write(); return true; }
  });
}
class ClassList {
  constructor(el) { this.el = el; }
  _get() { return (this.el.getAttribute("class") || "").split(/\s+/).filter(Boolean); }
  _set(list) { this.el.setAttribute("class", [...new Set(list)].join(" ")); }
  add(...c) { this._set([...this._get(), ...c]); }
  remove(...c) { this._set(this._get().filter((x) => !c.includes(x))); }
  contains(c) { return this._get().includes(c); }
  toggle(c, force) { const on = force === undefined ? !this.contains(c) : !!force; on ? this.add(c) : this.remove(c); return on; }
  replace(a, b) { if (!this.contains(a)) return false; this._set(this._get().map((x) => (x === a ? b : x))); return true; }
  get length() { return this._get().length; }
  item(i) { return this._get()[i] ?? null; }
  [Symbol.iterator]() { return this._get()[Symbol.iterator](); }
  toString() { return this._get().join(" "); }
}

const BOOL_PROPS = ["hidden", "disabled", "checked", "open", "required", "readOnly", "multiple", "selected", "autofocus", "inert", "draggable"];
const STR_PROPS = { id: "id", title: "title", type: "type", name: "name", placeholder: "placeholder", href: "href", src: "src", alt: "alt", role: "role",
  htmlFor: "for", method: "method", rel: "rel", target: "target", lang: "lang", autocomplete: "autocomplete", popover: "popover", min: "min", max: "max", step: "step", accept: "accept", download: "download", pattern: "pattern", spellcheck: "spellcheck", dir: "dir", inputMode: "inputmode", enterKeyHint: "enterkeyhint", ariaLabel: "aria-label" };

class Element extends Node {
  constructor(doc, name, ns) {
    super(doc);
    this.localName = ns ? name : name.toLowerCase(); this.namespaceURI = ns || "http://www.w3.org/1999/xhtml";
    this.nodeType = 1; this._attrs = new Map(); this.classList = new ClassList(this); this.dataset = datasetOf(this);
    this.style = styleOf(this); this._value = null; this.scrollTop = 0; this.scrollLeft = 0;
    this.selectionStart = null; this.selectionEnd = null;
    if (this.localName === "input" || this.localName === "textarea") this.selectionStart = this.selectionEnd = 0;
  }
  get tagName() { return this.localName.toUpperCase(); }
  get nodeName() { return this.tagName; }
  get children() { return this.childNodes.filter((c) => c instanceof Element); }
  get childElementCount() { return this.children.length; }
  get firstElementChild() { return this.children[0] || null; }
  get lastElementChild() { return this.children.at(-1) || null; }
  get nextElementSibling() { const p = this.parentNode; if (!p) return null; const s = p.childNodes.filter((c) => c instanceof Element); return s[s.indexOf(this) + 1] || null; }
  get previousElementSibling() { const p = this.parentNode; if (!p) return null; const s = p.childNodes.filter((c) => c instanceof Element); return s[s.indexOf(this) - 1] || null; }
  get className() { return this.getAttribute("class") || ""; }
  set className(v) { this.setAttribute("class", String(v)); }
  get attributes() { return [...this._attrs].map(([name, value]) => ({ name, value })); }
  getAttribute(n) { return this._attrs.has(n) ? this._attrs.get(n) : null; }
  getAttributeNames() { return [...this._attrs.keys()]; }
  setAttribute(n, v) {
    const old = this.getAttribute(n);
    this._attrs.set(n, String(v));
    if (n === "style") this.style._load(v);
    this._changed?.("attributes", this, n, old);
  }
  setAttributeNS(_, n, v) { this.setAttribute(n, v); }
  removeAttribute(n) { if (!this._attrs.has(n)) return; const old = this._attrs.get(n); this._attrs.delete(n); this._changed?.("attributes", this, n, old); }
  hasAttribute(n) { return this._attrs.has(n); }
  toggleAttribute(n, force) { const on = force === undefined ? !this.hasAttribute(n) : !!force; on ? this.setAttribute(n, "") : this.removeAttribute(n); return on; }
  _changed(kind, target = this, attributeName, oldValue, addedNodes = [], removedNodes = []) { this.ownerDocument?._mutated({ type: kind, target, attributeName, oldValue, addedNodes, removedNodes }); }
  get value() {
    if (this.localName === "select") { const o = this.options.find((x) => x.selected) || this.options[0]; return o ? o.value : ""; }
    if (this.localName === "option") return this._attrs.has("value") ? this._attrs.get("value") : this.textContent;
    if (this.localName === "button") return this.getAttribute("value") ?? "";
    if (this.localName === "textarea") return this._value ?? this.textContent;
    return this._value ?? this.getAttribute("value") ?? (this.type === "checkbox" || this.type === "radio" ? "on" : "");
  }
  set value(v) {
    if (this.localName === "select") { for (const o of this.options) o.selected = o.value === String(v); return; }
    if (this.localName === "option" || this.localName === "button") { this.setAttribute("value", v); return; }
    this._value = String(v ?? ""); if (this.selectionStart !== null) this.selectionStart = this.selectionEnd = this._value.length;
  }
  get valueAsNumber() { return this.value === "" ? NaN : Number(this.value); }
  get defaultValue() { return this.getAttribute("value") ?? ""; }
  set defaultValue(v) { this.setAttribute("value", v); }
  get options() { return query(this, "option", false); }
  get label() { return this.getAttribute("label") ?? (this.localName === "option" ? this.textContent : ""); }
  set label(v) { this.setAttribute("label", v); }
  get size() { return Number(this.getAttribute("size")) || 0; }
  get selectedIndex() { return this.options.findIndex((o) => o.selected); }
  set selectedIndex(i) { this.options.forEach((o, j) => { o.selected = i === j; }); }
  get selectedOptions() { return this.options.filter((o) => o.selected); }
  get labels() { return []; }
  get form() { return this.closest("form"); }
  get elements() { return query(this, "input, select, textarea, button", false); }
  get tabIndex() { return Number(this.getAttribute("tabindex") ?? (this.matches("button, input, select, textarea, a[href]") ? 0 : -1)); }
  set tabIndex(v) { this.setAttribute("tabindex", v); }
  get offsetParent() { return this.isConnected && !this.closest("[hidden]") ? this.parentElement : null; }
  get offsetWidth() { return this.getBoundingClientRect().width; }
  get offsetHeight() { return this.getBoundingClientRect().height; }
  get offsetTop() { return this.getBoundingClientRect().top; }
  get offsetLeft() { return this.getBoundingClientRect().left; }
  get clientWidth() { return this.getBoundingClientRect().width; }
  get clientHeight() { return this.getBoundingClientRect().height; }
  get scrollHeight() { return this._scrollHeight ?? this.getBoundingClientRect().height; }
  set scrollHeight(v) { this._scrollHeight = v; }
  get scrollWidth() { return this.getBoundingClientRect().width; }
  get innerText() { return this.textContent; }
  set innerText(v) { this.textContent = v; }
  get outerHTML() { return `<${this.localName}>${this.textContent}</${this.localName}>`; }
  get validity() { return { valid: !(this.required && !this.value) }; }
  checkValidity() { return this.validity.valid; }
  reportValidity() { return this.validity.valid; }
  setCustomValidity(m) { this._validity = m; }
  getBoundingClientRect() {
    const r = this._rect || { left: 0, top: 0, width: 100, height: 20 };
    return { x: r.left, y: r.top, left: r.left, top: r.top, width: r.width, height: r.height, right: r.left + r.width, bottom: r.top + r.height };
  }
  getClientRects() { return [this.getBoundingClientRect()]; }
  getRootNode() { let n = this; while (n.parentNode) n = n.parentNode; return n; }
  closest(s) { for (let n = this; n instanceof Element; n = n.parentNode) if (n.matches(s)) return n; return null; }
  matches(s) { return parseList(s).some((sel) => matchComplex(this, sel)); }
  querySelector(s) { return query(this, s, true); }
  querySelectorAll(s) { return query(this, s, false); }
  getElementsByTagName(t) { return query(this, t, false); }
  getElementsByClassName(c) { return query(this, "." + c, false); }
  focus() { const d = this.ownerDocument; if (d.activeElement === this) return; const old = d.activeElement; d.activeElement = this; old?.dispatchEvent(new Event("blur")); old?.dispatchEvent(new Event("focusout", { bubbles: true })); this.dispatchEvent(new Event("focus")); this.dispatchEvent(new Event("focusin", { bubbles: true })); }
  blur() { const d = this.ownerDocument; if (d.activeElement !== this) return; d.activeElement = d.body; this.dispatchEvent(new Event("blur")); this.dispatchEvent(new Event("focusout", { bubbles: true })); }
  click() { if (this.disabled) return; this.dispatchEvent(new Event("click", { bubbles: true, cancelable: true })); }
  _activate(e) {
    if (this.localName === "input" && (this.type === "checkbox" || this.type === "radio")) {
      this.checked = this.type === "radio" ? true : !this.checked;
      this.dispatchEvent(new Event("input", { bubbles: true })); this.dispatchEvent(new Event("change", { bubbles: true }));
    }
    if (this.localName === "label") { const f = this.htmlFor ? this.ownerDocument.getElementById(this.htmlFor) : this.querySelector("input"); if (f && f !== e.target) f.click(); }
    const button = this.localName === "button" ? this : null;
    if (button && (button.type || "submit") === "submit" && !button.disabled) button.form?.requestSubmit(button);
  }
  select() { this.selectionStart = 0; this.selectionEnd = this.value.length; }
  setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; }
  setRangeText(t) { const v = this.value; this.value = v.slice(0, this.selectionStart) + t + v.slice(this.selectionEnd); }
  scrollIntoView(o) { this._scrolled = o ?? true; }
  scrollTo(x, y) { if (typeof x === "object") { this.scrollTop = x.top ?? this.scrollTop; this.scrollLeft = x.left ?? this.scrollLeft; } else { this.scrollLeft = x; this.scrollTop = y; } }
  scrollBy(x, y) { if (typeof x === "object") { this.scrollTop += x.top || 0; this.scrollLeft += x.left || 0; } else { this.scrollLeft += x || 0; this.scrollTop += y || 0; } }
  setPointerCapture(id) { this._capture = id; }
  releasePointerCapture() { this._capture = null; }
  hasPointerCapture(id) { return this._capture === id; }
  animate() { return { finished: Promise.resolve(), cancel() {}, onfinish: null }; }
  attachShadow() { return this; }
  // dialog
  showModal() { if (this.open) throw new Error("InvalidStateError: dialog already open"); this.open = true; this._modal = true; this.ownerDocument._topLayer.push(this); }
  show() { this.open = true; this._modal = false; }
  close(rv) { if (!this.open) return; if (rv !== undefined) this.returnValue = rv; this.open = false; this._modal = false; this.ownerDocument._topLayer = this.ownerDocument._topLayer.filter((d) => d !== this); this.dispatchEvent(new Event("close")); }
  requestClose() { if (this.dispatchEvent(new Event("cancel", { cancelable: true }))) this.close(); }
  // popover
  showPopover() { this._popover = true; this.dispatchEvent(new Event("toggle")); }
  hidePopover() { this._popover = false; this.dispatchEvent(new Event("toggle")); }
  togglePopover(force) { const on = force ?? !this._popover; on ? this.showPopover() : this.hidePopover(); return on; }
  // form
  requestSubmit(submitter) {
    const e = new Event("submit", { bubbles: true, cancelable: true }); e.submitter = submitter ?? null;
    if (this.dispatchEvent(e) && this.getAttribute("method") === "dialog") { const d = this.closest("dialog"); if (d) d.close(submitter?.value ?? ""); }
  }
  submit() { this.requestSubmit(); }
  reset() { for (const f of this.elements) { f._value = null; } this.dispatchEvent(new Event("reset", { bubbles: true })); }
  // media / canvas
  getContext() { return new Proxy({}, { get: (t, k) => k in t ? t[k] : k === "measureText" ? (s) => ({ width: String(s).length * 7 }) : () => {}, set: (t, k, v) => { t[k] = v; return true; } }); }
  toDataURL() { return "data:image/png;base64,"; }
}
for (const p of BOOL_PROPS) {
  const attr = p.toLowerCase();
  Object.defineProperty(Element.prototype, p, {
    get() { return p === "checked" && this._checked !== undefined ? this._checked : p === "selected" && this._selected !== undefined ? this._selected : this.hasAttribute(attr); },
    set(v) { if (p === "checked") { this._checked = !!v; if (v && this.type === "radio" && this.name) for (const r of (this.form || this.ownerDocument).querySelectorAll(`input[type=radio][name="${this.name}"]`)) if (r !== this) r._checked = false; return; } if (p === "selected") { this._selected = !!v; if (v) { const s = this.closest("select"); if (s && !s.multiple) for (const o of s.options) if (o !== this) o._selected = false; } return; } this.toggleAttribute(attr, !!v); },
    configurable: true
  });
}
for (const [p, attr] of Object.entries(STR_PROPS)) {
  Object.defineProperty(Element.prototype, p, {
    get() {
      const v = this.getAttribute(attr);
      if (p === "type") return v ? v.toLowerCase() : this.localName === "button" ? "submit" : this.localName === "input" ? "text" : "";
      if (p === "href" && v != null && this.localName === "a") { try { return new URL(v, "http://tauri.localhost/").href; } catch { return v; } }
      return v ?? "";
    },
    set(v) { this.setAttribute(attr, v); },
    configurable: true
  });
}
Object.defineProperty(Element.prototype, "returnValue", { get() { return this._rv ?? ""; }, set(v) { this._rv = String(v); }, configurable: true });
Object.defineProperty(Element.prototype, "onclick", { get() { return this._onclick || null; }, set(fn) { if (this._onclick) this.removeEventListener("click", this._onclick); this._onclick = fn; if (fn) this.addEventListener("click", fn); }, configurable: true });
for (const ev of ["input", "change", "submit", "keydown", "close", "cancel", "load", "error", "pointerdown", "scroll", "toggle", "dblclick", "contextmenu", "mouseenter", "mouseleave", "focus", "blur", "wheel"]) {
  Object.defineProperty(Element.prototype, "on" + ev, { get() { return this["_on" + ev] || null; }, set(fn) { if (this["_on" + ev]) this.removeEventListener(ev, this["_on" + ev]); this["_on" + ev] = fn; if (fn) this.addEventListener(ev, fn); }, configurable: true });
}

// ---------------------------------------------------------------- selectors
function splitTop(s, sep) {
  const out = []; let depth = 0, quote = null, cur = "";
  for (const ch of s) {
    if (quote) { cur += ch; if (ch === quote) quote = null; continue; }
    if (ch === '"' || ch === "'") { quote = ch; cur += ch; continue; }
    if (ch === "(" || ch === "[") depth++;
    if (ch === ")" || ch === "]") depth--;
    if (depth === 0 && ch === sep) { out.push(cur); cur = ""; continue; }
    cur += ch;
  }
  out.push(cur);
  return out.map((x) => x.trim()).filter(Boolean);
}
const cache = new Map();
function parseList(s) {
  if (cache.has(s)) return cache.get(s);
  const list = splitTop(String(s), ",").map(parseComplex);
  cache.set(s, list);
  return list;
}
function parseComplex(s) {
  // tokens: compound, combinator
  const parts = []; let i = 0, cur = "", depth = 0, quote = null, comb = null;
  const flush = () => { if (cur.trim()) { parts.push({ comb, compound: parseCompound(cur.trim()) }); comb = null; } cur = ""; };
  while (i < s.length) {
    const ch = s[i];
    if (quote) { cur += ch; if (ch === quote) quote = null; i++; continue; }
    if (ch === '"' || ch === "'") { quote = ch; cur += ch; i++; continue; }
    if (ch === "(" || ch === "[") depth++;
    if (ch === ")" || ch === "]") depth--;
    if (depth === 0 && (ch === ">" || ch === "+" || ch === "~" || /\s/.test(ch))) {
      flush();
      if (!/\s/.test(ch)) comb = ch; else if (!comb) comb = comb || " ";
      i++; continue;
    }
    cur += ch; i++;
  }
  flush();
  if (parts[0]) parts[0].comb = null;
  return parts;
}
function parseCompound(s) {
  const c = { tag: null, id: null, classes: [], attrs: [], pseudos: [] };
  let i = 0;
  const name = () => { let n = ""; while (i < s.length && /[\w\-\\]/.test(s[i])) { if (s[i] === "\\") { n += s[i + 1]; i += 2; continue; } n += s[i++]; } return n; };
  if (s[0] === "*") i++; else if (/[a-zA-Z]/.test(s[0])) c.tag = name().toLowerCase();
  while (i < s.length) {
    const ch = s[i];
    if (ch === "#") { i++; c.id = name(); }
    else if (ch === ".") { i++; c.classes.push(name()); }
    else if (ch === "[") {
      let depth = 1, j = i + 1, quote = null;
      for (; j < s.length; j++) { const x = s[j]; if (quote) { if (x === quote) quote = null; continue; } if (x === '"' || x === "'") quote = x; else if (x === "]" && --depth === 0) break; }
      const body = s.slice(i + 1, j); i = j + 1;
      const m = body.match(/^\s*([\w\-:]+)\s*(?:([~^$*|]?=)\s*(?:"((?:[^"\\]|\\.)*)"|'([^']*)'|([^\s\]]+))\s*(i)?)?\s*$/);
      if (!m) throw new Error("selector attr " + body);
      c.attrs.push({ name: m[1], op: m[2], value: (m[3] ?? m[4] ?? m[5] ?? "").replace(/\\(.)/g, "$1") });
    } else if (ch === ":") {
      i++; if (s[i] === ":") i++;
      const p = name(); let arg = null;
      if (s[i] === "(") { let depth = 1, j = i + 1; for (; j < s.length; j++) { if (s[j] === "(") depth++; if (s[j] === ")" && --depth === 0) break; } arg = s.slice(i + 1, j); i = j + 1; }
      c.pseudos.push({ p, arg });
    } else throw new Error(`selector ${s} at ${i}`);
  }
  return c;
}
function matchCompound(el, c) {
  if (!(el instanceof Element)) return false;
  if (c.tag && el.localName.toLowerCase() !== c.tag) return false;
  if (c.id && el.getAttribute("id") !== c.id) return false;
  for (const k of c.classes) if (!el.classList.contains(k)) return false;
  for (const a of c.attrs) {
    const v = el.getAttribute(a.name);
    if (v === null) return false;
    if (!a.op) continue;
    if (a.op === "=" && v !== a.value) return false;
    if (a.op === "~=" && !v.split(/\s+/).includes(a.value)) return false;
    if (a.op === "^=" && !v.startsWith(a.value)) return false;
    if (a.op === "$=" && !v.endsWith(a.value)) return false;
    if (a.op === "*=" && !v.includes(a.value)) return false;
    if (a.op === "|=" && !(v === a.value || v.startsWith(a.value + "-"))) return false;
  }
  for (const { p, arg } of c.pseudos) {
    const sibs = el.parentNode ? el.parentNode.childNodes.filter((n) => n instanceof Element) : [el];
    const ok = {
      not: () => !el.matches(arg), is: () => el.matches(arg), where: () => el.matches(arg),
      has: () => query(el, arg.trim().replace(/^>/, ":scope >"), true) !== null,
      disabled: () => el.disabled, enabled: () => !el.disabled, checked: () => el.checked || el.selected,
      modal: () => !!el._modal && el.open, "popover-open": () => !!el._popover,
      "first-child": () => sibs[0] === el, "last-child": () => sibs.at(-1) === el, "only-child": () => sibs.length === 1,
      "first-of-type": () => sibs.filter((n) => n.localName === el.localName)[0] === el,
      "last-of-type": () => sibs.filter((n) => n.localName === el.localName).at(-1) === el,
      "nth-child": () => { const n = sibs.indexOf(el) + 1; if (arg === "odd") return n % 2 === 1; if (arg === "even") return n % 2 === 0; return n === Number(arg); },
      focus: () => el.ownerDocument.activeElement === el, "focus-visible": () => el.ownerDocument.activeElement === el,
      "focus-within": () => el.contains(el.ownerDocument.activeElement),
      hover: () => false, active: () => false, empty: () => el.childNodes.length === 0,
      scope: () => el === matchCompound.scope, root: () => el === el.ownerDocument.documentElement,
      "placeholder-shown": () => !el.value, invalid: () => !el.validity.valid, valid: () => el.validity.valid,
      "read-only": () => el.readOnly, "read-write": () => !el.readOnly, required: () => el.required, open: () => el.open,
      defined: () => true, visited: () => false, link: () => el.hasAttribute("href"), "any-link": () => el.hasAttribute("href"),
      "user-invalid": () => false, target: () => false, indeterminate: () => false
    }[p];
    if (!ok) throw new Error("pseudo " + p);
    if (!ok()) return false;
  }
  return true;
}
function matchComplex(el, parts, k = parts.length - 1) {
  if (!matchCompound(el, parts[k].compound)) return false;
  if (k === 0) return true;
  const comb = parts[k].comb;
  if (comb === ">") return !!el.parentElement && matchComplex(el.parentElement, parts, k - 1);
  if (comb === "+") return !!el.previousElementSibling && matchComplex(el.previousElementSibling, parts, k - 1);
  if (comb === "~") { for (let s = el.previousElementSibling; s; s = s.previousElementSibling) if (matchComplex(s, parts, k - 1)) return true; return false; }
  for (let a = el.parentElement; a; a = a.parentElement) if (matchComplex(a, parts, k - 1)) return true;
  return false;
}
function query(root, s, one) {
  const list = parseList(s), out = [];
  const prev = matchCompound.scope; matchCompound.scope = root;
  try {
    const walk = (n) => {
      for (const c of n.childNodes) {
        if (!(c instanceof Element)) continue;
        if (list.some((sel) => matchComplex(c, sel))) { out.push(c); if (one) return true; }
        if (walk(c)) return true;
      }
      return false;
    };
    walk(root);
  } finally { matchCompound.scope = prev; }
  return one ? out[0] || null : out;
}

// ---------------------------------------------------------------- document
class Document extends Node {
  constructor() {
    super(null); this.ownerDocument = this; this.nodeType = 9; this._topLayer = []; this._observers = [];
    this.documentElement = this.createElement("html"); this.appendChild(this.documentElement);
    this.head = this.createElement("head"); this.body = this.createElement("body");
    this.documentElement.append(this.head, this.body);
    this.activeElement = this.body; this.hidden = false; this.visibilityState = "visible"; this.readyState = "complete";
    this.fonts = { ready: Promise.resolve(), load: async () => [], check: () => true };
    this.execCommands = [];
  }
  get isConnected() { return true; }
  get title() { return this.querySelector("title")?.textContent || ""; }
  set title(v) { let t = this.querySelector("title"); if (!t) this.head.append((t = this.createElement("title"))); t.textContent = v; }
  createElement(t) { return new Element(this, t); }
  createElementNS(ns, t) { return new Element(this, t, ns); }
  createTextNode(t) { return new Text(this, String(t)); }
  createComment(t) { return new Comment(this, t); }
  createDocumentFragment() { return new Fragment(this); }
  createRange() { return new Range(); }
  createTreeWalker(root, what, filter) {
    // FILTER_REJECT (2) skips an element's whole subtree, FILTER_SKIP (3) only the element, as in browsers.
    const f = filter && (typeof filter === "function" ? filter : filter.acceptNode);
    const nodes = [];
    const visit = (n) => { for (const c of n.childNodes) {
      if ((what & 4 && c instanceof Text) || (what & 1 && c instanceof Element)) { const r = f ? f(c) : 1; if (r === 2) continue; if (r === 1) nodes.push(c); }
      visit(c);
    } };
    visit(root);
    let i = -1;
    return { currentNode: root, nextNode() { return ++i < nodes.length ? (this.currentNode = nodes[i]) : null; } };
  }
  getElementById(id) { return query(this, `[id="${id}"]`, true); }
  querySelector(s) { return query(this, s, true); }
  querySelectorAll(s) { return query(this, s, false); }
  getElementsByTagName(t) { return query(this, t, false); }
  elementFromPoint() { return this._pointElement || this.body; }
  elementsFromPoint() { return [this.elementFromPoint()]; }
  execCommand(...args) { this.execCommands.push(args); return true; }
  hasFocus() { return this._focus !== false; }
  adoptNode(n) {
    n.remove?.();
    const from = n.ownerDocument;
    if (from && from !== this) from._topLayer = from._topLayer.filter((d) => d !== n);
    const walk = (x) => { x.ownerDocument = this; for (const c of x.childNodes || []) walk(c); };
    walk(n);
    return n;
  }
  _mutated(rec) { for (const o of this._observers) o._note(rec); }
}
class Range {
  constructor() { this.collapsed = true; }
  cloneRange() { return this; }
  selectNodeContents(n) { this.node = n; }
  setStart() {} setEnd() {} collapse() {}
  getBoundingClientRect() { return { left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0 }; }
}
// HTMLSelectElement and friends: `instanceof` checks the tag, as code tells a <select> from an <input> that way.
const tagClass = (tag) => class extends Element { static [Symbol.hasInstance](o) { return o instanceof Element && o.localName === tag; } };
class MutationObserver {
  constructor(fn) { this.fn = fn; this.records = []; this.targets = []; }
  observe(target, opts = {}) { this.targets.push({ target, opts }); const d = target.ownerDocument || target; if (!d._observers.includes(this)) d._observers.push(this); }
  disconnect() { this.targets = []; for (const d of [globalThis.document]) if (d) d._observers = d._observers.filter((o) => o !== this); }
  takeRecords() { const r = this.records; this.records = []; return r; }
  _note(rec) {
    const hit = this.targets.some(({ target, opts }) => {
      if (!(rec.target === target || (opts.subtree && target.contains(rec.target)))) return false;
      if (rec.type === "attributes") return !!(opts.attributes || opts.attributeFilter) && (!opts.attributeFilter || opts.attributeFilter.includes(rec.attributeName));
      if (rec.type === "characterData") return !!opts.characterData;
      return !!opts.childList;
    });
    if (!hit) return;
    this.records.push(rec);
    if (this.records.length === 1) queueMicrotask(() => { const r = this.takeRecords(); if (r.length) this.fn(r, this); });
  }
}

// ---------------------------------------------------------------- HTML parser (enough for index.html)
function parseHTML(doc, html, into) {
  const stack = [into];
  const top = () => stack.at(-1);
  const re = /<!--[\s\S]*?-->|<!doctype[^>]*>|<\/([\w-]+)\s*>|<([\w-]+)((?:\s+[^\s"'>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+))?)*)\s*(\/?)>|([^<]+)/gi;
  let m;
  while ((m = re.exec(html))) {
    if (m[0].startsWith("<!")) continue;
    if (m[5] !== undefined) { if (top() !== into || m[5].trim()) top().appendChild(doc.createTextNode(decode(m[5]))); continue; }
    if (m[1]) { const name = m[1].toLowerCase(); for (let i = stack.length - 1; i > 0; i--) if (stack[i].localName.toLowerCase() === name) { stack.length = i; break; } continue; }
    const name = m[2];
    const svg = name.toLowerCase() === "svg" || top().namespaceURI === "http://www.w3.org/2000/svg";
    const el = svg ? doc.createElementNS("http://www.w3.org/2000/svg", name) : doc.createElement(name);
    for (const a of m[3].matchAll(/([^\s"'>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?/g)) el._attrs.set(a[1], decode(a[2] ?? a[3] ?? a[4] ?? ""));
    if (el._attrs.has("style")) el.style._load(el._attrs.get("style"));
    top().appendChild(el);
    if (RAW.has(el.localName)) { const end = html.indexOf(`</${el.localName}`, re.lastIndex); if (end > re.lastIndex) el.appendChild(doc.createTextNode(html.slice(re.lastIndex, end))); re.lastIndex = html.indexOf(">", end) + 1; continue; }
    if (!m[4] && !VOID.has(el.localName)) stack.push(el);
  }
}
const decode = (s) => s.replace(/&(amp|lt|gt|quot|#39|nbsp|middot|times|rarr|larr|hellip);/g, (_, e) => ({ amp: "&", lt: "<", gt: ">", quot: '"', "#39": "'", nbsp: "\u00a0", middot: "·", times: "×", rarr: "→", larr: "←", hellip: "…" })[e]);

// ---------------------------------------------------------------- timers and window
const real = require("timers");
const internal = () => /node:internal/.test(new Error().stack.split("\n")[3] || "");
function createTimers() {
  let now = 0, seq = 0; const jobs = new Map();
  const add = (fn, ms, repeat, args) => { const id = ++seq; jobs.set(id, { fn, at: now + Math.max(0, Number(ms) || 0), ms: Math.max(1, Number(ms) || 0), repeat, args }); return id; };
  return {
    // Node's own fetch (undici) keeps real timers; only page code runs on the fake clock.
    setTimeout: (fn, ms, ...args) => internal() ? real.setTimeout(fn, ms, ...args) : add(fn, ms, false, args),
    setInterval: (fn, ms, ...args) => internal() ? real.setInterval(fn, ms, ...args) : add(fn, ms, true, args),
    clearTimeout: (id) => typeof id === "object" ? real.clearTimeout(id) : jobs.delete(id),
    clearInterval: (id) => typeof id === "object" ? real.clearInterval(id) : jobs.delete(id),
    requestAnimationFrame: (fn) => add(() => fn(now), 16, false, []),
    cancelAnimationFrame: (id) => jobs.delete(id),
    requestIdleCallback: (fn) => add(() => fn({ timeRemaining: () => 50, didTimeout: false }), 1, false, []),
    cancelIdleCallback: (id) => jobs.delete(id),
    get pending() { return jobs.size; },
    // Runs every job due within ms (default: all queued, at most `limit` rounds of repeating jobs).
    async tick(ms = 0) {
      const end = now + ms;
      for (let guard = 0; guard < 10000; guard++) {
        const next = [...jobs].filter(([, j]) => j.at <= end).sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
        if (!next) break;
        const [id, j] = next;
        now = Math.max(now, j.at);
        if (j.repeat) j.at = now + j.ms; else jobs.delete(id);
        j.fn(...j.args);
        await flush();
      }
      now = Math.max(now, end);
      await flush();
    },
    async run(rounds = 50) { for (let i = 0; i < rounds && [...jobs.values()].some((j) => !j.repeat); i++) { const due = Math.min(...[...jobs.values()].filter((j) => !j.repeat).map((j) => j.at)); await this.tick(due - now); } },
    clear() { jobs.clear(); }
  };
}
async function flush(n = 8) { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); }

function memoryStorage(initial = {}) {
  const m = new Map(Object.entries(initial));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), clear: () => m.clear(), key: (i) => [...m.keys()][i] ?? null, get length() { return m.size; }, _map: m };
}

// Installs window/document/etc. as globals. Options: html (string) or index (true → desktop/ui/index.html body).
function install(opts = {}) {
  const doc = new Document();
  const timers = createTimers();
  const win = new Target();
  if (opts.index) {
    const html = fs.readFileSync(path.join(__dirname, "..", "index.html"), "utf8");
    const body = html.slice(html.indexOf("<body"), html.lastIndexOf("</body>"));
    const attrs = body.match(/^<body([^>]*)>/);
    if (attrs) for (const a of attrs[1].matchAll(/([\w-]+)(?:="([^"]*)")?/g)) doc.body._attrs.set(a[1], a[2] ?? "");
    parseHTML(doc, body.replace(/^<body[^>]*>/, ""), doc.body);
  }
  if (opts.html) parseHTML(doc, opts.html, doc.body);
  const storage = opts.storage || memoryStorage(opts.stored);
  const clipboard = { text: "", writeText: async (t) => { if (clipboard.fail) throw new Error("denied"); clipboard.text = t; }, readText: async () => { if (clipboard.fail) throw new Error("denied"); return clipboard.text; } };
  const selection = { text: "", rangeCount: 0, toString() { return this.text; }, getRangeAt: () => new Range(), removeAllRanges() { this.rangeCount = 0; }, addRange() { this.rangeCount = 1; }, collapse() {}, selectAllChildren() {} };
  const media = {};
  Object.defineProperty(media, "change", { value: (q, on) => { media[q] = on; for (const [mq, fn] of media._on || []) if (mq === q) fn({ matches: on, media: q }); } });
  Object.defineProperty(media, "_on", { writable: true, value: [] });
  Object.assign(win, {
    document: doc, localStorage: storage, sessionStorage: memoryStorage(),
    innerWidth: 1280, innerHeight: 800, devicePixelRatio: 1, scrollX: 0, scrollY: 0,
    navigator: { language: "ko-KR", languages: ["ko-KR"], clipboard, userAgent: "node", platform: "Win32", onLine: true },
    location: { href: "http://tauri.localhost/", origin: "http://tauri.localhost", protocol: "http:", host: "tauri.localhost", hostname: "tauri.localhost", pathname: "/", search: "", hash: "", reload() { win._reloaded = (win._reloaded || 0) + 1; }, assign(u) { this.href = u; }, replace(u) { this.href = u; } },
    history: { state: null, pushState() {}, replaceState() {}, back() {} },
    getSelection: () => selection,
    // media[q] is read live; media.change(q, on) flips it and runs the "change" listeners
    matchMedia: (q) => ({ get matches() { return !!media[q]; }, media: q, addEventListener(t, fn) { if (t === "change") (media._on ||= []).push([q, fn]); }, removeEventListener() {}, addListener(fn) { (media._on ||= []).push([q, fn]); }, removeListener() {} }),
    getComputedStyle: (el) => new Proxy({}, { get: (_, k) => k === "getPropertyValue" ? (p) => el.style.getPropertyValue(p) : typeof k === "string" ? (el.style[k] || "") : undefined }),
    open: (...a) => { (win.opened ||= []).push(a); return null; },
    scrollTo() {}, scrollBy() {}, print() {}, focus() {}, blur() {}, close() { win.closed = true; },
    alert: (m) => { (win.alerts ||= []).push(m); }, confirm: () => true, prompt: () => null,
    CSS: { escape: (s) => String(s).replace(/["\\]/g, "\\$&"), supports: () => true },
    Event, CustomEvent, KeyboardEvent: Event, MouseEvent: Event, PointerEvent: Event, FocusEvent: Event, InputEvent: Event, WheelEvent: Event,
    Node, Element, HTMLElement: Element, HTMLInputElement: tagClass("input"), HTMLTextAreaElement: tagClass("textarea"), HTMLSelectElement: tagClass("select"), HTMLButtonElement: tagClass("button"), HTMLDialogElement: tagClass("dialog"), SVGElement: Element, Text, DocumentFragment: Fragment, Document,
    MutationObserver,
    Option: function Option(label = "", value, defaultSelected, selected) { const o = doc.createElement("option"); o.textContent = label; if (value !== undefined) o.value = value; if (defaultSelected) o.setAttribute("selected", ""); if (selected || defaultSelected) o.selected = true; return o; },
    Image: function Image() { return doc.createElement("img"); },
    ResizeObserver: class { constructor(fn) { this.fn = fn; (win._resizeObservers ||= []).push(this); } observe(t) { (this.targets ||= []).push(t); } unobserve() {} disconnect() { this.targets = []; } },
    IntersectionObserver: class { constructor(fn) { this.fn = fn; (win._intersectionObservers ||= []).push(this); } observe(t) { (this.targets ||= []).push(t); } unobserve() {} disconnect() { this.targets = []; } },
    ...timers,
    timers, media, flush, performance: globalThis.performance,
    URL, URLSearchParams, Blob: globalThis.Blob, File: globalThis.File, FormData: globalThis.FormData, AbortController, TextEncoder, TextDecoder, structuredClone, queueMicrotask,
    fetch: opts.fetch || (async () => { throw new Error("no network in tests"); })
  });
  win.window = win; win.self = win; win.top = win; win.parent = win; win.globalThis = win;
  const names = ["window", "self", "document", "localStorage", "sessionStorage", "innerWidth", "innerHeight", "navigator", "location", "history", "getSelection", "matchMedia", "getComputedStyle", "CSS",
    "Event", "CustomEvent", "KeyboardEvent", "MouseEvent", "PointerEvent", "FocusEvent", "InputEvent", "WheelEvent", "Node", "Element", "HTMLElement", "HTMLInputElement", "HTMLTextAreaElement", "HTMLSelectElement", "HTMLButtonElement", "HTMLDialogElement", "SVGElement", "Text", "DocumentFragment", "Document",
    "MutationObserver", "Option", "Image", "ResizeObserver", "IntersectionObserver", "setTimeout", "setInterval", "clearTimeout", "clearInterval", "requestAnimationFrame", "cancelAnimationFrame", "requestIdleCallback", "cancelIdleCallback", "fetch", "alert", "confirm", "prompt", "open", "scrollTo", "devicePixelRatio"];
  for (const n of names) {
    Object.defineProperty(globalThis, n, { configurable: true, enumerable: true, get: () => win[n], set: (v) => { win[n] = v; } });
  }
  for (const n of ["addEventListener", "removeEventListener", "dispatchEvent"]) globalThis[n] = (...a) => win[n](...a);
  return { window: win, document: doc, timers, storage, clipboard, selection, media };
}

// Helpers for tests
const $ = (s, root = globalThis.document) => root.querySelector(s);
const $$ = (s, root = globalThis.document) => root.querySelectorAll(s);
function fire(el, type, init = {}) {
  const e = new Event(type, { bubbles: true, cancelable: true, ...init });
  el.dispatchEvent(e);
  return e;
}
const key = (el, k, init = {}) => fire(el, "keydown", { key: k, ...init });
function type(el, value) { el.value = value; fire(el, "input"); fire(el, "change"); }
const text = (el) => (el ? el.textContent.replace(/\s+/g, " ").trim() : "");

module.exports = { install, parseHTML, memoryStorage, Event, CustomEvent, Element, Node, Text, Document, MutationObserver, flush, fire, key, type, text, $, $$ };
