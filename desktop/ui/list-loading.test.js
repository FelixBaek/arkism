const test = require("node:test"), assert = require("node:assert/strict");
require("./keep.js"); // the shared stale-reply and redraw guards the screens use
const { create: createPager } = require("./memory-pages.js");
const page = (items, next_cursor = "") => ({ items, next_cursor, authority: "data_only_untrusted" });

test("refresh retains the number of sparse, duplicate and withheld pages actually read", async () => {
  const calls = [];
  const pager = createPager(async (_, { cursor }) => {
    calls.push(cursor);
    if (!cursor) return page([], "empty");
    if (cursor === "empty") return page([{ id: "a" }], "duplicate");
    return page([{ id: "a" }, { id: "b" }]);
  });
  await pager.load("x"); await pager.load("x"); await pager.load("x");
  assert.deepEqual(pager.snapshot().items.map(r => r.id), ["a", "b"]);
  await pager.refresh("x");
  assert.deepEqual(calls, ["", "empty", "duplicate", "", "empty", "duplicate"]);
});

test("a cursor cycle across separate more clicks is rejected before publication", async () => {
  const pager = createPager(async (_, { cursor }) => page([{ id: cursor || "first" }], cursor === "second" ? "first" : cursor === "first" ? "second" : "first"));
  await pager.load("x"); await pager.load("x");
  await assert.rejects(pager.load("x"), /memory_cursor_repeated/);
  assert.equal(pager.snapshot().cursor, "second");
  assert.equal(pager.snapshot().items.length, 1);
});

test("each collection reads one page, refreshes only opened pages and marks trash", async () => {
  const { createCollectionPaging } = await import("./modules/collectionPaging.mjs");
  const calls = [], state = { server: "local", scope: { project_id: "one" }, connection: { generation: 1 }, data: {} };
  const api = createCollectionPaging({ state, createPager, request: async path => {
    const url = new URL(path, "http://fixture"); calls.push(url);
    return page([{ id: url.searchParams.get("cursor") || "first" }], url.searchParams.has("cursor") ? "" : "next");
  } });
  for (const name of ["candidates", "conflicted", "skills", "skillTrash"]) await api.read(name);
  assert.equal(calls.length, 4);
  assert.ok(calls.every(url => url.searchParams.get("limit") === "50" && !url.searchParams.has("cursor")));
  await api.more("skills"); assert.equal(state.data.skills.length, 2);
  assert.equal(state.versionsDone, false);
  await api.read("skills", true); assert.equal(calls.length, 7);
  assert.equal(api.snapshot("skillTrash").items[0].status, "deprecated");
  assert.equal(calls[0].searchParams.get("status"), "candidate");
  assert.equal(calls[1].searchParams.get("status"), "conflicted");
});

test("old collection responses cannot publish after a scope change or reset", async () => {
  const { createCollectionPaging } = await import("./modules/collectionPaging.mjs");
  const state = { server: "local", scope: {}, connection: { generation: 1 }, data: {} };
  let release;
  const api = createCollectionPaging({ state, createPager, request: () => new Promise(resolve => { release = resolve; }) });
  const pending = api.read("skills");
  state.scope = { project_id: "different" };
  release(page([{ id: "private" }]));
  await assert.rejects(pending, /api_context_changed/);
  assert.deepEqual(api.snapshot("skills").items, []);
  const more = api.more("skills"); api.reset(); state.connection.generation++;
  release(page([{ id: "locked" }])); await more;
  assert.equal(state.data.skills, undefined);
});

test("variable-height lookup agrees with actual cumulative heights, including edges", async () => {
  const { createHeightIndex } = await import("./modules/virtualList.mjs");
  const index = createHeightIndex(10000), heights = Array.from({ length: 10000 }, (_, i) => 28 + i % 131);
  heights.forEach((height, i) => index.measure(i, height));
  let total = 0;
  heights.forEach((height, i) => {
    assert.equal(index.offset(i), total);
    assert.equal(index.at(total), i);
    assert.equal(index.at(total + height - 0.1), i);
    total += height;
  });
  assert.equal(index.offset(heights.length), total);
  assert.equal(index.at(total + 1000), heights.length - 1);
  assert.equal(index.measure(-1, 30), 0);
  assert.equal(index.measure(1, NaN), 0);
});

test("background publication preserves newer pages, failed reads and unchanged version caches", async () => {
  const { publishRefresh } = await import("./modules/visibleSync.mjs");
  const skills = [{ id: "a", version_id: "v", updated_at: "same" }], newer = [{ id: "next" }];
  const state = { data: { skills, candidates: newer, tools: "old" }, versions: new Map([["v", "cached"]]), versionsDone: true };
  const reads = [{ key: "candidates", old: [] }, { key: "tools", old: "old" }, { key: "skills", old: skills }];
  const results = [{ status: "fulfilled", value: [] }, { status: "rejected", reason: Error("offline") }, { status: "fulfilled", value: [...skills] }];
  let invalidated = 0;
  assert.equal(publishRefresh(state, reads, results, () => invalidated++), false);
  assert.equal(state.data.candidates, newer); assert.equal(state.data.tools, "old");
  results[2].value = [...skills, { id: "b", version_id: "new" }];
  assert.equal(publishRefresh(state, reads, results, () => invalidated++), true);
  assert.equal(state.versions.get("v"), "cached");
  assert.equal(state.versionsDone, false); assert.equal(invalidated, 1);
});

test("visible refresh does not publish a response after navigation or overwrite an in-flight more", async () => {
  const { createVisibleSync } = await import("./modules/visibleSync.mjs");
  const savedDocument = global.document, savedWindow = global.window;
  global.document = { hidden: false, activeElement: { id: "search" }, querySelector: () => null };
  global.window = {};
  try {
    const state = { token: "fixture", loading: false, pending: new Map(), view: "skills", connection: { generation: 1 }, data: { skills: [], skillTrash: [], tools: {} }, versions: new Map() };
    const releases = [], reads = []; let renders = 0;
    const sync = createVisibleSync({ state, memoryPaging: {}, collectionPaging: { read: (key, refresh) => { reads.push([key, refresh]); return new Promise(resolve => releases.push(resolve)); } }, request: async () => ({ items: [] }), query: () => "", loadTools: async () => state.data.tools, render: () => renders++, invalidateVersions: () => {} });
    const pending = sync(); state.view = "crew";
    releases.forEach(resolve => resolve([{ id: "old" }])); await pending;
    assert.deepEqual(reads, [["skills", true], ["skillTrash", true]]);
    assert.deepEqual(state.data.skills, []); assert.equal(renders, 0);
  } finally { global.document = savedDocument; global.window = savedWindow; }
});

test("chunked similarity preserves exhaustive ranking, dismissed pairs, threshold and stable ties", async () => {
  const { createSkillSimilarity } = await import("./modules/skillSimilarity.mjs");
  const skills = Array.from({ length: 185 }, (_, i) => ({ id: String(i), name: `group-${i % 9}-${i}`, description: ["문서를 간결하게 작성하는 작업", "브라우저로 페이지를 검증한다", "", "write readable documents"][i % 4] + (i % 3 ? "" : " 추가") }));
  const dismissed = ["0|4", "1|5"];
  // Deliberately use an independent, exhaustive multiset calculation.
  function terms(skill) {
    const text = skill.description.toLowerCase().replace(/[^a-z0-9가-힣]/g, "");
    return { pairs: Array.from({ length: Math.max(0, text.length - 1) }, (_, i) => text.slice(i, i + 2)), words: new Set(skill.name.split(/[-_]/).filter(word => word.length > 1)) };
  }
  const features = skills.map(terms), expected = [];
  for (let i = 0; i < skills.length; i++) for (let j = i + 1; j < skills.length; j++) {
    const a = features[i], b = features[j], remaining = [...b.pairs]; let common = 0;
    for (const pair of a.pairs) { const at = remaining.indexOf(pair); if (at >= 0) { common++; remaining.splice(at, 1); } }
    const words = [...a.words].filter(word => b.words.has(word)).length;
    const score = Math.max(a.pairs.length + b.pairs.length ? 2 * common / (a.pairs.length + b.pairs.length) : 0, words / (a.words.size + b.words.size - words) || 0);
    const key = [skills[i].id, skills[j].id].sort().join("|");
    if (score >= 0.6 && !dismissed.includes(key)) expected.push({ key, score });
  }
  expected.sort((a, b) => b.score - a.score);
  let complete; const done = new Promise(resolve => { complete = resolve; });
  const engine = createSkillSimilarity({ notify: complete, pause: () => Promise.resolve() });
  engine.read(skills, dismissed); await done;
  assert.deepEqual(engine.read(skills, dismissed).map(({ key, score }) => ({ key, score })), expected.slice(0, 20));
  assert.equal(engine.busy, false);
});

test("large similarity work yields and a lock/reset suppresses its late result", async () => {
  const { createSkillSimilarity } = await import("./modules/skillSimilarity.mjs");
  let release, notices = 0;
  const engine = createSkillSimilarity({ notify: () => notices++, pause: () => new Promise(resolve => { release = resolve; }) });
  const skills = Array.from({ length: 1000 }, (_, i) => ({ id: String(i), name: `similar-${i}`, description: "같은 설명으로 매우 많은 후보가 생기는 스킬입니다. ".repeat(10) }));
  assert.deepEqual(engine.read(skills, []), []);
  assert.equal(engine.busy, true); assert.equal(typeof release, "function");
  engine.reset(); release(); await new Promise(resolve => setImmediate(resolve));
  assert.equal(notices, 0); assert.equal(engine.busy, false);
});
