// A small DOM stand-in for the view-module tests: enough of Element, selectors and events to drive real handlers.
class Text {
  constructor(t) { this.nodeType = 3; this.data = String(t); this.parentNode = null; }
  get textContent() { return this.data; }
  set textContent(v) { this.data = String(v); }
  remove() { this.parentNode?._drop(this); }
}

function parseCompound(s) {
  const tag = /^[a-z0-9*]+/i.exec(s)?.[0];
  const classes = [...s.matchAll(/\.([\w-]+)/g)].map(m => m[1]);
  const attrs = [...s.matchAll(/\[([\w-]+)(?:="([^"]*)")?\]/g)].map(m => [m[1], m[2]]);
  const id = /#([\w-]+)/.exec(s)?.[1];
  return n => n.nodeType === 1 && (!tag || tag === "*" || n.tagName === tag.toUpperCase()) && (!id || n.id === id)
    && classes.every(c => n.classList.contains(c)) && attrs.every(([k, v]) => { const a = n.getAttribute(k); return v === undefined ? a != null : a === v; });
}
function matcher(sel) {
  const groups = sel.split(",").map(g => g.trim().split(/\s+/).filter(x => x !== ">").map(parseCompound));
  return n => groups.some(parts => {
    if (!parts.at(-1)(n)) return false;
    let i = parts.length - 2, p = n.parentNode;
    while (i >= 0 && p) { if (parts[i](p)) i--; p = p.parentNode; }
    return i < 0;
  });
}

class Element {
  constructor(tag, doc) {
    this.nodeType = 1; this.tagName = tag.toUpperCase(); this.ownerDocument = doc; this.childNodes = []; this.parentNode = null;
    this.attributes = {}; this.dataset = {}; this.style = {}; this.listeners = {}; this.hidden = false; this.disabled = false; this._className = "";
    const self = this;
    this.classList = {
      add: (...c) => { const s = new Set(self._cls()); c.forEach(x => s.add(x)); self.className = [...s].join(" "); },
      remove: (...c) => { self.className = self._cls().filter(x => !c.includes(x)).join(" "); },
      toggle: (c, on) => { const has = self.classList.contains(c); const want = on === undefined ? !has : !!on; want ? self.classList.add(c) : self.classList.remove(c); return want; },
      contains: c => self._cls().includes(c),
    };
    if (this.tagName === "DIALOG") { this.open = false; this.returnValue = ""; }
    if (this.tagName === "INPUT" || this.tagName === "TEXTAREA") { this.value = ""; this.selectionStart = 0; this.selectionEnd = 0; }
  }
  _cls() { return this._className.split(/\s+/).filter(Boolean); }
  get className() { return this._className; }
  set className(v) { this._className = String(v); }
  get id() { return this.attributes.id || ""; }
  set id(v) { this.attributes.id = v; }
  get children() { return this.childNodes.filter(n => n.nodeType === 1); }
  get childElementCount() { return this.children.length; }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes.at(-1) || null; }
  get isConnected() { let p = this; while (p.parentNode) p = p.parentNode; return p === this.ownerDocument?.documentElement; }
  get textContent() { return this.childNodes.map(n => n.textContent).join(""); }
  set textContent(v) { this.childNodes.forEach(n => { n.parentNode = null; }); this.childNodes = []; if (v !== "" && v != null) this.append(String(v)); }
  get options() { return this.querySelectorAll("option"); }
  get value() {
    if (this.tagName === "SELECT") return this._value ?? (this.options.find(o => o.selected) || this.options[0])?.value ?? "";
    if (this.tagName === "OPTION") return this._value ?? this.textContent;
    return this._value ?? "";
  }
  set value(v) { this._value = String(v); if (this.tagName === "SELECT") this.options.forEach(o => { o.selected = o.value === this._value; }); }
  _wrap(c) { if (typeof c === "string" || typeof c === "number") return new Text(c); if (c == null) return new Text(""); return c; }
  _drop(c) { const i = this.childNodes.indexOf(c); if (i >= 0) this.childNodes.splice(i, 1); c.parentNode = null; }
  _adopt(c) { c = this._wrap(c); c.parentNode?._drop(c); c.parentNode = this; return c; }
  append(...cs) { for (const c of cs) this.childNodes.push(this._adopt(c)); }
  appendChild(c) { this.append(c); return c; }
  prepend(...cs) { this.childNodes.unshift(...cs.map(c => this._adopt(c))); }
  insertBefore(c, ref) { c = this._adopt(c); const i = this.childNodes.indexOf(ref); this.childNodes.splice(i < 0 ? this.childNodes.length : i, 0, c); return c; }
  replaceChildren(...cs) { this.childNodes.forEach(n => { n.parentNode = null; }); this.childNodes = []; this.append(...cs); }
  replaceWith(n) { const p = this.parentNode; if (!p) return; n = p._adopt(n); const i = p.childNodes.indexOf(this); p.childNodes.splice(i, 1, n); this.parentNode = null; }
  remove() { this.parentNode?._drop(this); }
  setAttribute(k, v) { this.attributes[k] = String(v); if (k === "class") this.className = v; }
  getAttribute(k) { if (k === "class") return this.className || null; if (k.startsWith("data-")) { const d = this.dataset[k.slice(5).replace(/-(\w)/g, (_, x) => x.toUpperCase())]; if (d !== undefined) return String(d); } return this.attributes[k] ?? null; }
  removeAttribute(k) { delete this.attributes[k]; }
  addEventListener(t, fn) { (this.listeners[t] ||= []).push(fn); }
  removeEventListener(t, fn) { this.listeners[t] = (this.listeners[t] || []).filter(f => f !== fn); }
  dispatchEvent(e) {
    if (!e._fake) e = makeEvent(e.type, { bubbles: !!e.bubbles });
    if (!e.target) e.target = this;
    e.currentTarget = this;
    const out = (this.listeners[e.type] || []).map(fn => fn.call(this, e));
    if (e.bubbles !== false && !e._stop && this.parentNode && e.type !== "close") this.parentNode.dispatchEvent?.(Object.assign(e, { currentTarget: this.parentNode }));
    e._results = (e._results || []).concat(out);
    return !e.defaultPrevented;
  }
  async fire(type, extra = {}) { const e = makeEvent(type, { bubbles: false, ...extra }); e.target ??= this; this.dispatchEvent(e); await Promise.all(e._results || []); return e; }
  click() {
    if (this.disabled) return;
    const e = makeEvent("click"); this.dispatchEvent(e); this._lastClick = Promise.all(e._results || []);
    if (this.type === "submit" || (this.tagName === "BUTTON" && !this.type && this.closest("form"))) {
      const form = this.closest("form");
      if (form) { const s = makeEvent("submit", { bubbles: false, submitter: this }); form.dispatchEvent(s); this._lastSubmit = Promise.all(s._results || []); if (!s.defaultPrevented && form.method === "dialog") form.closest("dialog")?.close(this.value); }
    }
    return this._lastClick;
  }
  focus() { this.ownerDocument.activeElement = this; }
  blur() {}
  setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; }
  showModal() { this.open = true; }
  close(v) { this.open = false; if (v !== undefined) this.returnValue = v; this.dispatchEvent(makeEvent("close", { bubbles: false })); }
  closest(sel) { const m = matcher(sel); let n = this; while (n && n.nodeType === 1) { if (m(n)) return n; n = n.parentNode; } return null; }
  matches(sel) { return matcher(sel)(this); }
  _walk(out = []) { for (const c of this.childNodes) if (c.nodeType === 1) { out.push(c); c._walk(out); } return out; }
  querySelectorAll(sel) { const m = matcher(sel); return this._walk().filter(m); }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  getContext() { return { drawImage() {} }; }
  toDataURL() { return this.ownerDocument._dataUrl?.(this) ?? "data:image/webp;base64,AAAA"; }
  scrollIntoView() {}
  getBoundingClientRect() { return { top: 0, left: 0, width: 100, height: 20, bottom: 20, right: 100 }; }
}

function makeEvent(type, init = {}) {
  return { _fake: true, type, bubbles: true, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this._stop = true; }, ...init };
}

function createDocument() {
  const doc = { activeElement: null, hidden: false };
  doc.documentElement = new Element("html", doc);
  doc.body = new Element("body", doc);
  doc.documentElement.append(doc.body);
  doc.activeElement = doc.body;
  doc.createElement = t => new Element(t, doc);
  doc.createElementNS = (_, t) => new Element(t, doc);
  doc.createTextNode = t => new Text(t);
  doc.querySelector = s => doc.documentElement.querySelector(s);
  doc.querySelectorAll = s => doc.documentElement.querySelectorAll(s);
  doc.getElementById = id => doc.documentElement.querySelector(`#${id}`);
  doc.addEventListener = () => {};
  return doc;
}

// The house helpers app.js passes into every view: el, button, $, raw, section and the rest.
function house(doc) {
  const el = (tag, cls = "", text) => { const n = doc.createElement(tag); if (cls) n.className = cls; if (text !== undefined && text !== null) n.append(text); return n; };
  const button = (label, cls = "", icon, onClick) => { const b = el("button", `btn ${cls}`.trim()); b.type = "button"; if (icon) b.append(el("i", "icon", icon)); b.append(el("span", "", label)); if (onClick) b.addEventListener("click", onClick); return b; };
  const $ = sel => doc.querySelector(sel);
  const section = (title, aside) => { const s = el("section", "section"); const h = el("h3", "", title); if (aside) h.append(el("small", "", aside)); s.append(h); return s; };
  return { el, button, $, section, raw: n => n, icon: name => el("i", "icon", name), tr: t => t };
}

const flush = async (n = 8) => { for (let i = 0; i < n; i++) await new Promise(r => setImmediate(r)); };

module.exports = { createDocument, house, makeEvent, flush, Element };
