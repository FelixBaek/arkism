// Stress the production virtualizer with 10,000 measured, differently sized rows.
// The document is isolated; no network, API or native product process is started.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1000, height: 800 } });
    const errors = []; page.on("pageerror", error => errors.push(error.message));
    await page.route("**/*", route => route.abort());
    await page.setContent('<style>body{margin:0}#scroller{height:480px;width:600px;overflow:auto}.row{display:block;box-sizing:border-box;width:100%;padding:8px;border:0;border-bottom:1px solid gray;text-align:left;white-space:normal;font:16px/1.5 sans-serif}.virtual-spacer{pointer-events:none}</style><div id="scroller"><div id="rows"></div></div><button id="outside">After list</button>');
    const moduleURL = "data:text/javascript;base64," + fs.readFileSync(path.resolve(__dirname, "../ui/modules/virtualList.mjs")).toString("base64");
    await page.evaluate(async moduleURL => {
      const { createVirtualList } = await import(moduleURL);
      window.virtual = createVirtualList(); window.created = 0;
      const scroller = document.querySelector("#scroller"), container = document.querySelector("#rows");
      window.virtual.mount({ container, scrollport: scroller, items: Array.from({ length: 10000 }, (_, id) => ({ id })), row(item) {
        window.created++;
        const row = document.createElement("button"); row.className = "row"; row.dataset.id = String(item.id);
        row.textContent = `Row ${item.id} ` + "variable height content ".repeat(item.id % 7); return row;
      } });
    }, moduleURL);
    const evidence = [];
    async function geometry() {
      await page.waitForTimeout(100);
      const value = await page.evaluate(() => {
        const port = document.querySelector("#scroller"), bounds = port.getBoundingClientRect();
        const visible = [...document.querySelectorAll(".virtual-item")].map(node => node.getBoundingClientRect()).filter(rect => rect.bottom > bounds.top && rect.top < bounds.bottom);
        return { mounted: document.querySelectorAll(".row").length, height: port.scrollHeight, top: port.scrollTop,
          covers: visible.length > 0 && visible[0].top <= bounds.top + 1 && visible.at(-1).bottom >= bounds.bottom - 1,
          continuous: visible.every((rect, index) => index === 0 || Math.abs(rect.top - visible[index - 1].bottom) < 0.5) };
      });
      assert.ok(value.mounted < 80, JSON.stringify(value));
      assert.ok(value.covers && value.continuous, JSON.stringify(value)); evidence.push(value);
    }
    await geometry();
    await page.locator('[data-id="0"]').focus();
    for (const y of [5000, 170000, 460000, 0]) {
      await page.evaluate(y => { document.querySelector("#scroller").scrollTop = y; }, y); await geometry();
      assert.equal(await page.evaluate(() => document.activeElement.dataset.id), "0", "focused row was removed during scroll");
    }
    await page.keyboard.press("End");
    await page.waitForFunction(() => document.activeElement.dataset.id === "9999"); await geometry();
    await page.keyboard.press("Tab"); assert.equal(await page.evaluate(() => document.activeElement.id), "outside");
    await page.locator('[data-id="9999"]').focus(); await page.keyboard.press("Home");
    await page.waitForFunction(() => document.activeElement.dataset.id === "0");
    for (let i = 0; i < 50; i++) await page.keyboard.press("Tab");
    assert.equal(await page.evaluate(() => document.activeElement.dataset.id), "50");
    await page.keyboard.press("Shift+Tab"); assert.equal(await page.evaluate(() => document.activeElement.dataset.id), "49");
    await page.evaluate(() => { document.querySelector("#scroller").style.width = "250px"; document.querySelector("#scroller").style.fontSize = "24px"; });
    await geometry();
    await page.evaluate(() => window.virtual.clear());
    const created = await page.evaluate(() => window.created);
    await page.evaluate(() => { document.querySelector("#scroller").scrollTop = 0; document.querySelector("#scroller").style.width = "400px"; });
    await page.waitForTimeout(100);
    assert.equal(await page.evaluate(() => window.created), created, "disposed list kept rendering");
    assert.deepEqual(errors, []);
    console.log(`10,000-row virtualizer passed: max ${Math.max(...evidence.map(item => item.mounted))} mounted rows; no visible gaps, focus retained, End/Home/Tab/Shift+Tab, resize and observer teardown.`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
