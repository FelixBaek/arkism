// team-cover.js under the app's own CSP (tauri.conf.json): "내 사진 올리기" shrinks a real PNG to a JPEG data URL, nothing blocked.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    const csp = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../src-tauri/tauri.conf.json"), "utf8")).app.security.csp;
    const page = await browser.newPage();
    const errors = [], blocked = [];
    page.on("pageerror", error => errors.push(error.message));
    page.on("console", m => { if (/Content Security Policy/.test(m.text())) blocked.push(m.text().slice(0, 160)); });
    const files = {
      "/": ["text/html; charset=utf-8", '<!doctype html><body><script src="/team-cover.js"></script>'],
      "/team-cover.js": ["text/javascript; charset=utf-8", fs.readFileSync(path.resolve(__dirname, "../ui/team-cover.js"), "utf8")]
    };
    await page.route("http://app.test/**", route => { const [type, body] = files[new URL(route.request().url()).pathname] || ["text/plain", ""]; route.fulfill({ status: 200, contentType: type, body, headers: { "content-security-policy": csp } }); });
    await page.goto("http://app.test/");
    const picked = await page.evaluate(async () => {
      // a real 1500x750 PNG File, made here so the bytes are a genuine encoded image
      const c = document.createElement("canvas"); c.width = 1500; c.height = 750;
      const g = c.getContext("2d"); g.fillStyle = "#3a6"; g.fillRect(0, 0, 1500, 750); g.fillStyle = "#e33"; g.fillRect(100, 100, 600, 300);
      const file = new File([await new Promise(r => c.toBlob(r, "image/png"))], "a.png", { type: "image/png" });
      const el = (tag, cls = "", text = "") => { const n = document.createElement(tag); if (cls) n.className = cls; if (text) n.textContent = text; return n; };
      const notices = [];
      const picker = window.ArkismCoverPicker({ el, icon: () => el("i"), request: async () => ({ items: [], total_pages: 1 }), query: (o) => new URLSearchParams(o).toString(), state: { scope: "s" }, notice: (t) => notices.push(t) });
      const out = new Promise(r => picker.open({ current: null, onPick: r }));
      const input = document.querySelector("dialog.tp-cover input[type=file]");
      const dt = new DataTransfer(); dt.items.add(file); input.files = dt.files;
      input.dispatchEvent(new Event("change"));
      const pick = await Promise.race([out, new Promise(r => setTimeout(() => r(null), 5000))]);
      if (!pick) return { notices };
      const img = new Image(); img.src = pick.url; await img.decode();
      return { id: pick.id, head: pick.url.slice(0, 23), len: pick.url.length, w: img.width, h: img.height, notices };
    });
    assert.deepEqual(picked.notices, [], "no '사진을 줄이지 못했어요'");
    assert.equal(picked.id, "mine");
    assert.equal(picked.head, "data:image/jpeg;base64,");
    assert.ok(picked.len <= 140000, `fits the server limit (${picked.len})`);
    assert.deepEqual([picked.w, picked.h], [1000, 500], "resized to 1000px wide, aspect kept");
    assert.deepEqual(blocked, [], "nothing blocked by the CSP");
    assert.deepEqual(errors, []);
    console.log("team-cover browser: ok");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
