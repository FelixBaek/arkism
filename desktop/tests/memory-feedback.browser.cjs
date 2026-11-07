// Actual evaluation component DOM, keyboard interaction and bounded synthetic API.
const fs = require("node:fs"), path = require("node:path"), assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || path.resolve(__dirname,"../frontend/node_modules/playwright-core"));
(async () => {
 const browser = await chromium.launch({headless:true,...(process.env.CHROMIUM_PATH?{executablePath:process.env.CHROMIUM_PATH}:{})});
 try {
  const page=await browser.newPage({viewport:{width:980,height:900},locale:"ko-KR"});
  await page.route("**/*",r=>r.abort());
  const errors=[];page.on("pageerror",e=>errors.push(e.message));
  await page.setContent('<html lang="ko"><body><main id="panel" style="max-width:720px;margin:32px auto;padding:24px"></main></body></html>');
  for(const file of ["tokens.css","app.css","v4-memory.css"])await page.addStyleTag({content:fs.readFileSync(path.resolve(__dirname,"../ui",file),"utf8")});
  for(const file of ["keep.js","memory-evaluation.js"])await page.addScriptTag({path:path.resolve(__dirname,"../ui",file)});
  await page.evaluate(()=>{
   window.calls=[];window.failHistory=true;window.failPolicy=true;window.fixed=0;
   const el=(tag,cls="",text="")=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n};
   const section=title=>{const n=el("section","section");n.append(el("h3","",title));return n};
   const facts=pairs=>{const n=el("dl","facts");for(const [k,v]of pairs)n.append(el("dt","",k),el("dd","",v));return n};
   window.ArkismMemoryEvaluation.mount({container:document.querySelector("#panel"),record:{id:"memory",revision:2},scope:{workspace_id:"work",project_id:"project"},el,section,facts,onFix:()=>window.fixed++,request:async(path,opts)=>{
    window.calls.push(path);
    if(path.includes("evaluations/summary"))return {authority:"data_only_untrusted",target:{collection:"memories",id:"memory",revision:2},groups:{automated_check:{count:1}},passed:false};
    if(path.includes("signal-policy")){if(window.failPolicy){window.failPolicy=false;throw new Error("503")};return {authority:"data_only_untrusted",memory:{id:"memory",revision:2},mode:"recent_first_trace",as_of:"2026-11-07T10:00:00Z",policy:{dimension:"work_task",window_days:30,min_samples:3},historical_total:8,window_events:4,eligible_total:3,eligible_passed:2,excluded_window_events:1,minimum_met:true,evaluation_factor:1.1,work_factor:1,effective_factor:1.1}}
    if(path.includes("execution_traces"))return {id:"trace",revision:1,scope:{workspace_id:"work",project_id:"project"},text:"검사에서 잘못된 파일 경로를 확인했습니다. <b>원문</b>"};
    if(window.failHistory){window.failHistory=false;throw new Error("503")}
    const second=path.includes("cursor=next");
    return {authority:"data_only_untrusted",memory:{collection:"memories",id:"memory",revision:2},items:[{memory:{collection:"memories",id:"memory",revision:second?2:1},trace:{collection:"execution_traces",id:"trace",revision:1},dimension:"work_task",passed:second,judged_at:"2026-11-01T10:00:00Z"}],next_cursor:second?"":"next"};
   }});
  });
  await page.getByText("관측에서 실패",{exact:true}).waitFor();
  await page.getByRole("button",{name:"현재 순위 기준 보기"}).focus();await page.keyboard.press("Enter");
  await page.getByRole("button",{name:"순위 기준 다시 시도"}).click();
  await page.getByText("3건 · 통과 2건",{exact:true}).waitFor();
  await page.getByRole("button",{name:"실제 평가 이력 보기"}).focus();await page.keyboard.press("Enter");
  await page.getByText("평가 이력을 불러오지 못했어요.",{exact:true}).waitFor();
  await page.getByRole("button",{name:"다시 시도",exact:true}).click();
  await page.getByText("실패 · 업무 결과 · 버전 1 (과거)",{exact:true}).waitFor();
  await page.getByRole("button",{name:"평가 더 보기"}).click();
  await page.getByText("통과 · 업무 결과 · 버전 2 (현재)",{exact:true}).waitFor();
  // An earlier page's trace button must still work after pagination.
  await page.getByRole("button",{name:"작업 근거 보기"}).first().click();
  await page.getByText("검사에서 잘못된 파일 경로를 확인했습니다. <b>원문</b>",{exact:true}).waitFor();
  assert.equal(await page.locator("#panel b").count(),0);
  await page.getByRole("button",{name:"기억 내용 고치기"}).focus();await page.keyboard.press("Enter");
  assert.equal(await page.evaluate(()=>window.fixed),1);
  assert.deepEqual(errors,[]);
  assert.ok(await page.evaluate(()=>window.calls.filter(p=>!p.includes("evaluations/summary")).every(p=>p.includes("workspace_id=work")&&p.includes("project_id=project"))));
  await page.screenshot({path:process.env.ARKISM_FEEDBACK_SCREENSHOT||"/tmp/arkism-memory-feedback.png",fullPage:true});
  await page.setViewportSize({width:390,height:844});
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  for(const file of ["memory-pages.js","memory-insight.js"])await page.addScriptTag({path:path.resolve(__dirname,"../ui",file)});
  await page.evaluate(()=>{
   window.mountRules=(source_state)=>{
    document.querySelector("#panel").replaceChildren();window.ruleWrites=[];
    const el=(tag,cls="",text="")=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n};
    const $=s=>document.querySelector(s);
    for(const id of ["app","chips","stats","table","detail"]){const n=el("div");n.id=id;$("#panel").append(n)}
    const state={view:"memories",memTab:"rules",server:"local",token:"synthetic",scope:{workspace_id:"work",project_id:"project"},caps:{role:"human"}};
    let current={id:"rule",revision:3,scope:state.scope,title:"배포 규칙",rule:"배포 전에 검증한다.",applies_to:"both",priority:80,active:source_state==="active",source_state,tags:[]};
    const button=(label,cls,unused,run)=>{const b=el("button","btn "+cls,label);b.type="button";if(run)b.addEventListener("click",run);return b};
    const section=title=>{const n=el("section","section");n.append(el("h3","",title));return n};
    const facts=pairs=>{const n=el("dl","facts");for(const[k,v]of pairs)n.append(el("dt","",k),el("dd","",v||""));return n};
    const view=window.ArkismMemoryInsight({state,$,el,raw:n=>n,icon:()=>el("span"),button,section,facts,query:(extra={})=>new URLSearchParams({...state.scope,...extra}),detailShell:()=>{$("#detail").replaceChildren();return $("#detail")},openDetail:()=>{},notice:m=>window.ruleNotice=m,request:async(p,o)=>{
     if(p.startsWith("/v1/directives?"))return {items:[current],next_cursor:""};
     const body=JSON.parse(o.body);window.ruleWrites.push({path:p,body});current={...current,...body,revision:4,source_state:undefined};return current;
    }},"rules");view.draw();
   };
  });
  for(const [state,label]of [["needs_review","원본 재검토 필요"],["inactive","원본 비활성"],["unavailable","원본 조회 불가"]]){
   await page.evaluate(s=>window.mountRules(s),state);
   await page.locator("#table").getByText(label,{exact:true}).waitFor();
   await page.locator("#table .original-row").focus();await page.keyboard.press("Enter");
   await page.getByText(/다시 승인하면 원본과 독립된 수동 규칙/).waitFor();
   if(state==="needs_review")await page.screenshot({path:"/tmp/arkism-rule-review.png",fullPage:true});
   await page.getByRole("button",{name:"수동 규칙으로 다시 승인",exact:true}).focus();await page.keyboard.press("Enter");
   await page.getByText("규칙 · 켜짐",{exact:true}).waitFor();
   assert.deepEqual(await page.evaluate(()=>window.ruleWrites.map(w=>[w.path,w.body.scope,w.body.expected_revision,w.body.active,w.body.source_state||null])),[["/v1/directives/rule",{workspace_id:"work",project_id:"project"},3,true,null]]);
   assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  }
  await page.evaluate(()=>window.mountRules("active"));
  await page.locator("#table .original-row").waitFor();
  await page.locator("#table .original-row").focus();await page.keyboard.press("Enter");
  await page.getByText(/수정하거나 끄면 독립된 수동 규칙으로 바뀌어요/).waitFor();
  await page.screenshot({path:"/tmp/arkism-rule-active-link.png",fullPage:true});
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  await page.getByRole("button",{name:"끄기",exact:true}).focus();await page.keyboard.press("Enter");
  await page.getByText("규칙 · 꺼짐",{exact:true}).waitFor();
  assert.deepEqual(await page.evaluate(()=>window.ruleWrites.map(w=>[w.body.active,w.body.expected_revision])),[[false,3]]);
  assert.deepEqual(errors,[]);
  console.log("PASS: evaluation retry/pagination/trace/scoped requests/safe text/keyboard correction; all suspended rule states/manual approval and active-linked disable disclosure");
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
