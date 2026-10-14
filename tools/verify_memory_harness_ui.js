// real desktop DOM, synthetic scoped APIs, no credentials.
const assert = require('node:assert/strict');
const { createServer } = require('node:http');
const { readFileSync, mkdirSync } = require('node:fs');
const { resolve, extname } = require('node:path');
const { createRequire } = require('node:module');
const playwright = createRequire(resolve(process.env.PLAYWRIGHT_FROM || '.', 'package.json'))('playwright-core');
const root = resolve(__dirname, '../desktop/ui');
const scope = {workspace_id:'workspace_test',project_id:'test-project',user_id:'management:owner',agent_id:''};
const memory = {id:'memory1',revision:1,scope,status:'active',kind:'semantic',text:'기억 평가 테스트',created_at:'2026-10-14T00:00:00Z',sources:[],tags:[]};
const candidate = {...memory,id:'candidate1',text:'새 내용 테스트',status:'conflicted',conflict_id:'conflict1'};
let mode='pending', budgetDenied=false, budgetExceeded=false, budgetHard=false;
const writes=[];
const server=createServer((req,res)=>{try{const path=resolve(root,'.'+new URL(req.url,'http://localhost').pathname);if(!path.startsWith(root+'/')&&path!==root)throw Error();const file=path===root?resolve(root,'index.html'):path;res.setHeader('content-type',({'js':'text/javascript','css':'text/css','html':'text/html'})[extname(file).slice(1)]||'application/octet-stream');res.end(readFileSync(file));}catch(_){res.statusCode=404;res.end();}});
(async()=>{
  await new Promise(r=>server.listen(0,'127.0.0.1',r));
  const url=`http://127.0.0.1:${server.address().port}`;
  const browser=await playwright.chromium.launch({headless:true});
  const page=await browser.newPage({locale:'ko-KR',viewport:{width:1440,height:1000}});const errors=[];page.on('pageerror',e=>errors.push(String(e)));
  try{
    await page.route(url+'/v1/**',async route=>{
      const req=route.request(),p=new URL(req.url()).pathname;
      let value={items:[]};
      if(p.endsWith('/capabilities'))value={role:'human',principal_id:'synthetic',actions:{review_list:true,review_decide:true},collections:[]};
      else if(p==='/v1/memories')value={items:[memory]};
      else if(p==='/v1/memory_candidates')value={items:new URL(req.url()).searchParams.get('status')==='conflicted'?[candidate]:[]};
      else if(p==='/v1/memory_relations/conflict1')value={id:'conflict1',revision:1,relations:[{collection:'memories',id:'memory1',revision:1},{collection:'memory_candidates',id:'candidate1',revision:1}]};
      else if(p==='/v1/memories/memory1')value=memory;
      else if(p==='/v1/evaluations/summary'){
        assert.deepEqual(req.postDataJSON(),{scope,target:{collection:'memories',id:'memory1',revision:1}});
        if(mode==='error'){await route.fulfill({status:403,json:{code:'access_denied'}});return;}
        value={authority:'data_only_untrusted',target:{collection:'memories',id:'memory1',revision:mode==='stale'?2:1},tracking:{status:mode==='failed'?'done':'pending'},groups:mode==='self'?{model_self:{count:1}}:mode==='failed'?{automated_check:{count:1}}:{},passed:mode==='self'?true:mode==='failed'?false:null};
      }else if(p==='/v1/model-budget/status'){
        if(budgetDenied){await route.fulfill({status:403,json:{code:'access_denied'}});return;}
        value={month:'2026-10',monthly_limit_pico_usd:100e12,general_limit_pico_usd:90e12,daily_soft_target_pico_usd:3e12,daily_soft_target_exceeded:budgetExceeded,...(budgetHard?{daily_limit_pico_usd:5e12}:{}),monthly_committed_pico_usd:12e12,general_committed_pico_usd:10e12,daily_committed_pico_usd:budgetExceeded?4e12:2e12,outstanding_pico_usd:.2e12};
      }else if(p==='/v1/memory_candidates/candidate1/review'){
        writes.push(req.postDataJSON());await route.fulfill({status:503,json:{code:'dependency_unavailable'}});return;
      }
      await route.fulfill({json:value});
    });
    await page.route(url+'/readyz',r=>r.fulfill({json:{status:'ready'}}));
    await page.addInitScript((sc)=>{localStorage.setItem('arkism.mode','remote');localStorage.setItem('arkism.ssh',JSON.stringify({host:'verify'}));window.__TAURI__={core:{invoke:async(cmd,a)=>cmd==='app_defaults'?{local:false}:cmd==='ssh_connect'?{server:location.origin,token:'synthetic-token',workspace_id:sc.workspace_id,project_id:sc.project_id,ssh_host:'verify'}:cmd==='api'?fetch(a.base+a.path,{method:a.method,body:a.body,headers:{Authorization:'Bearer '+a.token,'Content-Type':'application/json'}}).then(async r=>({status:r.status,body:await r.text()})):null}};localStorage.setItem('arkism.token','synthetic-token');localStorage.setItem('arkism.server',location.origin);},scope);
    await page.goto(url);
    await page.locator('.nav-item[data-menu="memory"]').click();
    await page.locator('#table [data-id="memory1"]').click();
    const panel=page.locator('.memory-evaluation');
    await panel.getByText('관측 대기',{exact:true}).first().waitFor();
    assert(!(await panel.innerText()).includes('관측에서 통과'));
    for(const [next,expected] of [['self','모델 자기평가만 있음'],['failed','관측에서 실패'],['error','평가 결과를 확인하지 못했어요'],['stale','평가 결과를 확인하지 못했어요']]){
      mode=next;await panel.getByRole('button',{name:'평가 다시 조회'}).click();await page.waitForFunction(s=>document.querySelector('.memory-evaluation')?.textContent.includes(s),expected);
      if(next==='self')assert(!(await panel.innerText()).includes('관측에서 통과'));
    }
    mode='pending';await panel.getByRole('button',{name:'평가 다시 조회'}).click();await panel.getByText('관측 대기',{exact:true}).first().waitFor();
    mkdirSync('/tmp/arkism-harness-ui',{recursive:true});await page.screenshot({path:'/tmp/arkism-harness-ui/evaluation.png'});
    await page.evaluate(()=>document.documentElement.setAttribute('data-theme','dark'));await page.screenshot({path:'/tmp/arkism-harness-ui/evaluation-dark.png'});
    await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.screenshot({path:'/tmp/arkism-harness-ui/evaluation-mobile.png'});
    await page.setViewportSize({width:1440,height:1000});
    await page.getByRole('button',{name:'하나씩 확인하기'}).click();await page.getByRole('tab',{name:/헷갈림/}).click();
    for(let i=0;i<2;i++){
      await page.locator('#table [data-id="candidate1"]').click();await page.getByRole('button',{name:'새 내용으로 바꾸기',exact:true}).click();await page.waitForTimeout(150);
    }
    assert.equal(writes.length,2);assert.equal(writes[0].operation_id,writes[1].operation_id);assert.equal(writes[0].conflict.resolution,'replace_old');assert.equal(writes[0].expected_revision,1);
    await page.locator('.nav-item[data-menu="memory"]').click();await page.locator('.area-tab',{hasText:'사용량'}).click();await page.locator('.model-budget').getByText('$12.00 / $100.00',{exact:true}).waitFor();
    const budget=page.locator('.model-budget');assert((await budget.innerText()).includes('오늘 목표'));assert(!(await budget.innerText()).includes('오늘 상한'));
    budgetExceeded=true;await budget.getByRole('button',{name:'예산 다시 조회'}).click();await budget.getByText('오늘 목표를 넘었어요.',{exact:false}).waitFor();assert((await budget.innerText()).includes('$4.00 / $3.00'));
    budgetHard=true;await budget.getByRole('button',{name:'예산 다시 조회'}).click();await budget.getByText('오늘 상한',{exact:true}).waitFor();
    await page.screenshot({path:'/tmp/arkism-harness-ui/budget.png'});budgetDenied=true;await page.getByRole('button',{name:'예산 다시 조회'}).click();await page.waitForFunction(()=>document.querySelector('.model-budget')?.textContent.includes('예산 설정을 확인할 수 없어요'));assert(!(await page.locator('.model-budget').innerText()).includes('$0.00'));
    assert.deepEqual(errors,[]);console.log('PASS: evaluation pending/self/fail/error/stale; mobile; atomic retry; budget and denied state');
  }finally{await browser.close();server.close();}
})().catch(e=>{console.error(e);server.close();process.exitCode=1;});
