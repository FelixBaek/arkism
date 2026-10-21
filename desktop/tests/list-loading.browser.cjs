// Real product DOM, bounded fake API, keyboard and measured rows. No product data.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
const output = process.env.ARKISM_LIST_EVIDENCE || path.join(require("node:os").tmpdir(), "arkism-list-evidence");
fs.mkdirSync(output, { recursive: true });
const ui = path.resolve(process.env.ARKISM_UI_DIRECTORY || path.resolve(__dirname, "../ui"));
(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  const page = await browser.newPage({ locale: "ko-KR", viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(12000);
  const errors = []; page.on("pageerror", error => errors.push(error.message));
  try {
    await page.route("**/*", async route => {
      const url = new URL(route.request().url());
      if (url.origin !== "https://arkism.fixture") return route.abort();
      const file = path.resolve(ui, "." + (url.pathname === "/" ? "/index.html" : decodeURIComponent(url.pathname)));
      if (!file.startsWith(ui + path.sep)) return route.abort();
      try { await route.fulfill({ body: fs.readFileSync(file), contentType: { ".html": "text/html", ".mjs": "application/javascript", ".js": "application/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".woff2": "font/woff2" }[path.extname(file)] || "application/octet-stream" }); }
      catch { await route.fulfill({ status: 404, body: "" }); }
    });
    await page.addInitScript(() => {
      window.listCalls = []; window.versionCalls = []; window.failCollection = ""; window.holdCollection = "";
      // Deterministic user-driven requests; the refresh scheduler is tested separately.
      const interval = window.setInterval;
      window.setInterval = (fn, ms, ...args) => ms === 10000 ? 0 : interval(fn, ms, ...args);
      window.__TAURI__ = { event: { listen: async () => () => {} }, core: { async invoke(command, args) {
        if (command === "app_defaults") return { local: true, updates: false };
        if (command === "local_start") return { server: "http://127.0.0.1:8091", token: "inert-fixture" };
        if (command !== "api") return null;
        const url = new URL(args.path, "https://fixture.invalid"), p = url.pathname;
        const catalog = p.endsWith("/query");
        const q = catalog ? new URLSearchParams(Object.entries(JSON.parse(args.body)).map(([k,v])=>[k,String(v)])) : url.searchParams;
        const route = catalog ? p.slice(0,-6) : p;
        let body = { items: [], next_cursor: "", authority: "data_only_untrusted" };
        if (p === "/v1/capabilities") body = { role: "human", actions: { review_list: true, review_decide: true } };
        if (p === "/v1/providers") body = { providers: [] };
        if (p.startsWith("/v1/skill_versions/")) { window.versionCalls.push(p.split("/").pop()); body = { id: p.split("/").pop(), analysis: {} }; }
        const name = route === "/v1/memories" ? "memories" : route === "/v1/memory_candidates" ? (q.get("state") || q.get("status")) === "conflicted" ? "conflicted" : "candidates" : route === "/v1/skills" ? q.get("state") === "trash" ? "skillTrash" : "skills" : p === "/v1/skills/trash" ? "skillTrash" : "";
        if (name) {
          const start = Number(q.get("cursor")) || 0, limit = Number(q.get("limit"));
          window.listCalls.push({ name, start, limit });
          if (start && name === window.failCollection) { window.failCollection = ""; return { status: 503, body: JSON.stringify({ code: "dependency_unavailable" }) }; }
          if (start && name === window.holdCollection) await new Promise(resolve => { window.releaseCollection = resolve; });
          const count = name === "skillTrash" ? 180 : 5000;
          const items = Array.from({ length: Math.min(limit, count - start) }, (_, offset) => {
            const i = start + offset, id = `${name}-${String(i).padStart(5, "0")}`;
            return { id, name: id, description: `Description ${i}`, kind: "semantic", status: name === "candidates" ? "candidate" : name === "conflicted" ? "conflicted" : "active", version_id: name === "skills" && i >= 50 ? `v-${i}` : "", text: name === "candidates" && window.groupCandidates ? "응답은 짧게" : `${name} ${i}${i % 3 === 0 ? " a long line of text".repeat(15) : ""}`, revision: 1, created_at: "2026-10-20T00:00:00Z", updated_at: "2026-10-20T00:00:00Z", created_by_type: "human", tags: [], sources: [], skill_compatibility: {} };
          });
          body = { items, next_cursor: start + limit < count ? String(start + limit) : "", authority: "data_only_untrusted", ...(catalog ? { total: count, facets: {kinds: {}, repositories: {}, categories: {"": count}} } : {}) };
        }
        return { status: 200, body: JSON.stringify(body) };
      } } };
    });
    await page.goto("https://arkism.fixture/");
    await page.waitForFunction(() => document.querySelector("#app")?.inert === false && document.querySelector("#count-memories")?.textContent === "50+");
    assert.deepEqual(await page.evaluate(() => window.listCalls.map(c => [c.name, c.start, c.limit]).sort((a, b) => a[0].localeCompare(b[0]))), ["candidates", "conflicted", "memories", "skills", "skillTrash"].map(name => [name, 0, 50]).sort((a, b) => a[0].localeCompare(b[0])));
    await page.getByRole("button", { name: /^기억 보기/ }).click();
    async function next(name) {
      const foot = page.locator(`[data-collection="${name}"]`);
      await foot.getByRole("button").click();
      await page.waitForFunction(name => !document.querySelector(`[data-collection="${name}"] button`)?.disabled, name);
    }
    for (let i = 0; i < 7; i++) await next("memories");
    await page.locator(".virtual-list").waitFor();
    assert.ok(await page.locator("#table [data-id]").count() < 80);
    assert.equal(await page.locator(".virtual-item .row").first().evaluate(row => getComputedStyle(row).borderBottomWidth), "1px");
    assert.equal(await page.evaluate(() => window.listCalls.filter(c => c.name === "memories" && c.start).length), 7);
    await page.evaluate(() => { const table = document.querySelector("#table"); table.scrollTop = 0; });
    await page.locator('[data-id="memories-00000"]').focus();
    await page.keyboard.press("End");
    await page.waitForFunction(() => document.activeElement?.dataset.id === "memories-00399");
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => document.querySelector("#detail")?.textContent.includes("memories 399"));
    await page.locator('[data-id="memories-00399"]').focus(); await page.keyboard.press("Home");
    await page.waitForFunction(() => document.activeElement?.dataset.id === "memories-00000");
    for (let i = 0; i < 35; i++) await page.keyboard.press("Tab");
    assert.equal(await page.evaluate(() => document.activeElement.dataset.id), "memories-00035");
    await page.evaluate(() => { document.querySelector("#table").scrollTop = 0; });
    await page.waitForTimeout(100);
    assert.equal(await page.evaluate(() => document.activeElement.dataset.id), "memories-00035", "scroll unmounted the focused row");
    assert.ok(await page.locator("#table [data-id]").count() < 80);
    await page.setViewportSize({ width: 760, height: 900 });
    await page.evaluate(() => document.documentElement.style.setProperty("--fs", "1.5"));
    await page.waitForTimeout(150);
    await page.locator('[data-id="memories-00035"]').focus(); await page.keyboard.press("End");
    await page.waitForFunction(() => document.activeElement?.dataset.id === "memories-00399");
    assert.ok(await page.locator("#table [data-id]").count() < 80);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.evaluate(() => document.documentElement.style.removeProperty("--fs"));
    await page.screenshot({ path: path.join(output, "memory.png") });
    // Candidates and conflicts retain a next-page control when local search hides all rows.
    await page.getByRole("combobox", { name: "모아보기", exact: true }).selectOption("provenance");
    await page.getByRole("tab", { name: /승인 대기/ }).click();
    await page.getByRole("searchbox", { name: "기억 검색", exact: true }).fill("outside-loaded");
    await next("candidates");
    assert.equal(await page.evaluate(() => window.listCalls.filter(c => c.name === "candidates" && c.start).length), 1);
    await page.evaluate(() => window.groupCandidates = true);
    await page.getByRole("searchbox", { name: "기억 검색", exact: true }).fill("");
    await next("candidates");
    await page.getByRole("tab", { name: /승인 대기/ }).click();
    await page.locator("details.candidate-group").waitFor();
    assert.equal(await page.locator(".candidate-children [data-id]").count(), 0);
    await page.locator("details.candidate-group summary").click();
    await page.waitForFunction(() => document.querySelectorAll(".candidate-children [data-id]").length === 50);
    await page.getByRole("button", { name: "다음 후보", exact: true }).click();
    assert.equal(await page.locator(".candidate-children [data-id]").last().getAttribute("data-id"), "candidates-00099");
    await page.getByRole("button", { name: "이전 후보", exact: true }).click();
    assert.equal(await page.locator(".candidate-children [data-id]").first().getAttribute("data-id"), "candidates-00000");
    await page.screenshot({ path: path.join(output, "candidates.png") });
    await page.locator("details.candidate-group summary").click();
    await page.waitForFunction(() => document.querySelectorAll(".candidate-children [data-id]").length === 0);
    await page.getByRole("tab", { name: /헷갈림/ }).click();
    await next("conflicted");
    // Skills: failure preserves rows, retry advances once; search never hides the remote cursor.
    await page.locator('[data-menu="connect"]').click();
    await page.getByRole("tab", { name: /^스킬/ }).click();
    await page.locator(".v4s-row").first().waitFor();
    await page.evaluate(() => window.failCollection = "skills");
    await next("skills");
    await page.locator('[data-collection="skills"]').getByRole("button", { name: "다시 불러오기", exact: true }).waitFor();
    assert.equal(await page.locator(".v4s-row").count(), 50);
    await next("skills");
    assert.equal(await page.locator(".v4s-row").count(), 100);
    await page.waitForFunction(() => window.versionCalls.includes("v-99"));
    for (let i = 0; i < 4; i++) await next("skills");
    assert.ok(await page.locator(".v4s-row").count() < 80);
    await page.screenshot({ path: path.join(output, "skills.png") });
    await page.getByRole("searchbox", { name: "스킬 검색", exact: true }).fill("not-loaded");
    await next("skills");
    await page.getByRole("searchbox", { name: "스킬 검색", exact: true }).fill("");
    await page.getByRole("combobox", { name: "상태", exact: true }).selectOption("trash");
    await page.locator('[data-collection="skillTrash"]').waitFor();
    await next("skillTrash");
    await page.waitForFunction(() => [...document.querySelectorAll(".v4s-name")].some(n => n.textContent === "skillTrash-00099"));
    assert.equal(await page.evaluate(() => window.listCalls.filter(c => c.name === "skillTrash" && c.start).length), 1);
    await page.getByRole("combobox", { name: "상태", exact: true }).selectOption("all");
    async function revealAll() {
      for (let i = 0; i < 30; i++) {
        const status = await page.locator('[data-collection="skills"] [role="status"]').innerText();
        const counts = status.match(/불러온 (\d+)개 중 (\d+)개 표시/);
        if (!counts || Number(counts[1]) <= Number(counts[2])) return;
        await next("skills");
      }
      throw Error("Local display limit did not advance");
    }
    // A late page may redraw controls, but cannot take focus/caret from typing.
    await revealAll(); await page.evaluate(() => { window.holdCollection = "skills"; window.releaseCollection = null; });
    await page.locator('[data-collection="skills"] button').click();
    await page.waitForFunction(() => !!window.releaseCollection);
    await page.getByRole("searchbox", { name: "스킬 검색", exact: true }).fill("skills");
    await page.getByRole("searchbox", { name: "스킬 검색", exact: true }).evaluate(node => node.setSelectionRange(2, 2));
    await page.evaluate(() => { window.holdCollection = ""; window.releaseCollection(); });
    await page.waitForFunction(() => document.querySelector("#controls input[aria-label='스킬 검색']")?.value === "skills");
    assert.deepEqual(await page.evaluate(() => [document.activeElement.getAttribute("aria-label"), document.activeElement.selectionStart]), ["스킬 검색", 2]);
    // Pending next page must not repaint another screen or steal its focus.
    await page.getByRole("searchbox", { name: "스킬 검색", exact: true }).fill("");
    await revealAll(); await page.evaluate(() => { window.holdCollection = "skills"; window.releaseCollection = null; });
    await page.locator('[data-collection="skills"] button').click();
    await page.waitForFunction(() => !!window.releaseCollection);
    await page.locator('[data-menu="home"]').click();
    await page.evaluate(() => window.releaseCollection());
    await page.waitForTimeout(100);
    assert.equal(await page.locator("#app.skills-mode").count(), 0);
    assert.deepEqual(errors, []);
    const report = { loadedMemories: 400, calls: await page.evaluate(() => window.listCalls) };
    fs.writeFileSync(path.join(output, "report.json"), JSON.stringify(report, null, 2));
    console.log("Lists passed: 50 per initial collection, 400 memories windowed, variable heights/zoom/narrow viewport, End/Home/Tab/focus, candidate/conflict cursors, skill retry/virtualization, trash next page, candidate group pages, new skill version checks, typing caret and late navigation.");
  } catch (error) { console.error(errors); console.error((await page.locator("body").innerText()).slice(-3500)); throw error; }
  finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
