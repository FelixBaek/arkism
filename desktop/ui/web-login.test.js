// Web mode in the DOM stand-in: memoryd serves the screens at /app/ and the person signs in with a pasted token.
// The token is kept in sessionStorage only (never localStorage) and goes out as a Bearer header to this origin.
// Run: node --test desktop/ui/web-login.test.js
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const { boot, startPreview } = require("./testdata/app-harness.js");

const realFetch = globalThis.fetch;
const sent = [];
let app, pages, preview;
test.before(async () => {
  preview = await startPreview();
  app = await boot({
    preview,
    stored: { "arkism.mode": null }, // a browser on this origin has never chosen a mode
    beforeEntry: (env) => {
      delete env.window.__TAURI__;
      const u = new URL(preview.origin + "/app/");
      Object.assign(env.window.location, { href: u.href, origin: u.origin, host: u.host, hostname: u.hostname, pathname: u.pathname });
      env.window.fetch = (url, init) => {
        const auth = init?.headers?.Authorization || "";
        sent.push([url, auth]);
        if (auth === "Bearer wrong") return Promise.resolve(new Response("{\"error\":\"unauthorized\"}", { status: 401 }));
        return realFetch(url, init);
      };
      let fn;
      Object.defineProperty(env.window, "ArkismPages", { configurable: true, get: () => fn, set: (f) => { fn = (d) => { pages = d; return f(d); }; } });
    }
  });
  await app.settle(300);
});
test.after(() => { app?.stop(); preview?.stop(); });

const tokenInput = () => app.$("#ssh-form input");
async function signIn(value) {
  sent.length = 0;
  tokenInput().value = value;
  await app.click("#ssh-button", 300);
}
const localValues = () => [...app.window.localStorage._map.values()];

test("the login form asks for a token, in Korean, with no SSH or this-PC choice", () => {
  assert.equal(app.$("#unlock").hidden, false);
  assert.equal(app.$("#unlock-title").textContent, "Arkism 서버에 로그인");
  assert.equal(app.$("#unlock-box").hidden, false, "the token form is visible");
  assert.equal(app.$("#start-choice").hidden, true, "no 이 PC / SSH cards in a browser");
  assert.equal(app.$("#ssh-test").hidden, true);
  assert.equal(tokenInput().type, "password");
  assert.ok(sent.every(([, auth]) => auth === "Bearer "), "no token goes out before sign-in");
});

test("a malformed token is refused before any request", async () => {
  await signIn("two words");
  assert.match(app.$("#unlock-error").textContent, /접속 토큰/);
  assert.ok(!sent.some(([url]) => url.includes("/v1/capabilities")), "no sign-in request");
});

test("a token the server rejects is not kept anywhere", async () => {
  await signIn("wrong");
  assert.ok(sent.some(([url, auth]) => url.startsWith(preview.origin + "/v1/") && auth === "Bearer wrong"));
  assert.notEqual(app.$("#unlock-error").textContent, "");
  assert.equal(app.$("#unlock").hidden, false);
  assert.equal(app.window.sessionStorage.getItem("arkism.webToken"), null);
  assert.equal(pages.state.token, "");
});

test("a good token signs in to this origin, lives in sessionStorage only, and 연결 끊기 forgets it", async () => {
  await signIn("  good-token  ");
  const done = () => app.window.sessionStorage.getItem("arkism.webToken") || app.$("#unlock-error").textContent;
  for (let i = 0; i < 300 && !done(); i++) await app.settle(20); // real fetches to the preview server
  assert.equal(app.$("#unlock-error").textContent, "");
  assert.equal(app.$("#unlock").hidden, true);
  assert.ok(sent.length > 0 && sent.filter(([url]) => url.includes("/v1/capabilities")).every(([url, auth]) => url.startsWith(preview.origin + "/") && auth === "Bearer good-token"));
  assert.equal(app.window.sessionStorage.getItem("arkism.webToken"), "good-token");
  assert.ok(!localValues().some((v) => String(v).includes("good-token")), "the token never reaches localStorage");
  pages.disconnect();
  await app.settle(50);
  assert.equal(app.window.sessionStorage.getItem("arkism.webToken"), null);
  assert.equal(app.$("#unlock").hidden, false);
});
