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
test('every cursor-paged collection loads from the preview',async()=>{
 const {createCollectionPaging}=await import('./modules/collectionPaging.mjs');
 const paging=createCollectionPaging({state:state(),request,createPager:create});
 for(const name of ['candidates','conflicted','skills','skillTrash'])assert.ok((await paging.read(name)).length>0,name);
});
