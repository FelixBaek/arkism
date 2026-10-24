// Boots the whole desktop UI (entry.mjs) in the DOM stand-in against the stock preview server (preview/serve.py),
// with the desktop shell replaced by the same stand-in the preview screenshots use (preview/shot.mjs).
"use strict";
const path = require("path");
const net = require("net");
const { spawn } = require("child_process");
const realTimers = require("timers");
const dom = require("./dom.js");

const UI = path.join(__dirname, "..");
const realFetch = globalThis.fetch;
const sleep = (ms) => new Promise((r) => realTimers.setTimeout(r, ms));
async function startPreview() {
  const port = await new Promise((r) => { const s = net.createServer().listen(0, "127.0.0.1", () => { const p = s.address().port; s.close(() => r(p)); }); });
  const proc = spawn("python3", ["-I", path.join(__dirname, "preview", "serve.py"), String(port)], { stdio: "ignore" });
  process.on("exit", () => proc.kill()); // a crashed test must not leave the server running
  const origin = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 200; i++) { try { if ((await realFetch(origin + "/readyz")).ok) return { origin, stop: () => proc.kill() }; } catch { /* not up yet */ } await sleep(25); }
  proc.kill();
  throw new Error("preview server did not start");
}

// opts: remote (server connection instead of the built-in Arkism), stored (localStorage), invoke (extra command answers),
// winbar (a desktop window for window-controls.js), lang.
async function boot(opts = {}) {
  const preview = opts.preview || await startPreview();
  try {
    const { origin } = preview;
    // "arkism.mode": "local" skips the first-run choice between the built-in Arkism and a server
    const stored = { "arkism.look": JSON.stringify({ theme: "dark", lang: opts.lang || "ko" }), "arkism.mode": "local", ...(opts.stored || {}) };
    if (opts.remote) Object.assign(stored, { "arkism.mode": "remote", "arkism.ssh": JSON.stringify({ host: "my-server", user: "me" }) });
    const env = dom.install({ index: true, stored });
    const calls = [], apiCalls = [];
    let inflight = 0;
    const fake = {
      app_defaults: { local: !opts.remote },
      ssh_connect: { server: origin, token: "preview", workspace_id: "default", project_id: "default", ssh_host: "my-server" },
      ssh_test: { host: "my-server" }, ssh_keys: ["~/.ssh/id_ed25519"], ssh_disconnect: null,
      local_start: { server: origin, token: "preview" },
      // the 저장 위치 row (storageRow) reads where the built-in Arkism keeps its data; the default folder, unchanged
      local_location: { path: "/home/me/.local/share/arkism/runtime", next: "/home/me/.local/share/arkism/runtime", default: "/home/me/.local/share/arkism/runtime", default_has_data: false },
      model_roles: { embedder: "auto", embed_model: "qwen3-embedding:0.6b", judge: "fixed", extract_model: "haiku", reflect_model: "sonnet", cloudflare_account: "", cloudflare_token_saved: false },
      autostart_get: true, capture_settings: { paused: false, exclude: ["~/private"], instructions: "" }, search_mode: "ollama:qwen3-embedding:0.6b",
      local_backups: { backups: [] }, gateway_token_status: { state: "set", updated_ms: Date.UTC(2026, 9, 13, 3), from_env: false },
      update_check: { current: "0.1.1", latest: "0.1.2", newer: true },
      popout_destroy: null,
      ...(opts.invoke || {})
    };
    const invoke = async (cmd, a = {}) => {
      calls.push([cmd, a]);
      inflight++;
      try {
        if (cmd === "git_op") { const r = await realFetch(origin + "/v1/git/" + a.request.action); return { authority: "data_only_untrusted", ...(await r.json()) }; }
        if (cmd === "api") {
          apiCalls.push([a.method, a.path, a.body]);
          if (env.window.apiOverride) { const o = await env.window.apiOverride(a); if (o) return o; }
          const r = await realFetch(a.base + a.path, { method: a.method, body: a.body == null ? undefined : (typeof a.body === "string" ? a.body : JSON.stringify(a.body)) });
          return { status: r.status, body: await r.text() };
        }
        if (Object.hasOwn(fake, cmd)) { const v = fake[cmd]; if (v instanceof Error) throw v.message; return typeof v === "function" ? await v(a) : structuredClone(v); }
        throw new Error("preview: " + cmd);
      } finally { inflight--; }
    };
    env.window.__TAURI__ = { core: { invoke } };
    if (opts.winbar) env.window.__TAURI__.window = { getCurrentWindow: () => opts.winbar };
    // Settles the app: runs due fake timers (up to ms ahead) and waits for every outstanding shell/API call.
    // Gives up quietly after the rounds: some tests hold a call open on purpose (a slow server, a pending dialog) and assert on the screen meanwhile.
    async function settle(ms = 0, rounds = 400) {
      for (let i = 0; i < rounds; i++) {
        await env.timers.tick(i === 0 ? ms : 0);
        if (inflight === 0) { await dom.flush(20); if (inflight === 0) return; }
        await sleep(2);
      }
    }
    require(path.join(UI, "look.js"));
    if (opts.beforeEntry) await opts.beforeEntry(env);
    await import(path.join(UI, "entry.mjs"));
    await settle(100);
    const $ = (s) => env.document.querySelector(s), $$ = (s) => env.document.querySelectorAll(s);
    const click = async (s, ms = 0) => { const n = typeof s === "string" ? $(s) : s; if (!n) throw new Error("no element " + s); n.click(); await settle(ms); return n; };
    return { ...env, preview, origin, invoke, calls, apiCalls, fake, settle, $, $$, click, dom, sleep, stop: () => { env.timers.clear(); if (!opts.preview) preview.stop(); } };
  } catch (e) { if (!opts.preview) preview.stop(); throw e; } // a failed boot must not leave the server running
}

module.exports = { boot, startPreview, sleep, UI };
