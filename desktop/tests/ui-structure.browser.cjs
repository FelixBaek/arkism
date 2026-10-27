// Compare the same real product screens before/after modularization.
// All host commands, API data and images are fixtures; external networking is blocked.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const {chromium} = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname,"../frontend/node_modules/playwright-core"));
const desktop = path.resolve(__dirname,".."), fixtureRoot = path.join(desktop,"ui/testdata/preview");
function fixtures(dir, prefix = "") {
  const out = {};
  for (const item of fs.readdirSync(dir,{withFileTypes:true})) {
    if (item.isDirectory()) Object.assign(out,fixtures(path.join(dir,item.name),prefix+item.name+"/"));
    else if (item.name.endsWith(".json")) out[prefix+item.name] = JSON.parse(fs.readFileSync(path.join(dir,item.name)));
  }
  return out;
}
const data = fixtures(fixtureRoot);
data["v1/memories.json"].items[0].text = data["v1/memories.json"].items[0].summary = "저장";
const directory = path.resolve(process.env.ARKISM_UI_DIRECTORY || path.join(desktop,"ui"));
const output = process.env.ARKISM_UI_SCREENSHOTS || path.join(require("node:os").tmpdir(), "arkism-ui-structure");
fs.mkdirSync(output,{recursive:true});
(async () => {
  const browser = await chromium.launch({headless:true,...(process.env.CHROMIUM_PATH?{executablePath:process.env.CHROMIUM_PATH}:{})});
  const page = await browser.newPage({locale:"ko-KR",reducedMotion:"reduce",viewport:{width:1440,height:1000}});
  page.setDefaultTimeout(10000);
  const errors=[], requests=[], snapshots={};
  page.on("pageerror", e => errors.push(e.message));page.on("request", r => requests.push(r.url()));
  try {
    await page.route("**/*",async route => {
      const url = new URL(route.request().url());
      if (url.origin !== "https://arkism.fixture") return route.abort();
      const file = path.resolve(directory,"."+(url.pathname==="/"?"/index.html":decodeURIComponent(url.pathname)));
      if (!file.startsWith(directory+path.sep)) return route.abort();
      try { await route.fulfill({body:fs.readFileSync(file),contentType:{".mjs":"application/javascript",".js":"application/javascript",".html":"text/html",".css":"text/css",".svg":"image/svg+xml",".woff2":"font/woff2"}[path.extname(file)]||"application/octet-stream"}); }
      catch { await route.fulfill({status:404,body:""}); }
    });
    await page.addInitScript(({data}) => { localStorage.setItem("arkism.mode","local");
      window.dialogEvents=[];
      for(const name of ["showModal","close"]){const original=HTMLDialogElement.prototype[name];HTMLDialogElement.prototype[name]=function(...args){window.dialogEvents.push([name,this.className]);return original.apply(this,args)}}
      const NativeDate=Date;
      window.Date=class extends NativeDate {constructor(...args){super(...(args.length?args:["2026-10-21T12:00:00Z"]))}static now(){return new NativeDate("2026-10-21T12:00:00Z").getTime()}};
      localStorage.setItem("arkism.look",JSON.stringify({theme:"dark",lang:"ko"}));
      window.open=()=>null; // popups stay on this page to be checked (popup-drag.browser.cjs covers 새 창)
      window.__TAURI__={event:{listen:async()=>()=>{}},core:{async invoke(cmd,args={}){
        if(cmd==="app_defaults")return {local:true,updates:false};
        if(cmd==="local_start")return {server:"http://127.0.0.1:8091",token:"inert-browser-fixture"};
        if(cmd==="capture_settings")return {paused:false,exclude:[],instructions:""};
        if(cmd==="model_roles")return {embedder:"auto",judge:"fixed",cloudflare_account:"",cloudflare_token_saved:false}; // 설정 › 에이전트 › 스킬·MCP·플러그인
        if(cmd!=="api")return null;
        const url=new URL(args.path,"https://fixture.invalid"),base=url.pathname.replace(/^\//,""),q=url.searchParams,status=q.get("status"),kind=q.get("kind");
        if(base.endsWith("/query")) {
          const inq=JSON.parse(args.body||"{}"), original=base.slice(0,-6), candidateState=inq.state==="conflicted"?"conflicted":"candidate", suffix=original==="v1/memory_candidates"?"."+candidateState:"";
          const source=data[original+suffix+".json"]||data[original+".json"]||{items:[]};
          const items=(source.items||[]).filter(x=>!inq.q||String(x.text||x.name||x.display_name||"").toLowerCase().includes(inq.q.toLowerCase()));
          return {status:200,body:JSON.stringify({...source,items,total:items.length,next_cursor:"",facets:{kinds:{},repositories:{},categories:{},states:{}},authority:"data_only_untrusted"})};
        }
        const candidates=[];
        if((args.method||"GET")==="GET") {if(kind&&status){candidates.push(base+"."+kind+"."+status+".json");}if(kind){candidates.push(base+"."+kind+".json");}if(status){candidates.push(base+"."+status+".json");}candidates.push(base+".json");}
        else candidates.push(base+".post.json");
        const key=candidates.find(k=>data[k]);
        return {status:200,body:JSON.stringify(key?data[key]:{items:[],next_cursor:"",authority:"data_only_untrusted"})};
      }}};
    },{data});
    async function capture(name) {
      await page.evaluate(async()=>{await document.fonts.ready;await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));});
      snapshots[name]=await page.evaluate(()=>({text:document.body.innerText,theme:document.documentElement.dataset.theme,buttons:[...document.querySelectorAll("button")].filter(n=>n.getClientRects().length).map(n=>[n.innerText,n.className,n.getAttribute("aria-label"),n.getAttribute("aria-selected"),n.getAttribute("aria-pressed")])}));
      await page.screenshot({path:path.join(output,name+".png"),animations:"disabled"});
    }
    await page.goto("https://arkism.fixture/");
    await page.waitForFunction(()=>document.querySelector("#app")?.inert===false&&document.querySelector("#count-memories")?.textContent);
    await capture("home");
    if (!process.env.ARKISM_UI_BASELINE) assert.equal(requests.some(u=>/i18n-en/.test(u)),false,"Korean startup fetched English");
    await page.locator("button.home-link",{hasText:"기억 보기"}).click();
    await page.locator("#table [data-id]").first().waitFor();
    await page.locator("#table [data-id]").first().focus();await page.keyboard.press("Enter");
    await page.locator("#detail h2").waitFor();await capture("memory");
    await page.locator('[data-menu="agent"]').click();
    await page.getByRole("tab",{name:/워크플로우/}).click();
    await page.locator(".wf-card, .crew-card").first().waitFor();await capture("workflows");
    await page.evaluate(()=>window.dispatchEvent(new CustomEvent("arkism:open-automation",{detail:{id:"wf-research"}})));
    await page.locator("dialog[open]").waitFor();await capture("workflow-popup");
    await page.getByRole("button",{name:"그래프로 편집 →",exact:true}).click();
    await page.locator(".g-stage").waitFor();await capture("graph");
    await page.locator(".g-node[data-id=plan] .g-node-head > .g-icon").click();
    await page.locator(".g-right").getByRole("button",{name:"자세히",exact:true}).click();
    await page.locator("dialog[open]").waitFor();await capture("node-editor");
    await page.keyboard.press("Escape");
    await page.locator("dialog[open]").waitFor({state:"detached"});
    await page.locator("#settings-button").click();
    // #218: 설정 › 역할별 모델 opens with the AI 도구 기본 모델 picker, its list grouped by provider like the workflow model list.
    await page.getByRole("tab",{name:"역할별 모델",exact:true}).click();
    const assist=page.locator(".ag-roles").first().getByRole("combobox",{name:"AI 도구 기본 모델"});
    await assist.click();await page.locator(".combo-pop .combo-head").first().waitFor();
    assert.equal(await page.locator(".combo-pop .combo-head .brand-mark").count()>0,true,"provider heads lost their logo");
    assert.equal(await page.locator(".combo-pop .combo-opt small").first().innerText()>"",true,"model rows lost their provider");
    await capture("settings-agents");await page.keyboard.press("Escape");
    await page.getByRole("tab",{name:"모양",exact:true}).click();await capture("settings");
    // English loads only on demand; original user content survives both language changes. The language switch is in 설정 › 일반.
    await page.getByRole("tab",{name:"일반",exact:true}).click();
    await page.getByRole("tab",{name:"English",exact:true}).click();
    await page.waitForFunction(()=>window.ArkismI18n.lang()==="en");
    assert.equal(requests.some(u=>/i18n-en/.test(u)),true);
    await page.getByRole("tab",{name:"한국어",exact:true}).click();
    await page.waitForFunction(()=>window.ArkismI18n.lang()==="ko");
    await page.locator('[data-menu="home"]').click();
    await page.locator("button.home-link",{hasText:"기억 보기"}).click();
    assert.equal(await page.locator("#table").getByText("저장",{exact:true}).count()>0,true);
    await page.evaluate(async()=>{ await window.ArkismI18n.setLang("en"); });
    assert.equal(await page.locator("#table").getByText("저장",{exact:true}).count()>0,true,"English changed user text");
    // #194: crew names, categories and intros are the user's words, even when one matches a dictionary entry.
    await page.locator('[data-menu="agent"]').click();
    await page.locator("#area-tabs .area-tab").nth(0).click();await page.locator(".wf-card").first().waitFor();
    const flowCats=new Set(data["v1/crew.json"].items.map(r=>r.crew.category));
    const chipCats=await page.locator(".auto-cat-chip span").allInnerTexts();
    assert.ok(chipCats.length>0&&chipCats.every(c=>flowCats.has(c)),"workflow category chips translated: "+chipCats);
    await page.locator(".auto-bar-top select").first().selectOption("agent");await page.locator('.flow-card[data-flow-kind="agent"]').first().waitFor();
    const agentsFixture=data["v1/crew.json"].items.filter(r=>r.crew.type==="agent");
    assert.deepEqual(await page.locator('.flow-card[data-flow-kind="agent"] strong').allInnerTexts(),agentsFixture.map(r=>r.crew.name));
    assert.deepEqual(await page.locator(".flow-card-category").allInnerTexts(),agentsFixture.map(r=>r.crew.category));
    assert.equal(await page.locator(".flow-card",{hasText:"코드리뷰어"}).getAttribute("title"),"코드리뷰어");
    await page.locator(".flow-card",{hasText:"디자이너"}).click();
    const flowNames=new Set(data["v1/crew.json"].items.map(r=>r.crew.name));
    const flowOptions=await page.locator("dialog[open] .ex-flow-pick option").allInnerTexts();
    assert.ok(flowOptions.length>1&&flowOptions.slice(1).every(n=>flowNames.has(n)),"workflow options translated: "+flowOptions);
    assert.equal(await page.locator("dialog[open] .ex-role").innerText(),"UI 시안 정리 · 디자인 검토");
    await page.keyboard.press("Escape");await page.locator("dialog[open]").waitFor({state:"detached"});
    // #193: the time before a meeting title is translated; the title is not, and the rows fit at 900px.
    await page.locator("#area-tabs .area-tab").nth(2).click();
    const lastMeeting=page.locator(".cu-team .tm-urow").nth(1).locator("> span").last();
    await lastMeeting.waitFor();
    assert.match(await lastMeeting.innerText(),/^\d+ days? ago · 로그인 화면 다크 테마 검토$/);
    await page.setViewportSize({width:900,height:1000});
    assert.deepEqual(await page.evaluate(()=>[...document.querySelectorAll(".cu-table .tm-urow")].filter(r=>r.scrollWidth>r.clientWidth).length),0,"usage rows overflow at 900px");
    await page.setViewportSize({width:1440,height:1000});
    await page.evaluate(async()=>{ await window.ArkismI18n.setLang("ko"); });
    if (!process.env.ARKISM_UI_BASELINE) { // #199: 템플릿·설정으로 만들기 asks before it drops a new automation's typing
      await page.locator('[data-menu="agent"]').click();
      await page.getByRole("tab",{name:/워크플로우/}).click();
      await page.getByRole("button",{name:"+ 새 워크플로우",exact:true}).click();
      await page.locator(".flow-create-option",{hasText:"단계 구성"}).click();
      const pop=page.locator("dialog.v4-auto-new[open]"), confirm=page.locator("dialog.cn-confirm[open]"), other=pop.getByRole("button",{name:"템플릿·설정으로 만들기",exact:true});
      await pop.getByRole("textbox",{name:"하려는 일"}).fill("매일 아침 뉴스 요약");
      await other.click();
      await confirm.getByText("아직 만들지 않았어요. 버리고 닫을까요?").waitFor();
      await confirm.getByRole("button",{name:"취소",exact:true}).click();
      await confirm.waitFor({state:"detached"});
      assert.equal(await pop.count(),1,"취소 keeps the new automation");
      await other.click();
      await confirm.getByRole("button",{name:"확인",exact:true}).click();
      await pop.waitFor({state:"detached"});
      await page.locator("dialog[open]:not(.cn-confirm)").waitFor(); // the template/settings window
      await page.keyboard.press("Escape");
    }
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(output,"screens.json"),JSON.stringify(snapshots,null,2));
    console.log("UI structure: 8 real screens, keyboard opening/Escape, lazy English, Korean round trip, user text and crew user data passed");
  } catch(error) {console.error(errors);console.error(await page.evaluate(()=>window.dialogEvents));console.error((await page.locator("body").innerText()).slice(-3500));throw error;}
  finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
