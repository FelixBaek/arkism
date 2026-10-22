// A small DOM for the crew and team screen tests (crew-*.test.js, team.test.js): elements, events, forms, dialogs,
// selects and the selectors those screens use. It runs on globalThis, the way the browser scripts expect window.
"use strict";

const SVG_NS = "http://www.w3.org/2000/svg";
const kebab = (s) => s.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);
const camel = (s) => s.replace(/-([a-z])/g, (_, c) => c.toUpperCase());

class Event {
  constructor(type, init = {}) {
    Object.assign(this, init);
    this.type = type;
    this.bubbles = !!init.bubbles;
    this.cancelable = init.cancelable !== false;
    this.defaultPrevented = false;
    this.target = null;
    this.currentTarget = null;
    this._stop = false;
    this._stopNow = false;
  }
  preventDefault() { if (this.cancelable) this.defaultPrevented = true; }
  stopPropagation() { this._stop = true; }
  stopImmediatePropagation() { this._stop = this._stopNow = true; }
  composedPath() { return this._path || []; }
}
class CustomEvent extends Event { constructor(type, init = {}) { super(type, init); this.detail = init.detail; } }

const errors = [];

class EventTarget {
  constructor() { this._ls = []; }
  addEventListener(type, fn, opts) {
    if (!fn) return;
    const capture = typeof opts === "boolean" ? opts : !!opts?.capture;
    if (this._ls.some((l) => l.type === type && l.fn === fn && l.capture === capture)) return;
    this._ls.push({ type, fn, capture, once: !!opts?.once });
  }
  removeEventListener(type, fn, opts) {
    const capture = typeof opts === "boolean" ? opts : !!opts?.capture;
    this._ls = this._ls.filter((l) => !(l.type === type && l.fn === fn && l.capture === capture));
  }
  _fire(e, phase) {
    for (const l of this._ls.slice()) {
      if (l.type !== e.type) continue;
      if (phase === "capture" && !l.capture) continue;
      if (phase === "bubble" && l.capture) continue;
      if (l.once) this.removeEventListener(l.type, l.fn, l.capture);
      e.currentTarget = this;
      try {
        const r = typeof l.fn === "function" ? l.fn.call(this, e) : l.fn.handleEvent(e);
        if (r && typeof r.catch === "function") r.catch((err) => errors.push(err));
      } catch (err) { errors.push(err); }
      if (e._stopNow) return;
    }
  }
  dispatchEvent(e) {
    e.target = this;
    const path = [];
    for (let n = this; n; n = n.parentNode) path.push(n);
    if (path[path.length - 1] === globalThis.document) path.push(globalThis.window);
    e._path = path;
    for (let i = path.length - 1; i > 0 && !e._stop; i--) path[i]._fire(e, "capture");
    if (!e._stop) this._fire(e, "target");
    if (e.bubbles) for (let i = 1; i < path.length && !e._stop; i++) path[i]._fire(e, "bubble");
    e.currentTarget = null;
    return !e.defaultPrevented;
  }
}

class Node extends EventTarget {
  constructor(doc) { super(); this.ownerDocument = doc; this.parentNode = null; this.childNodes = []; }
  get parentElement() { return this.parentNode instanceof Element ? this.parentNode : null; }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes[this.childNodes.length - 1] || null; }
  get nextSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) + 1] || null : null; }
  get previousSibling() { const p = this.parentNode; return p ? p.childNodes[p.childNodes.indexOf(this) - 1] || null : null; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === globalThis.document; }
  contains(o) { for (let n = o; n; n = n.parentNode) if (n === this) return true; return false; }
  _adopt(nodes) {
    const out = [];
    for (const x of nodes) {
      if (x == null || x === false) continue;
      if (x instanceof DocumentFragment) { out.push(...x.childNodes.splice(0)); continue; }
      out.push(x instanceof Node ? x : new Text(String(x)));
    }
    for (const n of out) if (n.parentNode) n.parentNode._drop(n);
    return out;
  }
  _drop(n) { const i = this.childNodes.indexOf(n); if (i >= 0) this.childNodes.splice(i, 1); n.parentNode = null; }
  _insert(nodes, at) {
    const list = this._adopt(nodes);
    if (at < 0 || at > this.childNodes.length) at = this.childNodes.length;
    this.childNodes.splice(at, 0, ...list);
    for (const n of list) n.parentNode = this;
  }
  append(...nodes) { this._insert(nodes, this.childNodes.length); }
  prepend(...nodes) { this._insert(nodes, 0); }
  appendChild(n) { this.append(n); return n; }
  removeChild(n) { this._drop(n); return n; }
  insertBefore(n, ref) { if (!ref) this.append(n); else { const list = this._adopt([n]); const i = this.childNodes.indexOf(ref); this.childNodes.splice(i, 0, ...list); list.forEach((x) => { x.parentNode = this; }); } return n; }
  replaceChild(n, old) { this.insertBefore(n, old); this._drop(old); return old; }
  replaceChildren(...nodes) { for (const c of this.childNodes.splice(0)) c.parentNode = null; this.append(...nodes); }
  remove() { if (this.parentNode) this.parentNode._drop(this); }
  before(...nodes) { const p = this.parentNode; if (!p) return; const list = p._adopt(nodes.filter((x) => x !== this)); const i = p.childNodes.indexOf(this); p.childNodes.splice(i, 0, ...list); list.forEach((x) => { x.parentNode = p; }); }
  after(...nodes) { const p = this.parentNode; if (!p) return; const list = p._adopt(nodes.filter((x) => x !== this)); const i = p.childNodes.indexOf(this); p.childNodes.splice(i + 1, 0, ...list); list.forEach((x) => { x.parentNode = p; }); }
  replaceWith(...nodes) { const p = this.parentNode; if (!p) return; if (nodes.includes(this)) return; this.after(...nodes); this.remove(); }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) { this.replaceChildren(); if (v !== "" && v != null) this.append(String(v)); }
}

class Text extends Node {
  constructor(data) { super(globalThis.document); this.nodeType = 3; this.data = data; }
  get textContent() { return this.data; }
  set textContent(v) { this.data = String(v); }
  get nodeValue() { return this.data; }
  cloneNode() { return new Text(this.data); }
}

class DocumentFragment extends Node {
  constructor() { super(globalThis.document); this.nodeType = 11; }
  get children() { return this.childNodes.filter((c) => c instanceof Element); }
  querySelector(s) { return query(this, s, true)[0] || null; }
  querySelectorAll(s) { return query(this, s, false); }
}

class ClassList {
  constructor(el) { this.el = el; }
  _get() { return (this.el.getAttribute("class") || "").split(/\s+/).filter(Boolean); }
  _set(a) { this.el.setAttribute("class", a.join(" ")); }
  contains(c) { return this._get().includes(c); }
  add(...cs) { const a = this._get(); for (const c of cs) if (!a.includes(c)) a.push(c); this._set(a); }
  remove(...cs) { this._set(this._get().filter((c) => !cs.includes(c))); }
  toggle(c, force) { const has = this.contains(c); const on = force === undefined ? !has : !!force; if (on && !has) this.add(c); if (!on && has) this.remove(c); return on; }
  replace(a, b) { if (!this.contains(a)) return false; this.remove(a); this.add(b); return true; }
  get length() { return this._get().length; }
  get value() { return this._get().join(" "); }
  [Symbol.iterator]() { return this._get()[Symbol.iterator](); }
}

function makeStyle() {
  const props = {};
  return new Proxy(props, {
    get(t, k) {
      if (k === "setProperty") return (n, v) => { t[n] = String(v); };
      if (k === "removeProperty") return (n) => { const v = t[n]; delete t[n]; return v || ""; };
      if (k === "getPropertyValue") return (n) => t[n] || "";
      if (k === "cssText") return Object.entries(t).map(([a, b]) => `${a}: ${b}`).join("; ");
      return t[k] ?? "";
    },
    set(t, k, v) { if (k === "cssText") { for (const x of Object.keys(t)) delete t[x]; } else t[k] = v; return true; }
  });
}

const RECT = { left: 10, top: 10, right: 110, bottom: 40, width: 100, height: 30, x: 10, y: 10 };

class Element extends Node {
  constructor(doc, tag, ns) {
    super(doc);
    this.nodeType = 1;
    this.namespaceURI = ns || "http://www.w3.org/1999/xhtml";
    this.localName = ns === SVG_NS ? tag : tag.toLowerCase();
    this.tagName = ns === SVG_NS ? tag : tag.toUpperCase();
    this._attrs = new Map();
    this.classList = new ClassList(this);
    this.style = makeStyle();
    this.scrollTop = 0; this.scrollLeft = 0;
    this.dataset = new Proxy({}, {
      get: (_, k) => (typeof k === "string" ? this.getAttribute(`data-${kebab(k)}`) ?? undefined : undefined),
      set: (_, k, v) => { this.setAttribute(`data-${kebab(k)}`, v); return true; },
      deleteProperty: (_, k) => { this.removeAttribute(`data-${kebab(k)}`); return true; },
      has: (_, k) => this.hasAttribute(`data-${kebab(k)}`),
      ownKeys: () => [...this._attrs.keys()].filter((a) => a.startsWith("data-")).map((a) => camel(a.slice(5))),
      getOwnPropertyDescriptor: (_, k) => (this.hasAttribute(`data-${kebab(k)}`) ? { enumerable: true, configurable: true, value: this.getAttribute(`data-${kebab(k)}`) } : undefined)
    });
  }
  get nodeName() { return this.tagName; }
  getAttribute(n) { return this._attrs.has(n) ? this._attrs.get(n) : null; }
  setAttribute(n, v) { this._attrs.set(n, String(v)); }
  removeAttribute(n) { this._attrs.delete(n); }
  hasAttribute(n) { return this._attrs.has(n); }
  toggleAttribute(n, force) { const on = force === undefined ? !this.hasAttribute(n) : !!force; if (on) this.setAttribute(n, ""); else this.removeAttribute(n); return on; }
  getAttributeNames() { return [...this._attrs.keys()]; }
  get attributes() { return [...this._attrs].map(([name, value]) => ({ name, value })); }
  get className() { return this.getAttribute("class") || ""; }
  set className(v) { this.setAttribute("class", v); }
  get id() { return this.getAttribute("id") || ""; }
  set id(v) { this.setAttribute("id", v); }
  get children() { return this.childNodes.filter((c) => c instanceof Element); }
  get firstElementChild() { return this.children[0] || null; }
  get lastElementChild() { const c = this.children; return c[c.length - 1] || null; }
  get childElementCount() { return this.children.length; }
  get nextElementSibling() { const p = this.parentNode; if (!p) return null; const s = p.childNodes; for (let i = s.indexOf(this) + 1; i < s.length; i++) if (s[i] instanceof Element) return s[i]; return null; }
  get previousElementSibling() { const p = this.parentNode; if (!p) return null; const s = p.childNodes; for (let i = s.indexOf(this) - 1; i >= 0; i--) if (s[i] instanceof Element) return s[i]; return null; }
  get innerText() { return this.textContent; }
  set innerText(v) { this.textContent = v; }
  get innerHTML() { return this.textContent; }
  set innerHTML(v) { this.textContent = String(v).replace(/<[^>]*>/g, ""); }
  get offsetWidth() { return 100; }
  get offsetHeight() { return 30; }
  get offsetLeft() { return 10; }
  get offsetTop() { return 10; }
  get offsetParent() { return this.parentElement; }
  get clientWidth() { return 100; }
  get clientHeight() { return 30; }
  get scrollHeight() { return 30; }
  get scrollWidth() { return 100; }
  getBoundingClientRect() { return { ...RECT }; }
  getClientRects() { return [this.getBoundingClientRect()]; }
  scrollIntoView() {}
  scrollTo() {}
  scrollBy() {}
  setPointerCapture() {}
  releasePointerCapture() {}
  hasPointerCapture() { return false; }
  animate() { return { finished: Promise.resolve(), cancel() {}, onfinish: null }; }
  getContext() { return { drawImage() {}, fillRect() {}, clearRect() {} }; }
  toDataURL() { return this._dataURL || "data:image/jpeg;base64,AAAA"; }
  getTotalLength() { return 100; }
  getPointAtLength() { return { x: 0, y: 0 }; }
  matches(s) { return parse(s).some((sel) => matchComplex(this, sel, null)); }
  closest(s) { for (let n = this; n instanceof Element; n = n.parentNode) if (n.matches(s)) return n; return null; }
  querySelector(s) { return query(this, s, true)[0] || null; }
  querySelectorAll(s) { return query(this, s, false); }
  getElementsByTagName(t) { return query(this, t, false); }
  focus() {
    const doc = globalThis.document, prev = doc.activeElement;
    if (prev === this) return;
    doc._active = this;
    if (prev && prev !== doc.body) { prev.dispatchEvent(new Event("blur")); prev.dispatchEvent(new Event("focusout", { bubbles: true })); }
    if (doc._active !== this) return;
    this.dispatchEvent(new Event("focus"));
    this.dispatchEvent(new Event("focusin", { bubbles: true }));
  }
  blur() {
    const doc = globalThis.document;
    if (doc.activeElement !== this) return;
    doc._active = null;
    this.dispatchEvent(new Event("blur"));
    this.dispatchEvent(new Event("focusout", { bubbles: true }));
  }
  click() {
    if (this.disabled) return;
    const e = new Event("click", { bubbles: true, button: 0, clientX: 20, clientY: 20 });
    const box = this.localName === "input" && (this.type === "checkbox" || this.type === "radio");
    const was = this.checked;
    if (box) this.checked = this.type === "radio" ? true : !was;
    this.dispatchEvent(e);
    if (e.defaultPrevented) { if (box) this.checked = was; return; }
    if (box) { this.dispatchEvent(new Event("input", { bubbles: true })); this.dispatchEvent(new Event("change", { bubbles: true })); return; }
    if (this.localName === "label") {
      const c = this.querySelector("input, textarea, select");
      if (c && !c.contains(e.target) && e.target !== c) c.click();
      return;
    }
    if ((this.localName === "button" && this.type === "submit") || (this.localName === "input" && this.type === "submit")) {
      const f = this.closest("form");
      if (f) f.requestSubmit(this);
    }
  }
  cloneNode(deep) {
    const c = globalThis.document.createElementNS(this.namespaceURI, this.localName);
    for (const [k, v] of this._attrs) c._attrs.set(k, v);
    for (const k of ["value", "checked"]) if (this["_" + k] !== undefined) c["_" + k] = this["_" + k];
    if (deep) for (const n of this.childNodes) c.append(n.cloneNode(true));
    return c;
  }
  // forms
  requestSubmit(submitter) {
    const e = new Event("submit", { bubbles: true, cancelable: true, submitter: submitter || null });
    this.dispatchEvent(e);
    if (!e.defaultPrevented && (this.getAttribute("method") || "").toLowerCase() === "dialog") this.closest("dialog")?.close(submitter?.value);
  }
  submit() { this.requestSubmit(); }
  reset() {}
  get elements() { return this.querySelectorAll("input, select, textarea, button"); }
  get form() { return this.closest("form"); }
  checkValidity() { return true; }
  reportValidity() { return true; }
  setCustomValidity(m) { this.validationMessage = m; }
  get validity() { return { valid: true }; }
  select() {}
  setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; }
  showPicker() {}
  // inputs and selects
  get value() {
    if (this.localName === "select") { const o = this.options.find((x) => x.selected) || this.options[0]; return o ? o.value : ""; }
    if (this.localName === "option") return this.getAttribute("value") ?? this.textContent;
    if (this._value !== undefined) return this._value;
    return this.localName === "textarea" ? this.textContent : this.getAttribute("value") ?? "";
  }
  set value(v) {
    if (this.localName === "select") { for (const o of this.options) o._selected = o.value === String(v); return; }
    if (this.localName === "option") { this.setAttribute("value", v); return; }
    this._value = v == null ? "" : String(v);
  }
  get valueAsNumber() { return Number(this.value); }
  get options() { return this.localName === "select" ? query(this, "option", false) : undefined; }
  get selectedIndex() { return this.options.findIndex((o) => o.selected); }
  set selectedIndex(i) { this.options.forEach((o, j) => { o._selected = i === j; }); }
  get selectedOptions() { return this.options.filter((o) => o.selected); }
  get selected() {
    if (this._selected !== undefined) return this._selected;
    const s = this.closest("select");
    if (!s) return this.hasAttribute("selected");
    const opts = s.options;
    if (opts.some((o) => o._selected)) return false;
    const marked = opts.find((o) => o.hasAttribute("selected"));
    return marked ? marked === this : opts[0] === this;
  }
  set selected(v) { const s = this.closest("select"); if (v && s) for (const o of s.options) o._selected = false; this._selected = !!v; }
  get label() { return this._label ?? this.getAttribute("label") ?? this.textContent; }
  set label(v) { this._label = v; }
  get checked() { return !!this._checked; }
  set checked(v) { this._checked = !!v; }
  get open() { return this.hasAttribute("open"); }
  set open(v) { this.toggleAttribute("open", !!v); }
  // dialogs
  showModal() { this.open = true; }
  show() { this.open = true; }
  close(rv) {
    if (!this.open) return;
    if (rv !== undefined) this.returnValue = rv;
    this.open = false;
    this.dispatchEvent(new Event("close"));
  }
  get isContentEditable() { return this.getAttribute("contenteditable") === "true"; }
}
// Properties that read and write an attribute, as in the browser.
for (const [prop, attr] of [["type", "type"], ["name", "name"], ["title", "title"], ["placeholder", "placeholder"], ["href", "href"], ["src", "src"], ["alt", "alt"],
  ["htmlFor", "for"], ["accept", "accept"], ["pattern", "pattern"], ["role", "role"], ["min", "min"], ["max", "max"], ["step", "step"], ["list", "list"], ["autocomplete", "autocomplete"],
  ["lang", "lang"], ["dir", "dir"], ["method", "method"], ["target", "target"], ["rel", "rel"], ["download", "download"], ["loading", "loading"], ["inputMode", "inputmode"], ["enterKeyHint", "enterkeyhint"], ["contentEditable", "contenteditable"]]) {
  Object.defineProperty(Element.prototype, prop, {
    get() {
      const v = this.getAttribute(attr);
      if (prop === "type" && v == null) return this.localName === "button" ? "submit" : this.localName === "input" ? "text" : this.localName === "select" ? "select-one" : "";
      return v ?? "";
    },
    set(v) { this.setAttribute(attr, v); },
    configurable: true
  });
}
for (const [prop, attr] of [["hidden", "hidden"], ["disabled", "disabled"], ["required", "required"], ["readOnly", "readonly"], ["multiple", "multiple"], ["inert", "inert"], ["draggable", "draggable"], ["autofocus", "autofocus"], ["spellcheck", "spellcheck"]]) {
  Object.defineProperty(Element.prototype, prop, { get() { return this.hasAttribute(attr); }, set(v) { this.toggleAttribute(attr, !!v); }, configurable: true });
}
for (const [prop, attr] of [["tabIndex", "tabindex"], ["maxLength", "maxlength"], ["minLength", "minlength"], ["rows", "rows"], ["cols", "cols"], ["size", "size"], ["width", "width"], ["height", "height"]]) {
  Object.defineProperty(Element.prototype, prop, { get() { const v = this.getAttribute(attr); return v == null ? (prop === "tabIndex" ? -1 : 0) : Number(v); }, set(v) { this.setAttribute(attr, v); }, configurable: true });
}

// ---------------------------------------------------------------- selectors
function splitTop(s, sep) {
  const out = []; let depth = 0, q = null, cur = "";
  for (const ch of s) {
    if (q) { cur += ch; if (ch === q) q = null; continue; }
    if (ch === '"' || ch === "'") { q = ch; cur += ch; continue; }
    if (ch === "(" || ch === "[") depth++;
    if (ch === ")" || ch === "]") depth--;
    if (ch === sep && depth === 0) { out.push(cur); cur = ""; continue; }
    cur += ch;
  }
  out.push(cur);
  return out;
}
const cache = new Map();
function parse(sel) {
  if (cache.has(sel)) return cache.get(sel);
  const groups = splitTop(sel, ",").map((g) => g.trim()).filter(Boolean).map(parseComplex);
  cache.set(sel, groups);
  return groups;
}
function parseComplex(s) {
  // tokens: compound selectors joined by combinators (" ", ">", "+", "~")
  const parts = []; let cur = "", depth = 0, q = null, comb = " ";
  const flush = () => { if (cur.trim()) { parts.push({ comb, c: parseCompound(cur.trim()) }); comb = " "; } cur = ""; };
  for (const ch of s) {
    if (q) { cur += ch; if (ch === q) q = null; continue; }
    if (ch === '"' || ch === "'") { q = ch; cur += ch; continue; }
    if (ch === "(" || ch === "[") depth++;
    if (ch === ")" || ch === "]") depth--;
    if (depth === 0 && (ch === ">" || ch === "+" || ch === "~")) { flush(); comb = ch; continue; }
    if (depth === 0 && /\s/.test(ch)) { if (cur.trim()) { flush(); } continue; }
    cur += ch;
  }
  flush();
  return parts;
}
function parseCompound(s) {
  const c = { tag: null, ids: [], classes: [], attrs: [], pseudos: [] };
  const re = /^(\*|[a-zA-Z][\w-]*)|#([\w-]+)|\.([\w-]+)|\[\s*([\w-]+)\s*(?:([~^$*|]?=)\s*(?:"([^"]*)"|'([^']*)'|([^\]\s]*)))?\s*\]|:([\w-]+)(?:\(((?:[^()]|\([^()]*\))*)\))?/g;
  let m, pos = 0;
  while ((m = re.exec(s))) {
    if (m.index !== pos) throw new Error(`selector not supported: ${s}`);
    pos = re.lastIndex;
    if (m[1]) c.tag = m[1] === "*" ? null : m[1].toLowerCase();
    else if (m[2]) c.ids.push(m[2]);
    else if (m[3]) c.classes.push(m[3]);
    else if (m[4]) c.attrs.push({ name: m[4], op: m[5], val: m[6] ?? m[7] ?? m[8] });
    else if (m[9]) c.pseudos.push({ name: m[9], arg: m[10] });
  }
  if (pos !== s.length) throw new Error(`selector not supported: ${s}`);
  return c;
}
function matchCompound(el, c, scope) {
  if (!(el instanceof Element)) return false;
  if (c.tag && el.localName.toLowerCase() !== c.tag) return false;
  for (const id of c.ids) if (el.id !== id) return false;
  for (const k of c.classes) if (!el.classList.contains(k)) return false;
  for (const a of c.attrs) {
    const v = el.getAttribute(a.name);
    if (v == null) return false;
    if (!a.op) continue;
    if (a.op === "=" && v !== a.val) return false;
    if (a.op === "^=" && !v.startsWith(a.val)) return false;
    if (a.op === "$=" && !v.endsWith(a.val)) return false;
    if (a.op === "*=" && !v.includes(a.val)) return false;
    if (a.op === "~=" && !v.split(/\s+/).includes(a.val)) return false;
  }
  for (const p of c.pseudos) {
    if (p.name === "scope") { if (el !== scope) return false; continue; }
    if (p.name === "checked") { if (!(el.checked || (el.localName === "option" && el.selected))) return false; continue; }
    if (p.name === "disabled") { if (!el.disabled) return false; continue; }
    if (p.name === "enabled") { if (el.disabled) return false; continue; }
    if (p.name === "focus" || p.name === "focus-visible" || p.name === "focus-within") { if (globalThis.document.activeElement !== el) return false; continue; }
    if (p.name === "hover" || p.name === "active") return false;
    if (p.name === "last-child") { if (el.parentNode?.lastElementChild !== el && el.parentNode?.children?.slice(-1)[0] !== el) return false; continue; }
    if (p.name === "first-child") { if ((el.parentNode?.children || [])[0] !== el) return false; continue; }
    if (p.name === "not") { if (parse(p.arg).some((sel) => matchComplex(el, sel, scope))) return false; continue; }
    if (p.name === "is" || p.name === "where") { if (!parse(p.arg).some((sel) => matchComplex(el, sel, scope))) return false; continue; }
    if (p.name === "empty") { if (el.childNodes.length) return false; continue; }
    throw new Error(`pseudo not supported: ${p.name}`);
  }
  return true;
}
function matchComplex(el, parts, scope) {
  const walk = (node, i) => {
    if (!matchCompound(node, parts[i].c, scope)) return false;
    if (i === 0) return true;
    const comb = parts[i].comb;
    if (comb === ">") return node.parentNode instanceof Element && walk(node.parentNode, i - 1);
    if (comb === " ") { for (let p = node.parentNode; p instanceof Element; p = p.parentNode) if (walk(p, i - 1)) return true; return false; }
    if (comb === "+") { const s = node.previousElementSibling; return !!s && walk(s, i - 1); }
    if (comb === "~") { for (let s = node.previousElementSibling; s; s = s.previousElementSibling) if (walk(s, i - 1)) return true; return false; }
    return false;
  };
  return walk(el, parts.length - 1);
}
function query(root, sel, first) {
  const groups = parse(sel), out = [];
  const visit = (n) => {
    for (const c of n.childNodes) {
      if (!(c instanceof Element)) continue;
      if (groups.some((g) => matchComplex(c, g, root))) { out.push(c); if (first) return true; }
      if (visit(c)) return true;
    }
    return false;
  };
  visit(root);
  return out;
}

class Document extends Node {
  constructor() {
    super(null);
    this.nodeType = 9;
    this._active = null;
  }
  _init() {
    this.documentElement = this.createElement("html");
    this.head = this.createElement("head");
    this.body = this.createElement("body");
    this.documentElement.append(this.head, this.body);
    this.append(this.documentElement);
    this.scrollingElement = this.documentElement;
  }
  get activeElement() { return this._active && this._active.isConnected ? this._active : this.body; }
  createElement(tag) { return new Element(this, tag); }
  createElementNS(ns, tag) { return new Element(this, tag, ns === SVG_NS ? SVG_NS : undefined); }
  createTextNode(t) { return new Text(String(t)); }
  createDocumentFragment() { return new DocumentFragment(); }
  getElementById(id) { return query(this, `#${id}`, true)[0] || null; }
  querySelector(s) { return query(this, s, true)[0] || null; }
  querySelectorAll(s) { return query(this, s, false); }
  elementFromPoint() { return this._pointTarget || null; }
  get hidden() { return false; }
  get visibilityState() { return "visible"; }
}

// ---------------------------------------------------------------- timers: a queue the test runs by hand
function fakeTimers() {
  let seq = 0, now = 0;
  const q = new Map();
  const add = (fn, ms, every) => { const id = ++seq; q.set(id, { fn, at: now + (Number(ms) || 0), every: every ? Math.max(1, Number(ms) || 1) : 0 }); return id; };
  const api = {
    setTimeout: (fn, ms, ...args) => add(() => fn(...args), ms, false),
    clearTimeout: (id) => { q.delete(id); },
    setInterval: (fn, ms, ...args) => add(() => fn(...args), ms, true),
    clearInterval: (id) => { q.delete(id); },
    requestAnimationFrame: (fn) => add(() => fn(now), 16, false),
    cancelAnimationFrame: (id) => { q.delete(id); },
    pending: () => q.size,
    // Runs every timer due within ms (default: all, but intervals only once each).
    tick(ms = Infinity, limit = 500) {
      const end = now + ms;
      for (let n = 0; n < limit; n++) {
        let best = null;
        for (const [id, t] of q) if (t.at <= end && (!best || t.at < best[1].at)) best = [id, t];
        if (!best) break;
        const [id, t] = best;
        now = Math.max(now, t.at);
        if (t.every && ms === Infinity) q.delete(id); else if (t.every) t.at += t.every; else q.delete(id);
        try { t.fn(); } catch (err) { errors.push(err); }
      }
      if (ms !== Infinity) now = end;
    },
    clear() { q.clear(); }
  };
  return api;
}

const realSetTimeout = setTimeout;
const flush = async (n = 20) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };

// Installs the DOM on globalThis. Returns helpers for the tests.
function install() {
  const g = globalThis;
  const doc = new Document();
  g.document = doc;
  doc._init();
  const timers = fakeTimers();
  const win = g;
  win._ls = []; // each boot starts with no window listeners
  for (const k of ["addEventListener", "removeEventListener", "_fire"]) win[k] = EventTarget.prototype[k].bind(win);
  win.dispatchEvent = (e) => { e.target = win; e._path = [win]; win._fire(e, "capture"); win._fire(e, "target"); return !e.defaultPrevented; };
  Object.assign(g, {
    window: g, Event, CustomEvent, Element, Node, Text, HTMLElement: Element, SVGElement: Element, HTMLInputElement: Element, HTMLDialogElement: Element, DocumentFragment, EventTarget,
    KeyboardEvent: Event, MouseEvent: Event, PointerEvent: Event, InputEvent: Event, FocusEvent: Event,
    setTimeout: timers.setTimeout, clearTimeout: timers.clearTimeout, setInterval: timers.setInterval, clearInterval: timers.clearInterval,
    requestAnimationFrame: timers.requestAnimationFrame, cancelAnimationFrame: timers.cancelAnimationFrame,
    innerWidth: 1440, innerHeight: 900, devicePixelRatio: 1,
    CSS: { escape: (s) => String(s).replace(/["\\]/g, "\\$&") },
    matchMedia: (q) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }),
    getComputedStyle: (el) => el.style,
    location: { search: "", href: "http://localhost/", hash: "", pathname: "/", reload() {} },
    localStorage: memoryStorage(), sessionStorage: memoryStorage(),
    ResizeObserver: class { observe() {} unobserve() {} disconnect() {} },
    IntersectionObserver: class { observe() {} unobserve() {} disconnect() {} },
    MutationObserver: class { observe() {} disconnect() {} takeRecords() { return []; } }
  });
  const clip = { text: "", fail: false, writeText(t) { if (clip.fail) return Promise.reject(new Error("denied")); clip.text = t; return Promise.resolve(); } };
  Object.defineProperty(g, "navigator", { value: { clipboard: clip, language: "ko-KR", userAgent: "node", platform: "Linux" }, configurable: true, writable: true });
  g.Image = class { constructor() { this.width = 2000; this.height = 1000; } set src(v) { this._src = v; const ok = !String(v).includes("bad"); setImmediate(() => (ok ? this.onload?.() : this.onerror?.())); } get src() { return this._src; } };
  g.URL.createObjectURL = (f) => `blob:${f?.name || "file"}`;
  g.URL.revokeObjectURL = () => {};
  errors.length = 0;
  return { doc, timers, clip, errors };
}
function memoryStorage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), clear: () => m.clear(), key: (i) => [...m.keys()][i] ?? null, get length() { return m.size; } };
}

// Fires events on elements.
const fire = (node, type, init = {}) => { const e = new Event(type, { bubbles: true, cancelable: true, ...init }); node.dispatchEvent(e); return e; };
const key = (node, k, init = {}) => fire(node, "keydown", { key: k, code: init.code || (k === " " ? "Space" : k), ...init });
const type = (node, text) => { node.value = text; fire(node, "input"); fire(node, "change"); };
const texts = (root) => (root?.textContent || "").replace(/\s+/g, " ").trim();
const byText = (root, sel, text) => (root.querySelectorAll(sel)).find((n) => (text instanceof RegExp ? text.test(n.textContent) : n.textContent.trim() === text || n.textContent.includes(text)));

// Clicks, types into and presses keys on every element that listens, a few rounds, so each handler runs once.
// skip(node) keeps away from controls the test drives itself.
async function crawl(root, { rounds = 3, cap = 400, skip = () => false, settle = async () => {} } = {}) {
  const seen = new Set();
  for (let r = 0; r < rounds; r++) {
    const all = [root, ...root.querySelectorAll("*")].filter((n) => n._ls.length && !skip(n));
    let done = 0;
    for (const n of all) {
      if (done >= cap) break;
      if (n !== root && !n.isConnected) continue; // a redraw took it away; a person could not click it
      const sig = `${n.localName}.${n.className}|${n.getAttribute("aria-label") || ""}|${n.textContent.slice(0, 40)}|${n._ls.map((l) => l.type).join(",")}`;
      if (seen.has(sig)) continue;
      seen.add(sig);
      done++;
      const types = new Set(n._ls.map((l) => l.type));
      try {
        if (n.localName === "input" || n.localName === "textarea") {
          if (types.has("input") || types.has("change")) type(n, n.type === "number" ? "3" : n.type === "checkbox" ? n.value : "테스트 값");
          if (types.has("keydown")) { key(n, "Enter"); key(n, "Escape"); }
          if (types.has("blur")) n.blur();
          if (types.has("click")) n.click();
        } else if (n.localName === "select") {
          const o = n.options[n.options.length - 1];
          if (o) n.value = o.value;
          fire(n, "change"); fire(n, "input");
        } else {
          if (types.has("click")) n.click();
          if (types.has("keydown")) { key(n, "Enter"); key(n, "ArrowDown"); key(n, "Escape"); }
          for (const t of ["pointerdown", "pointerenter", "pointerleave", "mouseenter", "mouseleave", "dblclick", "contextmenu", "dragstart", "focus"]) if (types.has(t)) fire(n, t, { clientX: 30, clientY: 30, pointerId: 1, button: 0, dataTransfer: { setData() {}, getData() { return ""; }, effectAllowed: "", dropEffect: "" } });
        }
      } catch (err) { errors.push(err); }
      await settle();
    }
  }
}

module.exports = { install, fire, key, type, texts, byText, crawl, flush, errors, Event, CustomEvent, realSetTimeout };
