// brand.js kind(): what input a model takes, shown as the tag in the model picker and used to fill the 이미지 picker.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const win = {};
vm.runInNewContext(fs.readFileSync(require("node:path").join(__dirname, "brand.js"), "utf8"), { window: win, document: {}, String });
const kind = win.ArkismBrand.kind;

test("vision, embedding and text models", () => {
  for (const m of ["claude-opus-4", "gpt-4o", "o3", "gemini-2.5-pro", "qwen2.5-vl-7b", "qwen-vl-max", "glm-4.5v", "llama-3.2-vision"]) assert.equal(kind(m), "vision", m);
  for (const m of ["bge-m3", "text-embedding-3-small", "nomic-embed-text"]) assert.equal(kind(m), "embed", m);
  for (const m of ["gpt-3.5-turbo", "glm-4.6", "deepseek-v4-flash", "unknown-model"]) assert.equal(kind(m), "text", m);
});

test("auto and empty have no kind", () => {
  assert.equal(kind("auto"), "");
  assert.equal(kind(""), "");
});

test("provider names and base URL hosts find their LobeHub mark; unknown ones find none", () => {
  const document = { createElement: (tag) => ({ tag, style: { setProperty(k, v) { this[k] = v; } }, setAttribute() {} }) };
  const w = {};
  vm.runInNewContext(fs.readFileSync(require("node:path").join(__dirname, "brand.js"), "utf8"), { window: w, document, String });
  const file = (n) => { const m = w.ArkismBrand.mark(n); return m && (m.src || m.style["--brand"]); };
  for (const [n, f] of [["NousPortal", "nousresearch"], ["opencode", "opencode"], ["QWEN", "qwen-color"], ["Deepseek", "deepseek-color"], ["deepseek.com", "deepseek-color"], ["Z.ai", "zai"]]) assert.match(file(n), new RegExp(`img/brand/${f}\\.svg`), n);
  assert.equal(w.ArkismBrand.mark("custom-abc"), null);
});
