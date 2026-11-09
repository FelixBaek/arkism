// Local product settings component with synthetic scoped HTTP replies; no external calls.
const fs=require("node:fs"),os=require("node:os"),path=require("node:path"),assert=require("node:assert/strict");
const shots=fs.mkdtempSync(path.join(os.tmpdir(),"arkism-memory-policy-")); // a private screenshot folder, not a fixed shared /tmp name
const {chromium}=require(process.env.PLAYWRIGHT_CORE_PATH||path.resolve(__dirname,"../frontend/node_modules/playwright-core"));
(async()=>{const browser=await chromium.launch({headless:true,...(process.env.CHROMIUM_PATH?{executablePath:process.env.CHROMIUM_PATH}:{})});try{
 const page=await browser.newPage({viewport:{width:1000,height:900},locale:"ko-KR"});await page.route("**/*",r=>r.abort());const errors=[];page.on("pageerror",e=>errors.push(e.message));
 await page.setContent('<html lang="ko"><body><main id="settings" style="max-width:760px;margin:auto;padding:20px"></main></body></html>');
 for(const f of ["tokens.css","app.css"])await page.addStyleTag({content:fs.readFileSync(path.resolve(__dirname,"../ui",f),"utf8")});
 for(const f of ["keep.js","space-settings.js"])await page.addScriptTag({path:path.resolve(__dirname,"../ui",f)});
 await page.evaluate(()=>{
  const el=(tag,cls="",text="")=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n};
  const button=(text,cls,icon,run)=>{const n=el("button","btn "+cls,text);n.type="button";if(run)n.addEventListener("click",run);return n};
  const group=t=>{const n=el("section","section");n.append(el("h2","",t));return n};
  const kit={side(){},sideSec:()=>el("div"),kvRow:()=>el("div"),sideNote:t=>el("p","meta",t),group,line:(title,hint,ctl)=>{const n=el("div","section");n.append(el("p","",title),el("p","meta",hint),ctl);return n},more:(title,...nodes)=>{const n=el("details");n.append(el("summary","",title),...nodes);return n},spaceJob:async()=>({}),sw:(on,label,set)=>{const n=button(label,"ghost small",null,()=>set(!on));n.setAttribute("role","switch");n.setAttribute("aria-checked",String(on));return n}};
  const state={view:"settings",settingsTab:"space",server:"local",scope:{workspace_id:"work",project_id:"project"},caps:{actions:{}}};
  let stored={recall_quality:{diversity:false}};window.writes=[];
  const request=async(p,o)=>{if(o?.method){const body=JSON.parse(o.body);window.writes.push({path:p,method:o.method,body});stored={...stored,...body};return {}};return {revision:2,config:structuredClone(stored),sources:{recall_quality:"space"}}};
  let view;const render=()=>{document.querySelector("#settings").replaceChildren(...view.tab(kit))};view=window.ArkismSpaceSettings({state,el,button,icon:()=>el("span"),request,notice:m=>window.notice=m,render});render();
 });
 const toggle=page.getByRole("switch",{name:"최근 작업 평가로 순위 조정",exact:true});await toggle.waitFor();assert.equal(await toggle.getAttribute("aria-checked"),"false");await toggle.focus();await page.keyboard.press("Space");
 await page.getByText("평가 순위 정책",{exact:true}).click();await page.getByLabel("평가 기간 (일)",{exact:true}).fill("60");await page.getByLabel("평가 기간 (일)",{exact:true}).press("Tab");
 // Redraw closes disclosure; use keyboard to reopen and select the dimension.
 await page.getByText("평가 순위 정책",{exact:true}).click();await page.getByLabel("평가 종류",{exact:true}).selectOption("personal_compliance");
 await page.getByRole("button",{name:"저장",exact:true}).click();await page.waitForFunction(()=>window.writes.length===1);
 assert.deepEqual(await page.evaluate(()=>window.writes[0].body.recall_quality),{diversity:false,signal_policy:{dimension:"personal_compliance",window_days:60,min_samples:3}});
 await toggle.focus();await page.keyboard.press("Space");await page.getByRole("button",{name:"저장",exact:true}).click();await page.waitForFunction(()=>window.writes.length===2);
 assert.deepEqual(await page.evaluate(()=>window.writes[1].body.recall_quality.signal_policy),{disabled:true});
 await toggle.focus();await page.keyboard.press("Space");await page.getByText("평가 순위 정책",{exact:true}).click();
 const panel=page.locator("section").filter({has:page.getByRole("switch",{name:"최근 작업 평가로 순위 조정",exact:true})}).first();await panel.screenshot({path:path.join(shots,"settings.png")});
 assert.deepEqual(errors,[]);console.log("PASS: explicit policy toggle, dimension/window change, sibling preservation, scoped save and explicit disable by keyboard");
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exitCode=1});
