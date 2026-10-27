// FLOW integration: one catalogue retains role, team, workflow and template paths.
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
data["v1/crew/templates/research.json"] = {crew:{...structuredClone(data["v1/crew.json"].items.find(r=>r.crew.type==="workflow").crew),research:{quick_node:"plan",deep_node:"collect",deep_alias:"딥리서치",evidence_strategy:"sources-and-gaps-v1"}},existing_id:"wf-research",expected_revision:17,aliases:{standard:"리서치",deep:"딥리서치"},changes:["근거가 부족할 때 추가 조사로 이어져요.","기존 모델·예산·승인·스킬을 유지해요."]};
data["v1/memories.json"].items[0].text = data["v1/memories.json"].items[0].summary = "저장";
const directory = path.resolve(process.env.ARKISM_UI_DIRECTORY || path.join(desktop,"ui"));
const output = process.env.ARKISM_UI_SCREENSHOTS || path.join(require("node:os").tmpdir(), "arkism-flow-ui");
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
      window.dialogEvents=[]; window.apiWrites=[];
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
        if(cmd!=="api")return null; if(args.method && args.method!=="GET") window.apiWrites.push(args);
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
      snapshots[name]=await page.evaluate(()=>({theme:document.documentElement.dataset.theme,width:innerWidth,overflow:document.documentElement.scrollWidth>innerWidth}));
      await page.screenshot({path:path.join(output,name+".png"),animations:"disabled"});
    }
    await page.goto("https://arkism.fixture/");
    await page.waitForFunction(()=>document.querySelector("#app")?.inert===false&&document.querySelector("#count-memories")?.textContent);
    await page.locator('[data-menu="agent"]').click();
    const tabs=page.locator("#area-tabs [role=tab]");
    assert.deepEqual(await tabs.locator("span").allTextContents(),["워크플로우","회의 기록","사용량"]);
    for(const kind of ["agent","team","workflow"]) assert.ok(await page.locator(`[data-flow-kind="${kind}"]`).count(),kind+" remains reachable");
    assert.equal(await page.locator('[data-id="crew:wf-research"]').count(),0,"owned definitions remain behind their role");
    await capture("flow-dark");
    await page.getByRole("combobox",{name:"실행 방식",exact:true}).selectOption("agent");
    assert.ok(await page.locator('[data-flow-kind="agent"]').count());
    assert.equal(await page.locator('[data-flow-kind="team"]').count(),0);
    await page.locator('[data-flow-kind="agent"]').first().focus();await page.keyboard.press("Enter");
    await page.locator("dialog.ex-pop[open]").waitFor();
    assert.match(await page.locator("dialog.ex-pop").innerText(),/모델/);
    assert.match(await page.locator("dialog.ex-pop").innerText(),/스킬/);
    await capture("role-editor");await page.keyboard.press("Escape");
    await page.locator('[data-id="crew:ag-research"]').click();
    await page.locator("dialog.ex-pop .wk-instr").fill("그래프를 보고 돌아와도 남는 초안");
    await page.getByRole("button",{name:"단계 고치기",exact:true}).click();
    await page.locator(".g-stage").waitFor();
    assert.equal(await page.locator("dialog.ex-pop[open]").count(),0);
    await page.getByRole("button",{name:"돌아가기",exact:true}).click();
    await page.locator("dialog.ex-pop[open]").waitFor();
    assert.equal(await page.locator("dialog.ex-pop .wk-instr").inputValue(),"그래프를 보고 돌아와도 남는 초안");
    await page.locator("dialog.ex-pop .ex-savebar").getByRole("button",{name:"취소",exact:true}).click();
    await page.keyboard.press("Escape");
    await page.getByRole("combobox",{name:"실행 방식",exact:true}).selectOption("team");
    await page.locator('[data-flow-kind="team"]').first().click();
    await page.locator("dialog.tm-pop[open]").waitFor();
    await capture("team-editor");await page.keyboard.press("Escape");
    await page.getByRole("combobox",{name:"실행 방식",exact:true}).selectOption("workflow");
    await page.locator('[data-flow-kind="workflow"]').first().click();
    await page.locator("dialog[open]").waitFor();
    assert.match(await page.locator("dialog[open]").innerText(),/기록/);
    await capture("workflow-editor");await page.keyboard.press("Escape");
    const create=page.getByRole("button",{name:"+ 새 워크플로우",exact:true});
    await create.focus();await page.keyboard.press("Enter");
    await page.locator("dialog.flow-create[open]").waitFor();
    assert.match(await page.evaluate(()=>document.activeElement.innerText),/^리서치/);
    await page.keyboard.press("Tab");
    assert.match(await page.evaluate(()=>document.activeElement.innerText),/단독 실행/);
    await capture("create-choices");
    await page.keyboard.press("Escape");
    assert.equal(await create.evaluate(n=>document.activeElement===n),true,"Esc restores keyboard focus");
    for(const [label,selector] of [["단독 실행",".ex-pop"],["협업·회의",".tm-pop"],["단계 구성",".v4-auto-new"]]){
      await create.click();await page.locator(".flow-create-option",{hasText:label}).click();
      await page.locator(`dialog${selector}[open]`).waitFor();await page.keyboard.press("Escape");
    }
    const before=await page.evaluate(()=>window.apiWrites.length);
    await create.click();await page.locator(".flow-create-option",{hasText:/^리서치/}).click();
    await page.locator("dialog.ex-pop[open]").waitFor();
    assert.equal(await page.evaluate(()=>window.apiWrites.length),before,"opening existing research does not install another definition");
    await page.keyboard.press("Escape");
    await page.getByRole("button",{name:"리서치 개선안 검토",exact:true}).click();
    await page.locator("dialog.wf-settings[open]").waitFor();
    assert.equal(await page.locator("dialog.wf-settings input").first().inputValue(),"리서치");
    assert.equal(await page.getByRole("textbox",{name:"깊은 조사 @이름",exact:true}).inputValue(),"딥리서치");
    assert.match(await page.locator(".research-review").innerText(),/시간이 오래 걸린다는 이유로 전환하지 않아요/);
    await page.getByRole("button",{name:"개선안 저장",exact:true}).waitFor();
    assert.equal(await page.evaluate(()=>window.apiWrites.length),before,"opening a template does not install it");
    assert.equal(await page.locator(".wf-preview").evaluate(n=>n.querySelector(".crew-cover-title").getBoundingClientRect().top>=n.querySelector(".wf-preview-empty").getBoundingClientRect().bottom),true,"preview image placeholder does not cover the workflow title");
    await capture("research-improvements-preview");await page.keyboard.press("Escape");
    await page.getByRole("button",{name:"페르소나 설정",exact:true}).click();
    await page.getByRole("tab",{name:"페르소나",exact:true}).waitFor();
    assert.equal(await page.getByRole("tab",{name:"페르소나",exact:true}).getAttribute("aria-selected"),"true");
    await page.locator('[data-menu="agent"]').click();
    await page.getByRole("combobox",{name:"실행 방식",exact:true}).selectOption("all");
    await page.setViewportSize({width:900,height:800});
    await page.evaluate(()=>document.documentElement.dataset.theme="light");
    await capture("flow-light-900");
    assert.equal(snapshots["flow-light-900"].overflow,false);
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(output,"results.json"),JSON.stringify({snapshots,errors},null,2));
    console.log("FLOW browser: unified catalogue, preserved editors, template preview, keyboard focus, dark/light and 900px passed. "+output);
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
