import { collectionFooter } from "./collectionPaging.mjs";
// Owns skill lists and details. Dependencies are explicit live ports, never a global scope.
export function createSkillViews(deps) {


  // ---------------------------------------------------------------- 스킬
  const compatOf = (sk) => sk.skill_compatibility || {};

  // Only "unsupported" stops the router; no label means every agent gets the skill.
  const allowed = (sk, key) => compatOf(sk)[key] !== "unsupported";

  const blockedAny = (sk) => deps.shownAgents().some(([k]) => !allowed(sk, k));

  const categoryFilter = (sk) => deps.state.skillCat === "all" || (deps.state.skillCat === "none" ? !deps.pages.categoryOf(sk) : deps.pages.categoryOf(sk)?.id === deps.state.skillCat);

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

  function skillFilter(sk) {
    if (deps.state.skillModel && allowed(sk, deps.state.skillModel.replace("!", "")) === deps.state.skillModel.startsWith("!")) return false; // "!key" = 못 쓰는 스킬
    if (deps.state.skillTab === "trash") return sk.status === "deprecated";
    if (sk.status === "deprecated") return false;
    if (deps.state.skillTab === "unused") return unused(sk);
    if (deps.state.skillTab === "plugin") return fromPlugin(sk);
    if (deps.state.skillTab === "update") return updatesOf(sk).length > 0;
    if (deps.state.skillTab === "missing") return blockedAny(sk);
    if (deps.state.skillTab === "caution") return ["warn", "bad"].includes(skillChecks(sk)?.security.cls);
    return true;
  }

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
  // state.skillPage opens a page inside the same view: 개선하기 (4 steps), 휴지통·중복, GitHub에서 설치.
  const SKILL_STATES = [["all", "전체"], ["missing", "빠진 곳 있음"], ["unused", "안 씀"], ["caution", "보안 주의"], ["update", "업데이트 있음"], ["plugin", "플러그인 스킬"], ["trash", "휴지통"]];

  const IMPROVE_STEPS = [["개선안", "무엇을 고칠까요?", "관련 교훈을 찾아 고를 관측을 선택하고, 평가 과제 파일을 넣어 주세요."],
    ["비교 평가", "기존 버전과 비교해요", "같은 평가 과제로 두 버전을 돌려 점수·비용·시간을 비교해요."],
    ["검토·승인", "무엇이 바뀌었나요", "후보 버전의 원문과 평가를 보고 승인하거나 제외해요."],
    ["시험 적용", "실제 작업에 시험 적용해요", "정한 프로그램에서 일정 기간 새 버전을 써 보고 결과를 모아요."]];

  let skillPageCache = null;
 // { key, main, side }: a page with forms survives the 10-second refresh
  const skillPageKey = () => JSON.stringify(deps.state.skillPage);

  const liveSkills = () => deps.state.data.skills.filter((sk) => sk.status !== "deprecated" && !deps.trashed().some((x) => x.id === sk.id));

  function openSkillPage(page) {
    deps.state.skillPage = page;
    if (page.id) deps.state.selected = `skill:${page.id}`; else deps.state.selected = null;
    skillPageCache = null;
    deps.$("#table").scrollTop = 0;
    deps.render();
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

  function skSelect(label, options, current, onPick) {
    const s = deps.el("select", "v4s-select");
    s.setAttribute("aria-label", label);
    for (const [k, l] of options) { const o = deps.el("option", "", l); o.value = k; o.selected = k === current; s.append(o); }
    s.addEventListener("change", () => onPick(s.value));
    return s;
  }

  function panelSearch(placeholder) {
    const box = deps.el("label", "v4s-search");
    const input = deps.el("input"); input.type = "search"; input.placeholder = placeholder; input.value = deps.$("#search").value; input.setAttribute("aria-label", placeholder);
    input.addEventListener("input", () => { deps.$("#search").value = input.value; deps.state.query = input.value.trim().toLowerCase(); deps.state.skillLimit = 0; deps.render(false); });
    box.append(deps.icon("magnifying-glass"), input);
    return box;
  }

  function keepFocus(sel) {
    const was = document.activeElement?.closest?.(sel);
    const input = was?.querySelector("input"), start = input?.selectionStart, end = input?.selectionEnd;
    return () => { if (!was) return; const n = deps.$(`${sel} input`); if (n) { n.focus({ preventScroll: true }); n.setSelectionRange(start, end); } };
  }

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
    deps.$("#title-count").textContent = `${catalogState.total ?? live.length}개`;
    const restore = keepFocus(".v4s-search");
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
    const bar = deps.el("div", "v4s-bar");
    const states = SKILL_STATES.map(([k, l]) => [k, `상태: ${l}${k === "all" ? "" : ` ${n[k] ?? "…"}`}`]);
    const modelKeys = deps.shownAgents().map(([k]) => k);
    if (deps.state.skillModel && !modelKeys.includes(deps.state.skillModel.replace("!", ""))) deps.state.skillModel = "";
    const models = [["", "모든 에이전트"], ...modelKeys.flatMap((k) => [[k, `${deps.agentName(k)} 쓰는 스킬 ${live.filter((sk) => allowed(sk, k)).length}`], [`!${k}`, `${deps.agentName(k)} 못 쓰는 스킬 ${live.filter((sk) => !allowed(sk, k)).length}`]])];
    const dir = deps.el("button", "v4s-dir", skillDir() > 0 ? "↑" : "↓"); dir.type = "button"; dir.title = "정렬 방향 바꾸기"; dir.setAttribute("aria-label", dir.title);
    dir.addEventListener("click", () => pickSkillSort(deps.state.skillSort));
    bar.append(panelSearch("스킬 검색"), skSelect("상태", states, deps.state.skillTab, (k) => deps.go("skills", k)), skSelect("정렬", SKILL_SORTS, deps.state.skillSort, pickSkillSort), dir,
      skSelect("에이전트", models, deps.state.skillModel, (k) => { deps.state.skillModel = k; deps.render(false); }));
    deps.$("#controls").replaceChildren(...parts, bar);
    // 분류 chips: 전체 + the owner's categories (설정 › 카테고리).
    const cats = deps.pages.prefs.categories;
    const source = deps.state.skillTab === "trash" ? skills.filter((sk) => sk.status === "deprecated") : live;
    const chip = (id, label, count) => {
      const c = deps.el("button", `chip${deps.state.skillCat === id ? " active" : ""}`); c.type = "button";
      c.append(label, deps.el("output", "", String(count)));
      c.addEventListener("click", () => { deps.state.skillCat = id; deps.state.skillLimit = 0; deps.render(false); });
      return c;
    };
    const counts = catalogState.facets.categories || {}, total = Object.values(counts).reduce((a,b) => a+b,0);
    const none = counts[""] || 0;
    deps.$("#chips").replaceChildren(...(cats.length ? [chip("all", "전체", total), ...cats.map((c) => chip(c.id, c.name, counts[c.id] || 0)), ...(none ? [chip("none", "분류 안 됨", none)] : [])] : []));
    deps.$("#chips").classList.add("v4s-cats");
    deps.$("#stats").replaceChildren(deps.el("p", "meta memory-page-note", catalogState.error ? "전체 목록을 불러오지 못했어요. 다시 불러오기를 눌러 주세요." : catalogState.loading ? "전체 목록에서 찾는 중…" : `전체 검색 결과 ${catalogState.total ?? 0}개 · 검색·정렬은 서버 전체 기준이에요. 같은 이름 묶음은 불러온 항목 기준이에요.`));
    const items = skills;
    const groups = groupSkills(items, true);
    const limit = deps.state.skillLimit || 50;
    if (!deps.state.selected && groups[0] && window.matchMedia("(min-width: 1100px)").matches) deps.state.selected = `skill:${groups[0][0].id}`;
    const list = deps.el("div", "v4s-list");
    const rows = groups.slice(0, limit).flatMap(copies => {
      const open = deps.state.openCopies.has(copies[0].name);
      return (open ? copies : copies.slice(0, 1)).map((sk, copyIndex) => ({ sk, copies, open, copyIndex }));
    });
    const table = deps.$("#table");
    table.className = "v4s-wrap";
    table.replaceChildren(list);
    deps.virtualList.mount({ container: list, scrollport: table, items: rows, row: skillRow, id: item => item.sk.id, key: JSON.stringify([deps.generation, deps.state.skillTab, deps.state.skillCat, deps.state.skillModel, deps.state.skillSort, deps.state.skillDir, deps.state.query]) });
    if (!items.length) list.append(deps.el("div", "empty", deps.state.query ? "검색과 맞는 스킬이 없어요." : "조건에 맞는 스킬이 없어요."));
    const generation = deps.generation, tab = deps.state.skillTab;
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
  const skillTalk = new Map();
 // AI로 쉽게 설명 talks per skill, kept while the app is open
  async function skillDetailV4(sk) {
    const cat = deps.pages.categoryOf(sk)?.name || "분류 안 됨";
    const body = deps.detailShell(cat);
    const top = deps.el("div", "v4s-head");
    top.append(deps.raw(deps.el("h2", "v4s-name", sk.name)));
    if (sk.description) top.append(deps.raw(deps.el("p", "v4s-lead", sk.description)));
    // AI로 쉽게 설명 (ai-chat.js): the same bubble as AI 제안; it only answers, nothing to apply.
    const aiBtn = window.ArkismAIChat.button({ state: skillTalk.get(sk.id) || skillTalk.set(sk.id, {}).get(sk.id), label: "AI로 쉽게 설명", title: "AI로 쉽게 설명",
      sub: "이 스킬을 쉬운 말로 풀어 줘요", greeting: "이 스킬의 무엇이 궁금하세요? 무엇을 하는지, 언제 쓰는지 쉬운 말로 알려 드려요.",
      placeholder: "예: 내 작업에서는 언제 쓰면 돼?", quick: ["쉽게 설명해 줘", "언제 쓰면 좋아?", "예시를 들어 줘"],
      ask: async (q, history) => ({ say: await deps.crew.explain(`Skill name: ${sk.name}\nDescription: ${sk.description || ""}\n${history.length ? `Conversation so far:\n${history.map((m) => `${m.who === "me" ? "User" : "You"}: ${m.text}`).join("\n")}\n` : ""}\nQuestion: ${q}\n\nAnswer in two or three plain Korean sentences about what this skill does and when to use it.`) }) });
    const c = skillChecks(sk), u = useOf(sk);
    const chips = deps.el("div", "v4s-chips");
    if (c) chips.append(deps.el("span", `v4s-pill${c.quality.cls === "ok" ? "" : " amber"}`, `품질 ${c.quality.grade}${c.quality.score === null ? "" : ` · ${c.quality.score}`}`), deps.el("span", `v4s-pill${c.security.cls === "ok" ? "" : " amber"}`, `보안 ${c.security.label}`));
    if (deps.state.data.skillUse) chips.append(deps.el("span", "v4s-pill muted", u ? `${u.count}번 사용 · ${deps.ago(u.last_used_at)}` : "최근 60일 안 씀"));
    top.append(chips, aiBtn);
    const parts = [top];
    if (sk.status === "deprecated") {
      const card = deps.el("div", "v4s-attn small");
      const t = deps.el("div", "v4s-attn-text"); t.append(deps.el("strong", "", "휴지통에 있어요"), deps.el("p", "", "에이전트가 읽지 않아요. 되돌리면 바로 다시 받아요."));
      card.append(t, deps.button("되돌리기", "primary", null, () => restoreSkill(sk)));
      parts.push(card);
    } else parts.push(skillAgentsSection(sk));
    if (c && ["warn", "bad"].includes(c.security.cls)) parts.splice(1, 0, securityCard(c));
    const when = skSection("언제 쓰나요");
    const whenRows = deps.el("div", ""); when.append(whenRows, deps.el("p", "meta", "불러오는 중…"));
    parts.push(when);
    const crewSec = deps.crew.skillExperts(sk);
    if (crewSec) parts.push(crewSec);
    const r = deps.repoOf(sk), ups = updatesOf(sk);
    const ver = (v) => (/^\d/.test(v || "") ? `v${v}` : v || "");
    const src = skSection("출처와 업데이트");
    const version = sk.version ? `${ver(sk.version)}${ups.length ? ` → ${ver(ups[0].to)} 있음` : r ? (r.behind ? ` · 새 커밋 ${r.behind}개` : " · 최신") : ""}` : "";
    src.append(skFacts([["출처", r ? (r.remote || r.repo) : "", "mono"], ["버전", version, "mono"], ["위치", (sk.source_path || "").replace(/\/SKILL\.md$/, ""), "mono"], ["업데이트", r ? `마지막 확인 ${deps.ago(r.checked_at)}` : ""]]));
    parts.push(src);
    const act = deps.el("div", "v4s-actions");
    if (sk.status !== "deprecated") {
      act.append(deps.button("AI로 개선", "", null, () => openSkillPage({ kind: "improve", id: sk.id, step: 0 })));
      if (ups.length) act.append(deps.button("업데이트 받기", "", null, () => deps.updateSkills(ups)));
      else if (r) {
        const checking = (deps.state.data.tools?.requests || []).some((q) => q.kind === "skill_check" && q.name === r.repo && q.status === "pending");
        const b = deps.button(checking ? "확인 중…" : "업데이트 확인", "", null, () => deps.checkSkillRepo(r)); b.disabled = checking; act.append(b);
      }
      act.append(deps.button("휴지통으로", "ghost", null, () => deps.confirmBatch([sk], () => deps.trashSkills([sk]))));
      parts.push(act);
      const hist = deps.el("button", "v4s-history", `버전 이력${sk.version_history?.length ? ` ${sk.version_history.length}건` : ""} ›`); hist.type = "button";
      hist.addEventListener("click", () => openSkillPage({ kind: "improve", id: sk.id, step: 2 }));
      parts.push(hist);
    }
    // 품질·보안 검사와 에이전트별 사용 기록은 접어 둔다.
    const more = deps.el("details", "v4s-more");
    more.append(deps.el("summary", "", "검사·사용 자세히"), deps.checksSection(sk));
    if (u) { const s = deps.section("최근 60일 사용", `마지막 ${deps.ago(u.last_used_at)}`); s.append(deps.facts(Object.entries(u.by_runtime || {}).sort((a, b) => b[1] - a[1]).map(([rt, n]) => [deps.agentName(rt) || rt, `${n}번`]))); more.append(s); }
    const pcs = skSection("에이전트 PC 연결");
    const pcRow = deps.el("div", "v4s-chips");
    for (const [k, n] of deps.shownAgents()) { const t = deps.el("span", "v4s-pc"); t.append(deps.pcLink(k), n); pcRow.append(t); }
    pcs.append(pcRow, deps.pcLegend());
    more.append(pcs);
    parts.push(more);
    body.append(...parts);
    const g = deps.generation;
    const v = await deps.request(`/v1/skill_versions/${encodeURIComponent(sk.version_id)}?${deps.query()}`).catch(() => null);
    if (g !== deps.generation || deps.state.selected !== `skill:${sk.id}` || !when.isConnected) return;
    const a = v?.analysis || {};
    const join = (x) => (Array.isArray(x) ? x.join(" · ") : x || "");
    const rows = [["이럴 때", join(a.use_when)], ["쓰지 마세요", join(a.avoid_when)], ["쓰는 도구", join(a.tools)]];
    when.replaceChildren(when.firstChild, ...(rows.some(([, x]) => x) ? [skFacts(rows, true)] : [deps.el("p", "meta", "스킬 설명에 쓰는 때가 적혀 있지 않아요.")]));
  }


  // ---------------------------------------------------------------- 스킬 안의 페이지: 개선하기 · 휴지통·중복 · GitHub에서 설치
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
    } else if (p.kind === "trash") {
      skillPageCache = { key, main: trashMain(p) };
    } else if (p.kind === "route") {
      if (fresh) { const side = deps.el("div", "v4s-side"); skillPageCache = { key, side, main: routeMain(side) }; }
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
    ["개선 목표", "이만큼 좋아지면 통과예요", "필요한 점수 향상·비용 상한·시간 상한을 정해요. 아직 검토하지 않은 교훈은 독립 관측이 3개 이상일 때만 써요.", "생성 결과는 ‘검토·승인’에서 확인해요."],
    ["지금 상태", "평가 서버에서 비교해요", "앱의 API 연결과 같은 평가 서버에서, 등록된 고정 과제와 실행 조건만 써요. 실행만으로 승인·적용되지는 않아요.", "저장된 평가로 다음 단계에서 후보를 검토해요."],
    ["결정", "후보 버전을 승인할까요?", "다른 사람이 만든 통과 평가가 있어야 승인할 수 있어요. 승인한 뒤 ‘이 버전 적용’으로 바꿔요.", "관리 계정에서만 쓸 수 있어요."],
    ["시험 상태", "정한 기간만 시험해요", "기본 버전은 그대로 두고, 고른 프로그램에서만 승인된 후보를 써요. 끝나는 시각이 지나면 기본 버전으로 돌아가요.", "연결이 끊겨도 요청은 보관돼요. 다시 연결한 뒤 상태를 확인하세요."]];

  function skillPageDetail() {
    const p = deps.state.skillPage;
    if (p.kind === "install") { const body = deps.detailShell("GitHub에서 설치"); body.append(skillPageCache?.side || ""); return; }
    if (p.kind === "route") {
      const picked = deps.catalogQuery.record(deps.state.selected) || deps.state.data.skills.find((x) => `skill:${x.id}` === deps.state.selected);
      if (picked) return skillDetailV4(picked);
      const body = deps.detailShell("고르기 시험"); body.append(skillPageCache?.side || ""); return;
    }
    if (p.kind === "trash") {
      const s = [...deps.trashed(), ...deps.state.data.skills].find((x) => `skill:${x.id}` === deps.state.selected);
      if (s) return skillDetailV4(s);
      if (p.tab === "copies") return copiesDetail();
      const body = deps.detailShell("휴지통");
      body.append(deps.el("h2", "", "휴지통에 있는 스킬은 에이전트가 읽지 않아요"), deps.el("p", "v4s-lead", "되돌리면 바로 다시 받아요. 60일 동안 한 번도 쓰이지 않은 스킬은 ‘안 씀’에서 한꺼번에 옮길 수 있어요."));
      return;
    }
    const sk = deps.catalogQuery.record(`skill:${p.id}`) || deps.state.data.skills.find((x) => x.id === p.id);
    const [label, title, text, foot] = IMPROVE_SIDE[p.step];
    const body = deps.detailShell(label);
    body.append(deps.el("h2", "", title), deps.el("p", "v4s-lead", text));
    const s = skSection("이 스킬");
    if (sk) s.append(skFacts([["이름", sk.name, "mono"], ["버전", sk.version || "", "mono"], ["위치", (sk.source_path || "").replace(/\/SKILL\.md$/, ""), "mono"]]));
    body.append(s, deps.el("p", "meta", foot));
  }

  // 스킬 고르기 시험: POST /v1/skills/route — the same search + judge an agent's skill_search uses, nothing runs.
  function routeMain(side) {
    const wrap = deps.el("div", "v4s-route");
    const card = deps.el("section", "v4s-card");
    const ask = deps.el("textarea", "v4s-route-ask"); ask.rows = 3; ask.placeholder = "예: 이 PR 바뀐 부분 리뷰해 줘"; ask.setAttribute("aria-label", "할 일");
    const agents = skSelect("에이전트", [["", "모든 에이전트"], ...deps.shownAgents().map(([k, n]) => [k, n])], "", () => {});
    const go = deps.button("어떤 스킬을 고를지 보기", "primary small", null, run);
    const bar = deps.el("div", "v4s-actions"); bar.append(agents, go);
    card.append(deps.el("h3", "", "에이전트에게 할 말"), deps.el("p", "v4s-card-lead", "에이전트가 스킬을 찾을 때와 같은 방법으로 후보를 뽑고 점수를 매겨요."), ask, bar);
    const out = deps.el("div", "v4s-list v4s-route-out"); out.hidden = true;
    const status = deps.el("p", "v4s-foot"); status.setAttribute("role", "status");
    wrap.append(card, status, out);
    const explain = () => side.replaceChildren(deps.el("h2", "", "실제로 실행하지 않아요"),
      deps.el("p", "v4s-lead", "후보는 설명·‘이럴 때 써요’가 할 일과 얼마나 맞는지로 뽑고, 판정 모델이 맞음 정도를 매겨요. 기대와 다른 스킬이 뽑히면 그 스킬의 설명을 고쳐 주세요."));
    explain();
    let busy = false;
    async function run() {
      const q = ask.value.trim();
      if (!q || busy) { if (!q) ask.focus(); return; }
      busy = go.disabled = true; status.textContent = "고르는 중…"; out.hidden = true;
      try {
        const r = await deps.request("/v1/skills/route", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, query: q, limit: 8, ...(agents.value ? { runtime: agents.value } : {}) }) });
        if (r?.authority !== "data_only_untrusted" || !Array.isArray(r.judgments)) throw new Error("invalid_response");
        const byVer = new Map(deps.state.data.skills.map((sk) => [sk.version_id, sk]));
        const list = [...r.judgments].sort((a, b) => (b.id === r.selected_id) - (a.id === r.selected_id) || (b.suitability || 0) - (a.suitability || 0));
        out.replaceChildren(...list.map((j) => {
          const sk = byVer.get(j.id);
          const cell = deps.el("div", "v4s-text");
          cell.append(deps.raw(deps.el("div", "v4s-title v4s-name", sk?.name || j.id)), deps.raw(deps.el("div", "v4s-meta", [deps.pages.categoryOf(sk || {})?.name, firstSentence(sk?.description)].filter(Boolean).join(" · ") || "목록에 없는 버전")));
          const badge = j.id === r.selected_id ? skBadge("고름") : j.conflict ? skBadge("충돌", "amber") : j.needs_human_review ? skBadge("확인 필요", "amber") : null;
          const score = deps.el("span", "v4s-uses", r.provider === "rules" ? "—" : `${Math.round((j.suitability || 0) * 100)}점`);
          const rr = deps.row(sk ? `skill:${sk.id}` : `route:${j.id}`, "", [cell, ...(badge ? [badge] : []), score], deps.renderDetail);
          rr.classList.add("v4s-row");
          return rr;
        }));
        if (!list.length) out.append(deps.el("div", "empty", "맞는 스킬이 없어요. 에이전트는 스킬 없이 일해요."));
        out.hidden = false;
        const picked = byVer.get(r.selected_id);
        status.textContent = picked ? `${picked.name}를 골라요 · 후보 ${list.length}개` : list.length ? `후보 ${list.length}개 · 확실한 하나를 고르지 못했어요` : "";
        side.replaceChildren(deps.el("h2", "", picked ? picked.name : "고른 스킬 없음"),
          deps.el("p", "v4s-lead", picked ? (picked.description || "") : "판정 모델이 확신하지 못했거나 맞는 후보가 없어요. 에이전트는 후보를 보고 직접 골라요."),
          skFacts([["판정", r.provider === "rules" ? "규칙 (판정 모델 없음)" : r.provider], ["후보", `${list.length}개`], ["에이전트", agents.value ? deps.agentName(agents.value) : "모두"],
            ["참고", (r.degraded || []).length ? `판정 모델을 못 써서 규칙으로 골랐어요 (${r.degraded.join(", ")})` : ""]]));
      } catch (e) {
        status.textContent = /HTTP 404|not_found/.test(e.message) ? "이 서버는 아직 스킬 고르기 시험을 지원하지 않아요." : `고르지 못했어요 (${e.message}).`;
        explain();
      } finally { busy = go.disabled = false; }
    }
    ask.addEventListener("keydown", (e) => { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void run(); });
    return wrap;
  }

  // 휴지통·중복 (Figma 198:4500)
  function trashMain(p) {
    const wrap = deps.el("div", "v4s-trash");
    const gone = deps.trashed();
    const idle = deps.state.data.skillUse ? liveSkills().filter(unused) : [];
    const copies = groupSkills(liveSkills()).filter((g) => g.length > 1);
    const tab = p.tab || "trash";
    const tabs = deps.el("div", "v4s-seg"); tabs.setAttribute("role", "tablist");
    for (const [k, l, n] of [["trash", "휴지통", gone.length], ["unused", "안 씀", idle.length], ["copies", "중복", copies.length]]) {
      const b = deps.el("button", k === tab ? "on" : "", l); b.type = "button"; b.setAttribute("role", "tab"); b.setAttribute("aria-selected", String(k === tab));
      b.append(deps.el("output", "", String(n)));
      b.addEventListener("click", () => { deps.state.selected = null; deps.state.skillPage = { kind: "trash", tab: k }; deps.render(); });
      tabs.append(b);
    }
    wrap.append(tabs);
    const reads = (sk) => deps.shownAgents().filter(([k]) => allowed(sk, k)).length;
    const line = (sk, action) => {
      const cell = deps.el("div", "v4s-text");
      const u = useOf(sk);
      cell.append(deps.raw(deps.el("div", "v4s-title v4s-name", sk.name)), deps.raw(deps.el("div", "v4s-meta", [firstSentence(sk.description), `최근 60일 ${u?.count || 0}번 사용 · ${reads(sk)}곳에서 읽힘`].filter(Boolean).join(" · "))));
      const r = deps.row(`skill:${sk.id}`, "", [cell, action], deps.renderDetail);
      r.classList.add("v4s-row");
      return r;
    };
    const list = deps.el("div", "v4s-list");
    if (tab === "trash") {
      list.append(...gone.map((sk) => { const b = deps.el("span", "v4s-link", "되돌리기"); b.addEventListener("click", (e) => { e.stopPropagation(); void restoreSkill(sk); }); return line(sk, b); }));
      if (!gone.length) list.append(deps.el("div", "empty", "휴지통이 비어 있어요."));
    } else if (tab === "unused") {
      list.append(...idle.map((sk) => { const b = deps.el("span", "v4s-link", "휴지통으로"); b.addEventListener("click", (e) => { e.stopPropagation(); deps.confirmBatch([sk], () => deps.trashSkills([sk])); }); return line(sk, b); }));
      if (!idle.length) list.append(deps.el("div", "empty", deps.state.data.skillUse ? "60일 동안 안 쓴 스킬이 없어요." : "이 서버는 아직 사용 기록을 모으지 않아요."));
    } else {
      for (const g of copies) {
        const cell = deps.el("div", "v4s-text");
        cell.append(deps.raw(deps.el("div", "v4s-title v4s-name", g[0].name)), deps.raw(deps.el("div", "v4s-meta", [firstSentence(g[0].description), `${g.length}곳에 있어요`].filter(Boolean).join(" · "))));
        const r = deps.row(`copies:${g[0].name}`, "", [cell, deps.el("span", "v4s-uses", `${g.length}곳`)], deps.renderDetail);
        r.classList.add("v4s-row");
        list.append(r);
      }
      if (!copies.length) list.append(deps.el("div", "empty", "같은 이름으로 여러 곳에 있는 스킬이 없어요."));
    }
    wrap.append(list);
    if (idle.length && tab !== "copies" && !p.dismissed) {
      const card = deps.el("section", "v4s-card");
      card.append(deps.el("h3", "", `안 쓰는 스킬 ${idle.length}개를 휴지통으로 옮길까요?`), deps.el("p", "v4s-card-lead", "60일 동안 한 번도 쓰이지 않았어요. 휴지통에서 언제든 되돌릴 수 있어요."),
        deps.el("p", "v4s-names mono", idle.slice(0, 6).map((sk) => sk.name).join(" · ") + (idle.length > 6 ? ` 외 ${idle.length - 6}개` : "")));
      const act = deps.el("div", "v4s-actions");
      act.append(deps.button(`${idle.length}개 옮기기`, "primary", null, () => deps.trashSkills(idle)), deps.button("취소", "ghost", null, () => { deps.state.skillPage = { ...p, dismissed: true }; deps.render(false); }));
      card.append(act);
      wrap.append(card);
    }
    return wrap;
  }

  function copiesDetail() {
    const name = (deps.state.selected || "").replace(/^copies:/, "");
    const g = groupSkills(liveSkills()).find((x) => x[0].name === name && x.length > 1);
    const body = deps.detailShell("중복");
    if (!g) { body.append(deps.el("p", "v4s-lead", "왼쪽에서 중복된 스킬을 골라요.")); return; }
    body.append(deps.el("h2", "", `${name}가 ${g.length}곳에 있어요`), deps.el("p", "v4s-lead", "에이전트마다 다른 쪽을 읽을 수 있어요. 한 곳을 원본으로 두고, 다른 쪽은 지우거나 에이전트에서 빼 주세요."));
    const s = skSection("위치");
    g.forEach((sk, i) => {
      const r = deps.el("button", "v4s-path"); r.type = "button";
      r.append(deps.el("span", "mono", (sk.source_path || "").replace(/\/SKILL\.md$/, "")), i ? deps.el("span", "v4s-path-tag", "사본") : skBadge("원본"));
      r.title = "누르면 이 사본의 상세를 봐요";
      r.addEventListener("click", () => { deps.state.selected = `skill:${sk.id}`; deps.renderDetail(); });
      s.append(r);
    });
    body.append(s, skillAgentsSection(g[0]), deps.el("p", "meta", "칩을 누르면 원본을 그 에이전트에 넣거나 빼요. 위치를 누르면 그 사본을 보고 휴지통으로 옮길 수 있어요."));
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
    get skSelect() { return skSelect; },
    get stage() { return stage; }
  };
}
