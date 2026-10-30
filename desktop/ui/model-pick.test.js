// model-pick.js: every model picker lists connected models first and keeps the rest behind one toggle.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/dom.js");
dom.install();
const { plan, fill, MORE, CONNECT } = require("./model-pick.js");

const groups = () => [
  { id: "anthropic", name: "Anthropic", usable: true, models: ["opus", "sonnet"] },
  { id: "xai", name: "xAI", usable: false, models: ["grok", "grok-mini"] },
  { id: "openai", name: "OpenAI", models: ["gpt-5"] }, // usable missing: connected
];

test("plan: connected groups show, the rest are counted behind the toggle", () => {
  const p = plan(groups());
  assert.deepEqual(p.shown.map((g) => g.id), ["anthropic", "openai"]);
  assert.equal(p.hidden, 2);
  assert.equal(p.currentOff, false);
  const all = plan(groups(), "", undefined, true);
  assert.deepEqual(all.shown.map((g) => [g.id, g.off]), [["anthropic", false], ["openai", false], ["xai", true]]);
  assert.equal(all.hidden, 0);
});

test("plan: a saved pick of a not connected provider stays, alone and marked", () => {
  const p = plan(groups(), "xai|grok");
  assert.deepEqual(p.shown.find((g) => g.id === "xai").models, ["grok"]);
  assert.equal(p.hidden, 1);
  assert.equal(p.currentOff, true);
});

test("plan: a name a connected provider also offers is not a warning", () => {
  const gs = [{ id: "a", name: "A", usable: false, models: ["m"] }, { id: "b", name: "B", usable: true, models: ["m"] }];
  const p = plan(gs, "m", (g, m) => m);
  assert.equal(p.currentOff, false);
  assert.deepEqual(p.shown.map((g) => g.id), ["b"]);
  assert.equal(p.hidden, 1);
});

function drawGroup(p, valueOf) {
  const g = document.createElement("optgroup"); g.label = p.name;
  for (const m of p.models) { const o = document.createElement("option"); o.textContent = m; o.value = valueOf(m); g.append(o); }
  return g;
}
const values = (sel) => [...sel.options].map((o) => o.value);
const change = (sel, v) => { sel.value = v; sel.dispatchEvent(new Event("change", { bubbles: true })); };

test("fill: the toggle shows the rest greyed with 연결 필요 and a way to connect; neither reaches the screen", () => {
  const sel = document.createElement("select");
  sel.append(Object.assign(document.createElement("option"), { value: "", textContent: "기본값" }));
  let connects = 0;
  const picks = [];
  fill(sel, groups(), { current: "", group: drawGroup, onConnect: () => connects++ });
  sel.addEventListener("change", () => picks.push(sel.value));
  assert.deepEqual(values(sel), ["", "anthropic|opus", "anthropic|sonnet", "openai|gpt-5", MORE]);
  assert.match(sel.options.at(-1).textContent, /연결 안 된 모델도 보기 \(2개\)/);
  change(sel, MORE);
  assert.deepEqual(picks, []);
  assert.equal(sel.value, "");
  const grok = [...sel.options].find((o) => o.value === "xai|grok");
  assert.equal(grok.disabled, true);
  assert.equal(grok.dataset.hint, "연결 필요");
  assert.match(grok.parentElement.label, /xAI · 연결 필요/);
  assert.equal(values(sel).at(-1), CONNECT);
  change(sel, CONNECT);
  assert.equal(connects, 1);
  assert.deepEqual(picks, []);
  change(sel, "openai|gpt-5");
  assert.deepEqual(picks, ["openai|gpt-5"]);
});

test("fill: a saved not connected pick stays picked with a warning; an unknown saved pick is still listed", () => {
  const sel = fill(document.createElement("select"), groups(), { current: "xai|grok", group: drawGroup, onConnect: () => {} });
  assert.equal(sel.value, "xai|grok");
  assert.match(sel.selectedOptions[0].textContent, /^연결 필요 · grok$/);
  assert.equal(sel.selectedOptions[0].disabled, false);
  assert.ok(sel.classList.contains("model-off"));
  assert.match(sel.title, /연결되지 않아/);
  assert.ok(values(sel).includes(CONNECT));
  change(sel, "anthropic|opus");
  assert.ok(!sel.classList.contains("model-off"));

  const gone = fill(document.createElement("select"), groups(), { current: "zai|glm", group: drawGroup });
  assert.equal(gone.value, "zai|glm");
  assert.equal(gone.selectedOptions[0].textContent, "glm");
  assert.ok(!values(gone).includes(CONNECT), "no onConnect: no jump");
});
