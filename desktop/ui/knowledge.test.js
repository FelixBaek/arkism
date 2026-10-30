// Run with: node --test desktop/ui/*.test.js
// TOOLS › 지식 (knowledge.js): sources, document chunks, test search and wiki pages against a fake server.
const test = require("node:test");
const assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { create: createPager } = require("./memory-pages.js");
const dom = require("./testdata/fake-dom.cjs");

const T = "2026-10-20T00:00:00Z";
const bind = (id, profile, status) => ({ id, specialist_profile_id: profile, lifecycle_status: status, revision: 3 });
const sources = () => [
  { id: "s1", display_name: "a.md", format: ".md", lifecycle_status: "draft", trust_level: "user", document_count: 2, chunk_count: 4, revision: 1, updated_at: T, created_at: T, bindings: [bind("b1", "design_consultant", "candidate")] },
  { id: "s2", display_name: "b.txt", format: ".txt", lifecycle_status: "active", trust_level: "reviewed", approved_by: "owner", document_count: 1, chunk_count: 2, revision: 2, updated_at: T, created_at: T,
    bindings: [bind("b2", "illustrator", "active_synthetic"), bind("b3", "listing_reviewer", "suspended"), bind("b4", "design_consultant", "candidate")] },
  { id: "s3", display_name: "c.json", lifecycle_status: "failed", trust_level: "user", document_count: 1, chunk_count: 0, revision: 1, updated_at: T, created_at: T, bindings: [] }
];
const documents = () => ({
  s1: [{ id: "d1", current: true, sections: ["A › B / C", "Intro", "Third", "Fourth"], chunk_count: 4, indexed_chunks: 2, chunker_version: "h1", parser_version: "p1", filename: "a.md",
    source_version: "abcdef123456", created_at: T, extraction_status: "ok", mime_type: "text/markdown", content_hash: "hash" },
  { id: "d0", current: false, sections: [], extraction_status: "failed", error_code: "bad_utf8", filename: "a-old.md", source_version: "0000000011", created_at: T }],
  s2: [{ id: "d2", current: true, sections: [], chunk_count: 2, indexed_chunks: 2, chunker_version: "h1", parser_version: "p1", filename: "b.txt", source_version: "bbbbbbbbbb", created_at: T, extraction_status: "ok" }],
  s3: [{ id: "d3", current: true, sections: [], error_code: "parse", extraction_status: "failed", filename: "c.json", source_version: "cccccccccc", created_at: T }]
});
const pages = () => [
  { id: "p1", slug: "first", type: "entity", review_state: "draft", revision: 1, updated_at: "2026-10-22", knowledge_source_ids: ["s1", "lost"], last_merge: "create",
    draft: { title: "First", text: "see [[Page Two]] and [[Ghost]]\n\npara two", links: ["Page Two", "Ghost"] } },
  { id: "p2", slug: "page-two", type: "concept", review_state: "draft", revision: 2, updated_at: "2026-10-21", knowledge_source_ids: ["s1"], last_merge: "rewrite",
    reviewed: { title: "Page Two", text: "old body", links: [] }, draft: { title: "Page Two", text: "new [[First]]", links: [] } },
  { id: "p3", slug: "locked", type: "concept", review_state: "reviewed", locked: true, revision: 1, updated_at: "2026-10-19", knowledge_source_ids: [], reviewed: { title: "Locked", text: "kept", links: [] } },
  { id: "p4", slug: "stale", type: "other", review_state: "reviewed", stale_sources: ["gone"], revision: 1, updated_at: "2026-10-18", reviewed: { title: "Stale", text: "zzz", links: [] } },
  { id: "p5", slug: "nope", review_state: "rejected", revision: 1, updated_at: "2026-10-17" },
  { id: "p6", slug: "dead", review_state: "deleted", revision: 1, updated_at: "2026-10-16", knowledge_source_ids: ["s1"] }
];
const jobs = () => [{ id: "j1", status: "failed", updated_at: "2026-10-22", job: { knowledge_source_id: "s1", calls: 2, tokens: 100, generator: { model: "wiki-model" }, error: "wiki_budget_exceeded",
  result: { created: ["x"], updated: [], rejected: [{ path: "w/x", reason: "secret_detected" }, { path: "w/y", reason: "odd_reason" }, { path: "w/z", reason: "bad_body" }] } } },
{ id: "j0", status: "done", updated_at: "2026-10-01", job: { knowledge_source_id: "s1" } }];

// Fake Arkism server: answers by path, records every call, and lets a test override any route.
function server(over = {}) {
  const calls = [], db = { sources: sources(), documents: documents(), pages: pages(), jobs: jobs() };
  async function request(path, opts = {}) {
    const body = opts.body ? JSON.parse(opts.body) : undefined;
    calls.push({ path, method: opts.method || "GET", body });
    const route = Object.keys(over).find((k) => path.startsWith(k));
    if (route) return over[route](path, body);
    if (path.startsWith("/v1/knowledge/sources/query")) {
      const items = db.sources.filter((s) => !body.q || s.display_name.includes(body.q));
      return { items, next_cursor: "", authority: "data_only_untrusted", total: items.length, facets: { kinds: { ".md": 1, ".txt": 1, ".json": 1 } } };
    }
    if (path.startsWith("/v1/knowledge/documents")) return { items: db.documents[new URLSearchParams(path.split("?")[1]).get("source")] || [] };
    if (path.startsWith("/v1/wiki/pages?")) return { items: db.pages };
    if (path.startsWith("/v1/wiki/graph")) return { links: [{ to_id: "p1" }, { to_id: "p2" }] };
    if (path.startsWith("/v1/wiki/jobs?")) return { items: db.jobs };
    if (path.includes("/remove?dry_run=1")) return { dry_run: true, deleted: ["p6"], detached: ["p1", "p2"], unlinked: [] };
    if (path.endsWith("/remove")) return { deleted: ["p6"], detached: [], unlinked: ["l"], jobs_ended: [] };
    if (path === "/v1/knowledge/search") return { authority: "data_only_untrusted", receipt: { candidate_count: 5, selected_count: 1, excluded_count: 2, retrieval_log_id: "log1",
      exclusion_summary: { duplicate_chunk: 1, mystery: 1 }, context: { items: [{ section_path: "", source_id: "s1", token_estimate: 12, content: "chunk text", citation: "a.md#1" }] } } };
    if (path === "/v1/knowledge/index") return { embedded: 3 };
    if (path === "/v1/wiki/jobs") return { duplicate: false };
    if (path === "/v1/knowledge/ingest") return {};
    if (path === "/v1/knowledge/bindings") return { id: "nb", revision: 1, status: "candidate" };
    return { ok: true };
  }
  return { calls, db, request };
}

// File inputs the view opens with input.click(): the test hands them a file and fires change.
const picked = [];
const click = dom.Element.prototype.click;
dom.Element.prototype.click = function () { if (this.type === "file") picked.push(this); return click.call(this); };

async function setup(over, stateOver = {}) {
  const { document } = dom.install();
  const h = dom.helpers(document);
  for (const id of ["table", "detail", "stats", "chips", "controls", "title-count", "title"]) { const n = h.el("div"); n.id = id; document.body.append(n); }
  const head = h.el("div", "page-head"); document.body.append(head);
  const { createRemoteCollection } = await import("./modules/remoteCollection.mjs");
  const { createVirtualList } = await import("./modules/virtualList.mjs");
  const state = { view: "knowledge", server: "local", scope: { workspace_id: "w" }, connection: { generation: 1 }, ...stateOver };
  const srv = server(over);
  const heads = [];
  const confirms = [];
  let view;
  const ctx = {
    ...h, query: (o = {}) => h.query(o), state, request: srv.request, ago: (v) => (v ? `ago(${v})` : "—"),
    render: () => view.views.knowledge.render(),
    detailShell: (label) => { const body = h.el("div", "detail-body"); body.dataset.label = label; h.$("#detail").replaceChildren(body); return body; },
    headExtras: (o) => heads.push(o),
    confirmPop: async (...a) => { confirms.push(a); return confirms.answer ?? true; },
    remoteCollection: (read) => createRemoteCollection({ state, read, createPager }),
    virtualList: createVirtualList()
  };
  require("./knowledge.js");
  view = window.ArkismKnowledge(ctx);
  const files = (picked.length = 0, picked);
  const k = {
    view, state, srv, heads, confirms, files, h, document,
    table: () => h.$("#table"), detail: () => h.$("#detail"),
    btn(root, label) { const b = dom.buttons(root, label)[0]; assert.ok(b, `button ${label}`); return b; },
    async press(root, label) { k.btn(root, label).click(); await dom.flush(); },
    async start() { view.views.knowledge.render(); await dom.flush(); },
    posted: (path) => srv.calls.filter((c) => c.path === path && c.method === "POST")
  };
  return k;
}
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const file = (name, content, size = content.length) => ({ name, size, text: async () => content });

test("first render loads sources, wiki, graph and jobs, then lists sources with review attention", async () => {
  const k = await setup();
  const v = k.view.views.knowledge;
  assert.equal(v.title(), "지식"); assert.match(v.sub(), /내 AI가 찾아 써요/); assert.equal(v.keepDetail, true);
  v.render();
  assert.match(k.table().textContent, /불러오는 중/);
  await dom.flush();
  const paths = k.srv.calls.map((c) => c.path.split("?")[0]);
  for (const p of ["/v1/knowledge/sources/query", "/v1/wiki/pages", "/v1/wiki/graph", "/v1/wiki/jobs"]) assert.ok(paths.includes(p), p);
  assert.deepEqual(k.srv.calls[0].body.scope, { workspace_id: "w" });
  const t = k.table().textContent;
  assert.match(t, /확인 전 1 · 읽기 실패 1/);
  assert.match(t, /전체 3개 중 3개 표시/);
  assert.match(t, /Markdown/); assert.match(t, /JSON/);
  assert.equal(k.heads.at(-1).count, "자료 3 · 위키 5");
  // first source is selected and its detail asks for review
  assert.equal(k.detail().firstChild.dataset.label, "문서 · .md");
  assert.match(k.detail().textContent, /새 버전이 올라왔어요/);
  assert.match(k.detail().textContent, /#1A › B \/ C/);
  assert.match(k.detail().textContent, /오늘 쓸 수 있는 모델 호출을 다 썼어요/);
  assert.match(k.detail().textContent, /비밀값이 들어 있음/); assert.match(k.detail().textContent, /odd_reason/); assert.match(k.detail().textContent, /여러 번 거부했어요/);
  // review attention opens the first source needing review, then 나중에 goes to the next one
  await k.press(k.table(), "하나씩 확인하기");
  await k.press(k.detail(), "나중에");
  assert.equal(k.detail().firstChild.dataset.label, "문서 · 파일");
  assert.match(k.detail().textContent, /글을 읽지 못했어요/);
  assert.match(k.detail().textContent, /읽은 오류parse/); // the code only under 자세히
  assert.doesNotMatch(k.detail().textContent, /내 AI가 쓰기/);
  // a reviewed source shows bindings that turn off, on, and a disabled candidate is not the case here
  const rows = k.table().querySelectorAll(".v4k-row");
  rows[1].click(); await dom.flush();
  assert.match(k.detail().textContent, /확인한 사람owner/);
  await k.press(k.detail(), "끄기");
  assert.deepEqual(k.posted("/v1/knowledge/bindings/b2/state")[0].body, { scope: { workspace_id: "w" }, expected_revision: 3, status: "suspended" });
  assert.match(k.detail().textContent, /껐어요/);
  k.table().querySelectorAll(".v4k-row")[1].dispatchEvent(Object.assign(new dom.Event("keydown"), { key: "Enter" }));
  await dom.flush();
  await k.press(k.detail(), "켜기");
  assert.equal(k.posted("/v1/knowledge/bindings/b3/state")[0].body.status, "active_synthetic");
  // a reviewed source turns on for every agent with one switch, and off again
  k.table().querySelectorAll(".v4k-row")[1].click(); await dom.flush();
  assert.match(k.detail().textContent, /지금은 AI가 이 자료를 쓰지 않아요/);
  await k.press(k.detail(), "쓰게 하기");
  assert.equal(k.posted("/v1/knowledge/sources/s2/review").length, 0);
  assert.equal(k.posted("/v1/knowledge/bindings")[0].body.specialist_profile_id, "default");
  assert.equal(k.posted("/v1/knowledge/bindings/nb/state")[0].body.status, "active_synthetic");
  k.srv.db.sources[1].bindings.push(bind("b9", "default", "active_synthetic"));
  await k.press(k.detail(), "켜기");
  assert.match(k.detail().textContent, /내 모든 AI가 이 자료를 찾아 써요/);
  await k.press(k.detail(), "그만 쓰게 하기");
  assert.deepEqual(k.posted("/v1/knowledge/bindings/b9/state")[0].body, { scope: { workspace_id: "w" }, expected_revision: 3, status: "suspended" });
});

test("source actions: review, connect a specialist, index, wiki job, run now and section chips", async () => {
  const k = await setup();
  await k.start();
  const d = () => k.detail();
  // a draft source cannot turn a binding on
  const on = k.btn(d(), "켜기"); assert.equal(on.disabled, true); assert.equal(on.title, "확인한 자료만 켤 수 있어요");
  assert.equal(k.btn(d(), "위키 만들기").disabled, true);
  await k.press(d(), "확인했어요 · 내 AI가 쓰게 하기");
  assert.deepEqual(k.posted("/v1/knowledge/sources/s1/review")[0].body, { scope: { workspace_id: "w" }, expected_revision: 1, status: "active", trust_level: "reviewed" });
  assert.equal(k.posted("/v1/knowledge/bindings")[0].body.binding_key, "s1:default");
  assert.equal(k.posted("/v1/knowledge/bindings/nb/state")[0].body.status, "active_synthetic");
  assert.match(d().textContent, /이제 내 모든 AI가 이 자료를 찾아 써요/);
  d().querySelector("select").value = "listing_reviewer";
  await k.press(d(), "연결");
  assert.equal(k.posted("/v1/knowledge/bindings").at(-1).body.binding_key, "s1:listing_reviewer");
  await k.press(d(), "벡터 색인");
  assert.match(d().textContent, /색인했어요 \(새로 3 · 그대로 0 · 실패 0\)/);
  d().querySelector(".v4k-cap").value = "5000";
  k.btn(d(), "위키 만들기").disabled = false;
  await k.press(d(), "위키 만들기");
  assert.equal(k.posted("/v1/wiki/jobs")[0].body.max_total_tokens, 5000);
  assert.match(d().textContent, /위키 작업을 대기열에 넣었어요/);
  await k.press(d(), "지금 실행");
  assert.equal(k.posted("/v1/wiki/jobs/j1/run").length, 1);
  // errors from the server become plain notices
  k.srv.db.sources[0].lifecycle_status = "active"; k.srv.db.sources[0].trust_level = "reviewed";
  for (const [msg, line] of [["revision_conflict", /그새 바뀌었어요/], ["nope", /다시 시도해 주세요/]]) {
    const k2 = await setup({ "/v1/knowledge/index": () => { throw new Error(msg); } });
    await k2.start();
    await k2.press(k2.detail(), "벡터 색인");
    assert.match(k2.detail().textContent, line);
  }
  // the third section chip opens the document view on that section
  d().querySelectorAll(".v4k-chunk")[2].click(); await dom.flush();
  assert.equal(k.view.views.knowledge.title(), "a.md");
  assert.match(k.view.views.knowledge.sub(), /부분 단위/);
  assert.match(k.detail().firstChild.dataset.label, /부분 #3/);
});

test("document view: sections with neighbours, versions and a test search with its receipt", async () => {
  const k = await setup();
  await k.start();
  await k.press(k.detail(), "내용 보기");
  const v = k.view.views.knowledge;
  const head = k.heads.at(-1); assert.equal(head.count, "부분 4개"); assert.equal(head.back[0], "자료 · a.md");
  assert.match(k.table().textContent, /목차4/);
  assert.match(k.table().textContent, /#1C/); // leaf of "A › B / C"
  assert.match(k.table().textContent, /부분 4개 · 제목 4개/);
  assert.equal(k.detail().firstChild.dataset.label, "부분 #1");
  assert.match(k.detail().textContent, /아직 확인 전이라/);
  assert.match(k.detail().textContent, /앞뒤 부분#2Intro/);
  k.detail().querySelector(".v4k-near button").click(); await dom.flush();
  assert.equal(k.detail().firstChild.dataset.label, "부분 #2");
  k.table().querySelectorAll(".v4k-row")[3].click(); await dom.flush();
  assert.equal(k.detail().firstChild.dataset.label, "부분 #4");
  await k.press(k.detail(), "확인했어요 · 내 AI가 쓰게 하기");
  assert.equal(k.posted("/v1/knowledge/sources/s1/review").length, 1);
  // versions
  k.table().querySelectorAll(".chip")[1].click(); await dom.flush();
  assert.match(k.table().textContent, /지금 버전/); assert.match(k.table().textContent, /읽기 실패/); assert.match(k.table().textContent, /00000000/);
  assert.equal(k.detail().firstChild.dataset.label, "버전 · 지금");
  assert.match(k.detail().textContent, /내용 해시hash/);
  k.table().querySelectorAll(".v4k-row")[1].click(); await dom.flush();
  assert.equal(k.detail().firstChild.dataset.label, "버전 · 예전");
  await k.press(k.detail(), "다시 올리기");
  assert.equal(k.files.length, 1);
  // search tab from a section title
  k.table().querySelectorAll(".chip")[0].click(); await dom.flush();
  await k.press(k.detail(), "이 제목으로 시험 검색");
  assert.deepEqual(k.posted("/v1/knowledge/search")[0].body, { scope: { workspace_id: "w" }, specialist_profile_id: "default", query: "Fourth", limit: 8 });
  assert.match(k.table().textContent, /chunk text/); assert.match(k.table().textContent, /a.md12 토큰/);
  assert.match(k.detail().textContent, /후보5/); assert.match(k.detail().textContent, /같은 부분1건/); assert.match(k.detail().textContent, /mystery/);
  // a typed search with another specialist
  const form = k.table().querySelector("form");
  form.querySelector("select").value = "illustrator"; dom.fire(form.querySelector("select"), "change");
  form.querySelector("input").value = "  hello ";
  dom.fire(form, "submit"); await dom.flush();
  assert.equal(k.posted("/v1/knowledge/search")[1].body.query, "hello");
  assert.equal(k.posted("/v1/knowledge/search")[1].body.specialist_profile_id, "illustrator");
  // empty query does not send
  k.table().querySelector("form input").value = ""; dom.fire(k.table().querySelector("form"), "submit"); await dom.flush();
  assert.equal(k.posted("/v1/knowledge/search").length, 2);
  assert.match(k.detail().textContent, /후보5/); // an empty query keeps the last receipt
  // head buttons in the document view
  const hb = v.head();
  assert.equal(hb.length, 1);
  hb[0].click(); assert.equal(k.files.length, 2);
  // back to sources
  k.heads.at(-1).back[1](); await dom.flush();
  assert.equal(v.title(), "지식");
  assert.equal(v.head().length, 1);
});

test("search answers: no binding, empty receipt, invalid response and failure", async () => {
  for (const [answer, want] of [
    [{ authority: "data_only_untrusted", reason: "no_active_binding" }, /켠 자료가 없어요/],
    [{ authority: "data_only_untrusted", receipt: { candidate_count: 0, selected_count: 0, excluded_count: 0, retrieval_mode: "hybrid" } }, /맞는 부분이 없어요/],
    [{ receipt: {} }, /다시 시도해 주세요/],
    [new Error("access_denied"), /권한이 없어요/]
  ]) {
    const k = await setup({ "/v1/knowledge/search": () => { if (answer instanceof Error) throw answer; return answer; } });
    await k.start();
    await k.press(k.detail(), "내용 보기");
    k.table().querySelectorAll(".chip")[2].click(); await dom.flush();
    assert.match(k.detail().textContent, /검색하면 몇 개를/);
    const form = k.table().querySelector("form");
    form.querySelector("input").value = "q"; dom.fire(form, "submit"); await dom.flush();
    assert.match(k.table().textContent, want);
  }
});

test("document loading and errors, a source without sections and a source that vanished", async () => {
  let release;
  const k = await setup({ "/v1/knowledge/documents": (path) => (path.includes("s2") ? { items: "bad" } : new Promise((r) => { release = r; })) });
  await k.start();
  assert.match(k.detail().textContent, /불러오는 중…/);
  await k.press(k.detail(), "내용 보기");
  assert.match(k.table().textContent, /불러오는 중…/);
  assert.equal(k.detail().childNodes.length, 0);
  release({ items: [{ id: "d9", current: false, sections: [], chunk_count: 0, indexed_chunks: 0, filename: "a.md", source_version: "1234567890", created_at: T }] });
  await dom.flush();
  assert.match(k.table().textContent, /목차가 없어요. 제목 없는 글은/);
  assert.equal(k.detail().childNodes.length, 0);
  // s2: invalid documents answer
  k.heads.at(-1).back[1](); await dom.flush();
  k.table().querySelectorAll(".v4k-row")[1].click(); await dom.flush();
  assert.match(k.detail().textContent, /목차가 없어요/);
  await k.press(k.detail(), "검색에 쓰는 부분 2개 보기");
  assert.match(k.table().textContent, /문서를 불러오지 못했어요/);
  // the opened document disappears from the server list: back to sources
  k.srv.db.sources = k.srv.db.sources.filter((s) => s.id !== "s2");
  k.view.views.knowledge.head()[0].click();
  k.files.at(-1).files = [file("b2.txt", "x")]; dom.fire(k.files.at(-1), "change"); await dom.flush();
  k.document.body.querySelector("dialog").close("only"); await dom.flush();
  assert.equal(k.view.views.knowledge.title(), "지식");
  assert.match(k.table().textContent, /전체 2개 중 2개/);
});

test("upload: wrong type, PDF, too large, Word, the uploader's check and a duplicate", async () => {
  const k = await setup({ "/v1/knowledge/ingest": (_, body) => (body.content === "same" ? { duplicate: true, source: { id: "s2", revision: 2 } } : { source: { id: body.source_key === "a.md" ? "s1" : "new", revision: 4 } }) });
  await k.start();
  const add = k.view.views.knowledge.head()[0];
  const pick = async (f, choice) => {
    add.click(); const input = k.files.at(-1); input.files = f ? [f] : []; dom.fire(input, "change"); await dom.flush();
    if (choice === undefined) return;
    const dlg = k.document.body.querySelector("dialog.v4k-ask"); assert.ok(dlg, "the uploader is asked");
    assert.match(dlg.textContent, /내 AI가 이 자료를 쓰게 할까요/);
    dlg.close(choice); await dom.flush();
  };
  await pick(null);
  assert.equal(k.posted("/v1/knowledge/ingest").length, 0);
  await pick(file("x.pdf", "pdf"));
  assert.match(k.table().textContent, /PDF는 아직 읽지 못해요/);
  await pick(file("x.hwp", "hwp"));
  assert.match(k.table().textContent, /Word·Markdown·텍스트·JSON 파일을 올릴 수 있어요/);
  await pick(file("big.json", "x".repeat(65 * 1024)));
  assert.match(k.table().textContent, /JSON 파일은 나누면 깨져서/);
  assert.equal(k.posted("/v1/knowledge/ingest").length, 0);
  await pick(file("big.docx", "x", 5 << 20));
  assert.match(k.table().textContent, /Word 파일은 4MB까지/);
  // 취소 sends nothing
  await pick(file("new.txt", "hello"), "no");
  assert.equal(k.posted("/v1/knowledge/ingest").length, 0);
  // 올리기만: ingested, still waiting for a person
  await pick(file("new.txt", "hello"), "only");
  const first = k.posted("/v1/knowledge/ingest")[0].body;
  assert.equal(first.mime_type, "text/plain"); assert.equal(first.source_key, "new.txt"); assert.equal(first.expected_revision, undefined); assert.equal(first.content, "hello");
  assert.equal(k.posted("/v1/knowledge/bindings").length, 0);
  assert.match(k.table().textContent, /'쓰게 하기'를 눌러/);
  // the uploader's check: reviewed, shared with every agent and turned on
  await pick(file("a.md", "v2"), "use");
  const second = k.posted("/v1/knowledge/ingest")[1].body;
  assert.equal(second.expected_revision, 1); assert.equal(second.mime_type, "text/markdown");
  assert.deepEqual(k.posted("/v1/knowledge/sources/s1/review")[0].body, { scope: { workspace_id: "w" }, expected_revision: 4, status: "active", trust_level: "reviewed" });
  assert.deepEqual(k.posted("/v1/knowledge/bindings")[0].body, { scope: { workspace_id: "w" }, binding_key: "s1:default", knowledge_source_id: "s1", specialist_profile_id: "default",
    specialist_profile_version_id: "v1", skill_id: "default.v1", trust_requirement: "reviewed", access_mode: "cite_required" });
  assert.deepEqual(k.posted("/v1/knowledge/bindings/nb/state")[0].body, { scope: { workspace_id: "w" }, expected_revision: 1, status: "active_synthetic" });
  assert.match(k.table().textContent, /이제 내 모든 AI가/);
  // a Word file travels as base64 with its own type
  await pick({ name: "guide.docx", size: 3, arrayBuffer: async () => new Uint8Array([80, 75, 3]).buffer }, "only");
  const word = k.posted("/v1/knowledge/ingest")[2].body;
  assert.equal(word.mime_type, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"); assert.equal(word.content_base64, "UEsD"); assert.equal(word.content, undefined);
  // the same text again on a reviewed source: no second review, only the shared switch
  await pick(file("b.txt", "same"), "use");
  assert.match(k.table().textContent, /같은 내용이에요/);
  assert.equal(k.posted("/v1/knowledge/sources/s2/review").length, 0);
  assert.equal(k.posted("/v1/knowledge/bindings")[1].body.knowledge_source_id, "s2");
  // one upload button: the side panel only says which files work
  assert.equal(dom.buttons(k.table(), "+ 파일 올리기").length, 0);
  assert.match(k.table().textContent, /올릴 수 있는 파일: Word/);
  await k.press(k.detail(), "새 버전 올리기");
  assert.equal(k.files.length, 11);
});

test("source filters, search typing, format groups and paging with a retry", async () => {
  let fail = false;
  const k = await setup({ "/v1/knowledge/sources/query": (_, body) => {
    if (fail) throw new Error("HTTP 500");
    const all = sources().filter((s) => !body.q || s.display_name.includes(body.q));
    const page = body.cursor ? all.slice(2) : all.slice(0, 2);
    return { items: page, next_cursor: body.cursor ? "" : "c2", authority: "data_only_untrusted", total: all.length };
  } });
  await k.start();
  // an older server sends no facets: 전체 falls back to the total
  assert.match(k.table().querySelector(".v4k-group").textContent, /전체모든 형식3/);
  assert.match(k.table().textContent, /전체 3개 중 2개 표시/);
  // a failing next page keeps the list and offers a retry
  fail = true;
  await k.press(k.table(), "자료 더 보기");
  assert.match(k.table().textContent, /자료를 더 불러오지 못했어요/);
  assert.match(k.table().textContent, /전체 3개 중 2개 표시/);
  fail = false;
  await k.press(k.table(), "다시 시도");
  assert.doesNotMatch(k.table().textContent, /불러오지 못했어요/);
  assert.match(k.table().textContent, /전체 3개 중 3개 표시/);
  const [status] = k.table().querySelectorAll("select");
  status.value = "실패"; dom.fire(status, "change"); await dom.flush();
  assert.equal(k.srv.calls.at(-1).body.state, "실패");
  k.table().querySelectorAll("select")[1].value = "name"; dom.fire(k.table().querySelectorAll("select")[1], "change"); await dom.flush();
  assert.equal(k.srv.calls.at(-1).body.sort, "name");
  k.table().querySelector(".v4k-group").click(); await dom.flush();
  assert.equal(k.srv.calls.at(-1).body.format, "");
  const input = k.table().querySelector("input[type=search]");
  input.value = " b. "; dom.fire(input, "input"); dom.fire(input, "input");
  await wait(300); await dom.flush();
  assert.equal(k.srv.calls.at(-1).body.q, "b.");
  assert.match(k.table().textContent, /전체 1개 중 1개/);
});

test("server states: off, error, wiki off and empty lists", async () => {
  for (const [msg, want] of [["HTTP 404", /지식 기능이 꺼져/], ["boom", /지식을 불러오지 못했어요/]]) {
    const k = await setup({ "/v1/knowledge/sources/query": () => { throw new Error(msg); }, "/v1/wiki/pages": () => { throw new Error(msg); }, "/v1/wiki/graph": () => ({}), "/v1/wiki/jobs": () => { throw new Error("x"); } });
    await k.start();
    assert.match(k.table().textContent, want);
    assert.equal(k.heads.at(-1).count, "");
  }
  const k = await setup({ "/v1/knowledge/sources/query": () => ({ items: [], next_cursor: "", authority: "data_only_untrusted", total: 0 }), "/v1/wiki/pages": () => { throw new Error("access_denied"); } });
  await k.start();
  assert.match(k.table().textContent, /아직 올린 자료가 없어요/);
  assert.equal(k.detail().childNodes.length, 0);
  k.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  assert.match(k.table().textContent, /이 서버는 아직 위키를 지원하지 않아요/);
  const k2 = await setup({ "/v1/wiki/pages": () => ({ items: "x" }) });
  await k2.start();
  k2.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  assert.match(k2.table().textContent, /위키를 불러오지 못했어요/);
  const k3 = await setup({ "/v1/wiki/pages": () => ({ items: [] }) });
  await k3.start();
  k3.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  assert.match(k3.table().textContent, /아직 위키가 없어요/);
  // while loading the wiki tab says so; a view that is not shown does not paint
  const k4 = await setup({}, { view: "other" });
  k4.view.views.knowledge.render();
  assert.match(k4.table().textContent, /불러오는 중/);
  await dom.flush();
  assert.match(k4.table().textContent, /불러오는 중/);
});

test("remove from wiki: dry-run preview, plain confirm on an old server, error and cancel", async () => {
  const k = await setup();
  await k.start();
  await k.press(k.detail(), "위키에서 빼기");
  assert.equal(k.posted("/v1/wiki/sources/s1/remove?dry_run=1")[0].body.dry_run, true);
  assert.match(k.detail().textContent, /페이지 1쪽이 지워지고, 2쪽은 출처만/);
  await k.press(k.detail(), "취소");
  assert.doesNotMatch(k.detail().textContent, /되돌릴 수 없어요/);
  await k.press(k.detail(), "위키에서 빼기");
  dom.buttons(k.detail(), "빼기").find((b) => b.textContent === "빼기").click(); await dom.flush();
  assert.equal(k.posted("/v1/wiki/sources/s1/remove").length, 1);
  assert.match(k.detail().textContent, /뺐어요 · 지움 1 · 출처만 뺌 0 · 링크를 글자로 1 · 끝낸 작업 0/);

  let mode = "old";
  const k2 = await setup({ "/v1/wiki/sources/": (path) => {
    if (path.includes("dry_run")) { if (mode === "old") throw new Error("HTTP 404"); if (mode === "bad") return {}; throw new Error("HTTP 500"); }
    return {};
  } });
  await k2.start();
  await k2.press(k2.detail(), "위키에서 빼기");
  assert.match(k2.detail().textContent, /미리보기를 지원하지 않아요/);
  k2.confirms.answer = false;
  dom.buttons(k2.detail(), "빼기").find((b) => b.textContent === "빼기").click(); await dom.flush();
  assert.equal(k2.confirms.length, 1);
  assert.equal(k2.posted("/v1/wiki/sources/s1/remove").length, 0);
  k2.confirms.answer = true;
  dom.buttons(k2.detail(), "빼기").find((b) => b.textContent === "빼기").click(); await dom.flush();
  assert.equal(k2.posted("/v1/wiki/sources/s1/remove").length, 1);
  assert.match(k2.detail().textContent, /뺐어요 · 지움 0/);
  for (mode of ["bad", "err"]) {
    await k2.press(k2.detail(), "위키에서 빼기");
    assert.match(k2.detail().textContent, /미리 보지 못했어요/);
    await k2.press(k2.detail(), "닫기");
    assert.doesNotMatch(k2.detail().textContent, /미리 보지 못했어요/);
  }
  // while the preview is pending the card says it is counting
  let release;
  const k3 = await setup({ "/v1/wiki/sources/": () => new Promise((r) => { release = r; }) });
  await k3.start();
  await k3.press(k3.detail(), "위키에서 빼기");
  assert.match(k3.detail().textContent, /미리 세어 보는 중/);
  release({ dry_run: true }); await dom.flush();
  assert.match(k3.detail().textContent, /페이지 0쪽/);
  // switching tabs drops the pending removal
  k3.table().querySelectorAll(".v4k-seg button")[0].click(); await dom.flush();
  assert.doesNotMatch(k3.detail().textContent, /페이지 0쪽/);
});

test("wiki list: attention, chips, type and sort filters, typed search and page detail actions", async () => {
  const k = await setup();
  await k.start();
  k.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  const t = () => k.table().textContent;
  assert.match(t(), /초안 2 · 출처가 바뀐 페이지 1/);
  assert.match(t(), /거절됨/);
  assert.match(t(), /5개 중 5개 표시/);
  assert.match(t(), /AI가 만듦/); assert.match(t(), /AI가 고침/); assert.match(t(), /잠금/);
  // first page is a fresh draft
  const d = () => k.detail().textContent;
  assert.equal(k.detail().firstChild.dataset.label, "위키 · 초안");
  assert.match(d(), /AI가 새로 만든 초안이에요/);
  assert.match(d(), /개체/); assert.match(d(), /새로 만듦/);
  assert.match(d(), /나가는 2 · 들어오는 1/);
  assert.match(d(), /목록에 없는 자료/);
  // a missing [[link]] stays dim, a known one opens its page
  const ghost = k.detail().querySelector(".v4k-wlink.missing"); assert.equal(ghost.title, "아직 없는 페이지예요");
  assert.equal(k.detail().querySelector(".v4k-wchip.missing").title, "아직 없는 페이지예요");
  k.detail().querySelector(".v4k-wchip:not(.missing)").click(); await dom.flush();
  assert.match(d(), /AI가 고친 초안이에요/); assert.match(d(), /검토본 있음/);
  await k.press(k.detail(), "검토본과 비교");
  assert.match(d(), /지금 보이는 글old body/); assert.match(d(), /AI 제안new \[\[First\]\]/);
  k.detail().querySelector(".v4k-wlink").click(); await dom.flush();
  assert.match(d(), /AI가 새로 만든 초안이에요/);
  k.table().querySelectorAll(".v4k-row")[1].click(); await dom.flush();
  await k.press(k.detail(), "검토본과 비교");
  await k.press(k.detail(), "닫기");
  assert.doesNotMatch(d(), /AI 제안/);
  // approve and reject
  await k.press(k.detail(), "검토됨으로 표시");
  assert.deepEqual(k.posted("/v1/wiki/pages/p2/review")[0].body, { scope: { workspace_id: "w" }, expected_revision: 2, decision: "approve" });
  assert.match(t(), /검토됨으로 표시했어요/);
  await k.press(k.detail(), "버리기");
  assert.equal(k.posted("/v1/wiki/pages/p2/review")[1].body.decision, "reject");
  // edit, cancel, edit and save
  await k.press(k.detail(), "고치기");
  assert.equal(k.detail().querySelector("textarea").value, "new [[First]]");
  await k.press(k.detail(), "취소");
  assert.equal(k.detail().querySelector("textarea"), null);
  await k.press(k.detail(), "고치기");
  dom.input(k.detail().querySelector("input.v4s-input"), "  Renamed ");
  dom.input(k.detail().querySelector("textarea"), "fresh");
  await k.press(k.detail(), "저장하고 잠그기");
  assert.deepEqual(k.posted("/v1/wiki/pages/p2/edit")[0].body, { scope: { workspace_id: "w" }, expected_revision: 2, title: "Renamed", text: "fresh" });
  assert.match(t(), /저장했어요/);
  // lock toggle
  await k.press(k.detail(), "잠그기");
  assert.equal(k.posted("/v1/wiki/pages/p2/lock")[0].body.locked, true);
  // source button in the page detail goes back to that source
  k.detail().querySelector(".v4k-src").click(); await dom.flush();
  assert.equal(k.detail().firstChild.dataset.label, "문서 · .md");
  k.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  // chips
  const chip = (label) => dom.buttons(k.table(), label).find((b) => b.classList.contains("chip"));
  chip("잠금").click(); await dom.flush();
  assert.match(t(), /5개 중 1개 표시/);
  k.table().querySelector(".v4k-row").click(); await dom.flush();
  assert.match(d(), /잠겨 있어요/);
  await k.press(k.detail(), "잠금 풀기");
  assert.equal(k.posted("/v1/wiki/pages/p3/lock")[0].body.locked, false);
  chip("거절됨").click(); await dom.flush();
  assert.match(t(), /5개 중 1개 표시/);
  k.table().querySelector(".v4k-row").click(); await dom.flush();
  assert.equal(k.detail().firstChild.dataset.label, "위키 · 거절됨");
  assert.match(d(), /출처가 없어요/); assert.match(d(), /나가는 링크가 없어요/);
  chip("검토됨").click(); await dom.flush();
  assert.match(t(), /5개 중 2개 표시/);
  // stale page: attention row opens drafts first; a stale page asks for an edit
  const stale = k.table().querySelectorAll(".v4k-row")[1]; stale.click(); await dom.flush();
  assert.match(d(), /지워진 자료의 내용이 남아/); assert.match(d(), /지워진 자료 · gone/);
  await k.press(k.detail(), "고치기");
  assert.equal(k.detail().querySelector("textarea").value, "zzz");
  chip("전체").click(); await dom.flush();
  // type and sort selects, typed search
  const [type] = k.table().querySelectorAll("select");
  type.value = "concept"; dom.fire(type, "change"); await dom.flush();
  assert.match(t(), /5개 중 2개 표시/);
  k.table().querySelectorAll("select")[1].value = "title"; dom.fire(k.table().querySelectorAll("select")[1], "change"); await dom.flush();
  assert.deepEqual(k.table().querySelectorAll(".v4k-row .v4s-title").map((n) => n.textContent), ["Locked", "Page Two"]);
  k.table().querySelectorAll("select")[0].value = ""; dom.fire(k.table().querySelectorAll("select")[0], "change"); await dom.flush();
  const input = k.table().querySelector("input[type=search]");
  input.value = "ZZZ"; dom.fire(input, "input"); await wait(300); await dom.flush();
  assert.match(t(), /5개 중 1개 표시/);
  k.table().querySelector("input[type=search]").value = "nothing"; dom.fire(k.table().querySelector("input[type=search]"), "input"); await wait(300); await dom.flush();
  assert.match(t(), /조건에 맞는 페이지가 없어요/);
  assert.equal(k.detail().textContent.length > 0, true);
  // attention button opens a draft
  await k.press(k.table(), "하나씩 검토하기");
  assert.match(d(), /초안이에요/);
});

test("wiki attention without drafts opens a stale page; reset returns to idle and reloads", async () => {
  const k = await setup({ "/v1/wiki/pages": () => ({ items: pages().filter((p) => p.review_state !== "draft") }) });
  await k.start();
  k.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  assert.match(k.table().textContent, /출처가 바뀐 페이지 1/);
  await k.press(k.table(), "하나씩 검토하기");
  assert.match(k.detail().textContent, /지워진 자료/);
  // reset forgets the list and the next render reads the server again
  k.srv.db.sources = [];
  const before = k.srv.calls.length;
  k.view.reset();
  await k.start();
  assert.ok(k.srv.calls.length > before);
  assert.equal(k.heads.at(-1).count, "자료 0 · 위키 3");
});

test("selection is dropped when the selected source and page are gone after a reload", async () => {
  let empty = false;
  const k = await setup({
    "/v1/knowledge/sources/query": () => ({ items: empty ? [] : sources(), next_cursor: "", authority: "data_only_untrusted", total: empty ? 0 : 3 }),
    "/v1/wiki/pages": () => ({ items: empty ? [] : pages() })
  });
  await k.start();
  k.table().querySelectorAll(".v4k-seg button")[1].click(); await dom.flush();
  assert.match(k.detail().textContent, /초안이에요/);
  empty = true;
  await k.press(k.detail(), "검토됨으로 표시");
  assert.match(k.table().textContent, /아직 위키가 없어요/);
  assert.equal(k.detail().childNodes.length, 0);
  k.table().querySelectorAll(".v4k-seg button")[0].click(); await dom.flush();
  assert.match(k.table().textContent, /아직 올린 자료가 없어요/);
});

test("a busy action ignores a second click and job lines cover pending, done and result-less runs", async () => {
  let release;
  const k = await setup({
    "/v1/knowledge/index": () => new Promise((r) => { release = r; }),
    "/v1/wiki/jobs?": () => ({ items: [{ id: "j2", status: "pending", updated_at: T, job: { knowledge_source_id: "s1", error: "revision_conflict", result: { rejected: [] } } },
      { id: "j3", status: "weird", updated_at: T, job: { knowledge_source_id: "s2", error: "other" } }] }),
    "/v1/wiki/jobs/": () => { throw new Error("payload_too_large"); },
    "/v1/wiki/jobs": () => ({ duplicate: true })
  });
  await k.start();
  assert.match(k.detail().textContent, /자료가 바뀌어 멈췄어요/);
  await k.press(k.detail(), "지금 실행");
  assert.match(k.detail().textContent, /파일이 너무 커요/);
  k.btn(k.detail(), "벡터 색인").click(); await dom.flush();
  assert.equal(k.btn(k.detail(), "새 버전 올리기").disabled, true);
  const calls = k.posted("/v1/knowledge/index").length;
  // a second action while busy does nothing
  k.btn(k.detail(), "확인했어요 · 내 AI가 쓰게 하기").disabled = false;
  k.btn(k.detail(), "확인했어요 · 내 AI가 쓰게 하기").click(); await dom.flush();
  assert.equal(k.posted("/v1/knowledge/sources/s1/review").length, 0);
  release({}); await dom.flush();
  assert.equal(calls, 1);
  assert.match(k.detail().textContent, /색인했어요 \(새로 0 · 그대로 0 · 실패 0\)/);
  k.table().querySelectorAll(".v4k-row")[1].click(); await dom.flush();
  assert.match(k.detail().textContent, /weird/); assert.match(k.detail().textContent, /만들지 못했어요/);
  k.table().querySelectorAll(".v4k-row")[0].click(); await dom.flush();
  k.btn(k.detail(), "위키 만들기").disabled = false;
  await k.press(k.detail(), "위키 만들기");
  assert.match(k.detail().textContent, /이미 있어요/);
  // keyboard: space opens a row, other keys do not
  const row = k.table().querySelectorAll(".v4k-row")[2];
  row.dispatchEvent(Object.assign(new dom.Event("keydown"), { key: "x" }));
  row.dispatchEvent(Object.assign(new dom.Event("keydown"), { key: " " })); await dom.flush();
  assert.equal(k.detail().firstChild.dataset.label, "문서 · 파일");
});

test("splitText: parts stay under the byte cap, cut at blank lines, then lines, then characters", () => {
  require("./knowledge.js");
  const split = window.ArkismKnowledge.splitText, bytes = (x) => Buffer.byteLength(x);
  assert.deepEqual(split("short"), ["short"]);
  // paragraphs pack greedily and are never cut inside when they fit
  const para = (c) => c.repeat(40) + "\n\n";
  const parts = split(para("a") + para("b") + para("c"), 90);
  assert.deepEqual(parts, [para("a") + para("b"), para("c")]);
  // a paragraph too big for one part falls back to line ends
  const lines = "l1".repeat(20) + "\n" + "l2".repeat(20) + "\n";
  assert.deepEqual(split(lines, 50), ["l1".repeat(20) + "\n", "l2".repeat(20) + "\n"]);
  // one huge line is cut between characters, never inside a multi-byte one (한글 3 bytes, emoji 4)
  const wide = "가😀".repeat(30);
  const cut = split(wide, 20);
  assert.equal(cut.join(""), wide);
  for (const c of cut) { assert.ok(bytes(c) <= 20); assert.equal(Buffer.from(c).toString(), c); }
  // nothing is lost, only space-only parts are dropped; 64KB is the default cap
  const big = Array.from({ length: 300 }, (_, i) => `문단 ${i} ` + "내용".repeat(80)).join("\n\n");
  const def = split(big);
  assert.ok(def.length > 1);
  assert.equal(def.join(""), big);
  for (const c of def) assert.ok(bytes(c) <= 64 << 10);
  assert.deepEqual(split("a".repeat(10) + "\n\n   \n\n", 12), ["a".repeat(10) + "\n\n"]);
});

test("upload: text over 64KB goes up in parts with one progress line and a plain partial failure", async () => {
  const seen = [];
  const k = await setup({ "/v1/knowledge/ingest": (_, body) => {
    seen.push(k.table().textContent.match(/나눠서 올리는 중… \d\/\d/)?.[0]);
    if (body.content.startsWith("bad")) throw new Error("secret_detected");
    return { source: { id: "p" + body.filename, revision: 1 } };
  } });
  await k.start();
  const add = k.view.views.knowledge.head()[0];
  const para = (c) => c + "x".repeat((40 << 10) - c.length) + "\n\n";
  const pick = async (content, choice) => {
    add.click(); const input = k.files.at(-1); input.files = [file("big.md", content)]; dom.fire(input, "change"); await dom.flush();
    const dlg = k.document.body.querySelector("dialog.v4k-ask");
    assert.match(dlg.textContent, /64KB가 넘어서 문단 단위로 3개로 나눠 올려요/);
    dlg.close(choice); await dom.flush(); await dom.flush();
  };
  await pick(para("a") + para("b") + para("c"), "only");
  const sent = k.posted("/v1/knowledge/ingest").map((c) => c.body);
  assert.deepEqual(sent.map((b) => [b.source_key, b.filename, b.mime_type]), [1, 2, 3].map((i) => [`big (${i}／3).md`, `big (${i}／3).md`, "text/markdown"]));
  assert.deepEqual(seen, ["나눠서 올리는 중… 1/3", "나눠서 올리는 중… 2/3", "나눠서 올리는 중… 3/3"]);
  assert.match(k.table().textContent, /3개로 나눠 올렸어요. 자료를 열어 확인하고/);
  assert.equal(k.posted("/v1/knowledge/bindings").length, 0);
  // one part refused: the rest still go up and the end says which part and why
  await pick(para("a") + para("bad") + para("c"), "use");
  assert.equal(k.posted("/v1/knowledge/ingest").length, 6);
  assert.match(k.table().textContent, /3개 중 2개를 올렸어요. 못 올린 부분은 2번째예요. 비밀값이 들어 있어 받지 않았어요./);
  assert.equal(k.posted("/v1/knowledge/bindings").length, 2);
  // every part refused: one plain error, no count line
  await pick(para("bad") + para("bad") + para("bad"), "only");
  assert.match(k.table().textContent, /비밀값이 들어 있어 받지 않았어요/);
  assert.doesNotMatch(k.table().textContent, /개를 올렸어요/);
  // a new version of one source cannot be split
  await k.press(k.detail(), "새 버전 올리기");
  const input = k.files.at(-1); input.files = [file("big.md", para("a") + para("b"))]; dom.fire(input, "change"); await dom.flush();
  assert.match(k.table().textContent, /새 버전은 64KB까지/);
  assert.equal(k.document.body.querySelector("dialog.v4k-ask"), null);
});
