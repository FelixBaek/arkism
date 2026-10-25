// 스킬 (modules/skillViews.mjs): checks, the list and its filters, staged agent changes, the detail and the in-view pages.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house, flush } = require("./testdata/views-dom.js");
const mod = import("./modules/skillViews.mjs");

const AGENTS = [["claude_code", "Claude Code", "cc"], ["codex", "Codex", "cx"], ["gemini", "Gemini", "gm"]];

function setup({ skills = [], snapshot = {}, handler, wide = true } = {}) {
  const doc = createDocument();
  globalThis.document = doc;
  const chats = [];
  globalThis.window = { matchMedia: () => ({ matches: wide }), ArkismAIChat: { button: (o) => { chats.push(o); return doc.createElement("button"); } } };
  const h = house(doc);
  for (const id of ["controls", "chips", "stats", "table", "title-count", "change-count", "change-text"]) doc.body.append(Object.assign(h.el("div"), { id }));
  doc.body.append(Object.assign(h.el("input"), { id: "search" }));
  doc.body.append(Object.assign(h.el("div"), { id: "change-bar" }));
  for (const id of ["change-apply", "change-revert"]) doc.body.append(Object.assign(h.el("button"), { id }));
  const calls = [], notices = [], log = [], keys = {};
  const state = { skillSort: "used", skillDir: 0, skillTab: "all", skillCat: "all", skillModel: "", selected: null, query: "", skillLimit: 0, view: "skills", scope: { project_id: "p" },
    pending: new Map(), openCopies: new Set(), versions: new Map(), versionsDone: true, data: { skills, skillUse: new Map(), tools: { skill_checks: [], skill_updates: [], requests: [] } } };
  const snap = { items: skills, total: skills.length, facets: {}, error: null, loading: false, cursor: "", ...snapshot };
  const records = () => state.data.skills;
  const deps = {
    ...h, state, generation: 1, TRASH_KEY: "trash",
    shownAgents: () => AGENTS,
    segment: (tabs, cur, pick) => { const w = h.el("div", "segment"); for (const [k, l, n] of tabs) { const b = h.el("button", `tab${k === cur ? " active" : ""}`, n === undefined ? l : `${l}${n}`); b.addEventListener("click", () => pick(k)); w.append(b); } return w; },
    loadAll: () => log.push("loadAll"), barActions: (reload) => [h.button("새로고침", "", null, reload), h.button("설치")],
    store: (k, v) => log.push(["store", k, v]), render: (x) => log.push(["render", x]), renderDetail: () => log.push("detail"),
    catalogQuery: { record: (id) => records().find((x) => `skill:${x.id}` === id) || null, ensure: (c) => log.push(["ensure", c]), snapshot: () => snap, more: async (c) => { log.push(["more", c]); if (snap.moreFails) throw new Error("x"); }, refreshVisible: async () => log.push("refresh") },
    trashed: () => state.trash || [],
    pages: { categoryOf: (sk) => (sk.category ? { name: sk.category } : null), prefs: { categories: [] } },
    go: (...a) => log.push(["go", ...a]), agentName: (k) => Object.fromEntries(AGENTS.map(([a, n]) => [a, n]))[k] || "",
    agentLogo: (logo, n) => Object.assign(h.el("img", "logo"), { title: n }),
    row: (id, cls, parts, onOpen) => { const r = h.el("div", `row ${cls}`.trim()); r.dataset.id = id; r.append(...parts); r.addEventListener("click", () => { state.selected = id; onOpen(); }); return r; },
    virtualList: { mount: ({ container, items, row }) => { container.append(...items.map(row)); log.push(["mount", items.length]); } },
    setSkillStatus: async (sk, st) => { if (state.restoreFails) throw new Error("locked"); log.push(["status", sk.id, st]); },
    writeKey: (k, v) => { keys[k] = v; }, readKey: (k, d) => keys[k] ?? d,
    collectionPaging: { read: async () => { if (state.readFails) throw new Error("r"); return state.data.skills; } },
    loadTrash: async () => [],
    notice: (t) => notices.push(t),
    detailShell: (t) => { const b = h.el("div", "detail"); b.heading = t; doc.body.append(b); return b; },
    crew: { explain: async (q) => `설명: ${q.length}`, skillExperts: (sk) => (sk.experts ? h.el("section", "experts") : null) },
    ago: () => "방금", repoOf: (sk) => sk.repo || null,
    updateSkills: (u) => log.push(["update", u.length]), checkSkillRepo: (r) => log.push(["check", r.repo]),
    confirmBatch: (list, fn) => { log.push(["confirm", list.length]); fn(); }, trashSkills: (list) => log.push(["trash", list.map((x) => x.id)]),
    checksSection: () => h.el("section", "checks"), facts: (rows) => h.el("dl", "facts", rows.map((r) => r.join(":")).join(",")),
    pcLink: (k) => Object.assign(h.el("span", "pc"), { title: `PC ${k}` }), pcLegend: () => h.el("p", "legend"),
    query: () => "q=1",
    pageBack: (label, fn) => { const b = h.el("button", "page-back", label); b.addEventListener("click", fn); doc.body.prepend(b); },
    catalog: { installPage: (side, alive) => { side.append("설치 안내"); const m = h.el("div", "install"); m.alive = alive; return m; } },
    skillGenerate: () => h.el("details", "generate"), skillReview: (sk, part) => h.el("details", `review-${part}`),
    request: async (path, opt) => { const body = opt?.body && JSON.parse(opt.body); calls.push([path, body]); return handler ? handler(path, body) : {}; },
  };
  return { doc, h, deps, state, snap, calls, notices, log, keys, chats };
}
async function views(o) { const s = setup(o); const { createSkillViews } = await mod; s.v = createSkillViews(s.deps); return s; }
const btn = (root, label) => root.querySelectorAll("button").find((b) => b.textContent.includes(label));
const SK = (id, x = {}) => ({ id, name: id, version_id: `v-${id}`, revision: 1, status: "active", description: "", source_path: `/home/u/.claude/skills/${id}/SKILL.md`, ...x });

test("checks: derived analysis, host scans, verdict order and finding text", async () => {
  const s = await views();
  const sk = SK("a");
  assert.equal(s.v.skillChecks(sk), null);
  s.state.versions.set("v-a", null);
  assert.equal(s.v.skillChecks(sk).security.label, "확인 못 함");
  s.state.versions.set("v-a", { validation_id: "x", analysis: { problem: "p", use_when: ["a"], avoid_when: [], output_format: " ", irreversible_actions: ["rm"], external_transfer: true, secret_names: ["TOKEN"] }, inspection: { redacted: true, personal_data: true } });
  const c = s.v.skillChecks(sk);
  assert.equal(c.quality.score, 33);
  assert.equal(c.quality.grade, "F");
  assert.equal(c.security.cls, "bad");
  assert.equal(c.security.reasons.length, 5);
  assert.equal(c.validated, true);
  for (const [parts, grade] of [[6, "A"], [5, "B"], [4, "C"], [3, "D"]]) {
    const a = Object.fromEntries(["problem", "use_when", "avoid_when", "output_format", "success_criteria", "verification"].slice(0, parts).map((k) => [k, "x"]));
    s.state.versions.set("v-a", { analysis: a });
    assert.equal(s.v.skillChecks(sk).quality.grade, grade);
  }
  s.state.data.tools.skill_checks = [
    { name: "a", verdict: "SAFE", quality_score: 80 },
    { name: "a", verdict: "CAUTION", quality_score: 50.4, quality_grade: "C", top_findings: ["secret · HIGH · run.sh:12"], findings: 3, risk_score: 4 },
    { name: "a", quality_score: 90 },
    { name: "b", verdict: "DO_NOT_INSTALL" },
  ];
  const h = s.v.skillChecks(sk);
  assert.equal(h.source, "host");
  assert.deepEqual(h.quality, { score: 50, grade: "C", cls: "warn", missing: ["결과 형식", "성공 기준", "확인 방법"] });
  assert.deepEqual(h.security.reasons, ["secret · HIGH · run.sh:12", "발견 3건"]);
  assert.equal(s.v.skillChecks(SK("b")).security.cls, "bad");
  s.state.data.tools.skill_checks = [{ name: "c", verdict: "SAFE" }, { name: "c", quality_score: 10 }];
  assert.equal(s.v.skillChecks(SK("c")).security.reasons.length, 0);
  s.state.data.tools.skill_checks = [{ name: "d", verdict: "UNKNOWN" }];
  assert.equal(s.v.skillChecks(SK("d")).source, "app");
  assert.equal(s.v.findingText("plain"), "plain");
  assert.equal(s.v.findingText("secret · HIGH · run.sh:12"), "키나 토큰 같은 비밀값이 그대로 적혀 있어요 · run.sh 12번째 줄 · 심각도 높음");
  assert.equal(s.v.findingText("odd thing · info"), "odd thing · 참고");
  assert.deepEqual(s.v.compatOf({}), {});
  assert.equal(s.v.allowed({ skill_compatibility: { codex: "unsupported" } }, "codex"), false);
  assert.equal(s.v.AGENT_SYNC.codex.includes("codex"), true);
});

test("list: attention card, filters, sort, copies, categories and the footer", async () => {
  const skills = [
    SK("alpha", { category: "개발", description: "첫 문장이다. 둘째 문장.", skill_compatibility: { codex: "unsupported" }, updated_at: "2026-10-01" }),
    SK("beta", { source_path: "/x/plugins/beta/SKILL.md", updated_at: "2026-10-03" }),
    SK("beta", { id: "beta2", source_path: "/x/codex/beta/SKILL.md" }),
    SK("gamma", { status: "deprecated" }),
    SK("delta", { source_path: "/repo/tools/delta/SKILL.md" }),
  ];
  const s = await views({ skills, snapshot: { total: 120, cursor: "next", facets: { categories: { dev: 2, "": 1 } } } });
  s.state.data.skillUse = new Map([["alpha", { count: 5, last_used_at: "2026-10-02" }], ["beta", { count: 1, last_used_at: "2026-10-05" }]]);
  s.state.versions.set("v-alpha", { analysis: { problem: "x" }, inspection: { personal_data: true } });
  s.state.data.tools.skill_updates = [{ name: "delta", repo: "~/repo/tools", to: "2.0" }];
  s.deps.pages.prefs.categories = [{ id: "dev", name: "개발" }, { id: "ops", name: "운영" }];
  s.state.skillModel = "!zzz";
  s.v.renderSkills();
  assert.equal(s.state.skillModel, "");
  assert.equal(s.doc.querySelector("#title-count").textContent, "120개");
  const attn = s.doc.querySelector("#controls .v4s-attn");
  assert.match(attn.textContent, /보안 주의 1 · 업데이트 1/);
  btn(attn, "하나씩 확인하기").click();
  assert.deepEqual(s.log.at(-1), ["go", "skills", "caution"]);
  // 필터 ⌄: one button opens grouped chips; counts stay, 0 is hidden; the picked chip is marked active.
  assert.equal(s.doc.querySelectorAll("#controls select").length, 0);
  assert.equal(s.doc.querySelectorAll("#chips .chip").length, 0);
  const bar = s.doc.querySelector("#controls .v4s-bar");
  assert.deepEqual(bar.querySelectorAll(".btn").map((b) => b.textContent), ["새로고침", "설치"]);
  btn(bar, "새로고침").click();
  assert.equal(s.log.at(-1), "loadAll");
  const open = () => { const b = s.doc.querySelector(".v4s-filter-btn"); if (b.getAttribute("aria-expanded") !== "true") b.click(); return s.doc.querySelector(".v4s-pop"); };
  let pop = open();
  const group = (title) => pop.querySelectorAll(".v4s-pop-group").find((g) => g.querySelector("h4").textContent === title);
  assert.deepEqual(pop.querySelectorAll("h4").map((x) => x.textContent), ["카테고리", "상태", "에이전트", "정렬"]);
  assert.deepEqual(group("카테고리").querySelectorAll(".chip").map((c) => c.textContent), ["전체3", "개발2", "분류 안 됨1"]);
  assert.deepEqual(group("상태").querySelectorAll(".chip").map((c) => c.textContent), ["전체", "빠진 곳 있음1", "안 씀2", "보안 주의1", "업데이트 있음1", "플러그인 스킬1", "휴지통1"]);
  assert.equal(group("상태").querySelector(".chip.active").textContent, "전체");
  btn(group("상태"), "휴지통").click();
  assert.deepEqual(s.log.at(-1), ["go", "skills", "trash"]);
  assert.equal(group("정렬").querySelector(".chip.active").textContent, "많이 쓴 순 ↓");
  btn(group("정렬"), "이름 순").click();
  assert.equal(s.state.skillSort, "name");
  assert.equal(s.state.skillDir, 0);
  // 에이전트: one chip per agent with its logo, counting the skills it can use; no toggle until one is picked.
  assert.deepEqual(group("에이전트").querySelectorAll(".chip").map((c) => c.textContent), ["모든 에이전트", "Claude Code4", "Codex3", "Gemini4"]);
  assert.equal(group("에이전트").querySelectorAll(".chip img").length, 3);
  assert.equal(group("에이전트").querySelector(".segment"), null);
  btn(group("에이전트"), "Codex").click();
  assert.equal(s.state.skillModel, "codex");
  s.v.renderSkills(); pop = s.doc.querySelector(".v4s-pop");
  const seg = group("에이전트").querySelector(".segment");
  assert.deepEqual(seg.querySelectorAll(".tab").map((b) => b.textContent), ["쓰는 스킬", "못 쓰는 스킬1"]);
  btn(seg, "못 쓰는 스킬").click();
  assert.equal(s.state.skillModel, "!codex");
  s.v.renderSkills(); pop = s.doc.querySelector(".v4s-pop");
  assert.match(group("에이전트").querySelector(".chip.active").textContent, /Codex/); // the agent stays picked
  btn(group("에이전트").querySelector(".segment"), "쓰는 스킬").click();
  assert.equal(s.state.skillModel, "codex");
  btn(group("에이전트"), "Claude Code").click();
  s.v.renderSkills(); pop = s.doc.querySelector(".v4s-pop");
  assert.equal(group("에이전트").querySelector(".segment"), null); // Claude Code can use every skill: nothing to toggle
  s.state.skillModel = "codex";
  btn(group("카테고리"), "개발").click();
  assert.equal(s.state.skillCat, "dev");
  // A re-render keeps the bubble open and counts what is set; the picked sort flips on a second pick.
  s.v.renderSkills();
  pop = s.doc.querySelector(".v4s-pop");
  assert.ok(pop);
  assert.match(s.doc.querySelector(".v4s-filter-btn").textContent, /필터2/);
  btn(group("정렬"), "이름 순").click();
  assert.equal(s.state.skillDir, -1);
  const rows = s.doc.querySelectorAll("#table .v4s-row");
  assert.equal(rows.length, 4);
  assert.match(rows[0].textContent, /개발첫 문장이다\./);
  assert.match(rows[0].textContent, /보안 주의/);
  assert.match(rows[0].textContent, /5번/);
  assert.match(rows[3].textContent, /업데이트 있음/);
  assert.equal(s.state.selected, null); // nothing opens until a row is clicked (the detail is a popup)
  const copy = btn(rows[1], "같은 이름 2개");
  copy.click();
  assert.ok(s.state.openCopies.has("beta"));
  s.v.renderSkills();
  const opened = s.doc.querySelectorAll("#table .v4s-row");
  assert.equal(opened.length, 5);
  assert.ok(opened[2].classList.contains("copy"));
  assert.match(opened[2].textContent, /\/x\/codex\/beta\/SKILL\.md/);
  btn(opened[1], "같은 이름 2개").click();
  assert.equal(s.state.openCopies.has("beta"), false);
  // Footer: more loads the next server page.
  const foot = s.doc.querySelector("#table .v4s-foot");
  assert.ok(foot);
  await foot.querySelector("button").click();
  assert.ok(s.log.some((x) => x[0] === "more"));
  // The search inside the panel keeps focus and caret across redraws.
  const input = s.doc.querySelector("#controls .v4s-search input");
  input.value = "Be"; input.selectionStart = 1; input.selectionEnd = 2; input.focus();
  await input.fire("input");
  assert.equal(s.state.query, "be");
  s.v.renderSkills();
  const again = s.doc.querySelector("#controls .v4s-search input");
  assert.equal(s.doc.activeElement, again);
  assert.equal(again.selectionEnd, 2);
  // Each sort key orders without error.
  for (const k of ["recent", "updated", "quality", "security", "agents", "unknown"]) { s.state.skillSort = k; s.state.skillDir = 0; s.v.renderSkills(); }
  s.state.openCopies.add("beta");
  s.state.skillSort = "quality"; s.v.renderSkills();
});

test("list: no usage yet, no categories, empty, search empty, failed first page and retry", async () => {
  const s = await views({ skills: [SK("a")], wide: false });
  s.state.data.skillUse = null; s.state.versionsDone = false;
  s.v.renderSkills();
  s.doc.querySelector(".v4s-filter-btn").click();
  assert.match(s.doc.querySelector(".v4s-pop").textContent, /안 씀…/);
  assert.doesNotMatch(s.doc.querySelector(".v4s-pop").textContent, /카테고리/); // no categories: no empty group
  assert.match(s.doc.querySelector("#table").textContent, /…/);
  assert.equal(s.state.selected, null);
  assert.equal(s.doc.querySelector("#chips").children.length, 0);
  s.state.data.skillUse = new Map();
  s.deps.pages.prefs.categories = [{ id: "dev", name: "개발" }];
  s.snap.total = undefined;
  s.v.renderSkills();
  assert.match(s.doc.querySelector("#table").textContent, /안 씀/);
  assert.deepEqual(s.doc.querySelectorAll(".v4s-pop .v4s-pop-group")[0].querySelectorAll(".chip").map((c) => c.textContent), ["전체", "개발"]); // older server: no number rather than 0
  // The empty text waits for a loaded page, so a filter change does not flash it.
  s.snap.items = []; s.v.renderSkills();
  assert.doesNotMatch(s.doc.querySelector("#table").textContent, /조건에 맞는 스킬이 없어요/);
  s.snap.ready = true; s.v.renderSkills();
  assert.match(s.doc.querySelector("#table").textContent, /조건에 맞는 스킬이 없어요/);
  s.state.query = "x"; s.v.renderSkills();
  assert.match(s.doc.querySelector("#table").textContent, /검색과 맞는 스킬이 없어요/);
  s.snap.loading = true; s.snap.items = [SK("a")]; s.v.renderSkills();
  assert.match(s.doc.querySelector("#stats").textContent, /찾는 중/);
  s.snap.loading = false; s.snap.error = "x"; s.v.renderSkills();
  assert.match(s.doc.querySelector("#stats").textContent, /전체 목록을 불러오지 못했어요/);
  s.snap.items = []; s.state.skillTab = "trash"; s.snap.moreFails = true;
  s.v.renderSkills();
  assert.equal(s.doc.querySelector("#title-count").textContent, "");
  assert.deepEqual(s.log.find((x) => x[0] === "ensure" && x[1] === "skillTrash"), ["ensure", "skillTrash"]);
  btn(s.doc.querySelector("#table"), "다시 불러오기").click();
  await flush();
  assert.deepEqual(s.log.filter((x) => x[0] === "more").at(-1), ["more", "skillTrash"]);
  s.state.view = "home";
  btn(s.doc.querySelector("#table"), "다시 불러오기").click();
  await flush();
});

test("staged agent changes: chips, change bar, apply per skill, revert and failures", async () => {
  const skills = [SK("a", { skill_compatibility: { codex: "unsupported" } }), SK("b")];
  const s = await views({ skills, handler: (path) => {
    if (path.startsWith("/v1/skills/a?") || path.startsWith("/v1/skills/b?")) { if (s.state.applyFails) throw new Error("conflict"); return { revision: 4, status: "active", skill_compatibility: { gemini: "supported" } }; }
    return {};
  } });
  s.state.data.skillUse = new Map([["a", { count: 2, last_used_at: "x", by_runtime: { codex: 1, claude_code: 3, odd: 1 } }]]);
  s.state.selected = "skill:a";
  s.v.stage("a", "codex", "allow");
  assert.equal(s.state.pending.get("a|codex"), "allow");
  s.v.stage("a", "codex", "allow");
  assert.equal(s.state.pending.size, 0);
  s.v.stage("a", "claude_code", "allow");
  assert.equal(s.state.pending.size, 0);
  s.v.renderChangeBar();
  assert.equal(s.doc.querySelector("#change-bar").hidden, true);
  await s.v.skillDetailV4(skills[0]);
  const d = s.doc.querySelector(".detail");
  const chips = d.querySelectorAll(".v4s-agent");
  assert.match(chips[1].title, /Codex에서 못 써요 · 누르면 추가 예정 · PC codex/);
  chips[0].click(); chips[1].click();
  s.v.stage("b", "gemini", "block");
  s.v.renderChangeBar();
  assert.equal(s.doc.querySelector("#change-count").textContent, "바뀐 칸 3개");
  assert.equal(s.doc.querySelector("#change-text").textContent, "a → Claude Code에서 빼기 외 2개");
  await s.v.skillDetailV4(skills[0]);
  const d2 = s.doc.querySelectorAll(".detail")[1];
  assert.match(d2.querySelector(".v4s-confirm").textContent, /Codex에 추가, Claude Code에서 빼기할까요/);
  assert.match(d2.querySelectorAll(".v4s-agent")[0].title, /예정을 취소해요/);
  assert.match(d2.textContent, /claude_code|Claude Code:3번/);
  btn(d2.querySelector(".v4s-confirm"), "취소").click();
  assert.equal(s.state.pending.size, 1);
  s.v.renderChangeBar();
  assert.equal(s.doc.querySelector("#change-text").textContent, "b → Gemini에서 빼기");
  s.state.pending.set("zz|codex", "allow");
  s.state.pending.delete("b|gemini");
  s.v.renderChangeBar();
  assert.equal(s.doc.querySelector("#change-text").textContent, "zz → Codex에 추가");
  s.state.pending.clear();
  s.v.stage("a", "codex", "allow");
  s.v.stage("b", "gemini", "block");
  await s.doc.querySelector("#change-apply").click();
  await flush();
  const posts = s.calls.filter((c) => c[0].endsWith("/availability"));
  assert.deepEqual(posts.map((c) => c[1].compatibility), [{ gemini: "supported", codex: "supported" }, { gemini: "unsupported" }]);
  assert.equal(posts[0][1].expected_revision, 4);
  assert.match(s.notices.at(-1), /스킬 2개에 적용했어요/);
  assert.equal(s.state.pending.size, 0);
  s.v.stage("a", "codex", "allow");
  s.state.applyFails = true; s.state.readFails = true;
  await s.doc.querySelector("#change-apply").click();
  await flush();
  assert.match(s.notices.at(-1), /적용하지 못했어요 \(conflict\)/);
  assert.equal(s.state.pending.size, 1);
  s.doc.querySelector("#change-revert").click();
  assert.equal(s.state.pending.size, 0);
  s.state.view = "home"; s.v.stage("a", "codex", "allow"); s.v.renderChangeBar();
  assert.equal(s.doc.querySelector("#change-bar").hidden, true);
});

test("detail: security card, updates, repo check, experts, AI chat, version facts and the late analysis", async () => {
  const sk = SK("a", { description: "설명.", version: "1.0", version_history: [1, 2, 3], active_version: 3, experts: true, category: "개발", source_path: "/repo/a/SKILL.md" });
  const s = await views({ skills: [sk], handler: (path) => (path.startsWith("/v1/skill_versions/") ? s.analysis : {}) });
  s.state.data.skillUse = new Map();
  s.state.data.tools.skill_checks = [{ name: "a", verdict: "DO_NOT_INSTALL", quality_score: 95 }];
  s.state.data.tools.skill_updates = [{ name: "a", repo: "/repo", to: "2.0" }];
  s.state.selected = "skill:a";
  s.analysis = { analysis: { use_when: ["PR 리뷰"], tools: "gh" } };
  await s.v.skillDetailV4(sk);
  const d = s.doc.querySelector(".detail");
  assert.equal(d.heading, "개발");
  assert.match(d.textContent, /보안 위험 · 쓰지 않는 걸 권해요/);
  assert.match(d.textContent, /검사 도구가 구체적인 이유를 보내지 않았어요/);
  assert.match(d.textContent, /품질 — · 95/);
  assert.match(d.textContent, /최근 60일 안 씀/);
  assert.match(d.textContent, /v1\.0 → v2\.0 있음/);
  assert.match(d.textContent, /이럴 때PR 리뷰/);
  assert.ok(d.querySelector(".experts"));
  btn(d, "업데이트 받기").click();
  assert.deepEqual(s.log.at(-1), ["update", 1]);
  // One AI button; its chat footer opens 개선하기.
  const chat = s.chats[0];
  assert.equal(chat.label, "AI");
  assert.match(chat.greeting, /무엇이 궁금하세요\?\n/);
  assert.equal(chat.footer.label, "이 스킬 개선하기");
  chat.footer.onClick();
  assert.deepEqual(s.state.skillPage, { kind: "improve", id: "a", step: 0 });
  // 버전과 기록: 언제 쓰나요 comes before it, history opens step 2, 검사·사용 reveals the folded checks and usage.
  assert.ok(d.textContent.indexOf("언제 쓰나요") < d.textContent.indexOf("버전과 기록"));
  btn(d, "지난 버전 2개 ›").click(); // three in the history, one of them in use
  assert.equal(s.state.skillPage.step, 2);
  const more = d.querySelector(".v4s-more");
  assert.equal(more.hidden, true);
  assert.ok(more.querySelector(".checks") && more.querySelector(".legend"));
  btn(d, "품질·보안 검사, 에이전트별 사용 기록").click();
  assert.equal(more.hidden, false);
  assert.equal(btn(d, "품질·보안 검사").getAttribute("aria-expanded"), "true");
  btn(d, "품질·보안 검사").click();
  assert.equal(more.hidden, true);
  assert.ok(d.querySelector(".v4s-trash"));
  btn(d.querySelector(".v4s-trash"), "휴지통으로").click();
  assert.deepEqual(s.log.at(-1), ["trash", ["a"]]);
  assert.equal(chat.state, s.chats[0].state);
  assert.match((await chat.ask("뭐야?", [])).say, /설명: /);
  assert.match((await chat.ask("또?", [{ who: "me", text: "a" }, { who: "ai", text: "b" }])).say, /설명: /);
  // A repo to check (pending check shows 확인 중), a caution card, no analysis text.
  const r = SK("r", { repo: { repo: "/repo", remote: "git@x", behind: 2, checked_at: "x" }, version: "main" });
  s.state.data.tools.skill_checks = [{ name: "r", verdict: "CAUTION" }];
  s.state.data.tools.requests = [{ kind: "skill_check", name: "/repo", status: "pending" }];
  s.state.selected = "skill:r";
  s.analysis = {};
  await s.v.skillDetailV4(r);
  const d2 = s.doc.querySelectorAll(".detail")[1];
  assert.match(d2.textContent, /보안 주의 · 아래 내용을 확인해 주세요/);
  assert.match(d2.textContent, /main · 새 커밋 2개/);
  assert.equal(btn(d2, "확인 중…").disabled, true);
  assert.match(d2.textContent, /스킬 설명에 쓰는 때가 적혀 있지 않아요/);
  s.state.data.tools.requests = [];
  const latest = SK("l", { repo: { repo: "/r2" }, version: "3" });
  s.state.selected = "skill:l";
  await s.v.skillDetailV4(latest);
  const d3 = s.doc.querySelectorAll(".detail")[2];
  assert.match(d3.textContent, /v3 · 최신/);
  assert.ok(btn(d3, "지난 버전 보기 ›"));
  btn(d3, "업데이트 확인").click();
  assert.deepEqual(s.log.at(-1), ["check", "/r2"]);
  // A detail replaced before its analysis arrives leaves the rows alone.
  s.state.selected = "skill:other";
  await s.v.skillDetailV4(SK("q"));
  assert.match(s.doc.querySelectorAll(".detail")[3].textContent, /불러오는 중/);
  // Without usage data or a version, and no category.
  s.state.data.skillUse = null; s.state.data.tools = null;
  await s.v.skillDetailV4(SK("n", { version: "" }));
  assert.equal(s.doc.querySelectorAll(".detail")[4].heading, "분류 안 됨");
});

test("deprecated detail restores the skill, and restore failures are reported", async () => {
  const sk = SK("a", { status: "deprecated" });
  const s = await views({ skills: [sk] });
  s.keys.trash = [{ id: "a" }, { id: "b" }];
  await s.v.skillDetailV4(sk);
  const d = s.doc.querySelector(".detail");
  assert.match(d.textContent, /휴지통에 있어요/);
  assert.equal(s.chats.at(-1).footer, null);
  assert.equal(btn(d, "휴지통으로"), undefined);
  assert.equal(btn(d, "지난 버전"), undefined);
  btn(d, "되돌리기").click();
  await flush();
  assert.deepEqual(s.keys.trash, [{ id: "b" }]);
  assert.match(s.notices.at(-1), /a를 되돌렸어요/);
  s.state.restoreFails = true;
  btn(d, "되돌리기").click();
  await flush();
  assert.match(s.notices.at(-1), /되돌리지 못했어요 \(locked\)/);
});

test("improve pages walk the four steps and their side text", async () => {
  const sk = SK("a", { version: "1" });
  const s = await views({ skills: [sk] });
  s.doc.querySelector("#table").scrollTop = 99;
  s.v.openSkillPage({ kind: "improve", id: "a", step: 0 });
  assert.equal(s.state.selected, "skill:a");
  assert.equal(s.doc.querySelector("#table").scrollTop, 0);
  s.v.renderSkills();
  assert.ok(s.doc.querySelector("#table .generate"));
  assert.equal(s.doc.querySelector("#table .generate").open, true);
  const steps = s.doc.querySelectorAll("#table .v4s-steps button");
  assert.equal(steps[0].getAttribute("aria-current"), "step");
  steps[0].click();
  assert.equal(s.state.skillPage.step, 0);
  steps[2].click();
  assert.equal(s.state.skillPage.step, 2);
  s.v.renderSkills();
  assert.ok(s.doc.querySelector("#table .review-review"));
  assert.match(s.doc.querySelector("#table .v4s-steps").textContent, /✓/);
  btn(s.doc.querySelector("#table .v4s-actions"), "← 비교 평가").click();
  assert.equal(s.state.skillPage.step, 1);
  s.v.renderSkills();
  btn(s.doc.querySelector("#table .v4s-actions"), "다음: 검토·승인").click();
  assert.equal(s.state.skillPage.step, 2);
  s.state.skillPage = { kind: "improve", id: "a", step: 3 };
  s.v.renderSkills();
  assert.equal(btn(s.doc.querySelector("#table .v4s-actions"), "다음"), undefined);
  // The same page key keeps the cached page (forms survive a refresh).
  const before = s.doc.querySelector("#table").firstChild;
  s.v.renderSkills();
  assert.equal(s.doc.querySelector("#table").firstChild, before);
  s.doc.querySelector(".page-back").click();
  assert.deepEqual(s.log.at(-1), ["go", "skills"]);
  for (const step of [0, 1, 2, 3]) { s.state.skillPage = { kind: "improve", id: "a", step }; s.v.skillPageDetail(); }
  assert.match(s.doc.querySelectorAll(".detail").at(-1).textContent, /정한 기간만 시험해요/);
  s.state.skillPage = { kind: "improve", id: "gone", step: 0 };
  s.v.skillPageDetail();
  s.v.renderSkills();
  assert.equal(s.state.skillPage, null);
  s.v.openSkillPage({ kind: "install" });
  assert.equal(s.state.selected, null);
});

test("install page: the catalog install form in the list, its guide in the side", async () => {
  const s = await views({ skills: [SK("a")] });
  s.state.skillPage = { kind: "install" };
  s.v.renderSkills();
  assert.ok(s.doc.querySelector("#table .install"));
  assert.equal(s.doc.querySelector("#table .install").alive(), true);
  s.v.skillPageDetail();
  assert.match(s.doc.querySelector(".detail").textContent, /설치 안내/);
});

// The 고르기 시험 (route) and 휴지통·중복 (trash) pages were removed from the skill view in e3f633a (#224); the list's 휴지통 tab is covered above.
