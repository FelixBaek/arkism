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
        if(base.endsWith("/query")) {
          const inq=JSON.parse(args.body||"{}"), original=base.slice(0,-6), suffix=original==="v1/memory_candidates"?"."+(inq.state==="conflicted"?"conflicted":"candidate"):"";
          const source=data[original+suffix+".json"]||data[original+".json"]||{items:[]};
          const items=(source.items||[]).filter(x=>!inq.q||String(x.text||x.name||x.display_name||"").toLowerCase().includes(inq.q.toLowerCase()));
          return {status:200,body:JSON.stringify({...source,items,total:items.length,next_cursor:"",facets:{kinds:{},repositories:{},categories:{},states:{}},authority:"data_only_untrusted"})};
        }
        const candidates=[];
        if((args.method||"GET")==="GET") {if(kind&&status)candidates.push(base+"."+kind+"."+status+".json");if(kind)candidates.push(base+"."+kind+".json");if(status)candidates.push(base+"."+status+".json");candidates.push(base+".json");}
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
    await page.getByRole("button",{name:/^기억 보기/}).click();
    await page.locator("#table [data-id]").first().waitFor();
    await page.locator("#table [data-id]").first().focus();await page.keyboard.press("Enter");
    await page.locator("#detail h2").waitFor();await capture("memory");
    await page.locator('[data-menu="agent"]').click();
    await page.getByRole("tab",{name:/자동화/}).click();
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
    await page.getByRole("tab",{name:"모양",exact:true}).click();await capture("settings");
    // English loads only on demand; original user content survives both language changes.
    await page.getByRole("tab",{name:"English",exact:true}).click();
    await page.waitForFunction(()=>window.ArkismI18n.lang()==="en");
    assert.equal(requests.some(u=>/i18n-en/.test(u)),true);
    await page.getByRole("tab",{name:"한국어",exact:true}).click();
    await page.waitForFunction(()=>window.ArkismI18n.lang()==="ko");
    await page.locator('[data-menu="home"]').click();
    await page.getByRole("button",{name:/^기억 보기/}).click();
    assert.equal(await page.locator("#table").getByText("저장",{exact:true}).count()>0,true);
    await page.evaluate(async()=>{ await window.ArkismI18n.setLang("en"); });
    assert.equal(await page.locator("#table").getByText("저장",{exact:true}).count()>0,true,"English changed user text");
    await page.evaluate(async()=>{ await window.ArkismI18n.setLang("ko"); });
    assert.deepEqual(errors,[]);
    fs.writeFileSync(path.join(output,"screens.json"),JSON.stringify(snapshots,null,2));
    console.log("UI structure: 7 real screens, keyboard opening/Escape, lazy English, Korean round trip and user text passed");
  } catch(error) {console.error(errors);console.error(await page.evaluate(()=>window.dialogEvents));console.error((await page.locator("body").innerText()).slice(-3500));throw error;}
  finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
