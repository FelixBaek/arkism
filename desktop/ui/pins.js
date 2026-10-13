// AGENT › 작업 에이전트 · 팀 — 항상 넣는 자료 (Figma 381:28425, 381:29154).
// A work agent or a team keeps `pinned[]` on its crew record: {collection, id, revision (0 = latest approved; a
// skill's version number), injection full|summary|reference, priority 0-100}, at most 20. This card edits the
// draft's list (crew.js / team.js save the record as before) and shows how each saved pin resolves right now
// (GET /v1/context/pins?agent=@handle): a pin that is not ok is not injected. Only approved records of the same
// scope can be picked; the server refuses anything else.
window.ArkismPins = (ctx) => {
  "use strict";
  const { state, el, button, request, query } = ctx;
  const raw = (n) => { n.setAttribute("data-no-i18n", ""); return n; };
  const tr = (s) => (window.ArkismI18n ? window.ArkismI18n.translate(s) : s);

  const MAX = 20;
  const KINDS = [["memories", "기억"], ["skills", "스킬"], ["knowledge_sources", "지식"]];
  const kindName = (c) => KINDS.find(([k]) => k === c)?.[1] || c;
  const MODES = [["full", "전체"], ["summary", "요약"], ["reference", "참조"]];
  const MEMORY_KIND = { user: "선호", procedural: "작업 규칙", decision: "결정", lesson: "교훈", episodic: "기록", semantic: "사실" };
  // Short chip label · the sentence for the 확인해 주세요 card.
  const STATUS = {
    not_found: ["없어짐", "자료가 지워졌거나 찾을 수 없어요."],
    not_approved: ["승인 취소", "승인이 취소됐거나 다른 기억으로 바뀌었어요."],
    revision_mismatch: ["버전 바뀜", "고정한 버전이 바뀌었어요."],
    secret_blocked: ["비밀값", "비밀값이 들어 있어서 넣지 않아요."],
    denied: ["권한 없음", "이 자료를 읽을 권한이 없어요."]
  };
  // Server caps (internal/memory/crew_pins.go): characters each injection mode keeps.
  const CAP = { full: 8000, summary: 380, reference: 80 };
  const tokensOf = (chars) => Math.max(1, Math.round(chars / 2.5)); // 약: Korean and code mixed, not a tokenizer
  const num = (n) => Number(n || 0).toLocaleString("ko-KR");
  const firstLine = (s) => String(s || "").trim().split("\n")[0].slice(0, 120);

  // ---------------------------------------------------------------- what can be pinned (same scope, approved only)
  const lib = { key: "", at: 0, knowledge: [], loading: null };
  // Every card on screen redraws when the library or a resolution arrives (a page redraw may have replaced the one that asked).
  const live = new Set();
  const redrawAll = () => { for (const f of live) f(); };
  function loadLibrary() {
    const key = query();
    if (lib.key === key && Date.now() - lib.at < 60000) return;
    if (lib.loading) return;
    lib.loading = request(`/v1/knowledge/sources?${query({ limit: 500 })}`).then((r) => r.items || [], () => [])
      .then((items) => { Object.assign(lib, { key, at: Date.now(), knowledge: items, loading: null }); redrawAll(); });
  }
  const skillVersions = (s) => Math.max(1, (s.version_history || []).length);
  const skillActive = (s) => { const i = (s.version_history || []).indexOf(s.active_version); return i >= 0 ? i + 1 : skillVersions(s); };
  function candidates(kind) {
    if (kind === "memories") return (state.data.memories || []).filter((m) => m.status === "active" && m.approved_by && !m.content_erased)
      .map((m) => ({ collection: kind, id: m.id, title: firstLine(m.text), meta: MEMORY_KIND[m.kind] || "기억", revision: m.revision, chars: (m.text || "").length }));
    if (kind === "skills") return (state.data.skills || []).filter((s) => s.status === "active" && s.active_version)
      .map((s) => ({ collection: kind, id: s.id, title: s.name, meta: firstLine(s.description), mono: true, versions: skillVersions(s), active: skillActive(s), chars: (s.description || "").length * 6 }));
    return lib.knowledge.filter((k) => k.status === "active" && k.trust_level === "reviewed" && k.approved_by)
      .map((k) => ({ collection: kind, id: k.id, title: k.display_name || k.id, meta: tr(`문서 · 조각 ${k.chunk_count || 0}개`), revision: k.revision, chars: (k.chunk_count || 1) * 600 }));
  }
  const known = (p) => candidates(p.collection).find((c) => c.id === p.id) || null;

  // ---------------------------------------------------------------- how saved pins resolve now
  const res = new Map(); // `${crew id}:${revision}` → {items} | {loading} | {error}
  function resolution(rec) {
    const key = `${rec.id}:${rec.revision}`;
    const got = res.get(key);
    if (got) return got.items ? got : null;
    if (!rec.crew?.handle || !(rec.crew.pinned || []).length) return null;
    res.set(key, { loading: true });
    request(`/v1/context/pins?${query({ agent: `@${rec.crew.handle}` })}`)
      .then((r) => { res.set(key, { items: r.items || [] }); redrawAll(); }, () => res.set(key, { error: true }));
    return null;
  }
  const same = (a, b) => a.collection === b.collection && a.id === b.id;
  // A pin's status counts only while it is the pin that was saved (same record and revision).
  function statusOf(rec, p, r) {
    const saved = (rec.crew?.pinned || []).find((q) => same(q, p) && (q.revision || 0) === (p.revision || 0));
    const item = saved && r?.items.find((x) => same(x.pin, saved) && (x.pin.revision || 0) === (saved.revision || 0) && x.pin.injection === saved.injection);
    return item || null;
  }

  // ---------------------------------------------------------------- the card
  // opts: {rec (saved record), draft (crew draft; draft.pinned is edited), sub (the line under the title, translated),
  //        noun ("이 에이전트는" | "이 팀은", translated), onChange (draft changed), cls (card class)}
  const kept = new Set(); // "유지" pressed on a newer-version notice, this session only
  const ui = { kind: "memories", q: "" };
  function card(opts) {
    const { rec, draft } = opts;
    const root = el("section", `${opts.cls || "wk-card"} pn-card`);
    const redraw = () => { if (root.isConnected) draw(); else live.delete(redraw); };
    for (const f of live) if (!f.root?.isConnected) live.delete(f);
    redraw.root = root;
    live.add(redraw);
    const changed = () => { opts.onChange(); draw(); };
    const pins = () => draft.pinned || (draft.pinned = []);
    const ordered = () => pins().map((p, i) => [p, i]).sort((a, b) => b[0].priority - a[0].priority || a[1] - b[1]).map(([p]) => p);
    // Priorities follow the shown order: 100, 95, 90 … so 위에 있을수록 먼저.
    const renumber = (list) => { list.forEach((p, i) => { p.priority = Math.max(0, 100 - i * 5); }); draft.pinned = list; };
    let searchBox = null;

    function info(p, r) {
      const c = known(p);
      const hit = statusOf(rec, p, r)?.item;
      const title = c?.title || firstLine(hit?.summary || hit?.text) || p.id;
      // Text that would go in for the chosen mode: the resolved item when the saved mode is unchanged, else an estimate.
      const savedMode = (rec.crew?.pinned || []).find((q) => same(q, p))?.injection;
      const chars = hit && savedMode === p.injection ? (hit.text || "").length + (hit.summary || "").length : Math.min(CAP[p.injection] || 0, c?.chars || hit?.text?.length || 0);
      return { c, title, tokens: chars ? tokensOf(chars) : 0 };
    }
    function versionOptions(p, c) {
      const out = [[0, "최신"]];
      if (p.collection === "skills") {
        const n = Math.max(c?.versions || 0, p.revision || 0);
        for (let v = n; v >= 1; v--) out.push([v, tr(`고정 v${v}`)]);
      } else {
        if (c?.revision) out.push([c.revision, tr(`고정 v${c.revision}`)]);
        if (p.revision && p.revision !== c?.revision) out.push([p.revision, tr(`고정 v${p.revision}`)]);
      }
      return out;
    }
    function pick(options, value, label, onPick) {
      const s = el("select", "pn-select");
      s.setAttribute("aria-label", label);
      for (const [k, l] of options) { const o = el("option", "", l); o.value = String(k); o.selected = String(k) === String(value); s.append(o); }
      s.addEventListener("change", () => onPick(s.value));
      return s;
    }
    function row(p, i, list, r) {
      const { c, title, tokens } = info(p, r);
      const st = statusOf(rec, p, r);
      const bad = st && st.status !== "ok";
      const line = el("div", `pn-row${bad ? " bad" : ""}`);
      const order = el("div", "pn-order");
      order.title = tr(`우선순위 ${p.priority}`);
      const move = (d) => { const l = list.slice(); [l[i], l[i + d]] = [l[i + d], l[i]]; renumber(l); changed(); };
      const up = el("button", "pn-move", "↑"); up.type = "button"; up.disabled = i === 0; up.setAttribute("aria-label", "위로"); up.addEventListener("click", () => move(-1));
      const down = el("button", "pn-move", "↓"); down.type = "button"; down.disabled = i === list.length - 1; down.setAttribute("aria-label", "아래로"); down.addEventListener("click", () => move(1));
      order.append(el("span", "pn-num", String(i + 1).padStart(2, "0")), up, down);
      const what = el("div", "pn-what");
      const name = el("div", "pn-name");
      name.append(el("span", `pn-kind ${p.collection}`, kindName(p.collection)), raw(el("span", c?.mono ? "pn-title mono" : "pn-title", title)));
      if (bad) { const chip = el("span", "wf-badge small fail", STATUS[st.status]?.[0] || st.status); chip.title = tr(STATUS[st.status]?.[1] || ""); name.append(chip); }
      const meta = el("small", "pn-meta");
      const bits = [];
      if (p.collection === "skills") bits.push(p.revision ? tr(`고정 v${p.revision}`) : c ? tr(`최신 v${c.active}`) : "최신");
      else bits.push(c?.meta || kindName(p.collection));
      if (bad) bits.push("넣지 않아요");
      else if (tokens) bits.push(tr(`약 ${num(tokens)} 토큰`));
      meta.append(bits.map(tr).join(" · "));
      if (p.collection === "skills" && p.revision && c && c.active > p.revision) meta.append(" · ", el("span", "pn-new", tr(`새 버전 v${c.active} 있음`)));
      what.append(name, meta);
      const mode = pick(MODES, p.injection, "넣는 방식", (v) => { p.injection = v; changed(); });
      const ver = pick(versionOptions(p, c), p.revision || 0, "버전", (v) => { p.revision = Number(v) || 0; changed(); });
      const del = el("button", "pn-del", "빼기"); del.type = "button";
      del.addEventListener("click", () => { renumber(list.filter((x) => x !== p)); changed(); });
      line.append(order, what, mode, ver, del);
      return line;
    }
    // 확인해 주세요: the first pin that needs a decision.
    function attention(list, r) {
      for (const p of list) {
        const c = known(p);
        if (p.collection === "skills" && p.revision && c && c.active > p.revision && !kept.has(`${p.id}:${c.active}:${p.revision}`)) {
          return { text: tr(`${c.title} 스킬에 v${c.active}가 나왔어요. ${opts.noun} v${p.revision}로 고정돼 있어요.`), acts: [
            [tr(`v${c.active}로 바꾸기`), "primary", () => { p.revision = c.active; changed(); }],
            [tr(`v${p.revision} 유지`), "", () => { kept.add(`${p.id}:${c.active}:${p.revision}`); draw(); }]] };
        }
        const st = statusOf(rec, p, r);
        if (st && st.status !== "ok") {
          const title = info(p, r).title;
          const acts = [["빼기", "", () => { renumber(list.filter((x) => x !== p)); changed(); }]];
          if (st.status === "revision_mismatch") acts.unshift(["최신으로 바꾸기", "primary", () => { p.revision = 0; changed(); }]);
          return { text: `${tr(kindName(p.collection))} '${title}' — ${tr(STATUS[st.status]?.[1] || st.status)} ${tr("이 자료는 넣지 않아요.")}`, acts, mixed: true };
        }
      }
      return null;
    }
    function alertBox(a) {
      const box = el("div", "pn-alert");
      const p = el("p", "", a.text);
      if (a.mixed) raw(p);
      const acts = el("div", "pn-alert-acts");
      for (const [label, cls, fn] of a.acts) acts.append(button(label, `${cls} small`.trim(), null, fn));
      box.append(p, acts);
      return box;
    }
    function picker(list) {
      const wrap = el("div", "pn-add");
      const bar = el("div", "pn-add-bar");
      const search = el("label", "search pn-search");
      const q = el("input");
      q.type = "search";
      q.placeholder = "기억·스킬·지식에서 찾아 넣기";
      q.setAttribute("aria-label", "넣을 자료 찾기");
      q.value = ui.q;
      q.disabled = list.length >= MAX;
      search.append(el("span", "pn-glass", "⌕"), q);
      searchBox = q;
      const chips = el("div", "pn-kinds");
      const drop = el("div", "pn-drop");
      const fill = () => {
        const taken = new Set(list.map((p) => `${p.collection}\x00${p.id}`));
        const words = ui.q.toLowerCase();
        const found = candidates(ui.kind).filter((c) => !taken.has(`${c.collection}\x00${c.id}`) && (!words || `${c.title} ${c.meta}`.toLowerCase().includes(words))).slice(0, 8);
        drop.hidden = !wrap.classList.contains("open");
        drop.replaceChildren(...found.map((c) => {
          const b = el("button", "pn-option");
          b.type = "button";
          b.append(raw(el("span", c.mono ? "mono" : "", c.title)), raw(el("small", "", c.meta || "")));
          b.addEventListener("mousedown", (e) => e.preventDefault()); // keep the box open while picking
          b.addEventListener("click", () => {
            const next = [...list, { collection: c.collection, id: c.id, revision: 0, injection: c.collection === "knowledge_sources" ? "summary" : "full", priority: 0 }];
            renumber(next);
            ui.q = "";
            changed();
            searchBox?.focus();
          });
          return b;
        }));
        if (!found.length) drop.append(el("p", "pn-none", ui.kind === "knowledge_sources" && lib.loading ? "불러오는 중…" : "넣을 수 있는 승인된 자료가 없어요."));
      };
      const drawChips = () => chips.replaceChildren(...KINDS.map(([k, label]) => {
        const b = el("button", `pn-kindchip${ui.kind === k ? " on" : ""}`, label);
        b.type = "button";
        b.setAttribute("aria-pressed", String(ui.kind === k));
        b.addEventListener("mousedown", (e) => e.preventDefault());
        b.addEventListener("click", () => { ui.kind = k; wrap.classList.add("open"); drawChips(); fill(); });
        return b;
      }));
      q.addEventListener("focus", () => { wrap.classList.add("open"); fill(); });
      q.addEventListener("blur", () => { wrap.classList.remove("open"); drop.hidden = true; });
      q.addEventListener("input", () => { ui.q = q.value.trim(); fill(); });
      q.addEventListener("keydown", (e) => { if (e.key === "Escape") q.blur(); });
      drawChips();
      fill();
      bar.append(search, chips);
      wrap.append(bar, drop);
      return wrap;
    }
    function draw() {
      loadLibrary();
      const r = resolution(rec);
      const list = ordered();
      const head = el("div", "pn-head");
      const words = el("div", "pn-head-words");
      const t = el("div", "pn-head-title");
      t.append(el("h3", "", "항상 넣는 자료"), el("span", "pn-count", tr(`고정 장착 ${list.length}`)));
      words.append(t, raw(el("p", "", opts.sub)));
      const add = button("+ 자료 넣기", "small", null, () => { if (list.length >= MAX) return; ui.q = ""; searchBox?.focus(); });
      add.disabled = list.length >= MAX;
      head.append(words, add);
      const kids = [head];
      const a = attention(list, r);
      if (a) kids.push(alertBox(a));
      if (list.length) {
        const table = el("div", "pn-table");
        const th = el("div", "pn-row pn-th");
        th.append(el("span", "pn-order", "순서"), el("span", "pn-what", "자료"), el("span", "", "넣는 방식"), el("span", "", "버전"), el("span", "pn-del-h"));
        table.append(th, ...list.map((p, i) => row(p, i, list, r)));
        kids.push(table);
      } else kids.push(el("p", "pn-empty", "아직 없어요. 늘 알아야 하는 결정·스킬·문서를 넣어 두면, 부를 때마다 검색보다 먼저 들어가요."));
      kids.push(picker(list));
      const total = list.reduce((n, p) => n + (statusOf(rec, p, r)?.status && statusOf(rec, p, r).status !== "ok" ? 0 : info(p, r).tokens), 0);
      const legend = "전체=다 넣기 · 요약=줄여서 넣기 · 참조=제목만 넣고 필요할 때 읽기 · 고정=그 버전만, 최신=바뀌면 따라가기";
      kids.push(el("p", "pn-foot", list.length ? `${tr(`합계 약 ${num(total)} 토큰`)} · ${tr(`최대 ${MAX}개`)} · ${tr(legend)}` : tr(legend)));
      root.replaceChildren(...kids);
    }
    draw();
    return root;
  }

  // The same 확인해 주세요 line for the right column (Figma: the agent detail repeats it at the top).
  function sideNotice(draft, onChange, redraw) {
    const p = (draft.pinned || []).find((x) => { const c = x.collection === "skills" && x.revision ? known(x) : null; return c && c.active > x.revision && !kept.has(`${x.id}:${c.active}:${x.revision}`); });
    if (!p) return null;
    const c = known(p);
    const box = el("section", "pn-alert pn-side");
    box.append(el("p", "", tr(`고정한 스킬 ${c.title}에 v${c.active}가 나왔어요. 바꾸기 전까지는 v${p.revision}를 계속 써요.`)));
    const acts = el("div", "pn-alert-acts");
    acts.append(button(tr(`v${c.active}로 바꾸기`), "primary small", null, () => { p.revision = c.active; onChange(); }),
      button("나중에", "ghost small", null, () => { kept.add(`${p.id}:${c.active}:${p.revision}`); redraw(); }));
    box.append(acts);
    return box;
  }

  return { card, sideNotice, _test: { tokensOf } };
};
