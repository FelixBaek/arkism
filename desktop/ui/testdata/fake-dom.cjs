// A small DOM stand-in for UI behavior tests (node --test desktop/ui/*.test.js): enough of Element, document and
// events for the memory and tool views to build, query and click their nodes. No layout; selectors are simple.
"use strict";

class ClassList {
  constructor(node) { this.node = node; }
  get list() { return this.node.className.split(/\s+/).filter(Boolean); }
  set(list) { this.node.className = [...new Set(list)].join(" "); }
  add(...c) { this.set([...this.list, ...c]); }
  remove(...c) { this.set(this.list.filter((x) => !c.includes(x))); }
  contains(c) { return this.list.includes(c); }
  toggle(c, force) {
    const on = force === undefined ? !this.contains(c) : !!force;
    if (on) this.add(c); else this.remove(c);
    return on;
  }
  replace(a, b) { if (!this.contains(a)) return false; this.set(this.list.map((x) => (x === a ? b : x))); return true; }
}

function parseSelector(sel) {
  // compound parts separated by descendant spaces or ">"; child marks a part joined to the previous one by ">"
  let child = false;
  return sel.trim().split(/\s*(>)\s*|\s+/).filter(Boolean).flatMap((part) => {
    if (part === ">") { child = true; return []; }
    const out = { tag: "", id: "", classes: [], attrs: [], not: [], child };
    child = false;
    const re = /(:not\(([^)]*)\))|(#[\w-]+)|(\.[\w-]+)|(\[([\w-]+)(?:([*^$~]?=)"?([^"\]]*)"?)?\])|(:[\w-]+(?:\([^)]*\))?)|([\w*-]+)/g;
    let m;
    while ((m = re.exec(part))) {
      if (m[1]) out.not.push(parseSelector(m[2])[0]);
      else if (m[3]) out.id = m[3].slice(1);
      else if (m[4]) out.classes.push(m[4].slice(1));
      else if (m[5]) out.attrs.push({ name: m[6], op: m[7] || "", value: m[8] });
      else if (m[9]) out.attrs.push({ pseudo: m[9] });
      else if (m[10] && m[10] !== "*") out.tag = m[10].toLowerCase();
    }
    return [out];
  });
}

function matchesPart(node, p, scope) {
  if (!(node instanceof Element)) return false;
  if (p.tag && node.tagName.toLowerCase() !== p.tag) return false;
  if (p.id && node.id !== p.id) return false;
  for (const c of p.classes) if (!node.classList.contains(c)) return false;
  for (const a of p.attrs) {
    if (a.pseudo) {
      if (a.pseudo === ":checked" && !node.checked) return false;
      if (a.pseudo === ":disabled" && !node.disabled) return false;
      if (a.pseudo === ":focus" && node.ownerDocument.activeElement !== node) return false;
      if (a.pseudo === ":first-child" && node.parentNode?.children[0] !== node) return false;
      if (a.pseudo === ":last-child" && node.parentNode?.children.at(-1) !== node) return false;
      if (a.pseudo === ":scope" && node !== scope) return false;
      continue;
    }
    const v = node.getAttribute(a.name);
    if (v === null) return false;
    if (a.op === "=" && v !== a.value) return false;
    if (a.op === "*=" && !v.includes(a.value)) return false;
    if (a.op === "^=" && !v.startsWith(a.value)) return false;
    if (a.op === "$=" && !v.endsWith(a.value)) return false;
    if (a.op === "~=" && !v.split(/\s+/).includes(a.value)) return false;
  }
  for (const n of p.not) if (matchesPart(node, n)) return false;
  return true;
}

function matchesFrom(node, parts, i, scope) {
  if (!matchesPart(node, parts[i], scope)) return false;
  if (i === 0) return true;
  if (parts[i].child) return matchesFrom(node.parentNode, parts, i - 1, scope);
  for (let cur = node.parentNode; cur; cur = cur.parentNode) if (matchesFrom(cur, parts, i - 1, scope)) return true;
  return false;
}

function matches(node, selector, scope = node) {
  return selector.split(",").some((one) => {
    const parts = parseSelector(one);
    return matchesFrom(node, parts, parts.length - 1, scope);
  });
}

class Node {
  constructor(doc) { this.ownerDocument = doc; this.parentNode = null; this.childNodes = []; this.listeners = {}; hideLinks(this); }
  get parentElement() { return this.parentNode instanceof Element ? this.parentNode : null; }
  get children() { return this.childNodes.filter((c) => c instanceof Element); }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes.at(-1) || null; }
  get firstElementChild() { return this.children[0] || null; }
  get lastElementChild() { return this.children.at(-1) || null; }
  get childElementCount() { return this.children.length; }
  get nextSibling() { const s = this.parentNode?.childNodes; return s ? s[s.indexOf(this) + 1] || null : null; }
  get previousSibling() { const s = this.parentNode?.childNodes; return s ? s[s.indexOf(this) - 1] || null : null; }
  get nextElementSibling() { const s = this.parentNode?.children; return s ? s[s.indexOf(this) + 1] || null : null; }
  get previousElementSibling() { const s = this.parentNode?.children; return s ? s[s.indexOf(this) - 1] || null : null; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === this.ownerDocument; }
  get textContent() { return this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) { this.replaceChildren(); if (v !== "" && v !== null && v !== undefined) this.append(String(v)); }
  wrap(c) { return c instanceof Node ? c : new Text(this.ownerDocument, String(c)); }
  insertAt(index, nodes) {
    const list = nodes.flatMap((n) => (n instanceof Fragment ? n.childNodes.splice(0) : [n])).map((n) => this.wrap(n));
    for (const n of list) { n.remove(); n.parentNode = this; }
    if (index < 0 || index > this.childNodes.length) index = this.childNodes.length;
    this.childNodes.splice(index, 0, ...list);
  }
  append(...n) { this.insertAt(this.childNodes.length, n); }
  appendChild(n) { this.append(n); return n; }
  prepend(...n) { this.insertAt(0, n); }
  insertBefore(n, ref) { this.insertAt(ref ? this.childNodes.indexOf(ref) : this.childNodes.length, [n]); return n; }
  removeChild(n) { n.remove(); return n; }
  replaceChild(n, old) { old.replaceWith(n); return old; }
  replaceChildren(...n) { for (const c of this.childNodes) c.parentNode = null; this.childNodes = []; this.append(...n); }
  remove() { if (!this.parentNode) return; const s = this.parentNode.childNodes; s.splice(s.indexOf(this), 1); this.parentNode = null; }
  before(...n) { const p = this.parentNode; if (p) p.insertAt(p.childNodes.indexOf(this), n); }
  after(...n) { const p = this.parentNode; if (p) p.insertAt(p.childNodes.indexOf(this) + 1, n); }
  replaceWith(...n) { const p = this.parentNode; if (!p) return; const i = p.childNodes.indexOf(this); this.remove(); p.insertAt(i, n); }
  contains(n) { while (n) { if (n === this) return true; n = n.parentNode; } return false; }
  cloneNode(deep) {
    const c = this instanceof Text ? new Text(this.ownerDocument, this.data) : this.ownerDocument.createElement(this.tagName);
    if (this instanceof Element) { c.attrs = new Map(this.attrs); for (const k of ["value", "checked", "disabled", "hidden", "type"]) c[k] = this[k]; }
    if (deep) c.append(...this.childNodes.map((x) => x.cloneNode(true)));
    return c;
  }
  addEventListener(type, fn, opts) {
    (this.listeners[type] ||= []).push({ fn, once: !!(opts && opts.once), capture: opts === true || !!(opts && opts.capture) });
  }
  removeEventListener(type, fn) { this.listeners[type] = (this.listeners[type] || []).filter((l) => l.fn !== fn); }
  dispatchEvent(ev) {
    if (!ev.target) Object.defineProperty(ev, "target", { value: this, configurable: true, writable: true });
    const path = []; for (let n = this; n; n = n.parentNode) path.push(n);
    if (this.ownerDocument && !path.includes(this.ownerDocument)) path.push(this.ownerDocument);
    if (this.ownerDocument?.defaultView) path.push(this.ownerDocument.defaultView);
    const run = (n, capture) => {
      for (const l of [...(n.listeners?.[ev.type] || [])]) {
        if (capture !== null && l.capture !== capture) continue;
        ev.currentTarget = n;
        if (l.once) n.removeEventListener(ev.type, l.fn);
        l.fn.call(n, ev);
        if (ev.immediateStopped) return;
      }
    };
    for (const n of [...path].reverse().slice(0, -1)) { run(n, true); if (ev.stopped) return !ev.defaultPrevented; }
    if (!ev.stopped) run(this, null);
    if (ev.bubbles !== false) for (const n of path.slice(1)) { if (ev.stopped) break; run(n, false); }
    return !ev.defaultPrevented;
  }
}

// A failing assert prints its operands; walking the whole linked tree and the dataset Proxy never ends, so print a short tag.
Node.prototype[require("node:util").inspect.custom] = function () { return `<${this.tagName ? this.tagName.toLowerCase() : "#node"}${this.className ? "." + this.className : ""}>`; };

class Text extends Node {
  constructor(doc, data) { super(doc); this.data = data; this.nodeType = 3; }
  get textContent() { return this.data; }
  set textContent(v) { this.data = String(v); }
  get nodeValue() { return this.data; }
}
class Fragment extends Node { constructor(doc) { super(doc); this.nodeType = 11; } }

// assert's failure diff ignores inspect.custom and walks enumerable fields; hiding the tree links keeps it to one node.
function hideLinks(n) {
  for (const k of ["ownerDocument", "parentNode", "childNodes", "listeners", "style", "classList", "dataset"])
    if (k in n) Object.defineProperty(n, k, { value: n[k], writable: true, configurable: true, enumerable: false });
}

const BOOL_ATTRS = ["hidden", "disabled", "checked", "open", "multiple", "required", "readonly", "selected"];
class Element extends Node {
  constructor(doc, tag) {
    super(doc); this.tagName = String(tag).toUpperCase(); this.nodeType = 1; this.attrs = new Map();
    this.style = new Proxy({ setProperty(k, v) { this[k] = v; }, removeProperty(k) { delete this[k]; }, getPropertyValue(k) { return this[k] || ""; } }, {});
    this.classList = new ClassList(this);
    this.value = ""; this.checked = false; this.disabled = false; this.hidden = false; this.open = false;
    this.scrollTop = 0; this.scrollLeft = 0; this.clientHeight = 0; this.clientWidth = 0; this.scrollHeight = 0; this.offsetHeight = 0; this.offsetTop = 0;
    this.files = [];
    const self = this;
    this.dataset = new Proxy({}, {
      get(_, k) { return typeof k === "string" ? self.getAttribute("data-" + k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase())) ?? undefined : undefined; },
      set(_, k, v) { self.setAttribute("data-" + k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase()), v); return true; },
      deleteProperty(_, k) { self.removeAttribute("data-" + k.replace(/[A-Z]/g, (c) => "-" + c.toLowerCase())); return true; },
      has(_, k) { return self.hasAttribute("data-" + String(k).replace(/[A-Z]/g, (c) => "-" + c.toLowerCase())); },
      ownKeys() { return [...self.attrs.keys()].filter((k) => k.startsWith("data-")).map((k) => k.slice(5).replace(/-(.)/g, (_, c) => c.toUpperCase())); },
      getOwnPropertyDescriptor(_, k) { const v = self.dataset[k]; return v === undefined ? undefined : { value: v, enumerable: true, configurable: true }; }
    });
    hideLinks(this);
  }
  get localName() { return this.tagName.toLowerCase(); }
  get nodeName() { return this.tagName; }
  get className() { return this.attrs.get("class") || ""; }
  set className(v) { this.attrs.set("class", String(v)); }
  get id() { return this.attrs.get("id") || ""; }
  set id(v) { this.attrs.set("id", String(v)); }
  get title() { return this.attrs.get("title") || ""; }
  set title(v) { this.attrs.set("title", String(v)); }
  get href() { return this.attrs.get("href") || ""; }
  set href(v) { this.attrs.set("href", String(v)); }
  get type() { return this.attrs.get("type") || (this.tagName === "INPUT" ? "text" : this.tagName === "BUTTON" ? "submit" : ""); }
  set type(v) { this.attrs.set("type", String(v)); }
  get name() { return this.attrs.get("name") || ""; }
  set name(v) { this.attrs.set("name", String(v)); }
  get placeholder() { return this.attrs.get("placeholder") || ""; }
  set placeholder(v) { this.attrs.set("placeholder", String(v)); }
  get tabIndex() { return Number(this.attrs.get("tabindex") ?? -1); }
  set tabIndex(v) { this.attrs.set("tabindex", String(v)); }
  get innerHTML() { return this.textContent; }
  set innerHTML(v) { this.textContent = String(v).replace(/<[^>]*>/g, ""); }
  get innerText() { return this.textContent; }
  set innerText(v) { this.textContent = v; }
  get options() { return this.querySelectorAll("option"); }
  get selectedIndex() { return this.options.findIndex((o) => o.value === this.value); }
  setAttribute(k, v) { k = String(k).toLowerCase(); if (BOOL_ATTRS.includes(k)) this[k] = true; this.attrs.set(k, String(v)); }
  getAttribute(k) { k = String(k).toLowerCase(); if (BOOL_ATTRS.includes(k)) return this[k] ? (this.attrs.get(k) ?? "") : null; return this.attrs.has(k) ? this.attrs.get(k) : null; }
  hasAttribute(k) { return this.getAttribute(k) !== null; }
  removeAttribute(k) { k = String(k).toLowerCase(); if (BOOL_ATTRS.includes(k)) this[k] = false; this.attrs.delete(k); }
  toggleAttribute(k, force) { const on = force === undefined ? !this.hasAttribute(k) : !!force; if (on) this.setAttribute(k, ""); else this.removeAttribute(k); return on; }
  getAttributeNames() { return [...this.attrs.keys()]; }
  matches(s) { return matches(this, s); }
  closest(s) { for (let n = this; n instanceof Element; n = n.parentNode) if (n.matches(s)) return n; return null; }
  querySelectorAll(s) {
    const out = [];
    const walk = (n) => { for (const c of n.children) { if (matches(c, s, this)) out.push(c); walk(c); } };
    walk(this);
    return out;
  }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
  getElementsByTagName(t) { return this.querySelectorAll(t); }
  getBoundingClientRect() { return { top: 0, left: 0, bottom: 0, right: 0, width: 0, height: 0, x: 0, y: 0 }; }
  getClientRects() { return []; }
  scrollIntoView() { this.scrolledIntoView = (this.scrolledIntoView || 0) + 1; }
  scrollTo(a, b) { if (typeof a === "object") { if (a.top !== undefined) this.scrollTop = a.top; } else { this.scrollLeft = a; this.scrollTop = b; } }
  focus() { this.ownerDocument.activeElement = this; this.focused = (this.focused || 0) + 1; }
  blur() { if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = this.ownerDocument.body; }
  click() { if (this.disabled) return; this.dispatchEvent(new Event("click", { bubbles: true })); }
  select() {}
  setSelectionRange() {}
  showModal() { this.open = true; this.setAttribute("open", ""); }
  show() { this.open = true; this.setAttribute("open", ""); }
  close(v) { if (v !== undefined) this.returnValue = v; this.open = false; this.removeAttribute("open"); this.dispatchEvent(new Event("close")); }
  requestSubmit() { this.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); }
  reset() {}
  animate() { return { finished: Promise.resolve(), cancel() {}, onfinish: null }; }
  checkValidity() { return true; }
  reportValidity() { return true; }
  setPointerCapture() {}
  releasePointerCapture() {}
  hasPointerCapture() { return false; }
  insertAdjacentElement(where, n) { if (where === "afterend") this.after(n); else if (where === "beforebegin") this.before(n); else if (where === "afterbegin") this.prepend(n); else this.append(n); return n; }
}

class Event {
  constructor(type, init = {}) { this.type = type; Object.assign(this, init); this.bubbles = !!init.bubbles; this.defaultPrevented = false; }
  preventDefault() { this.defaultPrevented = true; }
  stopPropagation() { this.stopped = true; }
  stopImmediatePropagation() { this.stopped = true; this.immediateStopped = true; }
}

class Document extends Node {
  constructor() {
    super(null); this.ownerDocument = this; this.nodeType = 9;
    this.documentElement = this.createElement("html"); this.append(this.documentElement);
    this.head = this.createElement("head"); this.body = this.createElement("body");
    this.documentElement.append(this.head, this.body);
    this.activeElement = this.body; this.hidden = false; this.visibilityState = "visible";
  }
  createElement(t) { return new Element(this, t); }
  createElementNS(_, t) { return new Element(this, t); }
  createTextNode(t) { return new Text(this, t); }
  createDocumentFragment() { return new Fragment(this); }
  querySelectorAll(s) { return this.documentElement.matches(s) ? [this.documentElement, ...this.documentElement.querySelectorAll(s)] : this.documentElement.querySelectorAll(s); }
  querySelector(s) { return this.querySelectorAll(s)[0] || null; }
  getElementById(id) { return this.querySelector("#" + id); }
  get children() { return [this.documentElement]; }
}

// Installs a fresh document and window on globalThis. Returns { document, window }.
function install(extra = {}) {
  const document = new Document();
  const listeners = {};
  const window = Object.assign(globalThis.window || {}, {
    document, listeners,
    addEventListener(t, fn) { (listeners[t] ||= []).push(fn); },
    removeEventListener(t, fn) { listeners[t] = (listeners[t] || []).filter((f) => f !== fn); },
    dispatchEvent(ev) { for (const f of [...(listeners[ev.type] || [])]) f(ev); return true; },
    requestAnimationFrame: (fn) => setTimeout(() => fn(Date.now()), 0),
    cancelAnimationFrame: (id) => clearTimeout(id),
    getComputedStyle: () => ({ getPropertyValue: () => "" }),
    innerHeight: 800, innerWidth: 1200,
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    ...extra
  });
  document.defaultView = window;
  window.listeners = listeners;
  Object.assign(globalThis, { window, document, Event, CustomEvent: Event, KeyboardEvent: Event, MouseEvent: Event, Node, Element, HTMLElement: Element, Text });
  for (const k of ["requestAnimationFrame", "cancelAnimationFrame", "getComputedStyle", "matchMedia"]) if (!globalThis[k]) globalThis[k] = window[k];
  return { document, window };
}

// The app.js helpers the views receive in ctx, built on the installed document.
function helpers(document) {
  const el = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text !== undefined && text !== null) n.textContent = text; return n; };
  const icon = (name, cls = "icon") => { const s = document.createElementNS("http://www.w3.org/2000/svg", "svg"); s.setAttribute("class", cls); s.setAttribute("data-icon", name); return s; };
  const button = (label, cls, iconName, onClick) => {
    const b = el("button", `btn ${cls || ""}`.trim()); b.type = "button";
    if (iconName) b.append(icon(iconName));
    b.append(label);
    if (onClick) b.addEventListener("click", onClick);
    return b;
  };
  const $ = (s) => document.querySelector(s);
  const query = (o) => new URLSearchParams(Object.entries(o).filter(([, v]) => v !== undefined && v !== null && v !== "")).toString();
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  return { el, icon, button, $, query, raw, tr: (s) => s };
}

// Lets pending promise callbacks and zero-delay timers run.
const flush = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 0)); };
// Buttons whose text contains `label` under `root`.
const buttons = (root, label) => root.querySelectorAll("button").filter((b) => !label || b.textContent.includes(label));
const text = (root) => root.textContent;
const fire = (node, type, init = {}) => node.dispatchEvent(new Event(type, { bubbles: true, cancelable: true, ...init }));
const input = (node, value) => { node.value = value; fire(node, "input"); fire(node, "change"); };

module.exports = { install, helpers, flush, buttons, text, fire, input, Event, Element, Document };
