// Values the owner asked for, more than once, that later changes kept undoing. Each test names the request.
// Run: node --test desktop/ui/*.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const css = (f) => fs.readFileSync(path.join(__dirname, f), "utf8");
const rule = (src, sel) => {
  const i = src.indexOf(sel + " {");
  assert.ok(i >= 0, `missing rule ${sel}`);
  return src.slice(i, src.indexOf("}", i));
};

test("Flow cards are 270 x 154, four across at 1440 (Figma 1478:12809, asked five times)", () => {
  const s = css("v4-agent.css");
  assert.match(rule(s, ".wf-grid"), /grid-template-columns: repeat\(auto-fill, 270px\)/);
  assert.match(rule(s, ".crew-card.wf-card"), /height: 154px/);
});

test("the rail triangle swings past -90 to fold and past 90 to open (Figma 198:7, 10-29 핑퐁)", () => {
  const s = css("shell.css");
  assert.match(rule(s, ".rail-tri"), /transition: transform 700ms linear\(0, \.109,[^)]*1\.341/);
  assert.match(rule(s, ".rail-toggle:is(:hover, :focus-visible) .rail-tri"), /rotate\(-90deg\)/);
  assert.match(rule(s, ":root.rail-collapsed .rail-toggle:is(:hover, :focus-visible) .rail-tri"), /rotate\(90deg\)/);
});

test("dreaming stays purple and diff lines keep their green and red fills (undone once by a Figma pass)", () => {
  assert.match(rule(css("v4-overview.css"), ".ov-win-tag.ov-tag-p"), /var\(--ov-p\)/);
  assert.match(rule(css("v4-overview.css"), ".ov-group-dream .ov-group-head"), /var\(--ov-p\)/);
  const d = css("v4-digests.css");
  assert.match(rule(d, ".dg-line.add, .dg-line.add .dg-op"), /background: var\(--nc-diff-add-bg\)/);
  assert.match(rule(d, ".dg-words ins"), /background: var\(--nc-diff-add-bg\)/);
  assert.match(rule(d, ".dg-words del"), /background: var\(--nc-diff-del-bg\)/);
});

test("run graph: running is white, a taken path green, a back edge red (asked on 10-24 and 10-27)", () => {
  assert.match(rule(css("crew.css"), ".g-node.st-running"), /var\(--text-1\)/);
  assert.match(rule(css("crew.css"), ".g-edge.used"), /--g-human/);
  assert.match(rule(css("v4-crew-graph.css"), ".g-edge.back"), /--status-danger/);
  assert.match(rule(css("v4-crew-graph.css"), ".g-edge.used"), /--g-human/);
});
