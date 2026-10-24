// Workflow building blocks with no screen: node vocabulary, graph model, cron and run commands.
const test = require("node:test");
const assert = require("node:assert/strict");

const load = async () => {
  const { createCrewVocabulary } = await import("./modules/crewVocabulary.mjs");
  const { createCrewModel } = await import("./modules/crewModel.mjs");
  const { createCrewSchedule } = await import("./modules/crewSchedule.mjs");
  const { createCrewExecution } = await import("./modules/crewExecution.mjs");
  const voc = createCrewVocabulary({});
  const recs = [{ id: "tpl1", crew: { type: "template", name: "틀", instructions: "지시", effort: "high", model: "", skills: ["git-review", "loose"] } },
    { id: "ag", crew: { type: "agent", model: "haiku" } }, { id: "bare", crew: { type: "template" } }];
  const state = { scope: { project_id: "p" }, data: { skills: [{ id: "sk-1", name: "git-review" }] } };
  const model = createCrewModel({ byId: (id) => recs.find((r) => r.id === id), IS_AI: voc.IS_AI, NODES: voc.NODES, PORTS: voc.PORTS, state });
  return { voc, model, state, createCrewSchedule, createCrewExecution };
};

test("vocabulary names the CLI for each model family and the colour of each node kind", async () => {
  const { voc } = await load();
  const cases = { auto: "Arkism 라우터", "gpt-5": "Codex CLI", o3: "Codex CLI", codex: "Codex CLI", "Opus 5": "Claude Code CLI", sonnet: "Claude Code CLI", "gemini-2.5": "Gemini CLI",
    antigravity: "Gemini CLI", "glm-4": "claude-glm", "deepseek-v4": "claude-ds", "grok-4": "Grok CLI", "": "호스트 CLI", qwen: "호스트 CLI" };
  for (const [m, cli] of Object.entries(cases)) assert.equal(voc.cliOf(m), cli, m);
  assert.equal(voc.cliOf(undefined), "호스트 CLI");
  assert.deepEqual(["ai", "team", "http", "n8n", "approval", "code"].map(voc.KIND_OF), ["ai", "ai", "http", "http", "human", "plain"]);
  assert.ok(voc.IS_AI("expert") && voc.IS_AI("ai_once") && !voc.IS_AI("code"));
  // n8n is gone from the 노드 추가 picker, but a saved n8n step still opens with its fields.
  assert.equal(voc.NODES.n8n.hidden, true);
  assert.deepEqual(voc.FIELDS.n8n.map(([k]) => k), ["url", "mode", "timeout"]);
  assert.equal(voc.TABS.length, 3);
  assert.equal(voc.EFFORTS[0][0], "low");
  assert.equal(voc.MODES.round_robin, "정해진 순서대로");
  assert.equal(voc.HUMAN_KIND.AI, "AI 노드");
  assert.deepEqual(voc.FIELDS.start, []);
});

test("graph model orders steps, finds cycles, ports and the one-step form", async () => {
  const { model } = await load();
  const g = { nodes: [{ id: "s", type: "start" }, { id: "a", type: "ai", config: { access: "write" } }, { id: "b", type: "ai", config: { access: "write", model: "m" } }, { id: "c", type: "condition" }, { id: "e", type: "end" }],
    edges: [{ from: "s", to: "a" }, { from: "s", to: "b" }, { from: "a", to: "c" }, { from: "b", to: "c" }, { from: "c", to: "e", port: "yes" }, { from: "c", to: "a", port: "no", max: 2 }, { from: "a", to: "e", port: "ok" }] };
  assert.deepEqual(model.levels(g).map((l) => l.map((n) => n.id)), [["s"], ["a", "b"], ["c"], ["e"]]);
  assert.deepEqual(model.levels(), []);
  assert.equal(model.wouldCycle(g, "c", "s"), true);
  assert.equal(model.wouldCycle(g, "a", "a"), true);
  assert.equal(model.wouldCycle(g, "s", "e"), false);
  assert.deepEqual(model.portsOf(g.nodes[3], g), [["yes", "예"], ["no", "아니오"]]);
  assert.deepEqual(model.portsOf(g.nodes[4], g), []);
  assert.deepEqual(model.portsOf(g.nodes[1], g), [["ok", "ok"], ["", "다른 갈래"]]);
  assert.deepEqual(model.portsOf(g.nodes[2], g), [["", ""]]);
  assert.deepEqual(model.portsOf(g.nodes[0], g), [["", ""]]);
  assert.equal(model.stepCount(g), 3);
  assert.equal(model.stepCount(null), 0);
  assert.equal(model.aiNodes(g).length, 2);
  assert.deepEqual(model.aiNodes(undefined), []);
  assert.equal(model.nodeTitle({ type: "wait" }), "기다리기");
  assert.equal(model.nodeTitle({ type: "odd" }), "odd");
  assert.equal(model.isBack({ max: 1 }), true);
  assert.equal(model.fwd(g).length, 6);
  assert.deepEqual(model.fwd(undefined), []);
  assert.deepEqual(model.upstream(g, "c").map((n) => n.id).sort(), ["a", "b", "s"]);
  assert.equal(model.simpleNode(g), null);
  const one = { nodes: [{ id: "s", type: "start" }, { id: "x", type: "ai_once" }, { id: "e", type: "end" }], edges: [] };
  assert.equal(model.simpleNode(one).id, "x");
  assert.equal(model.simpleNode({ ...one, edges: [{ from: "x", to: "x", max: 1 }] }), null);
  assert.equal(model.simpleNode(undefined), null);
  assert.equal(model.modelOf({ config: { model: "m" } }), "m");
  assert.equal(model.modelOf({ type: "expert", config: { expert: "ag" } }), "haiku");
  assert.equal(model.modelOf({ type: "ai" }, { model: "base" }), "base");
  assert.equal(model.modelOf({ type: "ai" }), "기본 모델");
  assert.ok(model.WORK({ type: "code" }) && !model.WORK({ type: "webhook" }));
});

test("default graphs: template, one AI step, or empty", async () => {
  const { model } = await load();
  const tpl = model.defaultGraph("tpl:tpl1", { model: "fallback" });
  assert.deepEqual(tpl.nodes[1].config, { model: "fallback", goal: "{{input}}", access: "read", instructions: "지시", effort: "high", skills: "sk-1,loose" });
  assert.equal(tpl.nodes[1].title, "틀");
  const bare = model.defaultGraph("tpl:bare", {});
  assert.deepEqual(bare.nodes[1].config, { model: "", goal: "{{input}}", access: "read" });
  assert.equal(bare.nodes[1].title, "AI 에이전트");
  assert.equal(model.defaultGraph("tpl:gone", {}).nodes.length, 2);
  const simple = model.defaultGraph("simple", { name: "요약", model: "m" });
  assert.equal(simple.nodes[1].title, "요약");
  assert.equal(model.defaultGraph("simple", {}).nodes[1].title, "AI 에이전트");
  assert.deepEqual(model.defaultGraph("blank", {}).edges, [{ from: "start", to: "end" }]);
});

test("rule checks report orphans, loops, self calls, missing models and risky steps", async () => {
  const { model } = await load();
  const g = { nodes: [{ id: "s", type: "start" }, { id: "a", type: "ai", config: { access: "write" } }, { id: "b", type: "ai", config: { access: "write" } }, { id: "sub", type: "subflow", config: { workflow: "$me" } },
    { id: "gh", type: "github" }, { id: "h", type: "http", config: { method: "put" } }, { id: "lost", type: "code" }, { id: "s2", type: "start" }],
  edges: [{ from: "s", to: "a" }, { from: "s", to: "b" }, { from: "a", to: "sub" }, { from: "sub", to: "gh" }, { from: "gh", to: "h" }, { from: "h", to: "a", max: 3 }, { from: "lost", to: "lost" }] };
  const out = model.ruleChecks({ handle: "me" }, g);
  const by = Object.fromEntries(out.map((r) => [r.label, r]));
  assert.equal(by["시작→끝 도달"].ok, false);
  assert.match(by["시작→끝 도달"].detail, /닿지 않는 노드: 코드/);
  assert.match(by["되돌아가기 제한"].detail, /최대 3번/);
  assert.equal(by["고리 없음"].ok, false);
  assert.match(by["AI 노드 모델"].detail, /모델이 비었어요/);
  assert.match(by["되돌릴 수 없는 동작"].detail, /GitHub, HTTP 요청/);
  assert.equal(by["쓰기 노드"].ok, true);
  const two = model.ruleChecks({ model: "m", graph: { nodes: [{ id: "x", type: "start" }, { id: "y", type: "start" }], edges: [] } });
  assert.match(two[0].detail, /시작이 2개예요/);
  assert.equal(two[1].detail, "되돌아가는 선이 없어요");
  assert.match(two[3].detail, /0개 모두 정해졌어요/);
  const fine = model.ruleChecks({ graph: { nodes: [{ id: "s", type: "start" }, { id: "ok", type: "approval" }, { id: "n", type: "notify" }], edges: [{ from: "s", to: "ok" }, { from: "ok", to: "n" }] } });
  assert.equal(fine[0].detail, "끝 1곳");
  assert.equal(fine.some((r) => r.label === "되돌릴 수 없는 동작"), false);
  assert.equal(model.ruleChecks({ graph: null }).length, 4);
});

test("cron parsing, next run and plain words", async () => {
  const { createCrewSchedule } = await load();
  const s = createCrewSchedule({});
  assert.equal(s.parseCron("0 9 * * 1-5").f[4], "1-5");
  for (const bad of ["", "0 9 * *", "x 9 * * *", "*/0 * * * *", "0 9 * * a-b"]) assert.equal(s.parseCron(bad), null, bad);
  assert.equal(s.parseCron(undefined), null);
  assert.ok(s.parseCron("*/15 0-23/2 1,15 * 7").p[4].has(0));
  assert.ok(s.parseCron("5/10 * * * *").p[0].has(55));
  const from = new Date(2026, 9, 23, 8, 30); // a Friday
  assert.equal(s.nextRun("0 9 * * *", from).getHours(), 9);
  assert.equal(s.nextRun("0 9 * * 1", from).getDay(), 1);
  assert.equal(s.nextRun("0 9 25 * *", from).getDate(), 25);
  assert.equal(s.nextRun("0 9 25 * 1", from).getDate(), 25); // day of month or weekday, whichever comes first
  assert.equal(s.nextRun("0 0 31 2 *", from), null);
  assert.equal(s.nextRun("bad"), null);
  assert.ok(s.nextRun("* * * * *") instanceof Date);
  const words = { "0 9 * * *": "매일 09:00", "30 18 * * 1-5": "평일 18:30", "0 7 * * 0,6": "일·토 07:00", "0 7 * * 7": "일 07:00", "0 8 1 * *": "매달 1일 08:00",
    "*/5 * * * *": "*/5 * * * *", "0 9 * 1 *": "0 9 * 1 *", "0 9 1 * 1": "0 9 1 * 1", nonsense: "nonsense", "": "—" };
  for (const [cron, text] of Object.entries(words)) assert.equal(s.cronText(cron), text, cron);
  assert.equal(s.triggerText({ trigger: { kind: "schedule", cron: "0 9 * * *" } }), "일정 · 매일 09:00");
  assert.equal(s.triggerText({ trigger: { kind: "webhook", label: "깃허브" } }), "웹훅 · 깃허브");
  assert.equal(s.triggerText({ trigger: { kind: "webhook" } }), "웹훅");
  assert.equal(s.triggerText({}), "");
  assert.equal(s.triggered({ trigger: { kind: "webhook" }, enabled: true }), true);
  assert.equal(s.triggered({ trigger: { kind: "webhook" } }), false);
  assert.equal(s.when(null), "—");
  assert.match(s.when(from), /10월/);
  globalThis.ArkismI18n = { locale: () => "en-US" };
  try { assert.match(s.when(from), /October/); } finally { delete globalThis.ArkismI18n; }
});

test("run commands: start, resume from a past run, stop at a step, approve and stop", async () => {
  const { model, createCrewExecution } = await load();
  const calls = [], notes = [];
  let fail = null;
  const request = async (url, o) => { calls.push({ url, body: JSON.parse(o.body) }); if (fail && fail.test(url)) { const m = fail.message; fail = fail.once ? null : fail; throw new Error(m); } return { id: "r", url }; };
  const ex = createCrewExecution({ notice: (t) => notes.push(t), request, runInput: (text, only) => JSON.stringify({ text, only: !!only }), state: { scope: { p: 1 } }, stepCount: model.stepCount, upstream: model.upstream });
  const g = { nodes: [{ id: "s", type: "start" }, { id: "a", type: "ai" }, { id: "b", type: "code" }, { id: "e", type: "end" }], edges: [{ from: "s", to: "a" }, { from: "a", to: "b" }, { from: "b", to: "e" }] };
  const rec = { id: "w", revision: 2, crew: { graph: g } };
  assert.equal(await ex.startRun({ crew: { graph: { nodes: [{ id: "s", type: "start" }] } } }, "x"), null);
  assert.match(notes.pop(), /아직 할 일이 없어요/);
  await ex.startRun(rec, "해 줘");
  assert.deepEqual(calls.at(-1).body.run.nodes.a, { status: "pending" });
  assert.match(notes.pop(), /실행을 맡겼어요/);
  await ex.startRun(rec, "{}", "a");
  assert.equal(calls.at(-1).body.run.only, "a");
  assert.match(notes.pop(), /이 노드만/);
  await ex.startRun(rec, "", null, { nodes: { a: { status: "done", output: "o", port: "", runs: 1 }, b: { status: "failed" }, gone: { status: "done" } }, loops: { "b>a": 1 } });
  assert.equal(calls.at(-1).body.run.nodes.a.status, "done");
  assert.deepEqual(calls.at(-1).body.run.loops, { "b>a": 1 });
  await ex.startRun(rec, "", null, null, "a");
  assert.deepEqual(Object.fromEntries(Object.entries(calls.at(-1).body.run.nodes).map(([k, v]) => [k, v.status])), { s: "pending", a: "pending", b: "skipped", e: "skipped" });
  assert.match(notes.pop(), /여기까지/);
  // A resume that the server refuses starts over from nothing.
  fail = { test: (u) => u === "/v1/crew/runs", message: "revision_conflict", once: true };
  const again = await ex.startRun(rec, "", null, { nodes: {} });
  assert.equal(again.id, "r");
  fail = { test: () => true, message: "offline" };
  assert.equal(await ex.startRun(rec, ""), null);
  assert.equal(notes.pop(), "실행하지 못했어요 (offline).");
  const run = { id: "r 1", revision: 4, crew_run: { status: "waiting", waiting: { node_id: "a" } } };
  fail = null;
  await ex.decide(run, true);
  assert.equal(calls.at(-1).url, "/v1/crew/runs/r%201/decision");
  assert.equal(notes.pop(), "승인했어요. 다음 단계로 넘어가요.");
  await ex.decide(run, false);
  assert.match(notes.pop(), /거절했어요/);
  fail = { test: () => true, message: "revision_conflict" };
  await ex.decide(run, true);
  assert.match(notes.pop(), /실행 상태가 바뀌었어요/);
  fail = { test: () => true, message: "down" };
  await ex.decide(run, true);
  assert.equal(notes.pop(), "보내지 못했어요 (down).");
  await ex.stopRun(run);
  assert.equal(notes.pop(), "멈추지 못했어요 (down).");
  fail = null;
  await ex.stopRun(run);
  assert.equal(calls.at(-1).body.run.status, "stopped");
  assert.match(notes.pop(), /멈췄어요/);
  assert.equal(ex.RUN_LABEL.waiting, "확인 대기");
  assert.equal(ex.NODE_LABEL.skipped, "건너뜀");
});
