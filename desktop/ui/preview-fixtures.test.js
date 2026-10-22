// The stock preview server (testdata/preview/serve.py) must pass the same page checks the app runs on load.
const {test,before,after}=require('node:test');const assert=require('node:assert/strict');
const {spawn}=require('node:child_process');const net=require('node:net');const path=require('node:path');
const {create}=require('./memory-pages.js');
let server,origin;
before(async()=>{
 const port=await new Promise(r=>{const s=net.createServer().listen(0,'127.0.0.1',()=>{const p=s.address().port;s.close(()=>r(p));});});
 server=spawn('python3',['-I',path.join(__dirname,'testdata/preview/serve.py'),String(port)],{stdio:'ignore'});origin=`http://127.0.0.1:${port}`;
 for(let i=0;i<100;i++){try{if((await fetch(origin+'/readyz')).ok)return;}catch{}await new Promise(r=>setTimeout(r,50));}
 throw Error('preview server did not start');
});
after(()=>server?.kill());
const request=async(p,init={})=>(await fetch(origin+p,init)).json();
const state=()=>({server:'preview',scope:{workspace_id:'default',project_id:'default'},connection:{generation:1},view:'memories',memTab:'active',kind:'all',repo:'',sort:'recent',query:'',skillTab:'all',skillSort:'recent',skillDir:0,skillModel:'',skillCat:''});
test('every catalog query list loads from the preview',async()=>{
 const {createCatalogQuery}=await import('./modules/catalogQuery.mjs');
 const catalog=createCatalogQuery({state:state(),request,createPager:create,preferences:()=>({kept:[],collectUnused:true,categories:[],agents:[]}),render(){}});
 for(const name of ['memories','candidates','conflicted','skills','skillTrash']){await catalog.more(name);const snap=catalog.snapshot(name);assert.ok(snap.items.length>0,name);assert.equal(snap.total,snap.items.length,name);}
 assert.ok(catalog.snapshot('conflicted').items.every(row=>row.status==='conflicted'));
});
test('the idle memory query keeps the server rule, not the whole active list',async()=>{
 const body=state=>({method:'POST',body:JSON.stringify({scope:{},state,limit:50})});
 const active=await request('/v1/memories/query',body('active')),idle=await request('/v1/memories/query',body('idle'));
 assert.ok(idle.items.length>0&&idle.items.length<active.items.length);assert.equal(idle.total,idle.items.length);
 assert.ok(idle.items.every(row=>!['user','procedural'].includes(row.kind)));
});
test('every cursor-paged collection loads from the preview',async()=>{
 const {createCollectionPaging}=await import('./modules/collectionPaging.mjs');
 const paging=createCollectionPaging({state:state(),request,createPager:create});
 for(const name of ['candidates','conflicted','skills','skillTrash'])assert.ok((await paging.read(name)).length>0,name);
});
test('the review window query merges candidates and conflicts and counts them as new and confusing',async()=>{
 const query=body=>request('/v1/memory_candidates/query',{method:'POST',body:JSON.stringify({scope:{},limit:50,...body})});
 const all=await query({state:'all'}),cand=await query({state:'candidate'}),conf=await query({state:'conflicted'});
 assert.equal(all.total,cand.total+conf.total);assert.ok(conf.total>0);
 assert.equal((all.facets.states.new||0)+(all.facets.states.confusing||0),all.total);assert.ok(all.facets.states.confusing>=conf.total);
 const confusing=await query({state:'all',category:'confusing'});assert.equal(confusing.total,all.facets.states.confusing);
});
test('the query pages by limit and cursor and keeps the whole total',async()=>{
 const page=cursor=>request('/v1/memories/query',{method:'POST',body:JSON.stringify({scope:{},state:'active',limit:4,...(cursor?{cursor}:{})})});
 const first=await page(''),ids=[...first.items];let next=first.next_cursor;
 assert.equal(first.items.length,4);assert.ok(first.total>4&&next);
 while(next){const more=await page(next);ids.push(...more.items);assert.ok(more.items.length<=4);next=more.next_cursor;}
 assert.equal(new Set(ids.map(row=>row.id)).size,first.total);
});
test('the skill query narrows by the user category rules',async()=>{
 const categories=[{id:'writing',rules:[['name','writing-*']]},{id:'code',rules:[['path','code-review']]}];
 const query=category=>request('/v1/skills/query',{method:'POST',body:JSON.stringify({scope:{},state:'all',limit:50,categories,category})});
 const all=await query(''),writing=await query('writing'),none=await query('none');
 assert.ok(writing.total>0&&writing.items.every(row=>/^writing-/i.test(row.name)));
 assert.equal(all.facets.categories.writing,writing.total);assert.equal(all.facets.categories[''],none.total);
 assert.ok(none.items.every(row=>!/^writing-/i.test(row.name)&&!/code-review/.test(row.id+' '+row.source_path)));
});
