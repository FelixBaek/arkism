// AI 도움 한 틀 (전문가 창의 AI 제안 · Figma 868:61909): 반짝이 알약 버튼 → 말풍선 대화 → 버튼 아래 보라색 제안 목록.
// 적용은 화면의 초안만 바꾸고, 저장은 각 화면의 저장 버튼이 해요. 화면들이 같은 모양·같은 동작을 쓰도록 여기 하나만 둔다.
//   ArkismAIChat.button(opts) → 알약 + 말풍선 + 제안 목록
//   ArkismAIChat.field(input, opts) → 칸 오른쪽 끝의 반짝이 아이콘; 그 칸에 맞춘 preset으로 대화하고 제안을 칸에 넣는다
// opts: { label, title, sub, greeting, placeholder, quick, preset, context, ask, state, kit, onDrop }
//   ask(text, history) → { say, props: [{ label, from, to, apply, undo? }] }; apply가 false를 돌려주면 적용 안 한 것으로 둔다
//   from이 null이면 비교 없이 제안 글만 보여 준다
//   state: 화면이 다시 그려져도 대화가 남게 화면이 들고 있는 빈 객체 (없으면 이 요소 안에만 남음)
window.ArkismAIChat = (() => {
  "use strict";
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = text;
    return n;
  }
  function icon(name) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    use.setAttribute("href", `#i-${name}`);
    svg.append(use);
    return svg;
  }
  function btn(label, cls, onClick) {
    const b = el("button", `btn ${cls}`, label);
    b.type = "button";
    b.addEventListener("click", onClick);
    return b;
  }
  const api = { kit: null }; // app.js puts the assist runner here (assist.js) for field()'s default ask

  // 전→후: short values as "앞 → 뒤", lists as gone/come, longer text as a word diff.
  function valueView(p) {
    const v = el("div", "v");
    const { from, to } = p;
    if (from === null) { v.classList.add("text"); raw(v).textContent = String(to ?? ""); return v; } // nothing to compare with: the proposal as written
    if (Array.isArray(to)) {
      const a = new Set(from || []), b = new Set(to);
      const gone = [...a].filter((x) => !b.has(x)), come = to.filter((x) => !a.has(x));
      for (const x of gone) v.append(raw(el("del", "", x)));
      for (const x of come) v.append(raw(el("ins", "", x)));
      if (!gone.length && !come.length) v.append(el("span", "", "순서만 바뀌어요"));
      return v;
    }
    const f = String(from ?? ""), t = String(to ?? "");
    if (f.length <= 24 && t.length <= 24 && !/\n/.test(f + t)) {
      v.append(raw(el("del", "", f || tr("없음"))), el("span", "arrow", "→"), raw(el("ins", "", t)));
      return v;
    }
    v.classList.add("text");
    raw(v);
    for (const [op, s] of window.ArkismTextDiff(f, t)) v.append(op === "=" ? document.createTextNode(s) : el(op === "-" ? "del" : "ins", "", s));
    return v;
  }

  function mount(opts, inp) {
    const st = opts.state || {};
    st.log ||= [];
    st.props ||= [];
    st.ask ??= "";
    const root = el(inp ? "div" : "section", inp ? "ex-ai ex-ai-field" : "ex-ai");
    if (inp) {
      st.input = inp; // a redraw hands in a new field; proposals write into the one on screen
      if (inp.tagName === "TEXTAREA") root.classList.add("is-area");
      if (inp.parentNode) inp.replaceWith(root);
    }
    const label = opts.label || "AI 제안";
    const title = opts.title || label;
    const anchor = el("button", inp ? "ex-ai-spark" : "ex-ai-btn");
    anchor.type = "button";
    anchor.setAttribute("aria-haspopup", "dialog");
    if (inp) { anchor.setAttribute("aria-label", tr(`${title} · AI에게 묻기`)); anchor.title = tr("AI에게 묻기"); anchor.append(icon("sparkle")); } else anchor.append(icon("sparkle"), el("span", "", label));
    anchor.addEventListener("click", () => toggle(!st.open));
    const zone = el("div", "ex-ai-zone");
    // Inside a <label>, a click on the bubble's text would jump to the field: keep clicks here.
    zone.addEventListener("click", (e) => { if (!e.target.closest("input, textarea")) e.preventDefault(); });
    if (inp) root.append(inp, anchor, zone); else root.append(anchor, zone);
    const toggle = (on) => { st.open = on; st.grow = on; paint(); if (!on) anchor.focus(); };
    const busy = () => st.log.some((m) => m.busy);
    const line = (who, text, more = {}) => { const m = { who, text, fresh: true, ...more }; st.log.push(m); if (st.log.length > 12) st.log.shift(); return m; };
    const repaint = () => st.paint?.();

    async function send(text) {
      text = String(text || "").trim();
      if (!text || busy()) return;
      const history = st.log.filter((m) => !m.busy && !m.err).map(({ who, text: t }) => ({ who, text: t }));
      st.ask = "";
      line("me", text);
      const reply = line("ai", tr("생각하는 중이에요…"), { busy: true });
      repaint();
      try {
        const r = (await (opts.ask || defaultAsk)(text, history)) || {};
        const props = Array.isArray(r.props) ? r.props.filter(Boolean) : [];
        if (props.length) { st.props = props.map((p) => ({ ...p, applied: false })); st.open = false; } // the bubble steps aside so the purple list shows
        Object.assign(reply, { busy: false, text: r.say || (props.length ? tr("아래 보라색 목록에 제안을 넣었어요. 보고 적용하세요.") : tr("바꿀 곳을 찾지 못했어요. 말을 바꿔 다시 물어봐 주세요.")) });
      } catch (e) { Object.assign(reply, { busy: false, err: true, text: e?.message || tr("AI가 답하지 못했어요.") }); }
      repaint();
    }
    // field()'s own ask: the field's preset, what is around it and the talk so far → { say, value }.
    async function defaultAsk(text, history) {
      const kit = opts.kit || api.kit;
      if (!kit) throw new Error(tr("AI 도움을 쓸 수 없어요."));
      const cur = st.input?.value || "";
      const talk = history.map((m) => `${m.who === "me" ? "User" : "You"}: ${m.text}`).join("\n");
      const prompt = `${opts.preset || "Help write this field."}\n\n${opts.context ? `Context:\n${opts.context()}\n\n` : ""}Current text of the field:\n${cur || "(empty)"}\n\n${talk ? `Conversation so far:\n${talk}\n\n` : ""}Request: ${text}\n\nAnswer with ONE JSON object: {"say": "one or two short sentences to the user, in the language of the request", "value": "the whole new text for the field, or null when nothing should change"}.`;
      const a = await kit.assist("json", prompt, { persona: { crew: { instructions: "You help write one field of a form. Reply with the JSON object only." } } });
      if (a.status !== "done") throw new Error(kit.assistError(a));
      const j = kit.parseJSON(a.output || "");
      if (!j || typeof j !== "object" || Array.isArray(j)) throw new Error(tr("AI의 답을 읽지 못했어요. 다시 해 보세요."));
      const max = st.input?.maxLength > 0 ? st.input.maxLength : 20000;
      const value = typeof j.value === "string" ? j.value.slice(0, max) : null;
      const write = (v) => { const i = st.input; if (!i) return; i.value = v; i.dispatchEvent(new Event("input", { bubbles: true })); };
      const props = value !== null && value !== cur ? [{ label: title, from: cur, to: value, apply: () => write(value), undo: () => write(cur) }] : [];
      return { say: typeof j.say === "string" ? j.say.slice(0, 600) : "", props };
    }
    async function apply(p, on) {
      const r = await (on ? p.apply() : p.undo?.());
      if (r === false) return;
      p.applied = on;
    }

    function bubble() {
      const pop = el("div", `ex-ai-pop${st.grow ? " grow" : ""}${inp ? " at-field" : ""}`);
      st.grow = false; // the grow plays once, not on every redraw while talking
      pop.setAttribute("role", "dialog");
      pop.setAttribute("aria-label", title);
      const head = el("div", "ex-ai-pop-head");
      const x = el("button", "icon-btn plain");
      x.type = "button";
      x.setAttribute("aria-label", "닫기");
      x.append(icon("x"));
      x.addEventListener("click", () => toggle(false));
      head.append(icon("sparkle"), el("strong", "", title), el("small", "", opts.sub || ""), x);
      const log = el("ol", "ex-chat");
      log.setAttribute("aria-live", "polite");
      if (opts.greeting) log.append(el("li", "ex-chat-ai", opts.greeting));
      for (const m of st.log) {
        const li = el("li", `ex-chat-${m.who}${m.busy ? " busy" : ""}${m.err ? " err" : ""}${m.fresh ? " fresh" : ""}`);
        if (m.busy) { li.setAttribute("aria-label", m.text); li.append(el("i"), el("i"), el("i")); } else raw(li).textContent = m.text;
        m.fresh = false;
        log.append(li);
      }
      const wait = busy();
      const ask = el("input");
      ask.maxLength = 400;
      ask.value = st.ask;
      ask.placeholder = opts.placeholder || "";
      ask.setAttribute("aria-label", title);
      ask.addEventListener("input", () => { st.ask = ask.value; });
      const go = btn("보내기", "primary small", () => void send(ask.value));
      go.disabled = wait;
      ask.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); if (!go.disabled) go.click(); } });
      const quick = el("div", "ex-ai-quick");
      for (const q of opts.quick || []) {
        const c = el("button", "ex-ai-chip", q);
        c.type = "button";
        c.disabled = wait;
        c.addEventListener("click", () => void send(tr(q)));
        quick.append(c);
      }
      const row = el("div", "ex-ai-row");
      row.append(ask, go);
      pop.append(head, log);
      if ((opts.quick || []).length) pop.append(quick);
      pop.append(row);
      pop.addEventListener("keydown", (e) => { if (e.key === "Escape" && !e.defaultPrevented) { e.preventDefault(); e.stopPropagation(); toggle(false); } });
      requestAnimationFrame(() => { log.scrollTop = log.scrollHeight; if (!wait && pop.isConnected) ask.focus({ preventScroll: true }); });
      return pop;
    }
    function propRow(p) {
      const r = el("div", `ex-prop${p.applied ? " applied" : ""}`);
      const acts = el("div", "acts");
      if (p.applied) {
        acts.append(el("small", "done", "적용함 · 저장하면 반영돼요"));
        if (p.undo) acts.append(btn("되돌리기", "ghost small", async () => { await apply(p, false); repaint(); }));
      } else {
        const ok = btn("적용", "ghost small", async () => { ok.disabled = true; try { await apply(p, true); } finally { ok.disabled = false; repaint(); } }); // a slow apply (a commit) can't run twice
        acts.append(ok,
          btn("빼기", "ghost small", () => { st.props = st.props.filter((y) => y !== p); opts.onDrop?.(p); repaint(); }));
      }
      r.append(el("span", "k", p.label), valueView(p), acts);
      return r;
    }
    function paint() {
      if (!root.isConnected && st.paint !== paint && st.paint) return st.paint(); // a newer copy of this box is on screen
      st.paint = paint;
      anchor.classList.toggle("on", !!st.open);
      anchor.setAttribute("aria-expanded", String(!!st.open));
      zone.replaceChildren();
      if (st.open) zone.append(bubble());
      if (st.props.length) {
        const open = st.props.filter((p) => !p.applied);
        const list = el("div", "ex-props");
        const hd = el("div", "ex-props-head");
        hd.append(icon("sparkle"), el("strong", "", `AI 제안 ${st.props.length}개`), el("small", "", "적용해도 저장하기 전에는 바뀌지 않아요"));
        if (open.length) hd.append(btn("모두 적용", "ghost small", async () => { for (const p of open) await apply(p, true); repaint(); }));
        list.append(hd, ...st.props.map(propRow));
        zone.append(list);
      }
    }
    st.paint = paint;
    paint();
    return root;
  }

  api.button = (opts = {}) => mount(opts, null);
  api.field = (inp, opts = {}) => mount(opts, inp);
  return api;
})();
