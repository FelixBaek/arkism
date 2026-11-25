const test=require("node:test"), assert=require("node:assert/strict"),{createHash}=require("node:crypto");
const {createDocument,house}=require("./testdata/views-dom.js");
const sha=s=>createHash("sha256").update(s).digest("hex");
const id="demo",scope={workspace_id:"w"},versions=["skill-"+sha("one"),"skill-"+sha("two")],marker="never expose file contents";
function bundle(files) {return {sha256:sha(files.map(f=>f.path+"\n"+f.sha256+"\n").join("")),files};}
const inline=(path,content)=>({path,content,sha256:sha(content)});
function setup({missing=false,deny=false,invalid=false,delay=false,mutate=null}={}) {
  const doc=createDocument();globalThis.document=doc;
  const state={server:"local",token:"tokenA",scope:{...scope},view:"skills",selected:"skill:demo"},calls=[];
  const skill={id,scope,active_version:versions[1],version_history:versions};
  const analysis={content_hash:sha("same"),input_format:"text"};
  const text=JSON.stringify(analysis);
  const b1=bundle([inline("guide.md",marker),inline("old.md",marker)]);
  const b2=bundle([inline("added.md",marker),{path:"guide.md",sha256:sha(marker),omitted:"binary",size:marker.length}]);
  let release; const gate=new Promise(resolve=>release=resolve);
  const req=async(path,opt)=>{
    calls.push([path,opt?.method||"GET"]);
    if(path.startsWith("/v1/skills/demo?"))return skill;
    if(path==="/v1/skills/resolve"){
      if(delay)await gate;
      const {refs}=JSON.parse(opt.body);
      const items=refs.map(ref=>{
        const i=ref.endsWith("@1")?0:1;
        return {ref,version_id:versions[i],resolved_version:i+1,skill_id:id,instructions:"same",content_hash:sha("same"),
          requirements:{reads:true,writes:!!i,secret_names:i?["PRIVATE-NEW"]:["PRIVATE-OLD"]},bundle:i?b2:missing?undefined:b1};
      });
      if(mutate)mutate(items,refs);
      return {items};
    }
    const i=versions.findIndex(v=>path.startsWith("/v1/skill_versions/"+v+"?"));
    if(deny||i<0)throw Error("denied");
    return {id:versions[i],skill_id:id,scope:invalid?{...scope,user_id:"other"}:scope,text,content_hash:sha(text),analysis};
  };
  return {deps:{el:house(doc).el,state,request:req,query:()=>"q=1"},doc,calls,release};
}
async function start(options) {
  const {skillSelectionView}=await import("./modules/skillSelectionView.mjs");
  const ctx=setup(options),panel=skillSelectionView(ctx.deps,id);ctx.doc.body.append(panel);
  assert.equal(ctx.calls.length,0);panel.open=true;
  return {...ctx,panel,loading:panel.fire("toggle")};
}
test("selected versus live approved version, exact file picker, hashes only",async()=>{
  const s=await start();await s.loading;
  assert.match(s.panel.textContent,/선택 1판 · 최신 승인 2판 · 본문 같음/);
  assert.match(s.panel.textContent,/쓰기 · 변경/);
  assert.doesNotMatch(s.panel.textContent,/PRIVATE-NEW|PRIVATE-OLD|never expose/);
  const [version,file]=s.panel.querySelectorAll("select");
  assert.match(file.textContent,/added.md · 추가/);assert.match(file.textContent,/old.md · 삭제/);
  file.value="guide.md";await file.fire("change");assert.match(s.panel.textContent,/지문만 제공 \(바이너리\)/);assert.match(s.panel.textContent,new RegExp(sha(marker)));
  version.value="1";await version.fire("change");assert.match(s.panel.textContent,/선택 2판 · 최신 승인 2판/);
  assert(s.calls.every(([path,method])=>method==="GET"||path==="/v1/skills/resolve"&&method==="POST"));
});
test("missing historical manifest remains unknown",async()=>{
  const s=await start({missing:true});await s.loading;assert.match(s.panel.textContent,/선택판 명세 미기재/);
  assert.match(s.panel.querySelectorAll("select")[1].textContent,/added.md · 확인 불가/);
});
test("inaccessible or wrong-scope version never publishes metadata",async()=>{
  for(const opt of [{deny:true},{invalid:true}]){const s=await start(opt);await s.loading;assert.match(s.panel.textContent,/확인할 수 없어요/);assert.doesNotMatch(s.panel.textContent,/보조 파일 비교|guide.md/);}
});
test("late resolve discarded when changing skill, closing or changing credentials",async()=>{
  for(const act of [s=>s.deps.state.selected="other",s=>s.panel.open=false,s=>s.deps.state.token="new"]){
    const s=await start({delay:true});await new Promise(r=>setImmediate(r));act(s);s.release();await s.loading;assert(!s.calls.some(([p])=>p.includes("/skill_versions/")));assert.doesNotMatch(s.panel.textContent,/최신 승인 2판|guide.md/);
  }
});

test("rejects contradictory numbered skill references before reading version records",async()=>{
  const faults=[
    items=>items[0].resolved_version=0,
    items=>items[0].resolved_version=1.5,
    items=>items[0].resolved_version=String(items[0].resolved_version),
    items=>items[0].resolved_version=items[1].resolved_version,
    items=>items[1].version_id=items[0].version_id,
    items=>items[1].resolved_version=Number.MAX_SAFE_INTEGER+1,
  ];
  for(const mutate of faults){
    const s=await start({mutate});await s.loading;
    assert.match(s.panel.textContent,/확인할 수 없어요/);
    assert(!s.calls.some(([p])=>p.includes("/skill_versions/")));
  }
});
test("same version ID with different self-consistent body hashes is refused",async()=>{
  const s=await start({mutate:(items,refs)=>{
    if(refs[0].endsWith("@2")){
      items[1].instructions="different but correctly hashed";items[1].content_hash=sha(items[1].instructions);
    }
  }});
  await s.loading;
  const selector=s.panel.querySelector("select");
  const reads=s.calls.filter(([p])=>p.includes("/skill_versions/")).length;
  selector.value="1";await selector.fire("change");
  assert.match(s.panel.textContent,/확인할 수 없어요/);
  assert.doesNotMatch(s.panel.textContent,/different but correctly hashed|보조 파일 비교/);
  assert.equal(s.calls.filter(([p])=>p.includes("/skill_versions/")).length,reads);
});