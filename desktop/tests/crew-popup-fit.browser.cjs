// Popup and popout window layout with the real stylesheets (size, scroll and placement regressions):
// - crew popups (automation · expert · team): a tall head and a long body still fit the window, the body scrolls and
//   the sticky 저장 bar stays on screen (86668546: 1650×1082 cut the bar off below the window)
// - FLOW popups open wide, not as narrow 826-940px windows (b25f2d56)
// - a wide memory window scrolls its list alone, under a fixed search bar, and stacks its panes in a narrow window (97204bce)
// - in its own window with the 창 버튼 strip shown, a popup fills the whole window: no 32px band, no 900px cap (beb626a6)
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
          dlg.innerHTML = `<div class="crew-popup-head has-cover"><div class="crew-popup-who"><div><h2>리서치</h2><p>${"긴 설명 ".repeat(300)}</p></div></div><div class="crew-popup-actions"><button>✕</button></div></div>
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
      for (const cls of ["crew-popup", "wf-settings", "node-dialog"]) {
        const w = await page.evaluate((cls) => {
          document.querySelectorAll("dialog").forEach((d) => d.remove());
          const dlg = document.createElement("dialog"); dlg.className = cls; dlg.innerHTML = "<p>x</p>";
          document.body.append(dlg); dlg.showModal();
          return dlg.getBoundingClientRect().width;
        }, cls);
        assert.equal(w, Math.min(1200, viewport.width - 32), `${cls} @ ${viewport.width}: FLOW popups open wide`);
      }
      const wide = await page.evaluate(() => {
        document.querySelectorAll("dialog").forEach((d) => d.remove());
        const dlg = document.createElement("dialog"); dlg.className = "ov-wide-win";
        dlg.innerHTML = `<div class="ov-win-head">h</div><div class="ov-wide-body"><div class="ov-wide-left"><div class="ov-wide-bar"><input class="ov-wide-search"></div><div class="ov-wide-list">${"<div class=\"ov-wide-row\" style=\"height:60px\">row</div>".repeat(80)}</div></div><div class="ov-wide-right">d</div></div>`;
        document.body.append(dlg); dlg.showModal();
        const left = dlg.querySelector(".ov-wide-left"), list = dlg.querySelector(".ov-wide-list"), r = dlg.getBoundingClientRect();
        const lr = left.getBoundingClientRect(), rr = dlg.querySelector(".ov-wide-right").getBoundingClientRect();
        return { inside: r.bottom <= innerHeight, listScrolls: list.scrollHeight > list.clientHeight, leftScrolls: left.scrollHeight > left.clientHeight, stacked: rr.top >= lr.bottom - 1 };
      });
      assert.ok(wide.inside && wide.listScrolls && !wide.leftScrolls, `wide window @ ${viewport.width}: the list scrolls alone, the search bar stays (${JSON.stringify(wide)})`);
      assert.equal(wide.stacked, false, `wide window @ ${viewport.width}: list and detail side by side`);
      await page.close();
    }
    // a narrow window: the wide window stacks list over detail (each half scrolls) instead of squeezing the detail
    {
      const page = await browser.newPage({ viewport: { width: 700, height: 800 } });
      await page.route("**/*", route => route.abort());
      await page.setContent(`<!doctype html><style>${css}</style><body><dialog class="ov-wide-win"><div class="ov-wide-body"><div class="ov-wide-left"><div class="ov-wide-list">${"<p>row</p>".repeat(80)}</div></div><div class="ov-wide-right">d</div></div></dialog></body>`);
      const stacked = await page.evaluate(() => { const d = document.querySelector("dialog"); d.showModal(); return d.querySelector(".ov-wide-right").getBoundingClientRect().top >= d.querySelector(".ov-wide-left").getBoundingClientRect().bottom - 1; });
      assert.ok(stacked, "wide window @ 700: list over detail");
      await page.close();
    }
    // its own window (html.popout) with the 창 버튼 strip shown: the popup is the window, edge to edge
    for (const cls of ["ov-wide-win", "crew-popup ex-pop", "dialog"]) {
      const page = await browser.newPage({ viewport: { width: 900, height: 700 } });
      await page.route("**/*", route => route.abort());
      await page.setContent(`<!doctype html><html class="popout"><style>${css}</style><body><div id="window-bar" class="window-bar"></div><dialog class="${cls} popout-root"><div class="dialog-head">h</div><div class="dialog-body">b</div></dialog></body></html>`);
      const r = await page.evaluate(() => { const d = document.querySelector("dialog"); d.showModal(); const b = d.getBoundingClientRect(); return { top: b.top, left: b.left, width: b.width, height: b.height }; });
      assert.deepEqual(r, { top: 0, left: 0, width: 900, height: 700 }, `${cls} popout window fills the window`);
      await page.close();
    }
    console.log("popup and popout window fit: ok");
  } finally { await browser.close(); }
})().catch((error) => { console.error(error); process.exit(1); });
