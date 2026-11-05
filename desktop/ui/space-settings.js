// @ts-check
// 설정 › 기억 공간: one space's settings over GET/PATCH/PUT /v1/spaces/{workspace}/{project}/config and where each value
// comes from (GET …/config/effective: this space, the server's template or the server default). Only a person edits;
// an agent token sees what the server shows it, read-only. Model output never activates here: a template import lands
// as candidates and automatic derivation only makes proposals.
(function (root) {
  "use strict";
  // ------------------------------------------------------------ pure helpers (node tests require them)
  const MODES = [["concise", "간결하게"], ["verbose", "자세하게"], ["verbatim", "원문 그대로"], ["custom", "직접 쓰기"]];
  const SOURCE = { space: "이 공간에서 정함", template: "템플릿에서 옴", server: "기본값" };
  // "template+space": every layer that adds defense rules
  const sourceLabel = (src) => String(src || "server").split("+").map((s) => SOURCE[s] || s).join(" + ");
  // The behaviour settings this screen edits and the value that hands each back to the layer below (PATCH semantics:
  // an empty string, 0, [] or {} clears; the server treats an all-empty auto_derive or reflect_disposition as cleared).
  // lanes and rerank have no empty value: a PATCH can set them but only 모두 물려받기 (PUT) hands them back.
  const EDIT = { extraction_enabled: undefined, extraction_mode: "", extraction_instructions: "", defense_rules: [], mcp_tools: [], reflect_mission: "", reflect_disposition: {}, auto_derive: {},
    lanes: undefined, rerank: undefined, recall_budget_tokens: 0, reflect_max_tokens: 0 };
  const unset = (v) => v === undefined || v === null || v === "" || v === 0 || (Array.isArray(v) && !v.length) || (typeof v === "object" && !Array.isArray(v) && !Object.values(v).some((x) => x !== undefined && x !== null && x !== "" && x !== 0));
  // What a PATCH must carry to turn stored into draft: only the keys that changed, cleared ones as their empty value.
  function configPatch(stored, draft) {
    const out = {};
    for (const [k, empty] of Object.entries(EDIT)) {
      const a = stored?.[k], b = draft?.[k];
      if (JSON.stringify(unset(a) ? null : a) === JSON.stringify(unset(b) ? null : b)) continue;
      out[k] = unset(b) && empty !== undefined ? empty : b;
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
  const lib = { MODES, sourceLabel, configPatch, resetBody, compile, testRules, TOOL };

  // ------------------------------------------------------------ the tab
  function create(ctx) {
    const { state, el, button, icon, request, notice, render } = ctx;
    const S = { key: "", loading: false, loaded: false, err: "", stored: null, revision: 0, eff: null, src: {}, draft: null, busy: false, sample: "", tool: "", job: "" };
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
        S.draft = S.stored ? clone(S.stored) : null; S.err = "";
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
      if (!(await ask("이 공간의 기억 뽑기·막을 내용·도구·답하는 방식·자동 정리 설정을 모두 지우고 템플릿과 기본값을 따를까요?", "모두 물려받기"))) return;
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
      return unset(sub ? v?.[sub] : v);
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
    const numberBox = (value, min, max, placeholder, label, editable, onSet) => {
      // the settings select box look, so a number sits like a choice
      const n = el("input", "ag-select"); n.type = "number"; n.min = String(min); n.max = String(max); n.value = value ? String(value) : ""; n.placeholder = placeholder ? String(placeholder) : ""; n.setAttribute("aria-label", label);
      n.disabled = !editable;
      n.addEventListener("change", () => {
        const v = n.value.trim() === "" ? 0 : Math.round(Number(n.value));
        if (v === 0 || (v >= min && v <= max)) onSet(v); else { n.value = value ? String(value) : ""; notice(`${min}에서 ${max} 사이로 넣어 주세요.`); }
      });
      return n;
    };
    const readOnly = (text) => { const o = el("output", "", text); o.dataset.noI18n = ""; return o; };
    const modeName = (m) => MODES.find(([k]) => k === m)?.[1] || "간결하게";

    // ---------------------------------------------------------- groups
    function extractGroup(kit) {
      const g = kit.group("기억 뽑기", "대화와 파일에서 기억 후보를 만드는 방식이에요. 후보는 내가 승인해야 기억이 돼요.");
      const d = S.draft, edit = human() && d;
      const on = d ? d.extraction_enabled !== false : S.eff.extraction_enabled !== false;
      g.append(kit.line("자동으로 기억 뽑기", on ? "대화와 파일에서 기억 후보를 만들어요." : "멈췄어요. 자동 수집과 서버의 기억 뽑기를 하지 않아요. 직접 저장은 그대로 돼요.",
        edit ? kit.sw(on, "자동으로 기억 뽑기", (next) => { d.extraction_enabled = next; redraw(); }) : readOnly(on ? "켜짐" : "꺼짐")));
      const inherited = S.src.extraction_mode !== "space" ? `물려받기 · ${modeName(S.eff.extraction_mode)}` : "물려받기";
      const mode = edit ? select([["", inherited], ...MODES], d.extraction_mode || "", "뽑는 방식", (v) => { setDraft("extraction_mode", v); redraw(); }) : readOnly(modeName(S.eff.extraction_mode));
      const modeRow = kit.line("뽑는 방식", "간결하게: 핵심만 · 자세하게: 맥락까지 · 원문 그대로: 말한 그대로 · 직접 쓰기: 아래 안내를 따라요", mode);
      g.append(modeRow, origin("extraction_mode", () => { setDraft("extraction_mode", ""); redraw(); }));
      const custom = (d?.extraction_mode || S.eff.extraction_mode) === "custom";
      const guide = edit ? area(d.extraction_instructions, 2000, "뽑을 때 따를 안내", "예: 배포 절차와 고객 요청은 꼭 남기고, 잡담은 빼 주세요.", (v) => setDraft("extraction_instructions", v))
        : readOnly(S.eff.extraction_instructions || "없음");
      g.append(field(custom ? "뽑을 때 따를 안내 (직접 쓰기에 꼭 필요해요)" : "뽑을 때 따를 안내 (선택)", guide, "서버가 기억을 뽑을 때 읽어요. 비밀값·개인정보 규칙보다 앞서지는 않아요. 2000자까지."),
        origin("extraction_instructions", () => { setDraft("extraction_instructions", ""); redraw(); }));
      if (edit && custom && !(d.extraction_instructions || "").trim()) g.append(el("p", "pg-hint warn", "직접 쓰기는 안내가 있어야 저장할 수 있어요."));
      return g;
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

    function toolsGroup(kit) {
      const g = kit.group("에이전트가 쓸 도구", "이 공간에서 에이전트가 쓸 Arkism 도구(MCP)예요. 비워 두면 모두 써요.");
      const list = S.draft ? S.draft.mcp_tools || [] : S.eff.mcp_tools || [];
      const chips = el("div", "sp-chips");
      for (const t of list) {
        const c = el("span", "pill on sp-chip"), name = el("span", "mono", t); name.dataset.noI18n = "";
        c.append(name);
        if (human() && S.draft) { const x = el("button", "icon-btn plain"); x.type = "button"; x.setAttribute("aria-label", `${t} 빼기`); x.append(icon("x")); x.addEventListener("click", () => { S.draft.mcp_tools = list.filter((y) => y !== t); redraw(); }); c.append(x); }
        chips.append(c);
      }
      if (!list.length) chips.append(el("small", "pg-hint", "모든 도구를 써요."));
      g.append(chips);
      if (human() && S.draft) {
        const input = el("input", "mono"); input.placeholder = "recall"; input.value = S.tool; input.setAttribute("aria-label", "도구 이름"); input.spellcheck = false;
        const msg = el("small", "warn");
        const add = button("도구 추가", "", null, () => {
          const t = input.value.trim();
          if (!TOOL.test(t)) { msg.textContent = "도구 이름은 영어 소문자·숫자·밑줄로 써요. 예: recall"; return; }
          if (list.includes(t)) { msg.textContent = "이미 있는 도구예요."; return; }
          if (list.length >= 64) { msg.textContent = "64개까지 넣을 수 있어요."; return; }
          S.tool = ""; S.draft.mcp_tools = [...list, t]; redraw();
        });
        input.addEventListener("input", () => { S.tool = input.value; });
        input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); add.click(); } });
        const box = el("div", "pg-input-act"); box.append(input, add);
        g.append(kit.more("자세히 · 도구 고르기", field("도구 이름", box, "이름 하나를 넣고 추가해요. 하나라도 넣으면 넣은 것만 써요."), msg));
      }
      g.append(origin("mcp_tools", () => { S.draft.mcp_tools = []; redraw(); }));
      return g;
    }

    // 기억 찾기: which recall lanes run when the caller names none, the model re-ranking (budget) and the default context size
    const LANES = [["fts", "낱말로 찾기", "적힌 낱말이 맞는 기억"], ["vector", "뜻으로 찾기", "낱말이 달라도 뜻이 비슷한 기억"], ["temporal", "시간으로 찾기", "물어본 때와 가까운 기억"], ["graph", "연결로 찾기", "같은 이름으로 이어진 기억"]];
    function recallGroup(kit) {
      const g = kit.group("기억 찾기", "에이전트가 기억을 찾을 때 쓰는 방법이에요. 에이전트가 따로 정하지 않을 때 이대로 찾아요.");
      const d = S.draft, edit = human() && d;
      const lane = (k) => (d?.lanes?.[k] ?? S.eff.lanes?.[k]) !== false;
      const rows = LANES.map(([k, title, hint]) => [kit.line(title, hint, edit ? kit.sw(lane(k), title, (next) => { d.lanes = { ...(d.lanes || {}), [k]: next }; redraw(); }) : readOnly(lane(k) ? "켜짐" : "꺼짐")), origin(`lanes.${k}`)]).flat();
      const rr = d?.rerank ?? S.eff.rerank;
      g.append(kit.line("검색 결과 다시 고르기", "찾은 기억을 모델이 한 번 더 읽고 순서를 고쳐요. 켜 두면 찾을 때마다 모델 사용량(예산)을 써요.",
        edit ? kit.sw(!!rr, "검색 결과 다시 고르기", (next) => { d.rerank = next; redraw(); }) : readOnly(rr ? "켜짐" : "꺼짐")), origin("rerank"));
      const budget = numberBox(d?.recall_budget_tokens, 1, 64000, S.eff.recall_budget_tokens, "에이전트에 줄 기억 양", edit, (n) => { setDraft("recall_budget_tokens", n); redraw(); });
      g.append(kit.more("자세히 · 찾는 방법과 양", ...rows, kit.line("에이전트에 줄 기억 양", "한 번에 넘기는 기억의 토큰 수예요 (1~64000). 비우면 물려받아요.", budget),
        origin("recall_budget_tokens", () => { setDraft("recall_budget_tokens", 0); redraw(); })));
      if (edit) g.append(el("p", "pg-hint", "찾는 방법과 다시 고르기를 물려받은 값으로 되돌리려면 ‘모두 물려받기’를 써요."));
      return g;
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
      return g;
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
      return g;
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
      if (S.key && S.key !== scopeKey()) Object.assign(S, { loaded: false, stored: null, draft: null, eff: null, src: {}, job: "", sample: "" });
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
      out.push(extractGroup(kit), defenseGroup(kit), recallGroup(kit), reflectGroup(kit), deriveGroup(kit), toolsGroup(kit), templateGroup(kit, kit.spaceJob), ...extra);
      if (dirty()) {
        const bar = el("div", "pg-save");
        const ok = button("저장", "primary", null, save); ok.disabled = S.busy;
        bar.append(el("span", "", "저장하지 않은 변경이 있어요."), button("취소", "ghost", null, () => { S.draft = clone(S.stored); redraw(); }), ok);
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
