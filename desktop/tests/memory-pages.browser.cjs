// Real app DOM and keyboard with a fake native API; no server or product data.
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {chromium}=require(process.env.PLAYWRIGHT_CORE_PATH||path.resolve(__dirname,'../frontend/node_modules/playwright-core'));
const ui=path.resolve(process.env.ARKISM_UI_DIRECTORY||path.resolve(__dirname,'../ui'));
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.CHROMIUM_PATH?{executablePath:process.env.CHROMIUM_PATH}:{})});
 const page=await browser.newPage({locale:'ko-KR',viewport:{width:1440,height:1000}});page.setDefaultTimeout(12000);const errors=[];page.on('pageerror',e=>errors.push(e.message));
 try{
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url());if(url.origin!=='https://arkism.fixture'){await route.abort();return;}
   const file=path.resolve(ui,'.'+decodeURIComponent(url.pathname==='/'?'/index.html':url.pathname));
   if(!file.startsWith(ui+path.sep)){await route.abort();return;}
   try{const type={'.html':'text/html','.js':'application/javascript','.mjs':'application/javascript','.css':'text/css','.svg':'image/svg+xml','.woff2':'font/woff2'}[path.extname(file)]||'application/octet-stream';await route.fulfill({body:fs.readFileSync(file),contentType:type});}catch{await route.fulfill({status:404,body:''});}
  });
  await page.addInitScript(()=>{
   window.memoryCalls=[];window.failNextPage=false;window.removed=new Set();
   window.__TAURI__={event:{listen:async()=>()=>{}},core:{async invoke(command,args){
    if(command==='app_defaults')return {local:true,updates:false};if(command==='local_start')return {server:'http://127.0.0.1:8091',token:'inert-browser-fixture'};
    if(command!=='api')return null;
    const url=new URL(args.path,'http://fixture'),p=url.pathname;let body={items:[],next_cursor:'',authority:'data_only_untrusted'};
    if(p==='/v1/capabilities')body={role:'human',actions:{review_list:true,review_decide:true}};
    if(p==='/v1/providers')body={providers:[]};
    if(p==='/v1/memories'||p==='/v1/memories/query'){
     const catalog=p.endsWith('/query');if(catalog){const input=JSON.parse(args.body);for(const [key,value] of Object.entries(input))if(value!==null)url.searchParams.set(key,String(value));}
     const q=url.searchParams.get('q')||'',kind=url.searchParams.get('kind')||'',cursor=url.searchParams.get('cursor')||'',limit=Number(url.searchParams.get('limit'));
     window.memoryCalls.push({q,kind,cursor,limit,ignore_case:url.searchParams.get("ignore_case")});
     if(window.legacySearch&&url.searchParams.has("ignore_case"))return {status:400,body:JSON.stringify({code:"invalid_request"})};
     if(cursor&&window.failNextPage){window.failNextPage=false;return {status:503,body:JSON.stringify({code:'dependency_unavailable'})};}
     const items=[];let i=Number(cursor)||0;
     for(;i<100000&&items.length<limit;i++){
      if(window.removed.has(i))continue;
      const row={id:`m-${String(i).padStart(6,'0')}`,kind:i%2?'decision':'semantic',status:'active',text:i===90000?'RARE outside first page':`memory ${i}`,summary:'',revision:1,tags:[],created_at:'2026-10-20T00:00:00Z',updated_at:'2026-10-20T00:00:00Z',created_by_type:'human'};
      if(q&&!(window.legacySearch?row.text.includes(q):row.text.toLowerCase().includes(q.toLowerCase()))||kind&&row.kind!==kind)continue;items.push(row);
     }
     body={items,next_cursor:i<100000?String(i):'',authority:'data_only_untrusted',...(catalog?{total:q?1:kind?50000:100000,facets:{kinds:{semantic:50000,decision:50000},repositories:{}}}:{})};
    }
    return {status:200,body:JSON.stringify(body)};
   }}};
  });
  await page.goto('https://arkism.fixture/');
  await page.waitForFunction(()=>document.querySelector('#app')?.inert===false&&document.querySelector('#count-memories')?.textContent==='50+');
  assert.equal(await page.evaluate(()=>window.memoryCalls.length),1);
  await page.getByRole('button',{name:/^기억 보기/}).click();
  await page.waitForFunction(()=>document.querySelectorAll('#table [data-id]').length===50);
  assert.equal(await page.evaluate(()=>window.memoryCalls.some(c=>c.cursor)),false);
  await page.getByRole('button',{name:'더 보기',exact:true}).focus();
  await page.keyboard.press('Enter');
  await page.waitForFunction(()=>document.querySelectorAll('#table [data-id]').length===100);
  assert.equal(await page.evaluate(()=>window.memoryCalls.filter(c=>c.cursor).length),1);
  assert.equal(await page.locator(':focus').innerText(),'더 보기');
  await page.evaluate(()=>window.failNextPage=true);await page.getByRole('button',{name:'더 보기',exact:true}).click();
  await page.getByRole('button',{name:'다시 불러오기',exact:true}).waitFor();assert.equal(await page.locator('#table [data-id]').count(),100);
  await page.getByRole('button',{name:'다시 불러오기',exact:true}).click();await page.waitForFunction(()=>document.querySelectorAll('#table [data-id]').length===150);
  await page.getByRole('searchbox',{name:'기억 검색',exact:true}).fill('rare');
  await page.locator('#table').getByText('RARE outside first page',{exact:true}).waitFor();assert.equal(await page.locator('#table [data-id]').count(),1);
  await page.locator('#table').getByText('RARE outside first page',{exact:true}).click();
  assert.match(await page.locator('#detail h2').innerText(),/RARE outside first page/);
  await page.getByRole('searchbox',{name:'기억 검색',exact:true}).fill('');
  await page.locator('.mem-kind').filter({hasText:'결정'}).click();
  await page.waitForFunction(()=>window.memoryCalls.some(c=>c.kind==='decision')&&document.querySelectorAll('#table [data-id]').length===50);
  await page.locator('#table [data-id]').first().click();await page.locator('#detail h2').waitFor();
  assert.equal(errors.length,0,errors.join('\n'));
  await page.setViewportSize({width:760,height:900});
  assert.equal(await page.locator('#stats .memory-page-note').isVisible(),true);
  if(process.env.MEMORY_PAGES_SCREENSHOT)await page.screenshot({path:process.env.MEMORY_PAGES_SCREENSHOT});
  console.log('Real memory screen passed: first 50 only, explicit next page, failure/retry preservation, search outside loaded rows, server kind filter and detail selection.');
 }catch(e){console.error('App errors:',errors);console.error(await page.locator('body').innerText().catch(()=>''));throw e;}finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
