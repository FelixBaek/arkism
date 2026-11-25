const scopeKeys = ["workspace_id", "project_id", "user_id", "agent_id"];
const encoder = new TextEncoder();
const hashPattern = /^[0-9a-f]{64}$/;
const fields = {
  analysis: { problem:"목적", use_when:"사용 조건", avoid_when:"피해야 할 조건", input_format:"입력 형식", output_format:"출력 형식", success_criteria:"성공 기준", failure_types:"실패 유형" },
  requirements: { tools:"도구", apis:"API", permissions:"권한", secret_names:"비밀 이름", irreversible_actions:"되돌릴 수 없는 작업", environments:"환경", dependencies:"의존성", reads:"읽기", writes:"쓰기", external_transfer:"외부 전송" },
  resolution: { compatibility:"런타임 호환 정보" },
};
async function sha(text) {
  const bytes = await crypto.subtle.digest("SHA-256", encoder.encode(text));
  return Array.from(new Uint8Array(bytes), b => b.toString(16).padStart(2, "0")).join("");
}
function fail() { throw Error("invalid_selection_evidence"); }
function matchesScope(row, scope) {
  return row?.scope && scopeKeys.every(k => (row.scope[k] || "") === (scope[k] || "")) && !row.content_erased && !row.privacy_quarantined;
}

async function manifest(bundle) {
  if (bundle == null) return null;
  if (!hashPattern.test(bundle.sha256) || !Array.isArray(bundle.files) || bundle.files.length > 2048) fail();
  let previous = "";
  const files = new Map();
  const manifestLines = [];
  for (const f of bundle.files) {
    if (!f || typeof f.path !== "string" || !f.path || f.path.length > 512 || files.has(f.path) ||
        (/[\\:]/.test(f.path) || Array.from(f.path).some(c => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)) || f.path.split("/").some(s => !s || s.startsWith(".")) || !hashPattern.test(f.sha256)) fail();
    // Go sorts paths as UTF-8 bytes. JavaScript's UTF-16 order differs for some Unicode names.
    if (previous && compareUTF8(previous, f.path) >= 0) fail();
    previous = f.path;
    const hashOnly = Object.hasOwn(f, "omitted");
    if (hashOnly) {
      if (Object.hasOwn(f,"content") || !["binary","too_large","limit","secret"].includes(f.omitted) || !Number.isSafeInteger(f.size) || f.size <= 0) fail();
    } else if (typeof f.content !== "string" || await sha(f.content) !== f.sha256) fail();
    files.set(f.path, { sha256:f.sha256, omitted:hashOnly ? f.omitted : "", size:hashOnly ? f.size : encoder.encode(f.content).length });
    manifestLines.push(f.path + "\n" + f.sha256 + "\n");
  }
  if (await sha(manifestLines.join("")) !== bundle.sha256) fail();
  return files;
}
function compareUTF8(left, right) {
  const a=encoder.encode(left), b=encoder.encode(right);
  for (let n=0; n<Math.min(a.length,b.length); n++) if (a[n] !== b[n]) return a[n]-b[n];
  return a.length-b.length;
}
function fieldValue(group, value) {
  if (value == null || value === "" || (Array.isArray(value) && !value.length)) return null;
  if (Array.isArray(value) && group === "requirements") return [...new Set(value)].sort(compareUTF8);
  if (typeof value === "object" && !Array.isArray(value)) return Object.fromEntries(Object.entries(value).sort(([a],[b]) => compareUTF8(a,b)));
  return value;
}
export async function selectionEvidence(deps, skill, index, isCurrent = () => true) {
  const references = [skill.id+"@"+(index+1),skill.id+"@latest-approved"];
  const scope = {...deps.state.scope}, query = deps.query();
  if (!isCurrent()) fail();
  const batch = await deps.request("/v1/skills/resolve", {method:"POST",body:JSON.stringify({scope,refs:references})});
  if (!isCurrent() || !Array.isArray(batch?.items) || batch.items.length !== 2) fail();
  const [selected, latest] = batch.items;
  if (!Number.isSafeInteger(index) || index < 0 || index >= skill.version_history.length) fail();
  const numbers = [index + 1, skill.version_history.indexOf(skill.active_version) + 1];
  for (const [i, item] of batch.items.entries()) {
    const n = numbers[i], expected = skill.version_history[n - 1];
    if (!item || item.error || item.ref !== references[i] || item.skill_id !== skill.id ||
        n < 1 || !Number.isSafeInteger(item.resolved_version) || item.resolved_version !== n ||
        !/^skill-[0-9a-f]{64}$/.test(expected) || item.version_id !== expected ||
        !hashPattern.test(item.content_hash) || typeof item.instructions !== "string") fail();
  }
  const sameID = selected.version_id === latest.version_id;
  if (sameID !== (selected.resolved_version === latest.resolved_version) ||
      (sameID && selected.content_hash !== latest.content_hash)) fail();
  const cache = new Map();
  const pair = await Promise.all(batch.items.map(async item => {
    const expected = item.version_id;
    if (await sha(item.instructions) !== item.content_hash) fail();
    if (!isCurrent()) fail();
    if (!cache.has(expected)) cache.set(expected, deps.request("/v1/skill_versions/"+encodeURIComponent(expected)+"?"+query));
    const v=await cache.get(expected);
    if (!matchesScope(v,scope) || v.id!==expected || v.skill_id!==skill.id || typeof v.text!=="string" || await sha(v.text)!==v.content_hash) fail();
    let analysis=v.analysis;
    if (!analysis) { try {analysis=JSON.parse(v.text);} catch {analysis=null;} }
    return {version:item.resolved_version,id:expected,hash:item.content_hash,analysis:analysis?.content_hash===item.content_hash ? analysis : {},
      requirements:item.requirements || {}, resolution:{compatibility:item.compatibility}, files:await manifest(item.bundle)};
  }));
  const [a,b]=pair, changes=[];
  for (const [group, names] of Object.entries(fields)) for(const [field,label] of Object.entries(names)) {
    const x=fieldValue(group,a[group][field]), y=fieldValue(group,b[group][field]);
    const status=x===null && y===null ? "양쪽 미기재" : x===null || y===null ? "한쪽 미기재" : JSON.stringify(x)===JSON.stringify(y) ? "같음" : "변경";
    const display = v => v===null ? "미기재" : field==="secret_names" && Array.isArray(v) ? v.length+"개" : Array.isArray(v) ? v.join(", ") : typeof v==="object" ? JSON.stringify(v) : String(v);
    changes.push({label,status,left:display(x),right:display(y)});
  }
  return {a,b,changes};
}
export function skillSelectionView(deps,skillID,preferred) {
  const {el,state}=deps;
  const panel=el("details","skill-selection-compare"),inner=el("div");
  panel.append(el("summary","","승인 버전 비교"),inner);
  const owner=() => JSON.stringify([state.server,state.token,state.scope,state.view,state.selected,state.skillPage]);
  const origin=owner();
  let turn=0;
  const live=n=>n===turn && panel.open && panel.isConnected && origin===owner();
  const warn=()=>el("p","meta","선택한 버전과 최신 승인판을 확인할 수 없어요. 접근 권한과 버전 상태를 확인해 주세요.");
  async function open(){
    const n=++turn;
    inner.replaceChildren();
    if (!panel.open) return;
    inner.append(el("p","meta","버전 기록을 읽는 중…"));
    try {
      const skill=await deps.request("/v1/skills/"+encodeURIComponent(skillID)+"?"+deps.query());
      if (!live(n)) return;
      if (!matchesScope(skill,state.scope) || skill.id!==skillID || !Array.isArray(skill.version_history) || !skill.version_history.includes(skill.active_version) ||
          new Set(skill.version_history).size!==skill.version_history.length) fail();
      const pick=el("select"),label=el("label","field","최신 승인판과 비교할 버전"),result=el("div");
      label.append(pick);
      skill.version_history.forEach((id,i)=>{const op=el("option","","버전 "+(i+1)+(id===skill.active_version?" · 사용 중":""));op.value=String(i);pick.append(op);});
      const active=skill.version_history.indexOf(skill.active_version),selected=skill.version_history.indexOf(preferred);
      pick.value=String(selected>=0 ? selected : Math.max(0,active-1));
      inner.replaceChildren(label,result);
      const update=async()=>{
        const current=++turn;
        result.replaceChildren(el("p","meta","선택한 버전을 비교하는 중…"));
        try {
          const {a,b,changes}=await selectionEvidence(deps,skill,Number(pick.value),() => live(current));
          if (!live(current)) return;
          result.replaceChildren(el("p","meta","선택 "+a.version+"판 · 최신 승인 "+b.version+"판 · 본문 "+(a.hash===b.hash?"같음":"변경")));
          const meta=el("details");meta.append(el("summary","","분석·요구사항 "+changes.length+"개"));
          for(const c of changes) meta.append(el("p","meta",c.label+" · "+c.status+(c.status==="변경"||c.status==="한쪽 미기재" ? " · "+c.left+" → "+c.right : "")));
          result.append(meta);
          const known=a.files!==null && b.files!==null;
          const paths=[...new Set([...(a.files?.keys() || []),...(b.files?.keys() || [])])].sort(compareUTF8);
          result.append(el("p","meta",known ? "보조 파일 비교 · "+paths.length+"개 경로" : "파일 차이 확인 불가 · "+(!a.files&&!b.files?"양쪽":!a.files?"선택판":"최신 승인판")+" 명세 미기재"));
          if(!paths.length)return;
          const file=el("select"),fileLabel=el("label","field","검증할 보조 파일"),info=el("div");
          const change=path=>{
            const x=a.files?.get(path),y=b.files?.get(path);
            return !known?"확인 불가":!x?"추가":!y?"삭제":x.sha256!==y.sha256?"내용 변경":x.omitted!==y.omitted||x.size!==y.size?"제공 정보 변경":"같음";
          };
          paths.forEach(path=>{const op=el("option","",path+" · "+change(path));op.value=path;file.append(op);});
          file.value=paths.find(path=>change(path)!=="같음") || paths[0];
          fileLabel.append(file);result.append(fileLabel,info);
          const reasons={binary:"바이너리",too_large:"파일 크기",limit:"전체 용량",secret:"민감 정보"};
          const detail=()=>{info.replaceChildren();for(const [name,v] of [["선택판",a],["최신 승인판",b]]){
            const f=v.files?.get(file.value);
            info.append(el("p","meta",name+" · "+(f ? (f.omitted?"지문만 제공 ("+reasons[f.omitted]+")":"본문 포함")+" · "+f.size+"바이트 · SHA-256 "+f.sha256 : v.files?"목록에 없음":"명세 미기재")));
          }};
          file.addEventListener("change",detail);detail();
        }catch{if(live(current))result.replaceChildren(warn());}
      };
      pick.addEventListener("change",update);await update();
    }catch{if(live(n))inner.replaceChildren(warn());}
  }
  panel.addEventListener("toggle",open);
  return panel;
}
