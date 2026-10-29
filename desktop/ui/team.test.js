// Behavior tests for the team pages (team.js): teams, the team window, meetings, Discord and usage.
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot, dom, iso } = require("./testdata/crew-harness.cjs");

const skipClose = (n) => n.matches(".crew-popup-close") || /^(취소|닫기)$/.test(n.textContent.trim());
const sweep = (h, root, rounds = 3) => dom.crawl(root, { settle: () => h.settle(), rounds, skip: skipClose });
const clean = () => dom.errors.splice(0).map((x) => String(x.stack || x).split("\n").slice(0, 2).join(" | "));
const table = () => document.querySelector("#table");

const team2 = () => ({ id: "tm2", revision: 3, updated_at: iso(-5), crew: { type: "team", name: "디자인 팀", handle: "design", role: "시안 검토", category: "디자인",
  members: [{ agent_id: "ag1", order: 1, role: "moderator", focus: "흐름" }, { agent_id: "ag2", order: 2, role: "worker", model_override: "haiku" }, { agent_id: "gone", order: 3, role: "reviewer", name: "옛 멤버" }],
  mode: "moderator", ending: { max_rounds: 3, token_cap: 50000, end_on_all_approved: true, end_on_moderator: false },
  context: [{ collection: "memories", id: "m1" }], templates: [{ key: "t1", name: "주간 검토", prompt: "지난주 시안", overrides: { mode: "round_robin", ending: { max_rounds: 2 } } }], discord_mirror: true,
  avatar: { art: "002", bg: "teal" } } });

const meeting = (id, status, extra = {}) => ({ id, revision: 2, created_at: iso(-60), updated_at: iso(-30), crew_meeting: {
  team_id: "tm2", title: `회의 ${id}`, topic: "다크 테마 검토", status, round: 2, max_rounds: 3, tokens_used: 1200, token_cap: 50000, cost_usd: 0.4, cost_estimate_usd: 0.9,
  trigger: "discord", finished_at: status === "concluded" ? iso(-10) : undefined,
  team_snapshot: { team: { name: "디자인 팀", members: [{ agent_id: "ag1", order: 1 }, { agent_id: "ag2", order: 2 }] }, agents: { ag1: { name: "리뷰어", model: "sonnet" }, ag2: { name: "기획자", model_mode: "auto" } } },
  turns: [{ kind: "brief", text: "브리프" }, { kind: "turn", agent_id: "ag1", speaker: "리뷰어", round: 1, stance: "agree", text: "좋아요" }, { kind: "turn", agent_id: "ag2", round: 1, ok: false, error: "시간 초과" },
    { kind: "human", speaker: "discord", text: "의견" }, { kind: "human", text: "내 의견" }, { kind: "turn", agent_id: "ag2", round: 2, model: "gpt-5", stance: "odd", text: "음" }, { kind: "conclusion", text: "끝" }],
  speaking: "ag2", notes_pending: [{ by: "human", text: "대기 의견" }], waiting: status === "waiting" ? { reason: "예산 한도" } : undefined,
  conclusion: status === "concluded" ? { summary: "정리", decisions: ["밝게"], todos: [{ text: "고치기" }, { x: 1 }], open_questions: [] } : undefined,
  saved: status === "concluded" ? { candidate_ids: ["c1", "c2"] } : undefined, ended_reason: status === "concluded" ? "all_approved" : status === "failed" ? "weird" : undefined,
  discord: { thread_url: "https://discord.com/t/1" }, budget_approved: { at: iso(-20) }, ...extra } });

const discordData = () => ({
  settings: { id: "ds", revision: 1, discord_settings: { bot_mode: "shared", reply_to: "channel", daily_usd: 3, guild_id: "g1", guild_name: "우리 서버" } },
  stats: { bots: 3, open_threads: 1, calls_week: 9, by_target: [{ label: "디자인", calls: 4 }, { action: { kind: "workflow", id: "wf1" }, count: 2 }] },
  bots: [
    { id: "b1", revision: 1, discord_bot: { name: "Arkism-bot", state: "connected", guild_id: "g1", token_last4: "abcd", last_seen_at: iso(-2), application_id: "123456789",
      channels: [{ channel_id: "c1", name: "general", action: { kind: "meeting", id: "tm2", template_key: "t1" }, auto_start: true }, { channel_id: "c2", name: "release", label: "배포", action: { kind: "workflow", id: "wf1" } }, { channel_id: "c9", action: { kind: "agent", id: "ag1" } }],
      allowed: { user_ids: ["u1"], role_ids: [] } } },
    { id: "b2", revision: 1, discord_bot: { name: "Perm", state: "needs_permission", missing: [{ channel_id: "c1", perms: ["Send Messages"] }], channels: [{ channel_id: "c1", name: "general", action: { kind: "meeting", id: "tm2" } }], application_id: "987654321" } },
    { id: "b3", revision: 1, discord_bot: { name: "Bad", state: "token_invalid", channels: [] } },
    { id: "b4", revision: 1, discord_bot: { name: "Odd", state: "strange" } }
  ]
});

const usageDoc = () => ({
  kpi: { cost_usd: 120, waiting: 2, oldest_waiting_at: iso(-90) },
  by_day: [{ date: "2026-10-20", agent: 2, team: 1, workflow: 3, failed: 2, agent_failed: 1, team_failed: 0 }, { date: "2026-10-21", ok: 4, failed: 1 }, { date: "2026-10-22", agent: 0 }],
  agents: [{ id: "ag1", name: "reviewer", calls: 10, failed: 2, tokens: 50000, cost_usd: 1.2, direct: 6 }, { id: "zz", name: "ghost", calls: 3, tokens: 10, cost_usd: 0 }, { id: "ag2", calls: 0 }],
  teams: [{ id: "tm2", name: "design", meetings: 4, concluded_rate: 75, avg_rounds: 2.5, cost_usd: 0.8 }, { id: "tm1", name: "release", meetings: 1, concluded_rate: 100 }],
  workflows: [{ id: "wf1", name: "flow", runs: 5, success_rate: 60, cost_usd: 0.3 }, { id: "wf2", runs: 1, success_rate: 100 }],
  failing_nodes: [{ workflow_id: "wf1", node_id: "code", failures: 3 }, { workflow_id: "wfx", node_id: "n", failures: 1 }]
});

async function bootTeams(view, opts = {}) {
  const h = await boot({ view, meetings: [meeting("mt1", "running"), meeting("mt2", "waiting"), meeting("mt3", "concluded"), meeting("mt4", "pending", { turns: [] }), meeting("mt5", "failed")], discord: discordData(), ...opts });
  h.data.crew.push(team2());
  h.routes.unshift(["GET", /^\/v1\/crew\/usage/, () => usageDoc()], ["GET", /^\/v1\/crew\/teams\/[^/]+\/stats/, () => ({ meetings_done: 4, tokens: 12000, cost_usd: 0.7 })],
    ["POST", /^\/v1\/crew\/teams\/estimate/, () => ({ tokens: 9000, cost_usd: 0.2 })],
    ["GET", /^\/v1\/crew\/meetings\/([^/?]+)/, (m) => h.data.meetings.find((x) => x.id === m[1]) || h.data.meetings[0]]);
  h.render(); await h.settle();
  return h;
}

test("teams page with cards, Discord badges and the team window tabs", async () => {
  const h = await bootTeams("teams");
  assert.match(table().textContent, /디자인 팀/);
  await sweep(h, document.body);
  for (const id of ["tm2", "tm1"]) {
    h.closeAll();
    window.dispatchEvent(new CustomEvent("arkism:open-team", { detail: { id } }));
    await h.settle();
    const d = h.top();
    assert.ok(d, "team window opens");
    // a saved team can open as its own window (popup-drag.js), titled with its name like the expert and automation windows
    assert.equal(JSON.parse(d.dataset.popout).event, "arkism:open-team");
    assert.equal(d.dataset.popoutTitle, h.data.crew.find((r) => r.id === id).crew.name);
    for (const tab of [...d.querySelectorAll(".crew-popup-tab")]) { tab.click(); await h.settle(); await sweep(h, d, 2); }
    for (const x of h.dialogs()) await sweep(h, x, 1);
  }
  assert.deepEqual(clean(), []);
});

test("new team window: AI draft proposals apply and undo, then save", async () => {
  const h = await bootTeams("teamNew");
  await h.settle();
  const d = h.top();
  assert.ok(d, "new team window opens");
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ name: "검토 팀", role: "시안을 봐요", mode: "moderator", max_rounds: 20, token_cap: 30000, members: [{ agent_id: "ag1", role: "worker", focus: "흐름" }, { agent_id: "ag2", role: "nope" }, { name: "없는 사람" }] }) });
  const chat = h.chats.at(-1);
  const r = await chat.ask("디자인 팀");
  assert.ok(r.props.length >= 5);
  assert.match(r.say, /없는 전문가/);
  for (const p of r.props) { p.apply(); p.undo?.(); p.apply(); }
  h.assistReply = () => ({ status: "done", output: "{}" });
  assert.match((await chat.ask("그대로")).say, /바꿀 곳/);
  h.assistReply = () => ({ status: "done", output: "[1]" });
  await assert.rejects(chat.ask("배열"));
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat.ask("실패"));
  await h.settle();
  await sweep(h, d, 2);
  assert.deepEqual(clean(), []);
});

test("meetings: list tabs, waiting decision, notes, stop and a new meeting", async () => {
  const h = await bootTeams("meetings");
  await h.settle();
  assert.match(table().textContent, /회의 mt1/);
  for (const id of ["mt2", "mt3", "mt4", "mt5", "mt1"]) {
    const row = [...document.querySelectorAll(".tm-meet-row")].find((r) => r.textContent.includes(`회의 ${id}`));
    row.click(); await h.settle();
  }
  // note box on a live meeting
  const note = document.querySelector(".tm-note-input");
  if (note) { dom.type(note, ""); note.closest("form").requestSubmit(); dom.type(document.querySelector(".tm-note-input"), "다음엔 짧게"); document.querySelector(".tm-note-row").requestSubmit(); await h.settle(); }
  assert.ok(h.notices.some((n) => /반영할게요/.test(n)));
  // waiting meeting: continue and stop
  [...document.querySelectorAll(".tm-meet-row")].find((r) => r.textContent.includes("회의 mt2")).click(); await h.settle();
  await h.click(table(), "계속");
  [...document.querySelectorAll(".tm-meet-row")].find((r) => r.textContent.includes("회의 mt2")).click(); await h.settle();
  await h.click(table(), "멈춤");
  const stop = h.btn(document, "회의 멈춤");
  if (stop) { h.answer = false; stop.click(); await h.settle(); h.answer = true; h.btn(document, "회의 멈춤").click(); await h.settle(); }
  for (const t of document.querySelectorAll(".tm-seg-tab")) { t.click(); await h.settle(); }
  // new meeting form: needs a title, then starts
  await h.click(document, "새 회의");
  const side = document.querySelector("#detail");
  await h.click(side, "시작");
  assert.ok(h.needs.length);
  dom.type([...side.querySelectorAll("input")].find((i) => i.placeholder.includes("다크 테마")), "새 회의 제목");
  const tpl = [...side.querySelectorAll("select")].at(-1);
  tpl.value = "t1"; dom.fire(tpl, "change");
  await h.click(side, "시작");
  assert.ok(h.notices.some((n) => /회의를 열었어요/.test(n)));
  await sweep(h, document.body, 2);
  assert.deepEqual(clean(), []);
});

test("meetings: polling while live and failed loads", async () => {
  const h = await bootTeams("meetings");
  const gets = () => h.calls.filter((c) => /meetings\/mt1/.test(c.url)).length;
  const n = gets();
  await h.settle(2100); await h.settle(2100);
  assert.ok(gets() > n, "the live meeting is polled");
  h.routes.unshift(["GET", /^\/v1\/crew\/meetings/, () => new Error("HTTP 404")]);
  for (const t of document.querySelectorAll(".tm-seg-tab")) { t.click(); await h.settle(); }
  assert.match(table().textContent, /지원하지 않아요/);
  await bootTeams("meetings", { meetings: [], routes: [["GET", /^\/v1\/crew\/meetings/, () => new Error("boom")]] });
  assert.match(table().textContent, /회의를 불러오지 못했어요 \(boom\)/);
  assert.deepEqual(clean(), []);
});

test("Discord: bots, bot side, settings and connect steps", async () => {
  const h = await bootTeams("discord");
  await h.settle();
  assert.match(table().textContent, /Arkism-bot/);
  assert.match(table().textContent, /토큰을 다시 넣어/);
  await sweep(h, document.body, 3);
  // open each bot's side panel and work its controls
  for (const name of ["Arkism-bot", "Perm", "Odd"]) {
    const card = [...document.querySelectorAll(".tm-bot")].find((c) => c.textContent.includes(name));
    if (!card) continue;
    dom.key(card, "Enter"); await h.settle();
    await sweep(h, document.querySelector("#detail"), 2);
  }
  // settings
  const s = h.btn(document, "봇 설정");
  if (s) { s.click(); await h.settle(); await sweep(h, document.querySelector("#detail"), 2); }
  // connect: fill the steps and connect
  await h.click(document, "봇 연결");
  const inputs = [...table().querySelectorAll("input")];
  dom.type(inputs.find((i) => i.placeholder === "1234567890123456789"), "123456789012");
  const cb = table().querySelector("input[type=checkbox]"); cb.click();
  dom.type(table().querySelector("input[type=password]"), "x".repeat(30));
  const go = h.btn(table(), "연결");
  assert.equal(go.disabled, false);
  go.click(); await h.settle(); await h.settle(2100);
  assert.ok(h.notices.some((n) => /토큰을 넘겼어요/.test(n)));
  await sweep(h, document.body, 2);
  assert.deepEqual(clean(), []);
});

test("usage: KPIs, chart, tables, filters and retrying failures", async () => {
  const h = await bootTeams("crewUsage");
  await h.settle();
  assert.match(table().textContent, /성공률/);
  for (const col of document.querySelectorAll(".tm-col")) { dom.fire(col, "mouseenter"); dom.fire(col, "mouseleave"); }
  h.data.runs.push({ id: "runf", revision: 1, created_at: iso(-5), updated_at: iso(-4), crew_run: { crew_id: "wf1", status: "failed", input: "{}", nodes: { code: { status: "failed", error: "exit 1" }, ai1: { status: "done", output: "x" } }, loops: { "code>ai1:fail": 1 } } });
  h.render(); await h.settle();
  await sweep(h, document.body, 2);
  for (const k of ["today", "7d", "90d", "all"]) {
    const t = [...document.querySelectorAll("#controls .tm-seg-tab")].find((b) => b.textContent === { today: "오늘", "7d": "7일", "90d": "90일", all: "전체" }[k]);
    t?.click(); await h.settle();
  }
  h.routes.unshift(["GET", /^\/v1\/crew\/usage/, () => new Error("HTTP 501")]);
  await h.click(document, "새로고침");
  assert.match(table().textContent, /지원하지 않아요/);
  h.routes[0] = ["GET", /^\/v1\/crew\/usage/, () => ({})];
  await h.click(document, "새로고침");
  assert.match(table().textContent, /불러오지 못했어요/);
  assert.deepEqual(clean(), []);
});

const exact = (root, text) => [...root.querySelectorAll("button")].find((b) => b.textContent.trim() === text);
async function openTeam(h, id) {
  window.dispatchEvent(new CustomEvent("arkism:open-team", { detail: { id } }));
  await h.settle();
  return h.top();
}

test("meeting templates: edit with AI, add, validate, discard and delete", async () => {
  const h = await bootTeams("teams");
  const d = await openTeam(h, "tm2");
  exact(d, "주간 검토").click(); await h.settle();
  let t = h.top();
  assert.match(t.className, /tm-tpl-dlg/);
  const chat = () => h.chats.filter((c) => c.label === "AI 제안").at(-1);
  h.assistReply = () => ({ status: "done", output: JSON.stringify({ name: "월간 검토", prompt: "- 한 달 시안", mode: "moderator_picks", max_rounds: 14 }) });
  const r = await chat().ask("월간으로");
  assert.equal(r.props.length, 4);
  for (const p of r.props) { p.apply(); p.undo(); p.apply(); }
  h.assistReply = () => ({ status: "done", output: "{}" });
  assert.match((await chat().ask("그대로")).say, /찾지 못했어요/);
  h.assistReply = () => ({ status: "done", output: "[1]" });
  await assert.rejects(chat().ask("배열"), /읽지 못했어요/);
  h.assistReply = () => ({ status: "failed" });
  await assert.rejects(chat().ask("실패"));
  // the template's own ending, its rounds, cap and switches
  const endSw = t.querySelector('[aria-label="끝나는 조건 따로 정하기"]');
  if (endSw) { endSw.click(); await h.settle(); }
  t = h.top();
  for (const i of t.querySelectorAll(".tm-tpl-over input")) dom.type(i, "12,000");
  const mode = t.querySelector('select[aria-label="회의 방식"]'); mode.value = ""; dom.fire(mode, "change"); await h.settle();
  exact(t, "저장").click(); await h.settle();
  assert.ok(h.notices.some((n) => /템플릿을 넣었어요/.test(n)));
  // a new one: no name, then discard asked and refused, then accepted
  [...d.querySelectorAll("button.tm-chip.add")].find((b) => b.closest("section")?.textContent.includes("회의 템플릿")).click(); await h.settle();
  t = h.top();
  exact(t, "저장").click(); await h.settle();
  assert.ok(h.needs.includes("템플릿 이름을 적어 주세요."));
  dom.type(t.querySelector('[aria-label="템플릿 이름"]'), "새 회의");
  h.answer = false; exact(t, "취소").click(); await h.settle();
  assert.ok(t.open);
  dom.fire(t, "cancel"); await h.settle();
  h.answer = true; t.querySelector('[aria-label="닫기"]').click(); await h.settle();
  assert.equal(t.open, false);
  // delete a template: refused, then done
  const chip = [...d.querySelectorAll(".tm-chips .tm-chip")].find((c) => !c.classList.contains("add") && /검토/.test(c.textContent));
  chip.click(); await h.settle();
  t = h.top();
  h.answer = false; exact(t, "이 템플릿 지우기").click(); await h.settle();
  h.answer = true; exact(t, "이 템플릿 지우기").click(); await h.settle();
  assert.equal(t.open, false);
  assert.deepEqual(clean(), []);
});

test("team window: channels, save, unsaved guard, delete and a test meeting", async () => {
  const h = await bootTeams("teams");
  h.routes.unshift(["GET", /^\/v1\/discord\/bots\/[^/]+\/channels/, () => ({ items: [{ channel_id: "c1", name: "general", parent: "글" }, { channel_id: "c3", name: "random" }] })]);
  let d = await openTeam(h, "tm2");
  await h.settle();
  // link a channel: none picked, one taken, then a free one
  exact(d, "+ 채널 연결").click(); await h.settle(); await h.settle();
  const form = () => d.querySelector(".tm-ch-add");
  exact(form(), "연결").click(); await h.settle();
  assert.ok(h.needs.includes("채널을 골라 주세요."));
  let ch = form().querySelector('select[aria-label="채널"]'); ch.value = "c1"; dom.fire(ch, "change");
  h.answer = false; exact(form(), "연결").click(); await h.settle();
  h.answer = true;
  ch = form().querySelector('select[aria-label="채널"]'); ch.value = "c3"; dom.fire(ch, "change");
  exact(form(), "연결").click(); await h.settle();
  // change a linked channel in place, and change the bot in the add form
  const name = d.querySelector(".tm-ch-name");
  if (name) {
    name.click(); await h.settle(); await h.settle();
    ch = form().querySelector('select[aria-label="채널"]'); ch.value = "c3"; dom.fire(ch, "change");
    exact(form(), "바꾸기").click(); await h.settle();
  }
  exact(d, "+ 채널 연결").click(); await h.settle();
  const bs = form().querySelector('select[aria-label="봇"]'); bs.value = "b2"; dom.fire(bs, "change"); await h.settle();
  exact(form(), "취소").click(); await h.settle();
  assert.ok(h.notices.includes("채널을 연결했어요."), h.notices.join(" / "));
  // save: a failure, then the save
  dom.type(d.querySelector('[aria-label="맡을 일"]'), "새 흐름"); await h.settle();
  h.routes.unshift(["POST", /^\/v1\/crew\/tm2$/, () => new Error("저장 실패")]);
  exact(d, "저장").click(); await h.settle();
  h.routes.shift();
  exact(d, "저장").click(); await h.settle();
  assert.ok(h.notices.includes("저장했어요."));
  // closing with changes asks first
  dom.type(d.querySelector('[aria-label="맡을 일"]'), "또 바꿈"); await h.settle();
  h.answer = false; dom.fire(d, "cancel"); await h.settle();
  assert.ok(d.open);
  h.answer = true; dom.fire(d, "cancel"); await h.settle();
  // the test tab: an empty task, a failure, then a live meeting that polls
  d = await openTeam(h, "tm2");
  exact(d, "테스트").click(); await h.settle();
  const f = () => d.querySelector(".tm-test-form");
  dom.fire(f(), "submit"); await h.settle();
  assert.ok(h.needs.includes("맡길 일을 적어 주세요."));
  dom.type(f().querySelector("input"), "시안 봐 줘");
  h.routes.unshift(["POST", /^\/v1\/crew\/meetings/, () => new Error("회의 실패")]);
  dom.fire(f(), "submit"); await h.settle();
  h.routes.shift();
  dom.type(f().querySelector("input"), "시안 봐 줘");
  dom.fire(f(), "submit"); await h.settle();
  assert.ok(d.querySelector(".tm-test-thread"));
  h.routes.unshift(["GET", /^\/v1\/crew\/meetings\/mt-new/, () => new Error("잠깐 끊김")]);
  await h.settle(2100);
  h.routes.shift();
  await h.settle(2100);
  d.close(); await h.settle(2100);
  // delete: refused, failed, done
  d = await openTeam(h, "tm2");
  h.typed = false; exact(d, "팀 삭제").click(); await h.settle();
  h.typed = true;
  h.routes.unshift(["DELETE", /^\/v1\/crew\//, () => new Error("삭제 실패")]);
  exact(d, "팀 삭제").click(); await h.settle();
  h.routes.shift();
  exact(d, "팀 삭제").click(); await h.settle();
  assert.ok(h.notices.includes("지웠어요."));
  assert.deepEqual(clean(), []);
});

test("a new team from a similar one, its members and its context", async () => {
  const h = await bootTeams("teamNew");
  h.state.data.memories = [{ id: "m9", kind: "decision", text: "결정 하나" }];
  h.state.data.designSystems = [{ id: "ds1", name: "디자인" }];
  await h.settle();
  const d = h.top();
  exact(d, "팀 만들기").click(); await h.settle();
  assert.ok(h.notices.includes("팀 이름을 넣어 주세요."));
  exact(d, "디자인 팀").click(); await h.settle();
  exact(d, "빈 팀").click(); await h.settle();
  exact(d, "디자인 팀").click(); await h.settle();
  // members: drag to reorder
  const rows = () => [...d.querySelectorAll(".tm-mrow")].filter((r) => r.draggable);
  if (rows().length >= 2) {
    const [a, b] = rows();
    const dt = { setData() {}, getData() { return ""; }, effectAllowed: "", dropEffect: "" };
    dom.fire(a, "dragstart", { dataTransfer: dt }); dom.fire(b, "dragover", { dataTransfer: dt }); dom.fire(b, "dragleave");
    dom.fire(b, "drop", { dataTransfer: dt }); dom.fire(a, "dragend"); dom.fire(a, "drop", { dataTransfer: dt });
    await h.settle();
  }
  // context: add from the list
  for (let i = 0; i < 2; i++) {
    const add = [...d.querySelectorAll("section")].filter((s) => /자료/.test(s.textContent)).map((s) => exact(s, "+ 추가")).find(Boolean);
    if (!add) break;
    add.click(); await h.settle();
    const sel = [...d.querySelectorAll("select")].find((s) => s.options[0]?.textContent === "넣을 자료 고르기");
    if (!sel) break;
    sel.value = sel.options[1].value; dom.fire(sel, "change"); await h.settle();
  }
  // the name: Escape puts it back, then it is created
  const name = d.querySelector('[aria-label="팀 이름"]');
  dom.fire(name, "focus"); dom.type(name, "임시"); dom.key(name, "Escape");
  dom.type(d.querySelector('[aria-label="팀 이름"]'), "검토 팀 둘"); dom.key(d.querySelector('[aria-label="팀 이름"]'), "Enter"); await h.settle();
  exact(d, "팀 만들기").click(); await h.settle();
  assert.ok(h.notices.includes("팀을 만들었어요."), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("Discord: unlink a bot and save the settings, with failures", async () => {
  const h = await bootTeams("discord");
  await h.settle();
  const card = [...document.querySelectorAll(".tm-bot")].find((c) => c.textContent.includes("Arkism-bot"));
  dom.key(card, "Enter"); await h.settle();
  const detail = () => document.querySelector("#detail");
  h.answer = false; exact(detail(), "연결 끊기").click(); await h.settle();
  h.answer = true;
  h.routes.unshift(["POST", /^\/v1\/discord\/bots\/[^/]+\/update/, () => new Error("끊기 실패")]);
  exact(detail(), "연결 끊기").click(); await h.settle();
  h.routes.shift();
  exact(detail(), "연결 끊기").click(); await h.settle();
  assert.ok(h.notices.includes("연결을 끊었어요."));
  h.btn(document, "봇 설정").click(); await h.settle();
  h.routes.unshift(["POST", /^\/v1\/discord\/settings/, () => new Error("설정 실패")]);
  exact(detail(), "저장").click(); await h.settle();
  h.routes.shift();
  exact(detail(), "저장").click(); await h.settle();
  assert.ok(h.notices.includes("설정을 저장했어요."));
  assert.deepEqual(clean(), []);
});

test("usage: retrying failed runs, refused, partly failed and done", async () => {
  const h = await bootTeams("crewUsage");
  h.data.runs.push({ id: "runf", revision: 1, created_at: iso(-5), updated_at: iso(-4), crew_run: { crew_id: "wf1", status: "failed", input: "{}", nodes: { code: { status: "failed", error: "exit 1" }, ai1: { status: "done", output: "x" } }, loops: { "code>ai1:fail": 1 } } });
  await h.click(document, "새로고침");
  const retry = () => dom.byText(table(), "button", /실패한 곳부터 다시/);
  h.answer = false; retry().click(); await h.settle();
  h.answer = true;
  h.routes.unshift(["POST", /^\/v1\/crew\/runs$/, () => new Error("맡기기 실패")]);
  retry().click(); await h.settle();
  h.routes.shift();
  retry().click(); await h.settle();
  const again = exact(table(), "다시 실행");
  if (again) { again.click(); await h.settle(); }
  assert.ok(h.notices.some((n) => /실행을 맡겼어요/.test(n)), h.notices.join(" / "));
  assert.deepEqual(clean(), []);
});

test("#465 usage: failed run and meeting reads are said under the numbers, not shown as no failures", async () => {
  const h = await bootTeams("crewUsage", {});
  h.routes.unshift(["GET", /^\/v1\/crew\/runs\?/, () => new Error("boom")], ["GET", /^\/v1\/crew\/meetings\?/, () => new Error("boom")]);
  await h.click(document, "새로고침");
  await h.settle();
  assert.match(table().textContent, /성공률/);
  assert.match(table().textContent, /실행·회의 기록을 불러오지 못해/);
});
