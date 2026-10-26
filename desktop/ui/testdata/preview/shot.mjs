// node shot.mjs <url> <out.png> [menu] [tabIndex] [theme]
// UILANG=en: English UI. REMOTE=1 opens it as a server connection.
// playwright-core comes from any local checkout that has it installed (PLAYWRIGHT_FROM, default the current folder). — opens the preview, logs in with the proxy token, clicks a menu/tab, screenshots.
import { createRequire } from "module";
const require = createRequire((process.env.PLAYWRIGHT_FROM || process.cwd()) + "/");
const { chromium } = require("playwright-core");
const [url, out, menu, tab, theme, click] = process.argv.slice(2); // click: optional CSS selector (or text=…) clicked last; "a||b" clicks several in order
const b = await chromium.launch();
const p = await b.newPage({ viewport: { width: Number(process.env.VW) || 1440, height: Number(process.env.VH) || 960 }, locale: "ko-KR" });
const errs = [];
p.on("pageerror", (e) => errs.push(String(e)));
p.on("console", (m) => { if (m.type() === "error") errs.push(m.text()); });
await p.addInitScript(([origin, theme, remote, gw, lang, winbar]) => { // GWTOKEN=missing|invalid: 설정 › 연결's gateway token state
  localStorage.setItem("arkism.server", origin); localStorage.setItem("arkism.token", "preview"); 
  if (!remote && !localStorage.getItem("arkism.mode")) localStorage.setItem("arkism.mode", "local");
  if (remote) { localStorage.setItem("arkism.mode", "remote"); localStorage.setItem("arkism.ssh", JSON.stringify({ host: "my-server", user: "me" })); }
  localStorage.setItem("arkism.look", JSON.stringify({ theme: theme || "dark", lang: lang || "ko" }));
  // Stand-in for the desktop app: the built-in Arkism is the read-only preview server.
  window.__TAURI__ = { core: { invoke: async (cmd, a = {}) => {
    if (cmd === "app_defaults") return { local: !remote };
    if (cmd === "ssh_connect") return { server: origin, token: "preview", workspace_id: "default", project_id: "default", ssh_host: "my-server" };
    if (cmd === "local_start") return { server: origin, token: "preview" };
    // settings pages (router worker): sample answers so 역할별 모델·고급 draw like the design mockups
    const fake = { model_roles: { embedder: "auto", embed_model: "qwen3-embedding:0.6b", judge: "fixed", extract_model: "haiku", reflect_model: "sonnet", cloudflare_account: "", cloudflare_token_saved: false },
      autostart_get: true, capture_settings: { paused: false, exclude: ["~/private", "~/work/client-a"], instructions: "" }, search_mode: "ollama:qwen3-embedding:0.6b",
      local_backups: { backups: [] }, gateway_token_status: { state: gw || "set", updated_ms: Date.UTC(2026, 9, 13, 3), from_env: false },
      gateway_token_regenerate: { status: { state: "set", updated_ms: Date.now(), from_env: false }, agents_updated: true }, update_check: { current: "0.1.1", latest: "0.1.2", newer: true }, general_get: { keep_running: true, shortcut: "Ctrl+Shift+Space", shortcut_error: "" } };
    if (cmd in fake) return fake[cmd];
    if (cmd === "api") { const r = await fetch(a.base + a.path, { method: a.method, body: a.body == null ? undefined : (typeof a.body === "string" ? a.body : JSON.stringify(a.body)) }); return { status: r.status, body: await r.text() }; }
    throw new Error("preview: " + cmd);
  } } };
  // WINBAR=1: a stand-in desktop window, so the 창 버튼 strip (window-controls.js) shows as in the app
  if (winbar) window.__TAURI__.window = { getCurrentWindow: () => ({ isMaximized: async () => false, onResized: async () => {}, minimize() {}, toggleMaximize: async () => {}, close() {} }) };
}, [new URL(url).origin, theme || "", process.env.REMOTE || "", process.env.GWTOKEN || "", process.env.UILANG || "", process.env.WINBAR || ""]);
await p.goto(url);
await p.waitForTimeout(2500);
if (menu) { await p.click(`.nav-item[data-menu="${menu}"]`).catch((e) => errs.push("menu " + e.message)); await p.waitForTimeout(1200); }
if (tab) { await p.click(`#area-tabs .area-tab >> nth=${tab}`).catch((e) => errs.push("tab " + e.message)); await p.waitForTimeout(1500); }
for (const c of (click || "").split("||").filter(Boolean)) { await p.click(c).catch((e) => errs.push("click " + e.message)); await p.waitForTimeout(1200); } // several clicks: "a||b"
if (process.env.EVAL) { for (const js of process.env.EVAL.split("||")) { await p.evaluate(js).catch((e) => errs.push("eval " + e.message)); await p.waitForTimeout(1200); } } // EVAL="js||js": run in the page after the clicks
const clip = process.env.CLIP ? (([x, y, width, height]) => ({ x, y, width, height }))(process.env.CLIP.split(",").map(Number)) : undefined;
await p.screenshot({ path: out, fullPage: !!process.env.FULL, clip }); // FULL=1: the whole page; CLIP=x,y,w,h: one part
if (process.env.PROBE) console.log(await p.evaluate(() => document.documentElement.dataset.probe));
console.log(JSON.stringify({ unlocked: await p.evaluate(() => !document.getElementById("app").inert), errs: errs.slice(0, 8) }));
await b.close();
