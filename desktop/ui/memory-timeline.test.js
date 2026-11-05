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

test("a list row shows one label: 정정 검토 중 > 임시 · 승인 전 > 이번만 > 파일 경로 없음; 규칙·결정·관계 never", async () => {
  const { rowLabels } = await import("./modules/memoryLabels.mjs");
  const L = (...t) => t.map((text) => ({ text }));
  assert.deepEqual(rowLabels(L("임시 · 승인 전", "정정 검토 중", "이번만 · 7일 뒤 만료", "파일 경로 없음")).map((l) => l.text), ["정정 검토 중"]);
  assert.deepEqual(rowLabels(L("임시 · 승인 전", "이번만 · 7일 뒤 만료")).map((l) => l.text), ["임시 · 승인 전"]);
  assert.deepEqual(rowLabels(L("규칙: 항상", "파일 경로 없음", "이번만 · 7일 뒤 만료")).map((l) => l.text), ["이번만 · 7일 뒤 만료"]);
  assert.deepEqual(rowLabels(L("규칙: 항상", "결정: 확정", "관계 1개", "파일 경로 없음")).map((l) => l.text), ["파일 경로 없음"]);
  assert.deepEqual(rowLabels(L("규칙: 항상", "결정: 확정", "관계 1개")), []);
});

test("timeline: newest first, each revision ends where the next began, a waiting correction on top", async () => {
  const { timelineEntries } = await import("./modules/memoryTimeline.mjs");
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
});

// n versions, newest first like timelineEntries: v0 is the current one, v1… older, one source more each
const versions = (n, pending = false) => [...(pending ? [{ kind: "pending", title: "검토 중", text: "fix", from: "2026-10-20T00:00:00Z", to: "", approver: "", evidence: 0 }] : []),
  ...Array.from({ length: n }, (_, i) => ({ kind: i ? "previous" : "current", title: i ? "이전 내용" : "지금 내용", text: `v${i}`,
    from: new Date(Date.UTC(2026, 9, 13 - i)).toISOString(), to: "", approver: i ? "" : "felix", evidence: i }))];

test("timeline layout: oldest left, labels alternate up and down, same-side labels never share a column, >5 keeps the newest 5", async () => {
  const { timelineLayout } = await import("./modules/memoryTimeline.mjs");
  for (const n of [2, 3, 5]) {
    const { hidden, dots } = timelineLayout(versions(n));
    assert.equal(hidden, 0);
    assert.deepEqual(dots.map((d) => d.row.text), Array.from({ length: n }, (_, i) => `v${n - 1 - i}`), "oldest left, newest right");
    assert.deepEqual(dots.map((d) => d.up), dots.map((_, i) => i % 2 === 0));
    for (const d of dots) assert.ok(d.from >= 1 && d.to <= 2 * n + 1 && d.from < d.to, `span inside the grid (${n})`);
    for (let i = 0; i + 2 < n; i++) assert.ok(dots[i].to <= dots[i + 2].from, `labels ${i} and ${i + 2} on one side do not overlap (${n})`);
    assert.equal(dots.at(-1).size, 18, "the current version is the largest");
    assert.ok(dots.slice(0, -1).every((d) => d.size >= 8 && d.size < 18));
  }
  const many = timelineLayout(versions(7, true));
  assert.equal(many.hidden, 3);
  assert.deepEqual(many.dots.map((d) => d.row.kind), ["previous", "previous", "previous", "current", "pending"], "the waiting correction is last");
  assert.equal(timelineLayout(versions(7, true), { all: true }).dots.length, 8);
  assert.equal(timelineLayout(versions(9)).dots[0].size, 16, "evidence size is clamped");
  assert.deepEqual(timelineLayout([...versions(2), { kind: "conflict", text: "x", from: "", evidence: 0 }]).dots.length, 2, "a conflict is another memory, not a version");
});

test("timeline view: dots are labelled buttons; a click or an arrow key shows that version; 이전 N개 shows them all; revert acts on the pick", async () => {
  const dom = require("./testdata/fake-dom.cjs");
  const { document } = dom.install();
  const h = dom.helpers(document);
  const { timelineView } = await import("./modules/memoryTimeline.mjs");
  const reverted = [];
  const box = timelineView(versions(7, true), { el: h.el, raw: h.raw, shortDate: (v) => v.slice(5, 10) }, { revert: (r) => reverted.push(r.text) });
  document.body.append(box);
  const dots = () => box.querySelectorAll(".mem-tlh-dot");
  const block = () => box.querySelector(".mem-tlh-version").textContent;
  assert.equal(dots().length, 5);
  assert.deepEqual(dots().map((d) => d.getAttribute("aria-label")), ["이전 내용 · 10-10", "이전 내용 · 10-11", "이전 내용 · 10-12", "지금 내용 · 10-13", "검토 중 · 10-20"]);
  assert.match(block(), /“v0”/); assert.match(block(), /승인 felix/); // default: the current version
  assert.equal(dom.buttons(box, "이전으로 되돌리기").length, 0, "nothing to go back to from the current one");
  assert.deepEqual(dots().map((d) => d.tabIndex), [-1, -1, -1, 0, -1], "one tab stop: the picked dot");
  dots()[2].click();
  assert.match(block(), /“v1”/); assert.match(block(), /근거 1개/);
  assert.equal(dots()[2].getAttribute("aria-pressed"), "true"); assert.match(box.querySelectorAll(".mem-tlh-label")[2].className, /\bon\b/);
  dom.buttons(box, "이전으로 되돌리기")[0].click(); assert.deepEqual(reverted, ["v1"]);
  dom.fire(dots()[2], "keydown", { key: "ArrowLeft" });
  assert.match(block(), /“v2”/); assert.equal(document.activeElement, dots()[1]);
  dom.fire(dots()[1], "keydown", { key: "End" });
  assert.match(block(), /사람이 고르기 전까지/); assert.ok(dots()[4].className.includes("pending"));
  const more = dom.buttons(box, "이전 3개")[0]; assert.ok(more);
  more.click();
  assert.equal(dots().length, 8); assert.equal(dom.buttons(box, "이전 3개").length, 0);
  // two versions: one label above, one below
  const two = timelineView(versions(2), { el: h.el, raw: h.raw, shortDate: (v) => v.slice(5, 10) });
  assert.deepEqual(two.querySelectorAll(".mem-tlh-label").map((l) => l.className.match(/up|down/)[0]), ["up", "down"]);
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
