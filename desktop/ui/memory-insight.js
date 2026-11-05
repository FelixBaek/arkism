// @ts-check
// MEMORY › 기억 › 모아보기: 물어보기 (POST /v1/reflect), 규칙 (/v1/directives), 자주 나온 이름 (/v1/entities),
// 검색 살펴보기 (POST /v1/search with explain: which lane found each memory and how it scored).
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
  const TITLE = { ask: "물어보기", rules: "규칙", entities: "자주 나온 이름", search: "검색 살펴보기" };
  const loads = globalThis.ArkismKeep.latest();
  // answer: the last reflect or search reply; sent: the search body it answered; mission: the space's reflect mission (undefined until read).
  let key = "", selected = null, editing = null, busy = false, answer = null, sent = null, failure = null, question = "", q = "", mission;
  let memo = new Map(), views = new Map();
  // form options under 자세히, kept per tab across redraws; shape: the 답 모양 fields [{ name, type, required }]
  const opts = { match: "any_strict" }, shape = [];
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
  function reset() { loads.cancel(); pager.reset(); key = ""; selected = null; editing = null; busy = false; answer = null; sent = null; failure = null; mission = undefined; memo = new Map(); views = new Map(); }
  const live = (ticket, owner) => () => ticket.live() && active() && identity() === owner;
  const formHere = () => $("#chips .insight-form")?.dataset.tab === tab;

  // A cited or mentioning memory: its text (or the server's snippet), one tap opens it in the memory screen.
  function memoryLink(id, snippet) {
    const D = window.ArkismDigests, item = D.memoryItem(ctx, snippet || D.memoryText(undefined)), text = item.querySelector("p");
    const read = () => {
      if (!memo.has(id)) memo.set(id, request(`/v1/memories/${encodeURIComponent(id)}?${query()}`).then((r) => (r?.id === id ? r : null), () => null));
      return memo.get(id);
    };
    const show = (r) => { if (!snippet) text.textContent = D.memoryText(r); item.disabled = !(r?.status === "active"); return r; };
    if (!snippet) void read().then(show);
    item.addEventListener("click", async () => {
      const r = show(await read());
      if (r?.status === "active") ctx.openRecord(r); else notice("이 기억은 지금 열 수 없어요. 지워졌거나 아직 승인되지 않았어요.");
    });
    return item;
  }
  function more(...nodes) { const d = el("details", "insight-more"); d.append(el("summary", "", "자세히"), ...nodes); return d; }
  function field(label, input) { const l = el("label", "insight-field"); l.append(el("span", "", label), input); return l; }
  // An option input under 자세히: starts from opts, read back on submit (readOpts).
  function opt(node, name) { node.dataset.opt = name; if (node.type === "checkbox") node.checked = !!opts[name]; else node.value = opts[name] ?? ""; return node; }
  const readOpts = (form) => form.querySelectorAll("[data-opt]").forEach((n) => { opts[n.dataset.opt] = n.type === "checkbox" ? n.checked : n.value; });
  function num(name, min, max, step = "1") { const n = el("input"); n.type = "number"; n.min = min; n.max = max; n.step = step; n.placeholder = "알아서"; return opt(n, name); }
  function check(name, label) { const l = el("label", "insight-check"), c = el("input"); c.type = "checkbox"; l.append(opt(c, name), el("span", "", label)); return l; }
  const int = (v, min, max) => { const n = v === "" || v == null ? NaN : Number(v); return Number.isInteger(n) && n >= min && n <= max ? n : undefined; };
  // The whole exchange as JSON, for a bug report or a second look; server text stays data.
  function jsonView(obj) {
    const text = JSON.stringify(obj, null, 2), pre = raw(el("pre", "insight-json", text));
    // its own window: a popout has the focus; a missing API rejects too
    const copy = button("JSON 복사", "small", null, () => Promise.resolve().then(() => copy.ownerDocument.defaultView.navigator.clipboard.writeText(text))
      .then(() => notice("복사했어요."), () => notice("복사하지 못했어요. 글자를 직접 골라 복사해 주세요.")));
    return [copy, pre];
  }
  // name → value rows of server data (a structured answer): names and text never translated, only 예/아니오.
  // A group nests its own rows; a list of groups is a numbered list (the server checked the shape, at most 8 levels).
  function dataFacts(entries) {
    const dl = el("dl", "facts");
    for (const [k, v] of entries) dl.append(raw(el("dt", "", k)), dataValue(v, "dd"));
    return dl;
  }
  const isGroup = (v) => !!v && typeof v === "object" && !Array.isArray(v);
  function dataValue(v, tag) {
    if (typeof v === "boolean") return el(tag, "", v ? "예" : "아니오");
    const node = el(tag);
    if (isGroup(v)) node.append(dataFacts(Object.entries(v)));
    else if (Array.isArray(v) && v.some((x) => x && typeof x === "object")) { const ol = el("ol", "insight-data-list"); for (const x of v.slice(0, 50)) ol.append(dataValue(x, "li")); node.append(ol); }
    else raw(node).textContent = Array.isArray(v) ? v.map((x) => String(x ?? "—")).join(", ") : String(v ?? "—");
    return node;
  }

  // ------------------------------------------------------------ 물어보기
  // 답 모양: a few named fields become the small JSON schema subset POST /v1/reflect accepts (reflect_schema.go).
  // 묶음 (object) and 묶음 목록 (array of objects) hold their own fields, one level deep; the server allows 8.
  const SHAPES = { string: "글", number: "숫자", integer: "정수", boolean: "예/아니오", list: "글 목록", object: "묶음", objects: "묶음 목록" };
  const GROUPS = new Set(["object", "objects"]);
  function fieldSchema(f) {
    if (f.type === "list") return { type: "array", items: { type: "string" } };
    if (!GROUPS.has(f.type)) return { type: SHAPES[f.type] ? f.type : "string" };
    const inner = schemaOf(f.children || []) || { type: "object" };
    return f.type === "object" ? inner : { type: "array", items: inner };
  }
  function schemaOf(fields) {
    const properties = {}, required = new Set();
    for (const f of fields) {
      const name = f.name.trim().slice(0, 64);
      if (!name) continue;
      properties[name] = fieldSchema(f);
      if (f.required) required.add(name);
    }
    return Object.keys(properties).length ? { type: "object", properties, required: [...required], additionalProperties: false } : undefined;
  }
  function shapeEditor() {
    const box = el("div", "insight-shape");
    box.append(el("span", "", "답 모양 (비워 두면 글로 답해요)"), shapeFields(shape, false));
    return box;
  }
  // The rows of one field list and its add button; inner: the fields of a 묶음 (no groups inside).
  function shapeFields(fields, inner) {
    const box = el("div", inner ? "insight-shape-sub" : "insight-shape-list"), rows = el("div", "insight-shape-rows");
    const add = button(inner ? "안에 칸 추가" : "칸 추가", "small", null, () => {
      if (fields.length < 20) { fields.push({ name: "", type: "string", required: true }); fill(); rows.querySelector(":scope > .insight-shape-item:last-child input")?.focus(); }
    });
    function fill() { rows.replaceChildren(...fields.map((f, i) => shapeRow(fields, f, i, inner, fill))); add.disabled = fields.length >= 20; }
    fill();
    box.append(rows, add);
    return box;
  }
  function shapeRow(fields, f, i, inner, refill) {
    const item = el("div", "insight-shape-item"), row = el("div", "insight-shape-row");
    const name = el("input"); name.maxLength = 64; name.value = f.name; name.placeholder = "칸 이름 (예: 할 일)"; name.setAttribute("aria-label", "칸 이름");
    name.addEventListener("input", () => { f.name = name.value; });
    const type = el("select"); type.setAttribute("aria-label", "칸 종류");
    for (const [k, l] of Object.entries(SHAPES)) if (!inner || !GROUPS.has(k)) { const o = el("option", "", l); o.value = k; type.append(o); }
    type.value = f.type;
    type.addEventListener("change", () => { const was = GROUPS.has(f.type); f.type = type.value; if (GROUPS.has(f.type)) f.children ||= []; if (was !== GROUPS.has(f.type)) refill(); });
    const must = el("label", "insight-check"), c = el("input"); c.type = "checkbox"; c.checked = f.required;
    c.addEventListener("change", () => { f.required = c.checked; }); must.append(c, el("span", "", "꼭 채우기"));
    const drop = button("빼기", "ghost small", null, () => { fields.splice(i, 1); refill(); }); drop.setAttribute("aria-label", "이 칸 빼기");
    row.append(name, type, must, drop);
    item.append(row);
    if (GROUPS.has(f.type)) item.append(shapeFields(f.children, true));
    return item;
  }
  function drawAsk() {
    if (!formHere()) {
      const form = el("form", "original-search insight-form"); form.dataset.tab = tab;
      const box = el("label", "panel-search");
      const input = el("input"); input.type = "text"; input.maxLength = 4000; input.value = question;
      input.placeholder = "궁금한 것 (예: 배포 전에 늘 하는 일은?)"; input.setAttribute("aria-label", "물어볼 내용");
      box.append(icon("magnifying-glass"), input);
      const go = button("물어보기", "primary small"); go.type = "submit";
      const goal = el("textarea"); goal.maxLength = 2000; goal.rows = 2; goal.placeholder = "비워 두면 이 공간의 목표를 써요";
      form.append(box, go, more(field("참고할 기억 수 (1~20)", num("limit", "1", "20")),
        field("AI가 더 찾아볼 수 있는 횟수 (1~8, 1이면 처음 찾은 기억만 봐요)", num("iterations", "1", "8")),
        field("답 길이 한도 (64~4096 토큰)", num("tokens", "64", "4096")), field("이번 질문의 목표", opt(goal, "mission")), shapeEditor()));
      form.addEventListener("submit", (e) => {
        e.preventDefault();
        question = input.value.trim(); readOpts(form);
        if (!question) { input.setCustomValidity("물어볼 내용을 적어 주세요."); input.reportValidity(); return; }
        void ask();
      });
      input.addEventListener("input", () => input.setCustomValidity(""));
      $("#chips").replaceChildren(form);
      readMission();
    }
    paintAsk();
  }
  // The space's own reflect mission, so the screen can say the answer follows it. Unreadable means "not shown".
  function readMission() {
    if (mission !== undefined) return;
    const { workspace_id: w, project_id: p } = state.scope || {}, owner = identity();
    mission = "";
    if (!w || !p) return;
    void request(`/v1/spaces/${encodeURIComponent(w)}/${encodeURIComponent(p)}/config/effective`)
      .then((r) => (typeof r?.config?.reflect_mission === "string" ? r.config.reflect_mission.trim() : ""), () => "")
      .then((m) => { if (identity() !== owner || !m) return; mission = m; if (active()) paintAsk(); });
  }
  async function ask() {
    if (busy) return;
    const now = live(loads.begin(), identity());
    busy = true; failure = null; answer = null; paintAsk();
    try {
      const own = (opts.mission || "").trim(), schema = schemaOf(shape);
      const n = int(opts.limit, 1, 20), iter = int(opts.iterations, 1, 8), tokens = int(opts.tokens, 64, 4096);
      const out = await request("/v1/reflect", { method: "POST", body: JSON.stringify({ scope: state.scope, query: question, ...(n ? { limit: n } : {}), ...(iter ? { max_iterations: iter } : {}),
        ...(tokens ? { max_tokens: tokens } : {}), ...(own ? { mission: own } : {}), ...(schema ? { response_schema: schema } : {}) }) });
      if (!now()) return;
      if (typeof out?.answer !== "string" || !Array.isArray(out.cited_memory_ids) || out.cited_memory_ids.some((id) => typeof id !== "string" || !id)) throw new Error("invalid_response");
      answer = { ...out, mission_used: own ? "own" : mission ? "space" : "" };
    } catch (e) { if (now()) failure = e; }
    finally { if (now()) { busy = false; paintAsk(); } }
  }
  function askError(e) {
    const code = e?.message;
    if (code === "model_not_configured") return callout("모델 설정 필요", "서버에 답을 쓸 AI 모델이 아직 연결되지 않았어요. 서버에서 모델을 정하면 물어볼 수 있어요.");
    if (code === "model_budget_exceeded") return callout("오늘 쓸 AI 사용량을 다 썼어요", "내일 다시 물어보거나 질문을 짧게 줄여 주세요.");
    if (code === "access_denied") return callout("물어볼 수 없는 계정이에요", "이 계정에는 기억을 읽거나 AI에 보낼 권한이 없어요.");
    if (code === "invalid_model_output") return callout("정한 답 모양에 맞는 답을 받지 못했어요", "칸을 줄이거나 '꼭 채우기'를 풀고 다시 물어봐 주세요.");
    if (code === "invalid_request") return callout("물어볼 수 없는 설정이에요", "자세히에서 바꾼 값을 확인해 주세요. 칸 이름은 서로 달라야 해요.");
    return callout("답을 받지 못했어요", "잠시 뒤 다시 물어봐 주세요.");
  }
  // One step the model took before answering: what it looked for, and what came back.
  const TOOLS = { recall: "기억 다시 찾기", observations: "정리된 관찰 찾기", mental_models: "생각 정리 읽기", entity: "이름으로 찾기", expand: "이어진 기억 펼치기" };
  const TOOL_ERRORS = { unknown_tool: "없는 방법이라 건너뛰었어요", invalid_arguments: "요청이 맞지 않아 건너뛰었어요", denied: "권한이 없어 건너뛰었어요" };
  function toolStep(step) {
    const li = el("li", "insight-step"), args = isGroup(step.args) ? step.args : {};
    const what = String(args.query || args.name || "");
    const ids = Array.isArray(step.ids) ? step.ids.filter((id) => typeof id === "string" && id) : [];
    const line = el("p", "", TOOLS[step.tool] || String(step.tool));
    if (what) line.append(" · ", raw(el("span", "", what)));
    li.append(line, el("small", "meta", step.error ? TOOL_ERRORS[step.error] || String(step.error) : ids.length ? `${ids.length}개 찾음` : "찾은 것 없음"));
    if (ids.length) { const list = el("div", "dg-list"); for (const id of ids.slice(0, 5)) list.append(memoryLink(id)); li.append(list); }
    return li;
  }
  function missionLine(own) {
    if (own) return el("p", "meta insight-mission", "이번 질문의 목표에 맞춰 답해요.");
    if (!mission) return null;
    const p = el("p", "meta insight-mission", "이 공간의 목표에 맞춰 답해요: ");
    p.append(raw(el("span", "", mission)));
    return p;
  }
  function paintAsk() {
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const status = el("p", "meta original-notice", busy ? "기억에서 답을 찾는 중…" : !answer && !failure ? "승인된 기억만 보고 답해요. 답은 저장하지 않아요." : "");
    status.setAttribute("role", "status"); status.hidden = !status.textContent; table.append(status); // an empty line draws as a bare bordered bar
    if (failure) { table.append(askError(failure)); return; }
    if (!answer) { const m = missionLine((opts.mission || "").trim()); if (m) table.append(m); return; }
    const box = el("div", "quote dg-quote insight-answer"), shaped = answer.structured_answer;
    if (shaped && typeof shaped === "object" && !Array.isArray(shaped)) box.append(dataFacts(Object.entries(shaped)));
    else box.append(raw(el("p", "", answer.answer || "답할 만한 기억을 찾지 못했어요.")));
    const ids = answer.cited_memory_ids.slice(0, 20), list = el("div", "dg-list");
    for (const id of ids) list.append(memoryLink(id));
    const card = el("div", "insight-ask");
    const m = missionLine(answer.mission_used === "own");
    card.append(box, ...(m ? [m] : []), el("h3", "insight-head", ids.length ? `근거 기억 ${ids.length}개` : "근거로 든 기억이 없어요"), list);
    const steps = Array.isArray(answer.tool_trace) ? answer.tool_trace.filter(isGroup) : [];
    if (steps.length) { const ol = el("ol", "insight-steps"); for (const s of steps.slice(0, 32)) ol.append(toolStep(s)); card.append(el("h3", "insight-head", `AI가 더 찾아본 과정 ${steps.length}번`), ol); }
    const { mission_used: _, ...reply } = answer;
    card.append(more(facts([["찾아본 기억", `${answer.retrieved?.length || 0}개`], ["지킨 규칙", `${answer.directives_applied?.length || 0}개`],
      ["AI 호출", answer.iterations ? `${answer.iterations}번` : ""], ["쓴 토큰", answer.usage?.total_tokens ? `${answer.usage.total_tokens}` : ""]]), ...jsonView(reply)));
    table.append(card);
  }

  // ------------------------------------------------------------ 검색 살펴보기
  // The same POST /v1/search agents use, with explain: lanes, each hit's rank per lane, fused and rerank scores.
  const MATCH = { any_strict: "하나라도 있는 기억", all_strict: "모두 있는 기억", any: "하나라도 있거나 태그가 없는 기억", all: "모두 있거나 태그가 없는 기억", exact: "태그가 똑같은 기억" };
  // 태그 조건, one line per condition that must hold: "a+b | -c" = (a and b) or not c.
  function tagGroups(text) {
    return String(text || "").split("\n").map((line) => line.split("|").map((t) => t.trim()).filter(Boolean).map((t) => {
      const not = t.startsWith("-"), tags = [...new Set(t.replace(/^-\s*/, "").split("+").map((x) => x.trim()).filter(Boolean))];
      return { tags, ...(tags.length > 1 ? { match: "all_strict" } : {}), ...(not ? { not: true } : {}) };
    }).filter((g) => g.tags.length)).filter((g) => g.length);
  }
  function searchBody() {
    const tags = [...new Set(String(opts.tags || "").split(",").map((t) => t.trim()).filter(Boolean))], groups = tagGroups(opts.groups);
    const at = opts.at ? new Date(opts.at) : null, min = opts.min === "" || opts.min == null ? NaN : Number(opts.min);
    const include = [opts.sources && "sources", opts.entities && "entities"].filter(Boolean);
    const limit = int(opts.limit, 1, 50), depth = int(opts.depth, 1, 3);
    return { scope: state.scope, query: question, explain: true, ...(limit ? { limit } : {}), ...(depth ? { graph_depth: depth } : {}),
      ...(at && Number.isFinite(at.getTime()) ? { query_time: at.toISOString() } : {}), ...(min > 0 && min <= 1 ? { min_score: min } : {}),
      ...(tags.length || groups.length ? { filters: { ...(tags.length ? { tags, tags_match: MATCH[opts.match] ? opts.match : "any_strict" } : {}), ...(groups.length ? { tag_groups: groups } : {}) } } : {}),
      ...(include.length ? { include } : {}), ...(opts.observations ? { prefer_observations: true } : {}), ...(opts.causal ? { expand_causal: true } : {}), ...(opts.norerank ? { rerank: false } : {}) };
  }
  function drawSearch() {
    if (!formHere()) {
      const form = el("form", "original-search insight-form"); form.dataset.tab = tab;
      const box = el("label", "panel-search");
      const input = el("input"); input.type = "search"; input.maxLength = 1000; input.value = question;
      input.placeholder = "검색어 (예: 지난주 배포 문제)"; input.setAttribute("aria-label", "검색어");
      box.append(icon("magnifying-glass"), input);
      const go = button("찾아보기", "primary small"); go.type = "submit";
      const at = el("input"); at.type = "datetime-local";
      const match = el("select");
      for (const [k, l] of Object.entries(MATCH)) { const o = el("option", "", l); o.value = k; match.append(o); }
      const groups = el("textarea"); groups.rows = 2; groups.maxLength = 2000; groups.placeholder = "예: 배포+운영 | -연습";
      form.append(box, go, more(field("찾을 기억 수 (1~50)", num("limit", "1", "50")), field("이 시각 기준으로 찾기 (‘어제’ 같은 말도 이때로 읽어요)", opt(at, "at")),
        field("이어서 따라갈 단계 (1~3)", num("depth", "1", "3")), field("이 점수보다 낮으면 빼기 (0~1)", num("min", "0", "1", "0.01")),
        field("태그 (쉼표로 나눠요)", opt(el("input"), "tags")), field("태그 맞추는 방법", opt(match, "match")),
        field("태그 조건 (한 줄에 하나, 모두 맞아야 해요. + 는 모두, | 는 하나라도, - 는 빼기)", opt(groups, "groups")),
        check("sources", "원문 조각 붙이기"), check("entities", "나온 이름 붙이기"), check("observations", "정리된 관찰이 있으면 그 원래 기억은 빼기"),
        check("causal", "원인과 결과로 이어진 기억도 찾기"), check("norerank", "AI로 순서 다시 매기지 않기")));
      form.addEventListener("submit", (e) => {
        e.preventDefault();
        question = input.value.trim(); readOpts(form);
        if (!question) { input.setCustomValidity("검색어를 적어 주세요."); input.reportValidity(); return; }
        void search();
      });
      input.addEventListener("input", () => input.setCustomValidity(""));
      $("#chips").replaceChildren(form);
    }
    paintSearch();
  }
  async function search() {
    if (busy) return;
    const now = live(loads.begin(), identity()), body = searchBody();
    busy = true; failure = null; answer = null; sent = body; selected = null; paintSearch();
    try {
      const out = await request("/v1/search", { method: "POST", body: JSON.stringify(body) });
      if (!now()) return;
      if (!Array.isArray(out?.items)) throw new Error("invalid_response");
      answer = out;
    } catch (e) { if (now()) failure = e; }
    finally { if (now()) { busy = false; paintSearch(); detail(); } }
  }
  // Lane names joined by "," or next to a number have no pattern to match whole, so each is translated here.
  const tr = (s) => globalThis.ArkismI18n?.translate(s) ?? s;
  const laneName = (l) => tr(l === "rerank" ? "순서 다시 매김" : window.ArkismMemoryTime.FOUND[l] || l);
  const lanesText = (lanes) => (lanes || []).map(laneName).join(", ");
  // lane_ms: whole milliseconds per lane (and rerank), slowest first
  const laneTimes = (ms) => Object.entries(isGroup(ms) ? ms : {}).filter(([, v]) => Number.isFinite(v)).sort((a, b) => b[1] - a[1])
    .map(([l, v]) => `${laneName(l)} ${v}ms`).join(" · ");
  const score = (v) => (typeof v === "number" ? v.toFixed(v < 0.1 ? 4 : 2) : "");
  function hitMeta(h) {
    const ranks = Object.entries(h.explain?.lane_ranks || {}).map(([l, r]) => `${laneName(l)} ${tr(`${r}위`)}`);
    return [ranks.join(" · ") || lanesText(h.lanes), h.explain?.graph_hops ? `${h.explain.graph_hops}단계 이어짐` : ""].filter(Boolean).join(" · ");
  }
  function searchSummary() {
    const x = answer.explain || {}, w = x.time_window;
    const range = w ? `‘${w.phrase}’ → ${day(w.from)} ~ ${day(new Date(Date.parse(w.to) - 1).toISOString())}` : "";
    return facts([["쓴 방법", lanesText(x.lanes) || "—"], ["걸린 시간", laneTimes(x.lane_ms)], ["때를 읽은 말", range], ["순서 다시 매김", x.reranked ? "AI로 다시 매겼어요" : "하지 않았어요"],
      ["점수가 낮아 뺀 기억", x.min_score_dropped ? `${x.min_score_dropped}개` : ""], ["못 쓴 방법", (answer.degraded || []).join(", ")]]);
  }
  function searchError(e) {
    const code = e?.message;
    if (code === "access_denied") return callout("찾아볼 수 없는 계정이에요", "이 계정에는 기억을 찾을 권한이 없어요.");
    if (code === "invalid_request") return callout("찾을 수 없는 설정이에요", "자세히에서 바꾼 값(태그, 기준 시각, 태그 조건)을 확인해 주세요.");
    return callout("찾지 못했어요", "잠시 뒤 다시 찾아봐 주세요.");
  }
  function paintSearch() {
    if (!active()) return;
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const items = answer?.items || [];
    const status = el("p", "meta original-notice", busy ? "찾는 중…" : failure ? "" : !answer ? "에이전트가 기억을 찾을 때와 똑같이 찾고, 어떤 방법으로 몇 위에 찾았는지 보여 줘요." : !items.length ? "맞는 기억이 없어요." : "");
    status.setAttribute("role", "status"); status.hidden = !status.textContent; table.append(status);
    if (failure) { table.append(searchError(failure)); return; }
    if (!answer) return;
    const card = el("div", "insight-ask");
    card.append(searchSummary());
    table.append(card);
    items.forEach((h, i) => table.append(listRow(h, h.text || h.summary || h.id, hitMeta(h), score(h.explain?.rerank_score ?? h.score) || `${i + 1}위`)));
    const box = el("div", "insight-ask");
    box.append(more(...jsonView({ request: sent, response: answer })));
    table.append(box);
  }
  function searchDetail(body) {
    const h = selected;
    if (!h) { body.append(el("h2", "", "찾은 기억을 골라 확인하세요"), el("p", "lead", "검색어를 넣으면 에이전트가 받는 결과와 같은 순서로 보여 줘요. 기억을 고르면 어떤 방법으로 찾았고 점수가 몇인지 볼 수 있어요.")); return; }
    const rank = (answer?.items || []).indexOf(h) + 1, x = h.explain || {};
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", lanesText(h.lanes) || "—"), el("h2", "", rank ? `${rank}위로 찾은 기억` : "찾은 기억"));
    body.append(head);
    if (h.collection === "memories") { const open = section("기억"), list = el("div", "dg-list"); list.append(memoryLink(h.id, h.text || h.summary)); open.append(list); body.append(open); }
    else body.append(raw(el("p", "original-text", h.text || h.summary || h.id)));
    const how = section("찾은 방법");
    how.append(facts([...Object.entries(x.lane_ranks || {}).map(([l, r]) => [window.ArkismMemoryTime.FOUND[l] || l, `${r}위`]),
      ["이어서 찾은 단계", x.graph_hops ? `${x.graph_hops}단계` : ""]]));
    const points = section("점수");
    points.append(facts([["합친 점수", score(x.fused_score)], ["AI가 다시 매긴 점수", score(x.rerank_score)], ["최종 점수", score(h.score)]]),
      el("p", "meta", "합친 점수는 방법마다 순위를 더한 값이라 작아요. 크기보다 순서를 보세요."));
    body.append(how, points);
    if (h.entities?.length) {
      const names = section("나온 이름"), chips = el("div", "insight-aliases");
      for (const n of h.entities.slice(0, 10)) { const c = el("span", "chip"); c.append(raw(el("span", "", n.name))); chips.append(c); }
      names.append(chips); body.append(names);
    }
    if (h.source_traces?.length) {
      const src = section("원문 조각");
      for (const t of h.source_traces.slice(0, 3)) if (t?.excerpt) src.append(raw(el("p", "quote", t.excerpt)));
      if (src.children.length > 1) body.append(src);
    }
    if (h.tags?.length) { const t = section("태그"), chips = el("div", "insight-aliases"); for (const g of h.tags.slice(0, 20)) chips.append(raw(el("span", "chip", g))); t.append(chips); body.append(t); }
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
    row.addEventListener("click", () => { selected = item; editing = null; paint(); openDetail(); detail(); const h = $("#detail h2"); if (h) { h.tabIndex = -1; h.focus(); } });
    return row;
  }
  function paintList() {
    if (!active()) return;
    const s = page(), table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(s.loading));
    const rules = tab === "rules";
    const empty = rules ? "아직 규칙이 없어요. 규칙을 적어 두면 AI가 답할 때 꼭 지켜요." : q ? "검색과 맞는 이름이 없어요." : "아직 모인 이름이 없어요. 승인된 기억에 사람·도구·프로젝트 이름이 나오면 여기에 모여요.";
    const error = s.error ? "불러오지 못했어요. 연결과 접근 권한을 확인하고 다시 불러와 주세요." : "";
    const status = el("p", "meta original-notice", s.loading && !s.items.length ? "불러오는 중…" : error || (s.ready && !s.items.length ? empty : ""));
    status.setAttribute("role", "status"); status.hidden = !status.textContent; table.append(status);
    for (const r of s.items) {
      table.append(rules ? listRow(r, r.title, `${APPLIES[r.applies_to] || r.applies_to} · 우선순위 ${r.priority}`, ruleState(r), !r.active)
        : listRow(r, r.name, `${TYPES[r.type] || r.type} · 기억 ${r.mentions ?? 0}개`, r.proposed ? "확인 필요" : r.summary_draft ? "요약 초안" : "확인됨", r.proposed || !!r.summary_draft));
    }
    if (error) table.append(button("다시 불러오기", "small", null, () => loadList("retry")));
    else if (s.cursor) table.append(button("더 보기", "small", null, () => loadList("load")));
  }

  // ------------------------------------------------------------ 규칙 detail and form
  const ruleFields = (r) => ({ title: r.title, rule: r.rule, priority: r.priority, applies_to: r.applies_to, active: r.active, tags: r.tags || [] });
  // An agent's rule waits as proposed (never applied) until a person saves it: 승인 saves it on, 거절 deletes it.
  const ruleState = (r) => (r.proposed ? "AI 제안" : r.active ? "켜짐" : "꺼짐");
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
  prev ? (prev.proposed ? (fields.active ? "규칙을 승인했어요. 지금부터 지켜요." : "규칙을 승인했어요. 꺼 둔 채로 두었어요.") : fields.active === prev.active ? "규칙을 바꿨어요." : fields.active ? "규칙을 켰어요. 지금부터 지켜요." : "규칙을 껐어요.") : "규칙을 만들었어요. 지금부터 지켜요.");
  function ruleDetail(body) {
    const r = selected;
    if (!r) { body.append(el("h2", "", "규칙을 고르세요"), el("p", "lead", "AI가 답하거나 에이전트에게 기억을 줄 때 꼭 지키는 문장이에요. 사람이 적은 규칙만 쓰여요.")); return; }
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", `규칙 · ${ruleState(r)}`), raw(el("h2", "", r.title)));
    if (r.proposed) head.append(el("p", "dg-warn", "에이전트가 제안한 규칙이에요. 사람이 승인하기 전에는 AI가 지키지 않아요."));
    const text = section("내용"); text.append(raw(el("p", "original-text", r.rule)));
    const use = section("언제 쓰나요");
    use.append(facts([["쓰는 때", APPLIES[r.applies_to] || r.applies_to], ["우선순위", `${r.priority} · 클수록 먼저 지켜요`], ["적은 사람", r.created_by], ["고친 날", day(r.updated_at)]]));
    body.append(head, text, use);
    if (!human()) { body.append(el("p", "meta", r.proposed ? "승인과 거절은 사람만 할 수 있어요." : "규칙은 사람만 만들고 바꿀 수 있어요.")); return; }
    const btns = el("div", "d-buttons");
    const lock = () => btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    btns.append(button(r.proposed ? "승인" : r.active ? "끄기" : "켜기", "primary small", null, () => { lock(); void saveRule({ ...ruleFields(r), active: r.proposed || !r.active }, r); }),
      button("고치기", "small", null, () => { editing = r; detail(); }),
      button(r.proposed ? "거절" : "지우기", "ghost small", null, async () => {
        if (!await window.ask(r.proposed ? "이 제안을 거절할까요?\n제안한 규칙을 지워요." : "이 규칙을 지울까요?\n지우면 AI가 더 이상 지키지 않아요.", r.proposed ? "거절" : "지우기")) return;
        lock();
        void writeRule(() => request(`/v1/directives/${encodeURIComponent(r.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: r.revision }) }), r.proposed ? "제안을 거절했어요." : "규칙을 지웠어요.", false);
      }));
    body.append(btns);
  }
  function ruleForm(body) {
    const r = editing.id ? editing : null;
    body.append(el("h2", "", r ? "규칙 고치기" : "새 규칙"), el("p", "lead", r?.proposed ? "에이전트가 제안한 규칙이에요. 저장하면 승인되고 바로 쓰여요." : "AI가 꼭 지킬 문장을 적어 주세요. 저장하면 바로 쓰여요."));
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
        active: r ? r.active || !!r.proposed : true, tags: r?.tags || [] }, r);
    });
    body.append(form);
    title.focus();
  }

  // ------------------------------------------------------------ 자주 나온 이름 detail
  function entityError(e) {
    return { revision_conflict: "그사이 다른 곳에서 바뀌었어요. 새로 불러왔어요.", access_denied: "이 계정은 이름을 고칠 수 없어요.", not_found: "이미 없어진 이름이에요." }[e?.message] || `처리하지 못했어요 (${e?.message})`;
  }
  // codes: plain sentences for this write's own error codes, ahead of entityError.
  async function editEntity(id, work, done, next, codes = {}) {
    try { const out = await work(); notice(done); views.clear(); await loadList(); selected = rows().find((r) => r.id === (next === undefined ? out?.id || id : next)) || null; }
    catch (e) { notice(codes[e?.message] || entityError(e)); views.delete(id); }
    detail();
  }
  const patch = (id, change, done, codes) => editEntity(id, () => request(`/v1/entities/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ scope: state.scope, ...change }) }), done, undefined, codes);
  // 요약: the server model drafts a cited summary (POST …/summary); it waits on the name until a person approves or rejects it
  // (PATCH summary_review). Approving checks every cited memory still stands; a changed one answers 409.
  function entitySummary(id, v) {
    const box = section("요약");
    if (!v) { box.append(el("p", "meta", v === null ? "불러오는 중…" : "불러오지 못했어요. 다시 골라 주세요.")); return box; }
    const d = v.summary_draft, can = human();
    if (v.summary) box.append(raw(el("p", "original-text", v.summary.text)), el("p", "meta", `${day(v.summary.at)} 승인 · 근거 기억 ${(v.summary.citations || []).length}개`));
    else if (!d) box.append(el("p", "meta", "아직 요약이 없어요."));
    if (d) {
      const card = el("div", "d-action dg-cand");
      card.append(el("p", "", "AI가 쓴 요약 초안이에요. 승인하기 전에는 쓰이지 않아요."),
        d.withheld ? el("p", "dg-warn", "근거 기억이 지워져서 내용을 가렸어요. 버리고 새 초안을 만들어 주세요.") : raw(el("p", "original-text", d.text)),
        el("p", "meta", `근거 기억 ${(d.citations || []).length}개`));
      if (can) {
        const btns = el("div", "d-buttons"), lock = () => btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
        const ok = button("승인", "primary small", null, () => { lock(); void patch(id, { summary_review: "approve" }, "요약을 승인했어요.",
          { revision_conflict: "근거 기억이 그사이 바뀌어서 이 초안은 승인할 수 없어요. 버리고 새 초안을 만들어 주세요." }); });
        ok.disabled = !!d.withheld;
        btns.append(ok, button("버리기", "ghost small", null, () => { lock(); void patch(id, { summary_review: "reject" }, "요약 초안을 버렸어요."); }));
        card.append(btns);
      } else card.append(el("p", "meta", "승인과 버리기는 사람만 할 수 있어요."));
      box.append(card);
    } else if (can) {
      const gen = button(v.summary ? "새 요약 초안 만들기" : "AI에게 요약 초안 쓰게 하기", "small", null, () => { gen.disabled = true;
        void editEntity(id, () => request(`/v1/entities/${encodeURIComponent(id)}/summary`, { method: "POST", body: JSON.stringify({ scope: state.scope }) }), "요약 초안을 만들었어요. 읽어 보고 승인해 주세요.", undefined,
          { revision_conflict: "확인을 기다리는 초안이 이미 있어요.", model_not_configured: "서버에 요약을 쓸 모델이 없어요. 서버 설정에서 모델을 연결해 주세요.", model_budget_exceeded: "오늘 쓸 수 있는 모델 호출을 다 썼어요. 내일 다시 해 주세요." }); });
      box.append(gen);
    }
    return box;
  }
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
    body.append(head, entitySummary(item.id, v));
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
  const paint = () => (tab === "search" ? paintSearch() : paintList());
  function draw() {
    if (key !== identity()) { reset(); key = identity(); }
    $("#stats").replaceChildren();
    if (tab === "ask") drawAsk(); else if (tab === "search") drawSearch(); else drawList();
  }
  function detail() {
    const body = detailShell(TITLE[tab]);
    if (tab === "ask") { body.append(el("h2", "", "근거 기억을 눌러 확인하세요"), el("p", "lead", "승인된 기억만 보고 답해요. 근거 기억을 누르면 그 기억을 열어요. 답은 저장하지 않아요.")); return; }
    if (tab === "rules") { if (editing) ruleForm(body); else ruleDetail(body); return; }
    if (tab === "search") { searchDetail(body); return; }
    entityDetail(body);
  }
  function close() { selected = null; editing = null; if (tab !== "ask") paint(); detail(); }
  return { draw, reset, detail, close };
};
