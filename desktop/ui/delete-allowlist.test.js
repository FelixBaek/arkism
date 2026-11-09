// Every DELETE the UI sends must be on the desktop app's allowlist (desktop/src-tauri/src/main.rs `allowed`),
// or the app refuses it with path_not_allowed (that is how gallery 삭제 broke in 0.4.3).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const path = require("path");

test("UI DELETE paths are allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const allowed = [...body.matchAll(/(?:one_id\(|query_id\(|starts_with\()"(\/v1\/[a-z_/-]+\/)"/g)].map((m) => m[1]);
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

// Calls built from a path helper (`${hookPath(h)}`) slip past the scan above, so the webhook card's DELETE and PATCH are named here.
test("webhook DELETE and PATCH are allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const ui = fs.readFileSync(path.join(__dirname, "messenger.js"), "utf8");
  assert.match(ui, /hookPath = \(h, tail = ""\) => `\/v1\/webhooks\//);
  assert.match(ui, /method: "DELETE"/); assert.match(ui, /method: "PATCH"/);
  assert.match(body, /query_id\("\/v1\/webhooks\/"\)/, "DELETE /v1/webhooks/{id}?scope");
  assert.match(body, /method == "PATCH" && \([^;]*one_id\("\/v1\/webhooks\/"\)/, "PATCH /v1/webhooks/{id}");
});

// 설정 › 기억 공간 builds its path with a helper too: PATCH saves, PUT hands everything back, both on …/config only.
test("space config PATCH and PUT are allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const ui = fs.readFileSync(path.join(__dirname, "space-settings.js"), "utf8");
  assert.match(ui, /write\("PATCH"/); assert.match(ui, /write\("PUT"/); assert.match(ui, /request\(`\$\{path\(\)\}\/config`, \{ method, body/);
  assert.match(body, /strip_prefix\("\/v1\/spaces\/"\)[^;]*strip_suffix\("\/config"\)/);
  assert.match(body, /method == "PATCH" && \([^;]*space_config\)/);
  assert.match(body, /let put = method == "PUT" && space_config;/);
});

// 제안된 주제 승인·버리기 in digests.js: PATCH and DELETE on /v1/mental-models/{id} only (the PATCH hides in a ternary the scan above skips).
test("proposed topic PATCH and DELETE are allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const ui = fs.readFileSync(path.join(__dirname, "digests.js"), "utf8");
  assert.match(ui, /request\(`\/v1\/mental-models\/\$\{encodeURIComponent\(m\.id\)\}`, action === "approve"/);
  assert.match(body, /method == "PATCH" && \([^;]*one_id\("\/v1\/mental-models\/"\)/);
  assert.match(body, /method == "DELETE"[^;]*one_id\("\/v1\/mental-models\/"\)/);
});

// 지식 › 위키 폴더 builds its DELETE path with a helper (folderUrl), so the scan above skips it; PATCH moves folders, pages and edits a document's tags.
test("wiki folder and page, and extraction document PATCH and DELETE are allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const ui = fs.readFileSync(path.join(__dirname, "knowledge.js"), "utf8");
  assert.match(ui, /folderUrl = \(extra\) => `\/v1\/wiki\/folders\?/); assert.match(ui, /request\(folderUrl\(/);
  assert.match(ui, /request\("\/v1\/wiki\/folders", \{ method: "PATCH"/);
  assert.match(ui, /request\(`\/v1\/wiki\/pages\/\$\{id\}`, \{ method: "PATCH"/);
  assert.match(ui, /request\(`\/v1\/files\/memories\/documents\/\$\{id\}`, \{ method: "PATCH"/);
  assert.match(body, /let wiki_folders = path\.split\(\['\?', '#'\]\)\.next\(\) == Some\("\/v1\/wiki\/folders"\);/);
  assert.match(body, /method == "DELETE"[^;]*wiki_folders/);
  assert.match(body, /method == "PATCH" && \([^;]*one_id\("\/v1\/files\/memories\/documents\/"\)[^;]*one_id\("\/v1\/wiki\/pages\/"\)[^;]*wiki_folders/);
});

// 별칭 빼기 and 기억만 비우기 in spaces.js build their paths with spacePath(): DELETE …/aliases/{alias} and …/memories?confirm= only.
test("space alias DELETE is allowed by the desktop app", () => {
  const rust = fs.readFileSync(path.join(__dirname, "..", "src-tauri", "src", "main.rs"), "utf8");
  const body = rust.slice(rust.indexOf("fn allowed("), rust.indexOf("fn valid_base("));
  const ui = fs.readFileSync(path.join(__dirname, "spaces.js"), "utf8");
  assert.match(ui, /request\(`\$\{spacePath\(\)\}\/aliases\$\{add \? "" : `\/\$\{enc\(alias\)\}`\}`[^\n]*method: "DELETE"/);
  assert.match(body, /p\.len\(\) == 4 && p\[2\] == "aliases"/);
  assert.match(body, /method == "DELETE"[^;]*\|\| space_alias \|\| space_clear\)/);
  assert.match(fs.readFileSync(path.join(__dirname, "spaces.js"), "utf8"), /request\(`\$\{spacePath\(id\)\}\/memories\?confirm=\$\{enc\(id\)\}`, \{ method: "DELETE" \}\)/, "기억만 비우기");
  assert.match(body, /p\.len\(\) == 3 && p\[2\] == "memories"/);
});
