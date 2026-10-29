// Run with: node --test desktop/ui/*.test.js
// The catalog view (window.ArkismCatalog(ctx)): GitHub skill install as a dialog and as a page, MCP presets, mcp_add watch.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");

const { document } = dom.install();
const h = dom.helpers(document);
// settle() polls the host every 5 s; run those waits at once so the tests stay fast.
const realTimeout = setTimeout;
globalThis.setTimeout = (fn, ms, ...a) => realTimeout(fn, ms === 5000 ? 0 : ms, ...a);
window.setTimeout = globalThis.setTimeout;
require("./catalog.js");

const SKILLS = { source: "o/r", commit: "abcdef1234567", skills: [
  { name: "pdf", description: "PDF 읽기", files: 2, bytes: 2048, installed: ["codex", "other"] },
  { name: "doc", files: 1, bytes: 10 }
] };

// host: how the fake server answers. post(body) → request list after the POST; poll(ids) → list on each GET.
function setup(host = {}) {
  document.body.replaceChildren();
  const calls = [], notices = [], needs = [];
  let reloads = 0, polls = 0, seq = 0;
  window.ArkismNeed = (node, msg) => needs.push([node, msg]);
  const state = { scope: { workspace_id: "w", project_id: "" }, data: { tools: { requests: [] } } };
  const request = async (path, opts = {}) => {
    calls.push([path, opts.method || "GET", opts.body ? JSON.parse(opts.body) : null]);
    if (opts.method === "POST") {
      const body = JSON.parse(opts.body);
      if (host.postError) throw new Error(host.postError);
      const id = `q${++seq}`;
      host.last = { id, kind: body.kind, name: body.name, status: "pending" };
      return host.post ? host.post(body, id) : { agent_tools: { requests: [host.last] } };
    }
    if (host.getError) throw new Error("offline");
    return { agent_tools: { requests: host.poll ? host.poll(host.last) : [] } };
  };
  const catalog = window.ArkismCatalog({ state, el: h.el, button: h.button, request, notice: (m) => notices.push(m),
    agentLogo: (spec, n) => { const s = h.el("span", "logo"); s.dataset.spec = spec; s.title = n; return s; },
    reload: () => reloads++, pollTools: () => polls++ });
  return { catalog, state, calls, notices, needs, reloads: () => reloads, polls: () => polls, host };
}
const done = (detail, extra = {}) => (q) => [{ ...q, status: "done", detail: JSON.stringify(detail), ...extra }];
const dialog = () => document.querySelector("dialog");
const btn = (root, label) => dom.buttons(root, label)[0];

test("install dialog: bad source asks for a fix, a good one previews skills and installs to the picked agents", async () => {
  const t = setup({ poll: done(SKILLS) });
  const b = t.catalog.installButton();
  assert.match(b.textContent, /GitHub에서 설치/);
  b.click();
  const dlg = dialog();
  assert.ok(dlg.open);
  const src = dlg.querySelector("input");
  assert.equal(document.activeElement, src);
  src.value = "not a repo";
  btn(dlg, "미리보기").click();
  assert.equal(t.needs.length, 1);
  assert.equal(t.calls.length, 0);

  src.value = "https://github.com/o/r";
  btn(dlg, "미리보기").click();
  await dom.flush();
  assert.deepEqual(t.calls[0].slice(0, 2), ["/v1/agent_tools/requests", "POST"]);
  assert.deepEqual(t.calls[0][2], { scope: t.state.scope, enabled: false, agent: "shared", kind: "skill_install", name: "o/r" });
  assert.match(t.calls[1][0], /^\/v1\/agent_tools\?workspace_id=w$/);
  const text = dlg.textContent;
  assert.match(text, /o\/r · 커밋 abcdef1 · 파일 3개 · 2KB/);
  assert.match(text, /이미 있음: Codex, other/);
  assert.match(text, /설명 없음/);
  assert.equal(t.state.data.tools.requests[0].status, "done");
  const update = dlg.querySelector("input[type=checkbox]");
  assert.equal(update.parentNode.hidden, false);
  update.checked = true;
  const callers = dlg.querySelectorAll(".crew-caller");
  assert.deepEqual(callers.map((c) => c.firstChild.dataset.spec), ["img:claude.svg", "sym:a-codex", "sym:i-stack", "sym:a-zcode"]);
  assert.ok(callers[0].classList.contains("on"));
  callers[0].click(); // claude off
  assert.ok(!callers[0].classList.contains("on"));
  const ok = btn(dlg, "설치");
  assert.equal(ok.hidden, false);
  await ok.onclick();
  assert.deepEqual(t.notices, ["넣을 에이전트를 골라 주세요."]);
  callers[1].click(); // codex on
  t.host.poll = done({ installed: ["codex"] });
  await ok.onclick();
  const post = t.calls.filter((c) => c[1] === "POST").at(-1)[2];
  assert.equal(post.name, "o/r@abcdef1234567");
  assert.deepEqual(post.targets, ["codex"]);
  assert.equal(post.update, true);
  assert.match(t.notices.at(-1), /스킬을 설치했어요: codex/);
  assert.equal(t.reloads(), 1);
  assert.equal(dialog(), null); // closed and removed
});

test("install dialog reports each preview outcome", async () => {
  const cases = [
    [{ post: () => ({ agent_tools: { requests: [] } }) }, /아직 답이 없어요/],
    [{ poll: (q) => [{ ...q, status: "failed", error: "404" }] }, /확인하지 못했어요: 404/],
    [{ poll: (q) => [{ ...q, status: "failed" }] }, /확인하지 못했어요: $/],
    [{ poll: done({ skills: [] }) }, /SKILL.md를 찾지 못했어요/],
    [{ poll: (q) => [{ ...q, status: "done", detail: "{bad" }] }, /SKILL.md를 찾지 못했어요/],
    [{ postError: "boom" }, /요청하지 못했어요 \(boom\)/],
    [{ getError: true }, /아직 답이 없어요/],
    [{ poll: (q) => [q] }, /아직 답이 없어요/] // still pending after every try
  ];
  for (const [host, want] of cases) {
    const t = setup(host);
    t.catalog.openInstall();
    const dlg = dialog();
    dlg.querySelector("input").value = "o/r";
    btn(dlg, "미리보기").click();
    await dom.flush(40);
    assert.match(dlg.querySelector("p.meta").textContent, want);
    assert.equal(btn(dlg, "미리보기").disabled, false);
    dlg.close();
  }
});

test("install dialog stops quietly when closed while waiting, and reports install failures", async () => {
  let t = setup({ poll: (q) => [q] });
  t.catalog.openInstall();
  let dlg = dialog();
  dlg.querySelector("input").value = "o/r";
  btn(dlg, "미리보기").click();
  await dom.flush(1);
  dlg.close();
  await dom.flush(10);
  assert.equal(dlg.querySelector("p.meta").textContent, "서버가 저장소를 내려받아 확인하고 있어요. 1분쯤 걸려요…");

  t = setup({ poll: done({ ...SKILLS, skills: [SKILLS.skills[1]] }) });
  t.catalog.openInstall();
  dlg = dialog();
  dlg.querySelector("input").value = "o/r";
  btn(dlg, "미리보기").click();
  await dom.flush();
  assert.equal(dlg.querySelector("input[type=checkbox]").parentNode.hidden, true);
  const ok = btn(dlg, "설치");
  t.host.poll = (q) => [{ ...q, status: "failed", error: "disk" }];
  await ok.onclick();
  assert.match(dlg.querySelector("p.meta").textContent, /설치하지 못했어요: disk/);
  t.host.poll = (q) => [{ ...q, status: "failed" }];
  dlg.open = false; // the owner closed it meanwhile
  await ok.onclick();
  assert.equal(t.notices.at(-1), "설치하지 못했어요: ");
  t.host.post = () => ({ agent_tools: { requests: [] } });
  await ok.onclick();
  assert.match(t.notices.at(-1), /설치 결과를 아직 받지 못했어요/);
  t.host.postError = "net";
  await ok.onclick();
  assert.match(dlg.querySelector("p.meta").textContent, /요청하지 못했어요 \(net\)/);
  assert.equal(ok.disabled, false);
  delete t.host.postError; delete t.host.post;
  t.host.poll = (q) => [{ ...q, status: "done" }]; // no detail: nothing listed, dialog already closed
  await ok.onclick();
  assert.equal(t.notices.at(-1), "스킬을 설치했어요: . 목록에는 몇 분 안에 보여요.");
});

test("install page: preview list, side detail, agent picks, cancel and install", async () => {
  const t = setup({ poll: done(SKILLS) });
  const side = h.el("aside");
  let alive = true;
  const page = t.catalog.installPage(side, () => alive);
  document.body.append(page, side);
  assert.match(side.textContent, /SKILL.md를 찾아요/);
  const form = page.querySelector("form"), src = page.querySelector("input");
  const submit = () => dom.fire(form, "submit");
  src.value = "bad";
  submit();
  assert.equal(t.needs.length, 1);
  const ok = btn(page, "설치");
  ok.click(); // nothing found yet
  assert.equal(t.calls.length, 0);

  src.value = "o/r/skills@main";
  submit();
  await dom.flush();
  assert.equal(t.calls[0][2].name, "o/r/skills@main");
  const status = page.querySelector(".v4s-status");
  assert.match(status.textContent, /스킬 2개 찾음 · o\/r · 커밋 abcdef1 · 파일 3개 · 2KB/);
  const rows = page.querySelectorAll(".v4s-row");
  assert.equal(rows.length, 2);
  assert.ok(rows[0].classList.contains("selected"));
  assert.equal(rows[0].querySelector(".v4s-uses").textContent, "이미 있음");
  assert.equal(rows[1].querySelector(".v4s-badge").textContent, "새 스킬");
  assert.match(rows[1].textContent, /설명 없음/);
  assert.equal(page.querySelector(".v4s-check").hidden, false);
  assert.equal(page.querySelector("strong").textContent, "스킬 2개");
  assert.match(side.textContent, /PDF 읽기/);
  assert.match(side.textContent, /이미 있음Codex, other/);
  assert.deepEqual(side.querySelectorAll(".v4s-loc").map((p) => p.textContent), ["~/.claude/skills/pdf"]);

  rows[1].click();
  assert.ok(page.querySelectorAll(".v4s-row")[1].classList.contains("selected"));
  assert.equal(side.querySelector("h2").textContent, "doc"); // no description → name
  assert.match(side.textContent, /이미 있음없음/);
  const chips = () => side.querySelectorAll(".v4s-agent");
  assert.equal(chips()[3].firstChild.dataset.spec, "sym:a-zcode");
  chips()[0].click(); // claude off
  assert.ok(chips()[0].classList.contains("off"));
  assert.equal(side.querySelectorAll(".v4s-loc").length, 0);
  ok.click();
  assert.deepEqual(t.notices, ["넣을 에이전트를 골라 주세요."]);
  chips()[2].click(); // shared folder on
  assert.deepEqual(side.querySelectorAll(".v4s-loc").map((p) => p.textContent), ["~/.agents/skills/doc"]);
  page.querySelector("input[type=checkbox]").checked = true;
  t.host.poll = done({ installed: ["agents"] });
  ok.click();
  await dom.flush();
  const post = t.calls.filter((c) => c[1] === "POST").at(-1)[2];
  assert.deepEqual([post.name, post.targets, post.update], ["o/r@abcdef1234567", ["agents"], true]);
  assert.match(t.notices.at(-1), /스킬을 설치했어요: agents/);
  assert.equal(t.reloads(), 1);

  btn(page, "취소").click();
  assert.equal(page.querySelector(".v4s-list").hidden, true);
  assert.equal(status.textContent, "");
  assert.match(side.textContent, /SKILL.md를 찾아요/);
  alive = false;
});

test("install page: a zip file goes in the request, previews and installs; a big one is refused", async () => {
  const t = setup({ poll: done({ ...SKILLS, source: "pdf.zip", commit: "f".repeat(64) }) });
  const side = h.el("aside");
  const page = t.catalog.installPage(side, () => true);
  document.body.append(page, side);
  const pick = (file) => {
    const made = [], create = document.createElement;
    document.createElement = (tag) => { const n = create.call(document, tag); if (tag === "input") made.push(n); return n; };
    try { btn(page, "zip 파일").click(); } finally { document.createElement = create; }
    assert.equal(made[0].type, "file");
    made[0].files = [file];
    dom.fire(made[0], "change");
  };
  const zipBody = Buffer.from("PK zip").toString("base64");
  pick({ name: "pdf.zip", size: 6, arrayBuffer: async () => new TextEncoder().encode("PK zip").buffer });
  await dom.flush();
  assert.deepEqual([t.calls[0][2].name, t.calls[0][2].zip, t.calls[0][2].enabled], ["pdf.zip", zipBody, false]);
  assert.match(page.querySelector(".v4s-status").textContent, /pdf\.zip · SHA-256 fffffff/);
  // A bad address after the zip preview keeps the zip: install still sends it.
  page.querySelector("input").value = "bad";
  dom.fire(page.querySelector("form"), "submit");
  assert.equal(t.needs.length, 1);
  t.host.poll = done({ installed: ["claude_code"] });
  btn(page, "설치").click();
  await dom.flush();
  const post = t.calls.filter((c) => c[1] === "POST").at(-1)[2];
  assert.deepEqual([post.name, post.zip, post.targets], ["pdf.zip", zipBody, ["claude_code"]]);
  const posts = t.calls.length;
  pick({ name: "big.zip", size: 200 * 1024, arrayBuffer: async () => new ArrayBuffer(0) });
  await dom.flush();
  assert.equal(t.calls.length, posts);
  assert.match(page.querySelector(".v4s-status").textContent, /160KB/);
});

test("install page reports each preview and install outcome", async () => {
  const cases = [
    [{ post: () => ({ agent_tools: { requests: [] } }) }, /아직 답이 없어요/],
    [{ poll: (q) => [{ ...q, status: "failed", error: "404" }] }, /확인하지 못했어요: 404/],
    [{ poll: (q) => [{ ...q, status: "failed" }] }, /확인하지 못했어요: $/],
    [{ poll: done({ skills: [] }) }, /SKILL.md를 찾지 못했어요/],
    [{ postError: "boom" }, /요청하지 못했어요 \(boom\)/]
  ];
  for (const [host, want] of cases) {
    const t = setup(host);
    const page = t.catalog.installPage(h.el("aside"), () => true);
    page.querySelector("input").value = "o/r";
    dom.fire(page.querySelector("form"), "submit");
    await dom.flush();
    assert.match(page.querySelector(".v4s-status").textContent, want);
  }
  // gone while waiting: the status is left as it was
  let alive = true;
  let t = setup({ poll: (q) => [q] });
  let page = t.catalog.installPage(h.el("aside"), () => alive);
  page.querySelector("input").value = "o/r";
  dom.fire(page.querySelector("form"), "submit");
  await dom.flush(1);
  alive = false;
  await dom.flush(10);
  assert.match(page.querySelector(".v4s-status").textContent, /1분쯤 걸려요/);

  // install outcomes on a skill with nothing installed yet (the update box stays hidden, so update is false)
  alive = true;
  t = setup({ poll: done({ ...SKILLS, skills: [SKILLS.skills[1]] }) });
  page = t.catalog.installPage(h.el("aside"), () => alive);
  page.querySelector("input").value = "o/r";
  dom.fire(page.querySelector("form"), "submit");
  await dom.flush();
  page.querySelector("input[type=checkbox]").checked = true;
  const ok = btn(page, "설치"), status = page.querySelector(".v4s-status");
  t.host.poll = (q) => [{ ...q, status: "failed", error: "disk" }];
  ok.click(); await dom.flush();
  assert.equal(t.calls.filter((c) => c[1] === "POST").at(-1)[2].update, false);
  assert.match(status.textContent, /설치하지 못했어요: disk/);
  alive = false;
  t.host.poll = (q) => [{ ...q, status: "failed" }];
  ok.click(); await dom.flush();
  assert.equal(t.notices.at(-1), "설치하지 못했어요: ");
  t.host.post = () => ({ agent_tools: { requests: [] } });
  ok.click(); await dom.flush();
  assert.match(t.notices.at(-1), /설치 결과를 아직 받지 못했어요/);
  t.host.postError = "net";
  ok.click(); await dom.flush();
  assert.match(status.textContent, /요청하지 못했어요 \(net\)/);
  delete t.host.postError; delete t.host.post;
  t.host.poll = (q) => [{ ...q, status: "done" }];
  ok.click(); await dom.flush();
  assert.equal(t.notices.at(-1), "스킬을 설치했어요: . 목록에는 몇 분 안에 보여요.");
});

test("preset picker fills name, method and program or address", () => {
  const t = setup();
  const inputs = { name: h.el("input"), how: h.el("select"), cmd: h.el("input"), url: h.el("input"), sync: () => syncs++ };
  let syncs = 0;
  const field = t.catalog.presetField(inputs);
  const pick = field.querySelector("select"), about = field.querySelector("p");
  assert.equal(pick.options.length, 7);
  assert.equal(pick.options[5].textContent, "github — 주소");
  pick.value = "github"; dom.fire(pick, "change");
  assert.deepEqual([inputs.name.value, inputs.how.value, inputs.url.value], ["github", "http", "https://api.githubcopilot.com/mcp/"]);
  assert.equal(about.querySelector("a").href, "https://github.com/github/github-mcp-server");
  assert.doesNotMatch(about.textContent, /필요:/);
  pick.value = "filesystem"; dom.fire(pick, "change");
  assert.equal(inputs.cmd.value, "npx -y @modelcontextprotocol/server-filesystem /path/to/allowed/dir");
  assert.equal(document.activeElement, inputs.cmd);
  assert.match(about.textContent, /필요: Node.js \(npx\)\./);
  assert.equal(syncs, 2);
  pick.value = ""; dom.fire(pick, "change");
  assert.equal(about.textContent, "");
  assert.equal(syncs, 2);
});

test("watchAdd warns about programs the host could not find, then refreshes tools", async () => {
  const t = setup();
  await t.catalog.watchAdd({ agent_tools: { requests: [{ id: "x", kind: "mcp_add", name: "fs", status: "done" }] } }, "fs");
  assert.equal(t.calls.length, 0); // nothing pending: no wait
  assert.equal(t.polls(), 0);
  await t.catalog.watchAdd(null, "fs");
  t.host.poll = () => [
    { id: "a", status: "done", detail: JSON.stringify({ warning: "command_missing", command: "npx" }) },
    { id: "b", status: "done", detail: JSON.stringify({ warning: "command_missing", command: "npx" }) },
    { id: "c", status: "failed" }
  ];
  const pending = (id) => ({ id, kind: "mcp_add", name: "fs", status: "pending" });
  await t.catalog.watchAdd({ agent_tools: { requests: [pending("a"), pending("b"), pending("c"), { ...pending("d"), name: "other" }] } }, "fs");
  assert.deepEqual(t.notices, ["fs을(를) 추가했지만 서버에서 npx 프로그램을 찾지 못했어요. 설치해야 동작해요."]);
  assert.equal(t.polls(), 1);
  t.host.poll = () => [{ id: "a", status: "done" }];
  await t.catalog.watchAdd({ agent_tools: { requests: [pending("a")] } }, "fs");
  assert.equal(t.notices.length, 1);
  assert.equal(t.polls(), 2);
  t.state.data.tools = null; // settle keeps working without a loaded tool list
  t.host.poll = () => [];
  await t.catalog.watchAdd({ agent_tools: { requests: [pending("z")] } }, "fs"); // never answered
  assert.equal(t.polls(), 2);
});
