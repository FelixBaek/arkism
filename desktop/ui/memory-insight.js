// @ts-check
// MEMORY › 기억 › 모아보기: 물어보기 (POST /v1/reflect), 규칙 (/v1/directives), 자주 나온 이름 (/v1/entities).
// Same panel shape as intelligence.js (draw · reset · detail · close). Server text is data: drawn as plain text, never markup.
// Rules and name edits are a person's own writes, so only a human session sees those buttons (the server refuses the rest).
window.ArkismMemoryInsight = (ctx, tab) => {
  "use strict";
  const { state, $, el, raw, icon, button, request, query, detailShell, openDetail, notice, section, facts, callout } = ctx;
  const human = () => state.caps?.role === "human";
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const active = () => state.view === "memories" && state.memTab === tab;
  const day = (v) => (v ? new Date(v).toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { year: "numeric", month: "long", day: "numeric" }) : "—");
  const APPLIES = { both: "답할 때와 에이전트에 기억을 줄 때", reflect: "물어보기에 답할 때만", context: "에이전트에 기억을 줄 때만" };
  const TYPES = { person: "사람", project: "프로젝트", repo: "저장소", tool: "도구", product: "제품", file: "파일", service: "서비스", place: "장소", organization: "단체", concept: "개념" };
  const TITLE = { ask: "물어보기", rules: "규칙", entities: "자주 나온 이름" };
  const loads = globalThis.ArkismKeep.latest();
  let key = "", selected = null, editing = null, busy = false, answer = null, failure = null, question = "", limit = "", q = "";
  let memo = new Map(), views = new Map();
  // 규칙 · 자주 나온 이름 pages go through the shared cursor pager (memory-pages.js): order, repeats and stale reads handled there.
  const pager = window.ArkismMemoryPages.create(async (k, at) => {
    const { find } = JSON.parse(k);
    const out = await request(`${tab === "rules" ? "/v1/directives" : "/v1/entities"}?${query({ limit: at.limit, ...(at.cursor ? { cursor: at.cursor } : {}), ...(find ? { q: find } : {}) })}`);
    if (!Array.isArray(out?.items) || out.items.some((r) => typeof (tab === "rules" ? r?.rule : r?.name) !== "string")) throw new Error("invalid_response");
    // These pages carry no authority label; their rows are drawn as plain text all the same.
    return { items: out.items, next_cursor: typeof out.next_cursor === "string" ? out.next_cursor : "", authority: "data_only_untrusted" };
  });
  const pageKey = () => JSON.stringify({ id: identity(), find: tab === "entities" ? q : "" });
  const page = () => { const s = pager.snapshot(); return s.key === pageKey() ? s : { items: [], cursor: "", ready: false, loading: false, error: "" }; };
  const rows = () => page().items;
  function reset() { loads.cancel(); pager.reset(); key = ""; selected = null; editing = null; busy = false; answer = null; failure = null; memo = new Map(); views = new Map(); }
  const live = (ticket, owner) => () => ticket.live() && active() && identity() === owner;
  const formHere = () => $("#chips .insight-form")?.dataset.tab === tab;

  // A cited or mentioning memory: its text (or the server's snippet), one tap opens it in the memory screen.
  function memoryLink(id, snippet) {
    const item = el("button", "dg-item"); item.type = "button";
    const text = raw(el("p", "", snippet || "불러오는 중…"));
    item.append(text);
    const read = () => {
      if (!memo.has(id)) memo.set(id, request(`/v1/memories/${encodeURIComponent(id)}?${query()}`).then((r) => (r?.id === id ? r : null), () => null));
      return memo.get(id);
    };
    const show = (r) => { if (!snippet) text.textContent = r ? r.text : "읽을 수 없는 기억이에요 (지워졌거나 바뀜)"; item.disabled = !(r?.status === "active"); return r; };
    if (!snippet) void read().then(show);
    item.addEventListener("click", async () => {
      const r = show(await read());
      if (r?.status === "active") ctx.openRecord(r); else notice("이 기억은 지금 열 수 없어요. 지워졌거나 아직 승인되지 않았어요.");
    });
    return item;
  }
  function more(...nodes) { const d = el("details", "insight-more"); d.append(el("summary", "", "자세히"), ...nodes); return d; }
  function field(label, input) { const l = el("label", "insight-field"); l.append(el("span", "", label), input); return l; }

  // ------------------------------------------------------------ 물어보기
  function drawAsk() {
    if (!formHere()) {
      const form = el("form", "original-search insight-form"); form.dataset.tab = tab;
      const box = el("label", "panel-search");
      const input = el("input"); input.type = "text"; input.maxLength = 4000; input.value = question;
      input.placeholder = "궁금한 것 (예: 배포 전에 늘 하는 일은?)"; input.setAttribute("aria-label", "물어볼 내용");
      box.append(icon("magnifying-glass"), input);
      const n = el("input"); n.type = "number"; n.min = "1"; n.max = "20"; n.value = limit; n.placeholder = "알아서";
      const go = button("물어보기", "primary small"); go.type = "submit";
      form.append(box, go, more(field("참고할 기억 수 (1~20)", n)));
      form.addEventListener("submit", (e) => {
        e.preventDefault();
        question = input.value.trim(); limit = n.value;
        if (!question) { input.setCustomValidity("물어볼 내용을 적어 주세요."); input.reportValidity(); return; }
        void ask();
      });
      input.addEventListener("input", () => input.setCustomValidity(""));
      $("#chips").replaceChildren(form);
    }
    paintAsk();
  }
  async function ask() {
    if (busy) return;
    const now = live(loads.begin(), identity());
    busy = true; failure = null; answer = null; paintAsk();
    try {
      const n = Number(limit);
      const out = await request("/v1/reflect", { method: "POST", body: JSON.stringify({ scope: state.scope, query: question, ...(Number.isInteger(n) && n >= 1 && n <= 20 ? { limit: n } : {}) }) });
      if (!now()) return;
      if (typeof out?.answer !== "string" || !Array.isArray(out.cited_memory_ids) || out.cited_memory_ids.some((id) => typeof id !== "string" || !id)) throw new Error("invalid_response");
      answer = out;
    } catch (e) { if (now()) failure = e; }
    finally { if (now()) { busy = false; paintAsk(); } }
  }
  function askError(e) {
    const code = e?.message;
    if (code === "model_not_configured") return callout("모델 설정 필요", "서버에 답을 쓸 AI 모델이 아직 연결되지 않았어요. 서버에서 모델을 정하면 물어볼 수 있어요.");
    if (code === "model_budget_exceeded") return callout("오늘 쓸 AI 사용량을 다 썼어요", "내일 다시 물어보거나 질문을 짧게 줄여 주세요.");
    if (code === "access_denied") return callout("물어볼 수 없는 계정이에요", "이 계정에는 기억을 읽거나 AI에 보낼 권한이 없어요.");
    return callout("답을 받지 못했어요", "잠시 뒤 다시 물어봐 주세요.");
  }
  function paintAsk() {
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const status = el("p", "meta original-notice", busy ? "기억에서 답을 찾는 중…" : !answer && !failure ? "승인된 기억만 보고 답해요. 답은 저장하지 않아요." : "");
    status.setAttribute("role", "status"); table.append(status);
    if (failure) { table.append(askError(failure)); return; }
    if (!answer) return;
    const box = el("div", "quote dg-quote insight-answer");
    box.append(raw(el("p", "", answer.answer || "답할 만한 기억을 찾지 못했어요.")));
    const ids = answer.cited_memory_ids.slice(0, 20), list = el("div", "dg-list");
    for (const id of ids) list.append(memoryLink(id));
    table.append(box, el("h3", "insight-head", ids.length ? `근거 기억 ${ids.length}개` : "근거로 든 기억이 없어요"), list,
      more(facts([["찾아본 기억", `${answer.retrieved?.length || 0}개`], ["지킨 규칙", `${answer.directives_applied?.length || 0}개`]])));
  }

  // ------------------------------------------------------------ 규칙 · 자주 나온 이름 (lists)
  // how: "load" (first or next page), "retry", "refresh" (after a write: every loaded page again)
  async function loadList(how = "refresh") {
    const k = pageKey(), run = pager[how](k);
    paintList();
    await run.catch(() => {});
    if (k !== pageKey()) return;
    if (selected) selected = rows().find((r) => r.id === selected.id) || selected;
    paintList();
  }
  function drawList() {
    if (!formHere()) {
      const bar = el("div", "mem-tools insight-form"); bar.dataset.tab = tab;
      if (tab === "entities") {
        const form = el("form", "original-search"), box = el("label", "panel-search");
        const input = el("input"); input.type = "search"; input.maxLength = 200; input.value = q; input.placeholder = "이름 찾기"; input.setAttribute("aria-label", "이름 찾기");
        box.append(icon("magnifying-glass"), input);
        const go = button("찾기", "small"); go.type = "submit";
        form.append(box, go);
        form.addEventListener("submit", (e) => { e.preventDefault(); q = input.value.trim(); selected = null; void loadList("load"); });
        bar.append(form);
      } else if (human()) bar.append(button("규칙 추가", "primary small", null, () => { selected = null; editing = {}; paintList(); openDetail(); detail(); }));
      else bar.append(el("p", "meta", "규칙은 사람만 만들고 바꿀 수 있어요."));
      $("#chips").replaceChildren(bar);
      const s = page();
      if (!s.ready && !s.loading && !s.error) void loadList("load");
    }
    paintList();
  }
  function listRow(item, title, meta, badge, warn) {
    const row = el("button", `original-row${selected?.id === item.id ? " selected" : ""}`); row.type = "button";
    const body = el("span", "original-body");
    body.append(raw(el("strong", "", title)), el("span", "meta", meta));
    row.append(body, el("span", `mem-badge${warn ? " warn" : ""}`, badge));
    row.addEventListener("click", () => { selected = item; editing = null; paintList(); openDetail(); detail(); const h = $("#detail h2"); if (h) { h.tabIndex = -1; h.focus(); } });
    return row;
  }
  function paintList() {
    if (!active()) return;
    const s = page(), table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(s.loading));
    const rules = tab === "rules";
    const empty = rules ? "아직 규칙이 없어요. 규칙을 적어 두면 AI가 답할 때 꼭 지켜요." : q ? "검색과 맞는 이름이 없어요." : "아직 모인 이름이 없어요. 승인된 기억에 사람·도구·프로젝트 이름이 나오면 여기에 모여요.";
    const error = s.error ? "불러오지 못했어요. 연결과 접근 권한을 확인하고 다시 불러와 주세요." : "";
    const status = el("p", "meta original-notice", s.loading && !s.items.length ? "불러오는 중…" : error || (s.ready && !s.items.length ? empty : ""));
    status.setAttribute("role", "status"); table.append(status);
    for (const r of s.items) {
      table.append(rules ? listRow(r, r.title, `${APPLIES[r.applies_to] || r.applies_to} · 우선순위 ${r.priority}`, r.active ? "켜짐" : "꺼짐", !r.active)
        : listRow(r, r.name, `${TYPES[r.type] || r.type} · 기억 ${r.mentions ?? 0}개`, r.proposed ? "확인 필요" : "확인됨", r.proposed));
    }
    if (error) table.append(button("다시 불러오기", "small", null, () => loadList("retry")));
    else if (s.cursor) table.append(button("더 보기", "small", null, () => loadList("load")));
  }

  // ------------------------------------------------------------ 규칙 detail and form
  const ruleFields = (r) => ({ title: r.title, rule: r.rule, priority: r.priority, applies_to: r.applies_to, active: r.active, tags: r.tags || [] });
  function ruleError(e) {
    return { revision_conflict: "그사이 다른 곳에서 바뀌었어요. 새로 불러왔어요.", access_denied: "이 계정은 규칙을 바꿀 수 없어요.",
      secret_detected: "비밀번호나 키처럼 보이는 내용은 규칙에 넣을 수 없어요." }[e?.message] || `처리하지 못했어요 (${e?.message})`;
  }
  async function writeRule(work, done, keep = true) {
    const id = selected?.id;
    try {
      const out = await work();
      notice(done);
      selected = keep && out?.id ? out : null; editing = null;
    } catch (e) { notice(ruleError(e)); }
    await loadList();
    if (keep && id && !selected) selected = rows().find((r) => r.id === id) || null;
    detail();
  }
  const saveRule = (fields, prev) => writeRule(() => request(prev ? `/v1/directives/${encodeURIComponent(prev.id)}` : "/v1/directives",
    { method: "POST", body: JSON.stringify({ scope: state.scope, ...fields, ...(prev ? { expected_revision: prev.revision } : {}) }) }),
  prev ? (fields.active === prev.active ? "규칙을 바꿨어요." : fields.active ? "규칙을 켰어요. 지금부터 지켜요." : "규칙을 껐어요.") : "규칙을 만들었어요. 지금부터 지켜요.");
  function ruleDetail(body) {
    const r = selected;
    if (!r) { body.append(el("h2", "", "규칙을 고르세요"), el("p", "lead", "AI가 답하거나 에이전트에게 기억을 줄 때 꼭 지키는 문장이에요. 사람이 적은 규칙만 쓰여요.")); return; }
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", r.active ? "규칙 · 켜짐" : "규칙 · 꺼짐"), raw(el("h2", "", r.title)));
    const text = section("내용"); text.append(raw(el("p", "original-text", r.rule)));
    const use = section("언제 쓰나요");
    use.append(facts([["쓰는 때", APPLIES[r.applies_to] || r.applies_to], ["우선순위", `${r.priority} · 클수록 먼저 지켜요`], ["적은 사람", r.created_by], ["고친 날", day(r.updated_at)]]));
    body.append(head, text, use);
    if (!human()) { body.append(el("p", "meta", "규칙은 사람만 만들고 바꿀 수 있어요.")); return; }
    const btns = el("div", "d-buttons");
    const lock = () => btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    btns.append(button(r.active ? "끄기" : "켜기", "primary small", null, () => { lock(); void saveRule({ ...ruleFields(r), active: !r.active }, r); }),
      button("고치기", "small", null, () => { editing = r; detail(); }),
      button("지우기", "ghost small", null, async () => {
        if (!await window.ask("이 규칙을 지울까요?\n지우면 AI가 더 이상 지키지 않아요.", "지우기")) return;
        lock();
        void writeRule(() => request(`/v1/directives/${encodeURIComponent(r.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: r.revision }) }), "규칙을 지웠어요.", false);
      }));
    body.append(btns);
  }
  function ruleForm(body) {
    const r = editing.id ? editing : null;
    body.append(el("h2", "", r ? "규칙 고치기" : "새 규칙"), el("p", "lead", "AI가 꼭 지킬 문장을 적어 주세요. 저장하면 바로 쓰여요."));
    const form = el("form", "insight-edit");
    const title = el("input"); title.maxLength = 200; title.required = true; title.value = r?.title || ""; title.placeholder = "예: 배포 규칙";
    const rule = el("textarea"); rule.maxLength = 4000; rule.rows = 5; rule.required = true; rule.value = r?.rule || ""; rule.placeholder = "예: 운영 서버에 올리기 전에는 꼭 사람에게 물어본다.";
    const where = el("select");
    for (const [k, l] of Object.entries(APPLIES)) { const o = el("option", "", l); o.value = k; where.append(o); }
    where.value = r?.applies_to || "both";
    const priority = el("input"); priority.type = "number"; priority.min = "0"; priority.max = "100"; priority.value = r ? String(r.priority) : ""; priority.placeholder = "알아서";
    const btns = el("div", "d-buttons");
    const save = button("저장", "primary small"); save.type = "submit";
    btns.append(save, button("취소", "ghost small", null, () => { editing = null; detail(); }));
    form.append(field("제목", title), field("규칙 내용", rule), more(field("쓰는 때", where), field("우선순위 (0~100)", priority)), btns);
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      if (!title.value.trim() || !rule.value.trim()) { (title.value.trim() ? rule : title).reportValidity(); return; }
      const p = priority.value === "" ? null : Number(priority.value);
      save.disabled = true;
      void saveRule({ title: title.value.trim(), rule: rule.value.trim(), applies_to: where.value, ...(Number.isInteger(p) && p >= 0 && p <= 100 ? { priority: p } : {}),
        active: r ? r.active : true, tags: r?.tags || [] }, r);
    });
    body.append(form);
    title.focus();
  }

  // ------------------------------------------------------------ 자주 나온 이름 detail
  function entityError(e) {
    return { revision_conflict: "그사이 다른 곳에서 바뀌었어요. 새로 불러왔어요.", access_denied: "이 계정은 이름을 고칠 수 없어요.", not_found: "이미 없어진 이름이에요." }[e?.message] || `처리하지 못했어요 (${e?.message})`;
  }
  async function editEntity(id, work, done, next) {
    try { const out = await work(); notice(done); views.clear(); await loadList(); selected = rows().find((r) => r.id === (next === undefined ? out?.id || id : next)) || null; }
    catch (e) { notice(entityError(e)); views.delete(id); }
    detail();
  }
  const patch = (id, change, done) => editEntity(id, () => request(`/v1/entities/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ scope: state.scope, ...change }) }), done);
  function entityDetail(body) {
    const item = selected;
    if (!item) { body.append(el("h2", "", "이름을 고르세요"), el("p", "lead", "승인된 기억에 나온 사람·도구·프로젝트 이름이에요. 고르면 그 이름이 나온 기억을 볼 수 있어요.")); return; }
    const owner = identity();
    if (!views.has(item.id)) {
      views.set(item.id, null);
      request(`/v1/entities/${encodeURIComponent(item.id)}?${query()}`).then((v) => (v?.id === item.id && Array.isArray(v.memories) ? v : false), () => false)
        .then((v) => { if (identity() !== owner) return; views.set(item.id, v); if (selected?.id === item.id && active() && !document.querySelector("#detail [data-sync-dirty]")) detail(); });
    }
    const v = views.get(item.id), e = v || item;
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", `${TYPES[e.type] || e.type}${e.proposed ? " · AI가 찾은 이름이라 확인이 필요해요" : ""}`), raw(el("h2", "", e.name)));
    body.append(head);
    const can = human();
    const aliases = section("다른 이름");
    const chips = el("div", "insight-aliases");
    for (const a of e.aliases || []) {
      const chip = el("span", "chip"); chip.append(raw(el("span", "", a)));
      if (can) { const x = button("빼기", "link-btn", null, () => patch(item.id, { remove_aliases: [a] }, "다른 이름을 뺐어요.")); x.setAttribute("aria-label", `${a} 빼기`); chip.append(x); }
      chips.append(chip);
    }
    if (!(e.aliases || []).length) chips.append(el("p", "meta", "다른 이름이 없어요."));
    aliases.append(chips);
    body.append(aliases);
    const mentions = section("나온 기억", v ? `${v.mentions}개` : "");
    if (v === null) mentions.append(el("p", "meta", "불러오는 중…"));
    else if (v === false) mentions.append(el("p", "meta", "불러오지 못했어요. 다시 골라 주세요."));
    else if (!v.memories.length) mentions.append(el("p", "meta", "읽을 수 있는 기억이 없어요."));
    else { const list = el("div", "dg-list"); for (const m of v.memories.slice(0, 20)) if (typeof m?.id === "string") list.append(memoryLink(m.id, m.snippet)); mentions.append(list); }
    body.append(mentions);
    if (v?.related?.length) {
      const near = section("같이 나온 이름"), list = el("div", "insight-aliases");
      for (const r of v.related.slice(0, 12)) {
        const b = el("button", "chip"); b.type = "button"; b.append(raw(el("span", "", r.name)));
        b.addEventListener("click", () => { selected = rows().find((x) => x.id === r.id) || { id: r.id, name: r.name, type: r.type, aliases: [] }; paintList(); detail(); });
        list.append(b);
      }
      near.append(list); body.append(near);
    }
    if (!can) { body.append(el("p", "meta", "이름 고치기와 합치기는 사람만 할 수 있어요.")); return; }
    const btns = el("div", "d-buttons");
    if (e.proposed) btns.append(button("맞아요", "primary small", null, () => patch(item.id, { confirm: true }, "이름을 확인했어요.")));
    btns.append(button("이름 바꾸기", "small", null, async () => {
      const name = (await window.askText("새 이름\n지금 이름은 다른 이름으로 남아요.", e.name, { okLabel: "바꾸기", primary: true }))?.trim();
      if (name && name !== e.name) void patch(item.id, { name }, "이름을 바꿨어요.");
    }), button("다른 이름 추가", "small", null, async () => {
      const alias = (await window.askText("추가할 다른 이름", "", { okLabel: "추가", primary: true }))?.trim();
      if (alias) void patch(item.id, { add_aliases: [alias] }, "다른 이름을 추가했어요.");
    }));
    body.append(btns);
    const into = el("select");
    for (const r of rows().filter((x) => x.id !== item.id)) { const o = el("option", "", r.name); o.value = r.id; into.append(o); }
    const merge = button("합치기", "small", null, async () => {
      if (!into.value || !await window.ask("두 이름을 하나로 합칠까요?\n이 이름이 나온 기억은 모두 고른 이름으로 옮겨지고, 이 이름은 다른 이름으로 남아요.", "합치기")) return;
      void editEntity(item.id, () => request(`/v1/entities/${encodeURIComponent(item.id)}/merge`, { method: "POST", body: JSON.stringify({ scope: state.scope, into: into.value }) }), "이름을 합쳤어요.", into.value);
    });
    merge.disabled = !into.options.length;
    const drop = button("이름 지우기", "ghost small", null, async () => {
      if (!await window.ask("이 이름을 지울까요?\n기억은 그대로 두고 이름 연결만 지워요. 다시 자동으로 만들어지지 않아요.", "지우기")) return;
      void editEntity(item.id, () => request(`/v1/entities/${encodeURIComponent(item.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope }) }), "이름을 지웠어요.", null);
    });
    const row = el("div", "d-buttons"); row.append(field("합칠 이름", into), merge);
    body.append(more(row, drop));
  }

  // ------------------------------------------------------------ panel
  function draw() {
    if (key !== identity()) { reset(); key = identity(); }
    $("#stats").replaceChildren();
    if (tab === "ask") drawAsk(); else drawList();
  }
  function detail() {
    const body = detailShell(TITLE[tab]);
    if (tab === "ask") { body.append(el("h2", "", "물어보기"), el("p", "lead", "승인된 기억만 보고 답해요. 근거 기억을 누르면 그 기억을 열어요. 답은 저장하지 않아요.")); return; }
    if (tab === "rules") { if (editing) ruleForm(body); else ruleDetail(body); return; }
    entityDetail(body);
  }
  function close() { selected = null; editing = null; if (tab !== "ask") paintList(); detail(); }
  return { draw, reset, detail, close };
};
