// modules/skillSimilarity.mjs: small catalogs answer at once and cache; a newer read retires an older chunked one.
const test = require("node:test");
const assert = require("node:assert/strict");

test("small catalogs answer synchronously, skip deprecated skills and reuse the same answer", async () => {
  const { createSkillSimilarity } = await import("./modules/skillSimilarity.mjs");
  const engine = createSkillSimilarity({ notify() {} });
  const skills = [
    { id: "b", name: "doc-writer", description: "문서를 간결하게 작성한다" },
    { id: "a", name: "doc-writer-pro", description: "문서를 간결하게 작성한다" },
    { id: "c", name: "old", description: "문서를 간결하게 작성한다", status: "deprecated" },
    { id: "d", name: "x", description: "" },
  ];
  const first = engine.read(skills, []);
  assert.deepEqual(first.map((p) => p.key), ["a|b"]);
  assert.equal(engine.read(skills, []), first);
  assert.deepEqual(engine.read(skills, ["a|b"]), []);
  assert.equal(engine.busy, false);
});

test("only the best twenty pairs stay, and a newer read retires the chunked one before it finishes", async () => {
  const { createSkillSimilarity } = await import("./modules/skillSimilarity.mjs");
  let notified = 0;
  const engine = createSkillSimilarity({ notify: () => notified++ }); // the default pause yields with setTimeout
  const many = Array.from({ length: 200 }, (_, i) => ({ id: `s${String(i).padStart(3, "0")}`, name: `same-${i}`, description: i < 30 ? "같은 설명 문장" : "같은 설명 문장 조금 다른 끝" }));
  assert.deepEqual(engine.read(many, []), []);
  assert.equal(engine.busy, true);
  const small = many.slice(0, 3);
  assert.equal(engine.read(small, []).length, 3);
  await new Promise((r) => setTimeout(r, 50));
  assert.equal(notified, 0);
  engine.read(many, ["x|y"]);
  for (let i = 0; i < 200 && engine.busy; i++) await new Promise((r) => setTimeout(r, 5));
  await new Promise((r) => setImmediate(r));
  const top = engine.read(many, ["x|y"]);
  assert.equal(top.length, 20);
  assert.ok(top.every((p, i) => !i || top[i - 1].score >= p.score));
  assert.equal(notified, 1);
});
