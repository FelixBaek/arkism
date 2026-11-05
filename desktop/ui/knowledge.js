// TOOLS › 지식 (Figma 384:30031 자료 · 384:30671 부분 · 384:31269 위키).
// 자료 = uploaded knowledge sources (GET /v1/knowledge/sources, …/documents). A new version puts the source back
// to draft: a person reviews it before search sees it. 부분 = the current document's heading paths, its versions and
// a test search (POST /v1/knowledge/search). The 부분·이력 window lists the chunks (…/chunks: text for a person only,
// withheld text stays hidden) and the uploads (…/history), for a source and for a file-extraction document.
// 위키 = pages an AI drafted from sources (GET /v1/wiki/pages): draft → a person approves → reviewed; edited pages lock.
// Removing a source from the wiki asks the server for a dry-run preview first (…/remove?dry_run=1); a server that
// doesn't know it gets a plain confirm instead. Everything read here is data, never instructions.
window.ArkismKnowledge = (ctx) => {
  "use strict";
  const { state, $, el, button, icon, render, request, query, ago, detailShell, headExtras, confirmPop, saveFile: save } = ctx;
  const PROFILES = [["design_consultant", "디자인 상담"], ["illustrator", "일러스트"], ["listing_reviewer", "상품 검수"]];
  const SHARED = "default"; // the server's default scope: every agent of this person searches it
  const FORMATS = [["", "전체"], [".docx", "Word"], [".pdf", "PDF"], [".xlsx", "표 (.xlsx)"], [".csv", "표 (.csv)"], [".tsv", "표 (.tsv)"], [".pptx", "발표 자료"],
    [".html", "웹 페이지"], [".htm", "웹 페이지 (.htm)"], [".eml", "메일"], [".md", "Markdown"], [".txt", "텍스트"], [".json", "JSON"]];
  const OOXML = "application/vnd.openxmlformats-officedocument.";
  // The server's own types (document_formats.go): a file sent with any other type is refused.
  const MIME = { ".md": "text/markdown", ".txt": "text/plain", ".json": "application/json", ".docx": `${OOXML}wordprocessingml.document`, ".pdf": "application/pdf",
    ".xlsx": `${OOXML}spreadsheetml.sheet`, ".pptx": `${OOXML}presentationml.presentation`, ".html": "text/html", ".htm": "text/html", ".csv": "text/csv",
    ".tsv": "text/tab-separated-values", ".eml": "message/rfc822" };
  const TEXT = [".md", ".txt", ".json"]; // sent as text and split past 64KB; any other file goes up whole (4MB) and the server reads it
  const KINDS = "Word·PDF·표·발표 자료·웹 페이지·메일·Markdown·텍스트·JSON";
  const REJECT = {
    path_outside_wiki: "위키 밖 경로", path_identity_mismatch: "경로와 제목이 다름", forbidden_field: "금지된 머리말", unknown_attribute: "모르는 속성",
    not_planned: "계획에 없던 페이지", not_in_source: "원문에 없는 주제", type_mismatch: "종류가 다름", duplicate_block: "같은 블록 반복",
    secret_detected: "비밀값이 들어 있음", content_refused: "내용 거부", bad_body: "본문 형식 오류", unterminated_block: "끝나지 않은 블록"
  };
  const MERGE = { create: "새로 만듦", append: "덧붙임", "replace-section": "한 절 바꿈", rewrite: "다시 씀", human_edit: "사람이 고침" };
  const EXCLUDE = { superseded_version: "예전 버전", duplicate_chunk: "같은 부분", result_limit: "개수 제한", context_budget: "토큰 한도", no_text_match: "글자가 안 맞음", embedding_unavailable: "벡터 없음" };
  const JOB = { pending: "대기", running: "실행 중", done: "끝남", failed: "실패" };
  const K = {
    tab: "sources", state: "idle", error: "", sources: [], total: 0, facets: {}, wiki: "idle", pages: [], links: [], jobs: [],
    src: { q: "", status: "", sort: "recent", format: "" }, srcSel: "", docs: {},
    pg: { q: "", type: "", sort: "recent", chip: "all", folder: null }, pageSel: "", compare: false, edit: null,
    doc: "", docTab: "sections", secSel: 0, verSel: "", search: { profile: SHARED, q: "", busy: false, result: null, error: "" },
    busy: false, notice: "", remove: null, upload: null, extract: null,
    // 기억 문서: file-extraction documents (GET /v1/files/memories/documents); q and the dates filter what is loaded
    fd: { state: "idle", items: [], next: "", busy: false, error: "", q: "", tag: "", from: "", to: "", sel: "", del: null },
    folders: [], folderOf: {} // 위키 폴더 (GET /v1/wiki/tree); pg.folder null = every folder
  };
  let typing = 0;
  const loads = globalThis.ArkismKeep.latest(); // "sources" and "wiki": a newer read or reset() drops the older answer
  const live = () => state.view === "knowledge";
  // Last heading of "A › B / C": split, then trim beside each separator — the same pieces as /\s*[›>/]\s*/ without its backtracking.
  const leafOf = (path) => { const p = path.split(/[›>/]/); return p.map((x, i) => (i ? x.trimStart() : x)).map((x, i) => (i < p.length - 1 ? x.trimEnd() : x)).filter(Boolean).pop() || "(제목 없음)"; };
  const paint = () => { if (live()) render(false); };
  const get = (path, extra) => request(`${path}?${query(extra)}`);
  const post = (path, body = {}) => request(path, { method: "POST", body: JSON.stringify({ scope: state.scope, ...body }) });
  const off = (e) => /HTTP 404|access_denied|HTTP 403/.test(e?.message || "");
  // A plain sentence first; an unknown server code stays out of the main line.
  const errText = (e) => ({ revision_conflict: "그새 바뀌었어요. 새로고침했어요.", access_denied: "권한이 없어요. 사람만 할 수 있는 동작이에요.", secret_detected: "비밀값이 들어 있어 받지 않았어요.",
    invalid_request: "파일을 읽지 못했어요. 내용과 형식을 확인하고 다시 올려 주세요.", payload_too_large: "파일이 너무 커요.", artifact_too_large: "파일이 너무 커요.",
    unsupported_media_type: `이 형식은 읽지 못해요. 서버에 변환기를 연결하지 않았어요. ${KINDS} 파일로 저장해서 올려 주세요.`,
    no_extractable_text: "파일에서 글자를 찾지 못했어요. 스캔한 PDF나 암호가 걸린 PDF는 읽지 못해요." }[e?.message] || "잠시 문제가 생겼어요. 다시 시도해 주세요.");
  const noI18n = (n) => { n.dataset.noI18n = ""; return n; };
  const num = (n) => Number(n || 0).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");

  const sourceFilters = () => ({ q: K.src.q, state: K.src.status || "all", sort: K.src.sort, format: K.src.format });
  const sourcePages = ctx.remoteCollection((filters, page) => post("/v1/knowledge/sources/query", { ...filters, limit: page.limit, ...(page.cursor ? { cursor: page.cursor } : {}) }));
  let sourceBusy = false, sourceError = "";
  async function loadSources(more = false) {
    const ticket = loads.begin("sources"), filters = sourceFilters(); sourceBusy = true; sourceError = "";
    if (!more && !sourcePages.snapshot(filters).ready) { K.sources = []; K.total = 0; }
    paint();
    try {
      const page = await sourcePages.load(filters, more);
      if (!ticket.live() || !page) return;
      K.sources = page.items; K.total = page.total; K.facets = page.facets; K.state = "ready";
    } catch (e) { if (ticket.live()) { sourceError = e.message; if (!K.sources.length) K.state = off(e) ? "off" : "error"; K.error = e.message; } }
    if (ticket.live()) { sourceBusy = false; paint(); }
  }
  // ---------------------------------------------------------------- load
  async function load() {
    const ticket = loads.begin("wiki");
    K.state = K.state === "ready" ? "ready" : "loading"; paint();
    const [, p, g, j, t] = await Promise.allSettled([loadSources(), get("/v1/wiki/pages", { state: "all" }), get("/v1/wiki/graph"), get("/v1/wiki/jobs"), get("/v1/wiki/tree")]);
    if (!ticket.live()) return;
    if (p.status === "fulfilled" && Array.isArray(p.value?.items)) { K.pages = p.value.items; K.wiki = "ready"; }
    else { K.pages = []; K.wiki = p.status === "rejected" && off(p.reason) ? "off" : "error"; }
    K.links = g.status === "fulfilled" && Array.isArray(g.value?.links) ? g.value.links : [];
    K.jobs = j.status === "fulfilled" && Array.isArray(j.value?.items) ? j.value.items : [];
    const tree = t.status === "fulfilled" ? t.value : null; // an older server has no tree: no folders
    K.folders = Array.isArray(tree?.folders) ? tree.folders : [];
    K.folderOf = Object.fromEntries((Array.isArray(tree?.pages) ? tree.pages : []).map((x) => [x.id, x.folder || ""]));
    if (K.pg.folder && !K.folders.some((f) => f.path === K.pg.folder)) K.pg.folder = null;
    if (K.srcSel && !K.sources.some((x) => x.id === K.srcSel)) K.srcSel = "";
    if (K.pageSel && !K.pages.some((x) => x.id === K.pageSel)) K.pageSel = "";
    for (const id of Object.keys(K.docs)) if (!K.sources.some((x) => x.id === id)) delete K.docs[id];
    paint();
  }
  async function docs(id, again = false) {
    if (!id || (K.docs[id] && !again)) return;
    K.docs[id] = { state: "loading", items: K.docs[id]?.items || [] };
    try {
      const r = await get("/v1/knowledge/documents", { source: id });
      if (!Array.isArray(r?.items)) throw new Error("invalid_response");
      K.docs[id] = { state: "ready", items: r.items };
    } catch (e) { K.docs[id] = { state: "error", items: [], error: e.message }; }
    paint();
  }
  const current = (id) => K.docs[id]?.items.find((d) => d.current) || K.docs[id]?.items[0] || null;
  async function act(work, done, err = errText) {
    if (K.busy) return;
    K.busy = true; K.notice = ""; paint();
    try { const r = await work(); K.notice = typeof done === "function" ? done(r) : done || ""; }
    catch (e) { K.notice = err(e); }
    K.busy = false;
    await load();
  }

  // ---------------------------------------------------------------- states
  const srcState = (s) => s.lifecycle_status === "failed" ? ["실패", "amber"] : s.lifecycle_status === "draft" || s.trust_level !== "reviewed" ? ["확인 전", "amber"] : ["사용 중", ""];
  const needs = (s) => s.lifecycle_status === "failed" || s.lifecycle_status === "draft" || s.trust_level !== "reviewed";
  const profileName = (id) => (id === SHARED ? "모든 AI" : PROFILES.find(([k]) => k === id)?.[1] || id);
  const sharedBind = (s) => s.bindings.find((b) => b.specialist_profile_id === SHARED);
  const inUse = (s) => !needs(s) && sharedBind(s)?.lifecycle_status === "active_synthetic";
  const bindState = (b) => b.lifecycle_status === "active_synthetic" ? "켜짐" : b.lifecycle_status === "suspended" ? "꺼짐" : "후보";
  const sourceName = (id) => K.sources.find((s) => s.id === id)?.display_name || id;
  const pageState = (p) => p.review_state === "draft" ? ["초안", "amber"] : p.stale_sources?.length ? ["출처 바뀜", "amber"] : p.locked ? ["잠금", "line"]
    : p.review_state === "rejected" ? ["거절됨", ""] : p.review_state === "deleted" ? ["지워짐", ""] : ["검토됨", ""];
  const body = (p) => p.draft || p.reviewed || { title: p.slug, text: "", links: [] };
  const titleOf = (p) => (p.reviewed || p.draft)?.title || p.slug;
  const norm = (s) => String(s || "").trim().toLowerCase().replace(/\s+/g, "-");
  const pageByLink = (name) => K.pages.find((p) => p.review_state !== "deleted" && (norm(titleOf(p)) === norm(name) || p.slug === norm(name)));

  // ---------------------------------------------------------------- shared parts
  function attn(n, line, label, onClick) {
    const box = el("div", "v4s-attn");
    const t = el("div", "v4s-attn-text"), head = el("div", "v4s-attn-line");
    head.append("확인해 주세요", el("output", "", String(n)));
    t.append(head, el("p", "", line));
    box.append(t, button(label, "primary", null, onClick));
    return box;
  }
  function searchBox(placeholder, value, onInput) {
    const box = el("label", "v4s-search v4k-search");
    const input = el("input"); input.type = "search"; input.placeholder = placeholder; input.value = value; input.setAttribute("aria-label", placeholder);
    input.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(() => onInput(input.value.trim()), 250); });
    box.append(icon("magnifying-glass"), input);
    return box;
  }
  function select(label, opts, cur, pick) {
    const s = el("select", "v4s-select"); s.setAttribute("aria-label", label);
    for (const [k, l] of opts) { const o = el("option", "", l); o.value = k; o.selected = k === cur; s.append(o); }
    s.addEventListener("change", () => pick(s.value));
    return s;
  }
  function chips(list, cur, pick) {
    const box = el("div", "v4k-chips");
    for (const [k, label, n] of list) {
      const c = el("button", `chip${k === cur ? " active" : ""}`); c.type = "button";
      c.append(label); if (n !== undefined) c.append(el("output", "", String(n)));
      c.addEventListener("click", () => pick(k));
      box.append(c);
    }
    return box;
  }
  function row(cls, selected, kids, onOpen) {
    const r = el("div", `row v4s-row ${cls}${selected ? " selected" : ""}`);
    r.tabIndex = 0; r.setAttribute("role", "button"); if (selected) r.setAttribute("aria-current", "true");
    r.append(...kids);
    r.addEventListener("click", onOpen);
    r.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(); } });
    return r;
  }
  const text = (title, meta, mono) => { const t = el("div", "v4s-text"); t.append(noI18n(el("div", `v4s-title${mono ? " mono" : ""}`, title)), el("div", "v4s-meta", meta)); return t; };
  const badge = ([label, tone]) => el("span", `v4s-badge${tone ? ` ${tone}` : ""}`, label);
  const sec = (title, aside, ...kids) => {
    const s = el("section", "v4s-sec"), h = el("div", "v4s-sec-head");
    h.append(el("h3", "", title)); if (aside) h.append(aside);
    s.append(h, ...kids.filter(Boolean)); return s;
  };
  // Fixed values translate; a row marked raw (user or server data) stays as written.
  const facts = (rows) => { const dl = el("dl", "v4s-facts wide"); for (const [k, v, raw] of rows) if (v !== "" && v != null) { const dd = el("dd", "", String(v)); dl.append(el("dt", "", k), raw ? noI18n(dd) : dd); } return dl; };
  const link = (label, onClick) => { const b = el("button", "link-btn v4k-link", label); b.type = "button"; b.addEventListener("click", onClick); return b; };
  function card(textLine, buttons, note) {
    const c = el("div", "v4k-card");
    c.append(el("p", "", textLine));
    if (note) c.append(el("p", "v4k-card-note", note));
    if (buttons.length) { const a = el("div", "v4s-actions"); a.append(...buttons); c.append(a); }
    for (const b of c.querySelectorAll("button")) b.disabled = K.busy;
    return c;
  }
  const noticeLine = () => K.notice ? el("p", "ss-note v4k-notice", K.notice) : null;
  // [[링크]] in wiki text: accent spans, a page that doesn't exist yet stays dim.
  function wikiText(str) {
    const box = noI18n(el("div", "v4k-prose"));
    for (const para of String(str || "").split(/\n{2,}/)) {
      const p = el("p");
      for (const [i, part] of para.split(/\[\[([^\]\n]+)\]\]/).entries()) {
        if (i % 2 === 0) { if (part) p.append(part); continue; }
        const target = pageByLink(part);
        const a = el("button", `v4k-wlink${target ? "" : " missing"}`, `[[${part}]]`); a.type = "button";
        if (target) a.addEventListener("click", () => openPage(target.id)); else a.title = "아직 없는 페이지예요";
        p.append(a);
      }
      box.append(p);
    }
    return box;
  }

  // ---------------------------------------------------------------- 올리기 (new source or a new version)
  // The uploader's own check is the human review: one dialog asks it before anything is sent.
  function askUse(name, note) {
    return new Promise((resolve) => {
      const dlg = el("dialog", "cn-confirm v4k-ask"), form = el("form"), btns = el("div", "cn-confirm-btns");
      form.method = "dialog";
      const opt = (label, cls, value) => { const b = button(label, cls); b.type = "submit"; b.value = value; return b; };
      const no = opt("취소", "", "no");
      btns.append(no, opt("올리기만", "", "only"), opt("확인했어요 · 쓰게 하기", "primary", "use"));
      form.append(el("h2", "", "내 AI가 이 자료를 쓰게 할까요?"), noI18n(el("p", "", name)), ...(note ? [el("p", "", note)] : []),
        el("p", "", "내용이 맞는지 확인했다면 '쓰게 하기'를 눌러 주세요. 내 모든 AI가 이 자료를 찾아 써요. 나중에 자료 상세에서 끌 수 있어요."), btns);
      dlg.append(form);
      dlg.addEventListener("close", () => { resolve(["use", "only"].includes(dlg.returnValue) ? dlg.returnValue : ""); dlg.remove(); });
      document.body.append(dlg);
      dlg.showModal();
      no.focus();
    });
  }
  // Reviewed + bound to the shared scope + on: the same three server steps a person did by hand before.
  async function useEverywhere(id, revision, reviewed, bindings) {
    if (!reviewed) await post(`/v1/knowledge/sources/${encodeURIComponent(id)}/review`, { expected_revision: revision, status: "active", trust_level: "reviewed" });
    const b = bindings.find((x) => x.specialist_profile_id === SHARED) || await post("/v1/knowledge/bindings", { binding_key: `${id}:${SHARED}`, knowledge_source_id: id,
      specialist_profile_id: SHARED, specialist_profile_version_id: "v1", skill_id: `${SHARED}.v1`, trust_requirement: "reviewed", access_mode: "cite_required" });
    if ((b?.lifecycle_status || b?.knowledge?.lifecycle_status || b?.status) !== "active_synthetic") await post(`/v1/knowledge/bindings/${encodeURIComponent(b.id)}/state`, { expected_revision: b.revision, status: "active_synthetic" });
  }
  const b64 = (buf) => { let bin = ""; const u = new Uint8Array(buf); for (let i = 0; i < u.length; i += 0x8000) bin += String.fromCharCode(...u.subarray(i, i + 0x8000)); return btoa(bin); };
  // One file picker for both uploads: onFile gets the file and its lower-case extension. Any file may be picked:
  // a server with a converter reads more formats, and one without answers 415 (shown as a plain sentence).
  // many: onFile gets every picked file at once (the add window's 파일 tab).
  const extOf = (f) => (f.name.match(/\.[^.]+$/)?.[0] || "").toLowerCase();
  function chooseFile(onFile, many = false) {
    const input = el("input"); input.type = "file"; input.multiple = many;
    input.addEventListener("change", () => { const fs = [...(input.files || [])]; if (many) { if (fs.length) onFile(fs); } else if (fs[0]) onFile(fs[0], extOf(fs[0])); });
    input.click();
  }
  function pickFile(source) {
    chooseFile(async (f, ext) => {
      const whole = !TEXT.includes(ext);
      if (whole && f.size > 4 << 20) { K.notice = "파일이 너무 커요. 4MB까지 올릴 수 있어요."; paint(); return; }
      const data = whole ? { content_base64: b64(await f.arrayBuffer()) } : { content: await f.text() };
      const parts = whole ? [] : window.ArkismKnowledge.splitText(data.content);
      if (parts.length > 1) {
        // The server takes 64KB of text per file: a bigger text goes up as several sources, one per part.
        if (ext === ".json") { K.notice = "JSON 파일은 나누면 깨져서 64KB까지만 올릴 수 있어요."; paint(); return; }
        if (source) { K.notice = "새 버전은 64KB까지 올릴 수 있어요. 더 큰 파일은 '+ 파일 올리기'로 올리면 나눠서 올려요."; paint(); return; }
        const choice = await askUse(f.name, `64KB가 넘어서 문단 단위로 ${parts.length}개로 나눠 올려요.`);
        if (choice) uploadParts(f.name, ext, parts, choice);
        return;
      }
      const same = source || K.sources.find((s) => s.display_name === f.name);
      const choice = await askUse(f.name);
      if (!choice) return;
      if (same) delete K.docs[same.id];
      void act(async () => {
        const r = await post("/v1/knowledge/ingest", { source_key: same?.display_name || f.name, filename: f.name, mime_type: MIME[ext] || "application/octet-stream", ...data, synthetic: false, ...(same ? { expected_revision: same.revision } : {}) });
        const src = r?.source;
        if (choice === "use" && src?.id) await useEverywhere(src.id, src.revision, r.duplicate && same && !needs(same), same?.bindings || []);
        return r;
      }, (r) => choice === "use" ? (r?.duplicate ? "같은 내용이에요. 이제 내 모든 AI가 이 자료를 찾아 써요." : "올렸어요. 이제 내 모든 AI가 이 자료를 찾아 써요.")
        : r?.duplicate ? "같은 내용이라 새 버전을 만들지 않았어요." : "올렸어요. 자료를 열어 확인하고 '쓰게 하기'를 눌러 주세요.");
    });
  }
  // One progress line while the parts go up; a part that fails does not stop the rest, and the end says which failed.
  // A name holds no "/" (the server refuses it), so "(1／3)" uses the full-width slash.
  function uploadParts(name, ext, parts, choice) {
    const base = name.slice(0, name.length - ext.length), n = parts.length;
    void act(async () => {
      const failed = [];
      for (const [i, content] of parts.entries()) {
        const part = `${base} (${i + 1}／${n})${ext}`, same = K.sources.find((s) => s.display_name === part);
        K.notice = `나눠서 올리는 중… ${i + 1}/${n}`; paint();
        try {
          if (same) delete K.docs[same.id];
          const r = await post("/v1/knowledge/ingest", { source_key: part, filename: part, mime_type: MIME[ext], content, synthetic: false, ...(same ? { expected_revision: same.revision } : {}) });
          if (choice === "use" && r?.source?.id) await useEverywhere(r.source.id, r.source.revision, r.duplicate && same && !needs(same), same?.bindings || []);
        } catch (e) { if (failed.length === n - 1) throw e; failed.push([i + 1, errText(e)]); }
      }
      return failed;
    }, (failed) => failed.length ? `${n}개 중 ${n - failed.length}개를 올렸어요. 못 올린 부분은 ${failed.map(([i]) => i).join(", ")}번째예요. ${failed[0][1]} 고친 뒤 같은 파일을 다시 올리면 바뀐 부분만 새로 올라가요.`
      : choice === "use" ? `${n}개로 나눠 올렸어요. 이제 내 모든 AI가 이 자료를 찾아 써요.` : `${n}개로 나눠 올렸어요. 자료를 열어 확인하고 '쓰게 하기'를 눌러 주세요.`);
  }

  // ---------------------------------------------------------------- 파일 → 기억 후보 (POST /v1/files/memories, …/extract_memories)
  // The server only proposes: every claim lands as a memory candidate a person approves in 확인해 주세요.
  // A big file runs as a job polled here (GET /v1/files/memories/jobs/{id}); 멈추기 asks /v1/operations/{id}/cancel (people only).
  // A failed job is resumed by its uploader through …/jobs/{id}/run (the operations retry route refuses this kind).
  const extractErr = (e) => ({ model_not_configured: "서버에 기억을 뽑을 모델이 없어요. 서버 설정에서 모델을 연결해 주세요.",
    model_budget_exceeded: "오늘 쓸 수 있는 모델 호출을 다 썼어요. 내일 다시 해 주세요.", candidate_daily_limit: "오늘 만들 수 있는 후보 수를 넘었어요. 내일 다시 해 주세요." }[e?.message] || errText(e));
  let extractTimer = 0;
  function showExtract(r, label) {
    const prev = K.extract?.id === (r?.id || r?.job_id) ? K.extract : null;
    const next = window.ArkismKnowledge.extractState(r);
    K.extract = { ...next, label: label || prev?.label || "", error: prev?.phase === next.phase ? prev.error || "" : "" }; // a refused cancel stays readable while the job runs
    clearTimeout(extractTimer);
    if (K.extract.active && K.extract.id) extractTimer = setTimeout(pollExtract, 1500);
    paint();
  }
  async function pollExtract() {
    const x = K.extract;
    if (!x?.id || !x.active) return;
    try { showExtract(await get(`/v1/files/memories/jobs/${encodeURIComponent(x.id)}`)); }
    catch (e) { if (K.extract === x) { x.error = extractErr(e); x.active = false; paint(); } }
  }
  async function startExtract(work, label) {
    if (K.busy) return;
    K.busy = true; K.notice = ""; K.extract = { label, phase: "queued", active: true, total: 0, next: 0, created: 0, skipped: 0 }; paint();
    try { showExtract(await work(), label); } catch (e) { K.extract = { label, phase: "failed", active: false, error: extractErr(e) }; }
    K.busy = false; K.fd.state = "idle"; paint(); // 기억 문서 reads again
  }
  const human = () => state.caps?.actions?.review_decide !== false;
  async function cancelExtract() {
    const x = K.extract;
    if (!x?.id || !(await confirmPop("기억 후보 만들기를 멈출까요?", "이미 만든 후보는 그대로 남아요.", "멈추기"))) return;
    try { await post(`/v1/operations/${encodeURIComponent(x.id)}/cancel`, x.revision ? { expected_revision: x.revision } : {}); await pollExtract(); }
    catch (e) { x.error = e?.message === "revision_conflict" ? "그사이 작업이 바뀌었어요. 다시 눌러 주세요." : extractErr(e); paint(); }
  }
  // The run route works through the rest of the job before it answers, so the card polls meanwhile.
  function resumeExtract() {
    const x = K.extract;
    if (!x?.id) return;
    Object.assign(x, { phase: "running", active: true, error: "", jobError: "" }); paint();
    post(`/v1/files/memories/jobs/${encodeURIComponent(x.id)}/run`).then((r) => { if (K.extract?.id === x.id) showExtract(r); },
      (e) => { if (K.extract?.id === x.id) { K.extract.error = extractErr(e); K.extract.active = false; clearTimeout(extractTimer); paint(); } });
    clearTimeout(extractTimer); extractTimer = setTimeout(pollExtract, 1500);
  }
  const extractFromSource = (s) => startExtract(() => post(`/v1/knowledge/sources/${encodeURIComponent(s.id)}/extract_memories`, {}), s.display_name);
  const EXTRACT_WORD = { queued: "기다리는 중", running: "읽는 중", done: "끝났어요", cancelled: "멈췄어요", failed: "멈췄어요" };
  function extractCard() {
    const x = K.extract;
    if (!x) return null;
    const c = el("div", "v4k-card v4k-extract");
    const head = el("p"); head.append(el("strong", "", "기억 후보 만들기"), " · ", EXTRACT_WORD[x.phase] || EXTRACT_WORD.queued);
    c.append(head);
    if (x.label) c.append(noI18n(el("p", "v4k-card-note mono", x.label)));
    if (x.active && x.total) { const bar = el("progress", "v4k-progress"); bar.max = x.total; bar.value = x.next; bar.setAttribute("aria-label", "진행"); c.append(bar, el("p", "v4k-card-note", `${num(x.total)}부분 중 ${num(x.next)}부분 읽음`)); }
    if (x.active && !x.total) c.append(el("p", "v4k-card-note", "파일을 읽고 있어요…"));
    if (!x.active && !x.error) c.append(el("p", "", x.created ? `기억 후보 ${num(x.created)}개를 만들었어요. 저장할지는 내가 골라요.` : "새로 만든 후보가 없어요."));
    if (x.delta) c.append(el("p", "v4k-card-note", deltaLine(x.delta)));
    if (x.delta?.left) c.append(el("p", "v4k-card-note", `빠진 부분에서 나온 기억 후보 ${num(x.delta.left)}개는 그대로 남아 있어요. 필요 없으면 '확인해 주세요'에서 버려 주세요.`));
    if (x.error || x.jobError) c.append(el("p", "v4k-warn", x.error || extractErr({ message: x.jobError })));
    const a = el("div", "v4s-actions");
    if ((x.created || x.delta?.left) && !x.active) a.append(button("후보 확인하러 가기", "primary", null, () => ctx.go?.("memories", "candidate")));
    if (x.active && x.id && human()) a.append(button("멈추기", "ghost", null, cancelExtract));
    if (!x.active && x.id && x.phase === "failed") a.append(button("이어서 하기", "primary", null, resumeExtract));
    if (!x.active && x.doc) a.append(button("부분·이력 보기", "", null, () => openParts({ title: x.label, doc: x.doc })));
    if (!x.active) a.append(link("닫기", () => { K.extract = null; paint(); }));
    c.append(a);
    if (x.skipped) { const more = el("details", "v4k-more"); more.append(el("summary", "", "자세히"), el("p", "meta", `건너뛴 부분 ${num(x.skipped)}곳 (비공개·비밀값이거나 읽을 수 없는 부분)`)); c.append(more); }
    return c;
  }

  // ---------------------------------------------------------------- 부분·이력 창 (GET …/chunks, …/history)
  // One window for a knowledge source (source) or a file-extraction document (doc): 부분 lists the chunks with their hash,
  // place and state (a document's chunks also with the memory candidates each made); 이력 lists the versions or uploads
  // with what changed. Pages of 50 load on demand. Everything here is data, drawn as plain text.
  const CHUNK = { unchanged: "그대로", extracted: "읽음", pending: "아직 안 읽음", active: "사용 중", draft: "확인 전", quarantined: "격리됨", erased: "지워짐", deleted: "지워짐" };
  const CAND = { candidate: "확인 대기", conflicted: "확인 대기", approved: "승인됨", active: "승인됨", rejected: "버림", superseded: "새 기억으로 바뀜", gone: "없어짐" };
  const UPLOAD = { pending: "기다리는 중", retry: "기다리는 중", running: "읽는 중", done: "끝남", failed: "실패", cancelled: "멈춤" };
  const ARTIFACT = { code: "코드", ocr: "글자 인식", tool_output: "도구 결과", pdf: "PDF", image: "이미지", screenshot_region: "화면 일부", audio: "소리" };
  const EXT = { "text/plain": ".txt", "application/json": ".json", "application/pdf": ".pdf", "image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "audio/mpeg": ".mp3", "audio/wav": ".wav", "audio/ogg": ".ogg" };
  const bytes = (n) => (n >= 1 << 20 ? `${(n / 1048576).toFixed(1)}MB` : `${Math.max(1, Math.round((n || 0) / 1024))}KB`);
  const deltaLine = (d) => `전체 ${num(d.total)} · 그대로 ${num(d.unchanged)} · 바뀜 ${num(d.changed)} · 빠짐 ${num(d.removed)}`;
  function openParts({ title, source, doc, initial }) {
    const base = source ? `/v1/knowledge/sources/${encodeURIComponent(source)}` : `/v1/files/memories/documents/${encodeURIComponent(doc)}`;
    const L = { chunks: { items: [], next: "", busy: false, error: "", read: false }, history: { items: [], next: "", busy: false, error: "", read: false } };
    let extract = "", pop = null, open = "chunks";
    const redraw = () => { if (pop?.dialog.isConnected) pop.select(open); };
    async function more(key) {
      const P = L[key];
      if (P.busy) return;
      P.busy = true; P.error = ""; redraw();
      try {
        const r = await get(`${base}/${key}`, { limit: 50, ...(P.next ? { cursor: P.next } : {}) });
        if (!Array.isArray(r?.items)) throw new Error("invalid_response");
        P.items.push(...r.items); P.next = r.next_cursor || ""; P.total = r.total;
        if (r.extract_document_id) extract = r.extract_document_id;
      } catch (e) { P.error = errText(e); }
      P.busy = false; P.read = true; redraw();
    }
    // head: a name or place drawn as is, or a node of its own (a document chunk's 쪽 is translated)
    const item = (head, meta, ...kids) => { const c = el("article", "v4k-ev"), top = el("div", "v4k-ev-top"); top.append(typeof head === "string" ? noI18n(el("span", "mono", head)) : head, el("small", "", meta.filter(Boolean).join(" · "))); c.append(top, ...kids.filter(Boolean)); return c; };
    const chunk = (x) => source
      ? item(`#${x.chunk_index + 1}${x.section_path ? ` · ${x.section_path}` : ""}`, [CHUNK[x.status] || x.status, `${num(x.token_estimate)} 토큰`, x.hash?.slice(0, 12)],
        x.text_withheld ? el("p", "meta", "내용을 가렸어요. 비공개·격리된 부분이거나 사람만 볼 수 있는 내용이에요.") : noI18n(el("p", "", x.text)))
      : item(place(x.locator), [CHUNK[x.status] || x.status, x.hash?.slice(0, 12)], candidates(x.candidates || []));
    // the server's locator ("chunk:3", "page:2/chunk:1") in plain words: #3, 2쪽 #1
    function place(loc = "") {
      const p = /page:(\d+)/.exec(loc), c = /chunk:(\d+)/.exec(loc);
      if (!c) return loc;
      const head = el("span", "mono");
      if (p) head.append(el("span", "", `${p[1]}쪽`), " ");
      head.append(noI18n(el("span", "", `#${c[1]}`)));
      return head;
    }
    // A candidate that became a memory opens that memory (its 바뀐 이력 shows every revision); a waiting one opens the review list.
    function candidates(list) {
      if (!list.length) return el("p", "meta", "이 부분에서 만든 기억 후보가 없어요.");
      const box = el("div", "v4k-chips");
      for (const [i, c] of list.entries()) {
        const b = el("button", `v4s-badge v4k-cand${c.status === "candidate" || c.status === "conflicted" ? " amber" : ""}`, `후보 ${i + 1} · ${CAND[c.status] || c.status}`); b.type = "button";
        if (c.memory_id) { b.title = "이 기억과 바뀐 이력 보기"; b.addEventListener("click", () => { pop.dialog.close(); ctx.openMemory?.(c.memory_id); }); }
        else if (c.status === "candidate" || c.status === "conflicted") { b.title = "확인해 주세요에서 보기"; b.addEventListener("click", () => { pop.dialog.close(); ctx.go?.("memories", "candidate"); }); }
        else b.disabled = true;
        box.append(b);
      }
      return box;
    }
    // 원본 파일: the files the server kept for this document's evidence (GET /v1/sources/{id}/artifacts; only with
    // MEMORY_FILE_KEEP_ORIGINALS on). Each downloads through GET …/artifacts/{artifact_id}.
    const kept = {}; // source id → { items, error } once read
    function originals(body) {
      open = "files";
      if (!L.chunks.read && !L.chunks.busy) void more("chunks");
      const ids = [...new Set(L.chunks.items.map((x) => x.source?.id).filter(Boolean))];
      for (const id of ids) if (!kept[id]) {
        kept[id] = { items: null, error: "" };
        get(`/v1/sources/${encodeURIComponent(id)}/artifacts`, { limit: 100 }).then((r) => { kept[id].items = Array.isArray(r?.items) ? r.items : []; },
          (e) => { kept[id].error = off(e) ? "원본 파일을 볼 권한이 없거나 남아 있지 않아요." : errText(e); kept[id].items = []; }).then(redraw);
      }
      const list = el("div", "v4k-evidence");
      const files = ids.flatMap((id) => (kept[id].items || []).map((a) => [id, a]));
      for (const [id, a] of files) {
        const c = item(a.id, [ARTIFACT[a.kind] || a.kind, a.mime, bytes(a.bytes)]);
        const acts = el("div", "v4s-actions"), dl = button("내려받기", "", null, () => download(id, a, dl));
        acts.append(dl); c.append(acts); list.append(c);
      }
      const waiting = L.chunks.busy || ids.some((id) => !kept[id].items);
      if (waiting) list.append(el("p", "meta", "불러오는 중…"));
      for (const id of ids) if (kept[id].error) list.append(el("p", "v4k-warn", kept[id].error));
      if (!waiting && !files.length) list.append(el("div", "empty", "보관한 원본 파일이 없어요. 서버에서 원본 보관(MEMORY_FILE_KEEP_ORIGINALS)을 켜야 올린 파일이 남아요."));
      body.append(list);
    }
    async function download(id, a, b) {
      b.disabled = true;
      try { save(`${a.id}${EXT[a.mime] || ""}`, await request(`/v1/sources/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(a.id)}?${query()}`, { binary: true }), a.mime); b.textContent = "내려받았어요"; }
      catch (e) { b.textContent = e?.message === "payload_too_large" ? "파일이 너무 커요" : "내려받지 못했어요"; b.disabled = false; }
    }
    const version = (x) => source
      ? item(x.filename, [x.current ? "지금 버전" : "예전 버전", x.extraction_status === "failed" ? "읽기 실패" : `부분 ${num(x.chunk_count)}개`, ago(x.created_at)])
      : item(x.label || title, [UPLOAD[x.status] || x.status, ago(x.created_at), `기억 후보 ${num(x.candidates_created)}개`],
        window.ArkismKnowledge.delta(x.delta) ? el("p", "v4k-card-note", deltaLine(window.ArkismKnowledge.delta(x.delta))) : null,
        x.delta?.removed_sources?.length ? el("p", "v4k-card-note", `빠진 부분 ${num(x.delta.removed_sources.length)}곳 · 그 후보는 그대로 남아 있어요`) : null);
    const tab = (key, row, empty) => (body) => {
      open = key;
      const P = L[key];
      if (!P.read && !P.busy) void more(key);
      const list = el("div", "v4k-evidence");
      list.append(...P.items.map(row));
      if (P.read && !P.items.length && !P.error) list.append(el("div", "empty", empty));
      if (P.busy) list.append(el("p", "meta", "불러오는 중…"));
      if (P.error) list.append(el("p", "v4k-warn", P.error));
      body.append(list);
      const acts = el("div", "v4s-actions");
      if (P.next || P.error) { const b = button(P.error ? "다시 시도" : "더 보기", "", null, () => more(key)); b.disabled = P.busy; acts.append(b); }
      if (key === "chunks" && extract) acts.append(button("이 자료로 만든 기억 후보 보기", "", null, () => { pop.dialog.close(); openParts({ title, doc: extract }); }));
      if (acts.childElementCount) body.append(acts);
    };
    const sub = source ? "검색에 쓰는 부분과 올린 버전이에요. 내용은 사람에게만 보여요." : "파일에서 읽은 부분과 그 부분에서 나온 기억 후보예요. 후보는 '확인해 주세요'에서 승인해야 쓰여요.";
    pop = window.ArkismCrewPopup({ el, button }).open({ title, sub, cls: "v4k-parts", initial, tabs: [
      { key: "chunks", label: "부분", render: tab("chunks", chunk, "부분이 없어요.") },
      { key: "history", label: source ? "버전" : "올린 이력", render: tab("history", version, "이력이 없어요.") },
      ...(doc ? [{ key: "files", label: "원본 파일", render: originals }] : [])] });
    noI18n(pop.dialog.querySelector("h2"));
  }

  // ---------------------------------------------------------------- 자료 (384:30031)
  function renderSources(table) {
    const parts = [];
    const ex = extractCard(); if (ex) parts.push(ex);
    const pending = K.sources.filter((s) => s.lifecycle_status === "draft" || (s.lifecycle_status !== "failed" && s.trust_level !== "reviewed")).length;
    const failed = K.sources.filter((s) => s.lifecycle_status === "failed").length;
    if (pending + failed) parts.push(attn(pending + failed, [pending && `확인 전 ${pending}`, failed && `읽기 실패 ${failed}`].filter(Boolean).join(" · "), "하나씩 확인하기",
      () => { const s = K.sources.find(needs); if (s) openSource(s.id); }));
    const bar = el("div", "v4s-bar");
    bar.append(searchBox("문서·목차 검색", K.src.q, (v) => { K.src.q = v; void loadSources(); }),
      select("상태", [["", "상태: 모두"], ["검토 대기", "확인 전"], ["사용 중", "사용 중"], ["실패", "실패"]], K.src.status, (v) => { K.src.status = v; void loadSources(); }),
      select("정렬", [["recent", "최근 올린 순"], ["name", "이름 순"], ["chunks", "긴 자료 순"]], K.src.sort, (v) => { K.src.sort = v; void loadSources(); }));
    parts.push(bar);
    if (noticeLine()) parts.push(noticeLine());
    const cols = el("div", "v4k-cols");
    const side = el("div", "v4k-groups");
    side.append(el("p", "v4k-label", "자료"));
    for (const [k, label] of FORMATS) {
      // an older server sends no facets: 전체 falls back to the total
      const n = k ? K.facets.kinds?.[k] || 0 : K.facets.kinds ? Object.values(K.facets.kinds).reduce((a,b) => a+b,0) : K.total || 0;
      if (k && !n) continue;
      const g = el("button", `v4k-group${K.src.format === k ? " on" : ""}`); g.type = "button";
      const t = el("span", "v4k-group-text");
      t.append(el("b", "", label), el("small", "", k ? `파일 ${n}개` : "모든 형식"));
      g.append(t, el("output", "", String(n)));
      g.addEventListener("click", () => { K.src.format = k; void loadSources(); });
      side.append(g);
    }
    side.append(el("p", "v4k-kinds", `올릴 수 있는 파일: ${KINDS}`));
    const main = el("div", "v4k-main");
    const items = K.sources;
    const list = el("div", "v4s-list");
    const sourceRow = (s) => {
      const meta = [s.lifecycle_status === "failed" ? "읽기 실패" : `버전 ${s.document_count}`, `${ago(s.updated_at)} 올림`];
      if (inUse(s)) meta.push("모든 AI가 씀");
      else if (!needs(s) && s.lifecycle_status !== "failed") meta.push("AI가 안 씀");
      const node = row("v4k-row", K.srcSel === s.id, [text(s.display_name, meta.join(" · "), true), badge(inUse(s) || needs(s) ? srcState(s) : ["꺼짐", "off"])], () => openSource(s.id)); node.dataset.id = s.id; return node;
    };
    ctx.virtualList.mount({ container: list, scrollport: $("#table"), items, row: sourceRow, key: JSON.stringify(sourceFilters()) });
    if (!items.length) {
      const filtered = K.src.q || K.src.status || K.src.format, box = el("div", "empty");
      box.append(el("p", "", filtered ? "조건에 맞는 자료가 없어요." : `아직 올린 자료가 없어요. ${KINDS} 파일을 올리면 내 AI가 찾아 써요.`),
        filtered ? button("조건 지우기", "", null, () => { Object.assign(K.src, { q: "", status: "", format: "" }); void loadSources(); }) : button("파일 올리기", "primary", null, () => pickFile(null)));
      list.append(box);
    }
    main.append(list, el("p", "v4s-foot", sourceBusy ? "불러오는 중…" : `전체 ${K.total}개 중 ${items.length}개 표시 · 전체에서 검색·정렬`));
    if (sourceError) main.append(el("p", "ss-note", "자료를 더 불러오지 못했어요."));
    if (sourcePages.snapshot(sourceFilters()).cursor || sourceError) { const b = button(sourceError ? "다시 시도" : "자료 더 보기", "", null, () => loadSources(true)); b.disabled = sourceBusy; main.append(b); }
    cols.append(side, main);
    parts.push(cols);
    table.replaceChildren(...parts);
    if (!K.srcSel && items[0]) K.srcSel = items[0].id;
    sourceDetail();
  }
  function openSource(id) { K.srcSel = id; K.remove = null; K.notice = ""; paint(); }

  function sourceDetail() {
    const s = K.sources.find((x) => x.id === K.srcSel);
    if (!s) { $("#detail").replaceChildren(); return; }
    void docs(s.id);
    const d = current(s.id);
    const box = detailShell(`문서 · ${s.format || "파일"}`);
    const head = el("div", "v4s-head");
    head.append(noI18n(el("h2", "mono v4k-h2", s.display_name)), el("p", "v4s-lead", [`버전 ${s.document_count}`, `${ago(s.updated_at)} 올림`].join(" · ")));
    box.append(head);
    const use = (reviewed) => act(() => useEverywhere(s.id, s.revision, reviewed, s.bindings), "이제 내 모든 AI가 이 자료를 찾아 써요.");
    // action card first
    if (s.lifecycle_status === "failed") box.append(card("파일에서 글을 읽지 못했어요. 파일을 고쳐 다시 올려 주세요.", [button("다시 올리기", "primary", null, () => pickFile(s))]));
    else if (needs(s)) box.append(card(s.document_count > 1 ? "새 버전이 올라왔어요. 내용을 확인하기 전까지 AI가 쓰지 않아요." : "아직 AI가 쓰지 않는 자료예요. 내용이 맞는지 확인했다면 쓰게 해 주세요.",
      [button("확인했어요 · 내 AI가 쓰게 하기", "primary", null, () => use(false)),
        button("내용 보기", "", null, () => openDoc(s.id)), link("나중에", () => { const n = K.sources.find((x) => x.id !== s.id && needs(x)); if (n) openSource(n.id); })]));
    // 내 AI가 쓰기: one switch for every agent
    if (!needs(s)) {
      const sb = sharedBind(s), on = inUse(s);
      const r = el("div", "v4k-bind");
      r.append(el("span", "", on ? "내 모든 AI가 이 자료를 찾아 써요." : "지금은 AI가 이 자료를 쓰지 않아요."),
        button(on ? "그만 쓰게 하기" : "쓰게 하기", on ? "ghost" : "primary", null, () => on
          ? act(() => post(`/v1/knowledge/bindings/${encodeURIComponent(sb.id)}/state`, { expected_revision: sb.revision, status: "suspended" }), "이제 AI가 이 자료를 쓰지 않아요.")
          : use(true)));
      r.lastChild.disabled = K.busy;
      box.append(sec("내 AI가 쓰기", null, r));
    }
    // 내용: the headings search uses
    const secs = d?.sections || [];
    const chunkList = el("div", "v4k-chunks");
    if (K.docs[s.id]?.state === "loading" && !secs.length) chunkList.append(el("p", "meta", "불러오는 중…"));
    else if (!secs.length) chunkList.append(el("p", "meta", "목차가 없어요."));
    secs.slice(0, 3).forEach((path, i) => {
      const c = el("button", "v4k-chunk"); c.type = "button";
      c.append(el("small", "", `#${i + 1}`), noI18n(el("span", "", path || "(제목 없음)")));
      c.addEventListener("click", () => { openDoc(s.id); K.secSel = i; paint(); });
      chunkList.append(c);
    });
    box.append(sec("내용", s.chunk_count ? link(`검색에 쓰는 부분 ${num(s.chunk_count)}개 보기`, () => openDoc(s.id)) : null, chunkList));
    // 위키
    box.append(wikiSection(s));
    box.append(sec("정보", null, facts([["형식", s.format], ["상태", srcState(s)[0]], ["확인한 사람", s.approved_by || "", true], ["올린 때", ago(s.created_at)]])));
    const tools = el("div", "v4s-actions");
    const upload = button("새 버전 올리기", "", null, () => pickFile(s)), extract = button("이 자료로 기억 후보 만들기", "", null, () => extractFromSource(s));
    upload.disabled = K.busy; extract.disabled = K.busy || !!K.extract?.active; // 부분·이력 only reads, so it stays open
    tools.append(upload, extract, button("부분·이력 보기", "", null, () => openParts({ title: s.display_name, source: s.id })));
    box.append(sec("관리", null, tools, noticeLine(), el("p", "meta", "AI는 자료를 지시가 아닌 참고로만 읽어요.")));
    // 자세히: specialist bindings and index facts for people who need them
    const more = el("details", "v4k-more");
    more.append(el("summary", "", "자세히"));
    const binds = el("div", "v4k-binds");
    binds.append(el("p", "meta", "특정 전문가에게만 따로 켤 수도 있어요."));
    for (const b of s.bindings.filter((x) => x.specialist_profile_id !== SHARED)) {
      const on = b.lifecycle_status === "active_synthetic";
      const r = el("div", "v4k-bind");
      r.append(el("span", "", profileName(b.specialist_profile_id)), el("small", "", bindState(b)),
        button(on ? "끄기" : "켜기", on ? "ghost" : "", null, () => act(() => post(`/v1/knowledge/bindings/${encodeURIComponent(b.id)}/state`, { expected_revision: b.revision, status: on ? "suspended" : "active_synthetic" }), on ? "껐어요." : "켰어요. 이 전문가가 검색할 때 써요.")));
      if (!on && needs(s)) { r.lastChild.disabled = true; r.lastChild.title = "확인한 자료만 켤 수 있어요"; }
      else r.lastChild.disabled = K.busy;
      binds.append(r);
    }
    const free = PROFILES.filter(([k]) => !s.bindings.some((b) => b.specialist_profile_id === k));
    if (free.length) {
      const add = el("div", "v4k-bind add");
      const pick = select("전문가", free, free[0][0], () => {});
      add.append(pick, button("연결", "", null, () => act(() => post("/v1/knowledge/bindings", { binding_key: `${s.id}:${pick.value}`, knowledge_source_id: s.id, specialist_profile_id: pick.value, specialist_profile_version_id: "v1",
        skill_id: `${pick.value}.v1`, trust_requirement: "reviewed", access_mode: "cite_required" }), "연결했어요. 켜야 검색에 써요.")));
      add.lastChild.disabled = K.busy;
      binds.append(add);
    }
    const idx = el("div", "v4s-actions");
    idx.append(button("벡터 색인", "ghost", null, () => act(() => post("/v1/knowledge/index", { knowledge_source_id: s.id }),
      (r) => `색인했어요 (새로 ${r?.embedded ?? 0} · 그대로 ${r?.reused ?? 0} · 실패 ${r?.failed ?? 0}).`)));
    idx.lastChild.disabled = K.busy;
    more.append(sec("전문가 연결", null, binds), facts([["검색에 쓰는 부분", `${num(s.chunk_count)}개`], ["색인", d ? `${num(d.indexed_chunks)} / ${num(d.chunk_count)}` : ""], ["나누는 법", d ? `제목 기준 · ${d.chunker_version}` : ""],
      ["읽은 오류", d?.error_code || "", true]]), idx);
    box.append(more);
  }

  function wikiSection(s) {
    const jobs = K.jobs.filter((j) => j.job?.knowledge_source_id === s.id).sort((a, b) => String(b.updated_at).localeCompare(String(a.updated_at)));
    const last = jobs[0];
    const kids = [];
    const pages = K.pages.filter((p) => p.knowledge_source_ids?.includes(s.id) && p.review_state !== "deleted");
    kids.push(el("p", "meta", "위키는 AI가 자료를 주제별로 정리한 글이에요. 검토하면 검색에도 쓰여요."));
    kids.push(el("p", "meta", pages.length ? `이 자료에서 나온 위키 ${pages.length}쪽` : "아직 이 자료로 만든 위키가 없어요."));
    const more = el("details", "v4k-more");
    more.append(el("summary", "", "자세히"));
    if (last) {
      const j = last.job || {}, r = j.result;
      kids.push(el("p", "meta", `마지막 작업: ${JOB[last.status] || last.status} · ${ago(last.updated_at)}`));
      if (j.error) kids.push(el("p", "meta", j.error === "wiki_budget_exceeded" ? "오늘 쓸 수 있는 모델 호출을 다 썼어요. 내일 다시 해 주세요." : j.error === "revision_conflict" ? "자료가 바뀌어 멈췄어요. 다시 만들어 주세요." : "만들지 못했어요. 다시 시도해 주세요."));
      const line = [`호출 ${j.calls || 0}번`, `토큰 ${num(j.tokens)}`, j.generator?.model, j.error && !["wiki_budget_exceeded", "revision_conflict"].includes(j.error) ? j.error : ""].filter(Boolean).join(" · ");
      const box = el("div", "v4k-job");
      box.append(el("p", "v4k-job-line", line)); // fixed words translate piece by piece; the model id has no Hangul
      if (r) {
        box.append(el("p", "meta", `새 ${r.created?.length || 0} · 고침 ${r.updated?.length || 0} · 그대로 ${r.unchanged?.length || 0} · 잠겨서 건너뜀 ${r.skipped_locked?.length || 0} · 거부 ${r.rejected?.length || 0}`));
        if ((r.rejected || []).length) {
          const ul = el("ul", "v4k-rejects");
          for (const x of r.rejected.slice(0, 6)) { const li = el("li"); li.append(noI18n(el("span", "mono", x.path)), " · ", REJECT[x.reason] || x.reason); ul.append(li); }
          box.append(ul);
          if (r.rejected.length >= 3) kids.push(el("p", "v4k-warn", "AI가 쓴 글을 여러 번 거부했어요. 자료에 AI를 속이려는 문장이 있을 수 있어요."));
        }
      }
      more.append(box);
    }
    const acts = el("div", "v4s-actions");
    const cap = el("input", "v4s-input v4k-cap"); cap.type = "number"; cap.min = "1000"; cap.max = "120000"; cap.step = "1000"; cap.placeholder = "40000"; cap.setAttribute("aria-label", "토큰 상한");
    acts.append(button("위키 만들기", "", null, () => act(() => post("/v1/wiki/jobs", { knowledge_source_id: s.id, ...(cap.value ? { max_total_tokens: Number(cap.value) } : {}) }),
      (r) => r?.duplicate ? "이 버전으로 만든 작업이 이미 있어요." : "위키 작업을 대기열에 넣었어요.")));
    const capRow = el("label", "v4k-caprow");
    capRow.append(el("span", "meta", "한 번에 쓸 토큰 (비우면 기본값)"), cap);
    more.append(capRow);
    if (last && ["pending", "failed"].includes(last.status)) acts.append(button("지금 실행", "ghost", null, () => act(() => post(`/v1/wiki/jobs/${encodeURIComponent(last.id)}/run`), "실행했어요. 초안은 위키 탭에서 검토해요.")));
    acts.append(button("위키에서 빼기", "ghost danger", null, () => previewRemove(s)));
    for (const b of acts.querySelectorAll("button")) b.disabled = K.busy || (needs(s) && b.textContent === "위키 만들기");
    if (needs(s)) acts.append(el("small", "meta", "확인한 자료로 만들 수 있어요."));
    kids.push(acts, more);
    if (K.remove?.id === s.id) kids.push(removeCard(s));
    return sec("위키", null, ...kids);
  }

  // ---------------------------------------------------------------- 위키에서 빼기 (dry-run preview, then confirm)
  // dry_run goes in the query and in the body: a server without the preview rejects the unknown body field (400)
  // instead of removing for real, and then we fall back to a plain confirm.
  async function previewRemove(s) {
    K.remove = { id: s.id, state: "loading" }; paint();
    try {
      const r = await request(`/v1/wiki/sources/${encodeURIComponent(s.id)}/remove?dry_run=1`, { method: "POST", body: JSON.stringify({ scope: state.scope, dry_run: true }) });
      if (r?.dry_run !== true && !Array.isArray(r?.deleted)) throw new Error("invalid_response");
      K.remove = { id: s.id, state: "preview", r };
    } catch (e) {
      K.remove = /HTTP 404|invalid_request|not_found|HTTP 405/.test(e?.message || "") ? { id: s.id, state: "plain" } : { id: s.id, state: "error", error: e.message };
    }
    paint();
  }
  function removeCard(s) {
    const R = K.remove;
    if (R.state === "loading") return card("미리 세어 보는 중…", []);
    if (R.state === "error") return card("미리 보지 못했어요. 잠시 뒤 다시 시도해 주세요.", [button("다시 시도", "", null, () => previewRemove(s)), link("닫기", () => { K.remove = null; paint(); })]);
    const go = async () => {
      if (R.state === "plain" && !(await confirmPop("위키에서 뺄까요?", `${s.display_name}만 근거인 위키 페이지는 지워지고, 다른 페이지는 이 출처만 빠져요. 잠긴 페이지는 지우지 않아요. 되돌릴 수 없어요.`, "빼기"))) return;
      K.remove = null;
      void act(() => post(`/v1/wiki/sources/${encodeURIComponent(s.id)}/remove`), (r) => `뺐어요 · 지움 ${r?.deleted?.length || 0} · 출처만 뺌 ${r?.detached?.length || 0} · 링크를 글자로 ${r?.unlinked?.length || 0} · 끝낸 작업 ${r?.jobs_ended?.length || 0}`);
    };
    const r = R.r || {};
    const line = R.state === "preview"
      ? `빼면 페이지 ${r.deleted?.length || 0}쪽이 지워지고, ${r.detached?.length || 0}쪽은 출처만 빠져요. 링크 ${r.unlinked?.length || 0}곳은 글자로 바뀌어요. 되돌릴 수 없어요.`
      : "이 서버는 미리보기를 지원하지 않아요. 이 자료만 근거인 페이지는 지워지고 잠긴 페이지는 남아요.";
    return card(line, [button("빼기", "danger", null, go), link("취소", () => { K.remove = null; paint(); })]);
  }

  // ---------------------------------------------------------------- 부분 (384:30671)
  function openDoc(id) { K.doc = id; K.docTab = "sections"; K.secSel = 0; K.verSel = ""; K.search.result = null; K.search.error = ""; K.notice = ""; void docs(id); paint(); }
  function renderDoc(table) {
    const s = K.sources.find((x) => x.id === K.doc);
    if (!s) { K.doc = ""; return renderSources(table); }
    const dl = K.docs[s.id], d = current(s.id);
    const parts = [];
    const tabs = [["sections", "목차", d?.sections.length || 0], ["versions", "버전", dl?.items.length || 0], ["search", "시험 검색"]];
    parts.push(chips(tabs, K.docTab, (k) => { K.docTab = k; paint(); }));
    if (noticeLine()) parts.push(noticeLine());
    if (!dl || dl.state === "loading") { parts.push(el("div", "empty", "불러오는 중…")); table.replaceChildren(...parts); $("#detail").replaceChildren(); return; }
    if (dl.state === "error") { const e = el("div", "empty"); e.append(el("p", "", "문서를 불러오지 못했어요."), button("다시 시도", "", null, () => docs(s.id, true))); parts.push(e); table.replaceChildren(...parts); $("#detail").replaceChildren(); return; }
    if (K.docTab === "sections") {
      const list = el("div", "v4s-list");
      (d?.sections || []).forEach((path, i) => {
        const n = el("span", "v4k-num", `#${i + 1}`);
        const leaf = leafOf(path);
        list.append(row("v4k-row", K.secSel === i, [n, text(leaf, path || "본문", false)], () => { K.secSel = i; paint(); }));
      });
      if (!d?.sections.length) list.append(el("div", "empty", "목차가 없어요. 제목 없는 글은 길이로 나눠요."));
      parts.push(list, el("p", "v4s-foot", d ? `부분 ${num(d.chunk_count)}개 · 제목 ${d.sections.length}개 · 부분 본문은 시험 검색에서 보여요` : ""));
      table.replaceChildren(...parts);
      return sectionDetail(s, d);
    }
    if (K.docTab === "versions") {
      const list = el("div", "v4s-list");
      for (const v of dl.items) {
        const meta = [v.extraction_status === "failed" ? "읽기 실패" : `부분 ${num(v.chunk_count)}개`, ago(v.created_at)].join(" · ");
        list.append(row("v4k-row", (K.verSel || d?.id) === v.id, [text(v.filename, meta, true), v.current ? badge(["지금 버전", "amber"]) : el("span", "v4s-uses v4k-uses", v.source_version.slice(0, 8))], () => { K.verSel = v.id; paint(); }));
      }
      parts.push(list, el("p", "v4s-foot", "새 버전을 올리면 다시 확인해야 AI가 써요."));
      table.replaceChildren(...parts);
      return versionDetail(s, dl.items.find((v) => v.id === (K.verSel || d?.id)));
    }
    parts.push(searchPane());
    table.replaceChildren(...parts);
    searchDetail(s);
  }
  function sectionDetail(s, d) {
    const secs = d?.sections || [], i = Math.min(K.secSel, Math.max(secs.length - 1, 0)), path = secs[i];
    if (path === undefined) { $("#detail").replaceChildren(); return; }
    const box = detailShell(`부분 #${i + 1}`);
    const head = el("div", "v4s-head");
    head.append(noI18n(el("h2", "", leafOf(path))), noI18n(el("p", "v4s-lead", path)));
    box.append(head);
    if (needs(s)) box.append(card("아직 확인 전이라 AI가 쓰지 않아요.", [button("확인했어요 · 내 AI가 쓰게 하기", "primary", null, () => act(() => useEverywhere(s.id, s.revision, false, s.bindings), "이제 내 모든 AI가 이 자료를 찾아 써요."))]));
    box.append(sec("원문", null, el("p", "meta", "부분 본문은 목록에 싣지 않아요. 이 제목으로 시험 검색하면 근거로 보여요."), button("이 제목으로 시험 검색", "", null, () => { K.search.q = path; K.docTab = "search"; void runSearch(); })));
    const near = el("div", "v4k-near");
    for (const j of [i - 1, i + 1]) if (secs[j] !== undefined) {
      const b = el("button", "v4k-chunk"); b.type = "button";
      b.append(noI18n(el("span", "", `#${j + 1} · ${secs[j]}`))); // one line per neighbour (Figma 384:30671)
      b.addEventListener("click", () => { K.secSel = j; paint(); });
      near.append(b);
    }
    if (near.childElementCount) box.append(sec("앞뒤 부분", null, near));
    const more = el("details", "v4k-more");
    more.append(el("summary", "", "자세히"), facts([["벡터", `${num(d.indexed_chunks)} / ${num(d.chunk_count)} 부분`], ["나누는 법", d.chunker_version], ["파서", d.parser_version]]));
    box.append(more);
  }
  function versionDetail(s, v) {
    if (!v) { $("#detail").replaceChildren(); return; }
    const box = detailShell(v.current ? "버전 · 지금" : "버전 · 예전");
    const head = el("div", "v4s-head");
    head.append(noI18n(el("h2", "mono v4k-h2", v.filename)), el("p", "v4s-lead", `${ago(v.created_at)} 올림`));
    box.append(head);
    if (v.extraction_status === "failed") box.append(card("파일에서 글을 읽지 못했어요. 고쳐서 다시 올려 주세요.", [button("다시 올리기", "primary", null, () => pickFile(s))]));
    box.append(sec("정보", null, facts([["부분", num(v.chunk_count)], ["올린 때", ago(v.created_at)]])));
    const more = el("details", "v4k-more");
    more.append(el("summary", "", "자세히"), facts([["형식", v.mime_type], ["추출", v.extraction_status], ["읽은 오류", v.error_code || "", true], ["색인", v.current ? `${num(v.indexed_chunks)} / ${num(v.chunk_count)}` : ""],
      ["원본 버전", v.source_version], ["파서", v.parser_version], ["나누는 법", v.chunker_version], ["내용 해시", v.content_hash]]));
    box.append(more);
  }
  async function runSearch() {
    const S = K.search;
    if (!S.q || S.busy) { paint(); return; }
    S.busy = true; S.error = ""; paint();
    try {
      const r = await post("/v1/knowledge/search", { specialist_profile_id: S.profile, query: S.q, limit: 8 });
      if (r?.authority !== "data_only_untrusted") throw new Error("invalid_response");
      S.result = r;
    } catch (e) { S.result = null; S.error = errText(e); }
    S.busy = false; paint();
  }
  function searchPane() {
    const S = K.search, wrap = el("div", "v4k-searchpane");
    const bar = el("form", "v4s-bar");
    const box = el("label", "v4s-search v4k-search");
    const input = el("input"); input.type = "search"; input.placeholder = "찾을 내용을 적어 보세요"; input.value = S.q; input.setAttribute("aria-label", "시험 검색");
    box.append(icon("magnifying-glass"), input);
    const go = button(S.busy ? "찾는 중…" : "찾기", "primary"); go.type = "submit"; go.disabled = S.busy;
    bar.append(box, select("누가 찾나요", [[SHARED, "모든 AI"], ...PROFILES], S.profile, (v) => { S.profile = v; }), go);
    bar.addEventListener("submit", (e) => { e.preventDefault(); S.q = input.value.trim(); void runSearch(); });
    wrap.append(bar, el("p", "ss-note", "AI가 쓰게 켠 자료만 찾아요. 결과는 참고 자료일 뿐 지시가 아니에요."));
    if (S.error) wrap.append(el("div", "empty", S.error));
    const r = S.result;
    if (r?.reason === "no_active_binding") wrap.append(el("div", "empty", S.profile === SHARED ? "AI가 쓰게 켠 자료가 없어요. 자료 상세에서 '쓰게 하기'를 눌러 주세요." : "이 전문가에 켜진 자료가 없어요. 자료 상세의 자세히에서 켜 주세요."));
    else if (r?.receipt) {
      const items = r.receipt.context?.items || [];
      const list = el("div", "v4k-evidence");
      for (const ev of items) {
        const c = el("article", "v4k-ev");
        const top = el("div", "v4k-ev-top");
        top.append(noI18n(el("span", "", ev.section_path || sourceName(ev.source_id))), el("small", "", `${num(ev.token_estimate)} 토큰`));
        c.append(top, noI18n(el("p", "", ev.content)), noI18n(el("small", "mono", ev.citation)));
        list.append(c);
      }
      if (!items.length) list.append(el("div", "empty", "맞는 부분이 없어요."));
      wrap.append(list);
    }
    return wrap;
  }
  function searchDetail(s) {
    const rc = K.search.result?.receipt;
    const box = detailShell("시험 검색");
    const head = el("div", "v4s-head");
    head.append(el("h2", "", "검색 결과 요약"), el("p", "v4s-lead", `${profileName(K.search.profile)} · ${s.display_name}`));
    box.append(head);
    if (!rc) { box.append(el("p", "meta", "검색하면 몇 개를 고르고 몇 개를 뺐는지 여기 보여요.")); return; }
    const ex = Object.entries(rc.exclusion_summary || {});
    box.append(sec("고른 근거", null, facts([["후보", num(rc.candidate_count)], ["고름", num(rc.selected_count)], ["뺌", num(rc.excluded_count)], ["방식", rc.retrieval_mode || "기본"], ["기록", rc.retrieval_log_id]])));
    if (ex.length) {
      const ul = el("ul", "v4k-excl");
      for (const [k, n] of ex) { const li = el("li"); li.append(el("span", "", EXCLUDE[k] || k), el("b", "", `${n}건`)); ul.append(li); }
      box.append(sec(`제외 ${num(rc.excluded_count)}건`, null, ul));
    }
    box.append(el("p", "meta", "AI는 이 결과를 자료로만 받아요. 안에 적힌 지시는 따르지 않아요."));
  }

  // ---------------------------------------------------------------- 위키 (384:31269)
  function filteredPages() {
    const { q, type, sort, chip } = K.pg;
    let items = K.pages.filter((p) => p.review_state !== "deleted" && (!type || p.type === type) && inFolder(p, K.pg.folder));
    items = items.filter((p) => chip === "all" || (chip === "locked" ? p.locked : chip === "stale" ? p.stale_sources?.length : p.review_state === chip));
    if (q) { const t = q.toLowerCase(); items = items.filter((p) => titleOf(p).toLowerCase().includes(t) || body(p).text.toLowerCase().includes(t)); }
    return [...items].sort(sort === "title" ? (a, b) => titleOf(a).localeCompare(titleOf(b)) : (a, b) => String(b.updated_at).localeCompare(String(a.updated_at)));
  }
  function renderWiki(table) {
    const parts = [];
    if (K.wiki !== "ready") { table.replaceChildren(el("div", "empty", K.wiki === "off" ? "이 서버는 아직 위키를 지원하지 않아요." : K.wiki === "error" ? "위키를 불러오지 못했어요." : "불러오는 중…")); $("#detail").replaceChildren(); return; }
    const alive = K.pages.filter((p) => p.review_state !== "deleted");
    const drafts = alive.filter((p) => p.review_state === "draft").length, stale = alive.filter((p) => p.stale_sources?.length).length;
    if (drafts + stale) parts.push(attn(drafts + stale, [drafts && `초안 ${drafts}`, stale && `출처가 바뀐 페이지 ${stale}`].filter(Boolean).join(" · "), "하나씩 검토하기",
      () => { const p = alive.find((x) => x.review_state === "draft") || alive.find((x) => x.stale_sources?.length); if (p) openPage(p.id); }));
    const bar = el("div", "v4s-bar");
    bar.append(searchBox("위키 페이지 검색", K.pg.q, (v) => { K.pg.q = v; paint(); }),
      select("종류", [["", "종류: 모두"], ["entity", "개체"], ["concept", "개념"]], K.pg.type, (v) => { K.pg.type = v; paint(); }),
      select("정렬", [["recent", "최근 고친 순"], ["title", "제목 순"]], K.pg.sort, (v) => { K.pg.sort = v; paint(); }));
    parts.push(bar);
    const n = (f) => alive.filter(f).length;
    const tabs = [["all", "전체", alive.length], ["draft", "초안", n((p) => p.review_state === "draft")], ["reviewed", "검토됨", n((p) => p.review_state === "reviewed")], ["locked", "잠금", n((p) => p.locked)]];
    if (n((p) => p.review_state === "rejected")) tabs.push(["rejected", "거절됨", n((p) => p.review_state === "rejected")]);
    parts.push(chips(tabs, K.pg.chip, (k) => { K.pg.chip = k; paint(); }));
    if (noticeLine()) parts.push(noticeLine());
    const items = filteredPages();
    const list = el("div", "v4s-list");
    for (const p of items) {
      const b = body(p), when = p.locked ? `${ago(p.updated_at)} 잠금` : p.review_state === "draft" ? `${ago(p.updated_at)} AI가 ${p.reviewed ? "고침" : "만듦"}` : `${ago(p.updated_at)} 검토`;
      list.append(row("v4k-row", K.pageSel === p.id, [text(titleOf(p), `링크 ${b.links?.length || 0} · 출처 ${p.knowledge_source_ids?.length || 0} · ${when}`, false), badge(pageState(p))], () => openPage(p.id)));
    }
    if (!items.length) list.append(el("div", "empty", alive.length ? "조건에 맞는 페이지가 없어요." : "아직 위키가 없어요. 자료 상세에서 '위키 만들기'를 눌러 보세요."));
    const main = el("div", "v4k-main"), cols = el("div", "v4k-cols");
    main.append(list, el("p", "v4s-foot", `${alive.length}개 중 ${items.length}개 표시 · 잠긴 페이지는 AI가 고치지 않아요`));
    cols.append(folderSide(), main);
    parts.push(cols);
    table.replaceChildren(...parts);
    if (!K.pageSel && items[0]) K.pageSel = items[0].id;
    pageDetail();
  }
  function openPage(id) { K.tab = "wiki"; K.doc = ""; K.pageSel = id; K.compare = false; K.edit = null; K.notice = ""; paint(); }
  function pageDetail() {
    const p = K.pages.find((x) => x.id === K.pageSel);
    if (!p) { $("#detail").replaceChildren(); return; }
    const b = body(p), id = encodeURIComponent(p.id);
    const kind = p.review_state === "draft" ? "초안" : pageState(p)[0];
    const box = detailShell(`위키 · ${kind}`);
    const head = el("div", "v4s-head");
    head.append(noI18n(el("h2", "", titleOf(p))), el("p", "v4s-lead", [p.type === "entity" ? "개체" : p.type === "concept" ? "개념" : p.type, `${ago(p.updated_at)} ${MERGE[p.last_merge] || "고침"}`, p.reviewed && p.draft ? "검토본 있음" : ""].filter(Boolean).join(" · ")));
    box.append(head);
    const review = (decision) => act(() => post(`/v1/wiki/pages/${id}/review`, { expected_revision: p.revision, decision }), decision === "approve" ? "검토됨으로 표시했어요. 이제 검색에 나와요." : "버렸어요. 검토본은 그대로예요.");
    const startEdit = () => { K.edit = { title: titleOf(p), text: (p.reviewed || p.draft)?.text || "" }; if (p.draft) K.edit.text = p.draft.text; paint(); };
    if (p.review_state === "draft") box.append(card(p.reviewed ? "AI가 고친 초안이에요. 지금 보이는 글과 비교하고 검토해 주세요." : "AI가 새로 만든 초안이에요. 검토해 주세요.",
      [button("검토됨으로 표시", "primary", null, () => review("approve")), button("고치기", "", null, startEdit), link("버리기", () => review("reject"))], "고치면 바로 검토된 글이 되고, 잠겨서 AI가 더 고치지 않아요."));
    else if (p.stale_sources?.length) box.append(card("지워진 자료의 내용이 남아 있을 수 있어요. 직접 고쳐 주세요.", [button("고치기", "primary", null, startEdit)]));
    // 본문 · 비교 · 고치기
    if (K.edit) {
      const t = el("input", "v4s-input"); t.value = K.edit.title; t.setAttribute("aria-label", "제목");
      const area = el("textarea", "v4s-input v4k-edit"); area.value = K.edit.text; area.rows = 12; area.setAttribute("aria-label", "본문");
      t.addEventListener("input", () => { K.edit.title = t.value; }); area.addEventListener("input", () => { K.edit.text = area.value; });
      const acts = el("div", "v4s-actions");
      acts.append(button("저장하고 잠그기", "primary", null, () => { const e = K.edit; K.edit = null; void act(() => post(`/v1/wiki/pages/${id}/edit`, { expected_revision: p.revision, title: e.title.trim(), text: e.text }), "저장했어요. 이 페이지는 잠겨서 AI가 고치지 않아요."); }),
        link("취소", () => { K.edit = null; paint(); }));
      for (const x of acts.querySelectorAll("button")) x.disabled = K.busy;
      box.append(sec("고치기", null, t, area, el("p", "meta", "제목은 같은 주소가 되는 범위에서만 바꿀 수 있어요."), acts));
    } else if (K.compare && p.reviewed && p.draft) {
      const pair = el("div", "v4k-compare");
      const side = (label, txt) => { const c = el("div", "v4k-side"); c.append(el("small", "", label), wikiText(txt)); return c; };
      pair.append(side("지금 보이는 글", p.reviewed.text), side("AI 제안", p.draft.text));
      box.append(sec("본문", link("닫기", () => { K.compare = false; paint(); }), pair));
    } else {
      box.append(sec("본문", p.reviewed && p.draft ? link("검토본과 비교", () => { K.compare = true; paint(); }) : null, wikiText(b.text)));
    }
    // 링크
    const out = b.links || [], incoming = K.links.filter((l) => l.to_id === p.id).length;
    const lbox = el("div", "v4k-wlinks");
    for (const name of out) {
      const target = pageByLink(name);
      const c = el("button", `v4k-wchip${target ? "" : " missing"}`, `[[${name}]]`); c.type = "button"; noI18n(c);
      if (target) c.addEventListener("click", () => openPage(target.id)); else c.title = "아직 없는 페이지예요";
      lbox.append(c);
    }
    if (!out.length) lbox.append(el("p", "meta", "나가는 링크가 없어요."));
    box.append(sec("링크", el("span", "v4k-aside", `나가는 ${out.length} · 들어오는 ${incoming}`), lbox));
    // 출처
    const srcs = el("div", "v4k-srcs");
    for (const sid of p.knowledge_source_ids || []) {
      const s = K.sources.find((x) => x.id === sid);
      const r = el("button", "v4k-src"); r.type = "button";
      const t = el("span", "v4k-src-text");
      t.append(noI18n(el("b", "", `문서 · ${s?.display_name || sid}`)), el("small", "", s ? `부분 ${num(s.chunk_count)}개 · ${srcState(s)[0]}` : "목록에 없는 자료"));
      r.append(t);
      if (s) r.addEventListener("click", () => { K.tab = "sources"; K.srcSel = s.id; K.doc = ""; paint(); });
      srcs.append(r);
    }
    for (const sid of p.stale_sources || []) {
      const r = el("div", "v4k-src stale"), t = el("span", "v4k-src-text");
      t.append(noI18n(el("b", "", `지워진 자료 · ${sid}`)), el("small", "", "내용이 남아 있을 수 있어요"));
      r.append(t, el("span", "v4s-badge amber", "지워짐"));
      srcs.append(r);
    }
    if (!srcs.childElementCount) srcs.append(el("p", "meta", "출처가 없어요."));
    box.append(sec("출처", el("span", "v4k-aside", String((p.knowledge_source_ids?.length || 0) + (p.stale_sources?.length || 0))), srcs));
    // 잠금
    const lock = el("div", "v4k-lock");
    lock.append(el("p", "meta", p.locked ? "잠겨 있어요. AI 작업은 이 페이지를 건너뛰어요." : "검토한 내용을 그대로 두려면 잠가요."),
      button(p.locked ? "잠금 풀기" : "잠그기", "", null, () => act(() => post(`/v1/wiki/pages/${id}/lock`, { expected_revision: p.revision, locked: !p.locked }), p.locked ? "잠금을 풀었어요." : "잠갔어요.")));
    lock.lastChild.disabled = K.busy;
    box.append(sec("잠금", null, lock), pageTools(p));
  }

  // ---------------------------------------------------------------- 기억 후보 만들기 (글 붙여넣기 · 여러 파일)
  // 글: POST /v1/memories/ingest (one item). 파일: POST /v1/files/memories, one file after another (the desktop bridge carries
  // JSON only, so the multipart batch route is not used; each file gets the same per-file result and job).
  // Everything optional sits under 자세히 and stays empty: the server's own defaults apply.
  const PARSERS = [["", "서버 기본 순서"], ["builtin", "내장 읽기"], ["converter", "변환기"], ["vision", "이미지 읽기 (모델)"], ["transcribe", "음성 받아쓰기 (모델)"],
    ["upload_job", "외부 읽기 (업로드)"], ["presigned_job", "외부 읽기 (주소)"]];
  let strategies = null; // the space's named extraction strategies, read once per window
  async function loadStrategies() {
    const { workspace_id: w, project_id: p } = state.scope || {};
    if (!w || !p) return [];
    try { return Object.keys((await get(`/v1/spaces/${encodeURIComponent(w)}/${encodeURIComponent(p)}/config/effective`))?.config?.extraction_strategies || {}).sort(); }
    catch (_) { return []; } // no strategies to offer; the server default applies
  }
  const field = (label, input) => { const f = el("label", "v4k-field"); f.append(el("span", "", label), input); return f; };
  function openAdd(initial = "text") {
    const A = { text: "", files: [], tags: "", context: "", when: "", meta: "", strategy: "", parser: "" };
    let pop = null;
    const redraw = (key) => { if (pop?.dialog.isConnected) pop.select(key); };
    if (!strategies) loadStrategies().then((list) => { strategies = list; if (list.length) redraw(A.files.length ? "files" : initial); });
    function options(files) {
      const more = el("details", "v4k-more v4k-opts"); more.append(el("summary", "", "자세히"));
      const input = (key, type = "text", ph = "") => { const i = el("input", "v4s-input"); i.type = type; i.value = A[key]; i.placeholder = ph; i.addEventListener("input", () => { A[key] = i.value; }); return i; };
      more.append(field("태그 (쉼표로 나눠요)", input("tags", "text", "회의, 디자인")), field("상황 설명 (AI가 글과 함께 읽어요)", input("context", "text", "팀 주간 회의록")),
        field("일이 있었던 때 (‘어제’ 같은 말의 기준)", input("when", "datetime-local")));
      if (!files) { const t = el("textarea", "v4s-input"); t.rows = 3; t.value = A.meta; t.placeholder = "project=arkism"; t.addEventListener("input", () => { A.meta = t.value; }); more.append(field("메타데이터 (한 줄에 이름=값)", t)); }
      if (strategies?.length) more.append(field("뽑는 방식", select("뽑는 방식", [["", "공간 기본"], ...strategies.map((x) => [x, x])], A.strategy, (v) => { A.strategy = v; })));
      if (files) more.append(field("파일 읽는 방법", select("파일 읽는 방법", PARSERS, A.parser, (v) => { A.parser = v; })));
      return more;
    }
    const actions = (label, ok, go) => {
      const a = el("div", "v4s-actions"), b = button(label, "primary", null, () => { pop.dialog.close(); go(); });
      b.disabled = !ok || K.busy || !!K.extract?.active;
      a.append(b, link("취소", () => pop.dialog.close()));
      return a;
    };
    const textTab = (body) => {
      const t = el("textarea", "v4s-input v4k-paste"); t.rows = 10; t.value = A.text; t.placeholder = "회의 메모, 대화, 글을 붙여넣어 주세요."; t.setAttribute("aria-label", "넣을 글");
      const acts = actions("기억 후보 만들기", A.text.trim(), () => extractText(A));
      t.addEventListener("input", () => { A.text = t.value; acts.firstChild.disabled = !t.value.trim() || K.busy; });
      body.append(t, options(false), el("p", "meta", "글에서 기억할 만한 내용을 뽑아 후보로 만들어요. 저장할지는 '확인해 주세요'에서 내가 골라요."), acts);
    };
    const filesTab = (body) => {
      const list = el("div", "v4k-chunks");
      for (const f of A.files) { const r = el("div", "v4k-chunk"); r.append(noI18n(el("span", "", f.name)), el("small", "", bytes(f.size))); list.append(r); }
      if (!A.files.length) list.append(el("p", "meta", "아직 고른 파일이 없어요."));
      const pick = el("div", "v4s-actions");
      pick.append(button(A.files.length ? "다른 파일 고르기" : "파일 고르기", "", null, () => chooseFile((fs) => { A.files = fs; redraw("files"); }, true)));
      body.append(pick, list, el("p", "meta", `${KINDS} 파일을 한 번에 여러 개 고를 수 있어요. 파일마다 4MB까지예요.`), options(true),
        actions(A.files.length > 1 ? `파일 ${A.files.length}개로 기억 후보 만들기` : "기억 후보 만들기", A.files.length, () => extractFiles(A)));
    };
    pop = window.ArkismCrewPopup({ el, button }).open({ title: "기억 후보 만들기", sub: "AI가 뽑은 내용은 후보로만 남아요. 내가 승인해야 기억이 돼요.", cls: "v4k-add", initial, tabs: [
      { key: "text", label: "글 붙여넣기", render: textTab }, { key: "files", label: "파일", render: filesTab }] });
  }
  function extractText(A) {
    const label = A.text.trim().split("\n")[0].slice(0, 40);
    return startExtract(async () => {
      const r = await post("/v1/memories/ingest", { items: [{ content: A.text, ...window.ArkismKnowledge.ingestExtras(A, true) }] });
      const it = r?.items?.[0];
      if (!it) throw new Error("invalid_response");
      if (it.status === "failed" && !it.job_id) throw new Error(it.error || "invalid_request");
      return it;
    }, label);
  }
  // One file after another; a file that fails does not stop the rest, and the end says how many went.
  async function extractFiles(A) {
    const n = A.files.length, extra = window.ArkismKnowledge.ingestExtras(A, false);
    let failed = 0, reason = "";
    for (const [i, f] of A.files.entries()) {
      if (f.size > 4 << 20) { failed++; reason ||= "파일이 너무 커요. 4MB까지 올릴 수 있어요."; continue; }
      const ext = extOf(f);
      await startExtract(async () => post("/v1/files/memories", { filename: f.name, content_base64: b64(await f.arrayBuffer()), ...(MIME[ext] ? { mime: MIME[ext] } : {}), ...extra }),
        n > 1 ? `${f.name} (${i + 1}/${n})` : f.name);
      if (K.extract?.error) { failed++; reason ||= K.extract.error; }
    }
    if (n > 1) { K.notice = failed ? `${n}개 중 ${n - failed}개를 보냈어요. 못 보낸 파일이 ${failed}개 있어요.` : `${n}개 파일을 보냈어요. '기억 문서'에서 파일마다 결과를 볼 수 있어요.`; if (reason) K.notice += ` ${reason}`; paint(); }
    else if (failed && !K.extract?.error) { K.notice = reason; paint(); }
  }

  // ---------------------------------------------------------------- 기억 문서 (GET /v1/files/memories/documents, PATCH · DELETE …/{id}, …/reprocess)
  // Every text or file a person turned into memory candidates. Tag edits reach the document's waiting candidates and
  // approved memories; reprocessing reads the stored text again and only makes new candidates; deleting first counts
  // what it touches (waiting candidates are rejected, approved memories stay).
  const DOC = { pending: ["기다리는 중", "amber"], retry: ["기다리는 중", "amber"], running: ["읽는 중", "amber"], done: ["끝남", ""], failed: ["멈춤", "amber"], cancelled: ["멈춤", ""] };
  const docErr = (e) => (/conflict/.test(e?.message || "") ? "아직 읽고 있거나 그새 바뀐 문서예요. 잠시 뒤 다시 해 주세요." : extractErr(e));
  async function loadDocs(more = false) {
    const F = K.fd, ticket = loads.begin("docs");
    F.busy = true; F.error = ""; paint();
    try {
      const r = await get("/v1/files/memories/documents", { limit: 50, ...(F.tag ? { tag: F.tag } : {}), ...(more && F.next ? { cursor: F.next } : {}) });
      if (!ticket.live()) return;
      if (!Array.isArray(r?.items)) throw new Error("invalid_response");
      F.items = more ? [...F.items, ...r.items] : r.items; F.next = r.next_cursor || ""; F.state = "ready";
    } catch (e) { if (!ticket.live()) return; F.error = errText(e); F.state = F.items.length ? "ready" : off(e) ? "off" : "error"; }
    F.busy = false; paint();
  }
  function renderDocs(table) {
    const F = K.fd, parts = [];
    const ex = extractCard(); if (ex) parts.push(ex);
    if (F.state === "idle") { F.state = "loading"; queueMicrotask(() => loadDocs()); }
    const bar = el("div", "v4s-bar");
    const date = (key, label) => { const i = el("input", "v4s-input v4k-date"); i.type = "date"; i.value = F[key]; i.setAttribute("aria-label", label); i.title = label; i.addEventListener("change", () => { F[key] = i.value; paint(); }); return i; };
    const tags = [...new Set([F.tag, ...F.items.flatMap((d) => d.tags || [])].filter(Boolean))].sort();
    bar.append(searchBox("문서 이름·태그 검색", F.q, (v) => { F.q = v; paint(); }),
      noI18nOpts(select("태그", [["", "태그: 모두"], ...tags.map((t) => [t, t])], F.tag, (v) => { F.tag = v; F.state = "loading"; void loadDocs(); })),
      date("from", "이날부터"), date("to", "이날까지"));
    parts.push(bar);
    if (noticeLine()) parts.push(noticeLine());
    const items = window.ArkismKnowledge.filterDocs(F.items, F);
    const list = el("div", "v4s-list");
    for (const d of items) {
      const meta = `부분 ${num(d.chunks)}개 · 후보 ${num(d.candidates_created)}개 · ${ago(d.updated_at)} 올림`; // one sentence, so it translates as a whole
      list.append(row("v4k-row", F.sel === d.id, [text(d.label || d.id, meta, true), badge(DOC[d.status] || [d.status || "—", ""])], () => { F.sel = d.id; F.del = null; K.notice = ""; paint(); }));
    }
    if (!items.length) {
      const box = el("div", "empty");
      if (F.state === "loading" || F.state === "idle") box.append(el("p", "", "불러오는 중…"));
      else if (F.state === "off") box.append(el("p", "", "이 서버는 기억 문서 목록을 아직 지원하지 않아요."));
      else if (F.state === "error") box.append(el("p", "", F.error), button("다시 시도", "", null, () => loadDocs()));
      else if (F.items.length || F.tag) box.append(el("p", "", "조건에 맞는 문서가 없어요."), button("조건 지우기", "", null, () => { Object.assign(F, { q: "", from: "", to: "" }); if (F.tag) { F.tag = ""; void loadDocs(); } else paint(); }));
      else box.append(el("p", "", "아직 기억 후보를 만든 글이나 파일이 없어요."), button("기억 후보 만들기", "primary", null, () => openAdd()));
      list.append(box);
    }
    parts.push(list, el("p", "v4s-foot", F.busy ? "불러오는 중…" : `불러온 ${F.items.length}개 중 ${items.length}개 표시 · 이름·태그·날짜는 불러온 목록에서 걸러요`));
    if (F.next) { const b = button("문서 더 보기", "", null, () => loadDocs(true)); b.disabled = F.busy; parts.push(b); }
    table.replaceChildren(...parts);
    if (!items.some((d) => d.id === F.sel)) F.sel = items[0]?.id || "";
    docDetail(items.find((d) => d.id === F.sel));
  }
  const noI18nOpts = (s) => { for (const o of [...s.options].slice(1)) noI18n(o); return s; }; // tag names are data
  function docDetail(d) {
    if (!d) { $("#detail").replaceChildren(); return; }
    const F = K.fd, id = encodeURIComponent(d.id);
    const box = detailShell("기억 문서");
    const head = el("div", "v4s-head");
    head.append(noI18n(el("h2", "mono v4k-h2", d.label || d.id)), el("p", "v4s-lead", [(DOC[d.status] || [d.status])[0], `${ago(d.updated_at)} 올림`].join(" · ")));
    box.append(head);
    const docAct = (work, done) => act(work, done, docErr).then(() => loadDocs());
    if (d.status === "failed") box.append(card("읽다가 멈췄어요. 이어서 읽으면 남은 부분만 읽어요.", [button("이어서 하기", "primary", null, () => { K.extract = { id: d.job_id, label: d.label, phase: "failed" }; resumeExtract(); })]));
    if (F.del?.id === d.id) box.append(deleteCard(d));
    // 태그
    const tagBox = el("div", "v4k-wlinks");
    for (const t of d.tags || []) {
      const c = el("button", "v4k-wchip v4k-tag", `${t} ×`); c.type = "button"; noI18n(c); c.setAttribute("aria-label", `${t} 태그 빼기`);
      c.disabled = K.busy || !human();
      c.addEventListener("click", () => docAct(() => request(`/v1/files/memories/documents/${id}`, { method: "PATCH", body: JSON.stringify({ scope: state.scope, remove: [t] }) }), tagDone));
      tagBox.append(c);
    }
    if (!d.tags?.length) tagBox.append(el("p", "meta", "태그가 없어요."));
    const add = el("form", "v4k-bind add"), input = el("input", "v4s-input"); input.placeholder = "새 태그"; input.setAttribute("aria-label", "새 태그");
    const addBtn = button("추가", "", null); addBtn.type = "submit"; addBtn.disabled = K.busy || !human();
    add.append(input, addBtn);
    add.addEventListener("submit", (e) => { e.preventDefault(); const t = input.value.trim(); if (t) docAct(() => request(`/v1/files/memories/documents/${id}`, { method: "PATCH", body: JSON.stringify({ scope: state.scope, add: [t] }) }), tagDone); });
    box.append(sec("태그", null, tagBox, add, el("p", "meta", "고친 태그는 이 문서의 기억 후보와 승인된 기억에도 같이 바뀌어요.")));
    // 기억 후보
    const cands = el("div", "v4s-actions");
    cands.append(button("부분·이력 보기", "", null, () => openParts({ title: d.label || d.id, doc: d.id })), button("후보 확인하러 가기", "", null, () => ctx.go?.("memories", "candidate")));
    box.append(sec("기억 후보", null, el("p", "meta", `이 문서에서 만든 후보 ${num(d.candidates_created)}개 · 부분을 누르면 그 부분에서 나온 후보와 기억, 바뀐 이력을 볼 수 있어요.`), cands));
    // 관리
    const tools = el("div", "v4s-actions");
    const again = button("다시 처리", "", null, async () => {
      if (!(await confirmPop("이 문서를 다시 읽을까요?", "저장해 둔 글을 처음부터 다시 읽어 새 기억 후보를 만들어요. 모델 호출을 써요. 이미 있는 후보와 기억은 그대로예요.", "다시 처리"))) return;
      void startExtract(() => post(`/v1/files/memories/documents/${id}/reprocess`), d.label || d.id);
    });
    again.disabled = K.busy || !!K.extract?.active || !["done"].includes(d.status);
    const del = button("지우기", "ghost danger", null, () => previewDelete(d)); del.disabled = K.busy || !human();
    tools.append(button("원본 파일", "", null, () => openParts({ title: d.label || d.id, doc: d.id, initial: "files" })), again, del);
    box.append(sec("관리", null, tools, noticeLine()));
    const more = el("details", "v4k-more");
    more.append(el("summary", "", "자세히"), facts([["문서 ID", d.id, true], ["작업 ID", d.job_id, true], ["올린 사람", d.requested_by, true], ["처음 올린 때", ago(d.created_at)], ["부분", num(d.chunks)]]));
    box.append(more);
  }
  const tagDone = (r) => (r?.skipped ? `태그를 고쳤어요 · 후보 ${num(r?.candidates)}개 · 기억 ${num(r?.memories)}개에도 반영 · 그사이 바뀐 ${num(r.skipped)}개는 건너뜀`
    : `태그를 고쳤어요 · 후보 ${num(r?.candidates)}개 · 기억 ${num(r?.memories)}개에도 반영`); // two whole sentences so each translates
  // Counts first from the document's chunks (…/chunks: each chunk's candidates and their state), then asks.
  async function previewDelete(d) {
    const F = K.fd;
    F.del = { id: d.id, state: "loading" }; paint();
    try {
      const all = [];
      for (let cursor = "", i = 0; i < 20; i++) {
        const r = await get(`/v1/files/memories/documents/${encodeURIComponent(d.id)}/chunks`, { limit: 200, ...(cursor ? { cursor } : {}) });
        all.push(...(r?.items || [])); cursor = r?.next_cursor || "";
        if (!cursor) break;
      }
      if (F.del?.id === d.id) F.del = { id: d.id, state: "preview", ...window.ArkismKnowledge.deleteImpact(all) };
    } catch (e) { if (F.del?.id === d.id) F.del = { id: d.id, state: "error", error: errText(e) }; }
    paint();
  }
  function deleteCard(d) {
    const R = K.fd.del, close = link("취소", () => { K.fd.del = null; paint(); });
    if (R.state === "loading") return card("이 문서로 만든 후보와 기억을 세는 중…", []);
    if (R.state === "error") return card(R.error, [button("다시 시도", "", null, () => previewDelete(d)), close]);
    const go = () => { K.fd.del = null; void act(() => request(`/v1/files/memories/documents/${encodeURIComponent(d.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope }) }),
      (r) => `지웠어요 · 버린 후보 ${num(r?.rejected)}개 · 남은 기억 ${num(r?.kept)}개`, docErr).then(() => { K.fd.sel = ""; return loadDocs(); }); };
    return card(`지우면 확인을 기다리는 후보 ${num(R.waiting)}개는 버려지고, 이미 승인한 기억 ${num(R.kept)}개는 그대로 남아요. 되돌릴 수 없어요.`, [button("지우기", "danger", null, go), close]);
  }

  // ---------------------------------------------------------------- 위키 폴더 · 내보내기 (GET /v1/wiki/tree, POST/PATCH/DELETE /v1/wiki/folders, GET /v1/wiki/export)
  const folderOf = (p) => p.folder ?? K.folderOf[p.id] ?? "";
  const inFolder = (p, f) => f == null || folderOf(p) === f || folderOf(p).startsWith(`${f}/`);
  const folderUrl = (extra) => `/v1/wiki/folders?${query(extra)}`; // DELETE takes the folder in the query
  const wikiErr = (e) => (/conflict/.test(e?.message || "") ? "폴더에 페이지가 있거나 그새 바뀌었어요. 새로고침했어요." : e?.message === "payload_too_large" ? "위키가 너무 커서 한 번에 내보내지 못했어요." : errText(e));
  const wikiAct = (work, done) => act(work, done, wikiErr);
  async function newFolder() {
    const path = (await window.askText("새 폴더 이름\n'/'로 나누면 안쪽 폴더가 돼요. 예: 운영/배포", K.pg.folder ? `${K.pg.folder}/` : "", { okLabel: "만들기", primary: true }))?.trim().replace(/^\/+|\/+$/g, "");
    if (!path) return;
    await wikiAct(() => post("/v1/wiki/folders", { path }), "폴더를 만들었어요.");
    K.pg.folder = path; paint();
  }
  async function moveFolder(from) {
    const to = (await window.askText("폴더의 새 경로\n이름만 바꾸거나 '/'로 다른 폴더 안으로 옮겨요. 안의 페이지도 함께 옮겨져요.", from, { okLabel: "바꾸기", primary: true }))?.trim().replace(/^\/+|\/+$/g, "");
    if (!to || to === from) return;
    await wikiAct(() => request("/v1/wiki/folders", { method: "PATCH", body: JSON.stringify({ scope: state.scope, from, to }) }), "폴더를 옮겼어요.");
    if (K.folders.some((f) => f.path === to)) K.pg.folder = to;
    paint();
  }
  async function deleteFolder(path) {
    const n = K.pages.filter((p) => p.review_state !== "deleted" && inFolder(p, path)).length;
    if (!(await confirmPop("폴더를 지울까요?", n ? `안에 있는 페이지 ${n}쪽도 함께 지워지고, 다른 페이지에서 그 페이지로 가는 링크는 글자로 바뀌어요. 되돌릴 수 없어요.` : "빈 폴더를 지워요.", "지우기"))) return;
    K.pg.folder = null;
    await wikiAct(() => request(folderUrl({ path, ...(n ? { recursive: "true" } : {}) }), { method: "DELETE" }), (r) => `지웠어요 · 페이지 ${num(r?.deleted?.length)}쪽`);
  }
  const exportWiki = () => wikiAct(async () => save("wiki.zip", await request(`/v1/wiki/export?${query({ format: "zip" })}`, { binary: true }), "application/zip"),
    "위키를 내보냈어요. 검토된 페이지만 들어 있어요.");
  function folderSide() {
    const side = el("div", "v4k-groups");
    side.append(el("p", "v4k-label", "폴더"));
    const alive = K.pages.filter((p) => p.review_state !== "deleted");
    const group = (key, label, sub, n) => {
      const g = el("button", `v4k-group${K.pg.folder === key ? " on" : ""}`); g.type = "button";
      const t = el("span", "v4k-group-text"), b = el("b", "", label);
      if (key) noI18n(b);
      t.append(b); if (sub) t.append(el("small", "", sub)); g.append(t, el("output", "", String(n)));
      g.addEventListener("click", () => { K.pg.folder = key; paint(); });
      return g;
    };
    side.append(group(null, "전체", "모든 폴더", alive.length));
    for (const f of K.folders) side.append(group(f.path, f.path, "", alive.filter((p) => inFolder(p, f.path)).length)); // the count includes inner folders
    const acts = el("div", "v4k-folder-acts");
    acts.append(link("+ 새 폴더", newFolder));
    if (K.pg.folder) acts.append(link("이름 바꾸기·옮기기", () => moveFolder(K.pg.folder)), link("폴더 지우기", () => deleteFolder(K.pg.folder)));
    acts.append(link("위키 내보내기 (.zip)", exportWiki));
    for (const b of acts.querySelectorAll("button")) b.disabled = K.busy;
    side.append(acts);
    return side;
  }
  // 페이지 관리: move to a folder, save as Markdown, delete (locked pages too; links to it become plain text).
  function pageTools(p) {
    const id = encodeURIComponent(p.id), b = body(p);
    const where = select("폴더", [["", "맨 위"], ...K.folders.map((f) => [f.path, f.path])], folderOf(p), (v) =>
      wikiAct(() => request(`/v1/wiki/pages/${id}`, { method: "PATCH", body: JSON.stringify({ scope: state.scope, expected_revision: p.revision, folder: v }) }), "페이지를 옮겼어요."));
    noI18nOpts(where); where.disabled = K.busy;
    const acts = el("div", "v4s-actions");
    acts.append(button("Markdown으로 저장", "", null, () => { save(`${p.slug || p.id}.md`, `# ${titleOf(p)}\n\n${b.text || ""}\n`, "text/markdown"); K.notice = "페이지를 저장했어요."; paint(); }),
      button("페이지 지우기", "ghost danger", null, async () => {
        if (!(await confirmPop("이 페이지를 지울까요?", "다른 페이지에서 이 페이지로 가는 링크는 글자로 바뀌어요. 잠긴 페이지도 지워져요. 되돌릴 수 없어요.", "지우기"))) return;
        K.pageSel = "";
        await wikiAct(() => request(`/v1/wiki/pages/${id}?${query({ expected_revision: p.revision })}`, { method: "DELETE" }), "페이지를 지웠어요.");
      }));
    for (const x of acts.querySelectorAll("button")) x.disabled = K.busy;
    return sec("관리", null, field("폴더", where), acts);
  }

  // ---------------------------------------------------------------- view
  function renderView() {
    const table = $("#table");
    table.className = "v4s-wrap v4k-wrap";
    $("#stats").replaceChildren(); $("#chips").replaceChildren(); $("#controls").replaceChildren();
    if (K.state === "idle") { K.state = "loading"; queueMicrotask(load); }
    const doc = K.doc && K.sources.find((x) => x.id === K.doc);
    if (doc) {
      const d = current(doc.id);
      headExtras({ count: `부분 ${num(d?.chunk_count ?? doc.chunk_count)}개`, back: [`자료 · ${doc.display_name}`, () => { K.doc = ""; K.notice = ""; paint(); }] });
    } else headExtras({ count: K.state === "ready" ? `자료 ${K.total} · 위키 ${K.pages.filter((p) => p.review_state !== "deleted").length}` : "" });
    if (K.state === "loading") { table.replaceChildren(el("div", "empty", "불러오는 중…")); $("#detail").replaceChildren(); return; }
    if (K.state === "off" || K.state === "error") {
      const e = el("div", "empty");
      e.append(el("p", "", K.state === "off" ? "이 서버에서는 지식 기능이 꺼져 있거나 아직 없어요." : "지식을 불러오지 못했어요."));
      if (K.state === "error") e.append(button("다시 시도", "", null, () => { K.state = "loading"; void load(); }));
      table.replaceChildren(e);
      $("#detail").replaceChildren(); return;
    }
    if (doc) return renderDoc(table);
    const seg = el("div", "v4s-seg v4k-seg");
    const docsN = K.fd.state === "ready" ? `${K.fd.items.length}${K.fd.next ? "+" : ""}` : "";
    for (const [k, label, n] of [["sources", "자료", K.total], ["wiki", "위키", K.pages.filter((p) => p.review_state !== "deleted").length], ["docs", "기억 문서", docsN]]) {
      const b = el("button", K.tab === k ? "on" : ""); b.type = "button"; b.setAttribute("aria-pressed", String(K.tab === k));
      b.append(label); if (K.tab === k && n !== "") b.append(el("output", "", String(n))); // the count sits next to the open tab only
      b.addEventListener("click", () => { K.tab = k; K.notice = ""; K.remove = null; paint(); });
      seg.append(b);
    }
    const holder = el("div", "v4k-body");
    if (K.tab === "wiki") renderWiki(holder); else if (K.tab === "docs") renderDocs(holder); else renderSources(holder);
    table.replaceChildren(seg, ...holder.childNodes);
  }

  return {
    reset: () => { clearTimeout(extractTimer); K.extract = null; sourcePages.reset(); loads.cancel(); sourceBusy = false; sourceError = ""; K.sources = []; K.total = 0; strategies = null;
      Object.assign(K.fd, { state: "idle", items: [], next: "", busy: false, sel: "", del: null }); Object.assign(K, { state: "idle", doc: "", notice: "", remove: null, edit: null, docs: {} }); },
    views: { knowledge: {
      title: () => (K.doc ? K.sources.find((x) => x.id === K.doc)?.display_name || "지식" : "지식"),
      sub: () => (K.doc ? "AI는 문서를 통째로 읽지 않고 이 부분 단위로 찾아 써요. 새 버전은 다시 확인해야 AI가 써요." : "올려 둔 문서를 내 AI가 찾아 써요. 올릴 때 내용을 확인하면 바로 쓰여요."),
      render: renderView, keepDetail: true,
      head: () => K.doc
        ? [button("새 버전 올리기", "", null, () => { const s = K.sources.find((x) => x.id === K.doc); if (s) pickFile(s); })]
        : [button("기억 후보 만들기", "", null, () => openAdd()), button("+ 파일 올리기", "primary", null, () => pickFile(null))]
    } }
  };
};
// One shape for a file-extraction answer: the first POST ({job_id, status, delta, …}) or a job read ({id, status, revision, job: {…, delta}}).
// Re-extraction counts (delta: chunks_total/unchanged/changed/removed, removed_sources) show only when the server sends them.
window.ArkismKnowledge.extractState = (r = {}) => {
  const j = r.job || r, phase = { pending: "queued", retry: "queued", waiting: "queued", running: "running", done: "done", cancelled: "cancelled", failed: "failed" }[r.status] || "queued";
  return { id: r.id || r.job_id || "", revision: r.revision || 0, phase, active: !r.job_id && !r.id ? false : phase === "queued" || phase === "running",
    total: j.total || 0, next: j.next || 0, created: j.candidates_created || 0, skipped: (j.skipped || []).length, jobError: phase === "failed" ? j.error || "failed" : "",
    doc: r.document_id || j.document || "", delta: window.ArkismKnowledge.delta(j.delta || r.delta) };
};
// A server delta (chunks_total/unchanged/changed/removed, removed_sources) in the screen's words; null when the server sent none.
window.ArkismKnowledge.delta = (d) => (d?.chunks_total != null ? { total: d.chunks_total, unchanged: d.chunks_unchanged || 0, changed: d.chunks_changed || 0, removed: d.chunks_removed || 0,
  left: (d.removed_sources || []).reduce((n, x) => n + (x.candidate_ids?.length || 0), 0) } : null);
// Cuts text into parts of at most max UTF-8 bytes (the server's per-file cap): at blank lines first,
// then at line ends, then between characters. Parts that hold only spaces are dropped.
window.ArkismKnowledge.splitText = (text, max = 64 << 10) => {
  const enc = new TextEncoder(), size = (s) => enc.encode(s).length;
  const cuts = [/(?<=\n[ \t]*\n)/, /(?<=\n)/, /(?<=[\s\S])/u];
  const pack = (str, level) => {
    if (size(str) <= max) return [str];
    const out = [];
    let cur = "", bytes = 0;
    for (const piece of str.split(cuts[level])) {
      for (const p of size(piece) > max ? pack(piece, level + 1) : [piece]) {
        const b = size(p);
        if (cur && bytes + b > max) { out.push(cur); cur = ""; bytes = 0; }
        cur += p; bytes += b;
      }
    }
    if (cur) out.push(cur);
    return out;
  };
  return pack(String(text), 0).filter((p) => p.trim());
};
// The add window's optional fields in the server's words; empty ones stay out so the space defaults apply.
// text: the paste tab (metadata lines "name=value"); otherwise the file tab (one reader to try).
window.ArkismKnowledge.ingestExtras = (A, text) => {
  const out = {}, tags = [...new Set(String(A.tags || "").split(/[,，]/).map((t) => t.trim()).filter(Boolean))];
  if (tags.length) out.tags = tags;
  if (String(A.context || "").trim()) out.context = A.context.trim();
  const when = A.when ? new Date(A.when) : null; // datetime-local is the person's own clock
  if (when && !Number.isNaN(when.getTime())) out.timestamp = when.toISOString();
  if (A.strategy) out.strategy = A.strategy;
  if (text) {
    const meta = {};
    for (const line of String(A.meta || "").split("\n")) { const m = /^\s*([^=:]+?)\s*[=:]\s*(.*?)\s*$/.exec(line); if (m) meta[m[1]] = m[2]; }
    if (Object.keys(meta).length) out.metadata = meta;
  } else if (A.parser) out.parsers = [A.parser];
  return out;
};
// 기억 문서 filters over the loaded list: q in the name, id or tags; from/to as YYYY-MM-DD against the last upload.
// shortcut: the day is the server's UTC day and only loaded pages are searched; a server-side text query would replace this.
window.ArkismKnowledge.filterDocs = (items, { q = "", from = "", to = "" } = {}) => {
  const t = q.trim().toLowerCase();
  return items.filter((d) => {
    const day = String(d.updated_at || d.created_at || "").slice(0, 10);
    return (!t || [d.label, d.id, ...(d.tags || [])].some((x) => String(x || "").toLowerCase().includes(t))) && (!from || day >= from) && (!to || day <= to);
  });
};
// What deleting a document touches, counted like the server does: waiting candidates are rejected, approved ones stay as memories.
window.ArkismKnowledge.deleteImpact = (chunks) => {
  const seen = new Map();
  for (const c of chunks || []) for (const x of c.candidates || []) seen.set(x.id, x.status);
  const st = [...seen.values()];
  return { waiting: st.filter((x) => x === "candidate").length, kept: st.filter((x) => x === "approved" || x === "active").length };
};
