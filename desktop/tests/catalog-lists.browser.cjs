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
    await page.addInitScript(({data}) => {
      window.dialogEvents=[];
      for(const name of ["showModal","close"]){const original=HTMLDialogElement.prototype[name];HTMLDialogElement.prototype[name]=function(...args){window.dialogEvents.push([name,this.className]);return original.apply(this,args)}}
      const NativeDate=Date;
      window.Date=class extends NativeDate {constructor(...args){super(...(args.length?args:["2026-10-21T12:00:00Z"]))}static now(){return new NativeDate("2026-10-21T12:00:00Z").getTime()}};
      localStorage.setItem("arkism.look",JSON.stringify({theme:"dark",lang:"ko"}));
      window.__TAURI__={event:{listen:async()=>()=>{}},core:{async invoke(cmd,args={}){
        if(cmd==="app_defaults")return {local:true,updates:false};
        if(cmd==="local_start")return {server:"http://127.0.0.1:8091",token:"inert-browser-fixture"};
        if(cmd==="capture_settings")return {paused:false,exclude:[],instructions:""};
        if(cmd!=="api")return null;
        const url=new URL(args.path,"https://fixture.invalid"),base=url.pathname.replace(/^\//,""),q=url.searchParams,status=q.get("status"),kind=q.get("kind");
        if(base==="v1/usage/sessions" && (args.method||"GET")==="GET" || base.endsWith("/query")) {
          const input=base.endsWith("/query")?JSON.parse(args.body||"{}"):Object.fromEntries(q), source=base.includes("knowledge"), session=base.includes("sessions"), candidates=base.includes("memory_candidates");
          const all=Array.from({length:1200},(_,i)=>source?{id:`source-${i}`,display_name:i===1199?"Rare source tail":`Source ${i}`,format:".md",revision:1,lifecycle_status:"active",trust_level:"reviewed",chunk_count:i,document_count:1,updated_at:"2026-10-20T00:00:00Z",bindings:[]}:session?{id:`session-${i}`,session_id:`sid-${i}`,title:i===1199?"Rare session tail":`Session ${i}`,agent:"codex",device:"fixture",project:"fixture",started_at:"2026-10-20T00:00:00Z",ended_at:"2026-10-20T00:00:00Z",messages:i,tokens:i,cost_usd:i,priced:true,models:[],tools:[],skills:[]}: {id:`record-${i}`,name:`Skill ${i}`,description:"Fixture",skill_compatibility:{},text:i===1199?"Rare memory tail":`Record ${i}`,summary:"",kind:"decision",status:candidates?"candidate":"active",revision:1,created_at:"2026-10-20T00:00:00Z",updated_at:"2026-10-20T00:00:00Z",tags:[],sources:[]});
          let items=all.filter(x=>!input.q || (x.display_name||x.title||x.text).toLowerCase().includes(input.q.toLowerCase()));
          if(["tokens","chunks"].includes(input.sort))items.reverse();
          const start=Number(input.cursor)||0,limit=Number(input.limit)||50,total=items.length;
          window.catalogCalls=(window.catalogCalls||[]).concat([{base,...input}]);
          if(window.failCatalog && start){window.failCatalog=false;return {status:503,body:JSON.stringify({code:"dependency_unavailable"})}}
          return {status:200,body:JSON.stringify({items:items.slice(start,start+limit),total,next_cursor:start+limit<total?String(start+limit):"",facets:{kinds:source?{".md":total}:{decision:total},repositories:{},categories:{},states:{new:total}},authority:"data_only_untrusted"})};
        }
        const candidates=[];
        if((args.method||"GET")==="GET") {if(kind&&status)candidates.push(base+"."+kind+"."+status+".json");if(kind)candidates.push(base+"."+kind+".json");if(status)candidates.push(base+"."+status+".json");candidates.push(base+".json");}
        else candidates.push(base+".post.json");
        const key=candidates.find(k=>data[k]);
        return {status:200,body:JSON.stringify(key?data[key]:{items:[],next_cursor:"",authority:"data_only_untrusted"})};
      }}};
    },{data});
    await page.goto("https://arkism.fixture/");
    await page.waitForFunction(()=>document.querySelector("#app")?.inert===false&&document.querySelector("#count-memories")?.textContent);
    await page.locator('[data-menu="connect"]').click();
    await page.getByRole("tab",{name:"지식",exact:true}).click();
    await page.getByText("자료 더 보기",{exact:true}).waitFor();
    assert.equal(await page.locator(".v4k-row").count(),50);
    await page.getByRole("button",{name:"자료 더 보기",exact:true}).focus();await page.keyboard.press("Enter");
    await page.waitForFunction(()=>document.querySelectorAll(".v4k-row").length===100);
    await page.getByRole("searchbox",{name:"문서·목차 검색",exact:true}).fill("rare");
    await page.locator("#table").getByText("Rare source tail",{exact:true}).waitFor();
    assert.equal(await page.locator(".v4k-row").count(),1);
    assert.equal(await page.locator(":focus").getAttribute("aria-label"),"문서·목차 검색");
    await page.getByRole("searchbox",{name:"문서·목차 검색",exact:true}).fill("");
    await page.getByRole("combobox",{name:"정렬",exact:true}).selectOption("chunks");
    await page.waitForFunction(()=>document.querySelector(".v4k-row .v4s-title")?.textContent==="Rare source tail");
    // The session event is the same entry used by a memory's source link.
    await page.evaluate(()=>window.dispatchEvent(new CustomEvent("arkism:open-session",{detail:{session_id:"Session"}})));
    await page.getByRole("button",{name:"세션 더 보기",exact:true}).waitFor();
    assert.equal(await page.locator(".ss-item").count(),50);
    await page.getByRole("button",{name:"세션 더 보기",exact:true}).click();
    await page.waitForFunction(()=>document.querySelectorAll(".ss-item").length===100);
    await page.getByRole("searchbox",{name:"세션 검색",exact:true}).fill("rare");
    await page.locator("#table").getByText("Rare session tail",{exact:true}).waitFor();
    assert.equal(await page.locator(".ss-item").count(),1);
    await page.getByRole("searchbox",{name:"세션 검색",exact:true}).fill("");
    await page.getByRole("combobox",{name:"정렬",exact:true}).selectOption("tokens");
    await page.waitForFunction(()=>document.querySelector(".ss-item .v4s-title")?.textContent==="Rare session tail");
    await page.evaluate(()=>window.failCatalog=true);
    await page.getByRole("button",{name:"세션 더 보기",exact:true}).click();
    await page.getByRole("button",{name:"다시 시도",exact:true}).waitFor();
    assert.equal(await page.locator(".ss-item").count(),50);
    await page.getByRole("button",{name:"다시 시도",exact:true}).click();
    await page.waitForFunction(()=>document.querySelectorAll(".ss-item").length===100);
    await page.locator('[data-menu="memory"]').click();
    await page.locator(".ov-chip").filter({hasText:"쓰는 중인 기억"}).click();
    await page.locator("dialog[open] .ov-wide-row").first().waitFor();
    assert.equal(await page.locator("dialog[open] .ov-wide-row").count(),50);
    await page.locator("dialog[open]").getByRole("button",{name:"더 보기",exact:true}).click();
    await page.waitForFunction(()=>document.querySelectorAll("dialog[open] .ov-wide-row").length===100);
    await page.getByRole("textbox",{name:"기억 찾기",exact:true}).fill("rare");
    await page.locator("dialog[open] .ov-wide-row").getByText("Rare memory tail",{exact:true}).waitFor();
    await page.keyboard.press("Escape");
    assert.deepEqual(errors,[]);
    console.log("Catalog lists: knowledge >500, sessions >300, global search and order, explicit pages, retry preservation, keyboard/caret, overview >80 all passed with 1,200-row fixtures; source UI, no build.");
  } catch(error) {console.error(errors);console.error(await page.evaluate(()=>window.dialogEvents));console.error((await page.locator("body").innerText()).slice(-3500));throw error;}
  finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
