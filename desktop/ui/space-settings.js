// @ts-check
// 설정 › 기억 공간: one space's settings over GET/PATCH/PUT /v1/spaces/{workspace}/{project}/config and where each value
// comes from (GET …/config/effective: this space, the server's template or the server default). Only a person edits;
// an agent token sees what the server shows it, read-only. Model output never activates here: a template import lands
// as candidates and automatic derivation only makes proposals.
(function (root) {
  "use strict";
  // ------------------------------------------------------------ pure helpers (node tests require them)
  const MODES = [["concise", "간결하게"], ["verbose", "자세하게"], ["verbatim", "원문 그대로"], ["custom", "직접 쓰기"], ["chunks", "조각 그대로"]];
  const SOURCE = { space: "이 공간에서 정함", template: "템플릿에서 옴", server: "기본값" };
  // "template+space": every layer that adds defense rules
  const sourceLabel = (src) => String(src || "server").split("+").map((s) => SOURCE[s] || s).join(" + ");
  // The behaviour settings this screen edits and the value that hands each back to the layer below (PATCH semantics:
  // an empty string, 0, [] or {} clears; the server treats an all-empty auto_derive or reflect_disposition as cleared).
  // The switches lanes, rerank and rerank_model hand back with null (one lane: lanes.<name> null; all lanes cleared: lanes null),
  // like every other on/off setting the server stores as "unset or a value"; a whole block (audit, recall_quality, …) clears with {}.
  const EDIT = { extraction_enabled: undefined, extraction_mode: "", extraction_instructions: "", defense_rules: [], mcp_tools: [], reflect_mission: "", reflect_disposition: {}, auto_derive: {},
    lanes: null, rerank: null, rerank_model: null, recall_budget_tokens: 0, reflect_max_tokens: 0,
    extraction_mission: "", extraction_chunk_size: 0, extract_causal: null, extraction_strategies: {}, extraction_default_strategy: "", default_timezone: "", memory_language: "",
    work_state_candidates: null, recall_session_archives: null, recall_lane_weights: {}, audit: {}, entity_resolution: "", entities_allow_free_form: null, entity_free_types: null,
    entity_labels: [], reflect_default_options: {}, mental_model_defaults: {}, consolidation: {}, recall_quality: {}, capture_mode: "", capture_density: "", decision_models: {},
    recall_budget: {}, extraction_batch_size: 0, extraction_images_per_chunk: 0, store_document_text: null };
  // empty all the way down ({tools: {}} is as empty as {}); false is a value
  const unset = (v) => v === undefined || v === null || v === "" || v === 0 || (Array.isArray(v) ? !v.length : typeof v === "object" && Object.values(v).every(unset));
  // a named strategy with nothing set is still a name an item can pick
  const none = (k, v) => (k === "extraction_strategies" ? !v || !Object.keys(v).length : unset(v));
  // What a PATCH must carry to turn stored into draft: only the keys that changed, cleared ones as their empty value.
  function configPatch(stored, draft) {
    const out = {};
    for (const [k, empty] of Object.entries(EDIT)) {
      const a = stored?.[k], b = draft?.[k];
      if (JSON.stringify(none(k, a) ? null : a) === JSON.stringify(none(k, b) ? null : b)) continue;
      out[k] = none(k, b) && empty !== undefined ? empty : b;
    }
    return out;
  }
  // PUT replaces the whole config: keep the space's name, reach, schedule, notes and the extraction switch, so every
  // behaviour setting goes back to the template or server default.
  const resetBody = (stored, revision) => ({ expected_revision: revision, display_name: stored?.display_name || "", default_reach: stored?.default_reach || "",
    extraction_enabled: stored?.extraction_enabled !== false, consolidation_schedule: stored?.consolidation_schedule || "off", notes: stored?.notes || "" });

  // Defense rules are RE2 on the server and no server route tries one out, so the live test is a preview in the
  // browser's engine: a leading (?flags) group becomes JS flags, (?P<name>) becomes (?<name>), and what RE2 refuses
  // (look-around, back references, \k<name>) is refused here too, so the test never passes a pattern the server would
  // reject. The server stays the judge when saving.
  const MARK = "[redacted]";
  function compile(pattern) {
    const p = String(pattern || "");
    if (!p) return { error: "empty" };
    if (new TextEncoder().encode(p).length > 512) return { error: "too_long" };
    if (/\(\?<?[=!]|\\[1-9]|\\k</.test(p)) return { error: "unsupported" };
    const m = /^\(\?([imsU]+)\)/.exec(p);
    if (m && m[1].includes("U")) return { error: "unsupported" };
    let re;
    try { re = new RegExp((m ? p.slice(m[0].length) : p).replaceAll("(?P<", "(?<"), `g${m ? m[1] : ""}`); } catch (_) { return { error: "invalid" }; }
    // a pattern matching nothing at all, or its own output, would rewrite every text again on each pass
    const hits = (t) => { re.lastIndex = 0; return re.test(t); };
    if (hits("") || hits(MARK)) return { error: "matches_empty" };
    return { re };
  }
  // { text, blocked: the blocking rule's name or "", redacted: matches replaced, errors: [index, code] for unusable rules }
  function testRules(rules, text) {
    let out = String(text || ""), redacted = 0, blocked = "";
    const errors = [];
    (rules || []).forEach((r, i) => {
      const c = compile(r.pattern);
      if (c.error) { errors.push([i, c.error]); return; }
      if (blocked) return;
      c.re.lastIndex = 0;
      if (r.action === "block") { if (c.re.test(out)) blocked = r.name || r.pattern; return; }
      out = out.replace(c.re, () => { redacted++; return MARK; });
    });
    return { text: out, blocked, redacted, errors };
  }
  const TOOL = /^[a-z0-9_]{1,64}$/;
  // 추출 시험: the draft is tried before it is saved, so the extraction settings it changes go as that call's overrides
  // (a setting the draft hands back cannot be said as an override; the test then runs with the stored one).
  function extractOverrides(stored, draft) {
    const p = configPatch(stored, draft), out = {};
    for (const k of ["extraction_mode", "extraction_instructions", "extraction_mission", "extraction_chunk_size", "extract_causal"]) if (k in p && !unset(p[k])) out[k] = p[k];
    return Object.keys(out).length ? out : undefined;
  }
  // a label group's values as one comma list; a value kept keeps its description
  const labelValues = (text, old = []) => globalThis.ArkismKeep.listOf(text).map((value) => old.find((v) => v.value === value) || { value });
  // the consolidation rule lists are edited as one JSON object; null = not readable or a key the server does not take
  const RULE_KEYS = ["default_scopes", "strategies", "scope_limits"];
  const rulesText = (block) => { const o = Object.fromEntries(RULE_KEYS.filter((k) => block?.[k]?.length).map((k) => [k, block[k]])); return Object.keys(o).length ? JSON.stringify(o, null, 2) : ""; };
  function parseRules(text) {
    if (!String(text || "").trim()) return {};
    let o;
    try { o = JSON.parse(text); } catch (_) { return null; }
    if (!o || typeof o !== "object" || Array.isArray(o) || Object.keys(o).some((k) => !RULE_KEYS.includes(k) || !Array.isArray(o[k]))) return null;
    return o;
  }
  // 정리 전략 편집: tags typed as one comma list; list order is priority (the first strategy whose rule fits a note group takes it)
  const moveAt = (list, i, by) => { const out = [...list], j = i + by; if (j >= 0 && j < out.length) [out[i], out[j]] = [out[j], out[i]]; return out; };
  // what the server would refuse (ConsolidationStrategy.valid), said before saving; "" when the list is fine
  function strategyIssue(list) {
    const names = new Set();
    if ((list || []).length > 16) return "전략은 16개까지 둘 수 있어요.";
    for (const st of list || []) {
      if (!st.name || st.name.length > 64) return "전략마다 64자 안의 이름을 넣어 주세요.";
      if (names.has(st.name)) return "같은 이름의 전략이 둘 있어요. 이름을 바꿔 주세요.";
      names.add(st.name);
      if (!st.scopes?.length || st.scopes.length > 8) return "전략마다 태그 규칙을 1~8개 넣어 주세요.";
      if (st.scopes.some((r) => !r.tags?.length || r.tags.length > 16)) return "태그 규칙마다 태그를 1~16개 넣어 주세요.";
    }
    return "";
  }
  // a renamed strategy keeps its place
  const renameKey = (map, from, to) => Object.fromEntries(Object.entries(map || {}).map(([k, v]) => [k === from ? to : k, v]));
  const lib = { MODES, sourceLabel, configPatch, resetBody, compile, testRules, TOOL, extractOverrides, labelValues, rulesText, parseRules, renameKey, moveAt, strategyIssue };

  // ------------------------------------------------------------ the tab
  function create(ctx) {
    const { state, el, button, icon, request, notice, render } = ctx;
    const S = { key: "", loading: false, loaded: false, err: "", stored: null, revision: 0, eff: null, src: {}, draft: null, busy: false, sample: "", job: "",
      chip: {}, rules: null, trial: { text: "", strategy: "", run: "prompt", op: "retain", out: null, busy: false } };
    const human = () => state.caps?.actions?.review_decide !== false; // config writes take people only; an agent token sees no editor
    const scopeKey = () => JSON.stringify([state.server, state.scope?.workspace_id, state.scope?.project_id]);
    const path = () => {
      const { workspace_id: w, project_id: p } = state.scope || {};
      if (!w || !p) throw new Error("no_space");
      return `/v1/spaces/${encodeURIComponent(w)}/${encodeURIComponent(p)}`;
    };
    const redraw = () => { if (state.view === "settings" && state.settingsTab === "space") render(false); };
    const clone = (v) => JSON.parse(JSON.stringify(v ?? null));
    const ERR = { no_space: "작업 공간과 프로젝트가 정해지지 않았어요. 설정 › 연결에서 범위를 먼저 정해 주세요.", access_denied: "이 계정으로는 공간 설정을 볼 수 없어요.",
      unsupported: "이 서버는 아직 공간 설정을 지원하지 않아요.", not_found: "이 서버는 아직 공간 설정을 지원하지 않아요.", invalid_request: "값을 확인해 주세요. 규칙 패턴이나 숫자 범위가 맞지 않아요.",
      secret_detected: "키나 토큰처럼 보이는 글이 있어 저장하지 않았어요.", revision_conflict: "다른 곳에서 먼저 바꿨어요. 새로 불러왔어요." };
    const errText = (e) => ERR[e?.message] || (/HTTP 404|HTTP 501|not_implemented/.test(e?.message || "") ? ERR.unsupported : "처리하지 못했어요. 연결 상태를 확인하고 다시 시도해 주세요.");

    async function load() {
      if (S.loading) return;
      S.loading = true;
      const key = scopeKey();
      try {
        const base = path();
        // the stored config is read:space_configs; an agent may lack it and still read the resolved view
        const [cfg, eff] = await Promise.allSettled([request(`${base}/config`), request(`${base}/config/effective`)]);
        if (eff.status === "rejected") throw eff.reason;
        if (key !== scopeKey()) return;
        S.eff = eff.value.config || {}; S.src = eff.value.sources || {}; S.revision = eff.value.revision || 0;
        S.stored = cfg.status === "fulfilled" ? cfg.value.config || {} : null;
        if (cfg.status === "fulfilled") S.revision = cfg.value.revision || 0;
        S.draft = S.stored ? clone(S.stored) : null; S.err = ""; S.rules = null;
      } catch (e) { S.err = errText(e); } finally { S.loading = false; S.loaded = true; S.key = key; redraw(); }
    }
    const patch = () => (S.stored && S.draft ? configPatch(S.stored, S.draft) : {});
    const dirty = () => Object.keys(patch()).length > 0;
    async function write(method, body, done) {
      if (S.busy) return;
      S.busy = true; redraw();
      try { await request(`${path()}/config`, { method, body: JSON.stringify(body) }); notice(done); S.loaded = false; await load(); }
      catch (e) { notice(errText(e)); if (e?.message === "revision_conflict") { S.loaded = false; await load(); } }
      finally { S.busy = false; redraw(); }
    }
    const save = () => write("PATCH", { expected_revision: S.revision, ...patch() }, "공간 설정을 저장했어요. 다음 작업부터 써요.");
    const resetAll = async () => {
      if (!(await ask("이 공간의 기억 뽑기·막을 내용·이름 정리·찾기·답하는 방식·자동 정리·도구·활동 기록 설정을 모두 지우고 템플릿과 기본값을 따를까요?", "모두 물려받기"))) return;
      await write("PUT", resetBody(S.stored, S.revision), "이 공간 설정을 지웠어요. 이제 템플릿과 기본값을 따라요.");
    };

    // ---------------------------------------------------------- small parts
    // where a value comes from, and 기본값으로 when this space sets it (the draft decides, so an unsaved reset shows at once)
    function origin(key, onReset) {
      const own = S.draft ? !unsetKey(key) : S.src[key] === "space";
      const box = el("div", "sp-origin");
      const label = own ? SOURCE.space : S.src[key] === "space" ? "저장하면 물려받은 값을 써요" : sourceLabel(S.src[key]);
      box.append(el("small", own ? "sp-own" : "", label));
      if (own && onReset && human()) box.append(button("기본값으로", "ghost small", null, onReset));
      return box;
    }
    function unsetKey(key) {
      const [k, sub] = key.split(".");
      const v = S.draft?.[k];
      return sub ? unset(v?.[sub]) : none(k, v);
    }
    const setDraft = (k, v) => { S.draft[k] = v; };
    const field = (label, node, hint) => { const f = el("label", "field wide", label); f.append(node); if (hint) f.append(el("small", "", hint)); return f; };
    const select = (list, value, label, onChange) => {
      const s = el("select", "ag-select"); s.setAttribute("aria-label", label);
      for (const [k, name] of list) { const o = el("option", "", name); o.value = k; o.selected = k === value; s.append(o); }
      s.addEventListener("change", () => onChange(s.value));
      return s;
    };
    const area = (value, max, label, placeholder, onChange) => {
      const t = el("textarea"); t.rows = 3; t.maxLength = max; t.value = value || ""; t.placeholder = placeholder; t.setAttribute("aria-label", label);
      t.addEventListener("change", () => { onChange(t.value.trim()); redraw(); });
      return t;
    };
    // a whole number in [min, max]; empty or 0 hands the value back (onSet(0)); anything else out of range is refused
    const numberBox = (value, min, max, placeholder, label, editable, onSet, step = 1) => {
      // the settings select box look, so a number sits like a choice
      const n = el("input", "ag-select"); n.type = "number"; n.min = String(min); n.max = String(max); n.step = String(step); n.value = value ? String(value) : ""; n.placeholder = placeholder ? String(placeholder) : ""; n.setAttribute("aria-label", label);
      n.disabled = !editable;
      n.addEventListener("change", () => {
        const v = n.value.trim() === "" ? 0 : step < 1 ? Number(n.value) : Math.round(Number(n.value));
        if (v === 0 || (v >= min && v <= max)) onSet(v); else { n.value = value ? String(value) : ""; notice(`${min}에서 ${max} 사이로 넣어 주세요.`); }
      });
      return n;
    };
    const readOnly = (text) => { const o = el("output", "", text); o.dataset.noI18n = ""; return o; };
    const modeName = (m) => MODES.find(([k]) => k === m)?.[1] || "간결하게";
    const onOff = (on) => readOnly(on ? "켜짐" : "꺼짐");
    // 물려받기 first in a choice list, naming what is inherited when this space sets nothing
    const inheritList = (key, list, eff = S.eff[key]) => [["", S.src[key] !== "space" && eff ? `물려받기 · ${list.find(([k]) => k === eff)?.[1] || eff}` : "물려받기"], ...list];
    const choice = (kit, key, list, title, hint) => {
      const d = S.draft, edit = human() && d;
      const ctl = edit ? select(inheritList(key, list), d[key] || "", title, (v) => { setDraft(key, v); redraw(); }) : readOnly(list.find(([k]) => k === S.eff[key])?.[1] || S.eff[key] || "기본값");
      return [kit.line(title, hint, ctl), origin(key, () => { setDraft(key, ""); redraw(); })];
    };
    // an on/off the server keeps as "unset or a value": 기본값으로 sends null; warn says what turning it on costs
    const flag = (kit, key, title, hint, warn) => {
      const d = S.draft, edit = human() && d;
      const on = !!(d && d[key] != null ? d[key] : S.eff[key]);
      const out = [kit.line(title, hint, edit ? kit.sw(on, title, (next) => { d[key] = next; redraw(); }) : onOff(on)), origin(key, () => { d[key] = null; redraw(); })];
      if (on && warn) out.push(el("p", "pg-hint warn", warn));
      return out;
    };
    // A block the server inherits whole: changing one field starts from what the space resolves to now, so the rest stays.
    const blockOf = (k) => (S.draft?.[k] ? S.draft[k] : clone(S.eff?.[k] || {}));
    const setBlock = (k, part) => { const next = { ...blockOf(k), ...part }; S.draft[k] = unset(next) ? null : next; redraw(); };
    const textBox = (value, max, label, placeholder, editable, onChange) => {
      const t = el("input", "ag-select"); t.value = value || ""; t.maxLength = max; t.placeholder = placeholder || ""; t.setAttribute("aria-label", label); t.spellcheck = false; t.disabled = !editable;
      t.addEventListener("change", () => onChange(t.value.trim()));
      return t;
    };
    // small number boxes side by side (lane weights, tool budgets, run sizes)
    const grid = (...cells) => { const g = el("div", "sp-grid"); for (const [label, node] of cells) { const c = el("label", "", label); c.append(node); g.append(c); } return g; };
    const pre = (text) => { const p = el("p", "mono", text); p.dataset.noI18n = ""; return p; };

    // ---------------------------------------------------------- groups
    function extractGroup(kit) {
      const g = kit.group("기억 뽑기", "대화와 파일에서 기억 후보를 만드는 방식이에요. 후보는 내가 승인해야 기억이 돼요.");
      const d = S.draft, edit = human() && d;
      const on = d ? d.extraction_enabled !== false : S.eff.extraction_enabled !== false;
      g.append(kit.line("자동으로 기억 뽑기", on ? "대화와 파일에서 기억 후보를 만들어요." : "멈췄어요. 자동 수집과 서버의 기억 뽑기를 하지 않아요. 직접 저장은 그대로 돼요.",
        edit ? kit.sw(on, "자동으로 기억 뽑기", (next) => { d.extraction_enabled = next; redraw(); }) : readOnly(on ? "켜짐" : "꺼짐")));
      const inherited = S.src.extraction_mode !== "space" ? `물려받기 · ${modeName(S.eff.extraction_mode)}` : "물려받기";
      const mode = edit ? select([["", inherited], ...MODES], d.extraction_mode || "", "뽑는 방식", (v) => { setDraft("extraction_mode", v); redraw(); }) : readOnly(modeName(S.eff.extraction_mode));
      const modeRow = kit.line("뽑는 방식", "간결하게: 핵심만 · 자세하게: 맥락까지 · 원문 그대로: 말한 그대로 · 직접 쓰기: 아래 안내를 따라요 · 조각 그대로: 글을 나눈 조각마다 후보 하나 (모델 안 씀)", mode);
      g.append(modeRow, origin("extraction_mode", () => { setDraft("extraction_mode", ""); redraw(); }));
      const custom = (d?.extraction_mode || S.eff.extraction_mode) === "custom";
      const guide = edit ? area(d.extraction_instructions, 2000, "뽑을 때 따를 안내", "예: 배포 절차와 고객 요청은 꼭 남기고, 잡담은 빼 주세요.", (v) => setDraft("extraction_instructions", v))
        : readOnly(S.eff.extraction_instructions || "없음");
      g.append(field(custom ? "뽑을 때 따를 안내 (직접 쓰기에 꼭 필요해요)" : "뽑을 때 따를 안내 (선택)", guide, "서버가 기억을 뽑을 때 읽어요. 비밀값·개인정보 규칙보다 앞서지는 않아요. 2000자까지."),
        origin("extraction_instructions", () => { setDraft("extraction_instructions", ""); redraw(); }));
      if (edit && custom && !(d.extraction_instructions || "").trim()) g.append(el("p", "pg-hint warn", "직접 쓰기는 안내가 있어야 저장할 수 있어요."));
      const chunk = numberBox(d?.extraction_chunk_size, 500, 12000, S.eff.extraction_chunk_size, "한 번에 읽는 글 길이", edit, (n) => { setDraft("extraction_chunk_size", n); redraw(); });
      g.append(kit.more("자세히 · 뽑는 방식 더 정하기",
        field("뽑을 때 목표", edit ? area(d.extraction_mission, 2000, "뽑을 때 목표", "예: 이 팀이 왜 그렇게 정했는지를 남겨요.", (v) => setDraft("extraction_mission", v)) : readOnly(S.eff.extraction_mission || "없음"),
          "무엇을 기억으로 남길지 한두 문장으로 알려 줘요. 2000자까지."), origin("extraction_mission", () => { setDraft("extraction_mission", ""); redraw(); }),
        kit.line("한 번에 읽는 글 길이", "긴 글을 이 글자 수로 나눠 읽어요 (500~12000). 비우면 물려받아요.", chunk), origin("extraction_chunk_size", () => { setDraft("extraction_chunk_size", 0); redraw(); }),
        ...flag(kit, "extract_causal", "원인과 결과도 후보로", "한 일이 다른 일의 원인이면 그 연결도 후보로 만들어요."),
        ...choice(kit, "memory_language", LANGS(), "기억을 쓰는 언어", "후보를 어떤 언어로 적을지예요. ‘말한 언어 그대로’는 내가 쓴 말을 따라요."),
        ...choice(kit, "default_timezone", ZONES(), "기준 시간대", "‘어제’·‘지난주’ 같은 말을 날짜로 바꿀 때 쓰는 시간대예요."),
        ...flag(kit, "work_state_candidates", "작업 상태를 후보로", "작업 중간 저장과 인계를 ‘지금 어디까지 했는지’ 후보로 만들어요. 처음에는 꺼져 있어요."),
        ...choice(kit, "capture_mode", CAPTURE_MODES, "대화에서 무엇을 뽑을지", "에이전트가 확인한 사실·도구 결과·다른 사람의 말도 후보로 만들지예요. 후보는 내가 승인해야 기억이 돼요."),
        ...choice(kit, "capture_density", DENSITIES, "얼마나 촘촘히 뽑을지", "촘촘할수록 후보가 많아져요."),
        ...(["balanced", "dense", "conversation"].includes(S.draft?.capture_density || S.eff.capture_density) ? [el("p", "pg-hint warn", "후보가 많아지는 만큼 뽑는 모델 사용량(예산)을 더 써요.")] : []),
        ...((S.draft?.capture_density || S.eff.capture_density) === "conversation" ? [el("p", "pg-hint", "사람끼리 나눈 대화의 모든 사실을 후보로 만들고, 대화 원문도 찾아볼 수 있게 남겨요. 모델을 가장 많이 써요.")] : []),
        ...extractSizes(kit),
        ...strategies(kit)));
      if (human()) g.append(kit.more("자세히 · 뽑기 시험해 보기", ...trialBox(kit)));
      return g;
    }

    const CAPTURE_MODES = [["user", "내가 한 말만"], ["assistant_results", "에이전트가 확인한 결과까지"]];
    const DENSITIES = [["durable", "오래 쓸 것만"], ["balanced", "보통"], ["dense", "촘촘하게"], ["conversation", "사람 사이 대화 전체"]];
    // the app's own language list (look.js); the server takes any language tag
    const LANG_LIST = [["auto", "말한 언어 그대로"], ...(window.ArkismLook?.languages || [])];
    // a stored value outside the list still shows as itself
    const withValue = (list, ...vals) => [...list, ...vals.filter((v) => v && !list.some(([k]) => k === v)).map((v) => [v, v])];
    const LANGS = () => withValue(LANG_LIST, S.draft?.memory_language, S.eff.memory_language);
    const ZONES = () => withValue((typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : ["UTC"]).map((z) => [z, z]), S.draft?.default_timezone, S.eff.default_timezone);

    // 이름 붙은 뽑기 방식: an upload or a capture may pick one by name; the default one applies when nothing is picked
    const STRATEGY_NAME = /^[a-z0-9_-]{1,32}$/;
    function strategies(kit) {
      const d = S.draft, edit = human() && d;
      const map = (d ? d.extraction_strategies : S.eff.extraction_strategies) || {};
      const names = Object.keys(map);
      const out = [el("strong", "sp-sub", "이름 붙은 뽑기 방식")];
      if (!names.length) out.push(el("p", "pg-hint", "아직 없어요. 파일 종류마다 다르게 뽑고 싶을 때 만들어요."));
      const put = (next) => { d.extraction_strategies = Object.keys(next).length ? next : null; redraw(); };
      for (const name of names) {
        const st = map[name] || {};
        if (!edit) { out.push(el("div", "sp-rule sp-rule-fixed", `${name} · ${modeName(st.extraction_mode)}`)); continue; }
        const row = el("div", "sp-rule");
        const n = el("input", "mono"); n.value = name; n.maxLength = 32; n.setAttribute("aria-label", "방식 이름"); n.spellcheck = false;
        n.addEventListener("change", () => {
          const to = n.value.trim();
          if (to === name) return;
          if (!STRATEGY_NAME.test(to) || to in map) { notice("이름은 영어 소문자·숫자·-·_로, 겹치지 않게 써요."); n.value = name; return; }
          if (d.extraction_default_strategy === name) d.extraction_default_strategy = to;
          put(renameKey(map, name, to));
        });
        const set = (part) => put({ ...map, [name]: Object.fromEntries(Object.entries({ ...st, ...part }).filter(([, v]) => !unset(v) || v === false)) });
        const mode = select([["", "공간 방식 따름"], ...MODES], st.extraction_mode || "", "이 방식의 뽑는 방식", (v) => set({ extraction_mode: v }));
        const size = numberBox(st.extraction_chunk_size, 500, 12000, "", "이 방식의 글 길이", true, (v) => set({ extraction_chunk_size: v }));
        const del = el("button", "icon-btn plain"); del.type = "button"; del.setAttribute("aria-label", `${name} 지우기`); del.append(icon("x"));
        del.addEventListener("click", () => { const { [name]: _gone, ...rest } = map; if (d.extraction_default_strategy === name) d.extraction_default_strategy = ""; put(rest); });
        row.append(n, mode, size, del);
        const causal = select([["", "공간 설정 따름"], ["on", "켜기"], ["off", "끄기"]], st.extract_causal == null ? "" : st.extract_causal ? "on" : "off", "원인과 결과도 후보로",
          (v) => set({ extract_causal: v === "" ? undefined : v === "on" }));
        out.push(row, field("이 방식의 안내", area(st.extraction_instructions, 2000, "이 방식의 안내", "예: 회의록이면 결정과 담당자만 남겨요.", (v) => set({ extraction_instructions: v }))),
          field("이 방식의 목표", area(st.extraction_mission, 2000, "이 방식의 목표", "", (v) => set({ extraction_mission: v }))), kit.line("원인과 결과도 후보로", "", causal));
      }
      if (edit) {
        const add = button("방식 추가", "ghost", "plus", () => { let i = names.length + 1; while (`style-${i}` in map) i++; put({ ...map, [`style-${i}`]: {} }); });
        add.disabled = names.length >= 16;
        out.push(add);
      }
      out.push(origin("extraction_strategies", () => { d.extraction_strategies = null; redraw(); }));
      if (names.length || S.eff.extraction_default_strategy) out.push(...choice(kit, "extraction_default_strategy", names.map((k) => [k, k]), "기본으로 쓸 방식", "따로 고르지 않으면 이 방식으로 뽑아요."));
      return out;
    }

    // 뽑기 시험: POST /v1/extract/preview with the unsaved draft as overrides; 프롬프트만 calls no model.
    // 보낼 프롬프트 보기: POST /v1/prompts/preview renders the saved settings' prompt for retain, consolidation or reflect.
    function trialBox(kit) {
      const T = S.trial;
      const sample = el("textarea"); sample.rows = 4; sample.maxLength = 65536; sample.value = T.text; sample.placeholder = "예: 어제 회의에서 배포는 금요일에 하지 않기로 했어요. 민수가 체크리스트를 맡았어요.";
      sample.setAttribute("aria-label", "뽑기 시험할 글"); sample.addEventListener("input", () => { T.text = sample.value; });
      const names = Object.keys((S.draft ? S.draft.extraction_strategies : S.eff.extraction_strategies) || {});
      const strat = select([["", "기본 방식"], ...names.map((k) => [k, k])], T.strategy, "쓸 방식", (v) => { T.strategy = v; });
      const run = select([["prompt", "프롬프트만 보기 · 모델 안 씀"], ["run", "실제로 뽑아 보기 · 모델 예산 씀"]], T.run, "시험 방식", (v) => { T.run = v; redraw(); });
      const go = button("시험해 보기", "", null, () => void trial("extract"));
      go.disabled = T.busy;
      const op = select([["retain", "기억 뽑기"], ["consolidation", "관찰 정리"], ["reflect", "질문에 답하기"]], T.op, "어떤 프롬프트", (v) => { T.op = v; });
      const show = button("프롬프트 보기", "ghost", null, () => void trial("prompt"));
      show.disabled = T.busy;
      const out = [field("뽑기 시험할 글", sample, "저장하지 않은 설정으로 시험해요. 아무것도 저장하지 않아요."), kit.line("쓸 방식", "", strat), kit.line("시험 방식", "", run)];
      if (T.run === "run") out.push(el("p", "pg-hint warn", "실제로 뽑으면 서버 모델을 불러 모델 예산을 써요."));
      out.push(go, kit.line("보낼 프롬프트 보기", "저장한 설정으로 서버가 모델에 보낼 글을 보여 줘요. 모델은 부르지 않아요.", op), show);
      if (T.out) out.push(T.out);
      return out;
    }
    async function trial(kind) {
      const T = S.trial;
      if (kind === "extract" && !T.text.trim()) { notice("시험해 볼 글을 넣어 주세요."); return; }
      T.busy = true; redraw();
      const box = el("div", "sp-test-out"); box.setAttribute("role", "status");
      try {
        const scope = { workspace_id: state.scope.workspace_id, project_id: state.scope.project_id };
        if (kind === "prompt") {
          const r = await request("/v1/prompts/preview", { method: "POST", body: JSON.stringify({ scope, operation: T.op }) });
          box.append(el("strong", "", "시스템 안내"), pre(r.system || ""), el("strong", "", "보낼 글"), pre(r.user || ""));
        } else {
          const body = { scope, text: T.text, prompt_only: T.run !== "run" };
          if (T.strategy) body.strategy = T.strategy;
          const overrides = S.stored && S.draft ? extractOverrides(S.stored, S.draft) : undefined;
          if (overrides) body.overrides = overrides;
          const r = await request("/v1/extract/preview", { method: "POST", body: JSON.stringify(body) });
          paintTrial(box, r);
        }
      } catch (e) { box.append(el("small", "warn", errText(e))); }
      T.out = box; T.busy = false; redraw();
    }
    function paintTrial(box, r) {
      const p = r.prompt;
      if (p) {
        if (p.skipped_reason) box.append(el("small", "warn", `뽑지 않아요 · ${p.skipped_reason}`));
        box.append(el("small", "", [p.strategy ? `방식 ${p.strategy}` : "", p.timezone ? `시간대 ${p.timezone}` : ""].filter(Boolean).join(" · ") || "기본 방식"));
        if (p.system) box.append(el("strong", "", "시스템 안내"), pre(p.system));
        if (p.user) box.append(el("strong", "", "보낼 글"), pre(p.user));
      }
      const claims = r.claims || [];
      if (!p) box.append(el("strong", "", claims.length ? `후보 ${claims.length}개가 나와요` : "뽑힌 후보가 없어요"));
      for (const c of claims) {
        const row = el("div", "sp-claim");
        row.append(pre(c.content || ""), el("small", "", [c.kind, c.fact_type, c.occurred_from ? `${c.occurred_from.slice(0, 10)}${c.occurred_to ? ` ~ ${c.occurred_to.slice(0, 10)}` : ""}` : ""].filter(Boolean).join(" · ")));
        box.append(row);
      }
      if (r.causes?.length) box.append(el("small", "", `원인과 결과 연결 ${r.causes.length}개`));
      if (r.skipped?.length) box.append(el("small", "", `빼고 읽은 부분 ${r.skipped.length}곳 (비밀값·개인 표시 등)`));
    }

    const ACTIONS = [["redact", "가리기"], ["block", "막기"]];
    const RULE_ERR = { empty: "패턴을 넣어 주세요.", too_long: "패턴이 너무 길어요 (512바이트까지).", unsupported: "서버가 받지 않는 형식이에요 (앞뒤 살피기·되돌아 참조는 못 써요).",
      invalid: "패턴을 읽을 수 없어요. 괄호와 기호를 확인해 주세요.", matches_empty: "빈 글에도 맞는 패턴이라 모든 글을 바꿔 버려요." };
    function defenseGroup(kit) {
      const g = kit.group("막을 내용", "기억에 들어가기 전에 글을 검사해요. 가리기는 맞는 부분을 [redacted]로 바꾸고, 막기는 저장을 거절해요. 규칙은 사람에게만 보여요.");
      if (!human()) { g.append(el("p", "pg-hint", "규칙은 사람 계정에게만 보여요. 어디서 정했는지만 보여 줘요."), origin("defense_rules")); return g; }
      const all = S.eff.defense_rules || [], own = S.stored?.defense_rules || [];
      const inherited = all.slice(0, Math.max(0, all.length - own.length)); // the server lists template rules first, then this space's
      const rules = S.draft?.defense_rules || [];
      const list = el("div", "sp-rules");
      for (const r of inherited) {
        const row = el("div", "sp-rule sp-rule-fixed");
        const pat = el("code", "", r.pattern); pat.dataset.noI18n = "";
        row.append(el("span", "", r.name || "이름 없음"), pat, el("small", "", `${ACTIONS.find(([k]) => k === r.action)?.[1] || r.action} · 템플릿 규칙이라 여기서 못 바꿔요`));
        list.append(row);
      }
      rules.forEach((r, i) => {
        const row = el("div", "sp-rule");
        const name = el("input"); name.value = r.name || ""; name.maxLength = 64; name.placeholder = "규칙 이름"; name.setAttribute("aria-label", "규칙 이름");
        name.addEventListener("input", () => { r.name = name.value.trim(); });
        name.addEventListener("change", redraw);
        const pat = el("input", "mono"); pat.value = r.pattern || ""; pat.placeholder = "ACC-\\d{6}"; pat.setAttribute("aria-label", "찾을 패턴 (정규식)"); pat.spellcheck = false;
        pat.addEventListener("input", () => { r.pattern = pat.value; paintTest(); });
        pat.addEventListener("change", redraw);
        const act = select(ACTIONS, r.action || "redact", "맞으면 할 일", (v) => { r.action = v; redraw(); });
        const del = el("button", "icon-btn plain"); del.type = "button"; del.setAttribute("aria-label", "규칙 지우기"); del.append(icon("x"));
        del.addEventListener("click", () => { rules.splice(i, 1); redraw(); });
        row.append(name, pat, act, del);
        const c = compile(r.pattern);
        if (c.error && r.pattern) row.append(el("small", "warn sp-rule-err", RULE_ERR[c.error]));
        list.append(row);
      });
      if (!inherited.length && !rules.length) list.append(el("p", "pg-hint", "아직 규칙이 없어요. 비밀번호·키 같은 값은 규칙이 없어도 서버가 따로 막아요."));
      const add = button("규칙 추가", "ghost", "plus", () => { S.draft.defense_rules = [...rules, { name: "", pattern: "", action: "redact" }]; redraw(); });
      add.disabled = rules.length + inherited.length >= 32;
      g.append(list, add, origin("defense_rules", () => { S.draft.defense_rules = []; redraw(); }));
      // 시험해 보기: every rule this space would run (template first, then the draft), applied as the server applies them
      const sample = el("textarea"); sample.rows = 3; sample.value = S.sample; sample.placeholder = "예: ACC-123456 으로 연락 주세요"; sample.setAttribute("aria-label", "시험해 볼 글");
      const out = el("div", "sp-test-out"); out.setAttribute("role", "status");
      function paintTest() {
        if (!S.sample.trim()) { out.replaceChildren(el("small", "", "글을 넣으면 규칙을 거친 결과가 바로 보여요.")); return; }
        const r = testRules([...inherited, ...rules], S.sample);
        const shown = el("p", "mono", r.text); shown.dataset.noI18n = "";
        if (r.blocked) out.replaceChildren(el("strong", "warn", `막혀요 · ‘${r.blocked}’ 규칙에 걸려 저장하지 않아요.`));
        else out.replaceChildren(el("strong", "", r.redacted ? `가려서 저장해요 · ${r.redacted}곳` : "그대로 통과해요"), shown);
        if (r.errors.length) out.append(el("small", "warn", "쓸 수 없는 규칙은 빼고 시험했어요."));
      }
      sample.addEventListener("input", () => { S.sample = sample.value; paintTest(); });
      paintTest();
      g.append(field("시험해 보기 (미리 보기)", sample, "앱에서 바로 돌려 본 미리 보기라 서버 결과와 드물게 다를 수 있어요. 저장할 때 서버가 다시 검사해요."), out);
      return g;
    }

    // A list of short names as chips with an add box (agent tools, audit actions). o: { id, list, set, empty, placeholder, label, add, ok, bad }
    function chipEditor(o) {
      const chips = el("div", "sp-chips");
      const editable = human() && S.draft;
      for (const t of o.list) {
        const c = el("span", "pill on sp-chip"), name = el("span", "mono", t); name.dataset.noI18n = "";
        c.append(name);
        if (editable) { const x = el("button", "icon-btn plain"); x.type = "button"; x.setAttribute("aria-label", `${t} 빼기`); x.append(icon("x")); x.addEventListener("click", () => o.set(o.list.filter((y) => y !== t))); c.append(x); }
        chips.append(c);
      }
      if (!o.list.length) chips.append(el("small", "pg-hint", o.empty));
      if (!editable) return { chips, box: null, msg: null };
      const input = el("input", "mono"); input.placeholder = o.placeholder; input.value = S.chip[o.id] || ""; input.setAttribute("aria-label", o.label); input.spellcheck = false;
      const msg = el("small", "warn");
      const add = button(o.add, "", null, () => {
        const t = input.value.trim();
        if (!o.ok(t)) { msg.textContent = o.bad; return; }
        if (o.list.includes(t)) { msg.textContent = "이미 있어요."; return; }
        if (o.list.length >= 64) { msg.textContent = "64개까지 넣을 수 있어요."; return; }
        S.chip[o.id] = ""; o.set([...o.list, t]);
      });
      input.addEventListener("input", () => { S.chip[o.id] = input.value; });
      input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); add.click(); } });
      const box = el("div", "pg-input-act"); box.append(input, add);
      return { chips, box, msg };
    }

    function toolsGroup(kit) {
      const g = kit.group("에이전트가 쓸 도구", "이 공간에서 에이전트가 쓸 Arkism 도구(MCP)예요. 비워 두면 모두 써요.");
      const list = S.draft ? S.draft.mcp_tools || [] : S.eff.mcp_tools || [];
      const c = chipEditor({ id: "tool", list, set: (next) => { S.draft.mcp_tools = next; redraw(); }, empty: "모든 도구를 써요.", placeholder: "recall", label: "도구 이름", add: "도구 추가",
        ok: (t) => TOOL.test(t), bad: "도구 이름은 영어 소문자·숫자·밑줄로 써요. 예: recall" });
      g.append(c.chips);
      if (c.box) g.append(kit.more("자세히 · 도구 고르기", field("도구 이름", c.box, "이름 하나를 넣고 추가해요. 하나라도 넣으면 넣은 것만 써요."), c.msg));
      g.append(origin("mcp_tools", () => { S.draft.mcp_tools = []; redraw(); }));
      return g;
    }

    // 활동 기록: what changes something is always recorded; a space may stop or narrow only the optional reads and searches
    function auditGroup(kit) {
      const g = kit.group("활동 기록", "누가 언제 무엇을 했는지 남기는 기록이에요. 바꾸는 일은 언제나 남겨요.");
      const d = S.draft, edit = human() && d, a = (d ? d.audit : S.eff.audit) || {};
      const on = a.enabled !== false;
      g.append(kit.line("읽기·찾기도 기록하기", on ? "읽고 찾은 일도 남겨요." : "읽기·찾기는 남기지 않아요. 원문 읽기와 바꾸는 일은 그대로 남겨요.",
        edit ? kit.sw(on, "읽기·찾기도 기록하기", (next) => setBlock("audit", { enabled: next ? undefined : false })) : onOff(on)));
      const c = chipEditor({ id: "audit", list: a.actions || [], set: (next) => setBlock("audit", { actions: next.length ? next : undefined }), empty: "읽기·찾기를 모두 남겨요.",
        placeholder: "search:*", label: "남길 기록 이름", add: "기록 추가", ok: (t) => t.length > 0 && t.length <= 128 && !/\s/.test(t), bad: "띄어쓰기 없이 128자까지 써요. 예: search:*" });
      g.append(kit.more("자세히 · 남길 읽기만 고르기", c.chips, ...(c.box ? [field("남길 기록 이름", c.box, "넣은 것만 남겨요. 끝에 *를 붙이면 그 앞이 같은 것 모두예요."), c.msg] : [])),
        origin("audit", () => { d.audit = null; redraw(); }));
      return g;
    }

    // 이름 정리: how names in memories become one entity, and the label schema extraction may fill
    const RESOLUTION = [["exact", "똑같은 이름만 같게 봐요"], ["propose", "비슷한 이름은 합칠지 물어봐요"], ["similar", "글자만 다른 이름은 바로 붙여요"]];
    const LABEL_TYPES = [["value", "값 하나"], ["multi-values", "값 여러 개"], ["text", "글 하나"], ["multi-text", "글 여러 개"]];
    const LABEL_KEY = /^[a-z0-9][a-z0-9_-]{0,31}$/;
    function entityGroup(kit) {
      const g = kit.group("이름 정리", "기억 속 사람·도구·프로젝트 이름을 하나로 모으는 방식이에요. 합치는 일은 내가 정해요.");
      g.append(...choice(kit, "entity_resolution", RESOLUTION, "같은 이름 찾기", "비슷한 이름을 어떻게 다룰지예요."));
      g.append(kit.more("자세히 · 이름 종류와 라벨",
        ...flag(kit, "entities_allow_free_form", "라벨 밖의 이름도 받기", "라벨을 정해 두어도 자유로운 이름을 함께 뽑아요."),
        ...flag(kit, "entity_free_types", "새 이름 종류 허용", "사람·도구 같은 기본 종류 밖의 종류도 받아요."),
        ...labels(kit)));
      return g;
    }
    function labels(kit) {
      const d = S.draft, edit = human() && d;
      const list = (d ? d.entity_labels : S.eff.entity_labels) || [];
      const out = [el("strong", "sp-sub", "라벨")];
      if (!list.length) out.push(el("p", "pg-hint", "아직 없어요. 예: 우선순위(높음, 보통, 낮음)처럼 정해 두면 그 값으로만 붙여요."));
      const put = (next) => { d.entity_labels = next; redraw(); };
      list.forEach((grp, i) => {
        if (!edit) { out.push(el("div", "sp-rule sp-rule-fixed", `${grp.key} · ${LABEL_TYPES.find(([k]) => k === (grp.type || "value"))?.[1]}`)); return; }
        const set = (part) => put(list.map((x, j) => (j === i ? { ...x, ...part } : x)));
        const row = el("div", "sp-rule");
        const key = el("input", "mono"); key.value = grp.key || ""; key.maxLength = 32; key.placeholder = "priority"; key.setAttribute("aria-label", "라벨 이름"); key.spellcheck = false;
        key.addEventListener("change", () => { const k = key.value.trim(); if (!LABEL_KEY.test(k)) { notice("라벨 이름은 영어 소문자·숫자·-·_로 써요."); key.value = grp.key || ""; return; } set({ key: k }); });
        const desc = el("input"); desc.value = grp.description || ""; desc.maxLength = 200; desc.placeholder = "무엇을 나누는 라벨인지"; desc.setAttribute("aria-label", "라벨 설명");
        desc.addEventListener("change", () => set({ description: desc.value.trim() || undefined }));
        const type = select(LABEL_TYPES, grp.type || "value", "라벨 값 종류", (v) => set({ type: v === "value" ? undefined : v, values: v.endsWith("text") ? undefined : grp.values }));
        const del = el("button", "icon-btn plain"); del.type = "button"; del.setAttribute("aria-label", "라벨 지우기"); del.append(icon("x"));
        del.addEventListener("click", () => put(list.filter((_, j) => j !== i)));
        row.append(key, desc, type, del);
        const textual = String(grp.type || "").endsWith("text");
        if (!grp.key || (!textual && !grp.values?.length)) row.append(el("small", "warn sp-rule-err", !grp.key ? "라벨 이름을 넣어 주세요." : "고를 수 있는 값을 하나 이상 넣어 주세요."));
        out.push(row);
        if (!textual) {
          const vals = el("input"); vals.value = (grp.values || []).map((v) => v.value).join(", "); vals.placeholder = "높음, 보통, 낮음"; vals.setAttribute("aria-label", "고를 수 있는 값");
          vals.addEventListener("change", () => set({ values: labelValues(vals.value, grp.values) }));
          out.push(field("고를 수 있는 값 (쉼표로 나눠요)", vals));
        }
        out.push(kit.line("태그로도 붙이기", "이 라벨을 기억의 태그로도 붙여요.", kit.sw(!!grp.tag, "태그로도 붙이기", (next) => set({ tag: next || undefined }))));
      });
      if (edit) {
        const add = button("라벨 추가", "ghost", "plus", () => put([...list, { key: "", values: [] }]));
        add.disabled = list.length >= 16;
        out.push(add);
      }
      out.push(origin("entity_labels", () => { d.entity_labels = []; redraw(); }));
      return out;
    }

    // 기억 찾기: which recall lanes run when the caller names none, the re-ranking, the server-model re-ranking (budget) and the default context size
    const LANES = [["fts", "낱말로 찾기", "적힌 낱말이 맞는 기억"], ["vector", "뜻으로 찾기", "낱말이 달라도 뜻이 비슷한 기억"], ["temporal", "시간으로 찾기", "물어본 때와 가까운 기억"], ["graph", "연결로 찾기", "같은 이름으로 이어진 기억"],
      ["semantic", "가까운 뜻 잇기", "찾은 기억과 뜻이 가장 가까운 기억을 이어 붙여요. 처음에는 꺼져 있어요."]];
    // the fusion weight of each recall lane (recall_weights.go recallLaneNames)
    const WEIGHTS = [["exact", "똑같은 글"], ["keyword", "낱말"], ["vector", "뜻"], ["relation", "관계"], ["temporal", "시간"], ["graph", "연결"], ["causal", "원인"], ["semantic", "가까운 뜻"]];
    const CONFLICT = [["attach", "같이 보여 주고 ‘정정 검토 중’ 표시"], ["exclude", "결정할 때까지 빼기"]];
    function recallGroup(kit) {
      const g = kit.group("기억 찾기", "에이전트가 기억을 찾을 때 쓰는 방법이에요. 에이전트가 따로 정하지 않을 때 이대로 찾아요.");
      const d = S.draft, edit = human() && d;
      const lane = (k) => { const v = d?.lanes?.[k] ?? S.eff.lanes?.[k]; return k === "semantic" ? v === true : v !== false; };
      const rows = LANES.map(([k, title, hint]) => [kit.line(title, hint, edit ? kit.sw(lane(k), title, (next) => { d.lanes = { ...(d.lanes || {}), [k]: next }; redraw(); }) : readOnly(lane(k) ? "켜짐" : "꺼짐")),
        origin(`lanes.${k}`, () => { d.lanes = { ...d.lanes, [k]: null }; redraw(); })]).flat();
      const rr = d?.rerank ?? S.eff.rerank;
      g.append(kit.line("검색 결과 다시 고르기", "찾은 기억의 순서를 한 번 더 고쳐요. 서버에 다시 고르는 기능이 준비돼 있을 때만 쓰여요.",
        edit ? kit.sw(!!rr, "검색 결과 다시 고르기", (next) => { d.rerank = next; redraw(); }) : readOnly(rr ? "켜짐" : "꺼짐")), origin("rerank", () => { d.rerank = null; redraw(); }));
      const rm = !!(d?.rerank_model ?? S.eff.rerank_model);
      g.append(kit.line("서버 모델로 다시 고르기", "서버 모델이 찾은 기억 위쪽 20개를 읽고 순서를 고쳐요. 켜면 찾을 때마다 모델 사용량(예산)을 써요.",
        edit ? kit.sw(rm, "서버 모델로 다시 고르기", (next) => { d.rerank_model = next; redraw(); }) : readOnly(rm ? "켜짐" : "꺼짐")), origin("rerank_model", () => { d.rerank_model = null; redraw(); }));
      if (rm) g.append(el("p", "pg-hint warn", "켜져 있어요. 찾을 때마다 서버 모델을 불러 모델 예산을 써요."));
      const budget = numberBox(d?.recall_budget_tokens, 1, 64000, S.eff.recall_budget_tokens, "에이전트에 줄 기억 양", edit, (n) => { setDraft("recall_budget_tokens", n); redraw(); });
      const w = (d ? d.recall_lane_weights : S.eff.recall_lane_weights) || {};
      const weights = grid(...WEIGHTS.map(([k, name]) => [name, numberBox(w[k], 0.25, 8, 1, `${name} 무게`, edit, (n) => {
        const next = { ...w, [k]: n || undefined }; d.recall_lane_weights = unset(next) ? null : next; redraw(); }, 0.25)]));
      g.append(kit.more("자세히 · 찾는 방법과 양", ...rows, kit.line("에이전트에 줄 기억 양", "한 번에 넘기는 기억의 토큰 수예요 (1~64000). 비우면 물려받아요.", budget),
        origin("recall_budget_tokens", () => { setDraft("recall_budget_tokens", 0); redraw(); }),
        ...flag(kit, "recall_session_archives", "세션 원문에서도 찾기", "저장된 대화 원문도 찾는 길에 넣어요. 처음에는 꺼져 있어요."),
        field("찾는 방법별 무게", weights, "1이 보통이에요. 크게 하면 그 방법으로 찾은 기억이 앞에 와요 (0.25~8)."),
        origin("recall_lane_weights", () => { d.recall_lane_weights = null; redraw(); })));
      // recall_quality is one block the server inherits whole
      const q = (d?.recall_quality ? d.recall_quality : S.eff.recall_quality) || {};
      const qsw = (k, title, hint, on) => kit.line(title, hint, edit ? kit.sw(on, title, (next) => setBlock("recall_quality", { [k]: next })) : onOff(on));
      g.append(kit.more("자세히 · 찾은 결과 거르기",
        qsw("relevance_gate", "관련 없는 결과 빼기", "낱말만 우연히 맞고 뜻이 먼 기억을 빼요. 처음에는 켜져 있어요.", q.relevance_gate !== false),
        kit.line("정정을 기다리는 기억", "고쳐 달라는 후보가 있는 기억을 어떻게 보여 줄지예요.",
          edit ? select(CONFLICT, q.conflict_pending || "attach", "정정을 기다리는 기억", (v) => setBlock("recall_quality", { conflict_pending: v === "attach" ? undefined : v })) : readOnly(CONFLICT.find(([k]) => k === (q.conflict_pending || "attach"))[1])),
        qsw("provisional", "승인 전 후보도 보여 주기", "아직 승인하지 않은 후보를 ‘승인 전’ 표시와 함께 몇 개 보여 줘요. 확정된 기억처럼 쓰이지 않아요. 처음에는 켜져 있어요.", q.provisional !== false),
        qsw("auto_close_on_correction", "정정되면 옛 사실 끝내기 제안", "정정을 승인하면 옛 기억에 끝난 날짜를 붙이자고 제안해요. 내가 승인해야 바뀌어요.", !!q.auto_close_on_correction),
        ...recallDefaults(kit, q, edit),
        origin("recall_quality", () => { d.recall_quality = null; redraw(); })));
      return g;
    }

    // recall_quality's recall defaults: what a search takes for what it leaves unset (provisional_vectors spends embedding calls)
    function recallDefaults(kit, q, edit) {
      const qsw = (k, title, hint, on) => kit.line(title, hint, edit ? kit.sw(on, title, (next) => setBlock("recall_quality", { [k]: next || undefined })) : onOff(on));
      const ro = q.rerank_options || {};
      const rsw = (k, title, hint) => kit.line(title, hint, edit ? kit.sw(!!ro[k], title, (next) => { const o = { ...ro, [k]: next || undefined }; setBlock("recall_quality", { rerank_options: unset(o) ? undefined : o }); }) : onOff(!!ro[k]));
      const out = [qsw("provisional_vectors", "후보도 뜻으로 찾기", "새 후보를 임베딩해 뜻으로 찾는 길에도 걸리게 해요. 처음에는 꺼져 있어요.", !!q.provisional_vectors)];
      if (q.provisional_vectors) out.push(el("p", "pg-hint warn", "켜져 있어요. 후보가 들어올 때마다 임베딩을 만들어 모델 예산을 써요."));
      out.push(rsw("prune", "다시 고를 때 관련 없는 것 빼기", "서버 모델로 다시 고를 때 질문과 먼 기억을 빼요."),
        rsw("dedup", "다시 고를 때 겹치는 것 합치기", "서버 모델로 다시 고를 때 같은 내용을 하나만 남겨요."),
        ...decisionModel(kit, edit, "recall", "찾은 결과를 판정 모델로 거르기", "위 두 스위치를 켜 두면 작은 판정 모델이 관련 없거나 겹치는 기억을 먼저 빼요. 서버 모델로 다시 고르지 않아도 돼요."),
        qsw("prefer_observations", "관찰을 먼저 보여 주기", "정리된 관찰이 있으면 그 바탕 기억보다 앞에 둬요.", !!q.prefer_observations),
        qsw("include_chunks", "원문 조각도 같이 주기", "찾은 기억이 나온 문서의 원문 조각을 함께 넘겨요.", !!q.include_chunks),
        kit.line("원문 조각 양", "원문 조각에 쓸 토큰 수예요 (1~16384). 비우면 기본값을 써요.",
          numberBox(q.chunks_max_tokens, 1, 16384, "", "원문 조각 양", edit, (n) => setBlock("recall_quality", { chunks_max_tokens: n || undefined }))));
      return out;
    }

    // file extraction sizes and whether a document's text is kept; 0 or 기본값으로 hands each back
    function extractSizes(kit) {
      const d = S.draft, edit = human() && d;
      const box = (k, max, title) => numberBox(d?.[k], 1, max, S.eff[k], title, edit, (n) => { setDraft(k, n); redraw(); });
      return [
        kit.line("한 번에 보내는 조각 수", "큰 파일을 이만큼씩 나눠 모델에 보내요 (1~1000). 비우면 한 번에 보내요.", box("extraction_batch_size", 1000, "한 번에 보내는 조각 수")),
        origin("extraction_batch_size", () => { setDraft("extraction_batch_size", 0); redraw(); }),
        kit.line("조각마다 그림 수", "한 조각과 함께 읽는 그림의 상한이에요 (1~64). 비우면 제한 없이 읽어요.", box("extraction_images_per_chunk", 64, "조각마다 그림 수")),
        origin("extraction_images_per_chunk", () => { setDraft("extraction_images_per_chunk", 0); redraw(); }),
        ...flagOn(kit, "store_document_text", "문서 글 보관", "끄면 파일에서 읽은 글을 저장하지 않아요. 그러면 글 고치기와 다시 읽기를 할 수 없어요.")];
    }
    // like flag, but unset means on (the server keeps document text unless told not to)
    const flagOn = (kit, key, title, hint) => {
      const d = S.draft, edit = human() && d;
      const v = d && d[key] != null ? d[key] : S.eff[key];
      const on = v !== false;
      return [kit.line(title, hint, edit ? kit.sw(on, title, (next) => { d[key] = next; redraw(); }) : onOff(on)), origin(key, () => { d[key] = null; redraw(); })];
    };

    // recall_budget: how many records each budget level reads (one block, inherited whole)
    const BUDGET_FNS = [["", "기본값"], ["fixed", "개수로 정하기"], ["adaptive", "답 길이에 맞추기"]];
    function recallBudget(kit) {
      const d = S.draft, edit = human() && d;
      const b = (d?.recall_budget ? d.recall_budget : S.eff.recall_budget) || {};
      const set = (part) => setBlock("recall_budget", part);
      const out = [kit.line("읽는 양 정하는 법", "적게·보통·많이가 기억을 몇 개 찾을지예요. 기본값은 지금처럼 정해진 개수예요.",
        edit ? select(BUDGET_FNS, b.function || "", "읽는 양 정하는 법", (v) => set({ function: v || undefined })) : readOnly(BUDGET_FNS.find(([k]) => k === (b.function || ""))?.[1] || b.function))];
      if (b.function === "fixed") {
        out.push(field("단계마다 찾는 기억 수", grid(...BUDGET.map(([lv, name]) => [name, numberBox(b[`fixed_${lv}`], 1, 50, "", `${name} 기억 수`, edit, (n) => set({ [`fixed_${lv}`]: n || undefined }))])), "1~50개. 비우면 기본값을 써요."));
      } else if (b.function === "adaptive") {
        out.push(field("답 길이에 곱하는 비율", grid(...BUDGET.map(([lv, name]) => [name, numberBox(b[`adaptive_${lv}`], 0.01, 1, "", `${name} 비율`, edit, (n) => set({ [`adaptive_${lv}`]: n || undefined }), 0.01)])),
          "답 토큰 수 × 비율만큼 찾아요 (0.01~1). 비우면 기본값을 써요."),
        field("찾는 기억 수 범위", grid(["최소", numberBox(b.min, 1, 50, "", "최소 기억 수", edit, (n) => set({ min: n || undefined }))], ["최대", numberBox(b.max, 1, 50, "", "최대 기억 수", edit, (n) => set({ max: n || undefined }))]), "1~50개."));
        if (b.min && b.max && b.min > b.max) out.push(el("p", "pg-hint warn", "최소가 최대보다 커요."));
      }
      out.push(origin("recall_budget", () => { d.recall_budget = null; redraw(); }));
      return out;
    }

    const LEVELS = [["0", "정하지 않음"], ["1", "1 · 아주 낮게"], ["2", "2"], ["3", "3 · 보통"], ["4", "4"], ["5", "5 · 아주 높게"]];
    const DISPOSITION = [["skepticism", "의심하는 정도", "기억끼리 다르면 얼마나 조심스럽게 말할지"], ["literalism", "글자 그대로 읽는 정도", "기억에 적힌 말을 얼마나 그대로 따를지"], ["empathy", "마음을 헤아리는 정도", "사람의 사정과 기분을 얼마나 살필지"]];
    function reflectGroup(kit) {
      const g = kit.group("질문에 답하는 방식", "에이전트가 기억을 모아 답할 때(reflect) 따를 목표와 태도예요.");
      const d = S.draft, edit = human() && d;
      g.append(field("답할 때 지킬 목표", edit ? area(d.reflect_mission, 2000, "답할 때 지킬 목표", "예: 이 저장소의 결정 이유를 짧게, 근거 기억과 함께 알려 줘요.", (v) => setDraft("reflect_mission", v)) : readOnly(S.eff.reflect_mission || "없음")),
        origin("reflect_mission", () => { setDraft("reflect_mission", ""); redraw(); }));
      const disp = d ? d.reflect_disposition || {} : S.eff.reflect_disposition || {};
      // with nothing set here, "0" is what the template or server gives, so it says so (like 뽑는 방식's 물려받기)
      const inh = (k) => (!d?.reflect_disposition && S.eff.reflect_disposition?.[k] ? [["0", `물려받기 · ${S.eff.reflect_disposition[k]}`], ...LEVELS.slice(1)] : LEVELS);
      const rows = DISPOSITION.map(([k, title, hint]) => kit.line(title, hint, edit ? select(inh(k), String(disp[k] || 0), title, (v) => {
        const next = { ...(d.reflect_disposition || {}), [k]: Number(v) };
        d.reflect_disposition = Object.values(next).some((n) => n) ? next : null; redraw();
      }) : readOnly(disp[k] ? String(disp[k]) : "정하지 않음")));
      const maxTok = numberBox(d?.reflect_max_tokens, 64, 4096, S.eff.reflect_max_tokens, "답 길이 상한", edit, (n) => { setDraft("reflect_max_tokens", n); redraw(); });
      g.append(kit.more("자세히 · 답하는 태도", ...rows, origin("reflect_disposition", () => { d.reflect_disposition = null; redraw(); }),
        kit.line("답 길이 상한", "답 한 번에 쓸 수 있는 토큰 수예요 (64~4096). 비우면 물려받아요.", maxTok), origin("reflect_max_tokens", () => { setDraft("reflect_max_tokens", 0); redraw(); })));
      g.append(kit.more("자세히 · 답할 때 읽는 것", ...reflectDefaults(kit), ...recallBudget(kit)));
      return g;
    }

    // reflect_default_options: what a reflect request takes for what it leaves unset (one block, inherited whole)
    const BUDGET = [["low", "적게"], ["mid", "보통"], ["high", "많이"]];
    const FACTS = [["", "기억과 관찰 모두"], ["memory", "기억만"], ["observation", "관찰만"]];
    const TOOL_CAPS = [["recall_max_tokens", "찾은 기억", 64], ["recall_max_chunk_tokens", "원문 조각", 16], ["observations_max_tokens", "관찰", 64], ["mental_models_max_tokens", "주제 요약 목록", 64],
      ["read_mental_models_max_tokens", "주제 요약 읽기", 64], ["expand_max_tokens", "더 펼쳐 읽기", 64]];
    function reflectDefaults(kit) {
      const d = S.draft, edit = human() && d;
      const o = (d?.reflect_default_options ? d.reflect_default_options : S.eff.reflect_default_options) || {};
      const set = (part) => setBlock("reflect_default_options", part);
      const sw = (k, title, hint) => kit.line(title, hint, edit ? kit.sw(!!o[k], title, (next) => set({ [k]: next || undefined })) : onOff(!!o[k]));
      const ft = o.fact_types?.length === 1 ? o.fact_types[0] : "";
      const tools = o.tools || {};
      const caps = grid(...TOOL_CAPS.map(([k, name, min]) => [name, numberBox(tools[k], min, 32000, "", `${name} 토큰`, edit, (n) => set({ tools: { ...tools, [k]: n || undefined } }))]));
      return [
        kit.line("읽을 기억의 양", "답하기 전에 기억을 얼마나 많이 찾아 읽을지예요.", edit ? select([["", "기본값"], ...BUDGET], o.budget || "", "읽을 기억의 양", (v) => set({ budget: v || undefined })) : readOnly(BUDGET.find(([k]) => k === o.budget)?.[1] || "기본값")),
        kit.line("읽을 종류", "", edit ? select(FACTS, ft, "읽을 종류", (v) => set({ fact_types: v ? [v] : undefined })) : readOnly(FACTS.find(([k]) => k === ft)[1])),
        sw("exclude_mental_models", "주제 요약은 빼고 답하기", "승인한 주제 요약을 읽지 않고 기억만으로 답해요."),
        sw("tag_scoped_directives", "태그가 맞는 지시만 쓰기", "태그를 붙인 지시는 같은 태그로 물을 때만 따라요."),
        field("도구 한 번에 읽는 양 (토큰)", caps, "비우면 기본값을 써요."),
        ...decisionModel(kit, edit, "reflect", "답하기 전에 관계없는 기억 거르기", "작은 판정 모델이 질문과 관계없는 기억을 먼저 빼고, 이미 답할 수 있으면 더 찾지 않게 해요."),
        origin("reflect_default_options", () => { d.reflect_default_options = null; redraw(); })];
    }

    const INTERVALS = [["", "물려받기"], ["1h", "1시간마다"], ["6h", "6시간마다"], ["24h", "하루마다"], ["168h", "일주일마다"]];
    function deriveGroup(kit) {
      const g = kit.group("자동 정리 제안", "승인한 기억이 쌓이면 서버 모델이 읽고 관찰·원인·이름 정리를 제안해요. 제안은 내가 승인해야 쓰여요.");
      const d = S.draft, edit = human() && d, cur = d ? d.auto_derive || {} : {};
      const on = d && cur.enabled != null ? !!cur.enabled : !!S.eff.auto_derive?.enabled;
      const set = (patchIn) => { const next = { ...(d.auto_derive || {}), ...patchIn }; d.auto_derive = unset(next) ? null : next; redraw(); };
      g.append(kit.line("자동으로 정리 제안 만들기", "처음에는 꺼져 있어요. 켜면 모델 사용량(예산)을 써요.", edit ? kit.sw(on, "자동으로 정리 제안 만들기", (next) => set({ enabled: next })) : readOnly(on ? "켜짐" : "꺼짐")));
      if (on) g.append(el("p", "pg-hint warn", "켜져 있어요. 정리할 때마다 서버 모델을 불러 모델 예산을 써요."));
      const thr = numberBox(cur.threshold, 1, 1000, S.eff.auto_derive?.threshold, "몇 개 승인마다", edit, (n) => set({ threshold: n || undefined }));
      const every = edit ? select(INTERVALS, cur.interval || "", "얼마나 자주", (v) => set({ interval: v || undefined })) : readOnly(S.eff.auto_derive?.interval || "—");
      g.append(kit.more("자세히 · 언제 만들지", kit.line("몇 개 승인마다", "승인한 기억이 이만큼 쌓이면 정리를 시작해요 (1~1000)", thr), kit.line("얼마나 자주", "쌓인 게 적어도 이 간격이 지나면 정리해요", every)),
        origin("auto_derive.enabled", () => { d.auto_derive = null; redraw(); }));
      g.append(kit.more("자세히 · 관찰 정리 방식", ...consolidation(kit)), kit.more("자세히 · 주제 요약 기본값", ...mentalModels(kit)));
      return g;
    }

    // consolidation: the observation mission, how memories group into scopes, run sizes and the strategy rules (JSON)
    const SCOPE_MODES = [["shared", "모두 한 묶음"], ["combined", "태그 조합마다"], ["per_tag", "태그마다"], ["all_combinations", "모든 태그 조합마다"]];
    function consolidation(kit) {
      const d = S.draft, edit = human() && d;
      const c = (d?.consolidation ? d.consolidation : S.eff.consolidation) || {};
      const set = (part) => setBlock("consolidation", part);
      const sizes = grid(["묶음마다 관찰 수", numberBox(c.max_observations_per_scope, 1, 1000, "", "묶음마다 관찰 수", edit, (n) => set({ max_observations_per_scope: n || undefined }))],
        ["한 번에 정리할 묶음", numberBox(c.batch_size, 1, 10, "", "한 번에 정리할 묶음", edit, (n) => set({ batch_size: n || undefined }))],
        ["동시에 정리", numberBox(c.parallel, 1, 4, "", "동시에 정리", edit, (n) => set({ parallel: n || undefined }))],
        ["한 번에 읽을 기억", numberBox(c.max_memories_per_round, 1, 100000, "", "한 번에 읽을 기억", edit, (n) => set({ max_memories_per_round: n || undefined }))]);
      const out = [
        field("관찰을 만들 때 목표", edit ? area(c.observations_mission, 2000, "관찰을 만들 때 목표", "예: 반복되는 실수와 그 해결을 관찰로 남겨요.", (v) => set({ observations_mission: v || undefined })) : readOnly(c.observations_mission || "없음")),
        kit.line("묶는 방식", "어떤 기억끼리 모아 관찰을 만들지예요.", edit ? select([["", "기본값 · 모두 한 묶음"], ...SCOPE_MODES], c.default_scope_mode || "", "묶는 방식", (v) => set({ default_scope_mode: v || undefined }))
          : readOnly(SCOPE_MODES.find(([k]) => k === c.default_scope_mode)?.[1] || "모두 한 묶음")),
        field("크기", sizes, "비우면 기본값을 써요."),
        ...decisionModel(kit, edit, "consolidation", "정리 전에 쉬운 것 먼저 거르기", "이미 관찰에 있는 내용이나 잡담은 작은 판정 모델이 먼저 걸러 서버 모델 호출을 줄여요. 아무것도 쓰지 않아요."),
        origin("decision_models", () => { d.decision_models = null; redraw(); })];
      out.push(strategyEditor(c, edit, set));
      // the rule lists as JSON: kept in S.rules while being typed, so a half-typed object is not lost on redraw
      if (!edit) {
        out.push(kit.more("자세히 · JSON으로 보기", field("정리 전략 규칙 (JSON)", pre(rulesText(c) || "없음"))), origin("consolidation"));
        return out;
      }
      if (S.rules === null) S.rules = rulesText(c);
      const rules = el("textarea", "mono"); rules.rows = 6; rules.value = S.rules; rules.spellcheck = false; rules.setAttribute("aria-label", "정리 전략 규칙 (JSON)");
      rules.placeholder = '{"strategies": [{"name": "bugs", "scopes": [{"tags": ["bug*"]}]}]}';
      const bad = parseRules(S.rules) === null;
      rules.addEventListener("input", () => { S.rules = rules.value; });
      rules.addEventListener("change", () => { const r = parseRules(rules.value); if (r) set({ default_scopes: undefined, strategies: undefined, scope_limits: undefined, ...r }); else redraw(); });
      out.push(button("전략 미리 보기", "ghost", null, () => void strategyPreview()));
      if (S.trial.strat) out.push(S.trial.strat);
      out.push(kit.more("자세히 · JSON으로 직접 고치기", field("정리 전략 규칙 (JSON)", rules, "default_scopes · strategies · scope_limits 를 적어요. 태그 모양으로 어떤 전략이 어떤 묶음을 맡을지 정해요."),
        ...(bad ? [el("small", "warn", "JSON을 읽을 수 없거나 모르는 이름이 있어요. 저장하지 않았어요.")] : [])));
      out.push(origin("consolidation", () => { d.consolidation = null; S.rules = null; redraw(); }));
      return out;
    }
    // 정리 전략: one card per strategy, the first whose tag rule fits a note group takes it; 맡은 묶음 counts come from the last 전략 미리 보기
    const MATCH = [["", "이 태그가 모두 있는 묶음"], ["exact", "딱 이 태그만 있는 묶음"]];
    function strategyEditor(c, edit, set) {
      const list = c.strategies || [], counts = S.trial.stratCounts || {};
      const save = (next) => { S.rules = null; set({ strategies: next.length ? next : undefined }); };
      const put = (i, part) => save(list.map((st, k) => (k === i ? { ...st, ...part } : st)));
      const box = el("div", "field wide sp-strats", "정리 전략");
      box.append(el("small", "", "위에 있는 전략이 먼저 골라요. 태그 규칙에 맞는 기억 묶음을 그 전략이 맡아 관찰을 만들어요. 태그 끝에 *를 붙이면 그 앞이 같은 태그 모두예요."));
      if (!list.length) box.append(el("p", "pg-hint", "전략이 없어 모두 기본 방식으로 정리해요."));
      list.forEach((st, i) => {
        const card = el("div", "sp-strat"), head = el("div", "sp-strat-row");
        head.append(textBox(st.name, 64, "전략 이름", "예: bugs", edit, (v) => { S.trial.stratCounts = renameKey(counts, st.name, v); put(i, { name: v }); }));
        if (counts[st.name] != null) head.append(el("small", "sp-strat-count", `묶음 ${counts[st.name]}개 맡음`));
        if (edit) {
          const up = button("위로", "ghost small", null, () => save(moveAt(list, i, -1))), down = button("아래로", "ghost small", null, () => save(moveAt(list, i, 1)));
          up.disabled = i === 0; down.disabled = i === list.length - 1;
          head.append(up, down, button("지우기", "ghost small", null, async () => { if (await window.ask("이 전략을 지울까요? 저장해야 반영돼요.", "지우기")) save(list.filter((_, k) => k !== i)); }));
        }
        card.append(head);
        const rules = st.scopes || [];
        const putRule = (j, part) => put(i, { scopes: rules.map((r, k) => (k === j ? { ...r, ...part } : r)) });
        rules.forEach((r, j) => {
          const line = el("div", "sp-strat-row");
          line.append(textBox((r.tags || []).join(", "), 2000, "태그", "예: bug*, 배포", edit, (v) => putRule(j, { tags: globalThis.ArkismKeep.listOf(v) })),
            edit ? select(MATCH, r.tags_match === "exact" ? "exact" : "", "맞추는 방식", (v) => putRule(j, { tags_match: v || undefined })) : readOnly(MATCH.find(([k]) => k === (r.tags_match === "exact" ? "exact" : ""))[1]));
          if (edit && rules.length > 1) line.append(button("빼기", "ghost small", null, () => put(i, { scopes: rules.filter((_, k) => k !== j) })));
          card.append(line);
        });
        if (edit && rules.length < 8) card.append(button("태그 규칙 더하기", "ghost small", null, () => put(i, { scopes: [...rules, { tags: [] }] })));
        box.append(card);
      });
      const issue = strategyIssue(list);
      if (issue) box.append(el("small", "warn", issue));
      if (edit && list.length < 16) box.append(button("전략 더하기", "ghost small", null, () => save([...list, { name: `rule-${list.length + 1}`, scopes: [{ tags: [] }] }])));
      return box;
    }
    // decision_models.<task>: a small decision engine settles easy cases of a task before the server model is asked (off by default)
    const ENGINES = [["off", "쓰지 않기"], ["clef", "clef"], ["jev", "jev"]];
    function decisionModel(kit, edit, task, title, hint) {
      const d = S.draft, eff = S.eff.decision_models?.[task] || "off";
      const own = d?.decision_models?.[task] || "";
      const list = [["", d?.decision_models ? "물려받기" : `물려받기 · ${ENGINES.find(([k]) => k === eff)?.[1] || eff}`], ...ENGINES];
      const pick = (v) => {
        const next = { ...(d.decision_models || S.eff.decision_models || {}) };
        if (v) next[task] = v; else delete next[task];
        d.decision_models = Object.keys(next).length ? next : null; redraw();
      };
      const ctl = edit ? select(list, own, title, pick) : readOnly(ENGINES.find(([k]) => k === eff)?.[1] || eff);
      const out = [kit.line(title, hint, ctl)];
      if ((own || eff) !== "off") out.push(el("p", "pg-hint warn", "켜져 있어요. 판정할 때마다 서버 모델의 하루 사용 한도에 함께 셈해요."));
      return out;
    }
    async function strategyPreview() {
      const box = el("div", "sp-test-out"); box.setAttribute("role", "status");
      try {
        const body = { scope: { workspace_id: state.scope.workspace_id, project_id: state.scope.project_id } };
        if (S.draft?.consolidation) body.consolidation = S.draft.consolidation;
        const r = await request("/v1/observations/strategies/preview", { method: "POST", body: JSON.stringify(body) });
        box.append(el("strong", "", `묶음 ${r.scopes ?? 0}개 · 맡은 전략 없음 ${r.unclaimed ?? 0}개`));
        for (const st of r.strategies || []) box.append(el("small", "", `${st.name} · 묶음 ${st.claimed ?? 0}개 맡음`));
        S.trial.stratCounts = Object.fromEntries((r.strategies || []).map((st) => [st.name, st.claimed ?? 0]));
        if (!(r.strategies || []).length) box.append(el("small", "", "전략이 없어 모두 기본 방식으로 정리해요."));
      } catch (e) { box.append(el("small", "warn", errText(e))); }
      S.trial.strat = box; redraw();
    }

    // mental_model_defaults: the trigger a summary made without one takes, and the shortest gap between refreshes
    const MM_MODES = [["manual", "직접 할 때만"], ["on_change", "기억이 바뀌면"], ["daily", "하루마다"]];
    const GAPS = [["", "기본값"], ["1h", "1시간"], ["6h", "6시간"], ["24h", "하루"], ["168h", "일주일"]];
    function mentalModels(kit) {
      const d = S.draft, edit = human() && d;
      const m = (d?.mental_model_defaults ? d.mental_model_defaults : S.eff.mental_model_defaults) || {};
      const t = m.trigger || null;
      const set = (part) => setBlock("mental_model_defaults", part);
      const setT = (part) => set({ trigger: { ...t, ...part } });
      const out = [kit.line("언제 새로 고칠지", "새 주제 요약을 만들 때 따로 정하지 않으면 이대로 해요. 새로 고친 요약도 내가 승인해야 쓰여요.",
        edit ? select([["", "기본값"], ...MM_MODES], t?.mode || "", "언제 새로 고칠지", (v) => set({ trigger: v ? { ...t, mode: v } : undefined })) : readOnly(MM_MODES.find(([k]) => k === t?.mode)?.[1] || "기본값"))];
      if (t) {
        const cron = textBox(t.cron, 64, "정해진 시각 (cron)", "0 9 * * 1", edit, (v) => setT({ cron: v || undefined }));
        const look = button("다음 시각 보기", "ghost", null, () => void cronPreview(t.cron));
        const bar = el("div", "pg-input-act"); bar.append(cron, look);
        out.push(field("정해진 시각 (cron, UTC)", bar, "다섯 칸으로 써요. 예: 0 9 * * 1 = 월요일 9시(UTC)."));
        if (S.trial.cronOut) out.push(S.trial.cronOut);
        out.push(kit.line("관찰 정리 뒤에 고치기", "관찰을 정리하고 나면 요약도 새로 고쳐요.", edit ? kit.sw(!!t.after_consolidation, "관찰 정리 뒤에 고치기", (next) => setT({ after_consolidation: next || undefined })) : onOff(!!t.after_consolidation)));
      }
      out.push(kit.line("가장 짧은 간격", "요약을 이보다 자주 고치지 않아요.", edit ? select(withValue(GAPS, m.min_interval), m.min_interval || "", "가장 짧은 간격", (v) => set({ min_interval: v || undefined })) : readOnly(m.min_interval || "기본값")),
        origin("mental_model_defaults", () => { d.mental_model_defaults = null; redraw(); }),
        ...decisionModel(kit, edit, "mental_model_refresh", "요약 쓰기 전에 관계없는 기억 거르기", "작은 판정 모델이 주제와 관계없는 기억을 먼저 빼서 요약이 흐려지지 않게 해요."),
        ...decisionModel(kit, edit, "mental_model_delta", "바뀐 부분만 고칠 때 먼저 거르기", "새 기억이 요약을 바꿀 수 있을 때만 서버 모델을 불러요. 바꿀 게 없으면 호출하지 않아요."));
      return out;
    }
    async function cronPreview(cron) {
      const box = el("div", "sp-test-out"); box.setAttribute("role", "status");
      if (!cron) { box.append(el("small", "warn", "cron을 먼저 넣어 주세요.")); S.trial.cronOut = box; redraw(); return; }
      try {
        const r = await request("/v1/mental-models/cron-preview", { method: "POST", body: JSON.stringify({ scope: { workspace_id: state.scope.workspace_id, project_id: state.scope.project_id }, cron, count: 5 }) });
        box.append(el("strong", "", "다음 실행 시각"), ...(r.next || []).map((n) => pre(new Date(n).toLocaleString())));
      } catch (e) { box.append(el("small", "warn", e?.message === "invalid_request" ? "cron을 읽을 수 없어요. 다섯 칸인지 확인해 주세요." : errText(e))); }
      S.trial.cronOut = box; redraw();
    }

    // 템플릿: the server's default template (ARKISM_SPACE_DEFAULT_TEMPLATE) sets up a space that has no saved settings yet;
    // its rules and outlines arrive as candidates to review, never active.
    function templateGroup(kit, spaceJob) {
      const g = kit.group("기본 템플릿", "서버에 준비된 기본 템플릿의 규칙과 정리 틀을 이 공간에 가져와요. 설정을 한 번도 저장하지 않은 공간에서만 돼요. 가져온 것은 후보로 들어가요.");
      const has = Object.values(S.src).some((s) => String(s).includes("template"));
      const b = button("기본 템플릿 가져오기", "", null, async () => {
        if (!(await ask("서버의 기본 템플릿을 이 공간에 가져올까요? 바로 쓰이지 않고 '확인해 주세요'에 후보로 들어가요.", "가져오기"))) return;
        S.job = "가져오는 중…"; redraw();
        try {
          const job = await spaceJob("imports", await request(`${path()}/import`, { method: "POST", body: JSON.stringify({ default_template: true, import_as: "candidate" }) }));
          S.job = `후보 ${job.report?.imported || 0}개를 가져왔어요. '확인해 주세요'에서 승인해 주세요.`; S.loaded = false; void load();
        } catch (e) { S.job = { not_found: "서버에 기본 템플릿이 없어요.", revision_conflict: "이미 설정을 저장한 공간이라 템플릿을 가져올 수 없어요.", access_denied: "템플릿은 사람 계정만 가져올 수 있어요." }[e?.message] || errText(e); }
        redraw();
      });
      b.disabled = S.revision > 0 || !human();
      g.append(kit.line(has ? "템플릿이 있어요" : "템플릿", S.revision > 0 ? "이 공간은 설정을 저장한 적이 있어 가져올 수 없어요." : "새 공간이라 가져올 수 있어요.", b));
      if (S.job) g.append(el("p", "pg-hint", S.job));
      return g;
    }

    function tab(kit, extra = []) {
      if (S.key && S.key !== scopeKey()) Object.assign(S, { loaded: false, stored: null, draft: null, eff: null, src: {}, job: "", sample: "", chip: {}, rules: null, trial: { text: "", strategy: "", run: "prompt", op: "retain", out: null, busy: false } });
      if (!S.loaded) void load();
      const { workspace_id: w = "", project_id: p = "" } = state.scope || {};
      const ws = el("b", "mono", w || "—"), pj = el("b", "mono", p || "—"); ws.dataset.noI18n = pj.dataset.noI18n = "";
      const kv = (k, v) => { const r = kit.kvRow(k, v); r.classList.add("sp-kv"); return r; }; // labels as wide as they need (German ones are long)
      kit.side(kit.sideSec("이 공간", kv("작업 공간", ws), kv("프로젝트", pj), kv("저장한 횟수", String(S.revision || 0))),
        kit.sideNote("값 아래의 작은 글은 그 값이 어디서 왔는지예요: 이 공간에서 정함 · 템플릿에서 옴 · 기본값. ‘기본값으로’를 누르면 이 공간의 값을 지우고 물려받아요."));
      if (human() && S.stored && S.revision > 0) kit.side(kit.more("자세히 · 모두 물려받기", kit.sideNote("이 공간에서 정한 동작 설정을 모두 지워요. 이름·메모는 남아요."), button("모두 물려받기", "ghost", null, resetAll)));
      if (!S.loaded) return [el("p", "pg-hint", "불러오는 중…"), ...extra];
      if (S.err) return [el("p", "pg-hint warn", S.err), ...extra];
      const out = [];
      if (!human()) out.push(el("p", "pg-hint", "에이전트 계정이라 보기만 할 수 있어요. 바꾸려면 사람 계정으로 로그인해 주세요."));
      else if (!S.stored) out.push(el("p", "pg-hint warn", "이 계정에는 공간 설정을 바꿀 권한이 없어요. 서버 관리자에게 공간 설정 권한을 받아 주세요."));
      out.push(extractGroup(kit), defenseGroup(kit), entityGroup(kit), recallGroup(kit), reflectGroup(kit), deriveGroup(kit), toolsGroup(kit), auditGroup(kit), templateGroup(kit, kit.spaceJob), ...extra);
      if (dirty()) {
        const bar = el("div", "pg-save");
        const ok = button("저장", "primary", null, save); ok.disabled = S.busy;
        bar.append(el("span", "", "저장하지 않은 변경이 있어요."), button("취소", "ghost", null, () => { S.draft = clone(S.stored); S.rules = null; redraw(); }), ok);
        out.push(bar);
      }
      return out;
    }
    return { tab, reset() { S.loaded = false; S.key = ""; } };
  }
  const api = Object.assign(create, lib);
  root.ArkismSpaceSettings = api;
  if (typeof window === "undefined" && typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
