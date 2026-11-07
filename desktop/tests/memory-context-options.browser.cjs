// Production search component with synthetic scoped HTTP, no model or external traffic.
const fs=require("node:fs"),path=require("node:path"),assert=require("node:assert/strict");
const {chromium}=require(process.env.PLAYWRIGHT_CORE_PATH||path.resolve(__dirname,"../frontend/node_modules/playwright-core"));
(async()=>{const browser=await chromium.launch({headless:true,...(process.env.CHROMIUM_PATH?{executablePath:process.env.CHROMIUM_PATH}:{})});try{
 const page=await browser.newPage({viewport:{width:1000,height:900},locale:"ko-KR"});await page.route("**/*",r=>r.abort());const errors=[];page.on("pageerror",e=>errors.push(e.message));
 await page.setContent('<html lang="ko"><body><main style="max-width:780px;margin:auto;padding:24px"><div id="app"></div><div id="chips"></div><div id="stats"></div><div id="table"></div><div id="detail"></div></main></body></html>');
 for(const f of ["tokens.css","app.css","memory-pages.css","v4-memory.css"])await page.addStyleTag({content:fs.readFileSync(path.resolve(__dirname,"../ui",f),"utf8")});
 for(const f of ["keep.js","memory-pages.js","digests.js","memory-time.js","memory-insight.js"])await page.addScriptTag({path:path.resolve(__dirname,"../ui",f)});
 await page.evaluate(()=>{
  const el=(tag,cls="",text="")=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n};
  const button=(text,cls,unused,run)=>{const n=el("button","btn "+cls,text);n.type="button";if(run)n.addEventListener("click",run);return n};
  const section=t=>{const n=el("section","section");n.append(el("h3","",t));return n};
  const facts=pairs=>{const n=el("dl","facts");for(const[k,v]of pairs)if(v)n.append(el("dt","",k),el("dd","",v));return n};
  const $=s=>document.querySelector(s),state={view:"memories",memTab:"search",server:"local",token:"synthetic",scope:{workspace_id:"work",project_id:"project"},caps:{role:"human"}};
  window.calls=[];window.packet=JSON.stringify({authority:"data_only_untrusted",items:[],conversation:[{text:"<b>회의는 위층</b>",authority:"data_only_untrusted"}]});
  const view=window.ArkismMemoryInsight({state,$,el,raw:n=>n,icon:()=>el("span"),button,section,facts,callout:(a,b)=>el("p","callout",a+b),KINDS:{decision:["결정"]},kindLabel:k=>k,query:()=>new URLSearchParams(state.scope),detailShell:()=>$("#detail"),openDetail:()=>{},notice:m=>window.notice=m,request:async(p,o)=>{window.calls.push({path:p,body:JSON.parse(o.body)});return {items:[],context_data:window.packet,tokens_used:45,token_encoding:"cl100k_base",degraded:["memory_vector_unavailable"],explain:{lanes:["keyword"]}}}},"search");view.draw();
 });
 await page.getByRole("searchbox",{name:"검색어"}).fill("회의실");await page.locator("#chips summary").focus();await page.keyboard.press("Enter");
 for(const name of ["원본 대화도 같은 토큰 한도에 넣기","내용을 자르지 않고 들어가는 기억만 담기","일부 검색 방법이 실패해도 나머지 결과 보기"]){const c=page.getByRole("checkbox",{name,exact:true});assert.equal(await c.isChecked(),false);await c.focus();await page.keyboard.press("Space")}
 await page.getByRole("button",{name:"찾아보기",exact:true}).click();assert.equal(await page.evaluate(()=>window.calls.length),0);assert.match(await page.evaluate(()=>window.notice),/토큰 한도/);
 await page.getByLabel("결과 전체 토큰 한도 (1~64000)",{exact:true}).fill("3000");await page.getByRole("button",{name:"찾아보기",exact:true}).focus();await page.keyboard.press("Enter");await page.getByText("최종 문맥 미리보기",{exact:true}).waitFor();
 const sent=await page.evaluate(()=>window.calls[0]);assert.equal(sent.path,"/v1/search");assert.deepEqual(sent.body.scope,{workspace_id:"work",project_id:"project"});for(const k of ["conversation_context","whole_items","best_effort_lanes"])assert.equal(sent.body[k],true);
 assert.equal(await page.locator("#table pre").first().textContent(),await page.evaluate(()=>window.packet));assert.equal(await page.locator("#table b").count(),0);
 await page.locator("#table").screenshot({path:"/tmp/arkism-memory-context-options.png"});await page.setViewportSize({width:390,height:844});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));assert.deepEqual(errors,[]);
 console.log("PASS: default-off options, keyboard toggles, explicit budget validation, scoped request, exact final data preview and no HTML execution");
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exitCode=1});
