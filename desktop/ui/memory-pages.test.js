const {test}=require('node:test');const assert=require('node:assert/strict');const {create}=require('./memory-pages.js');
const row=id=>({id});
test('first load reads one page; concurrent more requests share one call; cursor exhaustion stops',async()=>{
 const calls=[];let release;const pager=create(async(key,args)=>{calls.push([key,args]);if(args.cursor==='next')await new Promise(r=>release=r);return args.cursor?{authority:"data_only_untrusted",items:[row('b')],next_cursor:''}:{authority:"data_only_untrusted",items:[row('a')],next_cursor:'next'};});
 assert.deepEqual(await pager.load('scope-a'),[row('a')]);assert.equal(calls.length,1);
 const more=pager.load('scope-a');const same=pager.load('scope-a');assert.equal(more,same);release();await more;
 assert.deepEqual(pager.snapshot().items,[row('a'),row('b')]);await pager.load('scope-a');assert.equal(calls.length,2);
});
test('old scope or search replies cannot mutate a replacement or locked session',async()=>{
 let release;const pager=create(key=>key==='old'?new Promise(r=>release=r):Promise.resolve({authority:"data_only_untrusted",items:[row('new')],next_cursor:''}));
 const old=pager.load('old');await pager.load('new');release({authority:"data_only_untrusted",items:[row('private-old')],next_cursor:'old-next'});await old;assert.deepEqual(pager.snapshot().items,[row('new')]);
 const stale=pager.load('old');pager.reset();release({authority:"data_only_untrusted",items:[row('private-old')],next_cursor:''});await stale;assert.deepEqual(pager.snapshot().items,[]);
});
test('failed pages preserve the old list and cursor and can retry; repeated cursors fail closed',async()=>{
 let fail=true;const pager=create(async(_,args)=>{if(!args.cursor)return {authority:"data_only_untrusted",items:[row('a')],next_cursor:'next'};if(fail)throw Error('offline');return {authority:"data_only_untrusted",items:[row('b')],next_cursor:''};});
 await pager.load('x');await assert.rejects(pager.load('x'),/offline/);assert.equal(pager.snapshot().cursor,'next');assert.deepEqual(pager.snapshot().items,[row('a')]);fail=false;await pager.load('x');assert.equal(pager.snapshot().items.length,2);
 const bad=create(async()=>({authority:"data_only_untrusted",items:[row('a')],next_cursor:'same'}));await bad.load('x');await assert.rejects(bad.load('x'),/memory_cursor_repeated/);assert.equal(bad.snapshot().items.length,1);
});
test('refresh revalidates only the loaded window and removes deleted rows atomically',async()=>{
 let phase=0;const calls=[];const pager=create(async(_,args)=>{calls.push(args);return phase?{authority:"data_only_untrusted",items:[row('fresh')],next_cursor:'later'}:args.cursor?{authority:"data_only_untrusted",items:[row('b')],next_cursor:'tail'}:{authority:"data_only_untrusted",items:[row('a'),row('z')],next_cursor:'next'};},2);
 await pager.load('x');await pager.load('x');phase=1;
 // Two loaded pages request at most two pages. A bad second cursor keeps the old window.
 await assert.rejects(pager.refresh('x'),/memory_cursor_repeated/);assert.deepEqual(pager.snapshot().items,[row('a'),row('z'),row('b')]);assert.equal(calls.length,4);
});
test('oversized pages and malformed IDs never publish partial data',async()=>{
 for(const response of [{authority:"data_only_untrusted",items:[row('a'),row('b')],next_cursor:''},{authority:"data_only_untrusted",items:[{}],next_cursor:''},{authority:"data_only_untrusted",items:[],next_cursor:7}]){const pager=create(async()=>response,1);await assert.rejects(pager.load('x'),/memory_page_invalid/);assert.deepEqual(pager.snapshot().items,[]);}
});
test('retry after a failed refresh revalidates the loaded window instead of appending',async()=>{
 let phase=0;const calls=[];const pager=create(async(_,args)=>{calls.push(args.cursor);if(phase===1)throw Error('offline');return {authority:'data_only_untrusted',items:[row(phase===2?(args.cursor?'fresh-b':'fresh-a'):(args.cursor?'old-b':'old-a'))],next_cursor:args.cursor?'tail':'next'};},1);
 await pager.load('x');await pager.load('x');phase=1;await assert.rejects(pager.refresh('x'),/offline/);phase=2;await pager.retry('x');assert.deepEqual(pager.snapshot().items,[row('fresh-a'),row('fresh-b')]);assert.deepEqual(calls.slice(-2),['','next']);
});

test("dreaming proposals read memories outside the loaded first page, signal ones included", async () => {
  global.window = global.window || {};
  window.ArkismMemoryPages = window.ArkismMemoryPages || { create: () => ({ snapshot: () => ({}) }) };
  const { createMemoryPaging } = await import("./modules/memoryPaging.mjs");
  const read = [];
  const state = { data: { memories: [{ id: "a", revision: 1 }], maintenance: [
    { maintenance: { keep: { id: "a", revision: 1 }, retire: { id: "b", revision: 2 } } },
    { maintenance: { memory: { id: "c" } } }] } };
  const paging = createMemoryPaging({ state, generation: 1, query: () => "", render: () => {},
    request: async (url) => { const id = decodeURIComponent(url.split("/")[3].split("?")[0]); read.push(id); return { id, revision: id === "b" ? 2 : 5 }; } });
  await paging.hydrateMaintenance(1);
  assert.deepEqual(read.sort(), ["b", "c"]);
  assert.deepEqual([...paging.maintenanceRecords.keys()].sort(), ["b", "c"]);
});
