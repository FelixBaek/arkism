// real UI renders canonical context as data.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFileSync, mkdtempSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {resolve, extname, join} from 'node:path';
import {createRequire} from 'node:module';
const playwright = createRequire(resolve(process.env.PLAYWRIGHT_FROM || '.', 'package.json'))('playwright-core');
const root = resolve(import.meta.dirname, '../desktop/ui');
const scope={workspace_id:'workspace_test',project_id:'test-project',user_id:'management:owner',agent_id:''};
const memory={id:'context-memory',revision:1,scope,status:'active',kind:'user',text:'상황에 맞는 정리 방식',created_at:'2026-10-14T00:00:00Z',sources:[],tags:[],memory_context:{domains:['personal','work'],applicability:'업무 발표는 간결하게, 개인 일기는 자세하게',evidence_kind:'user_statement',semantic_kind:'preference',observed_at:'2026-10-14T01:00:00Z',valid_from:'다음 발표부터',uncertainty:'적용할 상황을 다시 확인해야 해요.'}};
const legacy={...memory,id:'legacy-memory',text:'기존 기억',memory_context:undefined};
const candidate={...memory,id:'context-candidate',text:'후보 맥락',status:'conflicted',conflict_id:'context-conflict',memory_context:{...memory.memory_context,applicability:'<img src=x onerror="alert(1)">',evidence_kind:'inference'}};
const server=createServer((req,res)=>{try{const path=resolve(root,'.'+new URL(req.url,'http://localhost').pathname);if(!path.startsWith(root+'/')&&path!==root){throw new Error('outside the UI root');}const file=path===root?resolve(root,'index.html'):path;res.setHeader('content-type',({'.js':'text/javascript','.css':'text/css','.html':'text/html'})[extname(file)]||'application/octet-stream');res.end(readFileSync(file));}catch{// missing file or outside the UI root
res.statusCode=404;res.end();}});
try {
 await new Promise(r=>server.listen(0,'127.0.0.1',r));const url=`http://127.0.0.1:${server.address().port}`;
 const browser=await playwright.chromium.launch({headless:true});const page=await browser.newPage({locale:'ko-KR',viewport:{width:1440,height:1000}});const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 try {
  await page.route(url+'/v1/**',async route=>{const p=new URL(route.request().url()).pathname;let value={items:[]};
   if(p.endsWith('/capabilities'))value={role:'human',principal_id:'synthetic',actions:{review_list:true,review_decide:true},collections:[]};
   else if(p==='/v1/memories')value={items:[memory,legacy]};
   else if(p==='/v1/memory_candidates')value={items:new URL(route.request().url()).searchParams.get('status')==='conflicted'?[candidate]:[]};
   else if(p==='/v1/memories/context-memory')value=memory;
   else if(p==='/v1/evaluations/summary')value={target:{collection:'memories',id:route.request().postDataJSON().target.id,revision:1},groups:{},tracking:{status:'pending'}};
   await route.fulfill({json:value});
  });
  await page.route(url+'/readyz',r=>r.fulfill({json:{status:'ready'}}));
  await page.addInitScript((sc)=>{const answers={app_defaults:()=>({local:false}),ssh_connect:()=>({server:location.origin,token:'synthetic-token',workspace_id:sc.workspace_id,project_id:sc.project_id,ssh_host:'verify'}),api:async a=>{const r=await fetch(a.base+a.path,{method:a.method,body:a.body,headers:{Authorization:'Bearer '+a.token,'Content-Type':'application/json'}});return {status:r.status,body:await r.text()};}};localStorage.setItem('arkism.mode','remote');localStorage.setItem('arkism.ssh',JSON.stringify({host:'verify'}));window.__TAURI__={core:{invoke:async(cmd,a)=>answers[cmd]?.(a)??null}};localStorage.setItem('arkism.token','synthetic-token');localStorage.setItem('arkism.server',location.origin);},scope);
  await page.goto(url);await page.locator('.nav-item[data-menu="memory"]').click();
  await page.locator('#table [data-id="context-memory"]').click();
  const panel=page.locator('.memory-context-panel');await panel.getByText('개인 · 업무',{exact:true}).waitFor();
  const body=await panel.innerText();assert(body.includes('업무 발표는 간결하게'));assert(body.includes('사용자 진술'));assert(body.includes('다음 발표부터'));assert(body.includes('적용할 상황을 다시 확인'));
  const shots=mkdtempSync(join(tmpdir(),'arkism-memory-context-ui-'));
  for(const theme of ['dark','light']){await page.evaluate(t=>{document.documentElement.dataset.theme=t;},theme);await panel.screenshot({path:join(shots,`${theme}.png`)});}
  await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.screenshot({path:join(shots,'mobile.png')});
  await page.setViewportSize({width:1440,height:1000});await page.locator('#table [data-id="legacy-memory"]').click();assert.equal(await panel.count(),0);
  await page.getByRole('button',{name:'하나씩 확인하기'}).click();await page.getByRole('tab',{name:/헷갈림/}).click();await page.locator('#table [data-id="context-candidate"]').click();await panel.getByText('개인 · 업무',{exact:true}).waitFor();assert((await panel.innerText()).includes('<img src=x'));assert.equal(await panel.locator('img').count(),0);assert((await panel.innerText()).includes('추론'));
  assert.deepEqual(errors,[]);console.log('PASS: canonical mixed context, candidate evidence, legacy omission, escaped data, dark/light/mobile; screenshots in '+shots);
 } finally {await browser.close();server.close();}
} catch(e) {console.error(e);server.close();process.exitCode=1;}
