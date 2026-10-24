// Crew popups (automation · expert · team) with the real stylesheets: a tall head and a long body still fit the window,
// the body scrolls and the sticky 저장 bar stays on screen (0.1.6 A4: 1650×1082 cut the bar off below the window).
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
const ui = path.resolve(__dirname, "../ui");
const css = [...fs.readFileSync(path.join(ui, "index.html"), "utf8").matchAll(/<link rel="stylesheet" href="([^"]+)">/g)]
  .map(([, href]) => fs.readFileSync(path.join(ui, href), "utf8").replace(/@import[^;]+;/g, "")).join("\n");
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    for (const viewport of [{ width: 1650, height: 1082 }, { width: 1280, height: 720 }]) {
      const page = await browser.newPage({ viewport });
      await page.route("**/*", route => route.abort());
      await page.setContent(`<!doctype html><style>${css}</style><body></body>`);
      for (const cls of ["v4-auto", "ex-pop", "tm-pop"]) {
        const box = await page.evaluate((cls) => {
          document.querySelectorAll("dialog").forEach((d) => d.remove());
          const dlg = document.createElement("dialog");
          dlg.className = `crew-dialog crew-popup ${cls}`;
          dlg.innerHTML = `<div class="crew-popup-head has-cover"><div class="crew-popup-who"><div><h2>리서치</h2><p>${"긴 설명 ".repeat(60)}</p></div></div><div class="crew-popup-actions"><button>✕</button></div></div>
            <div class="crew-popup-tabs"><button class="crew-popup-tab on">개요</button></div>
            <div class="dialog-body crew-popup-body ${cls === "tm-pop" ? "tm-pop-body" : ""}"><div style="height:2400px;flex:none"></div><div class="auto-bar">저장</div></div>`;
          document.body.append(dlg);
          dlg.showModal();
          const r = dlg.getBoundingClientRect(), body = dlg.querySelector(".crew-popup-body"), bar = dlg.querySelector(".auto-bar").getBoundingClientRect();
          return { top: r.top, bottom: r.bottom, scrolls: body.scrollHeight > body.clientHeight, barBottom: bar.bottom, bodyBottom: body.getBoundingClientRect().bottom };
        }, cls);
        const at = `${cls} @ ${viewport.width}×${viewport.height}`;
        assert.ok(box.top >= 0 && box.bottom <= viewport.height, `${at}: popup stays inside the window (${box.top}–${box.bottom})`);
        assert.ok(box.scrolls, `${at}: the body scrolls`);
        assert.ok(box.barBottom <= box.bodyBottom + 0.5 && box.barBottom <= box.bottom + 0.5, `${at}: the 저장 bar is visible (${box.barBottom})`);
      }
      await page.close();
    }
    console.log("crew popup fit: ok");
  } finally { await browser.close(); }
})().catch((error) => { console.error(error); process.exit(1); });
