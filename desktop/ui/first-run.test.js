// The very first start on a new PC: the 이 PC / SSH chooser covers the sidebar, the storage folder waits under 자세히,
// and the empty 홈 is a start checklist (agents · extraction model · talk · import) instead of "nothing to check".
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot } = require("./testdata/app-harness.js");

const routes = [];
const ok = (body) => ({ status: 200, body: JSON.stringify(body) });
const empty = () => ok({ items: [], next_cursor: "", total: 0, authority: "data_only_untrusted" });
let app, t;
test.before(async () => {
  app = await boot({
    stored: { "arkism.mode": "choose" },
    invoke: {
      agents_setup: { ok: true, at: "2026-10-30T00:00:00Z", summary: { installed: true, backup: "/home/me/.arkism/backup/20261030", files: ["/home/me/.claude/settings.json", "/home/me/.codex/config.toml"] } },
      import_agent_memories: (a) => (a.dryRun ? { statements: 3, files: 1 } : { candidates: 3 }),
      autostart_set: null,
      local_location_set: () => ({ path: "/home/me/.local/share/arkism/runtime", next: "/home/me/.local/share/arkism/runtime", default: "/home/me/.local/share/arkism/runtime", default_has_data: false })
    },
    beforeEntry(env) {
      env.window.apiOverride = (a) => { for (const [m, p, fn] of routes) if (a.method === m && a.path.startsWith(p)) return fn(a); return null; };
    }
  });
  t = app.dom.text;
  await app.settle(500);
});
test.after(() => app?.stop());

// a brand-new install: nothing saved yet, Claude Code found, the extraction model waits for a sign-in
routes.push(["GET", "/v1/memories", empty], ["GET", "/v1/memory_candidates", empty], ["POST", "/v1/memory_candidates/query", empty],
  ["GET", "/v1/agent_tools", () => ok({ agent_tools: { collected_at: new Date().toISOString(), servers: [], plugins: [], requests: [],
    agents: [{ id: "claude_code", name: "Claude Code", found: true, enabled: true }, { id: "codex", name: "Codex", found: false, enabled: true }],
    extractor: { model: "haiku", cli: "claude", state: "login_needed" } } })]);

test("first run: the chooser covers the sidebar and the storage folder waits under 자세히", async () => {
  assert.equal(app.$("#unlock").hidden, false);
  assert.ok(app.$("#unlock").classList.contains("first-run"), "no menus to show yet");
  assert.equal(app.$("#start-choice").hidden, false);
  app.$("#choose-local").click(); await app.settle(100);
  const body = app.$("#start-mode-body");
  const more = body.querySelector("details.storage-more");
  assert.ok(more, "자세히 (저장 위치)");
  assert.equal(more.open, false, "the default folder needs no decision");
  assert.ok(body.firstElementChild.querySelector("button"), "시작 comes first");
  // the SSH card shows the right words about the server
  assert.match(t(app.$("#ssh-hint")), /서버에 Arkism이 설치돼 있어야 해요/);
  assert.doesNotMatch(t(app.$("#ssh-hint")), /따로 켜거나/);
});

test("first run: 시작 opens an empty 홈 with the start checklist", async () => {
  const start = [...app.$$("#start-mode-body button")].find((b) => t(b) === "시작");
  start.click(); await app.settle(2500);
  for (let i = 0; i < 20 && !/시작하기/.test(t(app.$("#table"))); i++) await app.settle(500); // the first load lands a few rounds later
  assert.equal(app.$("#unlock").hidden, true);
  assert.ok(!app.$("#unlock").classList.contains("first-run"));
  const home = t(app.$("#table"));
  assert.match(home, /시작하기/);
  assert.match(home, /Claude Code · Codex에 Arkism을 이었어요/, "what install-arkism changed");
  assert.match(home, /\/home\/me\/\.arkism\/backup\/20261030/, "where the old files went");
  assert.match(home, /다시 시작하세요/);
  assert.match(home, /기억을 뽑으려면 로그인이 필요해요/);
  assert.match(home, /평소처럼 대화하기/);
  assert.match(home, /기억 3개를 찾았어요/, "the import dry run count");
  assert.match(home, /트레이에서 자동으로 켜져요/);
  // 끄기 turns autostart off and says so
  const off = [...app.$$("#table .start-autostart button")].find((b) => t(b) === "끄기");
  off.click(); await app.settle(100);
  assert.ok(app.calls.some(([c, a]) => c === "autostart_set" && a.on === false));
  assert.match(t(app.$("#table")), /켜지지 않아요/);
});
