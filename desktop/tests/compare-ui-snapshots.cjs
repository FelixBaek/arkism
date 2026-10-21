// Usage: node desktop/tests/compare-ui-snapshots.cjs BEFORE_DIR AFTER_DIR [AFTER_DIR...]
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const [before,...after] = process.argv.slice(2);
assert.ok(before && after.length, "Provide before/after snapshot directories from ui-structure.browser.cjs");
const baseline = JSON.parse(fs.readFileSync(path.join(before,"screens.json")));
for (const dir of after) {
  const screens = JSON.parse(fs.readFileSync(path.join(dir,"screens.json")));
  assert.deepEqual(screens,baseline,"Visible text, controls or selection changed: " + dir);
  for (const name of Object.keys(baseline)) assert.ok(fs.readFileSync(path.join(before,name+".png")).equals(fs.readFileSync(path.join(dir,name+".png"))),name+" rendered differently: "+dir);
  console.log(`${Object.keys(baseline).length} screens: identical rendered PNGs, labels, controls and selection (${dir})`);
}
