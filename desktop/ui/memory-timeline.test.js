require("./keep.js");
const { test } = require("node:test");
const assert = require("node:assert/strict");
const { create } = require("./memory-pages.js");

// a tiny element stub: enough for the label line, timeline and provisional block
const el = (tag, cls = "", text = "") => {
  const n = { tag, className: cls, children: [], textContent: text, title: "",
    append(...xs) { for (const x of xs) if (x != null) { this.children.push(x); if (typeof x === "string") this.textContent += x; } } };
  return n;
};
const raw = (n) => { n.raw = true; return n; };
const walk = (n, out = []) => { if (n && typeof n === "object") { out.push(n); n.children.forEach((c) => walk(c, out)); } return out; };
const texts = (n) => walk(n).filter((x) => x.className === "mem-label").map((x) => x.textContent);

test("labels: provisional, correction, temporary, rule, decision, relations and path_missing in order", async () => {
  const { memoryLabels, labelLine } = await import("./modules/memoryLabels.mjs");
  const m = { id: "m1", status: "active", conflict_id: "c1", tags: ["temporary", "rule:never", "decision_status:confirmed", "decision_status:bogus"],
    entity_relations: [{ source: "api", relation: "uses", target: "db" }, { source: "", relation: "x", target: "y" }] };
  const maintenance = [{ maintenance: { type: "invalidation", reason: "path_missing", memory: { id: "m1" } } }, { maintenance: { type: "invalidation", reason: "path_missing", memory: { id: "other" } } }];
  const labels = memoryLabels(m, { maintenance, provisional: true });
  assert.deepEqual(labels.map((l) => l.text), ["임시 · 승인 전", "정정 검토 중", "이번만 · 7일 뒤 만료", "규칙: 하지 않기", "결정: 확정", "관계 1개", "파일 경로 없음"]);
  assert.equal(labels[5].title, "api → uses → db");
  assert.deepEqual(memoryLabels({ id: "x" }, { maintenance: null }), []); // a list loaded before maintenance has none
  assert.equal(labelLine([], { el }), null);
  assert.deepEqual(texts(labelLine(labels, { el })), labels.map((l) => l.text));
});

test("timeline: newest first, each revision ends where the next began, a waiting correction on top", async () => {
  const { timelineEntries, timelineList } = await import("./modules/memoryTimeline.mjs");
  const m = { text: "now", memory_context: { valid_from: "2026-10-01T00:00:00Z" }, approved_by: "felix", sources: [{}, {}] };
  const lineage = { links: [
    { relation: "supersedes", record: { text: "old", effective_at: "2026-09-01T00:00:00Z", review: { by: "ann" } } },
    { relation: "causes", record: { text: "ignored" } }] };
  const rows = timelineEntries(m, lineage, { text: "fix" });
  assert.deepEqual(rows.map((r) => r.kind), ["pending", "current", "previous"]);
  assert.equal(rows[2].to, "2026-10-01T00:00:00Z"); // the old one held until the current began
  assert.equal(rows[1].to, ""); // the current one holds until now
  assert.equal(rows[1].approver, "felix"); assert.equal(rows[1].evidence, 2); assert.equal(rows[2].approver, "ann");
  assert.equal(timelineEntries({ text: "a", valid_to_proposal: { at: "2026-12-01T00:00:00Z" } }, null)[0].to, "2026-12-01T00:00:00Z");
  const ol = timelineList(rows, { el, raw, shortDate: (v) => v.slice(0, 10) });
  const when = walk(ol).filter((x) => x.className === "mem-tl-when").map((x) => x.textContent);
  assert.deepEqual(when, ["승인 전", "2026-10-01 → 지금", "2026-09-01 → 2026-10-01"]);
  assert.ok(walk(ol).some((x) => x.raw && x.textContent === "felix")); // the approver id is data, not translated
});

test("시점: the memory list asks /v1/memories/at for a past day and opens rows at revision 0", async () => {
  const { createCatalogQuery, endOfDay } = await import("./modules/catalogQuery.mjs");
  const state = { server: "s", scope: { workspace_id: "w", project_id: "p" }, connection: { generation: 1 }, view: "memories", memTab: "active", kind: "user", repo: "r", sort: "old", query: "deploy", validAt: "2026-09-15", validAsOf: "2026-11-01T00:00:00.000Z" };
  const calls = [];
  const c = createCatalogQuery({ state, createPager: create, preferences: () => ({ kept: [] }), render() {}, request: async (path, init) => {
    calls.push({ path, body: JSON.parse(init.body) });
    return { as_of: state.validAsOf, items: [{ id: "a", text: "then", kind: "user", observed_at: "2026-09-01T00:00:00Z", current_revision: 4 }], unknown: [], next_cursor: "", authority: "data_only_untrusted" };
  } });
  await c.more("memories");
  assert.equal(calls[0].path, "/v1/memories/at");
  assert.deepEqual(calls[0].body, { scope: state.scope, q: "deploy", kind: "user", as_of: state.validAsOf, valid_at: endOfDay("2026-09-15"), limit: 50 });
  const row = c.snapshot("memories").items[0];
  assert.equal(row.revision, 0); assert.equal(row.status, "active"); assert.equal(row.created_at, "2026-09-01T00:00:00Z");
  c.pin("a", { id: "a", revision: 4, text: "now" }); assert.equal(c.record("a").text, "now"); // the re-read current record wins
  state.validAt = ""; calls.length = 0;
  await c.more("memories").catch(() => {});
  assert.equal(calls[0].path, "/v1/memories/query"); // empty = now
});

test("recall answers show their provisional candidates apart, labelled", async () => {
  const { provisionalBlock } = await import("./modules/memoryLabels.mjs");
  assert.equal(provisionalBlock({ items: [{ text: "x" }] }, { el, raw }), null);
  const box = provisionalBlock({ provisional: [{ id: "c", text: "pending fact", tags: ["temporary"] }, { id: "empty" }] }, { el, raw });
  assert.deepEqual(texts(box), ["임시 · 승인 전", "이번만 · 7일 뒤 만료"]);
  assert.ok(walk(box).some((x) => x.raw && x.textContent === "pending fact"));
});

test("시점 picker: a picked day comes with a past as_of; 지금 clears both; a page without a total is accepted as unknown", async () => {
  const { whenPick } = await import("./modules/memoryTimeline.mjs");
  const { createRemoteCollection } = await import("./modules/remoteCollection.mjs");
  const picks = [], handlers = {};
  const node = (tag, cls = "", text = "") => ({ ...el(tag, cls, text), setAttribute() {}, addEventListener(k, f) { handlers[k] = f; } });
  const button = (text, _c, _i, f) => ({ ...el("button", "", text), click: f });
  const box = whenPick("", (d, at) => picks.push([d, at]), { el: node, button });
  const input = box.children[1]; input.value = "2026-09-15"; handlers.change();
  assert.equal(picks[0][0], "2026-09-15"); assert.ok(Date.parse(picks[0][1]) < Date.now());
  const on = whenPick("2026-09-15", (d, at) => picks.push([d, at]), { el: node, button });
  assert.match(on.className, /\bon\b/); on.children[2].click(); assert.deepEqual(picks[1], ["", ""]);
  const state = { server: "s", scope: {}, connection: { generation: 1 } };
  const c = createRemoteCollection({ state, createPager: create, read: async () => ({ items: [{ id: "a" }], total: null, next_cursor: "", authority: "data_only_untrusted" }) });
  assert.equal((await c.load({})).total, null);
  const bad = createRemoteCollection({ state, createPager: create, read: async () => ({ items: [], next_cursor: "", authority: "data_only_untrusted" }) });
  await assert.rejects(bad.load({}), /invalid/); // a missing total is still refused; only an explicit null means unknown
});
