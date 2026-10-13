// Every DELETE the UI sends must be on the desktop app's allowlist (desktop/src-tauri/src/main.rs `allowed`),
// or the app refuses it with path_not_allowed (that is how gallery 삭제 broke in 0.4.3).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");

test("UI DELETE paths are allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const allowed = [...body.matchAll(/(?:one_id\(|starts_with\()"(\/v1\/[a-z_]+\/)"/g)].map((m) => m[1]);
  const missing = [];
  for (const f of fs.readdirSync(__dirname).filter((n) => n.endsWith(".js") && !n.endsWith(".test.js"))) {
    const src = fs.readFileSync(path.join(__dirname, f), "utf8");
    for (const m of src.matchAll(/request\(\s*`(\/v1\/[^`$]*)(\$\{[^}]*\})?[^`]*`\s*,\s*\{[^}]*method:\s*"DELETE"/g)) {
      let prefix = m[1];
      if (!prefix.endsWith("/")) prefix = prefix.replace(/[^/]*$/, ""); // `/v1/${col}/…` → "/v1/" (checked below)
      const dynamicCol = m[0].startsWith("request(`/v1/${");
      const ok = dynamicCol ? ["/v1/prompts/", "/v1/design_systems/"].every((p) => allowed.includes(p)) : allowed.some((a) => prefix.startsWith(a));
      if (!ok) missing.push(`${f}: ${m[0].slice(0, 80)}`);
    }
  }
  assert.deepEqual(missing, []);
  assert.ok(allowed.includes("/v1/memories/") && allowed.includes("/v1/crew/"));
});
