const { test } = require('node:test');
const assert = require('node:assert/strict');
const { create } = require('./memory-pages.js');
const identity = () => ({server:'fixture',scope:{workspace_id:'w',project_id:'p'},connection:{generation:1},view:'memories',memTab:'active',kind:'all',repo:'',sort:'recent',query:''});
const result = (items,total,next_cursor='') => ({items,total,next_cursor,facets:{kinds:{},repositories:{}},authority:'data_only_untrusted'});

test('whole-catalog query and next page retain filters and bounded transfer', async () => {
 const {createCatalogQuery}=await import('./modules/catalogQuery.mjs');
 const state=identity(), calls=[];
 const catalog=createCatalogQuery({state,createPager:create,preferences:()=>({kept:[],collectUnused:true}),render(){},request:async(path,init)=>{
   const body=JSON.parse(init.body);calls.push({path,body});const start=Number(body.cursor)||0;
   return result(Array.from({length:50},(_,i)=>({id:String(start+i),text:body.q})),100,start?'':'50');
 }});
 state.query='Unloaded needle';state.kind='procedural';state.repo='far-repo';state.sort='old';
 await catalog.more('memories');await catalog.more('memories');
 assert.equal(catalog.snapshot('memories').items.length,100);
 assert.equal(calls[0].body.q,'Unloaded needle');assert.equal(calls[1].body.cursor,'50');
 assert.equal(calls[1].body.kind,'procedural');assert.equal(calls[1].body.repository,'far-repo');assert.equal(calls[1].body.sort,'old');assert.equal(calls[1].body.limit,50);
});
test('refreshed selection wins over pinned data; late old-scope replies stay hidden',async()=>{
 const {createCatalogQuery}=await import('./modules/catalogQuery.mjs');const state=identity();let release,version=1;
 const c=createCatalogQuery({state,createPager:create,preferences:()=>({kept:[]}),render(){},request:async()=>{if(version===3)await new Promise(r=>release=r);return result([{id:'selected',text:String(version)}],1)}});
 await c.more('memories');c.pin('selected');version=2;await c.refreshVisible();assert.equal(c.record('selected').text,'2');
 version=3;const pending=c.refreshVisible();state.scope={workspace_id:'other',project_id:'p'};release();await pending;
 assert.equal(c.record('selected'),null);assert.deepEqual(c.snapshot('memories').items,[]);
});
test('changed server snapshot restarts the opened window and failure preserves existing rows',async()=>{
 const {createRemoteCollection}=await import('./modules/remoteCollection.mjs');const state=identity();let changed=false,failed=false;const calls=[];
 const c=createRemoteCollection({state,createPager:create,read:async(filters,page)=>{calls.push(page.cursor);if(page.cursor&&changed){changed=false;throw Error('revision_conflict')}if(failed)throw Error('dependency_unavailable');return result([{id:page.cursor?'second':'first'}],2,page.cursor?'':'next')}});
 const f={q:'all'};await c.load(f);changed=true;await c.load(f,true);assert.deepEqual(calls,['','next','']);assert.equal(c.snapshot(f).items[0].id,'first');
 failed=true;await assert.rejects(c.load(f,true),/dependency/);assert.equal(c.snapshot(f).items.length,1);
 failed=false;await c.load(f,true);assert.equal(c.snapshot(f).items.length,2);
 state.connection.generation++;assert.deepEqual(c.snapshot(f).items,[]);
});
test('malformed page metadata and cursor cycles cannot be accepted as complete',async()=>{
 const {createRemoteCollection}=await import('./modules/remoteCollection.mjs');const state=identity();let malformed=true;
 const c=createRemoteCollection({state,createPager:create,read:async()=>malformed?{...result([],0),total:-1}:result([{id:'x'}],200,'repeat')});
 await assert.rejects(c.load({}),/invalid/);malformed=false;await c.load({});await assert.rejects(c.load({},true),/repeated/);assert.equal(c.snapshot({}).items.length,1);
});

test('failed multi-page refresh keeps counts and facets with the published rows',async()=>{
 const {createRemoteCollection}=await import('./modules/remoteCollection.mjs');
 const {createCatalogQuery}=await import('./modules/catalogQuery.mjs');
 for(const kind of ['remote','catalog']) {
  const state=identity();let phase='initial';
  const read=async page=>{if(phase==='broken'&&page.cursor)throw Error('offline');const fresh=phase!=='initial';return {...result([{id:(fresh?'new-':'old-')+(page.cursor?'b':'a')}],fresh?3:2,page.cursor?'':'next'),facets:{kinds:{decision:fresh?3:2}}}};
  const c=kind==='remote'?createRemoteCollection({state,createPager:create,read:(_,page)=>read(page)}):createCatalogQuery({state,createPager:create,preferences:()=>({kept:[]}),render(){},request:(_,init)=>read(JSON.parse(init.body))});
  const load=(more=false)=>kind==='remote'?c.load({},more):more?c.more('memories'):c.refreshVisible();
  const snapshot=()=>kind==='remote'?c.snapshot({}):c.snapshot('memories');
  await load();await load(true);phase='broken';await assert.rejects(load(),/offline/);
  assert.deepEqual(snapshot().items.map(x=>x.id),['old-a','old-b'],kind);
  assert.equal(snapshot().total,2,kind+' published new counts with old rows');
  assert.equal(snapshot().facets.kinds.decision,2,kind+' published new facets with old rows');
 }
});

test('a changed skill filter keeps the last rows, counts and facets on screen until its page arrives',async()=>{
 const {createCatalogQuery}=await import('./modules/catalogQuery.mjs');const state={...identity(),view:'skills',skillTab:'active'};const releases=[];
 const c=createCatalogQuery({state,createPager:create,preferences:()=>({kept:[],categories:[],agents:[]}),render(){},request:async(_,init)=>{const q=JSON.parse(init.body).q;if(q==='slow')await new Promise(r=>releases.push(r));return {...result(q==='slow'?[]:[{id:'old'}],q==='slow'?0:1),facets:{kinds:{decision:q==='slow'?0:1}}}}});
 await c.more('skills');await c.more('memories');state.query='slow';c.ensure('skills');c.ensure('memories');
 assert.deepEqual(c.snapshot('memories').items,[],'memory tabs never show another filter\'s rows');assert.equal(c.snapshot('memories').stale,undefined);
 const waiting=c.snapshot('skills');
 assert.equal(waiting.ready,false);assert.equal(waiting.loading,true);assert.equal(waiting.stale,true);
 assert.deepEqual(waiting.items.map(x=>x.id),['old']);assert.equal(waiting.total,1);assert.equal(waiting.facets.kinds.decision,1);
 const scope=state.scope;state.scope={workspace_id:'other',project_id:'p'};assert.deepEqual(c.snapshot('skills').items,[],'another scope never shows the old rows');state.scope=scope;
 await new Promise(r=>setImmediate(r));releases.forEach(r=>r());await new Promise(r=>setImmediate(r));
 const done=c.snapshot('skills');assert.equal(done.ready,true);assert.equal(done.stale,undefined);assert.deepEqual(done.items,[]);assert.equal(done.total,0);
});
