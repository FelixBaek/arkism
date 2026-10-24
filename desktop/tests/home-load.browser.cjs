// Home and the memory overview when one list fails or the whole load fails (#186-#190). Real product DOM, fake API.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname, "../frontend/node_modules/playwright-core"));
const ui = path.resolve(process.env.ARKISM_UI_DIRECTORY || path.resolve(__dirname, "../ui"));

async function open(browser, fail, clear = false) {
  const page = await browser.newPage({ locale: "ko-KR", viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(12000);
  const errors = []; page.on("pageerror", error => errors.push(error.message));
  await page.route("**/*", async route => {
    const url = new URL(route.request().url());
    if (url.origin !== "https://arkism.fixture") return route.abort();
    const file = path.resolve(ui, "." + (url.pathname === "/" ? "/index.html" : decodeURIComponent(url.pathname)));
    if (!file.startsWith(ui + path.sep)) return route.abort();
    try { await route.fulfill({ body: fs.readFileSync(file), contentType: { ".html": "text/html", ".mjs": "application/javascript", ".js": "application/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)] || "application/octet-stream" }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  });
  await page.addInitScript(([fail, clear]) => { localStorage.setItem("arkism.mode","local");
    const DAY = 86400000, at = (ms) => new Date(Date.now() - ms).toISOString(), old = at(20 * DAY);
    const row = (id, status) => ({ id, text: id, kind: "user", status, revision: 1, created_at: old, updated_at: old, tags: [], sources: [], skill_compatibility: {} });
    window.__TAURI__ = { event: { listen: async () => () => {} }, core: { async invoke(command, args) {
      if (command === "app_defaults") return { local: true, updates: false };
      if (command === "local_start") return { server: "http://127.0.0.1:8091", token: "inert-fixture" };
      if (command !== "api") return null;
      const url = new URL(args.path, "https://fixture.invalid"), p = url.pathname, list = (items, next = "") => ({ items, next_cursor: next, authority: "data_only_untrusted" });
      if ((window.__fail || fail).includes(p)) return { status: 500, body: JSON.stringify({ code: "internal" }) };
      let body = list([]);
      if (p === "/v1/capabilities") body = { role: "human", actions: { review_list: true, review_decide: true } };
      if (p === "/v1/providers") body = { providers: [] };
      if (p === "/v1/memories") body = list(["m1", "m2", "m3"].map((id) => row(id, "active")));
      if (clear && ["/v1/memory_candidates", "/v1/memory_candidates/query", "/v1/usage/sessions"].includes(p)) return { status: 200, body: JSON.stringify(list([])) }; // nothing to check
      if (p === "/v1/memory_candidates" && url.searchParams.get("status") === "candidate") body = list(Array.from({ length: 50 }, (_, i) => row(`c${i}`, "candidate")), "50");
      if (p === "/v1/memories/query") body = { items: [], total: 3, next_cursor: "", facets: { kinds: { user: 3 } } };
      if (p === "/v1/memory_candidates/query") body = { items: [], total: 120, next_cursor: "", facets: {} };
      if (p === "/v1/agent_tools") body = { agent_tools: { collected_at: at(60000), agents: [{ id: "claude_code", found: true, enabled: true }] } };
      // one session ended 15 days ago and nothing was extracted since: the stall is 360 hours
      if (p === "/v1/usage/sessions") body = { items: [{ id: "s1", ended_at: at(15 * DAY) }, { id: "s2", ended_at: at(3600000) }], total: 2, next_cursor: "" };
      return { status: 200, body: JSON.stringify(body) };
    } } };
  }, [fail, clear]);
  await page.goto("https://arkism.fixture/");
  await page.waitForFunction(() => document.querySelector("#app")?.inert === false && !document.querySelector("#status-dot")?.className.includes("loading"));
  return { page, errors };
}
const text = (page) => page.evaluate(() => document.body.innerText);
const times = (s, part) => s.split(part).length - 1;
const shot = (page, name) => process.env.ARKISM_HOME_EVIDENCE && page.screenshot({ path: path.join(process.env.ARKISM_HOME_EVIDENCE, `${name}.png`) });
async function overview(page) {
  await page.locator('.nav-item[data-menu="memory"]').click(); // its first tab is 개요
  await page.locator(".ov-host").waitFor();
  return page.locator("#table").innerText();
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    // #186: the skill list fails, the memories still load and Home names only the failed list.
    let { page, errors } = await open(browser, ["/v1/skills", "/v1/memory_usage"]);
    await page.waitForFunction(() => document.body.innerText.includes("기억 후보"));
    let home = await text(page);
    assert.ok(!home.includes("서버에 문제가 생겼어요"), "a failed skill list is not a whole load error");
    assert.ok(home.includes("스킬 목록을 불러오지 못했어요"), "the failed list says so");
    // #188: the server total of candidates, not the first page of 50
    assert.ok(home.includes("기억 후보 120개 검토"), "review count is the server total");
    // #190: 360 hours read as days
    await page.waitForFunction(() => document.body.innerText.includes("째 멈췄어요"));
    home = await text(page);
    assert.ok(home.includes("기억 추출이 15일째 멈췄어요") && !home.includes("360시간"), "long stall in days");
    // #189: an agent the finished report leaves out is not connected, not checking
    await shot(page, "partial-home");
    const opencode = await page.locator(".home-agent", { hasText: "OpenCode" }).innerText();
    assert.ok(opencode.includes("연결 안 됨"), `OpenCode row: ${opencode}`);
    const stalled = await overview(page);
    await shot(page, "partial-overview");
    assert.ok(stalled.includes("15일째 멈춤") && !stalled.includes("360시간"), "overview stall in days");
    // the use counts did not load: 불러오기 is unknown, not "0회"
    assert.ok(!stalled.includes("0회") && (await page.locator(".ov-chip", { hasText: "사용량" }).innerText()).includes("—"), "recall unknown without usage");
    // the status card lists the failed list instead of a healthy 0-skill hub
    await page.locator("#status-toggle").click();
    const card = await page.locator("#status-list").innerText();
    assert.equal(await page.locator("#count-skills").innerText(), "—", "sidebar skill count unknown");
    assert.ok(card.includes("불러오지 못한 목록") && !/스킬 허브\s*0개/.test(card), `status card: ${card}`);
    // a reload that fails drops the last good load's agents instead of showing them as current
    await page.evaluate(() => { window.__fail = ["/v1/memories"]; });
    await page.locator("#status-toggle").click();
    await page.locator('.nav-item[data-menu="home"]').click();
    await page.getByRole("button", { name: "다시 불러오기" }).first().click();
    await page.waitForFunction(() => document.body.innerText.includes("서버에 문제가 생겼어요"));
    await page.locator("#status-toggle").click();
    const claude = await page.locator('#status-agents [data-agent="claude_code"] .st-badge').innerText();
    assert.ok(!claude.includes("연결됨"), `stale agent after a failed reload: ${claude}`);
    const failedCard = await page.locator("#status-list").innerText();
    assert.ok(!/스킬 허브\s*0개/.test(failedCard), `failed load skill hub: ${failedCard}`);
    assert.deepEqual(errors, []);
    await page.close();

    // #187: the memory list fails; nothing claims a stall, zeros or a healthy pipeline, and the error reads once.
    ({ page, errors } = await open(browser, ["/v1/memories"]));
    await page.waitForFunction(() => document.body.innerText.includes("서버에 문제가 생겼어요"));
    await page.waitForTimeout(500); // the session list lands after the load
    home = await text(page);
    assert.ok(!home.includes("멈췄어요") && !home.includes("검토할 기억 0개") && !home.includes("지금은 확인할 일이 없어요"), "no stall, zero count or all-clear after a failed load");
    await shot(page, "failed-home");
    assert.equal(times(home, "서버가 요청을 처리하지 못했어요"), 1, "the error sentence shows once");
    const summary = await page.locator(".home-nums").innerText();
    assert.ok(!/먼저 확인\s*0/.test(summary) && !home.includes("새 일이 생기면 여기에 모여요"), `summary counts unknown: ${summary}`);
    await page.getByRole("button", { name: "전체 항목 보기" }).click();
    await page.waitForTimeout(200);
    assert.ok(!(await page.locator("#table").innerText()).includes("확인할 일이 없어요"), "전체 항목 keeps the error");
    const unknown = await overview(page);
    await shot(page, "failed-overview");
    assert.ok(!unknown.includes("멈춤") && !unknown.includes("모두 분류됨") && !unknown.includes("모두 움직임"), "overview does not look healthy or stalled");
    assert.ok(unknown.includes("알 수 없음"), "overview says unknown");
    assert.deepEqual(errors, []);
    await page.close();

    // the candidate total and the sessions fail: the first page is not passed off as the total, the session chip waits
    ({ page, errors } = await open(browser, ["/v1/memory_candidates/query", "/v1/usage/sessions"]));
    await page.waitForFunction(() => document.body.innerText.includes("기억 후보"));
    await page.waitForTimeout(500);
    assert.ok((await text(page)).includes("기억 후보 50개 이상 검토"), "unknown total reads as at least the loaded page");
    assert.notEqual(await page.locator("#count-todo").innerText(), "0", "a row with an unknown count still counts in the sidebar");
    await overview(page);
    assert.ok((await page.locator(".ov-chip", { hasText: "세션 기록" }).innerText()).includes("—"), "session chip unknown");
    assert.deepEqual(errors, []);
    await page.close();

    // the memory totals fail but the candidate total loads: the review count still uses it
    ({ page, errors } = await open(browser, ["/v1/memories/query"]));
    await page.waitForFunction(() => document.body.innerText.includes("기억 후보 120개 검토"));
    assert.deepEqual(errors, []);
    // Figma 372:27905: nothing to check first. The empty card sits under 먼저 확인 0, 이번 주 and the summary stay.
    ({ page, errors } = await open(browser, [], true));
    await page.locator(".todo-wrap > .st-panel").waitFor();
    await page.waitForTimeout(300);
    home = await text(page);
    assert.ok(home.includes("지금은 확인할 일이 없어요") && /먼저 확인\s*0/.test(await page.locator(".todo-wrap").innerText()), "empty card under 먼저 확인 0");
    assert.ok(home.includes("함께 일하는 전문가") && home.includes("바로가기"), "the summary column stays");
    await shot(page, "clear-home");
    assert.deepEqual(errors, []);
    await page.close();
    console.log("Home load passed: one failed list, server candidate total, unreported agents, stall in days, failed load shows unknown.");
  } finally { await browser.close(); }
})().catch((error) => { console.error(error); process.exit(1); });
