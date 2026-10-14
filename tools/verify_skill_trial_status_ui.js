// real Chromium, read-only trial status and stored model labels.
const assert=require('node:assert/strict');
const {resolve}=require('node:path');
const {readFileSync,mkdirSync}=require('node:fs');
const {createRequire}=require('node:module');
const playwright=createRequire(resolve(process.env.PLAYWRIGHT_FROM||'.','package.json'))('playwright-core');
const root=resolve(__dirname,'../desktop/ui');
(async()=>{
 const browser=await playwright.chromium.launch({headless:true});const page=await browser.newPage({viewport:{width:430,height:900}});const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 try{
  await page.route('http://localhost/',route=>route.fulfill({contentType:'text/html',body:'<html data-theme="dark"><body><main id="mount" style="width:392px;max-width:100%"></main></body></html>'}));
  await page.goto('http://localhost/');
  for(const file of ['tokens.css','app.css'])await page.addStyleTag({content:readFileSync(resolve(root,file),'utf8')});
  for(const file of ['skill-trial-status.js','model-generation.js'])await page.addScriptTag({content:readFileSync(resolve(root,file),'utf8')});
  await page.evaluate(()=>{
   window.reads=[];window.current='active';window.denied=false;
   const scope={workspace_id:'ws',project_id:'project',user_id:'owner',agent_id:''};
   window.testState={server:'local',token:'synthetic',scope,view:'skills',selected:'skill:check'};
   const el=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n};
   const button=(text,cls,icon,fn)=>{const b=el('button','btn '+cls,text);if(fn)b.addEventListener('click',fn);return b};
   const request=async(path,options)=>{window.reads.push({path,body:JSON.parse(options.body)});if(window.denied)throw Error('access_denied');return {authority:'data_only_untrusted',scope:window.badScope?{...scope,project_id:'other'}:scope,skill_id:'check',trial:{collection:'skill_trials',id:'trial-1',revision:1},candidate:{collection:'skill_versions',id:'version-1',revision:2},state:window.current,observation_state:'not_available',scheduler:{status:window.current==='recovery_pending'?'failed':'retry',attempts:window.current==='recovery_pending'?8:0}}};
   window.mountTrial=()=>document.getElementById('mount').replaceChildren(window.ArkismSkillTrialStatus({state:window.testState,el,button,request})('check','trial-1','version-1'));
   window.mountModel=()=>document.getElementById('mount').replaceChildren(window.ArkismModelGeneration({el})({generator:{model:'claude-haiku-5-5',prompt_version:'harness-session-v2',pipeline_version:'session-state-v1'}}));
   window.mountTrial();
  });
  const panel=page.locator('.skill-trial-status');await panel.getByText('시험 진행 중',{exact:true}).waitFor();
  for(const [state,label] of [['stopped_regression','회귀 관측으로 시험 중단'],['expired','시험 기간 종료'],['promoted','기본 버전으로 적용됨'],['recovery_pending','회귀 버전 차단 · 복구 미완료'],['rolled_back','기준판으로 복구됨']]){
   await page.evaluate(s=>window.current=s,state);await panel.getByRole('button',{name:'처리 상태 다시 조회'}).click();await panel.getByText(label,{exact:true}).waitFor();assert((await panel.innerText()).includes('독립 평가 결과는 이 조회에서 확인되지'));
  }
  await page.evaluate(()=>window.current='recovery_pending');await panel.getByRole('button').click();await panel.getByText('회귀 버전 차단 · 복구 미완료',{exact:true}).waitFor();
  mkdirSync('/tmp/arkism-skill-status-ui',{recursive:true});await panel.screenshot({path:'/tmp/arkism-skill-status-ui/recovery-dark.png'});
  await page.evaluate(()=>document.documentElement.setAttribute('data-theme','light'));await panel.screenshot({path:'/tmp/arkism-skill-status-ui/recovery-light.png'});
  await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  await page.evaluate(()=>window.denied=true);await panel.getByRole('button').click();await panel.getByText('처리 상태를 확인하지 못했어요',{exact:true}).waitFor();assert(!(await panel.innerText()).includes('복구됨'));
  await page.evaluate(()=>{window.denied=false;window.badScope=true});await panel.getByRole('button').click();await panel.getByText('처리 상태를 확인하지 못했어요',{exact:true}).waitFor();
  const reads=await page.evaluate(()=>window.reads);assert(reads.length>6);assert(reads.every(r=>r.path==='/v1/skill-trials/trial-1/status'&&r.body.skill_id==='check'));
  await page.evaluate(()=>window.mountModel());await page.getByText('claude-haiku-5-5',{exact:true}).waitFor();assert((await page.locator('.model-generation').innerText()).includes('harness-session-v2'));await page.locator('.model-generation').screenshot({path:'/tmp/arkism-skill-status-ui/model-generation.png'});
  await page.addScriptTag({content:readFileSync(resolve(root,'skill-trial.js'),'utf8')});
  await page.evaluate(()=>{
   const el=(tag,cls='',text='')=>{const n=document.createElement(tag);n.className=cls;n.textContent=text;return n};
   const button=(text,cls,icon,fn)=>{const b=el('button','btn '+cls,text);if(fn)b.addEventListener('click',fn);return b};
   window.integrationReads=[];window.testState.caps={role:'agent'};const scope=window.testState.scope,id='trial-'+ 'a'.repeat(64);
   const record={id,scope,skill_id:'check',revision:1,status:'active',created_at:new Date().toISOString(),skill_trial:{candidate:{collection:'skill_versions',id:'version-1',revision:1},runtime:'codex',ends_at:new Date(Date.now()+3600000).toISOString()}};
   const view={record,status:'active',resolved_version_id:'version-1',resolved_version:1};
   const request=async(path)=>{window.integrationReads.push(path);if(path.endsWith('/list'))return {authority:'data_only_untrusted',items:[view]};if(path.endsWith('/inspect'))return view;if(path.endsWith('/status'))return {authority:'data_only_untrusted',scope,skill_id:'check',trial:{collection:'skill_trials',id,revision:1},candidate:{collection:'skill_versions',id:'version-1',revision:1},state:'active',observation_state:'not_available'};throw Error('unexpected mutation')};
   const panel=window.ArkismSkillTrial({state:window.testState,el,button,request,query:()=>''})('check','version-1');document.getElementById('mount').replaceChildren(panel);panel.open=true;
  });
  await page.getByRole('button',{name:/Codex ·/}).click();await page.locator('.skill-trial .skill-trial-status').getByText('시험 진행 중',{exact:true}).waitFor();
  assert((await page.evaluate(()=>window.integrationReads)).every(p=>/\/(list|inspect|status)$/.test(p)));
  assert.deepEqual(errors,[]);console.log('PASS: canonical lifecycle/recovery, unavailable evidence, read-only full trial integration, denied/foreign response, responsive themes, persisted model provenance');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
