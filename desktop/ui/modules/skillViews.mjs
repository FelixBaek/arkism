import { collectionFooter } from "./collectionPaging.mjs";
// Owns skill lists and details. Dependencies are explicit live ports, never a global scope.
export function createSkillViews(deps) {


  // ---------------------------------------------------------------- 스킬
  const compatOf = (sk) => sk.skill_compatibility || {};

  // Only "unsupported" stops the router; no label means every agent gets the skill.
  const allowed = (sk, key) => compatOf(sk)[key] !== "unsupported";

  const blockedAny = (sk) => deps.shownAgents().some(([k]) => !allowed(sk, k));


  // 최근 60일 사용 (skill.usage traces); null while the server has no usage API.
  const useOf = (sk) => deps.state.data.skillUse?.get(sk.id) || null;

  const unused = (sk) => !!deps.state.data.skillUse && sk.status !== "deprecated" && !useOf(sk);

  const fromPlugin = (sk) => /\/plugins?\//.test(sk.source_path || "");

  const SKILL_SORTS = [["used", "많이 쓴 순"], ["recent", "최근 쓴 순"], ["name", "이름 순"], ["updated", "최근 바뀐 순"], ["quality", "품질 순"], ["security", "보안 검사 순"], ["agents", "에이전트 많은 순"]];

  const SORT_DIR0 = { name: 1 };
 // 나머지는 큰 값이 먼저
  const sortDirOf = (key) => SORT_DIR0[key] || -1;

  const skillDir = () => deps.state.skillDir || sortDirOf(deps.state.skillSort);

  function pickSkillSort(key) { // 같은 열을 다시 누르면 방향이 뒤집힌다
    deps.state.skillDir = key === deps.state.skillSort ? -skillDir() : 0;
    deps.state.skillSort = key;
    deps.store("arkism.skillSort", key); deps.store("arkism.skillDir", String(deps.state.skillDir));
    deps.render(false);
  }

  function sortSkills(list) {
    const time = (v) => new Date(v || 0).getTime();
    const val = {
      used: (sk) => useOf(sk)?.count || 0,
      recent: (sk) => time(useOf(sk)?.last_used_at),
      name: (sk) => sk.name,
      updated: (sk) => time(sk.updated_at),
      quality: (sk) => skillChecks(sk)?.quality.score ?? -1,
      security: (sk) => ({ ok: 2, warn: 1, bad: 0 }[skillChecks(sk)?.security.cls] ?? -1),
      agents: (sk) => deps.shownAgents().filter(([k]) => allowed(sk, k)).length,
    }[deps.state.skillSort];
    if (!val) return list;
    const cmp = (a, b) => { const x = val(a), y = val(b); return typeof x === "string" ? x.localeCompare(y) : x - y; };
    return list.slice().sort((a, b) => cmp(a, b) * skillDir() || a.name.localeCompare(b.name));
  }

  // 경로만 다른 같은 이름 스킬(도구별 사본·플러그인 캐시)은 한 줄로 묶고 눌러서 펼친다.
  const COPY_PATH = /\/(codex|copilot|build|\.claude-plugin|\.agents|plugins?|cache|marketplaces)\//;

  function groupSkills(list, serverOrder = false) { // 대표 사본을 먼저 고른 뒤, 대표 기준으로 정렬한다
    const groups = new Map();
    for (const sk of list) {
      if (!groups.has(sk.name)) groups.set(sk.name, []);
      groups.get(sk.name).push(sk);
    }
    const byRep = new Map();
    for (const g of groups.values()) {
      const rep = serverOrder ? g[0] : g.find((sk) => !COPY_PATH.test(sk.source_path || "")) || g[0];
      byRep.set(rep, [rep, ...sortSkills(g.filter((sk) => sk !== rep))]);
    }
    return (serverOrder ? [...byRep.keys()] : sortSkills([...byRep.keys()])).map((rep) => byRep.get(rep));
  }

  // 원본 저장소에 새 버전이 있는 스킬 (서버가 6시간마다 git fetch로 확인한 목록, 폴더 이름과 저장소 경로가 모두 맞아야 한다)
  const skillFolder = (sk) => (sk.source_path || "").split("/").slice(-2, -1)[0] || sk.name;
 // 표시 이름이 아니라 SKILL.md가 든 폴더 이름
  const updatesOf = (sk) => (deps.state.data.tools?.skill_updates || []).filter((u) => u.name === skillFolder(sk) && (sk.source_path || "").includes(`${(u.repo || "").replace(/^~/, "")}/`));

  function stage(skillId, key, want) {
    const sk = deps.catalogQuery.record(`skill:${skillId}`) || deps.state.data.skills.find((x) => x.id === skillId);
    const k = `${skillId}|${key}`;
    if (deps.state.pending.has(k) || (want === "allow") === allowed(sk, key)) deps.state.pending.delete(k);
    else deps.state.pending.set(k, want);
    deps.render(false);
    if (deps.state.selected === `skill:${skillId}`) deps.renderDetail();
  }

  function toggleCell(sk, key) {
    stage(sk.id, key, allowed(sk, key) ? "block" : "allow");
  }

  // 품질·보안 come from Arkism's built-in check (always) plus, where installed, NVIDIA SkillEvaluator
  // (quality-check, 0–100 and A–F) and SkillSpector (--no-llm: SAFE / CAUTION / DO_NOT_INSTALL),
  // which arkism-skill-checks.timer runs on the host's
  // skill folders and arkism-agents.timer reports with the agent tools. A skill no scan covers yet
  // falls back to what Arkism records for the active version: how complete the analysis is, and
  // what the import inspection found.
  const QUALITY_PARTS = [["problem", "풀려는 문제"], ["use_when", "이럴 때 써요"], ["avoid_when", "이럴 땐 쓰지 마세요"], ["output_format", "결과 형식"], ["success_criteria", "성공 기준"], ["verification", "확인 방법"]];

  const filled = (v) => (Array.isArray(v) ? v.length > 0 : !!(v && String(v).trim()));

  const VERDICTS = { SAFE: 0, CAUTION: 1, DO_NOT_INSTALL: 2 };

  const SECURITY = [{ label: "통과", cls: "ok" }, { label: "주의", cls: "warn" }, { label: "위험", cls: "bad" }];

  const gradeCls = (score) => (score >= 75 ? "ok" : score >= 40 ? "warn" : "bad");

  // Every agent folder holding a skill of this name; the scan with the worst verdict speaks for the skill.
  function hostChecks(sk) {
    const all = (deps.state.data.tools?.skill_checks || []).filter((c) => c.name === sk.name);
    return all.sort((a, b) => (VERDICTS[b.verdict] ?? -1) - (VERDICTS[a.verdict] ?? -1) || (a.quality_score ?? 101) - (b.quality_score ?? 101));
  }

  function derivedChecks(v) {
    const a = v?.analysis;
    if (!a) return { quality: { score: null, grade: "—", cls: "off", missing: [] }, security: { label: "확인 못 함", cls: "off", reasons: ["분석 기록이 없어요"] } };
    const missing = QUALITY_PARTS.filter(([k]) => !filled(a[k])).map(([, l]) => l);
    const score = Math.round(((QUALITY_PARTS.length - missing.length) / QUALITY_PARTS.length) * 100);
    const grade = score >= 90 ? "A" : score >= 75 ? "B" : score >= 60 ? "C" : score >= 40 ? "D" : "F";
    const reasons = [];
    let level = 0;
    if (v.inspection?.redacted) { reasons.push("본문에서 비밀값을 찾아 가렸어요"); level = 2; }
    if (v.inspection?.personal_data) { reasons.push("개인정보가 들어 있어요"); level = Math.max(level, 1); }
    if (a.irreversible_actions?.length) { reasons.push(`되돌릴 수 없는 동작: ${a.irreversible_actions.join(", ")}`); level = Math.max(level, 1); }
    if (a.external_transfer) { reasons.push("밖으로 자료를 보내요"); level = Math.max(level, 1); }
    if (a.secret_names?.length) { reasons.push(`비밀값이 필요해요: ${a.secret_names.join(", ")}`); level = Math.max(level, 1); }
    return { quality: { score, grade, cls: gradeCls(score), missing }, security: { ...SECURITY[level], reasons } };
  }

  function skillChecks(sk) { // null while nothing is known yet
    const v = deps.state.versions.get(sk.version_id);
    const host = hostChecks(sk);
    if (v === undefined && !host.length) return null;
    const derived = derivedChecks(v);
    const out = { ...derived, validated: !!v?.validation_id, source: "app", scans: host };
    const q = host.find((c) => typeof c.quality_score === "number");
    if (q) out.quality = { score: Math.round(q.quality_score), grade: q.quality_grade || "—", cls: gradeCls(q.quality_score), missing: derived.quality.missing };
    const s = host.find((c) => c.verdict in VERDICTS);
    if (s) {
      const reasons = s.top_findings?.length ? [...s.top_findings] : [];
      if (s.findings > reasons.length) reasons.push(`발견 ${s.findings}건`);
      out.security = { ...SECURITY[VERDICTS[s.verdict]], reasons, risk: s.risk_score };
    }
    if (q || s) out.source = "host";
    return out;
  }

  // v4 · 스킬 (Figma 198:802): 확인할 것 카드 → 검색·상태·정렬 → 분류 → 목록, 오른쪽은 고른 스킬.
  // state.skillPage opens a page inside the same view: 개선하기 (4 steps), GitHub에서 설치.
  const SKILL_STATES = [["all", "전체"], ["missing", "빠진 곳 있음"], ["unused", "안 씀"], ["caution", "보안 주의"], ["update", "업데이트 있음"], ["plugin", "플러그인 스킬"], ["trash", "휴지통"]];

  const IMPROVE_STEPS = [["개선안", "무엇을 고칠까요?", "이 스킬의 교훈과 평가 과제를 미리 채웠어요. 확인하고 만들기를 누르세요."],
    ["비교 평가", "기존 버전과 비교해요", "같은 평가 과제로 두 버전을 돌려 점수·비용·시간을 비교해요."],
    ["검토·승인", "무엇이 바뀌었나요", "후보 버전의 원문과 평가를 보고 승인하거나 제외해요."],
    ["시험 적용", "실제 작업에 시험 적용해요", "정한 프로그램에서 일정 기간 새 버전을 써 보고 결과를 모아요."]];

  let skillPageCache = null;
 // { key, main, side }: a page with forms survives the 10-second refresh
  const skillPageKey = () => JSON.stringify(deps.state.skillPage);

  function openSkillPage(page) {
    deps.state.skillPage = page;
    if (page.id) deps.state.selected = `skill:${page.id}`; else deps.state.selected = null;
    skillPageCache = null;
    deps.$("#table").scrollTop = 0;
    deps.render();
  }

  // After a keyboard jump into a skill page, focus its heading so the reader stays in the new screen.
  function focusPage() {
    const h = deps.$("#table h2") || deps.$("#table button");
    if (!h) return;
    if (h.tagName === "H2") h.tabIndex = -1;
    h.focus();
  }

  const skBadge = (text, cls = "") => deps.el("span", `v4s-badge ${cls}`.trim(), text);

  function skillBadge(sk) {
    const c = skillChecks(sk);
    if (["warn", "bad"].includes(c?.security.cls)) return skBadge(`보안 ${c.security.label}`, "amber");
    if (updatesOf(sk).length) return skBadge("업데이트 있음");
    return null;
  }

  function agentDots(sk) {
    const box = deps.el("span", "v4s-agents");
    for (const [k, n, logo] of deps.shownAgents()) if (allowed(sk, k)) box.append(deps.agentLogo(logo, n));
    return box;
  }

  function panelSearch(placeholder) {
    const box = deps.el("label", "v4s-search");
    const input = deps.el("input"); input.type = "search"; input.placeholder = placeholder; input.value = deps.$("#search").value; input.setAttribute("aria-label", placeholder);
    input.addEventListener("input", () => { deps.$("#search").value = input.value; deps.state.query = input.value.trim().toLowerCase(); deps.state.skillLimit = 0; deps.render(false); });
    box.append(deps.icon("magnifying-glass"), input);
    return box;
  }

  // 분류 chip (전체 · a category) with its count; 도구 › 플러그인·MCP use the same one.
  function filterChip(label, count, active, onPick) {
    const c = deps.el("button", `chip${active ? " active" : ""}`); c.type = "button";
    c.append(label);
    if (count !== "") c.append(deps.el("output", "", String(count)));
    c.addEventListener("click", onPick);
    return c;
  }

  // 필터 ⌄ (Figma 말풍선): grouped pill chips in a bubble under the button. A pick re-renders the list and the bubble
  // stays open for the same key; an outside click or Esc closes it. groups: [{ title, set, zero, chips: [[label, count, active, onPick]] }].
  // zero keeps 0-count chips (에이전트: one chip per agent, so 못 쓰는 스킬 stays reachable).
  let filterOpen = "";
  function closeFilter() {
    filterOpen = "";
    document.querySelectorAll(".v4s-filter").forEach((w) => { w.querySelector(".v4s-pop")?.remove(); w.querySelector(".v4s-filter-btn").setAttribute("aria-expanded", "false"); });
  }
  let filterWired = false;
  function filterMenu(key, groups) {
    if (!filterWired) {
      filterWired = true;
      document.addEventListener("pointerdown", (e) => { if (filterOpen && !e.target.closest?.(".v4s-filter")) closeFilter(); }, true);
      document.addEventListener("keydown", (e) => { if (filterOpen && e.key === "Escape") { closeFilter(); document.querySelector(".v4s-filter > button")?.focus(); } });
    }
    const wrap = deps.el("div", "v4s-filter");
    const n = groups.filter((g) => g.set).length;
    const btn = deps.el("button", `v4s-filter-btn${n ? " on" : ""}`, "필터"); btn.type = "button";
    btn.setAttribute("aria-haspopup", "dialog");
    if (n) btn.append(deps.el("output", "", String(n)));
    btn.append(deps.icon("caret-down"));
    const focused = [...document.querySelectorAll(".v4s-pop .chip")].indexOf(document.activeElement);
    const pop = () => {
      const box = deps.el("div", "v4s-pop"); box.setAttribute("role", "dialog"); box.setAttribute("aria-label", "필터");
      for (const g of groups) {
        const chips = g.chips.filter(([, count, active]) => g.zero || active || count !== 0).map(([label, count, active, onPick]) => filterChip(label, count, active, onPick));
        if (!chips.length) continue;
        const sec = deps.el("section", "v4s-pop-group");
        const row = deps.el("div", "v4s-pop-chips"); row.append(...chips);
        sec.append(deps.el("h4", "", g.title), row);
        if (g.extra) sec.append(g.extra);
        box.append(sec);
      }
      return box;
    };
    const open = filterOpen === key;
    btn.setAttribute("aria-expanded", String(open));
    wrap.append(btn);
    if (open) {
      const box = pop();
      wrap.append(box);
      if (focused >= 0) queueMicrotask(() => box.querySelectorAll(".chip")[focused]?.focus({ preventScroll: true }));
    }
    btn.addEventListener("click", () => {
      if (filterOpen === key) { closeFilter(); return; }
      closeFilter();
      filterOpen = key;
      btn.setAttribute("aria-expanded", "true");
      wrap.append(pop());
    });
    return wrap;
  }

  // The list's search box sits in #controls, which the list redraw rebuilds: take the snapshot before clearing it.
  const keepFocus = () => globalThis.ArkismKeep.view(deps.$("#controls"));

  function skillRow({ sk, copies, open, copyIndex: i }) {
    const cell = deps.el("div", "v4s-text");
    // 이름이 제목, 설명은 그 아래 작은 줄 (분류는 앞에 그대로).
    cell.append(deps.raw(deps.el("div", "v4s-title v4s-name", sk.name)));
    const meta = deps.el("div", "v4s-meta");
    const cat = deps.pages.categoryOf(sk)?.name;
    if (cat) meta.append(deps.el("span", "v4s-cat", cat));
    if (sk.description) meta.append(deps.raw(deps.el("span", "v4s-desc", firstSentence(sk.description))));
    if (!i && copies.length > 1) {
      const c = deps.el("button", `copy-chip${open ? " open" : ""}`, `같은 이름 ${copies.length}개`); c.type = "button";
      c.title = open ? "접기" : "경로가 다른 같은 이름 스킬을 펼쳐요";
      c.addEventListener("click", (e) => { e.stopPropagation(); deps.state.openCopies[open ? "delete" : "add"](sk.name); deps.render(false); });
      meta.append(c);
    }
    if (i) meta.append(deps.el("span", "mono copy-path", sk.source_path || ""));
    cell.append(meta);
    const u = useOf(sk);
    const uses = deps.el("span", "v4s-uses", !deps.state.data.skillUse ? "…" : u ? `${u.count}번` : "안 씀");
    const side = [skillBadge(sk), agentDots(sk), uses].filter(Boolean);
    const r = deps.row(`skill:${sk.id}`, "", [cell, ...side], deps.renderDetail);
    r.classList.add("v4s-row");
    if (i) r.classList.add("copy");
    return r;
  }

  function renderSkills() {
    if (deps.state.skillPage) return renderSkillPage();
    const collection = deps.state.skillTab === "trash" ? "skillTrash" : "skills";
    deps.catalogQuery.ensure(collection);
    const catalogState = deps.catalogQuery.snapshot(collection), skills = catalogState.items;
    const live = skills.filter((sk) => sk.status !== "deprecated");
    const versionsKnown = deps.state.versionsDone;
    const n = {
      missing: skills.filter(blockedAny).length, unused: deps.state.data.skillUse ? skills.filter(unused).length : null,
      caution: versionsKnown ? live.filter((sk) => ["warn", "bad"].includes(skillChecks(sk)?.security.cls)).length : null,
      update: live.filter((sk) => updatesOf(sk).length).length, plugin: skills.filter(fromPlugin).length, trash: skills.length - live.length
    };
    // The first page failed: one message in the list, no 0개 and no empty-filter text beside it.
    const failed = !!catalogState.error && !skills.length;
    deps.$("#title-count").textContent = deps.$("#title")?.classList.contains("sr-only") || failed || catalogState.total === null && !catalogState.ready ? "" : `${catalogState.total ?? live.length}개`; // no 0개 before the first page
    const restore = keepFocus();
    // 확인해 주세요: what needs a look, the first kind with any opens with 하나씩 확인하기.
    const parts = [];
    const todo = [["caution", "보안 주의"], ["update", "업데이트"], ["adapter", "어댑터 필요"]].filter(([k]) => n[k]);
    if (deps.state.skillTab === "all" && todo.length) {
      const card = deps.el("div", "v4s-attn");
      const text = deps.el("div", "v4s-attn-text");
      const line = deps.el("div", "v4s-attn-line"); line.append(deps.el("strong", "", "확인해 주세요"), deps.el("output", "", String(todo.reduce((a, [k]) => a + n[k], 0))));
      text.append(line, deps.el("p", "", todo.map(([k, l]) => `${l} ${n[k]}`).join(" · ")));
      card.append(text, deps.button("하나씩 확인하기", "primary", null, () => deps.go("skills", todo[0][0])));
      parts.push(card);
    }
    const modelKeys = deps.shownAgents().map(([k]) => k);
    if (deps.state.skillModel && !modelKeys.includes(deps.state.skillModel.replace("!", ""))) deps.state.skillModel = "";
    // 에이전트: one chip per agent (logo + name, count = skills it can use); a picked agent gets a 쓰는/못 쓰는 toggle.
    const pickModel = (k) => () => { deps.state.skillModel = k; deps.render(false); };
    const agentOf = deps.state.skillModel.replace("!", "");
    const agentLabel = (k) => { const [, name, logo] = deps.shownAgents().find(([a]) => a === k); const l = deps.el("span", "v4s-pop-agent"); l.append(deps.agentLogo(logo, name), name); return l; };
    const models = [["모든 에이전트", "", !agentOf, pickModel("")], ...modelKeys.map((k) => [agentLabel(k), live.filter((sk) => allowed(sk, k)).length, agentOf === k, pickModel(k)])];
    let modelToggle = null;
    const cannot = agentOf ? live.filter((sk) => !allowed(sk, agentOf)).length : 0;
    if (agentOf && (cannot || deps.state.skillModel.startsWith("!"))) {
      modelToggle = deps.segment([["use", "쓰는 스킬"], ["not", "못 쓰는 스킬", cannot]], deps.state.skillModel.startsWith("!") ? "not" : "use", (v) => pickModel(v === "not" ? `!${agentOf}` : agentOf)());
      modelToggle.classList.add("v4u-seg", "v4s-pop-toggle");
    }
    // 분류: 전체 + the owner's categories (설정 › 카테고리). An older server sends no category facets: no number rather than 0.
    const cats = deps.pages.prefs.categories;
    const pickCat = (id) => () => { deps.state.skillCat = id; deps.state.skillLimit = 0; deps.render(false); };
    const counts = catalogState.facets.categories, total = counts ? Object.values(counts).reduce((a,b) => a+b,0) : catalogState.total ?? "";
    const none = counts?.[""] || 0;
    const catChips = cats.length ? [["전체", total, deps.state.skillCat === "all", pickCat("all")], ...cats.map((c) => [c.name, counts ? counts[c.id] || 0 : "", deps.state.skillCat === c.id, pickCat(c.id)]), ...(none ? [["분류 안 됨", none, deps.state.skillCat === "none", pickCat("none")]] : [])] : [];
    // 정렬: the picked one shows its direction; picking it again flips it.
    const sorts = SKILL_SORTS.map(([k, l]) => [k === deps.state.skillSort ? `${l} ${skillDir() > 0 ? "↑" : "↓"}` : l, "", k === deps.state.skillSort, () => pickSkillSort(k)]);
    const bar = deps.el("div", "v4s-bar");
    // 휴지통 is its own list (skillTrash), so 0 here only means none loaded yet: keep the chip, without a number.
    bar.append(panelSearch("스킬 검색"), filterMenu("skills", [
      { title: "카테고리", set: deps.state.skillCat !== "all", chips: catChips },
      { title: "상태", set: deps.state.skillTab !== "all", chips: SKILL_STATES.map(([k, l]) => [l, k === "all" || k === "trash" && !n.trash ? "" : n[k] ?? "…", deps.state.skillTab === k, () => deps.go("skills", k)]) },
      { title: "에이전트", set: !!deps.state.skillModel, zero: true, chips: models, extra: modelToggle },
      { title: "정렬", chips: sorts }]), ...deps.barActions(deps.loadAll));
    deps.$("#controls").replaceChildren(...parts, bar);
    deps.$("#chips").replaceChildren();
    const statNote = failed ? "" : catalogState.error ? "전체 목록을 불러오지 못했어요. 다시 불러오기를 눌러 주세요." : catalogState.loading || catalogState.stale ? "전체 목록에서 찾는 중…" : "";
    deps.$("#stats").replaceChildren(...(statNote ? [deps.el("p", "meta memory-page-note", statNote)] : [])); // empty: no gap under the toolbar
    const items = skills;
    const groups = groupSkills(items, true);
    const limit = deps.state.skillLimit || 50;
    const list = deps.el("div", "v4s-list");
    const rows = groups.slice(0, limit).flatMap(copies => {
      const open = deps.state.openCopies.has(copies[0].name);
      return (open ? copies : copies.slice(0, 1)).map((sk, copyIndex) => ({ sk, copies, open, copyIndex }));
    });
    const table = deps.$("#table");
    table.className = "v4s-wrap";
    table.replaceChildren(list);
    deps.virtualList.mount({ container: list, scrollport: table, items: rows, row: skillRow, id: item => item.sk.id, key: JSON.stringify([deps.generation, deps.state.skillTab, deps.state.skillCat, deps.state.skillModel, deps.state.skillSort, deps.state.skillDir, deps.state.query]) });
    const generation = deps.generation, tab = deps.state.skillTab;
    if (failed) {
      const box = deps.el("div", "empty", "스킬 목록을 불러오지 못했어요. ");
      box.append(deps.button("다시 불러오기", "small", null, () => { void deps.catalogQuery.more(collection).catch(() => {}).finally(() => { if (generation === deps.generation && deps.state.view === "skills") deps.render(false); }); deps.render(false); }));
      list.append(box);
      restore();
      return;
    }
    if (!items.length && catalogState.ready) list.append(deps.el("div", "empty", deps.state.query ? "검색과 맞는 스킬이 없어요." : "조건에 맞는 스킬이 없어요."));
    const more = collectionFooter({ document, name: collection, total: groups.length, shown: Math.min(limit, groups.length),
      snapshot: () => deps.catalogQuery.snapshot(collection),
      reveal: () => { deps.state.skillLimit = limit + 50; },
      fetch: () => { deps.state.skillLimit = limit + 50; return deps.catalogQuery.more(collection); },
      render: () => deps.render(false),
      isCurrent: () => generation === deps.generation && deps.state.view === "skills" && tab === deps.state.skillTab
    });
    if (more) { more.classList.add("v4s-foot"); table.append(more); }
    restore();
  }


  // ---------------------------------------------------------------- 스킬 상세 (v4)
  function skFacts(rows, wide) {
    const dl = deps.el("dl", `v4s-facts${wide ? " wide" : ""}`);
    for (const [k, v, cls] of rows) { if (!v) continue; const dd = deps.el("dd", cls || ""); dd.append(v); dl.append(deps.el("dt", "", k), dd); }
    return dl;
  }

  function skSection(title, aside) {
    const s = deps.el("section", "v4s-sec");
    const h = deps.el("div", "v4s-sec-head"); h.append(deps.el("h3", "", title));
    if (aside) h.append(deps.el("span", "mono", aside));
    s.append(h);
    return s;
  }

  // 쓰는 에이전트: a chip per agent; clicking stages add/remove, the card under it applies or cancels.
  function skillAgentsSection(sk) {
    const shown = deps.shownAgents();
    const sec = skSection("쓰는 에이전트", `${shown.filter(([k]) => allowed(sk, k)).length} / ${shown.length}`);
    const grid = deps.el("div", "v4s-agent-chips");
    for (const [k, n, logo] of shown) {
      const staged = deps.state.pending.get(`${sk.id}|${k}`);
      const on = allowed(sk, k);
      const a = deps.el("button", `v4s-agent${on ? " on" : " off"}${staged ? " staged" : ""}`); a.type = "button";
      a.append(deps.agentLogo(logo, n), deps.el("span", "", n));
      a.title = `${staged ? "누르면 예정을 취소해요" : on ? `${n}에서 써요 · 누르면 뺄 예정` : `${n}에서 못 써요 · 누르면 추가 예정`} · ${deps.pcLink(k).title}`;
      a.addEventListener("click", () => toggleCell(sk, k));
      grid.append(a);
    }
    sec.append(grid);
    const mine = [...deps.state.pending.entries()].filter(([key]) => key.startsWith(`${sk.id}|`));
    if (mine.length) {
      const names = (want) => mine.filter(([, w]) => w === want).map(([key]) => deps.agentName(key.split("|")[1])).join("·");
      const ask = [names("allow") && `${names("allow")}에 추가`, names("block") && `${names("block")}에서 빼기`].filter(Boolean).join(", ");
      const card = deps.el("div", "v4s-confirm");
      card.append(deps.el("p", "", `${ask}할까요?`),
        deps.button("취소", "ghost", null, () => { mine.forEach(([key]) => deps.state.pending.delete(key)); deps.render(false); deps.renderDetail(); }),
        deps.button("적용", "primary", null, applyChanges));
      sec.append(card);
    }
    return sec;
  }

  async function restoreSkill(sk) {
    try {
      await deps.setSkillStatus(sk, "active");
      deps.writeKey(deps.TRASH_KEY, deps.readKey(deps.TRASH_KEY, []).filter((x) => x.id !== sk.id));
      deps.state.data.skills = await deps.collectionPaging.read("skills", true);
      deps.state.data.skillTrash = await deps.loadTrash();
      await deps.catalogQuery.refreshVisible();
      deps.state.selected = null;
      deps.notice(`${sk.name}를 되돌렸어요.`);
      deps.render(false);
      deps.renderDetail();
    } catch (e) { deps.notice(`되돌리지 못했어요 (${e.message}).`); }
  }

  const FINDING_KO = { secret: "키나 토큰 같은 비밀값이 그대로 적혀 있어요", remote_exec: "인터넷에서 받은 코드를 바로 실행해요",
    destructive: "폴더나 디스크를 통째로 지울 수 있는 명령이 있어요", credential_access: "SSH 키·AWS·브라우저 쿠키 같은 로그인 정보 파일을 읽어요",
    exfiltration: "비밀값을 모르는 주소로 보낼 수 있는 명령이 있어요", prompt_injection: "에이전트에게 원래 지시를 무시하거나 사용자에게 숨기라는 문장이 있어요" };
  const SEVERITY_KO = { CRITICAL: "심각도 아주 높음", HIGH: "심각도 높음", MEDIUM: "심각도 보통", LOW: "심각도 낮음", INFO: "참고" };
  function findingText(label) {
    const parts = String(label).split(" · ");
    if (parts.length < 2) return label;
    const at = /^(.+):(\d+)$/.exec(parts[parts.length - 1]);
    if (at) parts.pop();
    const sev = parts.find((x) => SEVERITY_KO[x.toUpperCase()]);
    const what = FINDING_KO[parts[0]] || parts.filter((x) => x !== sev).join(" · ");
    return [what, at ? `${at[1]} ${at[2]}번째 줄` : "", sev ? SEVERITY_KO[sev.toUpperCase()] : ""].filter(Boolean).join(" · ");
  }
  // 보안 주의·위험 says why right under the name; the verdict is advice, nothing blocks the skill.
  function securityCard(c) {
    const bad = c.security.cls === "bad";
    const card = deps.el("div", "v4s-attn small v4s-why");
    const t = deps.el("div", "v4s-attn-text");
    const list = deps.el("ul", "");
    for (const r of c.security.reasons.length ? c.security.reasons : ["검사 도구가 구체적인 이유를 보내지 않았어요"]) list.append(deps.el("li", "", findingText(r)));
    t.append(deps.el("strong", "", bad ? "보안 위험 · 쓰지 않는 걸 권해요" : "보안 주의 · 아래 내용을 확인해 주세요"),
      deps.el("p", "", bad ? "검사에서 위험한 내용을 찾았어요. Arkism이 막지는 않아서 지금도 에이전트가 쓸 수 있어요. 믿을 수 없으면 휴지통으로 옮기세요." : "검사에서 조심할 내용을 찾았어요. 막지는 않아서 그대로 쓸 수 있어요."),
      list);
    card.append(t);
    return card;
  }
  const skillTalk = new Map(); // AI talks per skill, kept while the app is open
  async function skillDetailV4(sk) {
    const cat = deps.pages.categoryOf(sk)?.name || "분류 안 됨";
    const body = deps.detailShell(cat);
    const top = deps.el("div", "v4s-head");
    const titleRow = deps.el("div", "v4s-namebar");
    titleRow.append(deps.raw(deps.el("h2", "v4s-name", sk.name)));
    top.append(titleRow);
    if (sk.description) top.append(deps.raw(deps.el("p", "v4s-lead", sk.description)));
    // One AI button (ai-chat.js, Figma 1385:5245): it explains in the bubble, and its footer opens 개선하기 for a live skill.
    const aiBtn = window.ArkismAIChat.button({ state: skillTalk.get(sk.id) || skillTalk.set(sk.id, {}).get(sk.id), label: "AI", title: "AI", sub: sk.name,
      greeting: "무엇이 궁금하세요?\n쉽게 설명하거나, 바로 고쳐 볼 수 있어요.",
      placeholder: "궁금한 것을 물어보세요", quick: ["쉽게 설명해 줘", "언제 쓰면 좋아?", "예시를 들어 줘"],
      footer: sk.status === "deprecated" ? null : { label: "이 스킬 개선하기", hint: "교훈을 모아 새 버전을 만들고, 기존 버전과 비교해요", onClick: () => { openSkillPage({ kind: "improve", id: sk.id, step: 0 }); focusPage(); } },
      ask: async (q, history) => ({ say: await deps.crew.explain(`Skill name: ${sk.name}\nDescription: ${sk.description || ""}\n${history.length ? `Conversation so far:\n${history.map((m) => `${m.who === "me" ? "User" : "You"}: ${m.text}`).join("\n")}\n` : ""}\nQuestion: ${q}\n\nAnswer in two or three plain Korean sentences about what this skill does and when to use it.`) }) });
    titleRow.append(aiBtn);
    const c = skillChecks(sk), u = useOf(sk);
    const chips = deps.el("div", "v4s-chips");
    if (c) chips.append(deps.el("span", `v4s-pill${c.quality.cls === "ok" ? "" : " amber"}`, `품질 ${c.quality.grade}${c.quality.score === null ? "" : ` · ${c.quality.score}`}`), deps.el("span", `v4s-pill${c.security.cls === "ok" ? "" : " amber"}`, `보안 ${c.security.label}`));
    if (deps.state.data.skillUse) chips.append(deps.el("span", "v4s-pill muted", u ? `${u.count}번 사용 · ${deps.ago(u.last_used_at)}` : "최근 60일 안 씀"));
    top.append(chips);
    const parts = [top];
    if (c && ["warn", "bad"].includes(c.security.cls)) parts.push(securityCard(c));
    if (sk.status === "deprecated") {
      const card = deps.el("div", "v4s-attn small");
      const t = deps.el("div", "v4s-attn-text"); t.append(deps.el("strong", "", "휴지통에 있어요"), deps.el("p", "", "에이전트가 읽지 않아요. 되돌리면 바로 다시 받아요."));
      card.append(t, deps.button("되돌리기", "primary", null, () => restoreSkill(sk)));
      parts.push(card);
    }
    // Section order follows the user's layout (Figma 1281:76816): 언제 쓰나요, then 쓰는 에이전트.
    const when = skSection("언제 쓰나요");
    const whenRows = deps.el("div", ""); when.append(whenRows, deps.el("p", "meta", "불러오는 중…"));
    parts.push(when);
    if (sk.status !== "deprecated") parts.push(skillAgentsSection(sk));
    const crewSec = deps.crew.skillExperts(sk);
    if (crewSec) parts.push(crewSec);
    // 버전과 기록: source, version (+ its update action), place, history, and the folded checks/usage.
    const r = deps.repoOf(sk), ups = updatesOf(sk);
    const ver = (v) => (/^\d/.test(v || "") ? `v${v}` : v || "");
    const rec = skSection("버전과 기록");
    rec.classList.add("v4s-rec");
    const version = sk.version ? `${ver(sk.version)}${ups.length ? ` → ${ver(ups[0].to)} 있음` : r ? (r.behind ? ` · 새 커밋 ${r.behind}개` : " · 최신") : ""}` : "";
    const verCell = deps.el("span", "v4s-ver");
    if (version) verCell.append(deps.el("span", "mono", version));
    const link = (label, onClick) => { const b = deps.el("button", "v4s-link", label); b.type = "button"; b.addEventListener("click", onClick); return b; };
    let history = null;
    if (sk.status !== "deprecated") {
      if (ups.length) verCell.append(deps.button("업데이트 받기", "small", null, () => deps.updateSkills(ups)));
      else if (r) {
        const checking = (deps.state.data.tools?.requests || []).some((q) => q.kind === "skill_check" && q.name === r.repo && q.status === "pending");
        const b = deps.button(checking ? "확인 중…" : "업데이트 확인", "small", null, () => deps.checkSkillRepo(r)); b.disabled = checking; verCell.append(b);
      }
      const n = (sk.version_history || []).filter((id) => id !== sk.active_version).length; // the version in use is not a past one
      history = link(n ? `지난 버전 ${n}개 ›` : "지난 버전 보기 ›", () => openSkillPage({ kind: "improve", id: sk.id, step: 2 }));
    }
    if (r) verCell.append(deps.el("small", "meta", `마지막 확인 ${deps.ago(r.checked_at)}`));
    // 품질·보안 검사와 에이전트별 사용 기록은 접어 두고, 검사·사용 줄을 누르면 연다.
    const more = deps.el("div", "v4s-more");
    more.hidden = true;
    more.append(deps.checksSection(sk));
    if (u) { const s = deps.section("최근 60일 사용", `마지막 ${deps.ago(u.last_used_at)}`); s.append(deps.facts(Object.entries(u.by_runtime || {}).sort((a, b) => b[1] - a[1]).map(([rt, n]) => [deps.agentName(rt) || rt, `${n}번`]))); more.append(s); }
    const pcs = skSection("에이전트 PC 연결");
    const pcRow = deps.el("div", "v4s-chips");
    for (const [k, n] of deps.shownAgents()) { const t = deps.el("span", "v4s-pc"); t.append(deps.pcLink(k), n); pcRow.append(t); }
    pcs.append(pcRow, deps.pcLegend());
    more.append(pcs);
    const checks = link("품질·보안 검사, 에이전트별 사용 기록 ›", () => { more.hidden = !more.hidden; checks.setAttribute("aria-expanded", String(!more.hidden)); });
    checks.setAttribute("aria-expanded", "false");
    rec.append(skFacts([["출처", r ? (r.remote || r.repo) : "", "mono"], ["버전", verCell.childNodes.length ? verCell : ""], ["위치", (sk.source_path || "").replace(/\/SKILL\.md$/, ""), "mono"], ["이력", history], ["검사·사용", checks]]), more);
    parts.push(rec);
    const foot = deps.el("div", "v4s-trash");
    if (sk.version_id) foot.append(deps.button("zip으로 내보내기", "ghost", null, () => exportSkillZip(sk)));
    if (sk.status !== "deprecated") foot.append(deps.button("휴지통으로", "ghost danger", null, () => deps.confirmBatch([sk], () => deps.trashSkills([sk]))));
    if (foot.childNodes.length) parts.push(foot);
    body.append(...parts);
    const g = deps.generation;
    const v = await deps.request(`/v1/skill_versions/${encodeURIComponent(sk.version_id)}?${deps.query()}`).catch(() => null);
    if (g !== deps.generation || deps.state.selected !== `skill:${sk.id}` || !when.isConnected) return;
    const a = v?.analysis || {};
    const join = (x) => (Array.isArray(x) ? x.join(" · ") : x || "");
    const rows = [["이럴 때", join(a.use_when)], ["쓰지 마세요", join(a.avoid_when)], ["쓰는 도구", join(a.tools)]];
    when.replaceChildren(when.firstChild, ...(rows.some(([, x]) => x) ? [skFacts(rows, true)] : [deps.el("p", "meta", "스킬 설명에 쓰는 때가 적혀 있지 않아요.")]));
  }


  // SKILL.md와 번들 파일을 zip으로 받는다. 앱의 API 다리는 JSON 글만 읽어서 base64로 받는다.
  async function exportSkillZip(sk) {
    let url;
    try {
      const z = await deps.request(`/v1/skill_versions/${encodeURIComponent(sk.version_id)}/zip?${deps.query({ format: "json" })}`);
      const bytes = Uint8Array.from(atob(z.content_base64), (c) => c.charCodeAt(0));
      url = URL.createObjectURL(new Blob([bytes], { type: "application/zip" }));
      const link = deps.el("a"); link.href = url; link.download = z.filename; link.click();
      deps.notice(`${z.filename} 다운로드를 시작했어요.`); // a link click cannot tell whether the save went through
    } catch (_) { deps.notice("zip을 만들지 못했어요. 잠시 뒤 다시 시도해 주세요."); }
    finally { if (url) setTimeout(() => URL.revokeObjectURL(url), 1000); }
  }


  // ---------------------------------------------------------------- 스킬 안의 페이지: 개선하기 · GitHub에서 설치
  function renderSkillPage() {
    const p = deps.state.skillPage;
    ["#controls", "#chips", "#stats"].forEach((q) => deps.$(q).replaceChildren());
    deps.$("#chips").classList.remove("v4s-cats");
    const table = deps.$("#table");
    table.className = "v4s-wrap v4s-page";
    deps.pageBack("스킬", () => deps.go("skills"));
    const key = skillPageKey();
    const fresh = !skillPageCache || skillPageCache.key !== key;
    if (p.kind === "improve") {
      const sk = deps.catalogQuery.record(`skill:${p.id}`) || deps.state.data.skills.find((x) => x.id === p.id);
      if (!sk) { deps.state.skillPage = null; return renderSkills(); }
      if (fresh) skillPageCache = { key, main: improveMain(sk, p.step) };
    } else if (p.kind === "install") {
      if (fresh) { const side = deps.el("div", "v4s-side"); skillPageCache = { key, side, main: deps.catalog.installPage(side, () => skillPageCache?.key === key && deps.state.view === "skills") }; }
    }
    table.replaceChildren(skillPageCache.main);
  }

  function improveMain(sk, step) {
    const wrap = deps.el("div", "v4s-improve");
    const steps = deps.el("ol", "v4s-steps");
    IMPROVE_STEPS.forEach(([label], i) => {
      const li = deps.el("li", i < step ? "done" : i === step ? "current" : "");
      const b = deps.el("button", "", ""); b.type = "button";
      b.append(deps.el("i", "", i < step ? "✓" : String(i + 1)), deps.el("span", "", label));
      if (i === step) b.setAttribute("aria-current", "step");
      b.addEventListener("click", () => { if (i !== step) openSkillPage({ kind: "improve", id: sk.id, step: i }); });
      li.append(b);
      steps.append(li);
    });
    const [, title, lead] = IMPROVE_STEPS[step];
    const card = deps.el("section", "v4s-card");
    card.append(deps.el("h3", "", title), deps.el("p", "v4s-card-lead", lead));
    const part = step === 0 ? deps.skillGenerate(sk) : deps.skillReview(sk, ["compare", "review", "trial"][step - 1]);
    part.open = true;
    card.append(part);
    const next = deps.el("div", "v4s-actions");
    if (step > 0) next.append(deps.button(`← ${IMPROVE_STEPS[step - 1][0]}`, "ghost", null, () => openSkillPage({ kind: "improve", id: sk.id, step: step - 1 })));
    if (step < 3) next.append(deps.button(`다음: ${IMPROVE_STEPS[step + 1][0]}`, "", null, () => openSkillPage({ kind: "improve", id: sk.id, step: step + 1 })));
    wrap.append(steps, card, next);
    return wrap;
  }

  const IMPROVE_SIDE = [
    ["개선 목표", "이만큼 좋아지면 통과예요", "통과 기준은 평가 서버의 기본값으로 채워요. 바꾸려면 고급 설정을 여세요. 아직 검토하지 않은 교훈은 관측이 3개 이상일 때만 써요.", "생성 결과는 ‘검토·승인’에서 확인해요."],
    ["지금 상태", "평가 서버에서 비교해요", "앱의 API 연결과 같은 평가 서버에서, 등록된 고정 과제와 실행 조건만 써요. 실행만으로 승인·적용되지는 않아요.", "저장된 평가로 다음 단계에서 후보를 검토해요."],
    ["결정", "후보 버전을 승인할까요?", "다른 사람이 만든 통과 평가가 있어야 승인할 수 있어요. 승인한 뒤 ‘이 버전 적용’으로 바꿔요.", "관리 계정에서만 쓸 수 있어요."],
    ["시험 상태", "정한 기간만 시험해요", "기본 버전은 그대로 두고, 고른 프로그램에서만 승인된 후보를 써요. 끝나는 시각이 지나면 기본 버전으로 돌아가요.", "연결이 끊겨도 요청은 보관돼요. 다시 연결한 뒤 상태를 확인하세요."]];

  function skillPageDetail() {
    const p = deps.state.skillPage;
    if (p.kind === "install") { const body = deps.detailShell("GitHub에서 설치"); body.append(skillPageCache?.side || ""); return; }
    const sk = deps.catalogQuery.record(`skill:${p.id}`) || deps.state.data.skills.find((x) => x.id === p.id);
    const [label, title, text, foot] = IMPROVE_SIDE[p.step];
    const body = deps.detailShell(label);
    body.append(deps.el("h2", "", title), deps.el("p", "v4s-lead", text));
    const s = skSection("이 스킬");
    if (sk) s.append(skFacts([["이름", sk.name, "mono"], ["버전", sk.version || "", "mono"], ["위치", (sk.source_path || "").replace(/\/SKILL\.md$/, ""), "mono"]]));
    body.append(s, deps.el("p", "meta", foot));
  }

  const firstSentence = (t) => (t || "").split(/(?<=[.다요])\s/)[0];

  function renderChangeBar() {
    const bar = deps.$("#change-bar");
    const changes = [...deps.state.pending.entries()];
    bar.hidden = deps.state.view !== "skills" || !changes.length;
    if (bar.hidden) return;
    const [[k, want]] = changes;
    const [id, agent] = k.split("|");
    const sk = deps.catalogQuery.record(`skill:${id}`) || deps.state.data.skills.find((x) => x.id === id);
    deps.$("#change-count").textContent = `바뀐 칸 ${changes.length}개`;
    const what = `${sk?.name || id} → ${deps.agentName(agent)}${want === "allow" ? "에 추가" : "에서 빼기"}`;
    deps.$("#change-text").textContent = changes.length > 1 ? `${what} 외 ${changes.length - 1}개` : what;
  }

  // One availability call per skill: the owner's labels are replaced as a whole, so
  // read the current ones first and change only the staged agents.
  async function applyChanges() {
    const bySkill = new Map();
    for (const [k, want] of deps.state.pending) {
      const [id, agent] = k.split("|");
      if (!bySkill.has(id)) bySkill.set(id, []);
      bySkill.get(id).push([agent, want]);
    }
    const btn = deps.$("#change-apply");
    btn.disabled = true;
    let done = 0;
    try {
      for (const [id, changes] of bySkill) {
        const path = `/v1/skills/${encodeURIComponent(id)}`;
        const sk = await deps.request(`${path}?${deps.query()}`);
        const compat = { ...sk.skill_compatibility };
        for (const [agent, want] of changes) compat[agent] = want === "allow" ? "supported" : "unsupported";
        await deps.request(`${path}/availability`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: sk.revision, status: sk.status, compatibility: compat }) });
        changes.forEach(([agent]) => deps.state.pending.delete(`${id}|${agent}`));
        done++;
      }
      deps.notice(`스킬 ${done}개에 적용했어요. 에이전트가 바로 받아요.`);
    } catch (e) {
      deps.notice(`적용하지 못했어요 (${e.message}). 남은 칸은 그대로 둘게요.`);
    } finally {
      btn.disabled = false;
      deps.state.data.skills = await deps.collectionPaging.read("skills", true).catch(() => deps.state.data.skills);
      await deps.catalogQuery.refreshVisible();
      deps.render(false);
      deps.renderDetail();
    }
  }

  deps.$("#change-apply").addEventListener("click", applyChanges);

  deps.$("#change-revert").addEventListener("click", () => { deps.state.pending.clear(); deps.render(false); deps.renderDetail(); });


  // ---------------------------------------------------------------- MCP·플러그인 (TOOLS)
  // Which agents' tool servers the host sync reads; the rest are only filtered by what the server reported.
  const AGENT_SYNC = { claude_code: "서버의 ~/.claude.json을 1분마다 읽어요", codex: "서버의 codex mcp list를 1분마다 읽어요" };

  return {
    get findingText() { return findingText; },
    get AGENT_SYNC() { return AGENT_SYNC; },
    get allowed() { return allowed; },
    get compatOf() { return compatOf; },
    get openSkillPage() { return openSkillPage; },
    get renderChangeBar() { return renderChangeBar; },
    get renderSkills() { return renderSkills; },
    get skillChecks() { return skillChecks; },
    get skillDetailV4() { return skillDetailV4; },
    get skillPageDetail() { return skillPageDetail; },
    get filterChip() { return filterChip; },
    get filterMenu() { return filterMenu; },
    get panelSearch() { return panelSearch; },
    get keepFocus() { return keepFocus; },
    get stage() { return stage; }
  };
}
