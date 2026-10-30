// A small DOM stand-in for the usage, routing and model-settings tests: just what those views touch.
class El {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase(); this.children = []; this.attrs = {}; this.dataset = {}; this.listeners = {};
    this.style = { setProperty(k, v) { this[k] = v; } }; this.className = ""; this.parent = null; this.disabled = false; this.hidden = false; this.value = "";
    const self = this;
    this.classList = {
      add: (...c) => { self.className = [...new Set([...self.className.split(" ").filter(Boolean), ...c])].join(" "); },
      remove: (...c) => { self.className = self.className.split(" ").filter((x) => x && !c.includes(x)).join(" "); },
      contains: (c) => self.className.split(" ").includes(c),
      toggle: (c, on = !self.classList.contains(c)) => { if (on) self.classList.add(c); else self.classList.remove(c); return on; },
    };
  }
  get childNodes() { return this.children; }
  get isConnected() { return true; }
  get options() { return this.all((n) => n.tagName === "OPTION"); }
  get textContent() { return this.children.map((c) => (typeof c === "string" ? c : c.textContent)).join(""); }
  set textContent(t) { this.children = [String(t)]; }
  append(...xs) { for (const x of xs) { if (x == null) continue; if (typeof x === "object") { x.remove(); x.parent = this; } this.children.push(typeof x === "object" ? x : String(x)); } }
  prepend(...xs) { const old = this.children; this.children = []; this.append(...xs); this.children.push(...old.filter((c) => !this.children.includes(c))); }
  replaceChildren(...xs) { this.children = []; this.append(...xs); }
  remove() { if (this.parent) { this.parent.children = this.parent.children.filter((c) => c !== this); this.parent = null; } }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k] ?? null; }
  addEventListener(t, f) { (this.listeners[t] ||= []).push(f); }
  // Events bubble to the parents unless a listener stops them.
  fire(t, ev = {}) {
    const e = { target: this, preventDefault() { e.prevented = true; }, stopPropagation() { e.stopped = true; }, stopImmediatePropagation() { e.stopped = e.now = true; }, ...ev };
    for (let n = this; n && !e.stopped; n = n.parent) for (const f of n.listeners[t] || []) { f(e); if (e.now) break; }
    return e;
  }
  click() { return this.fire("click"); }
  focus() { this.focused = true; } setSelectionRange() {} showModal() { this.open = true; } close() { this.open = false; this.fire("close"); }
  all(pred, out = []) { for (const c of this.children) if (typeof c === "object") { if (pred(c)) out.push(c); c.all(pred, out); } return out; }
  querySelectorAll(sel) { const cls = sel.replace(/^\./, ""); return this.all((n) => n.classList.contains(cls)); }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
class Option extends El { constructor(text = "", value = "", _d = false, selected = false) { super("option"); this.textContent = text; this.value = value; this.selected = selected; } }

function install() {
  const body = new El("body");
  const document = { body, activeElement: null, createElement: (t) => new El(t), createElementNS: (_, t) => new El(t), querySelector: (s) => body.querySelector(s) };
  globalThis.document = document; globalThis.Option = Option; globalThis.window ||= globalThis;
  const el = (tag, cls, text) => { const n = new El(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; };
  const button = (label, cls, iconName, onClick) => { const b = el("button", cls, label); if (onClick) b.addEventListener("click", onClick); return b; };
  const icon = (name) => el("i", `ph ph-${name}`);
  const segment = (tabs, current, onPick) => { const s = el("div", "seg"); for (const [k, l] of tabs) { const b = el("button", `tab${k === current ? " active" : ""}`, l); b.dataset.key = k; b.addEventListener("click", () => onPick(k)); s.append(b); } return s; };
  const agentLogo = (spec, name) => el("span", "agent-tile", name);
  return { El, document, el, button, icon, segment, agentLogo };
}
// Every node under root whose own text (or text of its whole subtree) is exactly t.
const byText = (root, t) => root.all((n) => n.textContent === t);
const text = (root) => root.textContent;
const flush = () => new Promise((r) => setImmediate(r));

module.exports = { install, byText, text, flush, El };
