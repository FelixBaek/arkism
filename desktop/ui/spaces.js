// @ts-check
// 설정 › 기억 공간, below the space's own settings: moving a space (bundle export/import, also as a template), its name
// and aliases, its project id (rename, preview first), cloning it, and every space of the workspace (GET /v1/spaces),
// searchable, where a person deletes one or several spaces (POST /v1/spaces/delete, after typing a confirm). Over /v1/spaces/{workspace}/{project}/…:
// export · import · clone answer with a job read until done; aliases and the name are a person's own config writes.
// Nothing imported or cloned is saved directly: memories land as candidates, a template's rules and outlines as proposals.
(function (root) {
  "use strict";
  // ------------------------------------------------------------ pure helpers (node tests require them)
  const ALIAS = /^[a-z0-9][a-z0-9_.-]{0,62}$/;
  // what a person may pick to clone (spaces.go spaceParts); config and data are the server's default
  const PARTS = [["config", "설정"], ["data", "기억"], ["history", "이력"]], MORE_PARTS = [["observations", "정리된 관찰"], ["knowledge", "자료"], ["attachments", "첨부 파일"]];
  // what an imported document does when the space already has one of that name (space_documents.go docConflict*)
  const DOC_CONFLICT = [["replace", "새 내용으로 바꾸기"], ["skip", "건너뛰기"], ["new_id", "둘 다 두기"]];
  const SECTION = { config: "설정", directives: "규칙", mental_models: "정리 틀", memories: "기억", observations: "정리된 관찰" };
  // The first alias is the one shown first (대표). The server appends an added alias, so making one the first takes
  // the ones in front of it out and back in, after it.
  function primaryMoves(aliases, pick) {
    const at = aliases.indexOf(pick);
    return at > 0 ? aliases.slice(0, at) : [];
  }
  // What a bundle file holds, read before it is sent: its manifest line (format checked against the server's
  // template schema), each section's count, the settings keys and the names of rules and outlines.
  function bundlePreview(text, schema) {
    const lines = String(text || "").split("\n").filter((l) => l.trim());
    let manifest;
    try { manifest = JSON.parse(lines[0] || "")?.manifest; } catch (_) { manifest = null; }
    if (!manifest || typeof manifest !== "object") return { error: "not_bundle" };
    if (schema?.["x-format"] && manifest.format !== schema["x-format"]) return { error: "not_bundle" };
    if (schema?.["x-format-version"] && manifest.format_version > schema["x-format-version"]) return { error: "newer" };
    const out = { template: !!manifest.template, sections: (manifest.sections || []).filter((s) => s && typeof s.name === "string").map((s) => ({ name: s.name, count: Number(s.count) || 0 })), settings: [], names: [] };
    for (const l of lines.slice(1)) {
      let v; try { v = JSON.parse(l); } catch (_) { continue; }
      if (v?.config && typeof v.config === "object") out.settings = Object.keys(v.config).filter((k) => { const x = v.config[k]; return x !== "" && x !== null && !(Array.isArray(x) && !x.length); });
      const r = v?.record;
      const name = r?.directive?.title || r?.mental_model?.name;
      if (typeof name === "string" && out.names.length < 20) out.names.push(name);
    }
    return out;
  }
  // the list filter: project id, shown name or an alias contains the words typed (any case)
  function matchSpace(s, q) {
    const words = String(q || "").toLowerCase().split(/\s+/).filter(Boolean);
    const hay = [s.project_id, s.config?.display_name, ...(s.config?.aliases || []).map((a) => `@${a}`)].join(" ").toLowerCase();
    return words.every((w) => hay.includes(w));
  }
  // a bulk delete answer → the ids that went and the ones that did not, with their error codes
  function deleteOutcome(batch) {
    const done = [], failed = [];
    for (const r of batch?.results || []) (r.error ? failed : done).push(r.error ? [r.project_id, r.error] : r.project_id);
    return { done, failed };
  }
  const lib = { ALIAS, PARTS, MORE_PARTS, DOC_CONFLICT, primaryMoves, bundlePreview, matchSpace, deleteOutcome };

  function create(ctx) {
    const { state, el, button, request, query, notice, render, go, pickFile, saveFile, spaceJob } = ctx;
    const ask = (...a) => globalThis.ask(...a), askText = (...a) => globalThis.askText(...a), askTyped = (...a) => globalThis.askTyped(...a);
    const human = () => state.caps?.actions?.review_decide !== false; // these writes take people only; an agent token never sees them
    const S = { busy: false, line: "", warn: false, review: false, key: "", view: null, list: null, listErr: "", next: "", preview: null, parts: new Set(["config", "data"]), target: "", find: "", picked: new Set(), newID: "", plan: null,
      docs: null, pickedDocs: new Set(), conflict: "replace" };
    const scopeKey = () => JSON.stringify([state.server, state.scope?.workspace_id, state.scope?.project_id]);
    const enc = encodeURIComponent, data = (node) => { node.dataset.noI18n = ""; return node; }; // server text and ids stay as they are
    const spacePath = (p = state.scope?.project_id) => {
      const w = state.scope?.workspace_id;
      if (!w || !p) throw new Error("no_space");
      return `/v1/spaces/${enc(w)}/${enc(p)}`;
    };
    const answer = (at) => (S.line && (S.at || "move") === at ? [el("p", `pg-hint${S.warn ? " warn" : ""}`, S.line)] : []);
    const redraw = () => { if (state.view === "settings" && state.settingsTab === "space") render(false); };
    const show = (line, warn = false, review = false) => { if (!S.busy) S.at = "move"; Object.assign(S, { line, warn, review }); redraw(); };
    const ERR = { access_denied: "이 계정에는 기억을 밖으로 내보낼 권한이 없어요. 서버 관리자에게 'external:space_bundle' 권한을 받아 주세요.",
      invalid_request: "Arkism에서 내보낸 기억 공간 파일인지 확인해 주세요.", payload_too_large: "파일이 너무 커요. 8MB까지 가져올 수 있어요.",
      no_space: "작업 공간과 프로젝트가 정해지지 않았어요. 설정 › 고급 › 연결에서 범위를 먼저 정해 주세요.",
      conflict: "설정을 저장한 적 있는 공간에는 템플릿을 넣을 수 없어요. 새 공간에 가져와 주세요.", handle_taken: "이미 다른 공간이 쓰는 별칭이에요.",
      revision_conflict: "그사이 다른 곳에서 바뀌었어요. 새로 불러왔어요.", not_found: "찾을 수 없는 공간이에요.",
      space_busy: "작업이 돌고 있거나 법적 보존 중인 공간이에요. 작업이 끝난 뒤 다시 지워 주세요.", id_taken: "이미 있는 프로젝트 ID예요. 다른 이름을 적어 주세요.",
      unavailable: "이 서버에서는 지금 할 수 없어요. 서버 관리자에게 물어봐 주세요." };
    const errText = (e) => ERR[e?.message] || "처리하지 못했어요. 연결 상태를 확인하고 다시 시도해 주세요.";
    // at: the group that shows the answer line (move, name or list)
    async function run(work, at = "move") {
      if (S.busy) return;
      S.busy = true; S.at = at;
      try { await work(); } catch (e) { show(errText(e), true); }
      S.busy = false;
      redraw();
    }
    // the current space's stored settings (revision, name, aliases) and the workspace's spaces, read once per scope
    async function load() {
      const key = scopeKey(); S.key = key;
      const [view, list] = await Promise.allSettled([request(`${spacePath()}/config`), request(`/v1/spaces?${query({ limit: 50 })}`)]);
      if (key !== scopeKey()) return;
      S.view = view.status === "fulfilled" ? view.value : false;
      S.list = list.status === "fulfilled" ? list.value.items || [] : []; S.next = list.status === "fulfilled" ? list.value.next || "" : "";
      S.listErr = list.status === "rejected" ? "공간 목록을 불러오지 못했어요. 이 계정의 읽기 권한을 확인해 주세요." : "";
      redraw();
    }
    const reload = () => { S.key = ""; S.view = null; return load().catch(() => {}); };

    // ---------------------------------------------------------- 기억 공간 옮기기 (also as a template)
    // docs: the space's memories and settings plus only the picked documents (the documents part takes people only)
    const exportSpace = (template, docs) => run(async () => {
      show("내보낼 파일을 만드는 중…");
      const body = template ? { template: true } : docs ? { parts: ["config", "data", "documents"], document_ids: [...S.pickedDocs] } : {};
      const job = await spaceJob("exports", await request(`${spacePath()}/export`, { method: "POST", body: JSON.stringify(body) }));
      const text = await request(`/v1/spaces/exports/${enc(job.id)}/download?${query()}`, { raw: true });
      saveFile(`arkism-${template ? "template" : "space"}-${state.scope.project_id}.jsonl`, text, "application/x-ndjson");
      show(template ? "템플릿 파일 다운로드를 시작했어요. 기억은 빠지고 설정과 규칙·정리 틀만 들어 있어요."
        : "파일 다운로드를 시작했어요. 다른 Arkism의 설정 › 고급에서 가져오세요. 기억 내용이 들어 있으니 안전한 곳에 두세요.");
    });
    const importSpace = async () => {
      const bundle = await pickFile(".jsonl,application/x-ndjson", 8 << 20, () => show(ERR.payload_too_large, true));
      if (!bundle) return;
      void run(async () => {
        const path = spacePath();
        if (!(await ask("이 파일의 기억을 가져올까요? 바로 저장되지 않고 모두 '확인해 주세요'에 후보로 들어가요.", "가져오기"))) return;
        show("가져오는 중…");
        const job = await spaceJob("imports", await request(`${path}/import`, { method: "POST", body: JSON.stringify({ bundle, import_as: "candidate", document_conflict: S.conflict }) }));
        const r = job.report || {};
        show(`후보 ${r.imported || 0}개를 가져왔어요. 이미 있는 기억 ${r.merged || 0}개는 건너뛰었고, 받지 않은 것 ${(r.rejected || 0) + (r.skipped || []).reduce((n, x) => n + (x.count || 0), 0)}개예요.`, false, (r.imported || 0) > 0);
      });
    };
    // 템플릿 가져오기: pick any bundle, see what it holds, then take only its settings and definitions (as_template)
    const browseTemplate = async () => {
      const bundle = await pickFile(".jsonl,application/x-ndjson", 8 << 20, () => show(ERR.payload_too_large, true));
      if (!bundle) return;
      const schema = await request(`/v1/spaces/template-schema?${query()}`).catch(() => null); // an older server: the file is still read, unchecked
      const p = bundlePreview(bundle, schema);
      if (p.error) { show(p.error === "newer" ? "더 새 버전의 Arkism에서 만든 파일이에요. 서버를 먼저 업데이트해 주세요." : ERR.invalid_request, true); return; }
      S.preview = { ...p, bundle }; show("");
    };
    const importTemplate = () => run(async () => {
      const bundle = S.preview?.bundle;
      if (!bundle) return;
      show("가져오는 중…");
      const start = await request(`${spacePath()}/import`, { method: "POST", body: JSON.stringify({ bundle, as_template: true, import_as: "candidate" }) })
        .catch((e) => { throw e?.message === "revision_conflict" ? new Error("conflict") : e; }); // a space with stored settings refuses a template
      const job = await spaceJob("imports", start);
      S.preview = null; void reload();
      show(`템플릿을 넣었어요. 규칙·정리 틀 ${job.report?.imported || 0}개는 '확인해 주세요'에서 승인해야 쓰여요.`, false, (job.report?.imported || 0) > 0);
    });
    function previewCard(kit) {
      const p = S.preview, card = el("div", "d-action sp-preview");
      card.append(el("strong", "", p.template ? "템플릿 파일이에요" : "기억 공간 파일이에요. 기억은 빼고 설정과 규칙·정리 틀만 가져와요."));
      const counts = p.sections.filter((s) => s.name in SECTION && (p.template || s.name !== "memories"));
      if (counts.length) card.append(el("p", "meta", counts.map((s) => `${SECTION[s.name]} ${s.count}`).join(" · ")));
      if (p.settings.length) card.append(el("small", "", "들어 있는 설정"), data(el("p", "meta mono", p.settings.join(", "))));
      if (p.names.length) card.append(el("small", "", "규칙·정리 틀"), data(el("p", "meta", p.names.join(", "))));
      const row = el("div", "v4s-actions"), ok = button("이 공간에 템플릿으로 넣기", "primary", null, importTemplate);
      ok.disabled = S.busy || (S.view?.revision || 0) > 0;
      row.append(ok, button("취소", "ghost", null, () => { S.preview = null; redraw(); }));
      card.append(row);
      if ((S.view?.revision || 0) > 0) card.append(el("p", "pg-hint warn", ERR.conflict));
      return card;
    }
    // the space's documents, read when 자세히 · 문서 opens the first time (one page: an export picks at most 200)
    async function loadDocs() {
      S.docs = "loading";
      try { S.docs = (await request(`/v1/files/memories/documents?${query({ limit: 50 })}`)).items || []; } catch (_) { S.docs = false; }
      redraw();
    }
    function docMoves() {
      if (S.docs === null) void loadDocs();
      const list = el("div", "v4s-actions");
      if (S.docs === false) list.append(el("p", "pg-hint warn", "문서 목록을 불러오지 못했어요."));
      else if (Array.isArray(S.docs) && !S.docs.length) list.append(el("p", "pg-hint", "이 공간에는 파일에서 읽은 문서가 없어요."));
      for (const d of Array.isArray(S.docs) ? S.docs : []) {
        const lab = el("label", "sp-check"), c = el("input"); c.type = "checkbox"; c.checked = S.pickedDocs.has(d.id);
        c.addEventListener("change", () => { if (c.checked) S.pickedDocs.add(d.id); else S.pickedDocs.delete(d.id); redraw(); });
        lab.append(c, data(el("span", "", d.label || d.id))); list.append(lab);
      }
      const go = button(S.pickedDocs.size ? `기억과 고른 문서 ${S.pickedDocs.size}개 내보내기` : "내보낼 문서를 골라 주세요", "", null, () => exportSpace(false, true));
      go.disabled = S.busy || !S.pickedDocs.size;
      const sel = el("select", "ag-select"); sel.setAttribute("aria-label", "같은 이름의 문서가 있으면");
      for (const [k, name] of DOC_CONFLICT) { const o = el("option", "", name); o.value = k; o.selected = k === S.conflict; sel.append(o); }
      sel.addEventListener("change", () => { S.conflict = sel.value; });
      const f = el("label", "field wide", "가져올 때 같은 이름의 문서가 있으면"); f.append(sel);
      return [el("p", "pg-hint", "고른 문서는 기억·설정과 함께 한 파일로 나가요."), list, go, f];
    }
    function moveGroup(kit) {
      const g = kit.group("기억 공간 옮기기", "이 공간의 기억을 파일로 내보내거나, 받은 파일을 가져와요. 가져온 기억은 내가 확인해야 저장돼요.");
      const row = el("div", "v4s-actions");
      row.append(button("기억 내보내기", "", null, () => exportSpace(false)));
      if (human()) row.append(button("기억 가져오기", "", null, importSpace)); // the import route takes people only; an agent token never sees it
      for (const b of row.children) b.disabled = S.busy;
      g.append(row);
      if (human()) g.append(kit.more("자세히 · 문서", ...docMoves()));
      const t1 = button("템플릿으로 내보내기", "", null, () => exportSpace(true)), t2 = button("템플릿 가져오기", "", null, browseTemplate);
      t1.disabled = t2.disabled = S.busy;
      const tpl = [kit.line("템플릿으로 내보내기", "기억은 빼고 설정과 규칙·정리 틀만 담아요. 다른 공간을 같은 방식으로 시작할 때 써요.", t1)];
      if (human()) tpl.push(kit.line("템플릿 가져오기", "파일을 고르면 무엇이 들어 있는지 먼저 보여 줘요. 설정을 저장한 적 없는 공간에만 넣을 수 있어요.", t2));
      if (S.preview) tpl.push(previewCard(kit));
      g.append(kit.more("자세히 · 템플릿", ...tpl));
      if (S.preview) g.lastElementChild.open = true;
      g.append(...answer("move"));
      if (S.review) g.append(button("후보 확인하러 가기", "primary", null, () => go("memories", "candidate")));
      return g;
    }

    // ---------------------------------------------------------- 이름과 별칭 (this space)
    const writeConfig = (body, done) => run(async () => {
      try { await request(`${spacePath()}/config`, { method: "PATCH", body: JSON.stringify({ expected_revision: S.view?.revision || 0, ...body }) }); notice(done); }
      finally { await reload(); }
    });
    const rename = async () => {
      const now = S.view?.config?.display_name || "";
      const name = (await askText("보이는 이름\n프로젝트 ID는 그대로예요.", now, { okLabel: "바꾸기", primary: true }))?.trim();
      if (name != null && name !== now) writeConfig({ display_name: name }, "이름을 바꿨어요.");
    };
    // one alias at a time, in order: each answer is the space with that change
    const aliasCall = (alias, add) => request(`${spacePath()}/aliases${add ? "" : `/${enc(alias)}`}`, add ? { method: "POST", body: JSON.stringify({ alias }) } : { method: "DELETE" });
    const aliasRun = (work, done) => run(async () => { try { await work(); notice(done); } finally { await reload(); } });
    const addAlias = async () => {
      const alias = (await askText("새 별칭\n영어 소문자·숫자·- _ . 로 63자까지. @별칭으로 이 공간을 부를 수 있어요.", "", { okLabel: "추가", primary: true }))?.trim().toLowerCase();
      if (!alias) return;
      if (!ALIAS.test(alias)) { show("별칭은 영어 소문자나 숫자로 시작하고, 영어 소문자·숫자·- _ . 만 쓸 수 있어요.", true); return; }
      aliasRun(() => aliasCall(alias, true), "별칭을 추가했어요.");
    };
    const makePrimary = (aliases, a) => aliasRun(async () => { for (const x of primaryMoves(aliases, a)) { await aliasCall(x, false); await aliasCall(x, true); } }, "대표 별칭을 바꿨어요.");
    const dropAlias = async (a) => { if (await ask(`별칭 ‘${a}’를 뺄까요?\n@${a}로 부르던 곳은 이 공간을 찾지 못해요.`, "빼기")) aliasRun(() => aliasCall(a, false), "별칭을 뺐어요."); };
    function nameGroup(kit) {
      const g = kit.group("이름과 별칭", "목록과 화면에 보이는 이름, 그리고 이 공간을 부르는 다른 이름(@별칭)이에요.");
      if (S.view === false) { g.append(el("p", "pg-hint", "이 계정으로는 공간 설정을 볼 수 없어요.")); return g; }
      const c = S.view?.config || {}, can = human() && !!S.view;
      const nb = button("이름 바꾸기", "small", null, rename), right = el("span", "v4s-actions"); nb.disabled = !can || S.busy;
      right.append(data(el("b", "", c.display_name || state.scope?.project_id || "—")), nb);
      g.append(kit.line("보이는 이름", "", right));
      const aliases = c.aliases || [], chips = el("div", "sp-chips");
      aliases.forEach((a, i) => {
        const chip = el("span", "chip sp-chip"); chip.append(data(el("span", "", `@${a}`)));
        if (i === 0 && aliases.length > 1) chip.append(el("b", "sp-here", "대표"));
        if (can) {
          if (i > 0) { const up = button("대표로", "link-btn", null, () => makePrimary(aliases, a)); up.setAttribute("aria-label", `${a} 대표로`); up.disabled = S.busy; chip.append(up); }
          const x = button("빼기", "link-btn", null, () => dropAlias(a)); x.setAttribute("aria-label", `${a} 빼기`); x.disabled = S.busy; chip.append(x);
        }
        chips.append(chip);
      });
      if (!aliases.length) chips.append(el("p", "meta", "별칭이 없어요."));
      const add = button("별칭 추가", "small", null, addAlias); add.disabled = !can || S.busy;
      g.append(kit.line("별칭", "", add), chips);
      g.append(...answer("name"));
      const more = kit.more("자세히 · 프로젝트 ID 바꾸기", ...idChange(kit, can));
      if (S.plan) more.open = true;
      g.append(more);
      return g;
    }
    // 프로젝트 ID 바꾸기: POST …/rename, first execute=false (what would move), then the move itself after a confirm
    const renameCall = (execute) => request(`${spacePath()}/rename`, { method: "POST", body: JSON.stringify({ project_id: S.plan?.to || S.newID.trim(), execute }) })
      .catch((e) => { throw new Error(e?.message === "revision_conflict" ? "id_taken" : e?.message === "dependency_unavailable" ? "unavailable" : e?.message); });
    const previewID = () => run(async () => {
      const to = S.newID.trim();
      if (!to || to === state.scope?.project_id || to.startsWith("@")) { show("새 프로젝트 ID를 적어 주세요. 지금 ID와 달라야 하고 @로 시작할 수 없어요.", true); return; }
      const r = await renameCall(false);
      S.plan = { to, rows: Object.values(r.records || {}).reduce((n, x) => n + x, 0), skipped: r.skipped || [] }; show("");
    }, "name");
    const applyID = () => run(async () => {
      const { to } = S.plan, from = state.scope?.project_id;
      if (!(await ask(`프로젝트 ID를 ‘${from}’에서 ‘${to}’로 바꿀까요?\n이 공간의 기억과 설정이 모두 새 ID로 옮겨져요. 에이전트와 다른 기기도 새 ID로 바꿔야 이 공간을 찾아요.`, "바꾸기"))) return;
      const r = await renameCall(true);
      S.plan = null; S.newID = "";
      show(`프로젝트 ID를 ‘${to}’로 바꿨어요. 설정 › 연결에서 프로젝트를 ‘${to}’로 바꿔 주세요.${r.skipped?.length ? " 권한 표 몇 개는 서버 관리자가 옮겨야 해요." : ""}`);
      void reload();
    }, "name");
    function idChange(kit, can) {
      const input = el("input", "sp-newid"); input.value = S.newID; input.maxLength = 128; input.placeholder = "새 프로젝트 ID"; input.setAttribute("aria-label", "새 프로젝트 ID");
      input.addEventListener("input", () => { S.newID = input.value; if (S.plan) { S.plan = null; redraw(); } });
      const check = button("미리 보기", "small", null, previewID); check.disabled = S.busy;
      const out = [el("p", "pg-hint", "보이는 이름과 달리 프로젝트 ID는 에이전트와 다른 기기가 이 공간을 부르는 이름이에요. 먼저 무엇이 옮겨지는지 보여 줘요."), ...(can ? [kit.line("새 ID", "", input), check] : [])];
      if (S.plan) {
        const card = el("div", "d-action sp-preview");
        card.append(el("strong", "", `기록 ${S.plan.rows.toLocaleString()}개가 ‘${S.plan.to}’로 옮겨져요.`));
        if (S.plan.skipped.length) card.append(el("p", "meta", "구성원·권한 표는 이 계정으로 옮길 수 없어 예전 ID에 남아요. 서버 관리자가 memoryd --space-rename 으로 옮길 수 있어요."));
        const row = el("div", "v4s-actions"), ok = button("ID 바꾸기", "primary", null, applyID); ok.disabled = S.busy;
        row.append(ok, button("취소", "ghost", null, () => { S.plan = null; redraw(); }));
        card.append(row); out.push(card);
      }
      return out;
    }

    // ---------------------------------------------------------- 공간 복제
    const cloneSpace = () => run(async () => {
      const target = S.target.trim();
      if (!target || target === state.scope?.project_id) { show("복제할 새 공간의 프로젝트 이름을 적어 주세요. 지금 공간과 달라야 해요.", true); return; }
      const parts = [...PARTS, ...MORE_PARTS].map(([k]) => k).filter((k) => S.parts.has(k));
      if (!parts.length) { show("복제할 것을 하나 이상 골라 주세요.", true); return; }
      if (!(await ask(`이 공간을 ‘${target}’에 복제할까요?\n복제한 기억은 그 공간의 '확인해 주세요'에 후보로 들어가요.`, "복제"))) return;
      show("복제하는 중…");
      const job = await spaceJob("exports", await request(`${spacePath()}/clone`, { method: "POST", body: JSON.stringify({ target: { workspace_id: state.scope.workspace_id, project_id: target }, parts }) }));
      const r = job.report || {};
      show(`‘${target}’에 후보 ${r.imported || 0}개를 넣었어요. 그 공간으로 바꾼 뒤 '확인해 주세요'에서 승인해 주세요.`);
      void reload();
    });
    function cloneGroup(kit) {
      const g = kit.group("공간 복제", "이 공간을 다른 프로젝트 이름으로 복사해요. 고른 것만 가고, 기억은 그 공간에서 내가 확인해야 저장돼요.");
      const input = el("input"); input.value = S.target; input.maxLength = 128; input.placeholder = "새 프로젝트 이름"; input.setAttribute("aria-label", "새 프로젝트 이름");
      input.addEventListener("input", () => { S.target = input.value; });
      const box = (list) => { const row = el("div", "v4s-actions"); for (const [k, l] of list) { const lab = el("label", "sp-check"), c = el("input"); c.type = "checkbox"; c.checked = S.parts.has(k); c.addEventListener("change", () => { if (c.checked) S.parts.add(k); else S.parts.delete(k); }); lab.append(c, el("span", "", l)); row.append(lab); } return row; };
      const run = button("복제하기", "", null, cloneSpace), act = el("div", "v4s-actions"); run.disabled = S.busy || !human(); act.append(run);
      g.append(kit.line("새 공간", "같은 작업 공간 안에 만들어요.", input), box(PARTS), kit.more("자세히 · 더 고르기", box(MORE_PARTS), el("p", "pg-hint", "이력은 모델 호출·활동 기록이에요. 사람 계정만 복제할 수 있어요.")), act);
      return g;
    }

    // ---------------------------------------------------------- 모든 공간
    async function moreSpaces() {
      const page = await request(`/v1/spaces?${query({ limit: 50, after: S.next })}`).catch(() => null);
      if (!page) { S.listErr = "공간 목록을 더 불러오지 못했어요."; redraw(); return; }
      S.list = [...(S.list || []), ...(page.items || [])]; S.next = page.next || ""; redraw();
    }
    // 지우기: one space after typing its project id, several after typing how many; the server deletes each on its own
    const deleteSpaces = (ids) => run(async () => {
      const ok = ids.length === 1
        ? await askTyped(`‘${ids[0]}’ 공간을 지울까요?\n이 공간의 기억·자료·첨부 파일·설정이 모두 지워지고 되돌릴 수 없어요. 지우려면 프로젝트 ID를 그대로 적어 주세요.`, ids[0], "지우기")
        : await askTyped(`고른 공간 ${ids.length}개를 지울까요?\n${ids.join(", ")}\n안의 기억·자료·첨부 파일·설정이 모두 지워지고 되돌릴 수 없어요. 지우려면 공간 수(${ids.length})를 적어 주세요.`, String(ids.length), "모두 지우기");
      if (!ok) return;
      show("지우는 중…");
      const { done, failed } = deleteOutcome(await request(`/v1/spaces/delete?${query()}`, { method: "POST", body: JSON.stringify({ spaces: ids.map((id) => ({ project_id: id, confirm: id })) }) }));
      S.list = (S.list || []).filter((s) => !done.includes(s.project_id));
      for (const id of done) S.picked.delete(id);
      const why = (code) => ERR[code === "revision_conflict" ? "space_busy" : code] || "처리하지 못했어요.";
      show([done.length ? `공간 ${done.length}개를 지웠어요.` : "", ...failed.map(([id, code]) => `‘${id}’: ${why(code)}`)].filter(Boolean).join(" "), failed.length > 0);
    }, "list");
    // 기억만 비우기: the space's memories, candidates and observations go after typing its project id; settings, documents, wiki and attachments stay
    const clearMemories = (id) => run(async () => {
      if (!(await askTyped(`‘${id}’ 공간의 기억만 비울까요?\n기억·후보·관찰이 모두 지워지고 되돌릴 수 없어요. 설정·문서·위키·첨부 파일은 그대로 남아요. 비우려면 프로젝트 ID를 그대로 적어 주세요.`, id, "비우기"))) return;
      show("비우는 중…");
      const r = await request(`${spacePath(id)}/memories?confirm=${enc(id)}`, { method: "DELETE" });
      const n = Object.values(r?.deleted || {}).reduce((a, b) => a + (Number(b) || 0), 0);
      const row = (S.list || []).find((x) => x.project_id === id);
      if (row) row.counts = { ...(row.counts || {}), memories: 0 };
      show(`‘${id}’ 공간에서 기록 ${n}개를 지웠어요.`);
    }, "list");
    const day = (v) => { const t = v ? new Date(v) : null; return t && !Number.isNaN(t.getTime()) ? t.toLocaleDateString() : "—"; };
    function spaceRow(s, can, canClear) {
      const here = s.project_id === state.scope?.project_id, name = s.config?.display_name || s.project_id;
      const t = el("div"), meta = el("small"), r = el("div", "setting sp-row");
      meta.append(data(el("span", "mono", s.project_id)), el("span", "", ` · 기억 ${(s.counts?.memories || 0).toLocaleString()}개`), ...(s.config?.aliases?.length ? [data(el("span", "", ` · ${s.config.aliases.map((a) => `@${a}`).join(" ")}`))] : []));
      const when = el("small", "sp-when"); when.append(el("span", "", "만든 날 "), data(el("span", "", day(s.created))), el("span", "", " · 마지막 활동 "), data(el("span", "", day(s.last_activity))));
      t.append(data(el("strong", here ? "sp-here" : "", name)), meta, when);
      if (can && !here) { // the space in use is not deleted from here: switch to another one first
        const lab = el("label", "sp-check"), c = el("input"); c.type = "checkbox"; c.checked = S.picked.has(s.project_id); c.setAttribute("aria-label", `${s.project_id} 고르기`);
        c.addEventListener("change", () => { if (c.checked) S.picked.add(s.project_id); else S.picked.delete(s.project_id); redraw(); });
        lab.append(c); r.append(lab);
      } else if (can) r.append(el("span", "sp-gap")); // keeps the names in one column
      const right = el("span", "v4s-actions");
      if (here) right.append(el("span", "sp-here", "지금 공간"));
      if (canClear) { const c = button("기억만 비우기", "small ghost", null, () => clearMemories(s.project_id)); c.setAttribute("aria-label", `${s.project_id} 기억만 비우기`); c.disabled = S.busy; right.append(c); }
      if (!here && can) { const x = button("지우기", "small ghost", null, () => deleteSpaces([s.project_id])); x.setAttribute("aria-label", `${s.project_id} 지우기`); x.disabled = S.busy; right.append(x); }
      r.append(t, right);
      return r;
    }
    function listGroup(kit) {
      const g = kit.group("이 작업 공간의 공간", "기억이 있거나 설정을 저장한 공간이에요. 쓰는 공간은 설정 › 연결에서 바꿔요.");
      if (S.listErr) g.append(el("p", "pg-hint warn", S.listErr));
      if (!S.list) { g.append(el("p", "pg-hint", "불러오는 중…")); return g; }
      if (!S.list.length && !S.listErr) { g.append(el("p", "pg-hint", "아직 공간이 없어요.")); return g; }
      const can = human() && state.caps?.actions?.delete_spaces !== false, canClear = human() && state.caps?.actions?.delete_memories !== false;
      const find = el("input", "sp-find"); find.value = S.find; find.placeholder = "이름·ID·@별칭으로 찾기"; find.setAttribute("aria-label", "공간 찾기");
      const none = el("p", "pg-hint", "찾는 공간이 없어요."), rows = S.list.map((s) => [s, spaceRow(s, can, canClear)]);
      // rows hide in place while typing: a redraw would take the focus out of the box
      const filter = () => { let n = 0; for (const [s, r] of rows) { r.hidden = !matchSpace(s, S.find); n += r.hidden ? 0 : 1; } none.hidden = n > 0; };
      find.addEventListener("input", () => { S.find = find.value; filter(); });
      g.append(find, none, ...rows.map(([, r]) => r));
      filter();
      if (S.next) g.append(button("더 보기", "small", null, moreSpaces));
      g.append(...answer("list"));
      const picked = [...S.picked].filter((id) => S.list.some((s) => s.project_id === id));
      if (can && picked.length) {
        const bar = el("div", "v4s-actions"), go2 = button(`고른 공간 ${picked.length}개 지우기`, "danger", null, () => deleteSpaces(picked)); go2.disabled = S.busy;
        bar.append(go2, button("고르기 취소", "ghost", null, () => { S.picked.clear(); redraw(); }));
        g.append(bar);
      }
      return g;
    }

    function groups(kit) {
      if (S.key !== scopeKey()) { Object.assign(S, { view: null, list: null, preview: null, line: "", review: false, target: "", find: "", picked: new Set(), newID: "", plan: null, docs: null, pickedDocs: new Set() }); void load().catch(() => {}); }
      return [nameGroup(kit), moveGroup(kit), cloneGroup(kit), listGroup(kit)];
    }
    return { groups };
  }
  const api = Object.assign(create, lib);
  root.ArkismSpaces = api;
  if (typeof window === "undefined" && typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
