// popup-drag.js in an isolated page: a popup keeps moving after a sideways drag, dropping it outside moves it into a window.open child window.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1000, height: 800 } });
    const errors = []; page.on("pageerror", error => errors.push(error.message));
    await page.route("**/*", route => route.abort());
    await page.setContent('<!doctype html><style>dialog{width:500px;height:300px;padding:0;border:0}.ov-win-head{display:flex;height:50px;margin:0}h2{margin:0}</style><dialog data-popout=\'{"event":"arkism:open-overview-window","detail":{"key":"extract"}}\'><div class="ov-win-head"><h2>분석</h2><span style="flex:1"></span><button>닫기</button></div></dialog>');
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
    // dropped outside the app: the same popup moves into a window.open child (one JS world, so nothing reloads)
    await page.evaluate(() => {
      window.calls = [];
      window.__TAURI__ = { core: { invoke: async (cmd, args) => { window.calls.push([cmd, args]); return null; } } };
    });
    const popup = page.waitForEvent("popup");
    await page.mouse.move(x0 + 300, 50); await page.mouse.down(); await page.mouse.move(x0 + 300, 10, { steps: 3 }); await page.mouse.move(x0 + 300, -5); await page.mouse.up();
    const child = await popup;
    assert.equal(await page.locator("dialog").count(), 0, "the popup left this page");
    assert.equal(await child.evaluate(() => document.querySelector("dialog.popout-root")?.open), true);
    assert.equal(await child.evaluate(() => document.documentElement.classList.contains("popout") && (document.adoptedStyleSheets.length > 0 || !!document.querySelector("style"))), true, "the child gets the app's styles");
    assert.equal(await child.evaluate(() => document.querySelector("h2").textContent), "분석");
    // closing its popup there closes the window through the app (popout_destroy)
    await child.evaluate(() => document.querySelector("dialog").close());
    await page.waitForFunction(() => window.calls.some(([cmd]) => cmd === "popout_destroy"));
    assert.match(await page.evaluate(() => window.calls.find(([cmd]) => cmd === "popout_destroy")[1].label), /^popout-\d+$/);
    // 처음부터 창으로: a popup that can live alone opens in its own window at once
    const second = page.waitForEvent("popup");
    await page.evaluate(() => { const d = document.createElement("dialog"); d.dataset.popout = "1"; d.innerHTML = "<p>둘째</p>"; document.body.append(d); d.showModal(); });
    const child2 = await second;
    assert.equal(await child2.evaluate(() => document.querySelector("dialog p").textContent), "둘째");
    assert.equal(await page.locator("dialog").count(), 0);
    await child2.evaluate(() => document.querySelector("dialog").close()); // else the next popup follows the window in focus
    await page.waitForFunction(() => window.calls.filter(([cmd]) => cmd === "popout_destroy").length === 2);
    // under the app's own CSP (tauri.conf.json): the child window must still get the styles, the font scale and the zoom
    const csp = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../src-tauri/tauri.conf.json"), "utf8")).app.security.csp;
    const app = await browser.newPage({ viewport: { width: 1000, height: 800 } });
    app.on("pageerror", error => errors.push(error.message));
    const blocked = []; app.context().on("console", m => { if (/Content Security Policy/.test(m.text())) blocked.push(m.text().slice(0, 120)); }); // the child window too, not just this page
    const files = {
      "/": ['text/html', '<!doctype html><link rel="stylesheet" href="/a.css"><body><script src="/popup-drag.js"></script><script src="/start.js"></script>'],
      "/a.css": ["text/css", "dialog{width:500px;height:300px;padding:0;border:0;background:rgb(1, 2, 3)}h2{font-size:calc(10px * var(--fs, 1))}"],
      "/popup-drag.js": ["text/javascript", fs.readFileSync(path.resolve(__dirname, "../ui/popup-drag.js"), "utf8")],
      "/start.js": ["text/javascript", 'document.documentElement.style.setProperty("--fs", "2"); document.documentElement.style.zoom = "1.25"; window.__TAURI__ = { core: { invoke: async () => null } }; addEventListener("load", () => { const d = document.createElement("dialog"); d.dataset.popout = "1"; d.append(Object.assign(document.createElement("h2"), { textContent: "창" })); document.body.append(d); });']
    };
    await app.route("http://app.test/**", route => { const [type, body] = files[new URL(route.request().url()).pathname] || ["text/plain", ""]; route.fulfill({ status: 200, contentType: type, body, headers: { "content-security-policy": csp } }); });
    await app.goto("http://app.test/");
    const third = app.waitForEvent("popup");
    await app.evaluate(() => document.querySelector("dialog").showModal());
    const child3 = await third;
    await child3.waitForFunction(() => document.querySelector("dialog.popout-root"));
    assert.deepEqual(await child3.evaluate(() => { const d = document.querySelector("dialog"), h = d.querySelector("h2"); return [getComputedStyle(d).backgroundColor, getComputedStyle(d).width, getComputedStyle(h).fontSize, document.documentElement.style.zoom]; }), ["rgb(1, 2, 3)", "500px", "20px", "1.25"], "styles, --fs and zoom apply in the child under the CSP");
    assert.deepEqual(blocked, [], "nothing blocked by the CSP");
    await app.close();
    // keyboard path (#200): without the desktop app it says why instead of nothing
    await page.evaluate(() => {
      window.notices = []; window.ArkismNotice = (t) => window.notices.push(t); delete window.__TAURI__;
      const d = document.createElement("dialog"); d.dataset.popout = "1"; d.innerHTML = "<button>닫기</button>"; document.body.append(d); d.showModal(); d.querySelector("button").focus();
    });
    await page.keyboard.press("Alt+Shift+O");
    // Dvorak: O sits on the S key, so e.code is "KeyS" while e.key is "O"
    await page.evaluate(() => document.dispatchEvent(new KeyboardEvent("keydown", { code: "KeyS", key: "O", altKey: true, shiftKey: true, bubbles: true })));
    assert.deepEqual(await page.evaluate(() => window.notices), ["새 창은 데스크톱 앱에서 열 수 있어요.", "새 창은 데스크톱 앱에서 열 수 있어요."]);
    assert.equal(await page.evaluate(() => document.querySelector("dialog").open), true);
    // a window whose page never gets ready: the popup stays here and that empty window is closed
    await page.evaluate(() => {
      window.calls = [];
      window.__TAURI__ = { core: { invoke: async (cmd, args) => { window.calls.push([cmd, args]); return null; } } };
      window.open = () => ({ closed: false, get document() { throw new Error("not ready"); }, close() { this.closed = true; } });
    });
    await page.keyboard.press("Alt+Shift+O");
    await page.waitForFunction(() => window.calls.some(([cmd]) => cmd === "popout_destroy"), null, { timeout: 10000 });
    assert.equal(await page.evaluate(() => document.querySelector("dialog").open), true, "the popup is still shown here");
    assert.deepEqual(errors, []);
    console.log("popup drag browser check passed");
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exit(1); });
