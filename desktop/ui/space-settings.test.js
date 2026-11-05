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
