// A captured correction waits for a person in the real UI, and "둘 다 지우기"
// rejects it and deletes the current memory at its post-review revision.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFileSync} from 'node:fs';
import {resolve, extname} from 'node:path';
import {createRequire} from 'node:module';
const playwright = createRequire(resolve(process.env.PLAYWRIGHT_FROM || '.', 'package.json'))('playwright-core');
const root = resolve(import.meta.dirname, '../desktop/ui');
const scope={workspace_id:'workspace_test',project_id:'test-project',user_id:'management:owner',agent_id:''};
let old={id:'old-memory',revision:2,scope,status:'active',kind:'user',text:'답변은 길게 쓴다',conflict_id:'fix-conflict',created_at:'2026-10-14T00:00:00Z',sources:[],tags:[]};
const fix={id:'fix-candidate',revision:3,scope,status:'conflicted',kind:'user',text:'답변은 짧게 쓴다',conflict_id:'fix-conflict',created_at:'2026-10-15T00:00:00Z',sources:[],tags:['capture','basis:explicit_user','review:pending_correction']};
const rel={id:'fix-conflict',revision:1,relations:[{collection:'memories',id:old.id,revision:old.revision},{collection:'memory_candidates',id:fix.id,revision:fix.revision}]};
const server=createServer((req,res)=>{try{const path=resolve(root,'.'+new URL(req.url,'http://localhost').pathname);if(!path.startsWith(root+'/')&&path!==root){throw new Error('outside the UI root');}const file=path===root?resolve(root,'index.html'):path;res.setHeader('content-type',({'.js':'text/javascript','.css':'text/css','.html':'text/html'})[extname(file)]||'application/octet-stream');res.end(readFileSync(file));}catch{// missing file or outside the UI root
res.statusCode=404;res.end();}});
try {
 await new Promise(r=>server.listen(0,'127.0.0.1',r));const url=`http://127.0.0.1:${server.address().port}`;
 const browser=await playwright.chromium.launch({headless:true});const page=await browser.newPage({locale:'ko-KR',viewport:{width:1440,height:1000}});const errors=[];page.on('pageerror',e=>errors.push(String(e)));page.on('dialog',d=>d.accept());
 const writes=[];let reviewed=false;
 try {
  await page.route(url+'/v1/**',async route=>{const r=route.request(),p=new URL(r.url()).pathname;let value={items:[]};
   if(r.method()!=='GET')writes.push({method:r.method(),path:p,body:r.postDataJSON()});
   if(p.endsWith('/capabilities'))value={role:'human',principal_id:'synthetic',actions:{review_list:true,review_decide:true},collections:[]};
   else if(p==='/v1/memories')value={items:[old]};
   else if(p==='/v1/memory_candidates')value={items:!reviewed&&new URL(r.url()).searchParams.get('status')==='conflicted'?[fix]:[]};
   else if(p==='/v1/memory_relations/fix-conflict')value=rel;
   else if(p==='/v1/memories/old-memory'&&r.method()==='GET')value=old;
   else if(p==='/v1/memory_candidates/fix-candidate/review'){reviewed=true;old={...old,revision:3,conflict_id:undefined};value={id:fix.id,status:'rejected'};}
   else if(p==='/v1/evaluations/summary')value={target:{collection:'memories',id:old.id,revision:old.revision},groups:{},tracking:{status:'pending'}};
   await route.fulfill({json:value});
  });
  await page.route(url+'/readyz',r=>r.fulfill({json:{status:'ready'}}));
  await page.addInitScript((sc)=>{const answers={app_defaults:()=>({local:false}),ssh_connect:()=>({server:location.origin,token:'synthetic-token',workspace_id:sc.workspace_id,project_id:sc.project_id,ssh_host:'verify'}),api:async a=>{const r=await fetch(a.base+a.path,{method:a.method,body:a.body,headers:{Authorization:'Bearer '+a.token,'Content-Type':'application/json'}});return {status:r.status,body:await r.text()};}};localStorage.setItem('arkism.mode','remote');localStorage.setItem('arkism.ssh',JSON.stringify({host:'verify'}));window.__TAURI__={core:{invoke:async(cmd,a)=>answers[cmd]?.(a)??null}};localStorage.setItem('arkism.token','synthetic-token');localStorage.setItem('arkism.server',location.origin);},scope);
  await page.goto(url);await page.locator('.nav-item[data-menu="memory"]').click();
  await page.getByRole('button',{name:'하나씩 확인하기'}).click();await page.getByRole('tab',{name:/헷갈림/}).click();
  const row=page.locator('#table [data-id="fix-candidate"]');assert((await row.innerText()).includes('정정 대기'));await row.click();
  const detail=page.locator('#detail');await detail.getByText('고친 내용',{exact:true}).waitFor();await detail.getByText('“답변은 길게 쓴다”').waitFor();
  assert((await detail.innerText()).includes('지금 기억을 그대로 써요'));
  for(const name of ['새 내용으로 바꾸기','지금 기억 유지','둘 다 지우기'])assert.equal(await detail.getByRole('button',{name}).count(),1,name);
  assert.equal(await detail.getByRole('button',{name:'둘 다 맞아요'}).count(),0);
  await detail.getByRole('button',{name:'둘 다 지우기'}).click();
  await page.waitForFunction(()=>document.body.innerText.includes('모두 지웠어요'));
  const [review,del]=writes;
  assert.equal(review.path,'/v1/memory_candidates/fix-candidate/review');assert.equal(review.body.action,'reject');assert.equal(review.body.expected_revision,3);
  assert.equal(review.body.conflict.resolution,'keep_old');assert.deepEqual(review.body.conflict.memory,{collection:'memories',id:'old-memory',revision:2});
  assert.equal(del.method,'DELETE');assert.equal(del.path,'/v1/memories/old-memory');assert.equal(del.body.expected_revision,3);
  assert.equal(writes.length,2);assert.deepEqual(errors,[]);
  console.log('PASS: correction waits for a person, old memory shown, drop-both rejects then deletes at the new revision');
 } finally {await browser.close();server.close();}
} catch(e) {console.error(e);server.close();process.exitCode=1;}
