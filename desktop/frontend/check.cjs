// Check the exact bundle output consumed by the Windows packager.
const fs = require("node:fs"), path = require("node:path"), crypto = require("node:crypto"), assert = require("node:assert/strict");
const desktop = path.resolve(__dirname, ".."), dest = path.resolve(process.env.ARKISM_UI_OUTPUT || path.join(desktop, "ui-dist"));
const manifest = JSON.parse(fs.readFileSync(path.join(dest, "build-manifest.json")));
assert.equal(manifest.esbuild, "0.28.2");
for (const entry of manifest.inputs) {
  const source = path.resolve(desktop, entry.path);
  assert.ok(source.startsWith(path.join(desktop, "ui") + path.sep));
  assert.equal(crypto.createHash("sha256").update(fs.readFileSync(source)).digest("hex"), entry.sha256, entry.path + " changed after bundling");
}
const html = fs.readFileSync(path.join(dest, "index.html"), "utf8");
assert.match(html, /type="module" src="assets\/entry.js"/);
assert.ok(!html.includes('src="i18n-en.js"'));
for (const match of html.matchAll(/(?:src|href)="([^"#]+)"/g)) if (!/^(?:data:|https?:)/.test(match[1])) assert.ok(fs.existsSync(path.join(dest, match[1])), match[1]);
assert.ok(!fs.existsSync(path.join(dest, "node_modules")));
assert.ok(!fs.existsSync(path.join(dest, "testdata")));
assert.ok(!fs.existsSync(path.join(dest, "app.js")));
const assets = fs.readdirSync(path.join(dest, "assets"));
assert.ok(assets.some(name => name.startsWith("i18n-en-")), "English must remain a separate lazy chunk");
console.log(`Frontend manifest and product asset checks passed (${manifest.inputs.length} inputs, ${manifest.outputBytes} script bytes)`);
