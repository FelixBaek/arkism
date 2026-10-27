// AI 도움 한 틀 (전문가 창의 AI 제안 · Figma 868:61909): 반짝이 알약 버튼 → 말풍선 대화 → 보라색 제안 목록 (버튼은 아래 저장 줄 오른쪽 끝에 둘 수 있어요: root.dock).
// 적용은 화면의 초안만 바꾸고, 저장은 각 화면의 저장 버튼이 해요. 화면들이 같은 모양·같은 동작을 쓰도록 여기 하나만 둔다.
//   ArkismAIChat.button(opts) → 알약 + 말풍선 + 제안 목록
//   ArkismAIChat.field(input, opts) → 칸 오른쪽 끝의 반짝이 아이콘; 누르면 대화 없이 그 칸의 글(비었으면 주변 맥락)로 바로 채우거나 다듬는다
// opts: { label, title, sub, greeting, placeholder, quick, ask, state, onDrop, footer } · field(): { title, preset, context, state, kit }
//   ask(text, history) → { say, props: [{ label, from, to, apply, undo? }] }; apply가 false를 돌려주면 적용 안 한 것으로 둔다
//   from이 null이면 비교 없이 제안 글만 보여 준다
//   footer: { label, hint, onClick } → 말풍선 맨 아래 보라 줄 (예: 이 스킬 개선하기); 누르면 말풍선을 닫고 onClick
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
  const api = { kit: null }; // app.js puts the assist runner here (assist.js) for field()

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

  function mount(opts) {
    const st = opts.state || {};
    st.log ||= [];
    st.props ||= [];
    st.ask ??= "";
    const root = el("section", "ex-ai");
    const label = opts.label || "AI 제안";
    const title = opts.title || label;
    const anchor = el("button", "ex-ai-btn");
    anchor.type = "button";
    anchor.setAttribute("aria-haspopup", "dialog");
    anchor.append(icon("sparkle"), el("span", "", label));
    anchor.addEventListener("click", () => toggle(!st.open));
    const zone = el("div", "ex-ai-zone");
    // Inside a <label>, a click on the bubble's text would jump to the field: keep clicks here.
    zone.addEventListener("click", (e) => { if (!e.target.closest("input, textarea")) e.preventDefault(); });
    // dock: the button with its bubble. A caller may move it into the window's bottom bar (root.dock);
    // the purple list stays in root, where the caller put it in the body.
    const dock = el("span", "ex-ai-dock");
    dock.append(anchor); root.append(dock, zone); root.dock = dock;
    const docked = () => dock.parentNode !== root;
    const toggle = (on, refocus = true) => { st.open = on; st.grow = on; paint(); if (!on && refocus) anchor.focus(); };
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
        const r = (await opts.ask(text, history)) || {};
        const props = Array.isArray(r.props) ? r.props.filter(Boolean) : [];
        if (props.length) { st.props = props.map((p) => ({ ...p, applied: false })); st.open = false; } // the bubble steps aside so the purple list shows
        Object.assign(reply, { busy: false, text: r.say || (props.length ? tr("아래 보라색 목록에 제안을 넣었어요. 보고 적용하세요.") : tr("바꿀 곳을 찾지 못했어요. 말을 바꿔 다시 물어봐 주세요.")) });
      } catch (e) { Object.assign(reply, { busy: false, err: true, text: e?.message || tr("AI가 답하지 못했어요.") }); }
      repaint();
      if (st.props.length && !st.open && docked() && root.isConnected) root.scrollIntoView({ block: "nearest", behavior: "smooth" }); // the list is up in the body, away from the bar
    }
    async function apply(p, on) {
      const r = await (on ? p.apply() : p.undo?.());
      if (r === false) return;
      p.applied = on;
    }

    function bubble() {
      const pop = el("div", `ex-ai-pop${st.grow ? " grow" : ""}`);
      st.grow = false; // the grow plays once, not on every redraw while talking
      pop.setAttribute("role", "dialog");
      pop.setAttribute("aria-label", title);
      const head = el("div", "ex-ai-pop-head");
      const x = el("button", "icon-btn plain");
      x.type = "button";
      x.setAttribute("aria-label", "닫기");
      x.append(icon("x"));
      x.addEventListener("click", () => toggle(false));
      head.append(icon("sparkle"), el("strong", "", title), ...(opts.sub ? [el("small", "", opts.sub)] : []), x);
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
      if (opts.footer) {
        const f = opts.footer;
        const foot = el("button", "ex-ai-foot");
        foot.type = "button";
        const txt = el("span", "");
        txt.append(el("strong", "", f.label), ...(f.hint ? [el("small", "", f.hint)] : []));
        foot.append(txt, el("span", "arrow", "→"));
        foot.addEventListener("click", () => { toggle(false, false); f.onClick?.(); }); // onClick usually redraws the screen: it moves focus, not the old anchor
        pop.append(foot);
      }
      pop.addEventListener("keydown", (e) => { if (e.key === "Escape" && !e.defaultPrevented) { e.preventDefault(); e.stopPropagation(); toggle(false); } });
      requestAnimationFrame(() => {
        // Keep the bubble on screen: docked it opens upward, so it may reach the top edge; below a button, the bottom edge.
        // The chat list (flex: 1, min-height: 0 in CSS) scrolls inside; the head, chips and input row stay in view.
        if (pop.isConnected) { const r = pop.getBoundingClientRect(); pop.style.maxHeight = `${Math.max(160, Math.floor((docked() ? r.bottom : innerHeight - r.top) - 8))}px`; }
        log.scrollTop = log.scrollHeight;
        if (!wait && pop.isConnected) ask.focus({ preventScroll: true });
      });
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
      dock.querySelector(".ex-ai-pop")?.remove();
      if (st.open) (docked() ? dock : zone).append(bubble());
      if (st.props.length) {
        const open = st.props.filter((p) => !p.applied);
        const list = el("div", "ex-props");
        const hd = el("div", "ex-props-head");
        hd.append(icon("sparkle"), el("strong", "", `AI 제안 ${st.props.length}개`), el("small", "", "적용해도 저장하기 전에는 바뀌지 않아요"));
        if (open.length) hd.append(btn("모두 적용", "ghost small", async () => { try { await open.reduce((prev, p) => prev.then(() => apply(p, true)), Promise.resolve()); } finally { repaint(); } }));
        list.append(hd, ...st.props.map(propRow));
        zone.append(list);
      }
      root.hidden = docked() && !st.props.length; // only the list is left in the body
    }
    st.paint = paint;
    paint();
    queueMicrotask(() => { if (st.paint === paint) paint(); }); // again once the caller has docked the button
    return root;
  }

  api.button = (opts = {}) => mount(opts);
  // field(): the sparkle fills this one field at once — improves what is written, or writes it from the context when empty.
  // No chat. The result goes in like typing (Ctrl+Z works, the screen's input listener marks it unsaved); 저장 / 취소 in the note too.
  function field(inp, opts) {
    const st = opts.state || {};
    st.input = inp; // a redraw hands in a new field; the result goes into the one on screen
    const root = el("div", "ex-ai ex-ai-field");
    if (inp.tagName === "TEXTAREA") root.classList.add("is-area");
    if (inp.parentNode) inp.replaceWith(root);
    const title = opts.title || "AI 제안";
    const star = el("button", "ex-ai-spark");
    star.type = "button";
    star.setAttribute("aria-label", tr(`${title} · AI로 채우기`));
    star.title = tr("AI로 채우기");
    star.append(icon("sparkle"));
    const note = el("div", "ex-ai-note");
    note.setAttribute("role", "status");
    // After a fill the box shows what changed in place (added / removed); a click goes back to editing the new text.
    const diff = raw(el("div", "ex-ai-diff")); // the user's own words: not run through the translator
    diff.tabIndex = 0;
    diff.title = tr("눌러서 고치기");
    const edit = () => { if (st.note) st.note.diff = false; show(); inp.focus(); };
    diff.addEventListener("click", edit);
    diff.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); edit(); } });
    root.append(inp, diff, star, note);
    inp.addEventListener("input", (e) => { if (e.isTrusted && !st.writing && st.note) { st.note = null; show(); } }); // the user typed on: the old note (and its undo) is stale
    const write = (v) => {
      const i = st.input;
      if (!i) return;
      st.writing = true;
      try {
        i.focus({ preventScroll: true });
        i.select();
        // insertText keeps the browser's undo stack (Ctrl+Z) and fires input itself; plain assignment is the fallback
        const typed = document.activeElement === i && document.execCommand?.("insertText", false, v); // NOSONAR: only insertText keeps native undo; only into this field
        if (!typed || i.value !== v) { i.value = v; i.dispatchEvent(new Event("input", { bubbles: true })); }
      } finally { st.writing = false; }
    };
    async function run() {
      if (st.busy) return;
      const kit = opts.kit || api.kit;
      const cur = st.input?.value || "";
      st.busy = true; st.note = null; show();
      try {
        if (!kit) throw new Error(tr("AI 도움을 쓸 수 없어요."));
        const task = cur.trim() ? "Improve the current text: keep what the user meant and every link, name and {{variable}} they wrote; make it clearer and complete." : "The field is empty: write it from the context.";
        const prompt = `${opts.preset || "Help write this field."}\n\n${opts.context ? `Context:\n${opts.context()}\n\n` : ""}Current text of the field:\n${cur || "(empty)"}\n\nTask: ${task}\n\nAnswer with ONE JSON object: {"value": "the whole new text for the field"}.`;
        const a = await kit.assist("json", prompt, { persona: { crew: { instructions: "You write one field of a form. Reply with the JSON object only." } } });
        if (a.status !== "done") throw new Error(kit.assistError(a));
        const j = kit.parseJSON(a.output || "");
        if (!j || typeof j.value !== "string") throw new Error(tr("AI의 답을 읽지 못했어요. 다시 해 보세요."));
        let v = j.value.trim();
        if (st.input?.tagName === "INPUT" || opts.oneLine) v = v.split("\n").map((x) => x.replace(/^\s*(?:[-*•]|\d+[.)])\s+/, "").trim()).find(Boolean) || ""; // one line for a one-line box
        const max = st.input?.maxLength > 0 ? st.input.maxLength : 20000;
        v = v.slice(0, max);
        if (!v || v === cur) st.note = { text: tr("더 고칠 곳이 없어요.") };
        else { write(v); st.note = { text: "", undo: cur, diff: true }; }
      } catch (e) { st.note = { text: e?.message || tr("AI가 답하지 못했어요."), err: true }; }
      finally { st.busy = false; show(); if (st.note?.diff) st.input?.closest(".ex-ai-field")?.querySelector(".ex-ai-note button")?.focus(); } // the box is hidden while the diff shows: keep focus on 저장 of the copy on screen (a redraw may have replaced this one)
    }
    star.addEventListener("click", () => void run());
    function show() {
      if (!root.isConnected && st.show && st.show !== show) return st.show(); // a newer copy of this field is on screen
      st.show = show;
      star.disabled = !!st.busy;
      star.classList.toggle("busy", !!st.busy);
      star.setAttribute("aria-busy", String(!!st.busy));
      star.title = tr(st.busy ? "AI가 쓰는 중이에요…" : "AI로 채우기");
      const n = st.note;
      const viewing = !!n?.diff && !st.busy;
      inp.style.display = viewing ? "none" : "";
      diff.hidden = !viewing;
      diff.replaceChildren(...(viewing ? window.ArkismTextDiff(n.undo, inp.value).map(([op, t]) => op === "=" ? document.createTextNode(t) : el(op === "-" ? "del" : "ins", "", t)) : []));
      note.replaceChildren();
      note.className = `ex-ai-note${n?.err ? " err" : ""}`;
      note.hidden = !n;
      if (!n) return;
      if (n.text) note.append(el("span", "", n.text));
      // 저장 keeps the AI text (the screen's own save still sends it); 취소 puts the old text back
      if (n.undo !== undefined) note.append(btn("저장", "small", () => { st.note = null; show(); }), btn("취소", "ghost small", () => { write(n.undo); st.note = null; show(); }));
    }
    st.show = show; // this copy is the one going on screen (as mount() sets st.paint)
    show();
    return root;
  }

  api.field = (inp, opts = {}) => field(inp, opts);
  return api;
})();
