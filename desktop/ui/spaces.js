// @ts-check
// 설정 › 기억 공간, below the space's own settings: moving a space (bundle export/import, also as a template), its name
// and aliases, cloning it, and every space of the workspace (GET /v1/spaces). Over /v1/spaces/{workspace}/{project}/…:
// export · import · clone answer with a job read until done; aliases and the name are a person's own config writes.
// Nothing imported or cloned is saved directly: memories land as candidates, a template's rules and outlines as proposals.
(function (root) {
  "use strict";
  // ------------------------------------------------------------ pure helpers (node tests require them)
  const ALIAS = /^[a-z0-9][a-z0-9_.-]{0,62}$/;
  // what a person may pick to clone (spaces.go spaceParts); config and data are the server's default
  const PARTS = [["config", "설정"], ["data", "기억"], ["history", "이력"]], MORE_PARTS = [["observations", "정리된 관찰"], ["knowledge", "자료"], ["attachments", "첨부 파일"]];
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
  const lib = { ALIAS, PARTS, MORE_PARTS, primaryMoves, bundlePreview };

  function create(ctx) {
    const { state, el, button, request, query, notice, render, go, pickFile, saveFile, spaceJob } = ctx;
    const ask = (...a) => globalThis.ask(...a), askText = (...a) => globalThis.askText(...a);
    const human = () => state.caps?.actions?.review_decide !== false; // these writes take people only; an agent token never sees them
    const S = { busy: false, line: "", warn: false, review: false, key: "", view: null, list: null, listErr: "", next: "", preview: null, parts: new Set(["config", "data"]), target: "" };
    const scopeKey = () => JSON.stringify([state.server, state.scope?.workspace_id, state.scope?.project_id]);
    const enc = encodeURIComponent, data = (node) => { node.dataset.noI18n = ""; return node; }; // server text and ids stay as they are
    const spacePath = (p = state.scope?.project_id) => {
      const w = state.scope?.workspace_id;
      if (!w || !p) throw new Error("no_space");
      return `/v1/spaces/${enc(w)}/${enc(p)}`;
    };
    const redraw = () => { if (state.view === "settings" && state.settingsTab === "space") render(false); };
    const show = (line, warn = false, review = false) => { Object.assign(S, { line, warn, review }); redraw(); };
    const ERR = { access_denied: "이 계정에는 기억을 밖으로 내보낼 권한이 없어요. 서버 관리자에게 'external:space_bundle' 권한을 받아 주세요.",
      invalid_request: "Arkism에서 내보낸 기억 공간 파일인지 확인해 주세요.", payload_too_large: "파일이 너무 커요. 8MB까지 가져올 수 있어요.",
      no_space: "작업 공간과 프로젝트가 정해지지 않았어요. 설정 › 고급 › 연결에서 범위를 먼저 정해 주세요.",
      conflict: "설정을 저장한 적 있는 공간에는 템플릿을 넣을 수 없어요. 새 공간에 가져와 주세요.", handle_taken: "이미 다른 공간이 쓰는 별칭이에요.",
      revision_conflict: "그사이 다른 곳에서 바뀌었어요. 새로 불러왔어요.", not_found: "찾을 수 없는 공간이에요." };
    const errText = (e) => ERR[e?.message] || "처리하지 못했어요. 연결 상태를 확인하고 다시 시도해 주세요.";
    async function run(work) {
      if (S.busy) return;
      S.busy = true;
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
    const exportSpace = (template) => run(async () => {
      show("내보낼 파일을 만드는 중…");
      const job = await spaceJob("exports", await request(`${spacePath()}/export`, { method: "POST", body: JSON.stringify(template ? { template: true } : {}) }));
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
        const job = await spaceJob("imports", await request(`${path}/import`, { method: "POST", body: JSON.stringify({ bundle, import_as: "candidate" }) }));
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
    function moveGroup(kit) {
      const g = kit.group("기억 공간 옮기기", "이 공간의 기억을 파일로 내보내거나, 받은 파일을 가져와요. 가져온 기억은 내가 확인해야 저장돼요.");
      const row = el("div", "v4s-actions");
      row.append(button("기억 내보내기", "", null, () => exportSpace(false)));
      if (human()) row.append(button("기억 가져오기", "", null, importSpace)); // the import route takes people only; an agent token never sees it
      for (const b of row.children) b.disabled = S.busy;
      g.append(row);
      const t1 = button("템플릿으로 내보내기", "", null, () => exportSpace(true)), t2 = button("템플릿 가져오기", "", null, browseTemplate);
      t1.disabled = t2.disabled = S.busy;
      const tpl = [kit.line("템플릿으로 내보내기", "기억은 빼고 설정과 규칙·정리 틀만 담아요. 다른 공간을 같은 방식으로 시작할 때 써요.", t1)];
      if (human()) tpl.push(kit.line("템플릿 가져오기", "파일을 고르면 무엇이 들어 있는지 먼저 보여 줘요. 설정을 저장한 적 없는 공간에만 넣을 수 있어요.", t2));
      if (S.preview) tpl.push(previewCard(kit));
      g.append(kit.more("자세히 · 템플릿", ...tpl));
      if (S.preview) g.lastElementChild.open = true;
      if (S.line) g.append(el("p", `pg-hint${S.warn ? " warn" : ""}`, S.line));
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
      g.append(kit.more("자세히 · 프로젝트 ID 바꾸기", el("p", "pg-hint", "프로젝트 ID 자체는 서버를 멈춘 뒤 서버에서 memoryd --space-rename 으로만 바꿀 수 있어요. 보이는 이름과 별칭은 여기서 바로 바꿔요.")));
      return g;
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
    function listGroup(kit) {
      const g = kit.group("이 작업 공간의 공간", "기억이 있거나 설정을 저장한 공간이에요. 쓰는 공간은 설정 › 연결에서 바꿔요.");
      if (S.listErr) g.append(el("p", "pg-hint warn", S.listErr));
      if (!S.list) { g.append(el("p", "pg-hint", "불러오는 중…")); return g; }
      if (!S.list.length && !S.listErr) g.append(el("p", "pg-hint", "아직 공간이 없어요."));
      for (const s of S.list) {
        const here = s.project_id === state.scope?.project_id, name = s.config?.display_name || s.project_id;
        const t = el("div"), meta = el("small");
        meta.append(data(el("span", "mono", s.project_id)), el("span", "", ` · 기억 ${(s.counts?.memories || 0).toLocaleString()}개`), ...(s.config?.aliases?.length ? [data(el("span", "", ` · ${s.config.aliases.map((a) => `@${a}`).join(" ")}`))] : []));
        t.append(data(el("strong", here ? "sp-here" : "", name)), meta);
        const r = el("div", "setting"); r.append(t, here ? el("span", "sp-here", "지금 공간") : el("span"));
        g.append(r);
      }
      if (S.next) g.append(button("더 보기", "small", null, moreSpaces));
      return g;
    }

    function groups(kit) {
      if (S.key !== scopeKey()) { Object.assign(S, { view: null, list: null, preview: null, line: "", review: false, target: "" }); void load().catch(() => {}); }
      return [nameGroup(kit), moveGroup(kit), cloneGroup(kit), listGroup(kit)];
    }
    return { groups };
  }
  const api = Object.assign(create, lib);
  root.ArkismSpaces = api;
  if (typeof window === "undefined" && typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
