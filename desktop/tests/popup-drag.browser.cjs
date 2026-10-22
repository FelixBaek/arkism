// popup-drag.js in an isolated page: a popup keeps moving after a sideways drag, dropping it outside opens a separate window.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1000, height: 800 } });
    const errors = []; page.on("pageerror", error => errors.push(error.message));
    await page.route("**/*", route => route.abort());
    await page.setContent('<!doctype html><style>dialog{width:500px;height:300px;padding:0;border:0}.ov-win-head{display:flex;height:50px;margin:0}h2{margin:0}</style><dialog data-popout=\'{"event":"arkism:open-overview-window","detail":{"key":"extract"}}\'><div class="ov-win-head"><h2>분석</h2><span style="flex:1"></span><button>닫기</button></div></dialog>');
    await page.evaluate(() => {
      window.opened = [];
      window.__TAURI__ = { core: { invoke: async (cmd, args) => { if (cmd === "popout_open") { window.opened.push(args); } return "popout-1"; } } };
      window.ArkismPopoutSession = () => ({ token: "t" });
    });
    await page.addScriptTag({ content: fs.readFileSync(path.resolve(__dirname, "../ui/popup-drag.js"), "utf8") });
    await page.evaluate(() => document.querySelector("dialog").showModal());
    const box = () => page.evaluate(() => { const r = document.querySelector("dialog").getBoundingClientRect(); return [Math.round(r.x), Math.round(r.y)]; });
    const drag = async (from, dx, dy) => { await page.mouse.move(from[0], from[1]); await page.mouse.down(); await page.mouse.move(from[0] + dx, from[1] + dy, { steps: 5 }); await page.mouse.up(); };
    const [x0, y0] = await box();
    await drag([x0 + 100, y0 + 20], -80, 0); // leaves translate as "-80px" (no y part)
    assert.deepEqual(await box(), [x0 - 80, y0]);
    await drag([x0 + 20, y0 + 20], 40, 60);
    assert.deepEqual(await box(), [x0 - 40, y0 + 60], "a second drag after a sideways one must still move");
    await drag([x0 + 60, y0 + 80], 0, -(y0 + 75)); // pinned at the top edge, pointer still inside
    assert.equal((await box())[1], 0);
    await drag([x0 + 60, 20], 30, 30);
    assert.deepEqual(await box(), [x0 - 10, 30], "a drag after one stopped at the top must still move");
    assert.equal(await page.locator(".popout-btn").count(), 0, "no 새 창으로 button");
    await page.mouse.move(x0 + 300, 50); await page.mouse.down(); await page.mouse.move(x0 + 300, 10, { steps: 3 }); await page.mouse.move(x0 + 300, -5); await page.mouse.up();
    await page.waitForFunction(() => window.opened.length === 1);
    const sent = await page.evaluate(() => window.opened[0]);
    assert.equal(JSON.parse(sent.payload).recipe.detail.key, "extract");
    assert.equal(await page.evaluate(() => document.querySelector("dialog").open), false, "torn-off popup closes here");
    // keyboard path (#200): Alt+Shift+O tears off the top popup; without the desktop app it says why instead of nothing
    await page.evaluate(() => { window.notices = []; window.ArkismNotice = (t) => window.notices.push(t); document.querySelector("dialog").showModal(); document.querySelector("dialog button").focus(); });
    await page.keyboard.press("Alt+Shift+O");
    await page.waitForFunction(() => window.opened.length === 2);
    assert.equal(await page.evaluate(() => document.querySelector("dialog").open), false, "Alt+Shift+O closes it here too");
    await page.evaluate(() => { delete window.__TAURI__; document.querySelector("dialog").showModal(); });
    await page.keyboard.press("Alt+Shift+O");
    assert.deepEqual(await page.evaluate(() => window.notices), ["새 창은 데스크톱 앱에서 열 수 있어요."]);
    assert.equal(await page.evaluate(() => document.querySelector("dialog").open), true);
    assert.deepEqual(errors, []);
    console.log("popup drag browser check passed");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
