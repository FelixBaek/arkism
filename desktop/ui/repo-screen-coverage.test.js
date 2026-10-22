// Git screen, code graph, gallery delete, SSH trust and gateway token: behavior through a tiny DOM stand-in.
const test = require("node:test");
const assert = require("node:assert/strict");

// ------------------------------------------------------------ DOM stand-in
class Text { constructor(s) { this.nodeType = 3; this.textContent = String(s); this.childNodes = []; } }
class El {
  constructor(tag) {
    this.tagName = tag.toUpperCase(); this.childNodes = []; this.parentNode = null; this.listeners = {}; this.attrs = {}; this.style = {};
    this.className = ""; this._text = ""; this.value = ""; this.checked = false; this.disabled = false; this.hidden = false; this.open = false;
    const self = this;
    this.classList = {
      list: () => self.className.split(/\s+/).filter(Boolean),
      add(...c) { self.className = [...new Set([...this.list(), ...c])].join(" "); },
      remove(c) { self.className = this.list().filter((x) => x !== c).join(" "); },
      toggle(c, on) { if (on === undefined ? !this.contains(c) : on) this.add(c); else this.remove(c); },
      contains: (c) => this.classList.list().includes(c),
    };
  }
  append(...xs) { for (const x of xs) { const n = typeof x === "object" && x ? x : new Text(x); if (n.parentNode) n.remove(); n.parentNode = this; this.childNodes.push(n); } }
  prepend(...xs) { const old = this.childNodes; this.childNodes = []; this.append(...xs); this.childNodes.push(...old); }
  replaceChildren(...xs) { this.childNodes.forEach((c) => { c.parentNode = null; }); this.childNodes = []; this.append(...xs); }
  remove() { if (this.parentNode) { this.parentNode.childNodes = this.parentNode.childNodes.filter((c) => c !== this); this.parentNode = null; } }
  get firstChild() { return this.childNodes[0] || null; }
  get textContent() { return this._text + this.childNodes.map((c) => c.textContent).join(""); }
  set textContent(v) { this._text = String(v); this.childNodes = []; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === document.body; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k]; }
  addEventListener(t, f) { (this.listeners[t] ||= []).push(f); }
  dispatch(type, extra = {}) {
    const ev = { type, target: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {}, ...extra };
    (this.listeners[type] || []).slice().forEach((f) => f(ev));
    return ev;
  }
  click() { return this.dispatch("click"); }
  focus() { document.activeElement = this; }
  showModal() { this.open = true; }
  close() { if (this.open) { this.open = false; this.dispatch("close"); } }
  scrollIntoView() {}
  setSelectionRange() {}
}
globalThis.window = globalThis;
globalThis.document = {
  body: new El("body"), activeElement: null,
  createElement: (t) => new El(t), createElementNS: (_, t) => new El(t),
  querySelector: (q) => (q === ".cg-impact" ? new El("section") : null),
};
const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => { if (k === "boom") throw new Error("full"); store.set(k, v); } };
globalThis.requestAnimationFrame = (f) => f();
const opened = [];
window.open = (u) => opened.push(u);
const clipboard = { ok: true, text: [] };
Object.defineProperty(globalThis, "navigator", { configurable: true, value: { clipboard: { writeText: (t) => { clipboard.text.push(t); return clipboard.ok ? Promise.resolve() : Promise.reject(new Error("no")); } } } });

const el = (tag, cls = "", text) => { const n = new El(tag); n.className = cls || ""; if (text != null) n.textContent = text; return n; };
const button = (label, cls = "", _icon, onClick) => { const b = el("button", cls, label); if (onClick) b.addEventListener("click", onClick); return b; };
const all = (n, out = []) => { out.push(n); n.childNodes.forEach((c) => all(c, out)); return out; };
const els = (root) => all(root).filter((n) => n instanceof El);
const btn = (root, text) => els(root).filter((n) => n.tagName === "BUTTON" && n.textContent.includes(text)).pop();
const has = (root, text) => root.textContent.includes(text);
const byTitle = (root, title) => els(root).find((n) => n.tagName === "BUTTON" && n.title === title);
const tick = async (n = 8) => { for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r)); };
const change = (node, value) => { node.value = value; node.dispatch("change"); };

require("./git.js");
require("./codegraph.js");
require("./gallery-delete.js");
require("./ssh-trust.js");
require("./gateway-token.js");

// ------------------------------------------------------------ Git
const sha = (c) => c.repeat(40);
const commits = () => [
  { sha: sha("m"), parents: [sha("a"), sha("b")], subject: "merge", author: "fx", time: 1 },
  { sha: sha("a"), parents: [], subject: "root a", author: "", time: 1 },
  { sha: sha("y"), parents: [sha("b")], subject: "y", author: "fx", time: 1 },
  { sha: sha("b"), parents: [], subject: "root b", author: "fx", time: 1 },
];
const graph = (more = true) => ({ repo: { default_branch: "main" }, more, commits: commits(), refs: [
  { kind: "branch", name: "main", sha: sha("m") }, { kind: "branch", name: "feat/a", sha: sha("m") }, { kind: "branch", name: "feat/b", sha: sha("m") },
  { kind: "branch", name: "zeta/x", sha: sha("y") }, { kind: "branch", name: "lonely", sha: sha("y") }, { kind: "tag", name: "v1", sha: sha("b") }] });
const pulls = [
  { number: 7, title: "Seven", state: "open", head: "feat/a", base: "main", checks: "pending", mine: true, author: "fx", updated_at: "t" },
  { number: 8, title: "Eight", state: "merged", head: "x", checks: "success" },
  { number: 9, title: "Nine", state: "open", head: "zeta/x", base: "main", checks: "failure", assigned: true, review_requested: true },
];
const runs = [
  { id: 1, workflow: "ci", branch: "feat/a", status: "completed", conclusion: "failure", sha: sha("h"), title: "CI", url: "u1", created_at: "t" },
  { id: 2, workflow: "ci", branch: "main", status: "completed", conclusion: "success", sha: sha("m") },
  { id: 3, workflow: "lint", branch: "main", status: "in_progress", sha: sha("m"), url: "u3" },
];
const commitDetail = (s = sha("m")) => ({ sha: s, message: "subject line\n\nbody text", author: "fx", time: 1, parents: [sha("a")],
  branches: ["a", "b", "c", "d", "e", "f", "g"], files: [{ path: "a.go", added: 1, deleted: 2 }, { path: "b.bin", old_path: "old.bin" }], more_files: 3 });
const prDetailData = (extra = {}) => ({ number: 7, title: "Seven", state: "open", draft: false, mergeable: "mergeable", head: "feat/a", base: "main", head_sha: sha("h"),
  check_summary: "pending", author: "fx", additions: 1, deletions: 2, changed_files: 1, commits: 1, review: "changes_requested", updated_at: "t", url: "https://pr/7",
  body: "desc", checks: [{ name: "ci", state: "success" }, { name: "lint", state: "error" }, { name: "x", state: "queued" }],
  reviews: [{ author: "r", state: "APPROVED", body: "ok" }, { author: "q", state: "COMMENTED" }], files: [{ path: "a.go", added: 1, deleted: 0 }], ...extra });

function gitWorld(opts = {}) {
  document.body.replaceChildren(); store.clear();
  const nodes = {}; for (const id of ["table", "detail", "title", "stats", "head"]) { nodes[id] = el("div"); document.body.append(nodes[id]); }
  const log = { notice: [], go: [], review: [], calls: [] };
  const replies = {
    repos: () => ({ repos: [{ name: "old", archived: true }, { name: "arkism", url: "https://github.com/o/arkism" }] }),
    graph: (r) => (r.skip ? { commits: [{ sha: sha("z"), parents: [], subject: "z", author: "fx", time: 1 }], more: false } : graph()),
    prs: () => ({ pulls }), runs: () => ({ runs }),
    commit: (r) => commitDetail(r.sha), pr: (r) => prDetailData({ number: r.number }),
    diff: (r) => ({ path: r.path, patch: "diff --git a b\nindex 1\n--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n ctx", truncated: true }),
    branch_create: () => ({ created: "feat/new" }), pr_create: () => ({ url: "https://pr/new" }), pr_merge: () => ({}), branch_delete: () => ({}),
    revert: () => ({ url: "u", branch: "revert-1" }), cherry_pick: () => ({ branch: "feat/a" }), rerun: () => ({}),
    ...opts.replies,
  };
  const tauri = opts.noTauri ? null : { invoke: async (cmd, args) => {
    assert.equal(cmd, "git_op");
    log.calls.push(args.request);
    const f = replies[args.request.action];
    const out = typeof f === "function" ? await f(args.request) : f;
    return out && out.raw ? out.raw : { authority: "data_only_untrusted", ...out };
  } };
  const state = { defaults: { ssh_host: "host" }, view: "git", mode: "server", caps: { role: "human" },
    data: { reviews: { items: [{ number: 7, repo: "o/arkism", findings: [{ severity: "blocking", path: "a.go", line: 3, text: "bad" }, { severity: "note", path: "b.go", line: 1, text: "meh" }] }] } }, ...opts.state };
  const codegraph = opts.codegraph === false ? null : window.ArkismCodeGraph({ state: { scope: {} }, el, raw: (x) => x, button, query: () => "q=1", notice: (m) => log.notice.push(m), ago: () => "now",
    request: opts.cgRequest || (async () => ({ repos: [] })) });
  const git = window.ArkismGit({ state, $: (q) => nodes[q.slice(1)], el, button, tauri, ago: () => "now", notice: (m) => log.notice.push(m), go: (v) => log.go.push(v),
    headExtras: (o) => { nodes.head.replaceChildren(...(o?.aside || []), ...(o?.back ? [button(o.back[0], "back", null, o.back[1])] : [])); },
    openReview: (r) => log.review.push(r), codegraph });
  const render = () => git.views.git.render();
  return { git, nodes, log, state, render, replies, body: document.body };
}
const lastDialog = () => els(document.body).filter((n) => n.tagName === "DIALOG").pop();
const submit = async (fill = {}) => {
  const d = lastDialog();
  for (const [k, v] of Object.entries(fill)) { const i = els(d).filter((n) => ["INPUT", "SELECT", "TEXTAREA"].includes(n.tagName))[k]; if (typeof v === "boolean") i.checked = v; else i.value = v; }
  els(d).filter((n) => n.tagName === "BUTTON").pop().click();
  await tick();
};

test("git: plain messages without desktop, server, or human account", () => {
  let w = gitWorld({ noTauri: true }); w.render(); assert.ok(has(w.nodes.table, "데스크톱 앱"));
  w = gitWorld({ state: { mode: "local" } }); w.render(); assert.ok(has(w.nodes.table, "서버에 연결했을 때"));
  w = gitWorld({ state: { caps: { role: "service" } } }); w.render(); assert.ok(has(w.nodes.table, "관리 계정"));
  assert.equal(w.git.views.git.title(), "저장소");
  assert.deepEqual(w.git.views.git.head(), []);
});

test("git: loads repos, graph, PRs and checks; side lists, lanes and tabs work", async () => {
  store.set("arkism.gitRepo", "missing");
  const w = gitWorld({ codegraph: false });
  w.render(); assert.ok(has(w.nodes.table, "저장소를 불러오는 중"));
  await tick();
  w.render();
  assert.ok(has(w.nodes.table, "merge") && has(w.nodes.table, "+1"), "graph rows with ref overflow");
  assert.ok(has(w.nodes.table, "feat/") && has(w.nodes.table, "#7") && has(w.nodes.table, "기본"));
  assert.equal(w.nodes.title.textContent, "저장소");
  assert.match(w.git.views.git.sub(), /한눈에/);
  // section toggles and the tags block
  btn(w.nodes.table, "태그").click(); assert.ok(has(w.nodes.table, "v1"));
  btn(w.nodes.table, "v1").click(); await tick();
  assert.ok(has(w.nodes.detail, "subject line"));
  btn(w.nodes.table, "브랜치").click(); btn(w.nodes.table, "브랜치").click();
  // PR tabs inside the side list
  for (const t of ["내 PR", "나에게 배정", "내 리뷰 대기", "열린 PR"]) btn(w.nodes.table, t).click();
  w.render();
  // more commits
  btn(w.nodes.table, "커밋 더 보기").click(); await tick();
  assert.ok(has(w.nodes.table, "z"));
  // search reloads
  const search = els(w.nodes.table).find((n) => n.tagName === "INPUT");
  w.replies.graph = () => ({ ...graph(false), commits: [] });
  search.value = "nothing"; search.dispatch("keydown", { key: "Enter" }); search.dispatch("keydown", { key: "a" });
  await tick(); w.render();
  assert.ok(has(w.nodes.table, "검색 결과가 없어요"));
  btn(w.nodes.table, "코드 리뷰").click(); assert.deepEqual(w.log.go, ["reviews"]);
});

test("git: commit detail, diff, branch and commit actions", async () => {
  const w = gitWorld({ codegraph: false });
  w.render(); await tick(); w.render();
  // commit row
  els(w.nodes.table).find((n) => n.tagName === "BUTTON" && n.className.startsWith("git-row") && has(n, "root a")).click();
  w.render(); await tick();
  assert.ok(has(w.nodes.detail, "body text") && has(w.nodes.detail, "…"));
  btn(w.nodes.detail, "a.go").click(); assert.ok(has(w.nodes.detail, "변경을 불러오는 중"));
  await tick();
  assert.ok(has(w.nodes.detail, "+new") && has(w.nodes.detail, "앞부분만"));
  // new branch from the commit
  btn(w.nodes.detail, "여기서 새 브랜치").click(); await submit({ 0: "feat/new" });
  assert.ok(w.log.notice.some((m) => m.includes("feat/new")));
  // revert: default value is the first containing branch "a", which is not a known branch
  btn(w.nodes.detail, "되돌리기").click(); await submit();
  assert.ok(w.log.notice.includes("브랜치를 찾지 못했어요."));
  btn(w.nodes.detail, "되돌리기").click(); await submit({ 0: "main" });
  assert.ok(w.log.notice.some((m) => m.includes("revert-1")));
  btn(w.nodes.detail, "다른 브랜치로 가져오기").click(); await submit({ 0: "feat/a" });
  assert.ok(w.log.notice.includes("feat/a에 올렸어요."));
  // a dialog can be cancelled
  btn(w.nodes.detail, "되돌리기").click(); btn(lastDialog(), "취소").click(); assert.equal(lastDialog(), undefined);
  // branch detail: with PR, default branch, other branch
  w.render();
  byTitle(w.nodes.table, "feat/a").click(); await tick(); // feat/a → shows its PR
  w.render();
  assert.ok(has(w.nodes.detail, "PR #7 보기"));
  btn(w.nodes.detail, "PR #7 보기").click(); await tick();
  assert.equal(w.git.views.git.title(), "PR 상세");
  w.git.reset(); w.render(); await tick(); w.render();
  byTitle(w.nodes.table, "lonely").click(); await tick(); w.render();
  btn(w.nodes.detail, "PR 만들기").click(); await submit({ 2: "title", 3: "body", 4: true });
  assert.ok(w.log.notice.some((m) => m.includes("https://pr/new")));
  btn(w.nodes.detail, "여기서 새 브랜치").click(); await submit({ 0: "x" });
  btn(w.nodes.detail, "브랜치 지우기").click(); await submit();
  assert.ok(w.log.notice.includes("lonely을(를) 지웠어요."));
  byTitle(w.nodes.table, "main").click(); await tick(); w.render();
  assert.ok(!btn(w.nodes.detail, "브랜치 지우기"));
  // head buttons
  btn(w.nodes.head, "새 브랜치").click(); await submit({ 0: "y" });
  btn(w.nodes.head, "PR 만들기").click(); btn(lastDialog(), "취소").click();
  btn(w.nodes.head, "가져오기").click(); await tick();
  const pick = els(w.nodes.head).find((n) => n.tagName === "SELECT");
  change(pick, "old"); await tick();
  assert.equal(store.get("arkism.gitRepo"), "old");
});

test("git: PR and run detail, merge, rerun, PR page and errors", async () => {
  const w = gitWorld({ codegraph: false });
  w.render(); await tick(); w.render();
  btn(w.nodes.table, "Seven").click(); await tick(); w.render();
  assert.ok(has(w.nodes.detail, "심각") && has(w.nodes.detail, "권고") && has(w.nodes.detail, "수정 요청"));
  btn(w.nodes.detail, "코드 리뷰에서 자세히").click(); assert.deepEqual(w.log.review, ["review:o/arkism#7"]);
  btn(w.nodes.detail, "GitHub에서 열기").click(); assert.ok(opened.includes("https://pr/7"));
  btn(w.nodes.detail, "병합하기").click(); await submit({ 0: "squash" });
  assert.ok(w.log.notice.includes("PR #7을(를) 병합했어요."));
  w.render();
  btn(w.nodes.detail, "실패한 검사 다시").click(); await tick();
  assert.ok(w.log.notice.includes("실패한 검사를 다시 돌려요."));
  // other PR states render their labels
  for (const extra of [{ state: "merged" }, { state: "closed", body: "" }, { draft: true, mergeable: "conflicting", review: "approved" }, { review: "x", mergeable: "weird", check_summary: "zzz" }]) {
    w.replies.pr = () => prDetailData(extra);
    btn(w.nodes.table, "Nine").click(); await tick(); w.render();
  }
  // run detail
  btn(w.nodes.table, "ci").click(); await tick(); w.render();
  assert.ok(has(w.nodes.detail, "failure"));
  btn(w.nodes.detail, "실패한 검사 다시").click(); await tick();
  w.render(); btn(w.nodes.detail, "GitHub에서 열기").click();
  btn(w.nodes.table, "lint").click(); await tick(); w.render();
  assert.ok(has(w.nodes.detail, "진행 중"));
  // PR page (mode prs)
  w.replies.pr = () => prDetailData({ checks: [], reviews: [] });
  btn(w.nodes.table, "PR 상세").click(); await tick(); w.render();
  assert.match(w.git.views.git.sub(), /병합해요/);
  const search = els(w.nodes.table).find((n) => n.tagName === "INPUT");
  search.value = "#9"; search.selectionStart = 2; search.dispatch("input");
  assert.ok(has(w.nodes.table, "Nine") && !has(w.nodes.table, "Seven"));
  search.value = "nomatch"; search.dispatch("input");
  assert.ok(has(w.nodes.table, "해당하는 PR이 없어요"));
  els(w.nodes.table).find((n) => n.tagName === "INPUT").value = ""; els(w.nodes.table).find((n) => n.tagName === "INPUT").dispatch("input");
  btn(w.nodes.table, "Nine").click(); await tick(); w.render();
  btn(w.nodes.table, "전체").click();
  btn(w.nodes.head, "저장소").click();
  assert.equal(w.git.views.git.title(), "저장소");
  // PR 상세 with a PR already selected just repaints
  btn(w.nodes.table, "PR 상세").click(); w.render();
  // action failure keeps the screen and reports
  w.replies.pr_merge = () => { throw new Error("git_conflict"); };
  btn(w.nodes.head, "저장소").click();
  btn(w.nodes.table, "Seven").click(); await tick(); w.render();
  btn(w.nodes.detail, "병합하기").click(); await submit();
  assert.ok(w.log.notice.includes("충돌이 나서 아무것도 올리지 않았어요."));
});

test("git: failures, busy guard, reconnect and lane edge cases", async () => {
  const w = gitWorld({ codegraph: false, replies: { repos: () => { throw new Error("ssh failed"); } } });
  w.render(); await tick(); w.render();
  assert.ok(has(w.nodes.table, "서버에 연결하지 못했어요"));
  w.replies.repos = () => ({ raw: { authority: "other" } });
  btn(w.nodes.table, "다시 연결").click(); await tick(); w.render();
  assert.ok(has(w.nodes.table, "git_unavailable"));
  w.replies.repos = () => { throw new Error("git_weird"); };
  btn(w.nodes.table, "다시 연결").click(); await tick(); w.render();
  assert.ok(has(w.nodes.table, "(git_weird)"));
  // empty repo list
  w.replies.repos = () => ({ repos: [] });
  els(w.nodes.table).find((n) => n.tagName === "INPUT").value = "";
  btn(w.nodes.table, "다시 연결").click(); await tick(); w.render();
  assert.equal(w.nodes.table.childNodes.length, 0);
  // prs and runs fail, graph detail fails, diff fails, more fails
  const v = gitWorld({ codegraph: false, replies: { prs: () => { throw new Error("x"); }, runs: () => { throw new Error("x"); },
    commit: () => { throw new Error("git_timeout"); } } });
  v.render(); await tick(); v.render();
  assert.ok(has(v.nodes.table, "PR을 불러오지 못했어요") && has(v.nodes.table, "검사 기록을 불러오지 못했어요"));
  btn(v.nodes.table, "y").click(); assert.ok(has(v.nodes.detail, "불러오는 중"));
  await tick(); assert.ok(has(v.nodes.detail, "시간이 너무 걸려"));
  v.replies.graph = () => { throw new Error("x"); };
  btn(v.nodes.table, "커밋 더 보기").click(); await tick();
  assert.ok(v.log.notice.length);
  v.replies.commit = (r) => commitDetail(r.sha); v.replies.diff = () => { throw new Error("x"); };
  btn(v.nodes.table, "y").click(); await tick(); v.render();
  btn(v.nodes.detail, "a.go").click(); await tick(); assert.ok(has(v.nodes.detail, "서버에 연결하지 못했어요"));
  // PR page while PRs failed / still loading
  btn(v.nodes.table, "PR 상세").click(); v.render(); assert.ok(has(v.nodes.table, "PR을 불러오지 못했어요"));
  // stale replies after reset are dropped; busy act is ignored
  let release;
  const u = gitWorld({ codegraph: false, replies: { prs: () => new Promise((r) => { release = r; }) } });
  u.render(); await tick(); u.render();
  assert.ok(has(u.nodes.table, "PR을 불러오는 중"));
  u.git.reset(); release({ pulls }); await tick();
  u.state.view = "other"; u.render(); await tick();
  // repo list arrives after a reset: dropped
  let rel2;
  const t = gitWorld({ codegraph: false, replies: { repos: () => new Promise((r) => { rel2 = r; }), graph: () => new Promise(() => {}) } });
  t.render(); await tick(); t.git.reset(); rel2({ repos: [{ name: "r" }] }); await tick();
  // graph resolves after reset
  let rel3;
  const s = gitWorld({ codegraph: false, replies: { graph: () => new Promise((r) => { rel3 = r; }) } });
  s.render(); await tick(); s.render(); assert.ok(has(s.nodes.table, "불러오는 중"));
  s.git.reset(); rel3(graph()); await tick();
  // more() result after a reset is dropped
  const q = gitWorld({ codegraph: false });
  q.render(); await tick(); q.render();
  let rel4; q.replies.graph = () => new Promise((r) => { rel4 = r; });
  btn(q.nodes.table, "커밋 더 보기").click(); await tick(); q.git.reset(); rel4({ commits: [] }); await tick();
  // busy: second action ignored while first runs
  const p = gitWorld({ codegraph: false });
  p.render(); await tick(); p.render();
  let rel5; p.replies.rerun = () => new Promise((r) => { rel5 = r; });
  btn(p.nodes.table, "ci").click(); await tick(); p.render();
  btn(p.nodes.detail, "실패한 검사 다시").click(); btn(p.nodes.detail, "실패한 검사 다시").click();
  rel5({}); await tick();
  assert.equal(p.log.calls.filter((c) => c.action === "rerun").length, 1);
  // localStorage that throws on read is tolerated
  const real = localStorage.getItem; localStorage.getItem = () => { throw new Error("no"); };
  gitWorld({ codegraph: false }); localStorage.getItem = real;
});

test("git: code graph modes are drawn by codegraph.js", async () => {
  const w = gitWorld({ cgRequest: async () => ({ repos: [{ repo: "o/arkism", commit: "abc", stats: {} }] }) });
  w.render(); await tick(); w.render();
  btn(w.nodes.table, "코드 그래프").click(); await tick(); w.render();
  assert.match(w.git.views.git.sub(), /함수와 메서드/);
  assert.ok(els(w.nodes.table).some((n) => n.className.includes("cg-status")));
  btn(w.nodes.table, "PR 영향").click(); await tick(); w.render();
  btn(w.nodes.table, "커밋").click();
  assert.equal(w.git.views.git.title(), "저장소");
});

// ------------------------------------------------------------ Code graph (standalone host)
const sym = (id, extra = {}) => ({ id, qname: `pkg.${id}`, name: id, kind: "func", loc: `internal/svc/${id}.go:10`, ...extra });
function cgWorld(opts = {}) {
  const log = { notice: [], posts: [], review: [], mode: [], paints: 0 };
  const handlers = {
    repos: () => ({ repos: [{ repo: "o/arkism", commit: "abc1234", symbols: 10, files: 2, edges: 3, indexed_at: "t",
      stats: { go_files: 1, js_files: 1, mode: "incremental", duration_ms: 1500, type_errors: 2, by_kind: { func: 3, type: 1 } } }, { repo: "o/other", commit: "x" }] }),
    search: () => ({ items: [sym("b", { exported: true }), sym("a", { kind: "type", loc: "web/a.ts:3" }), sym("c", { loc: "c.js:1" })], truncated: true, reason: "limit" }),
    callers: (b) => (b.symbol === "dup" ? { candidates: [sym("a"), sym("b")] } : { symbol: sym(b.symbol, { signature: "func x()" }), items: [sym("c1", { at: "x.go:1", via: "interface" }), sym("c2"), sym("c3"), sym("c4")], truncated: true }),
    callees: () => ({ items: [] }),
    impact: (b) => ({ items: b.symbol === "lonely" ? [] : [sym("i1", { depth: 1 }), sym("i2", { depth: 2 }), sym("i3", { depth: 2 }), sym("i4", { depth: 2 }), sym("i5", { depth: 2 }), sym("i6")], truncated: true, reason: b.depth > 7 ? "depth_cap" : "node_cap" }),
    changed: () => ({ commit: "old", depth: 2, truncated: true, reason: "x", items: [
      { symbol: sym("f1", { loc: "a.go:2" }), end_line: 4, impact_total: 12, impact: [sym("i1", { depth: 1 }), sym("i2", { depth: 2 })], truncated: true, reason: "limit" },
      { symbol: sym("f2", { loc: "b.ts:1", kind: "weird" }), impact_total: 0, impact: [] }] }),
    ...opts.handlers,
  };
  const request = async (path, o) => {
    const op = path.split("/v1/code_graph/")[1].split("?")[0];
    const body = o?.body ? JSON.parse(o.body) : {};
    log.posts.push([op, body]);
    return handlers[op](body);
  };
  const cg = window.ArkismCodeGraph({ state: { scope: { p: 1 } }, el, raw: (x) => x, button, request, query: () => "q=1", notice: (m) => log.notice.push(m), ago: () => "now" });
  const gitCalls = {
    pr: () => ({ number: 7, title: "Seven", head: "feat", base: "main", commits: 2, changed_files: 3, head_sha: "new",
      files: [{ path: "a.go" }, { path: "a_test.go" }, { path: "b.ts" }, { path: "README.md" }, { path: "bad.js" }] }),
    diff: (r) => (r.path === "bad.js" ? Promise.reject(new Error("x")) : { patch: r.path === "a.go"
      ? "--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,4 @@\n ctx\n+add2\n-del\n+add3\n\\ No newline\n ctx\n@@ -9 +10 @@\n+far"
      : "@@ -0,0 +1 @@\n+x" }),
    ...opts.gitCalls,
  };
  const host = { paint: () => { log.paints++; }, call: async (a, r) => gitCalls[a](r), openReview: (r) => log.review.push(r), gitRepo: "arkism",
    repoUrl: "https://github.com/o/arkism/", headSha: "fff", prs: () => opts.prs ?? [{ number: 7, state: "open", title: "Seven" }, { number: 8, state: "closed" }],
    commitsSince: () => opts.behind ?? 4, setMode: (m) => log.mode.push(m), ...opts.host };
  const draw = (mode) => { const out = cg.render(mode, host); const root = el("div"); root.append(...out.parts, out.detail); return root; };
  return { cg, log, host, draw, handlers, gitCalls };
}

test("codegraph: index status, search, filters and symbol detail", async () => {
  const w = cgWorld();
  let r = w.draw("code"); assert.ok(has(r, "코드 그래프를 불러오는 중"));
  await tick();
  r = w.draw("code");
  assert.ok(has(r, "색인이 오래됐어요") && has(r, "새 커밋 4개") && has(r, "타입 오류") && has(r, "1.5초") && has(r, "바뀐 파일만 색인"));
  assert.ok(has(r, "함수·메서드·타입 이름을 찾아보세요"));
  btn(r, "다시 색인").click(); await tick();
  assert.equal(clipboard.text.pop(), "arkism codegraph index --mirror arkism --repo o/arkism");
  r = w.draw("code"); assert.ok(has(r, "앱에서는 색인을 돌리지 않아요"));
  btn(r, "복사").click(); clipboard.ok = false; btn(r, "복사").click(); await tick(); clipboard.ok = true;
  assert.ok(w.log.notice.includes("복사했어요.") && w.log.notice.some((m) => m.includes("복사하지 못했어요")));
  // search via input debounce and Enter
  const input = () => els(r).find((n) => n.tagName === "INPUT");
  input().value = "pkg"; input().dispatch("input");
  await new Promise((res) => setTimeout(res, 300)); await tick();
  r = w.draw("code");
  assert.ok(has(r, "공개") && has(r, "개수 한도에서 멈춤") && has(r, "3개 중 3개"));
  const [folder, sort] = els(r).filter((n) => n.tagName === "SELECT");
  change(sort, "name"); r = w.draw("code"); change(els(r).filter((n) => n.tagName === "SELECT")[1], "path"); r = w.draw("code");
  change(folder, "internal/svc"); r = w.draw("code"); assert.ok(has(r, "3개 중 1개"));
  btn(r, "타입").click(); r = w.draw("code"); assert.ok(has(r, "이 종류·폴더에 맞는 결과가 없어요"));
  btn(r, "전체").click(); change(els(w.draw("code")).filter((n) => n.tagName === "SELECT")[0], "");
  r = w.draw("code");
  // open a symbol
  btn(r, "pkg.b").click(); r = w.draw("code"); assert.ok(has(r, "불러오는 중"));
  await tick(); r = w.draw("code");
  assert.ok(has(r, "이 함수를 바꾸면 6곳") && has(r, "일부만 셌어요") && has(r, "인터페이스 경유") && has(r, "x.go:1에서") && has(r, "func x()"));
  assert.ok(has(r, "300개에서 멈춤") && has(r, "외 1") && has(r, "4곳 모두 보기") && has(r, "부르는 것이 없어요"));
  btn(r, "4곳 모두 보기").click(); r = w.draw("code");
  btn(r, "이름 모두 보기").click(); r = w.draw("code");
  btn(r, "영향 받는 곳 보기").click();
  btn(r, "코드 열기").click(); assert.ok(opened.pop().startsWith("https://github.com/o/arkism/blob/abc1234/internal/svc/b.go#L10"));
  r = w.draw("code");
  btn(r, "pkg.i2").click(); await tick(); r = w.draw("code");
  btn(r, "pkg.c1").click(); await tick(); r = w.draw("code");
  // depth change, including depth 8 (no partial note) and a failure
  change(els(r).find((n) => n.className.includes("cg-depth-pick")), "8"); await tick(); r = w.draw("code");
  w.handlers.impact = () => { throw new Error("boom"); };
  change(els(r).find((n) => n.className.includes("cg-depth-pick")), "2"); await tick();
  assert.ok(w.log.notice.includes("boom"));
  // a symbol with no impact and an unknown kind
  w.handlers.impact = () => ({ items: [] });
  w.handlers.callers = (b) => ({ symbol: sym(b.symbol, { kind: "macro", loc: "x.ts:1" }), items: [] });
  input().value = "lonely"; input().dispatch("keydown", { key: "Enter" }); input().dispatch("keydown", { key: "x" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "부르는 곳이 없어요") && has(r, "영향 받는 곳이 없어요") && has(r, "TypeScript"));
  // candidates
  w.handlers.callers = () => ({ candidates: [sym("a"), sym("b")] });
  input().value = "dup"; input().dispatch("keydown", { key: "Enter" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "어느 심볼인가요"));
  w.handlers.callers = () => ({});
  btn(r, "pkg.a").click(); await tick();
  // not found by name: back to empty; by id: message; other error
  w.handlers.callers = () => { throw new Error("HTTP 404"); };
  input().value = "ghost"; input().dispatch("keydown", { key: "Enter" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "함수·메서드를 고르면"));
  input().value = "0123456789abcdef"; input().dispatch("keydown", { key: "Enter" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "색인에 없어요"));
  w.handlers.callers = () => { throw new Error("weird"); };
  input().value = "w"; input().dispatch("keydown", { key: "Enter" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "weird"));
  // search error and empty query, and an empty result
  w.handlers.search = () => { throw new Error("search down"); };
  input().value = "q"; input().dispatch("keydown", { key: "Enter" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "search down"));
  w.handlers.search = () => ({ items: [] });
  input().value = "q2"; input().dispatch("keydown", { key: "Enter" }); await tick();
  r = w.draw("code"); assert.ok(has(r, "찾는 이름이 없어요"));
  input().value = " "; input().dispatch("input"); await new Promise((res) => setTimeout(res, 300));
  // head buttons: switch repo, re-index
  const [pick, again] = w.cg.headButtons();
  change(pick, "o/other"); again.click();
  r = w.draw("code"); assert.ok(has(r, "마지막 색인 커밋"));
  assert.ok(w.cg.sub("code").includes("함수와"));
  // a search still pending when the screen resets
  let rel; w.handlers.search = () => new Promise((res) => { rel = res; });
  input().value = "late"; input().dispatch("keydown", { key: "Enter" }); await tick();
  w.cg.reset(); rel({ items: [] }); await tick();
});

test("codegraph: empty, unsupported and failing servers; repo matching", async () => {
  let w = cgWorld({ handlers: { repos: () => { throw new Error("HTTP 404"); } } });
  w.draw("code"); await tick();
  assert.ok(has(w.draw("code"), "지원하지 않아요"));
  w = cgWorld({ handlers: { repos: () => { throw new Error("down"); } } });
  w.draw("code"); await tick(); assert.ok(has(w.draw("code"), "down"));
  w = cgWorld({ handlers: { repos: () => ({ repos: [{ repo: "x/other", commit: "1" }] }) } });
  w.draw("code"); await tick();
  let r = w.draw("code"); assert.ok(has(r, "아직 색인하지 않았어요") && has(r, "다른 저장소를 고를 수도"));
  assert.ok(has(r, "arkism"));
  const [pick] = w.cg.headButtons(); assert.ok(has(pick, "색인 안 됨"));
  w = cgWorld({ handlers: { repos: () => ({}) }, host: { gitRepo: "" } });
  w.draw("code"); await tick(); r = w.draw("code"); assert.ok(!has(r, "다른 저장소"));
  assert.ok(w.cg.headButtons()[0].disabled);
  // last-path match, fresh index, a host without commit counts
  w = cgWorld({ host: { gitRepo: "other", headSha: "zzz" }, behind: 0 });
  w.draw("code"); await tick(); r = w.draw("code");
  assert.ok(has(r, "기본 브랜치가 색인한 커밋보다 앞서"));
  w = cgWorld({ handlers: { repos: () => ({ repos: [{ repo: "arkism", commit: "fff", stats: { mode: "full" } }] }) }, host: { repoUrl: "http://gitlab" } });
  w.draw("code"); await tick(); r = w.draw("code");
  assert.ok(has(r, "색인 최신") && has(r, "전체 색인"));
  w.cg.search; // no-op read
  btn(r, "다시 색인").click(); // clipboard path without the repo flag
  assert.equal(clipboard.text.pop(), "arkism codegraph index --mirror arkism");
  const s = w.cg.seg("code"); btn(s, "PR 영향").click(); assert.deepEqual(w.log.mode, ["impact"]);
  // the code button hides without a github URL
  r = w.draw("code"); const inp = els(r).find((n) => n.tagName === "INPUT");
  inp.value = "b"; inp.dispatch("keydown", { key: "Enter" }); await tick();
  assert.ok(!btn(w.draw("code"), "코드 열기"));
  // a search without a repo clears results
  w = cgWorld({ handlers: { repos: () => ({ repos: [] }) } });
  w.draw("code"); await tick(); r = w.draw("code");
  // the repo picked elsewhere disappears → re-picked on next render
  w = cgWorld();
  w.draw("code"); await tick();
  w.handlers.repos = () => ({ repos: [{ repo: "o/arkism", commit: "abc" }] });
  w.cg.reset(); w.draw("code"); await tick(); w.draw("code");
});

test("codegraph: PR impact reads changed lines and shows each function's reach", async () => {
  const w = cgWorld();
  w.draw("impact"); await tick();
  let r = w.draw("impact"); // no PR yet → auto-loads the first open one
  assert.ok(has(r, "PR을 고르세요"));
  await tick(); await tick();
  r = w.draw("impact");
  assert.ok(has(r, "10곳 넘게") && has(r, "색인이 PR보다 오래돼") && has(r, "영향 큼") && has(r, "+2 −1") && has(r, "일부만 보여요"));
  assert.ok(has(r, "PR #7") && has(r, "깊이별 영향") && has(r, "개수 한도에서 멈춤") && has(r, "+add2"));
  assert.deepEqual(w.log.posts.find(([op]) => op === "changed")[1].ranges.map((x) => [x.path, x.start_line, x.end_line]), [["a.go", 2, 3], ["a.go", 10, 10], ["b.ts", 1, 1]]);
  btn(r, "다시 색인").click();
  r = w.draw("impact");
  btn(r, "pkg.f2").click(); r = w.draw("impact");
  assert.ok(has(r, "부르는 곳이 없어요") && has(r, "바뀐 함수"));
  for (const t of ["코드 리뷰에서 보기"]) els(r).filter((n) => n.tagName === "BUTTON" && has(n, t)).forEach((b) => b.click());
  btn(r, "코드 그래프에서 열기").click(); await tick();
  btn(r, "pkg.f1").click(); r = w.draw("impact");
  r = w.draw("impact");
  btn(r, "코드 열기")?.click();
  els(r).filter((n) => n.tagName === "BUTTON" && has(n, "코드 리뷰에서 보기")).forEach((b) => b.click());
  assert.ok(w.log.review.every((x) => x === "review:arkism#7"));
  btn(r, "영향 받는 곳").click(); r = w.draw("impact");
  btn(r, "pkg.i2").click(); await tick();
  assert.ok(w.log.mode.includes("code"));
  btn(r, "바뀐 함수").click();
  // switch PR with the picker; a PR whose patches are unreadable or touch no code
  w.gitCalls.diff = () => Promise.reject(new Error("x"));
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); r = w.draw("impact");
  assert.ok(has(r, "바뀐 줄을 읽지 못했어요"));
  w.gitCalls.pr = () => ({ number: 7, files: [{ path: "README.md" }] });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); r = w.draw("impact");
  assert.ok(has(r, "바꾸지 않았어요"));
  change(els(r).find((n) => n.tagName === "SELECT"), "");
  // a PR not indexed, another error
  w.gitCalls.pr = () => Promise.reject(new Error("not_found"));
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); r = w.draw("impact");
  assert.ok(has(r, "아직 색인하지 않았어요"));
  w.gitCalls.pr = () => ({ number: 7, files: [{ path: "a.go" }], head_sha: "old1" });
  w.handlers.changed = () => ({ items: [], commit: "old" });
  w.gitCalls.diff = () => ({ patch: "@@ -1 +1 @@\n+x" });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); r = w.draw("impact");
  assert.ok(has(r, "걸치는 함수·메서드가 색인에 없어요"));
  btn(r, "영향 받는 곳").click(); r = w.draw("impact"); assert.ok(has(r, "영향 받는 곳이 없어요"));
  // big only (not stale): "하나씩 확인하기"
  w.handlers.changed = () => ({ items: [{ symbol: sym("f1", { loc: "a.go:2" }), impact_total: 11, impact: [] }] });
  w.gitCalls.pr = () => ({ number: 7, files: [{ path: "a.go" }] });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); r = w.draw("impact");
  btn(r, "하나씩 확인하기").click();
  // a busy load shows its step; a stale ticket is ignored
  let rel; w.gitCalls.pr = () => new Promise((res) => { rel = res; });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); r = w.draw("impact"); assert.ok(has(r, "PR을 불러오는 중"));
  w.cg.reset(); rel({ files: [] }); await tick();
  let rel2; w.gitCalls.pr = () => ({ files: [{ path: "a.go" }] }); w.gitCalls.diff = () => new Promise((res) => { rel2 = res; });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); w.cg.reset(); rel2({ patch: "" }); await tick();
  let rel3; w.gitCalls.diff = () => ({ patch: "@@ -1 +1 @@\n+x" }); w.handlers.changed = () => new Promise((res) => { rel3 = res; });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); w.cg.reset(); rel3({ items: [] }); await tick();
  let rel4; w.gitCalls.pr = () => new Promise((_, rej) => { rel4 = rej; });
  change(els(r).find((n) => n.tagName === "SELECT"), "7"); await tick(); w.cg.reset(); rel4(new Error("late")); await tick();
  // no open PRs
  const v = cgWorld({ prs: [] });
  v.draw("impact"); await tick(); assert.ok(has(v.draw("impact"), "열린 PR이 없어요"));
  // open a symbol whose result arrives after another open
  const u = cgWorld();
  u.draw("code"); await tick();
  let relA; u.handlers.callers = () => new Promise((res) => { relA = res; });
  const ri = els(u.draw("code")).find((n) => n.tagName === "INPUT");
  ri.value = "a"; ri.dispatch("keydown", { key: "Enter" }); await tick();
  u.cg.reset(); relA({}); await tick();
  u.handlers.callers = () => Promise.reject(new Error("late"));
  ri.value = "b"; ri.dispatch("keydown", { key: "Enter" }); await tick(); u.cg.reset(); await tick();
});

// ------------------------------------------------------------ Gallery delete
function galleryWorld(opts = {}) {
  document.body.replaceChildren();
  const nodes = { table: el("div"), stats: el("div") };
  const log = { notice: [], renders: 0, req: [] };
  const state = { data: { designSystems: [{ id: "d1", revision: 1, design_system: { title: "DS1", product: "p", purpose: "q" } }, { id: "b", builtin: true }, { id: "d2", revision: 2 }] },
    scope: { s: 1 }, selected: "prompt:p1", promptRun: "p1" };
  const answers = { confirm: true };
  const handlers = {
    list: (col) => ({ items: [{ id: `${col}-fresh` }] }),
    archived: (col) => ({ items: col === "prompts" ? [{ id: "p1", revision: 1, updated_at: "t", image_prompt: { title: "P1", kind: "slide", engine: "e" } }, { id: "p2", image_prompt: { kind: "image" } }] : [{ id: "d9", design_system: { product: "x" } }] }),
    del: () => ({}), restore: () => ({}),
    ...opts.handlers,
  };
  const request = async (path, o = {}) => {
    log.req.push([o.method || "GET", path]);
    const [, col, rest] = /^\/v1\/(\w+)(.*)$/.exec(path);
    if (o.method === "DELETE") return handlers.del(col, rest);
    if (rest.endsWith("/restore")) return handlers.restore(col, rest);
    if (rest.startsWith("/archived")) return handlers.archived(col);
    return handlers.list(col);
  };
  const g = window.ArkismGalleryDelete({ state, $: (q) => nodes[q.slice(1)], el, button, icon: (n) => el("i", n), raw: (x) => x,
    render: () => { log.renders++; }, request, query: () => "q=1", notice: (m) => log.notice.push(m),
    confirmPop: async () => answers.confirm, shortDate: () => "d" });
  return { g, state, nodes, log, answers, handlers };
}

test("gallery: delete from a detail or a card, with confirm and failures", async () => {
  const w = galleryWorld();
  const r = { id: "p1", revision: 3 };
  w.g.deleteButton("prompts", r).click(); await tick();
  assert.deepEqual(w.log.req[0], ["DELETE", "/v1/prompts/p1"]);
  assert.equal(w.state.selected, null); assert.equal(w.state.promptRun, null);
  assert.deepEqual(w.state.data.prompts, [{ id: "prompts-fresh" }]);
  assert.equal(w.log.notice.pop(), "삭제했어요.");
  w.answers.confirm = false; await w.g.remove("design_systems", { id: "x" }); assert.equal(w.log.req.length, 2);
  w.answers.confirm = true;
  for (const [msg, want] of [["revision_conflict", "다른 곳에서 먼저"], ["forbidden", "권한이 없어요"], ["oops", "(oops)"]]) {
    w.handlers.del = () => { throw new Error(msg); };
    await w.g.remove("design_systems", { id: "x", design_system: { title: "T" } });
    assert.ok(w.log.notice.pop().includes(want));
  }
  w.handlers.del = () => { throw new Error("not_found"); };
  await w.g.remove("prompts", { id: "gone" }); assert.equal(w.log.notice.pop(), "삭제했어요.");
  // card shortcut: click and keyboard
  const card = el("button");
  w.g.cardShortcut("prompts", { id: "c" }, card);
  const x = card.childNodes.find((n) => n.className === "gd-card-del");
  x.click(); x.dispatch("keydown", { key: "Enter" }); x.dispatch("keydown", { key: " " }); x.dispatch("keydown", { key: "a" });
  await tick();
  assert.equal(w.log.req.filter(([m, p]) => m === "DELETE" && p === "/v1/prompts/c").length, 3);
  assert.ok(card.classList.contains("gd-card"));
});

test("gallery: archive lists, restore, remove all, and the design system picker", async () => {
  const w = galleryWorld();
  const chip = w.g.archiveChip("prompts"); chip.click(); await tick();
  assert.ok(w.g.isOpen("prompts"));
  assert.ok(w.g.archiveChip("prompts").className.includes("active"));
  w.g.renderArchive("prompts", "slide");
  let page = w.nodes.table;
  assert.ok(has(page, "1개") && has(page, "슬라이드 · e · d 보관"));
  w.g.renderArchive("prompts"); assert.ok(has(w.nodes.table, "이미지") && has(w.nodes.table, "이 프롬프트"));
  btn(w.nodes.table, "되살리기").click(); await tick(); assert.ok(w.log.notice.pop().startsWith("되살렸어요"));
  w.handlers.restore = () => { throw new Error("nope"); };
  btn(w.nodes.table, "되살리기").click(); await tick(); assert.ok(w.log.notice.pop().includes("(nope)"));
  // delete inside the open archive reloads it
  btn(w.nodes.table, "삭제").click(); await tick();
  assert.ok(w.log.req.some(([, p]) => p.startsWith("/v1/prompts/archived")));
  btn(w.nodes.table, "모두 삭제").click(); await tick();
  assert.equal(w.log.notice.pop(), "2개를 삭제했어요.");
  // a partial failure and an empty/failed archive
  let n = 0; w.handlers.del = () => { if (n++ === 0) throw new Error("x"); };
  w.g.renderArchive("prompts"); btn(w.nodes.table, "모두 삭제").click(); await tick();
  assert.equal(w.log.notice.pop(), "1개를 삭제했어요. 1개는 지우지 못했어요.");
  w.handlers.list = () => { throw new Error("x"); };
  w.g.renderArchive("prompts"); btn(w.nodes.table, "모두 삭제").click(); await tick(); // reload failure is quiet
  w.answers.confirm = false; await w.g.removeMany("prompts", [{ id: "a" }]); w.answers.confirm = true;
  await w.g.removeMany("prompts", []);
  w.handlers.archived = () => ({ items: [] });
  btn(w.nodes.table, "목록으로").click(); assert.ok(!w.g.isOpen("prompts"));
  w.g.archiveChip("prompts").click(); w.g.renderArchive("prompts"); assert.ok(has(w.nodes.table, "불러오는 중"));
  await tick(); w.g.renderArchive("prompts"); assert.ok(has(w.nodes.table, "비어 있어요"));
  w.handlers.archived = () => { throw new Error("x"); };
  w.g.archiveChip("prompts").click(); w.g.archiveChip("prompts").click(); await tick();
  w.g.renderArchive("prompts"); assert.ok(has(w.nodes.table, "불러오지 못했어요"));
  // toggling away before the archive arrives does not repaint it
  w.g.archiveChip("design_systems").click(); w.g.archiveChip("design_systems").click(); await tick();
  w.handlers.archived = (col) => ({ items: [{ id: "d9", design_system: { product: "x" } }] });
  // design system library buttons and picking
  w.g.reset();
  let [bulk, archive] = w.g.libraryButtons();
  btn(bulk, "선택").click(); assert.ok(w.g.picking());
  [bulk] = w.g.libraryButtons();
  const card1 = w.g.markCard({ id: "d1" }, el("button"));
  assert.ok(card1.className.includes("gd-pickable"));
  assert.equal(w.g.markCard({ id: "b", builtin: true }, el("button")).className, "");
  assert.equal(w.g.pick({ id: "b", builtin: true }, el("button")), false);
  assert.equal(w.g.pick({ id: "d1" }, card1), true); assert.equal(card1.getAttribute("aria-pressed"), "true");
  w.g.pick({ id: "d1" }, card1); assert.equal(card1.getAttribute("aria-pressed"), "false");
  btn(bulk, "모두 선택").click(); [bulk] = w.g.libraryButtons();
  btn(bulk, "선택 풀기").click(); [bulk] = w.g.libraryButtons();
  btn(bulk, "모두 선택").click(); [bulk] = w.g.libraryButtons();
  btn(bulk, "삭제 (2)").click(); await tick();
  assert.equal(w.log.notice.pop(), "2개를 삭제했어요.");
  [bulk] = w.g.libraryButtons(); btn(bulk, "선택").click(); [bulk] = w.g.libraryButtons();
  assert.ok(btn(bulk, "삭제").disabled); btn(bulk, "취소").click(); assert.ok(!w.g.picking());
  assert.equal(w.g.markCard({ id: "d1" }, el("button")).className, "");
  assert.equal(w.g.pick({ id: "d1" }, el("button")), false);
  // design system archive view
  archive.click(); await tick();
  assert.equal(w.g.libraryButtons().length, 1);
  w.g.renderArchive("design_systems"); assert.ok(has(w.nodes.table, "이 디자인 시스템") && has(w.nodes.table, "x · d 보관"));
  btn(w.nodes.table, "모두 삭제").click(); await tick();
  // no deletable systems → no 선택 button
  w.state.data.designSystems = [{ id: "b", builtin: true }];
  w.g.reset(); const [empty] = w.g.libraryButtons(); assert.equal(empty.childNodes.length, 0);
  // a busy bulk delete ignores a second request
  let rel; w.handlers.del = () => new Promise((r) => { rel = r; });
  const first = w.g.removeMany("design_systems", [{ id: "q" }]); await tick();
  await w.g.removeMany("design_systems", [{ id: "q" }]);
  rel(); await first;
});

// ------------------------------------------------------------ SSH trust
test("ssh: trusted hosts pass, unknown keys need a checked approval, changed keys retry", async () => {
  assert.equal(window.ArkismSSH(null), null);
  const calls = [];
  let review = { state: "trusted" }, fail = null;
  const core = { other: 1, invoke: async (cmd, args) => {
    calls.push([cmd, args]);
    if (cmd === "ssh_host_key_check") return typeof review === "function" ? review(args) : review;
    if (cmd === "ssh_host_key_cancel") throw new Error("ignored");
    if (["ssh_connect", "git_op"].includes(cmd) && fail) { const f = fail; fail = null; throw f; }
    return "ok:" + cmd;
  } };
  const ssh = window.ArkismSSH(core);
  assert.equal(ssh.other, 1);
  assert.equal(await ssh.invoke("plain"), "ok:plain");
  assert.equal(await ssh.invoke("ssh_connect", { host: "h" }), "ok:ssh_connect");
  // probe without a challenge fails
  review = { state: "unknown" };
  await assert.rejects(ssh.invoke("ssh_test", { form: { a: 1 } }), /ssh_host_key_probe_failed/);
  // unknown key: dialog, approve only after the box is checked; Enter on approve while unchecked does nothing
  document.body.replaceChildren();
  const before = el("button"); document.body.append(before); before.focus();
  review = { state: "unknown", challenge: "c1", host: "h", port: 22, key_type: "ed25519", fingerprint: "SHA256:x" };
  const p = ssh.invoke("ssh_connect", { host: "h" });
  const again = ssh.invoke("ssh_connect", { host: "h" }); // shares the pending check
  await tick();
  const dlg = () => els(document.body).find((n) => n.tagName === "DIALOG");
  assert.ok(has(dlg(), "SSH 지문을 확인하세요") && has(dlg(), "SHA256:x"));
  const approve = btn(dlg(), "이 키를 저장하고 계속"), check = els(dlg()).find((n) => n.tagName === "INPUT");
  assert.ok(approve.disabled);
  approve.click(); dlg().dispatch("keydown", { key: "Enter", target: approve }); dlg().dispatch("keydown", { key: "Tab" });
  dlg().dispatch("keydown", { key: "Enter", target: check });
  check.checked = true; check.dispatch("change"); assert.ok(!approve.disabled);
  dlg().dispatch("keydown", { key: "Enter", target: approve });
  assert.equal(await p, "ok:ssh_connect"); await again;
  assert.equal(document.activeElement, before);
  assert.ok(calls.some(([c, a]) => c === "ssh_host_key_approve" && a.replace === false));
  // changed key: cancel button, Escape (cancel event), Enter on cancel, close → all reject
  review = { state: "changed", challenge: "c2", host: "h", port: 22, key_type: "rsa", fingerprint: "new", previous_fingerprint: "old" };
  for (const how of ["button", "escape", "enter", "close"]) {
    before.remove();
    const q = ssh.invoke("git_op", { host: "h" + how });
    await tick();
    const d = dlg(); assert.ok(has(d, "SSH 키가 바뀌었어요") && has(d, "이전 지문"));
    const cancel = btn(d, "취소");
    if (how === "button") cancel.click();
    if (how === "escape") assert.ok(d.dispatch("cancel").defaultPrevented);
    if (how === "enter") d.dispatch("keydown", { key: "Enter", target: cancel });
    if (how === "close") d.close();
    await assert.rejects(q, /ssh_host_key_cancelled/);
  }
  // approve via click; a host key change during the command reruns the check with force
  review = (args) => (args.force ? { state: "changed", challenge: "c3", host: "h", port: 1, key_type: "k", fingerprint: "f", previous_fingerprint: "o" } : { state: "trusted" });
  fail = new Error("ssh_host_key_changed");
  const r = ssh.invoke("ssh_connect", { host: "z" });
  await tick();
  const d = dlg(); els(d).find((n) => n.tagName === "INPUT").checked = true; btn(d, "새 키로 교체하고 계속").click();
  assert.equal(await r, "ok:ssh_connect");
  assert.ok(calls.some(([c, a]) => c === "ssh_host_key_approve" && a.replace === true));
  fail = "other";
  await assert.rejects(ssh.invoke("ssh_connect", { host: "z" }), (e) => e === "other");
});

// ------------------------------------------------------------ Gateway token
test("gateway token: status card, alert, regenerate and later", async () => {
  assert.equal(window.ArkismGatewayToken({ el, button, tauri: null, notice() {} }), null);
  const notices = [];
  let status = { state: "missing" }, regen = { agents_updated: true, status: { state: "set", updated_ms: 0, from_env: true } };
  globalThis.ask = async () => window.askAnswer;
  window.askAnswer = true;
  const tauri = { invoke: async (cmd) => {
    if (cmd === "gateway_token_status") { if (status instanceof Error) throw status; return status; }
    if (regen instanceof Error) throw regen;
    return regen;
  } };
  const sideSec = (title, ...xs) => { const s = el("section", "", title); s.append(...xs); return s; };
  const gw = window.ArkismGatewayToken({ el, button, tauri, notice: (m) => notices.push(m) });
  let [alert, sec] = gw.side(sideSec);
  assert.ok(has(sec, "확인하는 중"));
  await tick();
  assert.ok(!alert.hidden && has(alert, "토큰이 없어요") && has(sec, "없음"));
  btn(alert, "토큰 다시 만들기").click(); await tick();
  assert.ok(notices.pop().startsWith("새 토큰을 만들고") && has(sec, "설정됨") && has(sec, "환경 변수") && alert.hidden);
  // set → asks first; declined does nothing; accepted without agent update
  window.askAnswer = false; btn(sec, "다시 만들기").click(); await tick(); assert.equal(notices.length, 0);
  window.askAnswer = true; regen = { agents_updated: false, status: { state: "weird" } }; btn(sec, "다시 만들기").click(); await tick();
  assert.ok(notices.pop().includes("못 넣었어요") && has(sec, "확인 필요") && has(alert, "파일에 문제가"));
  regen = new Error("x"); btn(alert, "토큰 다시 만들기").click(); btn(alert, "토큰 다시 만들기").click(); await tick();
  assert.equal(notices.pop(), "토큰을 만들지 못했어요.");
  btn(alert, "나중에").click(); assert.ok(alert.hidden);
  // a new card after 나중에 stays hidden; a failing status says so
  status = { state: "invalid" }; [alert, sec] = gw.side(sideSec); await tick(); assert.ok(alert.hidden);
  status = new Error("x"); [alert, sec] = gw.side(sideSec); await tick(); assert.ok(has(sec, "확인하지 못했어요"));
  status = { state: "set", updated_ms: Date.UTC(2026, 0, 2) }; [alert, sec] = gw.side(sideSec); await tick(); assert.ok(has(sec, "설정됨"));
});
