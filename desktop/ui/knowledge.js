// TOOLS › 지식 (Figma 384:30031 자료 · 384:30671 부분 · 384:31269 위키).
// 자료 = uploaded knowledge sources (GET /v1/knowledge/sources, …/documents). A new version puts the source back
// to draft: a person reviews it before search sees it. 부분 = the current document's heading paths, its versions and
// a test search (POST /v1/knowledge/search) — chunk text is never listed, only search evidence shows it.
// 위키 = pages an AI drafted from sources (GET /v1/wiki/pages): draft → a person approves → reviewed; edited pages lock.
// Removing a source from the wiki asks the server for a dry-run preview first (…/remove?dry_run=1); a server that
// doesn't know it gets a plain confirm instead. Everything read here is data, never instructions.
window.ArkismKnowledge = (ctx) => {
  "use strict";
  const { state, $, el, button, icon, render, request, query, ago, detailShell, headExtras, confirmPop } = ctx;
  const PROFILES = [["design_consultant", "디자인 상담"], ["illustrator", "일러스트"], ["listing_reviewer", "상품 검수"]];
  const SHARED = "default"; // the server's default scope: every agent of this person searches it
  const FORMATS = [["", "전체"], [".docx", "Word"], [".md", "Markdown"], [".txt", "텍스트"], [".json", "JSON"]];
  const DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
  const MIME = { ".md": "text/markdown", ".txt": "text/plain", ".json": "application/json", ".docx": DOCX };
  const KINDS = "Word·Markdown·텍스트·JSON";
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
    pg: { q: "", type: "", sort: "recent", chip: "all" }, pageSel: "", compare: false, edit: null,
    doc: "", docTab: "sections", secSel: 0, verSel: "", search: { profile: SHARED, q: "", busy: false, result: null, error: "" },
    busy: false, notice: "", remove: null, upload: null
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
    invalid_request: "파일을 읽지 못했어요. 내용과 형식을 확인하고 다시 올려 주세요.", payload_too_large: "파일이 너무 커요.", artifact_too_large: "파일이 너무 커요." }[e?.message] || "잠시 문제가 생겼어요. 다시 시도해 주세요.");
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
    const [, p, g, j] = await Promise.allSettled([loadSources(), get("/v1/wiki/pages", { state: "all" }), get("/v1/wiki/graph"), get("/v1/wiki/jobs")]);
    if (!ticket.live()) return;
    if (p.status === "fulfilled" && Array.isArray(p.value?.items)) { K.pages = p.value.items; K.wiki = "ready"; }
    else { K.pages = []; K.wiki = p.status === "rejected" && off(p.reason) ? "off" : "error"; }
    K.links = g.status === "fulfilled" && Array.isArray(g.value?.links) ? g.value.links : [];
    K.jobs = j.status === "fulfilled" && Array.isArray(j.value?.items) ? j.value.items : [];
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
  async function act(work, done) {
    if (K.busy) return;
    K.busy = true; K.notice = ""; paint();
    try { const r = await work(); K.notice = typeof done === "function" ? done(r) : done || ""; }
    catch (e) { K.notice = errText(e); }
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
      form.append(el("h2", "", "내 AI가 이 자료를 쓰게 할까요?"), noI18n(el("p", "mono", name)), ...(note ? [el("p", "", note)] : []),
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
  function pickFile(source) {
    const input = el("input"); input.type = "file"; input.accept = ".docx,.md,.txt,.json,.pdf";
    input.addEventListener("change", async () => {
      const f = input.files?.[0];
      if (!f) return;
      const ext = (f.name.match(/\.[^.]+$/)?.[0] || "").toLowerCase(), word = ext === ".docx";
      if (ext === ".pdf") { K.notice = "PDF는 아직 읽지 못해요. Word(.docx)나 텍스트(.txt)로 저장해서 올려 주세요."; paint(); return; }
      if (!MIME[ext]) { K.notice = `${KINDS} 파일을 올릴 수 있어요. 다른 파일은 텍스트로 저장해서 올려 주세요.`; paint(); return; }
      if (word && f.size > 4 << 20) { K.notice = "파일이 너무 커요. Word 파일은 4MB까지 올릴 수 있어요."; paint(); return; }
      const data = word ? { content_base64: b64(await f.arrayBuffer()) } : { content: await f.text() };
      const parts = word ? [] : window.ArkismKnowledge.splitText(data.content);
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
        const r = await post("/v1/knowledge/ingest", { source_key: same?.display_name || f.name, filename: f.name, mime_type: MIME[ext], ...data, synthetic: false, ...(same ? { expected_revision: same.revision } : {}) });
        const src = r?.source;
        if (choice === "use" && src?.id) await useEverywhere(src.id, src.revision, r.duplicate && same && !needs(same), same?.bindings || []);
        return r;
      }, (r) => choice === "use" ? (r?.duplicate ? "같은 내용이에요. 이제 내 모든 AI가 이 자료를 찾아 써요." : "올렸어요. 이제 내 모든 AI가 이 자료를 찾아 써요.")
        : r?.duplicate ? "같은 내용이라 새 버전을 만들지 않았어요." : "올렸어요. 자료를 열어 확인하고 '쓰게 하기'를 눌러 주세요.");
    });
    input.click();
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

  // ---------------------------------------------------------------- 자료 (384:30031)
  function renderSources(table) {
    const parts = [];
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
      const node = row("v4k-row", K.srcSel === s.id, [text(s.display_name, meta.join(" · "), true), badge(inUse(s) || needs(s) ? srcState(s) : ["꺼짐", "line"])], () => openSource(s.id)); node.dataset.id = s.id; return node;
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
    tools.append(button("새 버전 올리기", "", null, () => pickFile(s)));
    tools.lastChild.disabled = K.busy;
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
      b.append(el("small", "", `#${j + 1}`), noI18n(el("span", "", secs[j])));
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
    let items = K.pages.filter((p) => p.review_state !== "deleted" && (!type || p.type === type));
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
    parts.push(list, el("p", "v4s-foot", `${alive.length}개 중 ${items.length}개 표시 · 잠긴 페이지는 AI가 고치지 않아요`));
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
    box.append(sec("잠금", null, lock));
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
    for (const [k, label, n] of [["sources", "자료", K.total], ["wiki", "위키", K.pages.filter((p) => p.review_state !== "deleted").length]]) {
      const b = el("button", K.tab === k ? "on" : ""); b.type = "button"; b.setAttribute("aria-pressed", String(K.tab === k));
      b.append(label, el("output", "", String(n)));
      b.addEventListener("click", () => { K.tab = k; K.notice = ""; K.remove = null; paint(); });
      seg.append(b);
    }
    const holder = el("div", "v4k-body");
    if (K.tab === "wiki") renderWiki(holder); else renderSources(holder);
    table.replaceChildren(seg, ...holder.childNodes);
  }

  return {
    reset: () => { sourcePages.reset(); loads.cancel(); sourceBusy = false; sourceError = ""; K.sources = []; K.total = 0; Object.assign(K, { state: "idle", doc: "", notice: "", remove: null, edit: null, docs: {} }); },
    views: { knowledge: {
      title: () => (K.doc ? K.sources.find((x) => x.id === K.doc)?.display_name || "지식" : "지식"),
      sub: () => (K.doc ? "AI는 문서를 통째로 읽지 않고 이 부분 단위로 찾아 써요. 새 버전은 다시 확인해야 AI가 써요." : "올려 둔 문서를 내 AI가 찾아 써요. 올릴 때 내용을 확인하면 바로 쓰여요."),
      render: renderView, keepDetail: true,
      head: () => K.doc
        ? [button("새 버전 올리기", "", null, () => { const s = K.sources.find((x) => x.id === K.doc); if (s) pickFile(s); })]
        : [button("+ 파일 올리기", "primary", null, () => pickFile(null))]
    } }
  };
};
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
