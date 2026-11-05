// Web mode (memoryd serves the screens under /app/): detection, the same-origin API base and
// the session-only token. Run: node --test desktop/ui/web-mode.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");

const load = () => import("./modules/webMode.mjs");
const memory = () => { const m = new Map(); return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), m }; };

test("web mode is a plain browser under /app/, never the desktop shell", async () => {
  const { isWebMode } = await load();
  assert.equal(isWebMode({ pathname: "/app/" }, undefined), true);
  assert.equal(isWebMode({ pathname: "/app/index.html" }, null), true);
  assert.equal(isWebMode({ pathname: "/app/" }, { invoke() {} }), false, "the desktop app keeps its own transport");
  assert.equal(isWebMode({ pathname: "/" }, undefined), false, "the UI preview server is not web mode");
  assert.equal(isWebMode({ pathname: "/application" }, undefined), false);
  assert.equal(isWebMode(undefined, undefined), false);
});

test("a pasted token is trimmed; whitespace, control characters or huge pastes are refused", async () => {
  const { cleanToken } = await load();
  assert.equal(cleanToken("  abc.DEF-123_x  \n"), "abc.DEF-123_x");
  assert.equal(cleanToken("two words"), "");
  assert.equal(cleanToken("a\r\nX-Evil: 1"), "", "no header injection");
  assert.equal(cleanToken("a\x00b"), "");
  assert.equal(cleanToken("x".repeat(4097)), "");
  assert.equal(cleanToken(null), "");
});

test("the token lives in the given session storage and is removed on empty", async () => {
  const { readWebToken, saveWebToken, WEB_TOKEN_KEY } = await load();
  const s = memory();
  saveWebToken(s, "tok");
  assert.equal(s.m.get(WEB_TOKEN_KEY), "tok");
  assert.equal(readWebToken(s), "tok");
  saveWebToken(s, "");
  assert.equal(s.m.has(WEB_TOKEN_KEY), false);
  s.setItem(WEB_TOKEN_KEY, "bad token");
  assert.equal(readWebToken(s), "", "a stored value is validated again before it becomes a header");
  const broken = { getItem() { throw new Error("off"); }, setItem() { throw new Error("off"); }, removeItem() { throw new Error("off"); } };
  assert.equal(readWebToken(broken), "");
  assert.doesNotThrow(() => saveWebToken(broken, "tok"));
  assert.equal(readWebToken(undefined), "");
});

test("in web mode the API goes to the same origin with the token as a Bearer header", async () => {
  const { createAppApi } = await import("./modules/appApi.mjs");
  const seen = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = async (url, opts) => { seen.push([url, opts.headers]); return { status: 200, text: async () => "{\"ok\":true}" }; };
  try {
    const state = { server: "http://10.0.0.5:8091", token: "tok", scope: { workspace_id: "default" }, connection: { generation: 0 } };
    const api = createAppApi({ state, tauri: undefined, trimSlash: (u) => u.replace(/\/+$/, ""), readJSON: (t) => JSON.parse(t) });
    assert.deepEqual(await api.request("/v1/capabilities"), { ok: true });
    assert.equal(seen[0][0], "http://10.0.0.5:8091/v1/capabilities");
    assert.equal(seen[0][1].Authorization, "Bearer tok");
  } finally { globalThis.fetch = realFetch; }
});
