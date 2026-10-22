// 크루 › 자동화 상세·새로 만들기 팝업 (Figma 993:60224 · 998:59605 · 998:60693 · 1044:65953).
// One window on the shared frame (crew-popup.js): 개요 (목표 · 언제 실행 · 워크플로우 · 진행 알림 · 실행 설정 · 관리), 테스트, 기록.
// Every field edits a draft; the 저장/취소 bar shows only while something differs. crew.js passes its helpers in once.
window.ArkismAutomation = (h) => {
  const { state, el, icon, button, notice, request, query, byId, kit } = h;
  const popup = window.ArkismCrewPopup({ el, button });
  const NOTIFY_KINDS = [["discord", "디스코드"], ["telegram", "텔레그램"], ["slack", "슬랙"]];
  const MOMENTS = [["start", "시작"], ["step", "단계마다"], ["approval", "확인 필요"], ["fail", "실패"], ["end", "끝"]];
  const DEFAULT_MOMENTS = ["start", "approval", "fail", "end"];
  const DEFAULT_CRON = "0 9 * * 1-5";
  // 일정 in plain words → cron, for the common Korean shapes ("평일 아침 9시", "매주 월·수 오후 3시 반", "매일 18:30").
  // null when it can't tell; the caller then asks the AI.
  function whenToCron(text) {
    const t = String(text || "").replace(/\s+/g, " ").trim();
    if (!t) return null;
    if (/매월|매달|달마다|\d\s*일(?!요)/.test(t)) return null; // monthly or a day of the month: the AI handles it
    const DAYS = "일월화수목금토";
    let dow = null;
    if (/평일|주중/.test(t)) dow = "1-5";
    else if (/주말/.test(t)) dow = "0,6";
    else if (/매일|날마다|하루/.test(t)) dow = "*";
    else {
      const found = [...new Set([...t.matchAll(/([일월화수목금토])(?:요일)?(?=[\s,·.요]|$)/g)].map((m) => DAYS.indexOf(m[1])))];
      found.sort((a, b) => a - b);
      if (found.length) dow = found.join(",");
    }
    let hour = null, min = 0;
    const hm = /(\d{1,2})\s*[:시]\s*(\d{1,2})?\s*([분반])?/.exec(t);
    if (hm) { hour = Number(hm[1]); min = hm[3] === "반" || /반/.test(t.slice(hm.index, hm.index + hm[0].length + 1)) && !hm[2] ? 30 : Number(hm[2] || 0); }
    else if (/아침/.test(t)) hour = 9; else if (/점심/.test(t)) hour = 12; else if (/저녁/.test(t)) hour = 18; else if (/밤/.test(t)) hour = 21;
    if (hour == null || hour > 23 || min > 59) return null;
    if ((/오후|저녁|pm/i.test(t) || /밤/.test(t) && hour >= 6) && hour < 12) hour += 12; // 밤 2시 is 2 at night, 밤 9시 is 21
    if (/오전|새벽|밤|am/i.test(t) && hour === 12) hour = 0;
    return `${min} ${hour} * * ${dow ?? "*"}`;
  }
  // "m h * * dow" with plain numbers is what the picker edits; anything else stays a raw cron line.
  function simpleCron(cron) {
    const f = String(cron || "").trim().split(/\s+/);
    if (f.length !== 5 || !/^\d+$/.test(f[0]) || !/^\d+$/.test(f[1]) || f[2] !== "*" || f[3] !== "*") return null;
    const days = f[4] === "*" ? [0, 1, 2, 3, 4, 5, 6] : f[4] === "1-5" ? [1, 2, 3, 4, 5] : /^[0-7](,[0-7])*$/.test(f[4]) ? f[4].split(",").map((x) => Number(x) % 7) : null;
    return days ? { min: Number(f[0]), hour: Number(f[1]), days } : null;
  }
  const cronOf = ({ min, hour, days }) => {
    const ds = [...new Set(days)].sort((a, b) => a - b);
    const dow = ds.length === 7 || !ds.length ? "*" : ds.join(",") === "1,2,3,4,5" ? "1-5" : ds.join(",");
    return `${min} ${hour} * * ${dow}`;
  };
  const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
  const flat = (g) => h.levels(g).flat();
  const goalKey = (n) => (n.type === "ai_once" ? "prompt" : "goal");
  const kindText = (n) => (n.type === "ai_once" ? "한 번 묻기" : n.config?.collab === "on" ? `전문가 · 워커 최대 ${n.config.max_workers || 5}` : IS_AI(n) ? "전문가" : h.NODES[n.type]?.label || n.type);
  const IS_AI = (n) => h.IS_AI(n.type);
  const fmtTokens = (n) => (n >= 1000 ? `${Math.round(n / 1000)}k` : String(Math.round(n)));
  const TRIGGERS = { manual: "직접", schedule: "일정", webhook: "웹훅", call: "$이름", discord: "디스코드", agent: "에이전트", test: "테스트" };

  // 판 이력: what differs between two saved definitions, value by value ("조사 워커 3 → 5"). a = older, b = newer.
  const short = (v) => { const t = String(v ?? "").trim(); return t.length > 24 ? "" : t; };
  const limitText = (l) => [l?.max_minutes ? `${l.max_minutes}분` : "", l?.max_steps ? `단계 ${l.max_steps}번` : ""].filter(Boolean).join(" · ") || "없음";
  const change = (label, x, y) => (short(x) && short(y) ? `${label} ${short(x)} → ${short(y)}` : `${label} 바꿈`);
  function diffLines(a, b) {
    const out = [];
    const sub = (k, label, f = (v) => v) => { if (!same(a[k], b[k])) out.push(k === "name" || k === "handle" ? `${label} ${f(a[k]) || "없음"} → ${f(b[k]) || "없음"}` : change(label, f(a[k]), f(b[k]))); };
    sub("name", "이름"); sub("handle", "$이름", (v) => (v ? `$${v}` : "")); sub("role", "설명"); sub("category", "카테고리");
    for (const [k, label] of [["task", "하려는 일"], ["done", "끝난 기준"], ["output", "결과물"]]) if (!same(a.goal?.[k], b.goal?.[k])) out.push(change(label, a.goal?.[k], b.goal?.[k]));
    if (!same(a.trigger, b.trigger)) out.push(`일정·웹훅 ${a.trigger?.kind === "schedule" ? h.cronText(a.trigger.cron) : a.trigger?.kind === "webhook" ? "웹훅" : "없음"} → ${b.trigger?.kind === "schedule" ? h.cronText(b.trigger.cron) : b.trigger?.kind === "webhook" ? "웹훅" : "없음"}`);
    if (!!a.enabled !== !!b.enabled) out.push(`켜기 ${a.enabled ? "켬" : "끔"} → ${b.enabled ? "켬" : "끔"}`);
    sub("model", "모델", (v) => v || "기본");
    if (!same(a.limits, b.limits)) out.push(`멈추는 조건 ${limitText(a.limits)} → ${limitText(b.limits)}`);
    const chans = (x) => (x.notify?.channels || []).map((c) => `${c.kind}${c.channel ? ` ${c.channel}` : ""}`).join(", ") || "없음";
    if (!same(a.notify?.channels, b.notify?.channels)) out.push(`진행 알림 채널 ${chans(a)} → ${chans(b)}`);
    else if (!same(a.notify, b.notify)) out.push("진행 알림 바꿈");
    if (!same(a.cover, b.cover)) out.push("배경 사진 바꿈");
    const A = new Map((a.graph?.nodes || []).map((n) => [n.id, n]));
    const B = new Map((b.graph?.nodes || []).map((n) => [n.id, n]));
    for (const [id, n] of B) {
      const o = A.get(id);
      if (!o) { out.push(`단계 추가 ${h.nodeTitle(n)}`); continue; }
      const t = h.nodeTitle(n);
      if ((o.title || "") !== (n.title || "")) out.push(`단계 이름 ${h.nodeTitle(o)} → ${t}`);
      for (const [k, label] of [["model", "모델"], ["max_workers", "워커"], ["persona", "전문가"], ["collab", "협업"], ["access", "권한"]]) if (!same(o.config?.[k], n.config?.[k])) out.push(`${t} ${label} ${o.config?.[k] ?? "기본"} → ${n.config?.[k] ?? "기본"}`);
      const CFG = { goal: "목표", prompt: "질문", question: "질문", code: "코드", url: "주소", instructions: "지시", fence: "실행 범위" };
      for (const [k, label] of Object.entries(CFG)) if (!same(o.config?.[k], n.config?.[k])) out.push(`${t} ${label} 바꿈`);
    }
    for (const [id, n] of A) if (!B.has(id)) out.push(`단계 뺌 ${h.nodeTitle(n)}`);
    const back = (g) => new Map((g?.edges || []).filter(h.isBack).map((e) => [`${e.from}>${e.to}`, e]));
    const ba = back(a.graph), bb = back(b.graph);
    const name = (e) => `${h.nodeTitle(B.get(e.from) || A.get(e.from) || { id: e.from })} → ${h.nodeTitle(B.get(e.to) || A.get(e.to) || { id: e.to })}`;
    for (const [k, e] of bb) { const o = ba.get(k); if (!o) out.push(`되돌아가는 선 ${name(e)} 최대 ${e.max}번`); else if (o.max !== e.max) out.push(`되돌아가는 선 ${name(e)} ${o.max} → ${e.max}번`); }
    for (const [k, e] of ba) if (!bb.has(k)) out.push(`되돌아가는 선 ${name(e)} 뺌`);
    if (!same(a.graph?.edges, b.graph?.edges) && !out.some((x) => x.startsWith("되돌아가는") || x.startsWith("단계 추가") || x.startsWith("단계 뺌"))) out.push("단계 이은 순서 바꿈");
    return out;
  }

  function section(iconName, title, aside) {
    const s = el("section", "auto-sec");
    const head = el("div", "auto-sec-head");
    const t = el("h3", "");
    t.append(icon(iconName), title);
    head.append(t);
    if (aside) head.append(aside);
    s.append(head);
    return s;
  }
  const box = (...rows) => { const b = el("div", "auto-box"); b.append(...rows); return b; };
  const link = (label, fn) => { const b = el("button", "auto-link", label); b.type = "button"; b.addEventListener("click", fn); return b; };
  const pill = (label, fn) => { const b = el("button", "auto-pill", label); b.type = "button"; b.addEventListener("click", fn); return b; };
  // {{input}} and {변수} read as chips of the accent colour inside a step's goal text.
  function varText(text, cls) {
    const p = el("span", cls);
    String(text || "").split(/(\{\{?[^{}]+\}?\})/).forEach((part) => { if (/^\{.+\}$/.test(part)) p.append(el("em", "auto-var", part)); else if (part) p.append(part); });
    return p;
  }

  // One line that turns into a text box on click: Enter or leaving it saves, Esc cancels.
  function inlineText(node, get, set, attrs = {}) {
    node.classList.add("auto-inline");
    node.tabIndex = 0;
    node.setAttribute("role", "button");
    const show = () => { const v = get(); node.textContent = v || attrs.placeholder || ""; node.classList.toggle("empty", !v); };
    const edit = () => {
      const i = h.input(get(), { maxLength: attrs.maxLength || 200, placeholder: attrs.placeholder || "", cls: `auto-inline-edit ${attrs.cls || ""}` });
      let done = false;
      const end = (commit) => { if (done) return; done = true; if (commit) set(i.value.trim()); i.replaceWith(node); show(); };
      i.addEventListener("keydown", (e) => { if (e.isComposing) return; if (e.key === "Enter") { e.preventDefault(); end(true); } if (e.key === "Escape") { e.stopPropagation(); end(false); } });
      i.addEventListener("blur", () => end(true));
      node.replaceWith(i);
      i.focus();
      i.select();
    };
    node.addEventListener("click", edit);
    node.addEventListener("keydown", (e) => { if (e.key === "Enter") edit(); });
    show();
    return node;
  }

  // A row of 이름 | 값 | 버튼 whose value edits in place with the given editor.
  function valueRow(label, valueNode, aside) {
    const r = el("div", "auto-row");
    r.append(el("span", "auto-key", label), valueNode);
    if (aside) r.append(aside);
    return r;
  }

  // ------------------------------------------------------------------ the window
  function open(rec, startTab = "overview", withAI = false) {
    const creating = !rec;
    const base = creating ? { type: "workflow", name: "", handle: "", role: "", category: "", model: "", enabled: false, graph: h.defaultGraph("simple", {}) } : structuredClone(rec.crew);
    base.graph?.nodes?.forEach((n) => { n.config ||= {}; }); // so a just-opened popup does not count as edited
    const d = structuredClone(base);
    if (creating) { const s = d.graph.nodes.find((n) => n.id === "ai1"); s.title = ""; s.config.goal = ""; }
    const blank = structuredClone(d); // what a new one looks like before anything is typed
    let handleTouched = !creating;
    const picked = new Set(); // new automation: steps whose expert was chosen on purpose (until then the picker reads 맡을 전문가 고르기)
    const aiTalk = { open: withAI }; // AI 제안 / AI로 초안 talk (ai-chat.js state), kept while this window is open
    let pop = null, closeAsking = () => {};
    let bar = null;
    let paintHead = () => {};
    let stats = null;
    const live = { run: null, timer: null };
    const stopLive = () => { if (live.timer) { clearTimeout(live.timer); live.timer = null; } };

    const CHANGE = [["name", "이름"], ["handle", "$이름"], ["role", "설명"], ["category", "카테고리"], ["goal", "목표"], ["trigger", "일정·웹훅"], ["enabled", "켜기"], ["model", "모델"], ["limits", "멈추는 조건"], ["notify", "진행 알림"], ["cover", "배경 사진"], ["graph", "단계"]];
    const changed = () => (creating ? [] : CHANGE.filter(([k]) => !same(d[k], base[k])));
    const updateBar = () => {
      if (!bar) return;
      const n = creating ? 0 : changed().length;
      bar.querySelector(".primary").disabled = !creating && !n; // the bar stays for AI 제안
      bar.querySelector(".auto-bar-note").textContent = creating ? `단계 ${h.stepCount(d.graph)}개 · 만든 뒤 테스트 탭에서 전체를 시험해요` : `바뀐 칸 ${n}개 · 저장하면 다음 판 v${(rec.revision || 0) + 1} · 기록 탭에서 되돌릴 수 있어요`;
    };
    const touch = () => { updateBar(); paintHead(); };
    const dirty = () => !creating && changed().length > 0;
    const graphDirty = () => creating || !same(d.graph, base.graph);

    // -------------------------------------------------------------- head: photo, category, name, $name, one line, estimates
    function head(dlg) {
      const who = dlg.querySelector(".crew-popup-who");
      const side = dlg.querySelector(".crew-popup-actions");
      who.replaceChildren();
      const thumb = el("button", "auto-thumb");
      thumb.type = "button";
      thumb.setAttribute("aria-label", "배경 사진 고르기");
      thumb.addEventListener("click", pickCover);
      const names = el("div", "auto-names");
      const chipRow = el("div", "auto-chiprow");
      const cat = el("button", "auto-cat");
      cat.type = "button";
      cat.setAttribute("aria-label", "카테고리");
      cat.addEventListener("click", () => h.catMenu(cat, { list: h.categories(h.workflows()).map(([c]) => c), current: d.category || "", onPick: (v) => { d.category = v; touch(); } }));
      const name = el("h2", "");
      const handle = el("span", "auto-handle");
      const nameRow = el("div", "auto-namerow");
      const copyHost = el("span", "");
      nameRow.append(name, handle, copyHost);
      const role = el("p", "");
      const credit = el("small", "auto-credit");
      chipRow.append(cat);
      names.append(chipRow, nameRow, role, credit);
      who.append(thumb, names);
      inlineText(name, () => d.name, (v) => {
        d.name = v;
        if (!handleTouched) d.handle = h.freeHandle(v || h.UNTITLED);
        touch();
      }, { maxLength: 60, placeholder: "자동화 이름을 적어 주세요" });
      inlineText(role, () => d.role, (v) => { d.role = v; touch(); }, { maxLength: 200, placeholder: "무엇을 하는 자동화인가요? 한 줄로 적어요" });
      const hv = el("span", "");
      inlineText(hv, () => (d.handle ? `$${d.handle}` : "$이름"), (v) => {
        v = v.replace(/^\$/, "");
        if (v && !/^[\p{L}\p{N}_-]{1,40}$/u.test(v)) { notice("$이름은 띄어쓰기 없이 글자·숫자·_·-만 써요."); return; }
        if (v && h.workflows().some((r) => r.crew.handle === v && r.id !== rec?.id)) { notice("이미 쓰는 이름이에요. 다른 이름을 써 주세요."); return; }
        d.handle = v;
        handleTouched = true;
        touch();
      }, { maxLength: 41, cls: "mono" });
      handle.append(hv);
      paintHead = () => {
        thumb.replaceChildren();
        if (d.cover?.thumb) { const img = el("img"); img.alt = ""; img.src = d.cover.thumb; thumb.append(img); } else thumb.append(el("span", "", (d.name || "NEW").slice(0, 8).toUpperCase() + "."));
        cat.replaceChildren(...h.catChipBody(d.category));
        name.toggleAttribute("data-no-i18n", !!d.name); // the user's words stay; only the placeholder is UI text
        name.textContent = d.name || "자동화 이름을 적어 주세요";
        name.classList.toggle("empty", !d.name);
        hv.textContent = d.handle ? `$${d.handle}` : "$이름";
        role.toggleAttribute("data-no-i18n", !!d.role);
        role.textContent = d.role || "무엇을 하는 자동화인가요? 한 줄로 적어요";
        role.classList.toggle("empty", !d.role);
        copyHost.replaceChildren(d.handle ? h.copyButton(`$${d.handle}`) : "");
        credit.replaceChildren();
        if (d.cover) { const c = h.coverCredit({ cover: d.cover }); if (c) credit.append(c); }
      };
      paintHead();
      // 예상: per-run averages of the last 30 days (usage API: tokens·cost, runs: time).
      const est = el("dl", "auto-est");
      const estRow = (k, v) => { const r = el("div", ""); r.append(el("dt", "", k), el("dd", "", v)); est.append(r); };
      est.dataset.est = "1";
      side.prepend(est);
      dlg.querySelector(".crew-popup-head").append(dlg.querySelector(".crew-popup-tabs")); // 993:60224: tabs are a pill group under the estimate, like the expert and team popups
      paintEst();
      function paintEst() {
        est.replaceChildren();
        const avg = stats?.avgMs;
        estRow("예상 토큰", stats?.tokens && stats.runs ? `약 ${fmtTokens(stats.tokens / stats.runs)}` : "—");
        estRow("예상 비용", stats?.cost != null && stats.runs ? `약 $${(stats.cost / stats.runs).toFixed(2)}` : "—");
        estRow("예상 시간", avg ? `약 ${h.spanText(avg)}` : "—");
      }
      head.paintEst = paintEst;
    }

    // 배경 사진: the same Unsplash picker the settings window used.
    function pickCover() {
      const dlg = el("dialog", "crew-dialog wf-dialog");
      const form = el("form");
      form.method = "dialog";
      const cover = h.coverPicker(d.cover, d.category || "", () => ({ name: d.name, role: d.role, category: d.category, meta: d.handle ? `$${d.handle}` : "" }));
      const hd = el("div", "dialog-head");
      hd.append(el("h2", "", "배경 사진"));
      const body = el("div", "dialog-body");
      body.append(cover.side, cover.node, el("small", "dim", "고른 사진은 카드와 이 창 위쪽에 깔려요 · 사진: Unsplash"));
      const foot = el("div", "dialog-foot");
      const cancel = button("취소", "ghost");
      cancel.type = "submit";
      cancel.value = "cancel";
      const ok = button("고르기", "primary");
      ok.type = "submit";
      ok.value = "ok";
      foot.append(cancel, ok);
      form.append(hd, body, foot);
      form.addEventListener("submit", (e) => { if (e.submitter?.value === "ok") { const v = cover.value(); if (v) d.cover = v; else delete d.cover; touch(); } });
      dlg.append(form);
      dlg.addEventListener("close", () => dlg.remove());
      document.body.append(dlg);
      dlg.showModal();
    }

    // -------------------------------------------------------------- 개요
    function overview(body) {
      body.classList.add("auto-body");
      const top = el("div", "auto-top");
      body.append(top);
      if (!creating) {
        top.append(...statCells());
        const ruleBad = h.ruleChecks(base).filter((c) => !c.ok);
        for (const run of h.waitingRuns().filter((w) => w.crew_run.crew_id === rec.id)) top.append(approvalCard(run));
        if (ruleBad.length) {
          const card = el("div", "wf-attn small");
          const t = el("div", "");
          t.append(el("strong", "", `구조 검토 · 확인할 곳 ${ruleBad.length}`));
          for (const c of ruleBad) t.append(el("p", "", `${c.label}: ${c.detail}`));
          card.append(t, button("구조 검토", "", null, () => leave(() => h.openGraph(rec, { review: true, fromPopup: true }))));
          top.append(card);
        }
      }
      const ai = aiSection();
      body.prepend(ai);
      body.append(goalSection(), whenSection(), flowSection(), notifySection(), settingSection());
      if (!creating) body.append(manageSection());
      bar = el("div", "auto-bar");
      const save = button(creating ? "자동화 만들기" : "저장", "primary", null, doSave);
      const cancel = button("취소", "ghost", null, (e) => { if (creating) closeAsking(e); else { reset(); } });
      bar.append(save, cancel);
      if (creating) bar.append(button("템플릿·설정으로 만들기", "ghost", null, (e) => closeAsking(e, () => h.openSettingsNew())));
      bar.append(el("span", "auto-bar-note"), ai.dock); // AI 제안 at the right end; its purple list stays on top of the body
      body.append(bar);
      updateBar();
    }
    // Back to what was saved: close and open again so every row redraws.
    function reset() { leaveSilently(); h.openAutomation(rec.id); }
    function leaveSilently() { pop.dialog.close(); }
    const leave = async (fn) => {
      if (dirty() && !await ask("저장하지 않은 변경은 사라져요. 계속할까요?")) return;
      leaveSilently();
      fn();
    };

    function statCells() {
      const cells = [["runs", "최근 30일 실행"], ["done", "끝까지 마친 실행"], ["cost", "추정 비용"]].map(([k, label]) => {
        const c = el("div", "auto-stat");
        const v = el("div", "");
        const val = el("strong", "");
        v.append(val, el("small", "", label));
        const bars = el("span", "auto-bars");
        c.append(v, bars);
        c.dataset.k = k;
        c.paint = () => {
          const s = stats;
          val.textContent = !s ? "…" : k === "runs" ? `${s.runs}회` : k === "done" ? (s.listed ? `${s.ok} / ${s.listed}` : "—") : s.cost != null ? `$${s.cost.toFixed(2)}` : "—";
          bars.replaceChildren(...(s && k !== "cost" ? s.bins[k] : []).map((n, i, all) => { const b = el("i", i === all.length - 1 ? "now" : ""); b.style.height = `${Math.max(2, Math.round(n * 22))}px`; return b; }));
        };
        c.paint();
        return c;
      });
      const wrap = el("div", "auto-stats");
      wrap.append(...cells);
      wrap.paint = () => cells.forEach((c) => c.paint());
      statsBox = wrap;
      return [wrap];
    }
    let statsBox = null;
    async function loadStats() {
      if (creating) return;
      const runs = [];
      try { runs.push(...((await request(`/v1/crew/runs?${query({ crew_id: rec.id, limit: 30 })}`)).items || [])); } catch (_) { /* empty */ }
      let cost = null;
      let tokens = 0;
      let usageRuns = null; // the usage total counts every non-test run in 30 days; the runs list stops at 30
      try { const w = (await request(`/v1/crew/usage?${query({ range: "30d", kind: "workflow" })}`)).workflows?.find((x) => x.id === rec.id); cost = w?.cost_usd ?? null; tokens = w?.tokens || 0; usageRuns = w?.runs ?? null; } catch (_) { /* no usage yet */ }
      const now = Date.now();
      const recentRuns = runs.filter((r) => r.crew_run?.trigger !== "test" && now - new Date(r.created_at).getTime() < 30 * 864e5);
      const bin = (list) => { const a = Array(15).fill(0); for (const r of list) a[14 - Math.min(14, Math.floor((now - new Date(r.created_at).getTime()) / (2 * 864e5)))]++; const m = Math.max(1, ...a); return a.map((n) => n / m); };
      const done = recentRuns.filter((r) => r.crew_run?.status === "succeeded");
      const spans = done.map((r) => h.runMs(r)).filter(Boolean);
      stats = { runs: usageRuns ?? recentRuns.length, listed: recentRuns.length, ok: done.length, cost, tokens, avgMs: spans.length ? spans.reduce((a, b) => a + b, 0) / spans.length : 0, bins: { runs: bin(recentRuns), done: bin(done) } };
      statsBox?.paint?.();
      head.paintEst?.();
    }

    function approvalCard(run) {
      const node = (rec.crew.graph?.nodes || []).find((n) => n.id === run.crew_run.waiting.node_id);
      const card = el("div", "wf-attn");
      const t = el("div", "");
      t.append(el("strong", "", `확인해 주세요${node ? ` · ${h.nodeTitle(node)}` : ""}`), el("p", "", run.crew_run.waiting.question || node?.config?.question || "다음 단계로 갈까요?"));
      const acts = el("div", "wf-attn-actions");
      const after = async () => { await h.refreshRuns(); pop.select("overview"); };
      acts.append(button("거절", "", null, async () => { await h.decide(run, false); void after(); }), button("승인", "primary", null, async () => { await h.decide(run, true); void after(); }));
      card.append(t, acts);
      return card;
    }

    // 목표: three lines of what it is for. Saved with the workflow (crew.goal). Each line is a box with the AI sparkle.
    const GOAL_HINT = { task: "what this automation does, in one line", done: "when the run counts as done, e.g. at least 3 sources", output: "what it leaves behind, e.g. a report, a message, a file" };
    const goalRowTalk = { task: {}, done: {}, output: {} };
    function goalSection() {
      const s = section("check", "목표");
      const row = (k, label, hint) => {
        const i = h.input(d.goal?.[k] || "", { maxLength: 300, placeholder: hint, cls: "auto-goal-input" });
        i.setAttribute("aria-label", label);
        i.addEventListener("input", () => { const v = i.value.trim(); if (v) (d.goal ||= {})[k] = v; else if (d.goal) { delete d.goal[k]; if (!Object.keys(d.goal).length) delete d.goal; } touch(); });
        const f = window.ArkismAIChat.field(i, { state: goalRowTalk[k], title: `${label} 다듬기`, preset: `You write the "${k}" line of an automation's goal (${GOAL_HINT[k]}). Korean, one short line, at most 300 characters.`,
          context: () => JSON.stringify({ workflow: d.name || "", role: d.role || "", goal: d.goal || {}, steps: flat(d.graph).filter((n) => !["start", "end"].includes(n.type)).map((n) => n.title || h.nodeTitle(n)) }) });
        return valueRow(label, f);
      };
      s.append(box(row("task", "하려는 일", creating ? "무엇을 하는 자동화인지 한 줄로 적어요" : "한 줄로 적어요"), row("done", "끝난 기준", "언제 끝났다고 볼지 · 예: 출처 3개 이상"), row("output", "결과물", "무엇을 남길지 · 예: 보고서, 메시지, 파일")));
      return s;
    }

    // 단계 목표·질문 칸의 반짝이 (ai-chat.js field): talks about this one step's goal; the proposal goes into the box.
    const goalTalk = new WeakMap();
    function goalAI(n, ta) {
      if (!goalTalk.has(n)) goalTalk.set(n, {});
      const ask = goalKey(n) === "prompt";
      return window.ArkismAIChat.field(ta, { state: goalTalk.get(n), title: ask ? "질문 다듬기" : "목표 다듬기", preset: `You write the ${ask ? "question for a one-shot AI step" : "goal for an AI step"} of an automation workflow: what to do, when it is done, and the shape of the result. Keep {{input}} and other {{variables}} that are there. Korean, at most 8000 characters, short and clear.`,
        context: () => JSON.stringify({ workflow: d.name || "", step: n.title || h.nodeTitle(n), goal: d.goal || {} }) });
    }
    // AI proposals for 목표 and 언제 실행 (from AI 제안). null = nothing to change.
    const GOAL_LABEL = { task: "하려는 일", done: "끝난 기준", output: "결과물" };
    function goalProps(goal, redraw) {
      if (!goal || typeof goal !== "object") return [];
      return Object.keys(GOAL_LABEL).map((k) => {
        const to = typeof goal[k] === "string" ? goal[k].trim().slice(0, 300) : "";
        const from = d.goal?.[k] || "";
        if (!to || to === from) return null;
        const set = (v) => { if (v) (d.goal ||= {})[k] = v; else if (d.goal) { delete d.goal[k]; if (!Object.keys(d.goal).length) delete d.goal; } redraw(); };
        return { label: GOAL_LABEL[k], from, to, apply: () => set(to), undo: () => set(from) };
      }).filter(Boolean);
    }
    const trigText = (t, on) => (!on || !t ? "직접 부르기만" : t.kind === "schedule" ? `일정 · ${h.cronText(t.cron)}` : `웹훅${t.label ? ` · ${t.label}` : ""}`);
    function triggerProp(t, redraw) {
      if (!t || typeof t !== "object") return null;
      const next = t.kind === "schedule" && typeof t.cron === "string" && h.parseCron(t.cron.trim()) ? { kind: "schedule", cron: t.cron.trim() }
        : t.kind === "webhook" ? { kind: "webhook", label: String(t.label || "").slice(0, 60) } : null;
      if (!next || (d.enabled && same(next, d.trigger))) return null;
      const was = { trigger: d.trigger, enabled: d.enabled };
      return { label: "언제 실행", from: trigText(d.trigger, d.enabled), to: trigText(next, true),
        apply: () => { d.trigger = structuredClone(next); d.enabled = true; redraw(); },
        undo: () => { d.trigger = was.trigger; d.enabled = was.enabled; if (!was.trigger) delete d.trigger; redraw(); } };
    }
    // AI로 고치기 / AI로 초안 (ai-chat.js): the assist runner rewrites the graph (and in a new one the name) as
    // proposals in the purple list; 적용 puts them in the draft, the user reviews and saves.
    function aiSection() {
      return window.ArkismAIChat.button({ state: aiTalk, label: creating ? "AI로 초안" : "AI 제안", title: creating ? "AI로 초안 만들기" : "AI 제안",
        sub: creating ? "어떤 일을 자동으로 할지 말하면 단계를 그려요" : "고칠 곳이나 언제 실행할지 평소 말로 적으면 바뀐 목표·일정·단계를 보여 줘요",
        greeting: creating ? "어떤 일을 자동으로 할까요? 언제, 무엇을 하고, 결과를 어디로 보낼지 말해 주세요." : "무엇을 고칠까요? '평일 아침 9시마다'처럼 평소 말로 적으면 언제 실행을 일정이나 웹훅으로 바꿔요. 목표를 바꾸고, 단계를 넣거나 빼고, 순서와 조건도 바꿀 수 있어요.",
        placeholder: creating ? "예: 매일 아침 열린 PR을 모아 요약해서 슬랙으로 보내 줘" : "예: 평일 아침마다 돌려 줘 · 실패하면 2번까지 다시 찾게",
        quick: creating ? ["매일 아침 PR 요약", "새 이슈 분류해서 알림", "주간 보고서 쓰기"] : ["평일 아침마다 실행", "실패하면 다시 시도", "보내기 전에 확인 받기", "단계를 줄여 줘"],
        ask: async (text) => {
          const cur = JSON.stringify({ nodes: d.graph.nodes.map(({ id, type, title, config }) => ({ id, type, title, config })), edges: d.graph.edges }).slice(0, 12000);
          const extra = `Also give "goal": {"task","done","output"} (one short Korean line each) and, when the request says when to run, "trigger": {"kind":"schedule","cron":"m h dom mon dow"} or {"kind":"webhook","label":"..."} (local time zone, 1-5 = weekdays; something arriving from outside, like a PR opening, is a webhook).`;
          const prompt = creating
            ? `Build an Arkism workflow from this request. Decide sensibly without asking. ${extra}\nUser: ${text}`
            : `Change this Arkism workflow as the user asks. Keep every node id, type, title and setting the request does not touch. Answer with the whole changed workflow as {"graph":{"nodes":[],"edges":[]}} (plus "name" and "role" only if they should change). ${extra}\nWorkflow: ${cur}\nGoal: ${JSON.stringify(d.goal || {})}\nTrigger: ${JSON.stringify(d.enabled ? d.trigger || null : null)}\nRequest: ${text}`;
          const a = await kit.assist("build", prompt, { purpose: "workflow" });
          if (a.status !== "done") throw new Error(kit.assistError(a));
          const j = kit.parseJSON(a.output);
          const g = j?.graph;
          if (!Array.isArray(g?.nodes) || !g.nodes.length) throw new Error(j?.ask ? String(j.ask).slice(0, 300) : "AI 답을 읽지 못했어요. 다시 보내 주세요.");
          g.edges = Array.isArray(g.edges) ? g.edges : [];
          const next = h.layoutGraph(g);
          next.nodes.forEach((n) => { n.config ||= {}; });
          const steps = (gr) => flat(gr).filter((n) => !["start", "end"].includes(n.type)).map((n, i) => `${i + 1}. ${n.title || h.nodeTitle(n)} · ${kindText(n)}`).join("\n");
          const redraw = () => { pop.select("overview"); touch(); };
          const was = d.graph;
          // edges and step settings count too, not only the step list; layout (x, y) does not
          const shape = (gr) => JSON.stringify({ nodes: gr.nodes.map((n) => ({ id: n.id, type: n.type, title: h.nodeTitle(n), config: n.config || {} })),
            edges: (gr.edges || []).map(({ from, to, port, max }) => JSON.stringify([from, to, port || "", max || 0])).sort() }); // the shape layoutGraph keeps
          const more = diffLines({ graph: was }, { graph: next }).filter((x) => !x.startsWith("단계 추가") && !x.startsWith("단계 뺌"));
          const props = shape(was) === shape(next) ? [] : [{ label: "단계", from: steps(was), to: [steps(next), ...more].join("\n"), apply: () => { d.graph = structuredClone(next); redraw(); }, undo: () => { d.graph = was; redraw(); } }];
          if (creating && j.name && !d.name) {
            const name = String(j.name).slice(0, 60);
            props.push({ label: "이름", from: "", to: name, apply: () => { d.name = name; if (!handleTouched) d.handle = h.freeHandle(name); redraw(); }, undo: () => { d.name = ""; redraw(); } });
          }
          if (creating && j.role && !d.role) {
            const role = String(j.role).slice(0, 200);
            props.push({ label: "설명", from: "", to: role, apply: () => { d.role = role; redraw(); }, undo: () => { d.role = ""; redraw(); } });
          }
          props.push(...goalProps(j.goal, redraw), triggerProp(j.trigger, redraw));
          return { say: creating ? "AI가 단계를 그렸어요. 아래에서 보고 적용한 뒤 만들어 주세요." : "AI가 고친 단계예요. 아래에서 보고 적용한 뒤 저장해 주세요.", props };
        } });
    }

    // 언제 실행: 직접 부르기 is always on; 일정 and 웹훅 share the workflow's one trigger.
    function whenSection() {
      // Before, the link only switched 일정 on, so with 일정 already on it did nothing: now it lists every way to start.
      const add = link("+ 시작 조건 추가", () => triggerMenu());
      const s = section("clock-counter-clockwise", "언제 실행", add);
      s.querySelector(".auto-sec-head").classList.add("auto-chiprow");
      function triggerMenu() {
        const old = document.querySelector(".auto-menu");
        if (old) { old.remove(); return; }
        const m = el("div", "auto-menu right");
        const close = () => { m.remove(); document.removeEventListener("click", away, true); };
        const away = (e) => { if (!m.contains(e.target) && e.target !== add) close(); };
        const item = (label, sub, fn, cls = "") => { const b = el("button", cls); b.type = "button"; b.append(el("span", "", label), el("small", "", sub)); b.addEventListener("click", () => { close(); fn(); }); m.append(b); };
        const name = (k) => (k === "schedule" ? "일정" : "웹훅");
        const turn = async (kind, other) => {
          if (on(kind)) { notice(`${name(kind)}은 이미 켜져 있어요.`); return; }
          if (on(other) && !await ask(`${name(other)}을 끄고 ${name(kind)}으로 바꿀까요? 일정과 웹훅 중 하나만 켤 수 있어요.`)) return;
          setTrigger(kind, true); draw();
        };
        item("일정", on("schedule") ? "켜져 있어요" : "정해 둔 요일·시간에 시작", () => turn("schedule", "webhook"), on("schedule") ? "on" : "");
        item("웹훅", on("webhook") ? "켜져 있어요" : "주소로 요청이 오면 시작", () => turn("webhook", "schedule"), on("webhook") ? "on" : "");
        item("이벤트", "GitHub·메일·폴더 · 준비 중", () => notice("이벤트로 시작하기는 아직 준비 중이에요. 웹훅으로 받을 수 있어요."), "soon");
        s.querySelector(".auto-sec-head").append(m);
        setTimeout(() => document.addEventListener("click", away, true));
      }
      const list = el("div", "auto-box");
      const trig = () => d.trigger?.kind;
      const on = (k) => trig() === k && !!d.enabled;
      function setTrigger(kind, v) {
        if (v) { d.trigger = kind === "schedule" ? { kind, cron: d.trigger?.kind === "schedule" ? d.trigger.cron : DEFAULT_CRON } : { kind, label: d.trigger?.kind === "webhook" ? d.trigger.label || "" : "" }; d.enabled = true; } else d.enabled = false;
        touch();
      }
      function draw() {
        list.replaceChildren();
        list.append(valueRow("직접 부르기", el("span", "", `$${d.handle || "이름"}으로 부르거나 화면의 실행 버튼`), h.switchEl(true, "직접 부르기는 항상 켜져 있어요", () => notice("직접 부르기는 항상 켜져 있어요."))));
        const cronWrap = el("span", "auto-sched");
        const paintCron = () => {
          cronWrap.replaceChildren();
          if (trig() !== "schedule") { cronWrap.append(creating ? "요일·시간 고르기 · 예: 월·수·금 09:00" : "정해 둔 시간에 시작해요"); return; }
          const n = h.nextRun(d.trigger.cron);
          const put = (cron) => { d.trigger.cron = cron; touch(); draw(); };
          // 평소 말로: simple shapes are read here, the rest goes to the AI once.
          const say = h.input("", { maxLength: 80, placeholder: "평소 말로 적어요 · 예: 평일 아침 9시, 매주 월·목 오후 3시 반", cls: "auto-sched-say" });
          say.addEventListener("keydown", async (e) => {
            if (e.isComposing || e.key !== "Enter" || !say.value.trim()) return;
            e.preventDefault();
            const local = whenToCron(say.value);
            if (local && h.parseCron(local)) { put(local); return; }
            say.disabled = true;
            try {
              const a = await kit.assist("json", `Turn this into a 5-field cron "m h dom mon dow" in local time (1-5 = weekdays). Text: ${say.value.trim()}\nAnswer with ONE JSON object: {"cron": "..."} or {"cron": null} when it is not a time.`, { persona: { crew: { instructions: "You set when an automation runs. Reply with the JSON object only." } } });
              const j = a.status === "done" ? kit.parseJSON(a.output || "") : null;
              if (j?.cron && h.parseCron(j.cron)) put(j.cron); else notice("언제인지 알아듣지 못했어요. 요일과 시간을 넣어 다시 적어 주세요.");
            } catch (err) { notice(`AI가 답하지 못했어요 (${err?.message || err}).`); } finally { say.disabled = false; }
          });
          const sc = simpleCron(d.trigger.cron);
          const pick = el("div", "auto-sched-pick");
          if (sc) {
            const days = el("div", "auto-days");
            ["일", "월", "화", "수", "목", "금", "토"].forEach((label, i) => {
              const b = pill(label, () => { const next = sc.days.includes(i) ? sc.days.filter((x) => x !== i) : [...sc.days, i]; if (next.length) put(cronOf({ ...sc, days: next })); });
              b.classList.add("toggle"); b.classList.toggle("on", sc.days.includes(i)); b.setAttribute("aria-pressed", String(sc.days.includes(i)));
              days.append(b);
            });
            const pm = sc.hour >= 12;
            const ampm = el("div", "auto-ampm");
            [["오전", false], ["오후", true]].forEach(([label, isPm]) => {
              const b = pill(label, () => { if (pm !== isPm) put(cronOf({ ...sc, hour: (sc.hour + 12) % 24 })); });
              b.classList.add("toggle"); b.classList.toggle("on", pm === isPm); b.setAttribute("aria-pressed", String(pm === isPm));
              ampm.append(b);
            });
            const num = (v, max, label, fn) => { const i = h.input(String(v).padStart(2, "0"), { maxLength: 2, cls: "auto-num" }); i.inputMode = "numeric"; i.setAttribute("aria-label", label); i.addEventListener("change", () => { const x = Number(i.value); if (Number.isInteger(x) && x >= 0 && x <= max) fn(x); else i.value = String(v).padStart(2, "0"); }); return i; };
            const h12 = sc.hour % 12 || 12;
            const hh = num(h12, 12, "시", (x) => { if (x < 1) return draw(); put(cronOf({ ...sc, hour: (x % 12) + (pm ? 12 : 0) })); });
            const mm = num(sc.min, 59, "분", (x) => put(cronOf({ ...sc, min: x })));
            const time = el("div", "auto-time");
            time.append(hh, el("span", "auto-dim", ":"), mm);
            pick.append(days, ampm, time);
          } else {
            const raw = el("span", "");
            inlineText(raw, () => d.trigger.cron, (v) => { if (!h.parseCron(v)) { notice("언제 칸의 형식을 확인해 주세요. 분 시 일 월 요일 (예: 0 9 * * 1-5)"); return; } put(v); }, { maxLength: 64, cls: "mono" });
            pick.append(raw);
          }
          const label = el("span", "auto-dim auto-sched-next", `${h.cronText(d.trigger.cron)}${d.enabled && n ? ` · 다음 실행 ${h.when(n)}` : ""}`);
          cronWrap.append(say, pick, label);
        };
        paintCron();
        list.append(valueRow("일정", cronWrap, h.switchEl(on("schedule"), "일정 켜기", async (v) => { if (v && trig() === "webhook" && d.enabled && !await ask("웹훅을 끄고 일정으로 바꿀까요? 일정과 웹훅 중 하나만 켤 수 있어요.")) return; setTrigger("schedule", v); draw(); })));
        list.append(valueRow("이벤트", el("span", "auto-dim", "GitHub·메일·폴더에서 일이 생기면 · 준비 중이에요"), (() => { const b = h.switchEl(false, "이벤트 (준비 중)", () => notice("이벤트로 시작하기는 아직 준비 중이에요. 웹훅으로 받을 수 있어요.")); b.classList.add("soon"); return b; })()));
        const hook = el("span", "");
        if (!creating && trig() === "webhook") {
          const cmd = `arkism-crew hook-url ${rec.id}`;
          hook.append("주소와 토큰은 서버에서 이 명령으로 받아요 · ", el("code", "mono", cmd), h.copyButton(cmd));
        } else hook.append(creating ? "만들고 나면 주소가 생겨요" : "이 주소로 요청이 오면 시작해요");
        list.append(valueRow("웹훅", hook, h.switchEl(on("webhook"), "웹훅 켜기", async (v) => { if (v && trig() === "schedule" && d.enabled && !await ask("일정을 끄고 웹훅으로 바꿀까요? 일정과 웹훅 중 하나만 켤 수 있어요.")) return; setTrigger("webhook", v); draw(); })));
        if (trig() === "webhook") {
          const name = el("span", "");
          inlineText(name, () => d.trigger.label || "", (v) => { d.trigger.label = v; touch(); }, { maxLength: 60, placeholder: "예: GitHub PR 열림" });
          list.append(valueRow("웹훅 이름", name, el("span", "")));
        }
        if (!creating && d.trigger?.kind === "webhook") list.append(el("p", "auto-hint", "웹훅 토큰은 Arkism에 저장하지 않아요."));
      }
      draw();
      s.append(list);
      return s;
    }

    // 워크플로우: the mini flow, then each AI step with its goal and the two trial buttons.
    function flowSection() {
      const s = section("tree-structure", "워크플로우", link(creating ? "그래프로 만들기 →" : "그래프로 편집 →", () => {
        if (creating) { notice("먼저 자동화를 만든 뒤 그래프로 이어요."); return; }
        leave(() => h.openGraph(rec, { graphMode: true, fromPopup: true })).catch(console.error);
      }));
      const card = el("div", "auto-box auto-flow");
      const paint = () => {
        card.replaceChildren();
        const g = d.graph;
        if (!creating) {
          const strip = el("div", "auto-strip");
          const lv = h.levels(g);
          lv.forEach((nodes, i) => {
            if (i) strip.append(el("span", "auto-arrow", "→"));
            const grp = el("span", "auto-grp");
            for (const n of nodes) {
              const c = el("span", "auto-node");
              const p = byId(n.type === "expert" ? n.config?.expert : n.config?.persona);
              c.append(IS_AI(n) && p ? h.avatarMark(p.crew) : icon(h.NODES[n.type]?.icon || "play"), h.nodeTitle(n));
              grp.append(c);
            }
            strip.append(grp);
          });
          const back = (g.edges || []).filter(h.isBack);
          const nodes = g.nodes;
          const count = (f) => nodes.filter(f).length;
          const info = [`노드 ${nodes.length}개`, count((n) => n.type === "ai") ? `전문가 ${count((n) => n.type === "ai")}` : "", count((n) => n.type === "ai_once") ? `한 번 묻기 ${count((n) => n.type === "ai_once")}` : "", count((n) => ["code", "http", "github", "notify"].includes(n.type)) ? `처리 ${count((n) => ["code", "http", "github", "notify"].includes(n.type))}` : ""].filter(Boolean).join(" · ");
          card.append(strip);
          const meta = el("div", "auto-meta");
          if (back.length) meta.append(el("span", "auto-back", `↺ 되돌아가는 선 ${back.length}개 (최대 ${Math.max(...back.map((e) => e.max))}번)`));
          meta.append(el("span", "", info));
          card.append(meta);
        }
        const work = flat(g).filter(h.WORK);
        const ais = work.filter(IS_AI);
        if (creating) work.forEach((n, i) => card.append(stepRow(n, i)));
        else if (ais.length) {
          card.append(el("p", "auto-count", `AI 노드 ${ais.length}개 · 노드를 눌러 바로 고치거나, 여기서 따로 시험해요`));
          ais.forEach((n, i) => card.append(stepRow(n, i)));
        }
        if (!work.length) card.append(el("p", "wf-empty", "아직 할 일이 없어요. 그래프에서 노드를 이어 주세요."));
        if (creating) {
          card.append(el("p", "auto-hint", "단계마다 맡길 전문가를 고르고 할 일을 적어요. 위에서 아래로 차례대로 실행되고, 앞 단계 결과가 다음 단계로 넘어가요."), link("+ 단계 추가", () => { addStep(); paint(); touch(); }));
        } else {
          card.append(link("+ 노드 추가 — 그래프에서 이어 붙여요", () => leave(() => h.openGraph(rec, { graphMode: true, fromPopup: true }))));
        }
      };
      const addStep = () => {
        const g = d.graph;
        const last = flat(g).filter(h.WORK).pop();
        const id = `n${crypto.randomUUID().slice(0, 8)}`;
        g.nodes.push({ id, type: "ai_once", title: "", x: 0, y: 0, config: { prompt: "", access: "read" } });
        const toEnd = g.edges.find((e) => e.from === last?.id && g.nodes.find((n) => n.id === e.to)?.type === "end");
        if (toEnd) { toEnd.to = id; } else if (last) g.edges.push({ from: last.id, to: id });
        const end = g.nodes.find((n) => n.type === "end");
        if (end) g.edges.push({ from: id, to: end.id });
        d.graph = h.layoutGraph(g);
        d.graph.nodes.forEach((n) => { n.config ||= {}; });
      };
      function stepRow(n, i) {
        const row = el("div", "auto-step");
        const top = el("div", "auto-step-top");
        top.append(el("span", "mono auto-idx", String(i + 1).padStart(2, "0")));
        const p = byId(n.type === "expert" ? n.config?.expert : n.config?.persona);
        const face = el("span", "auto-face");
        face.append(IS_AI(n) ? (p ? h.avatarMark(p.crew) : h.modelMark(h.modelOf(n, d))) : icon(h.NODES[n.type]?.icon || "play"));
        const who = el("div", "auto-step-who");
        const title = el("strong", "");
        inlineText(title, () => n.title || "", (v) => { n.title = v; touch(); }, { maxLength: 80, placeholder: creating ? `단계 ${i + 1} 이름 · 예: ${["자료 모으기", "핵심 정리", "검토하기", "보고서 쓰기"][i % 4]}` : h.nodeTitle(n) });
        if (!creating && !n.title) title.textContent = h.nodeTitle(n);
        const kind = el("small", "");
        if (creating && IS_AI(n)) {
          const chosen = picked.has(n.id);
          // The 전문가 menu's own experts (crew type "agent"); the old list showed every saved persona instead.
          const cur = n.type === "ai_once" ? "" : n.type === "expert" ? `x:${n.config.expert}` : n.config.persona ? `p:${n.config.persona}` : "agent";
          const pick = h.select([...(chosen ? [] : [["__pick", "맡을 전문가 고르기"]]), ...h.agents().map((x) => [`x:${x.id}`, x.crew.name]), ...(cur.startsWith("p:") ? [[cur, `페르소나 · ${p?.crew.name || "?"}`]] : []), ["agent", "AI 에이전트 (직접 쓰기)"], ["", "AI에게 한 번 묻기 (빠르고 싸요)"]], chosen ? cur : "__pick");
          pick.classList.add("auto-pick");
          if (!chosen) face.classList.add("blank");
          pick.setAttribute("aria-label", "맡을 전문가");
          pick.addEventListener("change", () => {
            picked.add(n.id);
            const key = goalKey(n);
            const text = n.config[key] || "";
            delete n.config.goal; delete n.config.prompt; delete n.config.persona; delete n.config.expert;
            const v = pick.value;
            if (v === "") n.type = "ai_once"; else if (v.startsWith("x:")) { n.type = "expert"; n.config.expert = v.slice(2); } else { n.type = "ai"; if (v.startsWith("p:")) n.config.persona = v.slice(2); }
            n.config[goalKey(n)] = text;
            touch(); paint();
          });
          kind.append(pick);
        } else kind.textContent = [kindText(n), p?.crew.name].filter(Boolean).join(" · ");
        who.append(title, kind);
        top.append(face, who, el("span", "grow"));
        if (IS_AI(n)) {
          const model = h.modelSelect(n.config.model || "", n.type === "expert" && p?.crew.model ? `전문가 모델 · ${p.crew.model}` : `기본 · ${d.model || "자동"}`);
          model.classList.add("auto-model");
          model.setAttribute("aria-label", "모델");
          model.addEventListener("change", () => { if (model.value) n.config.model = model.value; else delete n.config.model; touch(); });
          top.append(creating ? model : el("span", "mono auto-model-text", h.modelOf(n, d)));
        }
        const only = button("이 단계만", "ghost small", "play", () => trial(n.id, false));
        const upto = button("여기까지", "ghost small", "play", () => trial(n.id, true));
        for (const b of [only, upto]) { b.classList.add("auto-try"); }
        const ok = !creating && !h.ownerOf(rec) && !graphDirty() && !dirty();
        only.disabled = upto.disabled = !ok;
        const why = creating ? "만든 뒤에 시험할 수 있어요" : h.ownerOf(rec) ? "에이전트를 통해서만 돌아요" : "저장한 뒤에 시험할 수 있어요";
        only.title = ok ? "앞 단계는 건너뛰고 이 단계에만 값을 넣어 돌려요" : why;
        upto.title = ok ? "처음부터 이 단계까지 돌려요" : why;
        top.append(only, upto);
        row.append(top);
        if (IS_AI(n)) {
          const gk = goalKey(n);
          const wrap = el("div", "auto-goal");
          if (creating) {
            const ta = h.input(n.config[gk] || "", { tag: "textarea", rows: 2, maxLength: 8000, placeholder: "이 단계에서 할 일을 평소 말로 적어요 · 예: 받은 자료에서 핵심 3가지만 뽑아 표로 정리해 줘. 앞 단계 결과는 알아서 넘어와요" });
            ta.addEventListener("input", () => { n.config[gk] = ta.value; touch(); });
            wrap.append(el("small", "", gk === "goal" ? "목표" : "질문"), goalAI(n, ta));
          } else {
            const t = el("span", "auto-goal-text");
            const paintG = () => { t.replaceChildren(el("small", "", gk === "goal" ? "목표 · " : "질문 · "), varText(n.config[gk] || "(비어 있어요)")); };
            paintG();
            t.tabIndex = 0;
            t.title = "눌러서 고치기";
            const edit = () => {
              const ta = h.input(n.config[gk] || "", { tag: "textarea", rows: 3, maxLength: 8000 });
              const edit2 = el("div", "auto-goal-edit");
              edit2.append(goalAI(n, ta));
              let done = false;
              const away = (e) => { if (!edit2.contains(e.target)) end(true); };
              const end = (c) => { if (done) return; done = true; document.removeEventListener("pointerdown", away, true); if (c) { n.config[gk] = ta.value; touch(); } edit2.replaceWith(t); paintG(); };
              // leaving the box ends the edit; the AI bubble and the variable chips are inside it, and a bubble
              // redraw drops focus to the page without the person leaving (so no plain blur here)
              edit2.addEventListener("focusout", (e) => { if (e.relatedTarget && !edit2.contains(e.relatedTarget)) end(true); });
              document.addEventListener("pointerdown", away, true);
              ta.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.stopPropagation(); end(false); } });
              t.replaceWith(edit2);
              ta.focus();
            };
            t.addEventListener("click", edit);
            t.addEventListener("keydown", (e) => { if (e.key === "Enter") edit(); });
            wrap.append(t);
          }
          row.append(wrap);
        }
        return row;
      }
      paint();
      s.append(card);
      return s;
    }

    async function trial(nodeId, upto) {
      const text = await h.askTask(rec, upto ? "여기까지 시험" : "이 단계만 시험");
      if (text === null) return;
      const r = await h.startRun(rec, text, upto ? null : nodeId, null, upto ? nodeId : null);
      if (r) { live.run = r; pop.select("test"); }
    }

    // 진행 알림 + 채널 연결 (993:60224): one section. The rows are where progress goes (saved with the workflow, 저장 bar) and, below them, the
    // bot channels that start this workflow (channel action kind "workflow", same data as the expert and team popups; those save at once:
    // POST /v1/discord/bots/{id}/update with the whole channel list). Bot tokens and connection state live in 설정 › 메신저.
    function notifySection() {
      const s = el("section", "auto-sec");
      const head2 = el("div", "auto-sec-head");
      const t = el("h3", "");
      t.append(icon("chat-text"), "진행 알림");
      let editing = -1; // the channel row open for editing; the others read 메신저 · 채널
      const addChannel = () => { setNotify((n) => { n.channels = [...(n.channels || []), { kind: "telegram", channel: "", when: "events" }]; }); editing = channels().length - 1; paint(); };
      head2.append(t, link("+ 채널 연결", addChannel), el("span", "auto-dim", "진행 상황을 어디로 보낼지 정해요 · 봇은 설정 › 메신저에서 연결해요"));
      const list = el("div", "auto-box");
      const channels = () => d.notify?.channels || [];
      const setNotify = (fn) => { d.notify ||= {}; fn(d.notify); if (!d.notify.channels?.length && !d.notify.moments) delete d.notify; touch(); };
      const st = { bots: creating ? [] : null, add: null, known: {} };
      const botOf = (r) => (r?.discord_bot ? { ...r.discord_bot, id: r.id, revision: r.revision } : r);
      const alive = () => pop?.dialog.isConnected;
      const saveBot = async (b, chans, okText) => {
        try {
          const saved = await request(`/v1/discord/bots/${encodeURIComponent(b.id)}/update`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: b.revision,
            bot: { name: b.name, channels: chans, allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.target ? { target: b.target } : {}), ...(b.guild_id ? { guild_id: b.guild_id } : {}) } }) });
          Object.assign(b, saved?.id ? botOf(saved) : { channels: chans, revision: (b.revision || 0) + 1 });
          notice(okText);
        } catch (e) { notice(h.errorText(e)); }
        if (alive()) paint();
      };
      // The bot channels that call this workflow: 메신저 · 채널 · 시작 방식 (자동 시작 / 부를 때만) · 빼기.
      function botRows() {
        const bots = st.bots || [];
        const mine = bots.flatMap((b) => (b.channels || []).filter((c) => c.action?.kind === "workflow" && c.action.id === rec?.id).map((c) => ({ c, b })));
        const rows = mine.map(({ c, b }) => {
          const r = el("div", "auto-row notify");
          const mode = pill(c.auto_start ? "글이 올라오면 시작" : `$${rec.crew.handle}로 부를 때만`, () => saveBot(b, b.channels.map((x) => (x === c ? { ...x, auto_start: !x.auto_start } : x)), "바꿨어요."));
          mode.title = "눌러서 바꿔요";
          const warn = ["needs_permission", "token_invalid"].includes(b.state);
          r.append(el("span", "auto-key chan", `디스코드 · #${c.name || c.channel_id}`), el("small", "auto-dim", b.name || ""), ...(warn ? [el("span", "wf-badge small warn", "확인 필요")] : []), el("span", "grow"), mode,
            pill("빼기", async () => { if (await ask(`#${c.name || c.channel_id} 연결을 뺄까요?`)) saveBot(b, b.channels.filter((x) => x !== c), "연결을 뺐어요.").catch(() => {}); }));
          return r;
        });
        if (!creating && bots.length) {
          const r = el("div", "auto-row");
          r.append(el("span", "auto-key", "부르기"), el("span", "auto-dim", `채널에서 $${rec.crew.handle}로 이 자동화를 부르게 연결해요`), pill(st.add ? "닫기" : "채널 추가", () => { st.add = st.add ? null : { bot: bots[0].id }; paint(); }));
          rows.push(r);
        }
        if (st.add && bots.length) {
          const f = st.add;
          const b = bots.find((x) => x.id === f.bot) || bots[0];
          const known = st.known[b.id];
          if (known === undefined) {
            st.known[b.id] = null;
            request(`/v1/discord/bots/${encodeURIComponent(b.id)}/channels?${query()}`).then((r) => r.items || [], () => []).then((items) => { st.known[b.id] = items; if (alive()) paint(); });
          }
          const row = el("div", "auto-row");
          const botSel = h.select(bots.map((x) => [x.id, x.name || x.id]), b.id);
          botSel.setAttribute("aria-label", "봇");
          botSel.addEventListener("change", () => { f.bot = botSel.value; paint(); });
          const chSel = h.select([["", known ? "채널 고르기" : "채널 불러오는 중…"], ...(known || []).map((c) => [c.channel_id, `#${c.name}${c.parent ? ` · ${c.parent}` : ""}`])], "");
          chSel.setAttribute("aria-label", "채널");
          row.append(botSel, chSel, el("span", "grow"), pill("연결", async () => {
            const id = chSel.value;
            if (!id) { window.ArkismNeed(chSel, "채널을 골라 주세요."); return; }
            if ((b.channels || []).some((x) => x.channel_id === id) && !await ask("이 채널은 이미 다른 곳에 연결돼 있어요. 이 자동화로 바꿀까요?")) return;
            const ch = { channel_id: id, name: (known || []).find((x) => x.channel_id === id)?.name || "", action: { kind: "workflow", id: rec.id }, auto_start: false };
            st.add = null;
            saveBot(b, [...(b.channels || []).filter((x) => x.channel_id !== id), ch], "채널을 연결했어요.").catch(() => {});
          }));
          rows.push(row);
        }
        return { rows, count: mine.length };
      }
      // 보낼 곳 (one line per channel: 메신저 · 채널 · 빼기) and 알림 시점 (a check list). The per-channel 시작 방식 choice
      // (events / approval / report) said the same as 알림 시점, so it is no longer shown; a saved value is kept as it is.
      function paint() {
        list.replaceChildren();
        const bot = botRows();
        if (!channels().length) {
          const empty = el("div", "auto-row");
          empty.append(el("span", "auto-dim", "아직 연결한 채널이 없어요 · 디스코드·텔레그램·슬랙"), pill("연결하기", addChannel));
          list.append(empty);
        }
        const cur = () => d.notify?.moments || DEFAULT_MOMENTS;
        // What one channel gets (progress.go wants): events → every 알림 시점, approval → only 확인 필요, report → the end with the whole summary.
        const whenText = (c) => (c.when === "approval" ? "사람 확인이 필요할 때만" : c.when === "report" ? "끝나면 보고서 올리기" : MOMENTS.filter(([k]) => cur().includes(k)).map(([, l]) => l).join(" · ") || "보내지 않음");
        const NEXT = { events: "approval", approval: "report", report: "events" };
        channels().forEach((c, i) => {
          const r = el("div", "auto-row notify");
          const kindName = NOTIFY_KINDS.find(([k]) => k === c.kind)?.[1] || c.kind;
          const del = el("button", "icon-btn plain");
          del.type = "button";
          del.setAttribute("aria-label", "보낼 곳 빼기");
          del.append(icon("x"));
          del.addEventListener("click", () => { setNotify((n) => { n.channels = channels().filter((_, j) => j !== i); if (!n.channels.length) delete n.channels; }); editing = -1; paint(); });
          const when = pill(whenText(c), () => { c.when = NEXT[c.when] || "approval"; touch(); paint(); });
          when.title = "눌러서 바꿔요 · 알림 시점마다 / 확인 필요할 때만 / 끝나면 보고서";
          const note = c.kind === "discord" ? [el("small", "auto-dim", "디스코드 알림은 준비 중")] : [];
          if (editing !== i) {
            const label = el("button", "auto-chan", `${kindName} · ${c.channel ? `#${c.channel.replace(/^#/, "")}` : (c.kind === "telegram" ? "나에게 (기본)" : "기본 채널")}`);
            label.type = "button";
            label.title = "눌러서 고치기";
            label.addEventListener("click", () => { editing = i; paint(); });
            r.append(label, el("span", "grow"), ...note, when, del);
          } else {
            const kind = h.select(NOTIFY_KINDS, c.kind);
            kind.setAttribute("aria-label", "메신저");
            kind.addEventListener("change", () => { c.kind = kind.value; const m = (st.dests || []).find((x) => x.channel === c.kind); c.channel = m && m.name !== "default" ? m.name : ""; touch(); paint(); }); // the old kind's place is not valid here: take the first one the picker shows
            // 보낼 곳 = a destination already set up in 설정 › 메신저 (GET /v1/messengers), picked, never typed.
            if (st.dests === undefined) { st.dests = null; request(`/v1/messengers?${query()}`).then((x) => x.destinations || [], () => []).then((x) => { st.dests = x; if (alive()) paint(); }); }
            const mine = (st.dests || []).filter((x) => x.channel === c.kind);
            const destName = (x) => (x.name === "default" ? (c.kind === "telegram" ? "나에게 (기본)" : "기본 채널") : `#${x.name}`);
            const opts = st.dests === null ? [["", "불러오는 중…"]] : mine.length ? mine.map((x) => [x.name === "default" ? "" : x.name, destName(x)]) : [["", "설정 › 메신저에서 먼저 연결해요"]];
            const ch = h.select(opts, opts.some(([v]) => v === (c.channel || "").replace(/^#/, "")) ? (c.channel || "").replace(/^#/, "") : opts[0][0]);
            ch.setAttribute("aria-label", "채널");
            ch.disabled = !mine.length;
            ch.addEventListener("change", () => { c.channel = ch.value; touch(); editing = -1; paint(); });
            r.append(kind, ch, ...(st.dests && !mine.length ? [link("설정 열기", () => leave(() => window.dispatchEvent(new CustomEvent("arkism:open-settings", { detail: { tab: "messenger" } }))))] : []), el("span", "grow"), ...note, when, del);
          }
          list.append(r);
        });
        const mo = el("div", "auto-row");
        const checks = el("span", "auto-chips auto-moments");
        for (const [k, label] of MOMENTS) {
          const on = cur().includes(k);
          const b = pill(`${on ? "✓ " : ""}${label}`, () => { const next = MOMENTS.map(([m]) => m).filter((m) => (m === k ? !on : cur().includes(m))); setNotify((n) => { n.moments = next; if (same(next, DEFAULT_MOMENTS)) delete n.moments; }); paint(); });
          b.classList.add("toggle");
          b.classList.toggle("on", on);
          b.setAttribute("aria-pressed", String(on));
          checks.append(b);
        }
        mo.append(el("span", "auto-key", "알림 시점"), checks);
        list.append(mo);
        if (bot.rows.length) {
          list.append(el("div", "auto-subhead", "채널에서 부르기 · 봇은 설정 › 메신저에서 연결해요"));
          list.append(...bot.rows);
        }
      }
      if (!creating) request(`/v1/discord?${query()}`).then((r) => { st.bots = (r.bots || []).map(botOf); }, () => { st.bots = []; }).then(() => { if (alive()) paint(); });
      paint();
      s.append(head2, list);
      return s;
    }

    // 실행 설정: 모델, 멈추는 조건.
    function settingSection() {
      const s = section("sliders-horizontal", "실행 설정");
      const list = el("div", "auto-box");
      const draw = () => {
        list.replaceChildren();
        const backs = (d.graph.edges || []).filter(h.isBack);
        const stops = [d.limits?.max_minutes ? `최대 ${d.limits.max_minutes}분` : "", d.limits?.max_steps ? `단계 ${d.limits.max_steps}번` : "", backs.length ? `되돌아가는 선은 ${Math.max(...backs.map((e) => e.max))}번까지` : ""].filter(Boolean).join(" · ") || "정하지 않음";
        const editRow = (label, valueEl, mk) => {
          const r = valueRow(label, valueEl, pill("바꾸기", () => {
            const form = mk(() => draw());
            r.replaceChildren(el("span", "auto-key", label), form);
          }));
          return r;
        };
        const modelText = el("span", "", `${d.model || "단계마다 정해요"} · ${d.model ? "모델을 비워 둔 AI 단계가 이걸 써요" : "단계마다 바꿀 수 있어요"}`);
        list.append(editRow("모델", modelText, () => {
          const model = h.modelSelect(d.model || "", "단계마다 정해요");
          model.setAttribute("aria-label", "기본 모델");
          model.addEventListener("change", () => { d.model = model.value; touch(); draw(); });
          setTimeout(() => model.focus());
          const wrap = el("span", "auto-model-row");
          wrap.append(model, pill("취소", () => draw()));
          return wrap;
        }), editRow("멈추는 조건", el("span", "", stops), (done) => {
          const wrap = el("span", "auto-limits");
          const mins = h.input(d.limits?.max_minutes || "", { type: "number", min: 1, max: 1440, placeholder: "30" });
          const steps = h.input(d.limits?.max_steps || "", { type: "number", min: 1, max: 500, placeholder: "60" });
          const commit = () => {
            const l = {};
            if (Number(mins.value) > 0) l.max_minutes = Math.min(1440, Math.round(Number(mins.value)));
            if (Number(steps.value) > 0) l.max_steps = Math.min(500, Math.round(Number(steps.value)));
            if (Object.keys(l).length) d.limits = l; else delete d.limits;
            touch(); done();
          };
          wrap.append("최대 시간(분)", mins, "최대 단계 수", steps, button("확인", "small", null, commit));
          return wrap;
        }));
      };
      draw();
      s.append(list);
      return s;
    }

    // 관리 (993:60224): one row each, label · what it does · its button. 지우기 and 예전 설정 창 are not in Figma but keep their jobs.
    function manageSection() {
      const s = section("gear-six", "관리");
      // One row: the four used to take a row each, and 예전 설정 창 repeats what this window already edits.
      const acts = [
        ["복제", "같은 단계로 새 자동화를 만들어요", () => leave(() => h.duplicate(rec))],
        ["보관", `목록에서 숨기고 $${base.handle} 부르기를 멈춰요`, async () => { if (dirty() && !await ask("저장하지 않은 변경은 사라져요. 계속할까요?")) return; await h.archive(rec); if (!h.byId(rec.id) || h.byId(rec.id).archived) leaveSilently(); }],
        ["지우기", "정의를 영구히 지워요 · 지난 실행 기록은 남아요", async () => { if (await h.remove(rec)) leaveSilently(); }],
        ["예전 설정 창", "템플릿·실행 방식·웹훅을 예전 설정 창에서 바꿔요", () => leave(() => h.openSettings(rec))]
      ];
      s.append(box(...acts.map(([label, sub, fn]) => valueRow(label, el("span", "", sub), pill(label === "예전 설정 창" ? "열기" : label, fn)))));
      return s;
    }

    async function doSave() {
      const out = structuredClone(d);
      out.type = "workflow";
      out.name = (out.name || "").trim();
      out.handle = (out.handle || "").trim();
      if (!out.name) { window.ArkismNeed(pop.dialog.querySelector(".auto-namerow > h2, .auto-namerow > .auto-inline-edit"), "자동화 이름을 적어 주세요."); return; }
      if (!out.handle) out.handle = h.freeHandle(out.name);
      if (!out.trigger) { out.enabled = false; delete out.trigger; }
      if (creating) {
        for (const n of out.graph.nodes) {
          if (!h.WORK(n)) continue;
          n.title ||= out.name;
          if (IS_AI(n) && !n.config[goalKey(n)]) n.config[goalKey(n)] = "{{input}}";
        }
        if (out.goal && !Object.keys(out.goal).length) delete out.goal;
      }
      for (const k of ["goal", "notify", "cover", "limits"]) if (out[k] === undefined) delete out[k];
      const b = bar.querySelector(".primary");
      b.disabled = true;
      try {
        const saved = await h.save(rec, out);
        leaveSilently();
        await h.reload(saved.id);
        notice(creating ? "만들었어요. 테스트 탭에서 시험해 보세요." : "저장했어요.");
        h.openAutomation(saved.id);
      } catch (x) {
        notice(h.errorText(x));
        b.disabled = false;
      }
    }

    // -------------------------------------------------------------- 테스트
    function testTab(body) {
      body.classList.remove("auto-body");
      body.classList.add("auto-test");
      const owned = h.ownerOf(rec);
      body.append(el("p", "auto-hint", "여기서 돌린 실행도 실제 실행이에요. 기록과 이번 달 한도에 남아요."));
      const stage = el("div", "auto-chat");
      body.append(stage);
      const row = el("div", "auto-ask");
      const q = h.input("", { maxLength: 4000, placeholder: `$${base.handle}에게 맡길 주제…` });
      const go = button("실행", "primary", null, async () => {
        go.disabled = true;
        const r = await h.startRun(rec, q.value.trim());
        go.disabled = false;
        if (r) { live.run = r; q.value = ""; paintRun(); void poll(); }
      });
      go.disabled = !!owned;
      q.disabled = !!owned;
      if (owned) q.placeholder = "에이전트를 통해서만 돌아요";
      q.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) go.click(); });
      row.append(q, go, link("실행 기록 보기", () => pop.select("history")));
      body.append(row);
      const paintRun = () => {
        stage.replaceChildren();
        const run = live.run;
        if (!run) { stage.append(el("p", "wf-empty", creating ? "" : "맡길 주제를 적고 실행하면 단계별 진행과 중간 결과가 여기에 보여요.")); return; }
        stage.append(runView(run));
      };
      const poll = async () => {
        stopLive();
        if (!live.run || !pop.dialog.isConnected) return;
        try {
          const r = await request(`/v1/crew/runs/${encodeURIComponent(live.run.id)}?${query()}`);
          if (r.id === live.run?.id) live.run = r;
        } catch (_) { /* a short outage: try again */ }
        if (!pop.dialog.isConnected) return;
        paintRun();
        const st = live.run?.crew_run?.status;
        if (st === "running" || st === "waiting") live.timer = setTimeout(poll, 2500);
      };
      paintRun();
      void poll();
    }

    function runView(run) {
      const cr = run.crew_run || {};
      const g = rec.crew.graph;
      const wrap = el("div", "auto-run");
      wrap.append(el("div", "auto-bubble", h.runRequest(cr) || "(맡긴 주제 없음)"));
      const work = flat(g).filter(h.WORK);
      const nodes = cr.nodes || {};
      const doneN = work.filter((n) => nodes[n.id]?.status === "done").length;
      const who = el("div", "auto-run-who");
      const face = el("span", "auto-face");
      face.append(h.modelMark(rec.crew.model));
      const runName = el("strong", "", rec.crew.name); runName.setAttribute("data-no-i18n", "");
      who.append(face, runName, el("small", "", `${work.length}단계 중 ${doneN}단계 · ${h.RUN_LABEL[cr.status] || ""}`));
      wrap.append(who);
      const steps = el("div", "auto-run-steps");
      const mark = { done: "✓", running: "●", waiting: "●", failed: "✕", skipped: "–", pending: "○" };
      for (const n of work) {
        const st = nodes[n.id] || { status: "pending" };
        const r = el("div", `auto-run-step st-${st.status}`);
        const detail = st.status === "done" ? [h.nodeDur(st), st.runs > 1 ? `${st.runs}회째` : ""].filter(Boolean).join(" · ") : st.status === "running" ? "하는 중…" : st.status === "waiting" ? "확인 대기" : st.status === "failed" ? (st.error || "실패").slice(0, 40) : st.status === "skipped" ? "건너뜀" : "대기";
        r.append(el("span", "auto-run-mark", mark[st.status] || "○"), el("span", "", h.nodeTitle(n)), el("small", "", detail));
        steps.append(r);
      }
      wrap.append(steps);
      if (cr.status === "waiting" && cr.waiting?.kind === "approval") wrap.append(approvalCard(run));
      const text = h.readable(cr.summary);
      wrap.append(text ? el("pre", "auto-run-out", text) : el("p", "auto-hint", cr.status === "running" ? "중간 결과가 생기면 여기에 보여요." : ""));
      const foot = el("div", "auto-run-foot");
      const tokens = Object.values(nodes).reduce((n, x) => n + (x.tokens || 0), 0);
      foot.append(el("small", "auto-dim", [tokens ? `${tokens.toLocaleString()} 토큰` : "", h.runTookText(run), h.ago(run.created_at)].filter(Boolean).join(" · ")), el("span", "grow"));
      foot.append(link("그래프로 보기", () => leave(() => h.openGraph(rec, { run }))));
      if (cr.status === "running" || cr.status === "waiting") foot.append(button("멈추기", "ghost small", null, async () => { await h.stopRun(run); live.run = (await request(`/v1/crew/runs/${encodeURIComponent(run.id)}?${query()}`).catch(() => run)); pop.select("test"); }));
      wrap.append(foot);
      return wrap;
    }

    // -------------------------------------------------------------- 기록: the runs of this workflow
    function historyTab(body) {
      body.classList.remove("auto-body");
      body.classList.add("auto-history");
      // 판 이력 from the server (GET /v1/crew/versions/{id}, same as the team popup): the live definition first, then each one a save replaced.
      const ver = el("section", "auto-sec auto-versions");
      const vh = el("div", "wf-sec-head");
      vh.append(el("strong", "", "버전 기록"), el("span", "grow"), el("small", "auto-dim", `저장할 때마다 버전이 하나 생겨요. 다른 버전으로 바꿔도 지금 버전은 남아요.`));
      const vbox = el("div", "tm-hist");
      ver.append(vh, vbox);
      const pad2 = (n) => String(n).padStart(2, "0");
      const what = (a, b) => diffLines(a, b);
      const open = new Set();
      let items = null;
      const paintVersions = () => {
        vbox.replaceChildren();
        (items || []).forEach((x, i) => {
          const cur = x.revision === rec.revision, at = new Date(x.at);
          const r = el("div", "tm-hist-row");
          const sum = items[i + 1] ? what(items[i + 1].crew, x.crew).slice(0, 2).join(" · ") || "저장" : x.revision === 1 ? "처음 만듦" : "처음 기록";
          r.append(el("b", "", `v${x.revision}`), el("time", "", isNaN(at) ? "" : `${pad2(at.getMonth() + 1)}-${pad2(at.getDate())} ${pad2(at.getHours())}:${pad2(at.getMinutes())}`), el("span", "", sum));
          const acts = el("span", "tm-hist-acts");
          if (cur) acts.append(el("em", "", "지금 버전"));
          else {
            const lk = (label, cls, fn) => { const b = el("button", `tm-link ${cls}`, label); b.type = "button"; b.addEventListener("click", fn); return b; };
            acts.append(lk("바뀐 점 보기", "quiet", () => { if (!open.delete(x.revision)) open.add(x.revision); paintVersions(); }), lk("이 버전으로 변경", "plain", async () => {
              if (!await ask(`v${x.revision} 버전으로 변경할까요? 새 버전으로 저장돼요.`)) return;
              try {
                const saved = await h.save(rec, { ...structuredClone(x.crew), type: "workflow" });
                leaveSilently();
                await h.reload(saved.id);
                notice(`v${x.revision}로 되돌렸어요.`);
                h.openAutomation(saved.id, "history");
              } catch (e) { notice(h.errorText(e)); }
            }));
          }
          r.append(acts);
          vbox.append(r);
          if (open.has(x.revision)) vbox.append(window.ArkismVersionDiff.render(el, items[0].crew, x.crew, `v${x.revision} 버전으로 바꾸면`)); // now (-) against that version (+)
        });
        if (!items?.length) vbox.append(el("p", "auto-hint auto-empty", items ? "버전 기록을 불러오지 못했어요." : "불러오는 중…"));
      };
      paintVersions();
      request(`/v1/crew/versions/${encodeURIComponent(rec.id)}?${query()}`).then((r) => { items = r.items || []; }).catch(() => { items = []; }).then(() => { if (pop?.dialog.isConnected) paintVersions(); });
      body.append(ver);
      const sec = el("section", "auto-sec");
      const head2 = el("div", "wf-sec-head");
      const pick = el("select", "wf-select small");
      pick.setAttribute("aria-label", "실행 상태");
      for (const [k, label] of [["all", "모든 상태"], ["failed", "실패"], ["waiting", "확인 대기"], ["running", "실행 중"], ["succeeded", "완료"], ["stopped", "멈춤"]]) { const o = el("option", "", label); o.value = k; pick.append(o); }
      const count = el("small", "", "불러오는 중…");
      const title = el("strong", "", "최근 실행");
      head2.append(title, count, el("span", "grow"), pick);
      head2.append(el("small", "auto-dim", "테스트 탭에서 돌린 것도 여기 남아요"));
      const list = el("div", "wf-runs");
      sec.append(head2, list);
      body.append(sec);
      // One run as one line (998:60693): ✓ done · ! stopped or failed · ● live, time, how it started, what was asked, then how it went.
      const MARK = { succeeded: "✓", failed: "!", stopped: "!", waiting: "●", running: "" };
      const runLine = (r) => {
        const cr = r.crew_run || {};
        const at = new Date(r.created_at);
        const row = el("button", `auto-runline st-${cr.status || ""}`);
        row.type = "button";
        const tokens = Object.values(cr.nodes || {}).reduce((n, x) => n + (x.tokens || 0), 0);
        const bad = ["failed", "stopped"].includes(cr.status);
        const err = bad ? (Object.values(cr.nodes || {}).find((n) => n.status === "failed")?.error || h.readable(cr.summary).split(/[.。]/)[0]).slice(0, 40) : "";
        const end = bad ? [h.RUN_LABEL[cr.status], err].filter(Boolean).join(" · ") : cr.status === "running" || cr.status === "waiting" ? h.RUN_LABEL[cr.status] : [h.runTookText(r), tokens ? `${fmtTokens(tokens)} 토큰` : ""].filter(Boolean).join(" · ");
        row.append(el("span", "auto-runline-mark", MARK[cr.status] ?? "○"), el("time", "", isNaN(at) ? "" : `${pad2(at.getMonth() + 1)}-${pad2(at.getDate())} ${pad2(at.getHours())}:${pad2(at.getMinutes())}`), el("small", "", TRIGGERS[cr.trigger] || ""), el("span", "auto-runline-title", h.runRequest(cr) || h.readable(cr.summary) || rec.crew.name), el("small", "auto-runline-end", end));
        row.addEventListener("click", () => leave(() => h.openGraph(rec, { run: r })));
        return row;
      };
      let all = false;
      const load = async () => {
        count.textContent = "불러오는 중…";
        let items = [];
        try { items = (await request(`/v1/crew/runs?${query({ crew_id: rec.id, ...(pick.value !== "all" ? { status: pick.value } : {}), limit: 30 })}`)).items || []; } catch (_) { /* empty */ }
        if (!pop.dialog.isConnected) return;
        count.textContent = `${items.length}건`;
        list.replaceChildren(...(all ? items : items.slice(0, 10)).map(runLine));
        if (items.length > 10 && !all) list.append(button("더 보기", "ghost small", null, () => { all = true; void load(); }));
        if (!items.length) list.append(el("p", "wf-empty", pick.value !== "all" ? "이 상태인 실행이 없어요." : "이 자동화는 아직 실행한 적이 없어요."));
      };
      pick.addEventListener("change", () => { all = false; void load(); });
      void load();
    }

    // -------------------------------------------------------------- open
    const tabs = creating ? [{ key: "overview", label: "개요", render: overview }] : [
      { key: "overview", label: "개요", render: (b) => { b.className = "dialog-body crew-popup-body"; overview(b); } },
      { key: "test", label: "테스트", render: (b) => { b.className = "dialog-body crew-popup-body"; testTab(b); } },
      { key: "history", label: "기록", render: (b) => { b.className = "dialog-body crew-popup-body"; historyTab(b); } }
    ];
    pop = popup.open({ title: d.name, sub: "", tabs, initial: startTab, cls: `v4-auto${creating ? " v4-auto-new" : ""}`, popout: creating ? null : { event: "arkism:open-automation", detail: { id: rec.id } }, onClose: () => { stopLive(); document.querySelector(".auto-menu")?.remove(); state.selected = null; h.afterClose(); } });
    // closing with unsaved changes asks first (Esc and 닫기); saving closes with dialog.close(), which skips this
    // then: what to open once it is closed (템플릿·설정으로 만들기)
    const guard = (e, then) => {
      if (!(creating ? !same(d, blank) : dirty())) return;
      e?.preventDefault(); e?.stopImmediatePropagation();
      void ask(creating ? "아직 만들지 않았어요. 버리고 닫을까요?" : "저장하지 않은 변경이 있어요. 버리고 닫을까요?").then((yes) => { if (yes) { leaveSilently(); then?.(); } });
    };
    closeAsking = (e, then) => { guard(e, then); if (!e?.defaultPrevented) { leaveSilently(); then?.(); } };
    pop.dialog.addEventListener("cancel", guard);
    pop.dialog.addEventListener("click", (e) => { if (e.target.closest(".crew-popup-close")) guard(e); }, true);
    head(pop.dialog);
    void loadStats();
    return pop;
  }

  return { open: (rec, tab) => open(rec, tab), create: (ai) => open(null, "overview", !!ai), diffLines, whenToCron };
};
