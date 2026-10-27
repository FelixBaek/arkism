// Drive the memory and candidate detail views through a small DOM/API stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");
const moduleTask = import("./modules/memoryDetail.mjs");

function el(tag, className = "", text = "") {
  return {tag, className, textContent: text, value: "", children: [], listeners: {}, attrs: {},
    append(...children) { this.children.push(...children); },
    prepend(...children) { this.children.unshift(...children); },
    insertBefore(child, ref) { this.children.splice(Math.max(0, this.children.indexOf(ref)), 0, child); },
    get lastChild() { return this.children.at(-1); },
    replaceChildren(...children) { this.children = children; },
    setAttribute(name, value) { this.attrs[name] = value; },
    addEventListener(name, fn) { this.listeners[name] = fn; },
    setCustomValidity(message) { this.validity = message; }, reportValidity() {},
    isConnected: true};
}

const all = (node, out = []) => { if (node && typeof node === "object") { out.push(node); for (const child of node.children || []) all(child, out); } return out; };
const find = (root, text) => all(root).find(node => node.tag === "button" && node.textContent === text);
const texts = root => all(root).map(node => node.textContent).filter(Boolean).join("\n");
const click = button => button.onClick({currentTarget: button});
const settle = () => new Promise(resolve => setImmediate(resolve));

async function setup({caps = {}, routes = {}, ...overrides} = {}) {
  const calls = [], notices = [], decisions = [], conflicts = [], sources = [];
  let reloads = 0, renders = 0, needs = 0;
  const shell = el("section");
  const ctx = {
    state: {selected: "m1", scope: {workspace_id: "w"}, caps: {actions: caps}},
    generation: 1, KEEP_KEY: "keep",
    el, raw: node => node, facts: rows => el("dl", "", rows.flat().join(" ")),
    section: (title, meta) => el("section", "", title + (meta ? ` ${meta}` : "")),
    actionCard: (text, ...buttons) => { const card = el("div", "card", text); card.append(...buttons); return card; },
    button: (text, cls, icon, onClick) => Object.assign(el("button", cls, text), {onClick}),
    detailShell: () => shell, detailHead: (body, ...head) => body.append(el("header", "", head.join(" | "))),
    usageOf: () => ({last: "2026-10-01", label7: 2, d30: 5}), memStat: () => ({injected: 3, used: 0}),
    memUse: () => ({days: 7}), kindLabel: kind => kind, originOf: () => "chat", shortDate: when => String(when || "-").slice(0, 10),
    memRepo: () => "", ago: () => "어제", standing: () => false, savedHow: () => "직접", tr: text => text, withAgent: () => "Claude",
    overlapReview: () => false, reviewOverdue: () => false, idle: () => false,
    renderMemoryContext: () => el("div", "", "context"), renderModelGeneration: () => el("div", "", "model"),
    privacy: {memorySections: () => [el("section", "", "privacy")]},
    originals: {sourceDetails: (box, record, response) => sources.push([record.id, response])},
    query: () => "scope=w", reviewOnce: (key, fn) => fn(),
    notice: text => notices.push(text), failNotice: (error, denied, other) => notices.push(other),
    render: () => renders++, renderDetail: () => {}, reloadMemories: async () => { reloads++; },
    decide: async (c, action, reason) => decisions.push([action, reason]),
    resolveConflict: async (c, choice) => conflicts.push(choice),
    waitReason: () => ["반복해서 나온 말이에요"], isCorrection: () => false,
    request: async (route, options) => {
      calls.push([route, options && JSON.parse(options.body)]);
      for (const [prefix, answer] of Object.entries(routes)) if (route.startsWith(prefix)) return typeof answer === "function" ? answer(route) : answer;
      return {};
    },
    ...overrides
  };
  const stored = {};
  globalThis.localStorage = {setItem: (key, value) => { stored[key] = value; }};
  globalThis.window = {
    ArkismMemoryManagement: {approval: () => ({label: "직접 승인", detail: "사람이 승인"})},
    ArkismMemoryEvaluation: {mount: () => {}},
    ArkismNeed: () => { needs++; }
  };
  globalThis.ask = async () => true;
  globalThis.askText = async () => "고친 문장";
  const { createMemoryDetail } = await moduleTask;
  Object.assign(ctx, createMemoryDetail(ctx));
  return {ctx, shell, calls, notices, decisions, conflicts, sources, stored,
    reloads: () => reloads, renders: () => renders, needs: () => needs};
}

const memory = {id: "m1", revision: 4, text: "결론부터 쓴다", kind: "procedural", status: "active", created_at: "2026-09-01T00:00:00Z"};

test("memory detail lists history, offers revert, and hands sources to the evidence view", async () => {
  const h = await setup({caps: {propose: true}, routes: {
    "/v1/memories/m1/sources": {items: ["src"]},
    "/v1/memories/m1/lineage": {links: [
      {relation: "proposal", record: {text: "ignored"}},
      {relation: "conflict", record: {created_at: "2026-09-03", text: "다른 말"}},
      {relation: "supersedes", record: {updated_at: "2026-08-01", text: "older"}},
      {relation: "supersedes", record: {updated_at: "2026-08-20", text: "newer"}}
    ]}
  }});
  await h.ctx.memoryDetail(memory);
  assert.deepEqual(h.sources, [["m1", {items: ["src"]}]]);
  const summary = all(h.shell).find(node => node.tag === "summary");
  assert.equal(summary.textContent, "변경 이력 4건");
  assert.match(texts(h.shell), /다른 기억과 부딪힘/);
  assert.doesNotMatch(texts(h.shell), /ignored/);
  const back = all(h.shell).find(node => node.textContent === "이전으로 되돌리기");
  back.listeners.click();
  await settle();
  const capture = h.calls.find(([route]) => route === "/v1/capture")[1];
  assert.equal(capture.claims[0].text, "newer");
  assert.ok(find(h.shell, "고치기"));
});

test("memory detail stops when the selection changed while reading", async () => {
  const h = await setup({routes: {"/v1/memories/m1/lineage": {links: [{relation: "supersedes", record: {text: "old"}}]}}});
  h.ctx.state.selected = "other";
  await h.ctx.memoryDetail(memory);
  assert.deepEqual(h.sources, []);
  assert.equal(all(h.shell).find(node => node.tag === "summary").textContent, "변경 이력 1건");
});

test("an unconfirmed memory can be kept or forgotten", async () => {
  const h = await setup({overlapReview: () => true, keptMemories: () => ["old"]});
  await h.ctx.memoryDetail(memory);
  assert.match(texts(h.shell), /3번 건넸는데/);
  find(h.shell, "맞아요, 유지").onClick();
  assert.deepEqual(JSON.parse(h.stored.keep), ["old", "m1"]);
  assert.equal(h.notices.at(-1), "유지할게요. 이 기억은 다시 묻지 않아요.");
  assert.equal(h.renders(), 1);
  await find(h.shell, "잊기").onClick();
  const [route, body] = h.calls.at(-1);
  assert.equal(route, "/v1/memories/m1");
  assert.equal(body.expected_revision, 4);
  assert.equal(h.notices.at(-1), "잊었어요.");
  assert.equal(h.ctx.state.selected, null);
  assert.equal(h.reloads(), 1);
});

test("forgetting reports a refused delete and does nothing when declined", async () => {
  const h = await setup({idle: () => true, request: async () => { throw new Error("forbidden"); }});
  await h.ctx.memoryDetail(memory);
  await find(h.shell, "잊기").onClick();
  assert.equal(h.notices.at(-1), "잊지 못했어요 (forbidden).");
  globalThis.ask = async () => false;
  await find(h.shell, "잊기").onClick();
  assert.equal(h.notices.length, 1);
});

for (const [name, overrides, expected] of [
  ["overdue", {reviewOverdue: () => true}, /재검토 예정일이 지났어요/],
  ["idle", {idle: () => true, caps: {propose: true}}, /검색으로 전달된 기록이 없어요/],
  ["standing", {standing: () => true}, /대화를 시작할 때마다/]
]) {
  test(`memory detail explains the ${name} state`, async () => {
    const h = await setup(overrides);
    await h.ctx.memoryDetail({...memory, conflict_id: "x", scope: {agent_id: "codex"}});
    assert.match(texts(h.shell), expected);
    assert.match(texts(h.shell), /정정 또는 충돌 검토가 대기 중/);
  });
}

test("original retention status reports both answers and failures", async () => {
  const h = await setup({routes: {"/v1/memories/m1/management-status": {originals_available: 2, originals_expired: 1, originals_unknown: 0, complete: false}}});
  await h.ctx.memoryDetail(memory);
  const check = find(h.shell, "원본 보관 확인");
  await click(check);
  assert.match(texts(h.shell), /원본 보관 중 2개 · 만료 1개 · 확인 불가 0개 · 일부만 확인/);
  assert.equal(check.disabled, false);
  h.ctx.request = async () => { throw new Error("offline"); };
  await click(check);
  assert.match(texts(h.shell), /원본 상태를 확인하지 못했어요/);
});

test("review due dates must lie ahead and can be saved or cleared", async () => {
  const h = await setup({caps: {review_decide: true}});
  await h.ctx.memoryDetail({...memory, review_due_at: "2026-11-01T00:00:00Z"});
  const date = all(h.shell).find(node => node.tag === "input");
  assert.equal(date.value, "2026-11-01");
  date.value = "2000-01-01";
  find(h.shell, "예정일 저장").onClick();
  assert.equal(date.validity, "앞으로의 날짜를 선택해 주세요.");
  date.listeners.input();
  assert.equal(date.validity, "");
  date.value = "2999-01-01";
  find(h.shell, "예정일 저장").onClick();
  await settle();
  let [route, body] = h.calls.at(-1);
  assert.equal(route, "/v1/memories/m1/review-due");
  assert.equal(body.review_due_at.slice(0, 4), "2999");
  assert.equal(h.notices.at(-1), "재검토 예정일을 변경했어요.");
  await find(h.shell, "예정일 해제").onClick();
  [, body] = h.calls.at(-1);
  assert.equal(body.review_due_at, null);
  h.ctx.request = async () => { throw new Error("stale"); };
  await find(h.shell, "예정일 해제").onClick();
  assert.equal(h.notices.at(-1), "변경하지 못했어요 (stale).");
});

const candidate = {id: "c1", revision: 2, text: "새 규칙", kind: "procedural", created_at: "2026-10-01", summary: "여러 번 반복됨"};

test("a new candidate is approved, rejected with a reason, or fixed", async () => {
  const h = await setup({caps: {review_decide: true}});
  await h.ctx.candidateDetail(candidate);
  assert.match(texts(h.shell), /반복해서 나온 말이에요\. 저장하면/);
  assert.match(texts(h.shell), /AI가 제안한 이유/);
  const reject = find(h.shell, "버리기");
  reject.onClick();
  assert.equal(h.needs(), 1);
  assert.deepEqual(h.decisions, []);
  all(h.shell).find(node => node.id === "reject-reason").value = " 틀림 ";
  reject.onClick();
  assert.deepEqual(h.decisions, [["reject", "틀림"]]);
  assert.equal(find(h.shell, "그대로 저장").disabled, true);
  const fresh = await setup({caps: {review_decide: true}});
  await fresh.ctx.candidateDetail(candidate);
  const approve = find(fresh.shell, "그대로 저장");
  assert.notEqual(approve.disabled, true);
  approve.onClick();
  assert.deepEqual(fresh.decisions, [["approve", ""]]);
  await find(h.shell, "고쳐서 저장").onClick();
  await settle();
  const [route, body] = h.calls.at(-1);
  assert.equal(route, "/v1/memory_candidates/c1/review");
  assert.deepEqual([body.action, body.content, body.expected_revision], ["edit_approve", "고친 문장", 2]);
  assert.equal(h.notices.at(-1), "고쳐서 저장했어요.");
  h.ctx.request = async () => { throw new Error("stale"); };
  await find(h.shell, "고쳐서 저장").onClick();
  await settle();
  assert.equal(h.notices.at(-1), "저장하지 못했어요 (stale).");
  globalThis.askText = async () => "새 규칙";
  const before = h.notices.length;
  await find(h.shell, "고쳐서 저장").onClick();
  assert.equal(h.notices.length, before);
});

test("an instruction-like candidate leads with discard and is marked as a note", async () => {
  const h = await setup({caps: {review_decide: true}});
  await h.ctx.candidateDetail({...candidate, summary: "", tags: ["review:instruction_like"]});
  const card = all(h.shell).find(node => node.className === "card");
  assert.equal(card.attrs.role, "note");
  assert.equal(card.children[0].textContent, "버리기");
  assert.doesNotMatch(texts(h.shell), /AI가 제안한 이유/);
});

test("a reader without review rights only sees the explanation", async () => {
  const h = await setup();
  await h.ctx.candidateDetail(candidate);
  assert.equal(find(h.shell, "그대로 저장"), undefined);
});

test("candidate sources load on request and failures are not treated as evidence", async () => {
  const h = await setup({routes: {"/v1/memory_candidates/c1/sources": {items: ["quote"]}}});
  h.ctx.state.selected = "c1";
  await h.ctx.candidateDetail(candidate);
  const read = find(h.shell, "근거 확인");
  await click(read);
  assert.deepEqual(h.sources, [["c1", {items: ["quote"]}]]);
  h.ctx.request = async () => { throw new Error("offline"); };
  await click(read);
  assert.match(texts(h.shell), /원문 근거를 확인하지 못했어요/);
  assert.equal(read.disabled, false);
});

for (const correction of [false, true]) {
  test(`a ${correction ? "correction" : "conflict"} compares the old memory and resolves`, async () => {
    const h = await setup({caps: {review_decide: true}, isCorrection: () => correction, routes: {
      "/v1/memory_relations/r1": {relations: [{collection: "memory_candidates", id: "c1"}, {collection: "memories", id: "m9"}]},
      "/v1/memories/m9": {id: "m9", text: "옛 규칙", created_at: "2026-01-01"}
    }});
    await h.ctx.candidateDetail({...candidate, conflict_id: "r1", tags: ["review:instruction_like"]});
    await settle(); await settle();
    assert.match(texts(h.shell), /“옛 규칙”/);
    assert.match(texts(h.shell), /AI에게 명령하는 문장처럼/);
    assert.match(texts(h.shell), correction ? /고친 내용/ : /새로 들어온 내용/);
    find(h.shell, "새 내용으로 바꾸기").onClick();
    find(h.shell, "지금 기억 유지").onClick();
    if (correction) await find(h.shell, "둘 다 지우기").onClick();
    else find(h.shell, "둘 다 맞아요").onClick();
    assert.deepEqual(h.conflicts, ["new", "keep_old", correction ? "drop" : "both"]);
  });
}

test("a conflict whose old memory cannot be read says so", async () => {
  const h = await setup({request: async () => { throw new Error("gone"); }});
  await h.ctx.candidateDetail({...candidate, conflict_id: "r1"});
  await settle(); await settle();
  assert.match(texts(h.shell), /기존 기억을 불러오지 못했어요/);
  assert.equal(find(h.shell, "지금 기억 유지"), undefined);
});
