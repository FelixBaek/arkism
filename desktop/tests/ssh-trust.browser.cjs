// Run with an installed playwright-core (or PLAYWRIGHT_CORE_PATH) and Chromium.
// No server, credentials, external requests or application installation.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_CORE_PATH || 'playwright-core');
const ui = path.resolve(__dirname, '../ui');

(async () => {
 const browser = await chromium.launch({headless:true, ...(process.env.CHROMIUM_PATH ? {executablePath:process.env.CHROMIUM_PATH} : {})});
 const page = await browser.newPage({viewport:{width:1000,height:800}});
 page.setDefaultTimeout(10000);
 try {
  // The dialog uses the real project's stylesheet cascade, including confirmation styling.
  await page.setContent('<!doctype html><html lang="ko"><body><button id="origin">서버 연결</button></body></html>');
  for (const name of [...fs.readFileSync(path.join(ui,'index.html'),'utf8').matchAll(/href="([^"]+\.css)"/g)].map(m=>m[1])) {
   await page.addStyleTag({content:fs.readFileSync(path.join(ui,name),'utf8')});
  }
  await page.addScriptTag({content:fs.readFileSync(path.join(ui,'ssh-trust.js'),'utf8')});
  const start = async (mode='unknown', command='ssh_connect', twice=false) => page.evaluate(({mode,command,twice})=>{
   window.calls=[];window.result=null;window.results=null;
   const review={state:mode==='changed'?'trusted':'unknown',host:'fixture.example',port:2222,key_type:'ssh-ed25519',fingerprint:'SHA256:'+'A'.repeat(43),previous_fingerprint:null,challenge:mode==='changed'?null:'fresh-review'};
   const core={async invoke(name,args){
    window.calls.push({name,args});
    if(name==='ssh_host_key_check')return args.force?{...review,state:'changed',fingerprint:'SHA256:'+'B'.repeat(43),previous_fingerprint:review.fingerprint,challenge:'changed-review'}:review;
    if(name==='ssh_host_key_approve'){if(mode==='stale'){throw new Error('ssh_host_key_review_stale');}return {state:'trusted'};}
    if(name==='ssh_host_key_cancel')return;
    if(mode==='changed'&&window.calls.filter(c=>c.name===command).length===1)throw new Error('ssh_host_key_changed');
    return {ok:true};
   }};
   const gate=window.ArkismSSH(core),args=command.startsWith('ssh_')?{form:{host:'fixture.example'}}:{host:'fixture.example',request:{action:'history'}};
   document.querySelector('#origin').focus();
   const run=()=>gate.invoke(command,args).then(value=>({ok:true,value}),e=>({ok:false,error:String(e.message||e)}));
   window.job=(twice?Promise.all([run(),run()]):run()).then(value=>{window.result=value;});
  },{mode,command,twice});
  const finished = async () => {await page.waitForFunction(()=>window.result!==null);return page.evaluate(()=>window.result);};
  const approve = async () => {await page.locator('.ssh-trust input').check();await page.getByRole('button',{name:/저장하고 계속|교체하고 계속/}).click();};
  const targetCalls = () => page.evaluate(()=>window.calls.filter(c=>['ssh_connect','ssh_test','git_op','skill_execution'].includes(c.name)));
  let count=0;
  await start();await page.locator('dialog[open]').waitFor();
  assert.equal(await page.locator(':focus').innerText(),'취소');
  assert.equal(await page.getByRole('button',{name:'이 키를 저장하고 계속'}).isDisabled(),true);
  await page.keyboard.press('Enter');assert.equal((await finished()).error,'ssh_host_key_cancelled');assert.equal((await targetCalls()).length,0);count++;
  await start('unknown','ssh_test');await page.locator('dialog[open]').waitFor();await page.keyboard.press('Escape');assert.equal((await finished()).error,'ssh_host_key_cancelled');assert.equal((await targetCalls()).length,0);count++;
  await start();await page.locator('dialog[open]').waitFor();await page.keyboard.press('Shift+Tab');assert.equal(await page.locator(':focus').getAttribute('type'),'checkbox');await page.keyboard.press('Space');await page.keyboard.press('Tab');await page.keyboard.press('Tab');await page.keyboard.press('Enter');assert.equal((await finished()).ok,true);assert.equal((await targetCalls()).length,1);assert.equal(await page.locator(':focus').getAttribute('id'),'origin');count++;
  await start('changed','git_op');await page.getByRole('heading',{name:'서버의 SSH 키가 바뀌었어요'}).waitFor();assert.match(await page.locator('.ssh-trust').innerText(),/이전 지문/);await page.keyboard.press('Escape');assert.equal((await finished()).ok,false);assert.equal((await targetCalls()).length,1);count++;
  await start('changed','skill_execution');await page.getByRole('heading',{name:'서버의 SSH 키가 바뀌었어요'}).waitFor();await approve();assert.equal((await finished()).ok,true);assert.equal((await targetCalls()).length,2);assert.equal(await page.evaluate(()=>window.calls.find(c=>c.name==='ssh_host_key_approve').args.replace),true);count++;
  await start('unknown','ssh_connect',true);await page.locator('dialog[open]').waitFor();assert.equal(await page.locator('dialog').count(),1);await approve();assert.equal((await finished()).every(x=>x.ok),true);assert.equal(await page.evaluate(()=>window.calls.filter(c=>c.name==='ssh_host_key_check').length),1);count++;
  await start('stale');await page.locator('dialog[open]').waitFor();await approve();assert.equal((await finished()).error,'ssh_host_key_review_stale');assert.equal((await targetCalls()).length,0);count++;
  await page.setViewportSize({width:360,height:640});await page.evaluate(()=>document.documentElement.style.setProperty('--fs','2'));
  await start('changed');await page.locator('dialog[open]').waitFor();
  assert.equal(await page.locator('.ssh-trust').evaluate(el=>el.scrollWidth<=el.clientWidth+1),true);
  if(process.env.SSH_TRUST_SCREENSHOT)await page.screenshot({path:process.env.SSH_TRUST_SCREENSHOT});
  await page.keyboard.press('Escape');await finished();count++;
  console.log(`${count} SSH dialog browser scenarios passed (keyboard, cancellation, approval, rotation, concurrency, stale review, narrow/large text).`);
 } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
