// Run with: node --test desktop/ui/*.test.js
// 설정 › 기억 공간 (space-settings.js): the PATCH/PUT bodies, the RE2-like rule check and the read-only agent view.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();
const h = dom.helpers(document);
require("./space-settings.js");
const S = window.ArkismSpaceSettings;

test("configPatch sends only changed keys and clears with the empty value of each type", () => {
  const stored = { extraction_mode: "verbose", defense_rules: [{ name: "a", pattern: "x+", action: "redact" }], mcp_tools: ["recall"], auto_derive: { enabled: true }, notes: "keep" };
  assert.deepEqual(S.configPatch(stored, structuredClone(stored)), {});
  assert.deepEqual(S.configPatch(stored, { ...stored, extraction_mode: "", defense_rules: [], mcp_tools: null, auto_derive: null }),
    { extraction_mode: "", defense_rules: [], mcp_tools: [], auto_derive: {} });
  assert.deepEqual(S.configPatch({}, { reflect_disposition: { skepticism: 4 } }), { reflect_disposition: { skepticism: 4 } });
  assert.deepEqual(S.configPatch({ reflect_disposition: { skepticism: 4 } }, { reflect_disposition: { skepticism: 0 } }), { reflect_disposition: {} }, "all zero = cleared");
  assert.ok(!("notes" in S.configPatch(stored, { ...stored, notes: "changed" })), "fields this screen does not edit never go out");
  assert.deepEqual(S.configPatch({ recall_budget_tokens: 3000, reflect_max_tokens: 512 }, { recall_budget_tokens: 0, reflect_max_tokens: 512, rerank: false, lanes: { graph: true } }),
    { recall_budget_tokens: 0, rerank: false, lanes: { graph: true } }, "0 hands a limit back; a switch off is a value, not a clear");
  const sw = { lanes: { fts: false, vector: false }, rerank: false, rerank_model: true };
  assert.deepEqual(S.configPatch(sw, { lanes: { fts: null, vector: false }, rerank: null, rerank_model: null }), { lanes: { fts: null, vector: false }, rerank: null, rerank_model: null },
    "one lane, rerank and rerank_model go back with null");
  assert.deepEqual(S.configPatch({ lanes: { fts: false } }, { lanes: { fts: null } }), { lanes: null }, "the last lane cleared clears them all");
});

test("resetBody keeps the name, reach, schedule, notes and the switch; everything else goes back to the layers below", () => {
  assert.deepEqual(S.resetBody({ display_name: "팀", default_reach: "project", extraction_enabled: false, consolidation_schedule: "daily", notes: "n", extraction_mode: "custom", mcp_tools: ["x"] }, 4),
    { expected_revision: 4, display_name: "팀", default_reach: "project", extraction_enabled: false, consolidation_schedule: "daily", notes: "n" });
});

test("compile refuses what the server's RE2 refuses, and patterns that match nothing or their own mark", () => {
  for (const [p, code] of [["", "empty"], ["a".repeat(513), "too_long"], ["(?=x)", "unsupported"], ["(?<!x)y", "unsupported"], ["(a)\\1", "unsupported"], ["(?U)a+", "unsupported"], ["(", "invalid"], ["a*", "matches_empty"], ["redacted", "matches_empty"]])
    assert.equal(S.compile(p).error, code, p);
  assert.ok(S.compile("(?i)secret-\\d+").re.test("SECRET-12"), "a leading flag group becomes a JS flag");
  assert.equal(S.compile("(?P<n>a)\\k<n>").error, "unsupported", "a named back reference is not RE2");
  assert.ok(S.compile("(?P<id>ID)-\\d+").re.test("ID-7"), "RE2's (?P<name>) group runs in the preview");
});

test("testRules redacts in order, stops at a block and skips unusable rules", () => {
  const rules = [{ name: "id", pattern: "ID-\\d{3}", action: "redact" }, { name: "bad", pattern: "(?=x)", action: "redact" }, { name: "stop", pattern: "secret", action: "block" }];
  assert.deepEqual(S.testRules(rules, "ID-123 and ID-456"), { text: "[redacted] and [redacted]", blocked: "", redacted: 2, errors: [[1, "unsupported"]] });
  assert.equal(S.testRules(rules, "a secret").blocked, "stop");
  assert.ok(S.TOOL.test("recall") && !S.TOOL.test("Recall") && !S.TOOL.test(""));
});

test("an agent token sees the server's values with their origin, no editor, no rules and no save bar", async () => {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: { review_decide: false } } };
  const calls = [];
  const request = async (path) => {
    calls.push(path);
    if (path.endsWith("/config")) throw new Error("access_denied");
    return { revision: 1, config: { extraction_mode: "verbatim", mcp_tools: ["recall"] }, sources: { extraction_mode: "template", mcp_tools: "space", defense_rules: "template" } };
  };
  const group = (title, hint) => { const g = h.el("section", "", title); if (hint) g.append(h.el("p", "", hint)); return g; };
  const line = (title, hint, ctl) => { const r = h.el("div", "", title); r.append(h.el("small", "", hint), ctl); return r; };
  const kit = { side() {}, sideSec: () => h.el("div"), kvRow: () => h.el("div"), sideNote: () => h.el("p"), group, line, sw: () => h.el("button", "", "sw"),
    more: (s, ...n) => { const d = h.el("details", "", s); d.append(...n); return d; }, spaceJob: async () => ({}) };
  const tab = S({ state, ...h, request, notice() {}, render() {} });
  tab.tab(kit); await dom.flush(); await dom.flush();
  const box = h.el("div"); box.append(...tab.tab(kit));
  const t = box.textContent;
  assert.deepEqual(calls, ["/v1/spaces/w/p/config", "/v1/spaces/w/p/config/effective"]);
  assert.match(t, /보기만 할 수 있어요/);
  assert.match(t, /원문 그대로/); assert.match(t, /템플릿에서 옴/);
  assert.match(t, /규칙은 사람 계정에게만 보여요/);
  assert.equal(box.querySelectorAll("textarea").length, 0);
  assert.equal(box.querySelectorAll("select").length, 0);
  assert.ok(!box.querySelectorAll("button").some((b) => /기본값으로|저장|규칙 추가/.test(b.textContent)));
  assert.equal(box.querySelectorAll("button").find((b) => b.textContent.includes("기본 템플릿 가져오기")).disabled, true);
});

test("a person turns on the server-model re-ranking with a budget note and hands a lane, rerank and rerank_model back one at a time", async () => {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: {} } };
  const sent = [];
  const stored = { lanes: { fts: false }, rerank: false };
  const request = async (path, o) => {
    if (o?.method) { sent.push([o.method, JSON.parse(o.body)]); return {}; }
    if (path.endsWith("/config")) return { revision: 2, config: structuredClone(stored) };
    return { revision: 2, config: { ...stored, rerank_model: false }, sources: { "lanes.fts": "space", rerank: "space", rerank_model: "server" } };
  };
  const group = (title) => h.el("section", "", title);
  const line = (title, hint, ctl) => { const r = h.el("div", "", title); r.append(h.el("small", "", hint), ctl); return r; };
  const sws = {};
  const kit = { side() {}, sideSec: () => h.el("div"), kvRow: () => h.el("div"), sideNote: () => h.el("p"), group, line, more: (s, ...n) => { const d = h.el("details", "", s); d.append(...n); return d; }, spaceJob: async () => ({}),
    sw: (on, label, set) => { const b = h.el("button", "", `sw:${label}:${on}`); sws[label] = set; return b; } };
  let box;
  const tab = S({ state, ...h, request, notice() {}, render() { box = h.el("div"); box.append(...tab.tab(kit)); } });
  tab.tab(kit); await dom.flush(); await dom.flush();
  assert.match(box.textContent, /sw:서버 모델로 다시 고르기:false/);
  assert.doesNotMatch(box.textContent, /모델 예산을 써요/);
  assert.doesNotMatch(box.textContent, /모두 물려받기’를 써요/);
  sws["서버 모델로 다시 고르기"](true);
  assert.match(box.textContent, /찾을 때마다 서버 모델을 불러 모델 예산을 써요/);
  // 기본값으로 next to 낱말로 찾기 and next to 검색 결과 다시 고르기
  const resets = () => box.querySelectorAll("button").filter((b) => b.textContent === "기본값으로");
  assert.equal(resets().length, 3, "lanes.fts, rerank and the rerank_model just set");
  for (const b of resets()) b.click();
  box.querySelectorAll("button").find((b) => b.textContent === "저장").click(); await dom.flush();
  assert.deepEqual(sent[0], ["PATCH", { expected_revision: 2, lanes: null, rerank: null }], "rerank_model was never saved, so it just drops out");
});

test("configPatch: on/off settings go back with null, whole blocks with {}, and an all-empty block counts as empty", () => {
  const stored = { extract_causal: true, work_state_candidates: false, recall_quality: { provisional: true }, reflect_default_options: { budget: "high", tools: {} }, extraction_strategies: { notes: { extraction_mode: "verbose" } } };
  assert.deepEqual(S.configPatch(stored, structuredClone(stored)), {});
  assert.deepEqual(S.configPatch(stored, { ...stored, extract_causal: null, work_state_candidates: null, recall_quality: null, reflect_default_options: { tools: {} }, extraction_strategies: null }),
    { extract_causal: null, work_state_candidates: null, recall_quality: {}, reflect_default_options: {}, extraction_strategies: {} });
  assert.deepEqual(S.configPatch({}, { recall_session_archives: false, entity_labels: [{ key: "team", values: [{ value: "a" }] }], memory_language: "ko", default_timezone: "Asia/Seoul" }),
    { recall_session_archives: false, entity_labels: [{ key: "team", values: [{ value: "a" }] }], memory_language: "ko", default_timezone: "Asia/Seoul" }, "false is a value");
  assert.deepEqual(S.configPatch({ audit: { enabled: false } }, { audit: null, entity_labels: [] }), { audit: {} });
});

test("the small helpers: draft overrides for the extraction test, label values, the consolidation rule JSON, renaming a strategy", () => {
  assert.equal(S.extractOverrides({ extraction_mode: "verbose" }, { extraction_mode: "verbose" }), undefined);
  assert.deepEqual(S.extractOverrides({ extraction_mode: "verbose" }, { extraction_mode: "chunks", extraction_chunk_size: 800, extract_causal: false, mcp_tools: ["x"] }),
    { extraction_mode: "chunks", extraction_chunk_size: 800, extract_causal: false });
  assert.deepEqual(S.labelValues("높음, 보통,, 높음", [{ value: "높음", description: "급함" }]), [{ value: "높음", description: "급함" }, { value: "보통" }]);
  assert.equal(S.rulesText({ strategies: [], batch_size: 2 }), "");
  const block = { strategies: [{ name: "bugs", scopes: [{ tags: ["bug*"] }] }] };
  assert.deepEqual(S.parseRules(S.rulesText(block)), block);
  assert.deepEqual(S.parseRules(""), {});
  for (const bad of ["{", "[]", '{"other": []}', '{"strategies": {}}']) assert.equal(S.parseRules(bad), null, bad);
  assert.deepEqual(Object.keys(S.renameKey({ a: 1, b: 2, c: 3 }, "b", "z")), ["a", "z", "c"]);
});

test("a person edits the new groups: a block starts from the resolved value, on/off goes back with null, and the tests call the preview routes", async () => {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: {} } };
  const sent = [];
  const stored = { extract_causal: true, extraction_mode: "verbose" };
  const request = async (path, o) => {
    if (o?.method) { sent.push([path, JSON.parse(o.body)]); return path.includes("/prompts/") ? { system: "SYS", user: "USER" } : path.includes("/extract/") ? { claims: [{ content: "금요일 배포 안 함", kind: "decision" }], skipped: [] } : {}; }
    if (path.endsWith("/config")) return { revision: 5, config: structuredClone(stored) };
    return { revision: 5, config: { ...stored, recall_quality: { conflict_pending: "exclude" }, memory_language: "auto" }, sources: { extract_causal: "space", recall_quality: "template", memory_language: "server" } };
  };
  const group = (title) => h.el("section", "", title);
  const line = (title, hint, ctl) => { const r = h.el("div", "", title); r.append(h.el("small", "", hint), ctl); return r; };
  const sws = {};
  const kit = { side() {}, sideSec: () => h.el("div"), kvRow: () => h.el("div"), sideNote: () => h.el("p"), group, line, more: (s, ...n) => { const d = h.el("details", "", s); d.append(...n); return d; }, spaceJob: async () => ({}),
    sw: (on, label, set) => { const b = h.el("button", "", `sw:${label}:${on}`); sws[label] = set; return b; } };
  let box;
  const tab = S({ state, ...h, request, notice() {}, render() { box = h.el("div"); box.append(...tab.tab(kit)); } });
  tab.tab(kit); await dom.flush(); await dom.flush();
  const t = box.textContent;
  for (const s of ["이름 정리", "활동 기록", "승인 전 후보도 보여 주기", "세션 원문에서도 찾기", "기억을 쓰는 언어", "기준 시간대", "이름 붙은 뽑기 방식", "관찰 정리 방식", "주제 요약 기본값", "찾는 방법별 무게", "작업 상태를 후보로"])
    assert.ok(t.includes(s), s);
  assert.match(t, /sw:원인과 결과도 후보로:true/);
  assert.match(t, /물려받기 · 말한 언어 그대로/);
  sws["승인 전 후보도 보여 주기"](true); // the template's block is kept whole
  sws["원인과 결과도 후보로"](false);
  sws["작업 상태를 후보로"](true);
  box.querySelectorAll("button").find((b) => b.textContent === "방식 추가").click();
  box.querySelectorAll("button").find((b) => b.textContent === "저장").click(); await dom.flush();
  assert.deepEqual(sent[0], ["/v1/spaces/w/p/config", { expected_revision: 5, extract_causal: false, work_state_candidates: true, extraction_strategies: { "style-1": {} }, recall_quality: { conflict_pending: "exclude", provisional: true } }]);
  // 뽑기 시험: the unsaved draft goes as overrides, prompt only by default (no model)
  sws["원인과 결과도 후보로"](false);
  const area = box.querySelectorAll("textarea").find((n) => n.getAttribute("aria-label") === "뽑기 시험할 글");
  area.value = "어제 회의에서 금요일 배포는 하지 않기로 했어요."; dom.fire(area, "input");
  box.querySelectorAll("button").find((b) => b.textContent === "시험해 보기").click(); await dom.flush(); await dom.flush();
  assert.deepEqual(sent.at(-1), ["/v1/extract/preview", { scope: { workspace_id: "w", project_id: "p" }, text: "어제 회의에서 금요일 배포는 하지 않기로 했어요.", prompt_only: true, overrides: { extract_causal: false } }]);
  assert.match(box.textContent, /후보 1개가 나와요/);
  box.querySelectorAll("button").find((b) => b.textContent === "프롬프트 보기").click(); await dom.flush(); await dom.flush();
  assert.deepEqual(sent.at(-1), ["/v1/prompts/preview", { scope: { workspace_id: "w", project_id: "p" }, operation: "retain" }]);
  assert.match(box.textContent, /SYS/);
  // 기본값으로 next to 원인과 결과도 후보로 hands it back with null
  const reset = box.querySelectorAll(".sp-origin").find((n) => n.previousSibling?.textContent.includes("원인과 결과도 후보로")).querySelector("button");
  reset.click();
  box.querySelectorAll("button").find((b) => b.textContent === "저장").click(); await dom.flush();
  assert.equal(sent.at(-1)[1].extract_causal, null);
});

test("recall defaults, capture mode and density and the consolidation decision model are reachable, with budget notes, and go back to inherit", async () => {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: {} } };
  const sent = [];
  const stored = { capture_mode: "assistant_results", decision_models: { consolidation: "clef" } };
  const request = async (path, o) => {
    if (o?.method) { sent.push([path, JSON.parse(o.body)]); return {}; }
    if (path.endsWith("/config")) return { revision: 2, config: structuredClone(stored) };
    return { revision: 2, config: { ...stored, capture_density: "durable", recall_quality: { conflict_pending: "attach" } }, sources: { capture_mode: "space", decision_models: "space" } };
  };
  const line = (title, hint, ctl) => { const r = h.el("div", "", title); r.append(h.el("small", "", hint), ctl); return r; };
  const sws = {};
  const kit = { side() {}, sideSec: () => h.el("div"), kvRow: () => h.el("div"), sideNote: () => h.el("p"), group: (t) => h.el("section", "", t), line,
    more: (s, ...n) => { const d = h.el("details", "", s); d.append(...n); return d; }, spaceJob: async () => ({}),
    sw: (on, label, set) => { const b = h.el("button", "", `sw:${label}:${on}`); sws[label] = set; return b; } };
  let box;
  const tab = S({ state, ...h, request, notice() {}, render() { box = h.el("div"); box.append(...tab.tab(kit)); } });
  tab.tab(kit); await dom.flush(); await dom.flush();
  for (const s of ["후보도 뜻으로 찾기", "다시 고를 때 관련 없는 것 빼기", "다시 고를 때 겹치는 것 합치기", "관찰을 먼저 보여 주기", "원문 조각도 같이 주기", "원문 조각 양",
    "대화에서 무엇을 뽑을지", "얼마나 촘촘히 뽑을지", "정리 전에 쉬운 것 먼저 거르기"])
    assert.ok(box.textContent.includes(s), s);
  assert.match(box.textContent, /판정할 때마다/, "a decision model on carries its budget note");
  sws["후보도 뜻으로 찾기"](true);
  assert.match(box.textContent, /임베딩을 만들어 모델 예산을 써요/);
  sws["다시 고를 때 겹치는 것 합치기"](true);
  sws["관찰을 먼저 보여 주기"](true);
  const sel = (label) => box.querySelectorAll("select").find((n) => n.getAttribute("aria-label") === label);
  const density = sel("얼마나 촘촘히 뽑을지"); density.value = "dense"; dom.fire(density, "change");
  assert.match(box.textContent, /뽑는 모델 사용량/);
  const dm = sel("정리 전에 쉬운 것 먼저 거르기"); dm.value = ""; dom.fire(dm, "change");
  const mode = sel("대화에서 무엇을 뽑을지"); mode.value = ""; dom.fire(mode, "change");
  const chunks = box.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === "원문 조각 양"); chunks.value = "800"; dom.fire(chunks, "change");
  box.querySelectorAll("button").find((b) => b.textContent === "저장").click(); await dom.flush();
  assert.deepEqual(sent[0], ["/v1/spaces/w/p/config", { expected_revision: 2, capture_mode: "", capture_density: "dense", decision_models: {},
    recall_quality: { conflict_pending: "attach", provisional_vectors: true, rerank_options: { dedup: true }, prefer_observations: true, chunks_max_tokens: 800 } }]);
});

test("recall budget, extraction sizes and document text: set in 자세히, saved as one block or a value, and handed back", async () => {
  const state = { view: "settings", settingsTab: "space", server: "s", scope: { workspace_id: "w", project_id: "p" }, caps: { actions: {} } };
  const sent = [];
  const stored = { extraction_batch_size: 20 };
  const request = async (path, o) => {
    if (o?.method) { sent.push([path, JSON.parse(o.body)]); return {}; }
    if (path.endsWith("/config")) return { revision: 3, config: structuredClone(stored) };
    return { revision: 3, config: { ...stored }, sources: { extraction_batch_size: "space" } };
  };
  const line = (title, hint, ctl) => { const r = h.el("div", "", title); r.append(h.el("small", "", hint), ctl); return r; };
  const sws = {};
  const kit = { side() {}, sideSec: () => h.el("div"), kvRow: () => h.el("div"), sideNote: () => h.el("p"), group: (t) => h.el("section", "", t), line,
    more: (s, ...n) => { const d = h.el("details", "", s); d.append(...n); return d; }, spaceJob: async () => ({}),
    sw: (on, label, set) => { const b = h.el("button", "", `sw:${label}:${on}`); sws[label] = set; return b; } };
  let box;
  const tab = S({ state, ...h, request, notice() {}, render() { box = h.el("div"); box.append(...tab.tab(kit)); } });
  tab.tab(kit); await dom.flush(); await dom.flush();
  assert.match(box.textContent, /sw:문서 글 보관:true/, "document text is kept by default");
  const sel = (label) => box.querySelectorAll("select").find((n) => n.getAttribute("aria-label") === label);
  const num = (label) => box.querySelectorAll("input").find((n) => n.getAttribute("aria-label") === label);
  const fn = sel("읽는 양 정하는 법"); fn.value = "adaptive"; dom.fire(fn, "change");
  const low = num("적게 비율"); low.value = "0.05"; dom.fire(low, "change");
  const max = num("최대 기억 수"); max.value = "30"; dom.fire(max, "change");
  const imgs = num("조각마다 그림 수"); imgs.value = "99"; dom.fire(imgs, "change");
  assert.equal(num("조각마다 그림 수").value, "", "out of range is refused");
  const imgs2 = num("조각마다 그림 수"); imgs2.value = "4"; dom.fire(imgs2, "change");
  sws["문서 글 보관"](false);
  box.querySelectorAll("button").find((b) => b.textContent === "기본값으로" && b.parentNode.previousSibling?.textContent.startsWith("한 번에 보내는 조각 수")).click();
  box.querySelectorAll("button").find((b) => b.textContent === "저장").click(); await dom.flush();
  assert.deepEqual(sent[0], ["/v1/spaces/w/p/config", { expected_revision: 3, recall_budget: { function: "adaptive", adaptive_low: 0.05, max: 30 },
    extraction_batch_size: 0, extraction_images_per_chunk: 4, store_document_text: false }]);
  assert.deepEqual(S.configPatch({ recall_budget: { function: "fixed" }, store_document_text: false }, { recall_budget: null, store_document_text: null }),
    { recall_budget: {}, store_document_text: null });
});
