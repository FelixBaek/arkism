// 크루 › 자동화 상세·새로 만들기 팝업 (Figma 993:60224 · 998:59605 · 998:60693 · 1044:65953).
// One window on the shared frame (crew-popup.js): 개요 (목표 · 언제 실행 · 워크플로우 · 진행 알림 · 실행 설정 · 관리), 테스트, 기록.
// Every field edits a draft; the 저장/취소 bar shows only while something differs. crew.js passes its helpers in once.
window.ArkismAutomation = (h) => {
  const { state, el, icon, button, notice, request, query, byId, kit } = h;
  const popup = window.ArkismCrewPopup({ el, button });
  const NOTIFY_KINDS = [["discord", "디스코드"], ["telegram", "텔레그램"], ["slack", "슬랙"]];
  const NOTIFY_WHEN = [["events", "시작 · 끝 · 실패"], ["approval", "사람 확인이 필요할 때만"], ["report", "끝나면 보고서 올리기"]];
  const MOMENTS = [["start", "시작"], ["step", "단계마다"], ["approval", "확인 필요"], ["fail", "실패"], ["end", "끝"]];
  const DEFAULT_MOMENTS = ["start", "approval", "fail", "end"];
  const DEFAULT_CRON = "0 9 * * 1-5";
  const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
  const flat = (g) => h.levels(g).flat();
  const goalKey = (n) => (n.type === "ai_once" ? "prompt" : "goal");
  const kindText = (n) => (n.type === "ai_once" ? "한 번 호출" : n.config?.collab === "on" ? `전문가 · 워커 최대 ${n.config.max_workers || 5}` : IS_AI(n) ? "전문가" : h.NODES[n.type]?.label || n.type);
  const IS_AI = (n) => h.IS_AI(n.type);
  const fmtTokens = (n) => (n >= 1000 ? `${Math.round(n / 1000)}k` : String(Math.round(n)));
  const TRIGGERS = { manual: "직접", schedule: "일정", webhook: "웹훅", call: "$이름", discord: "디스코드", agent: "에이전트", test: "테스트" };

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
  function open(rec, startTab = "overview") {
    const creating = !rec;
    const base = creating ? { type: "workflow", name: "", handle: "", role: "", category: "", model: "", enabled: false, graph: h.defaultGraph("simple", {}) } : rec.crew;
    const d = structuredClone(base);
    d.graph.nodes.forEach((n) => { n.config ||= {}; });
    if (creating) { const s = d.graph.nodes.find((n) => n.id === "ai1"); s.title = ""; s.config.goal = ""; }
    let handleTouched = !creating;
    let aiOpen = true;
    let pop = null;
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
      bar.hidden = !creating && !n;
      bar.querySelector(".auto-bar-note").textContent = creating ? `단계 ${h.stepCount(d.graph)}개 · 만든 뒤 테스트 탭에서 전체를 시험해요` : `바뀐 칸 ${n}개 · 저장하면 지금 설정으로 바뀌어요`;
      bar.querySelector(".primary").disabled = creating && !d.name.trim();
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
      cat.addEventListener("click", () => catMenu(cat));
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
        cat.replaceChildren(d.category || "카테고리", el("small", "", " ▾"));
        name.textContent = d.name || "자동화 이름을 적어 주세요";
        name.classList.toggle("empty", !d.name);
        hv.textContent = d.handle ? `$${d.handle}` : "$이름";
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

    function catMenu(anchor) {
      document.querySelector(".auto-menu")?.remove();
      const m = el("div", "auto-menu");
      const close = () => { m.remove(); document.removeEventListener("click", away, true); };
      const away = (e) => { if (!m.contains(e.target) && e.target !== anchor) close(); };
      const set = (v) => { d.category = v; touch(); close(); };
      for (const [c] of h.categories(h.workflows())) { const b = el("button", c === d.category ? "on" : "", c); b.type = "button"; b.addEventListener("click", () => set(c)); m.append(b); }
      if (d.category) { const b = el("button", "", "카테고리 없음"); b.type = "button"; b.addEventListener("click", () => set("")); m.append(b); }
      const add = h.input("", { maxLength: 40, placeholder: "+ 새 카테고리" });
      add.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing && add.value.trim()) { e.preventDefault(); set(add.value.trim()); } });
      m.append(add);
      anchor.parentElement.append(m);
      add.focus();
      setTimeout(() => document.addEventListener("click", away, true));
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
          card.append(t, button("구조 검토", "", null, () => leave(() => h.openGraph(rec, { review: true }))));
          top.append(card);
        }
      }
      body.append(goalSection(), aiSection(), whenSection(), flowSection(), notifySection(), channelSection(), settingSection());
      if (!creating) body.append(manageSection());
      bar = el("div", "auto-bar");
      const save = button(creating ? "자동화 만들기" : "저장", "primary", null, doSave);
      const cancel = button("취소", "ghost", null, () => { if (creating) pop.dialog.close(); else { reset(); } });
      bar.append(save, cancel);
      if (creating) bar.append(button("템플릿·설정으로 만들기", "ghost", null, () => { pop.dialog.close(); h.openSettingsNew(); }));
      bar.append(el("span", "auto-bar-note"));
      body.append(bar);
      updateBar();
    }
    // Back to what was saved: close and open again so every row redraws.
    function reset() { const t = pop; leaveSilently(); h.openAutomation(rec.id); void t; }
    function leaveSilently() { pop.dialog.close(); }
    const leave = (fn) => {
      if (dirty() && !confirm("저장하지 않은 변경은 사라져요. 계속할까요?")) return;
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
          val.textContent = !s ? "…" : k === "runs" ? `${s.runs}회` : k === "done" ? (s.runs ? `${s.ok} / ${s.runs}` : "—") : s.cost != null ? `$${s.cost.toFixed(2)}` : "—";
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
      try { const w = (await request(`/v1/crew/usage?${query({ range: "30d", kind: "workflow" })}`)).workflows?.find((x) => x.id === rec.id); cost = w?.cost_usd ?? null; tokens = w?.tokens || 0; } catch (_) { /* no usage yet */ }
      const now = Date.now();
      const recentRuns = runs.filter((r) => now - new Date(r.created_at).getTime() < 30 * 864e5);
      const bin = (list) => { const a = Array(15).fill(0); for (const r of list) a[14 - Math.min(14, Math.floor((now - new Date(r.created_at).getTime()) / (2 * 864e5)))]++; const m = Math.max(1, ...a); return a.map((n) => n / m); };
      const done = recentRuns.filter((r) => r.crew_run?.status === "succeeded");
      const spans = done.map((r) => h.runMs(r)).filter(Boolean);
      stats = { runs: recentRuns.length, ok: done.length, cost, tokens, avgMs: spans.length ? spans.reduce((a, b) => a + b, 0) / spans.length : 0, bins: { runs: bin(recentRuns), done: bin(done) } };
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

    // 목표: three lines of what it is for. Saved with the workflow (crew.goal).
    function goalSection() {
      const s = section("check", "목표");
      const g = () => (d.goal ||= {});
      const row = (k, label, hint, btn) => {
        const v = el("span", "");
        inlineText(v, () => d.goal?.[k] || "", (val) => { if (val) g()[k] = val; else if (d.goal) { delete d.goal[k]; if (!Object.keys(d.goal).length) delete d.goal; } touch(); }, { maxLength: 300, placeholder: hint });
        return valueRow(label, v, pill(d.goal?.[k] ? "바꾸기" : btn, () => v.click()));
      };
      s.append(box(row("task", "하려는 일", creating ? "무엇을 하는 자동화인지 한 줄로 적어요" : "한 줄로 적어요", "적기"), row("done", "끝난 기준", "언제 끝났다고 볼지 · 예: 출처 3개 이상", "적기"), row("output", "결과물", "무엇을 남길지 · 예: 보고서, 메시지, 파일", "적기")));
      return s;
    }

    // AI로 고치기 / 초안 만들기: the assist runner rewrites the graph (and in a new one the name); the user reviews and saves.
    function aiSection() {
      const s = el("section", "auto-sec");
      const head2 = el("button", "auto-ai-head");
      head2.type = "button";
      const draw = () => {
        head2.replaceChildren(icon("sparkle"), creating ? "AI로 초안 만들기" : "AI로 고치기", icon("caret-down"));
        head2.classList.toggle("open", aiOpen);
        form.hidden = !aiOpen;
      };
      head2.addEventListener("click", () => { aiOpen = !aiOpen; draw(); });
      const form = el("form", "auto-ai");
      const q = h.input("", { maxLength: 2000, placeholder: creating ? "어떤 일을 자동으로 할까요? 예: 매일 아침 열린 PR을 모아 요약해서 슬랙으로 보내 줘" : "무엇을 고칠까요? 예: 출처 확인이 실패하면 2번까지 다시 찾게 해 줘" });
      const go = button(creating ? "초안 만들기" : "고치기", "primary");
      go.type = "submit";
      form.append(q, go);
      const err = el("p", "form-error");
      err.setAttribute("role", "alert");
      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        const text = q.value.trim();
        if (!text) return;
        go.disabled = true;
        err.textContent = "";
        go.textContent = "AI가 생각하는 중…";
        try {
          const cur = JSON.stringify({ nodes: d.graph.nodes.map(({ id, type, title, config }) => ({ id, type, title, config })), edges: d.graph.edges }).slice(0, 12000);
          const prompt = creating
            ? `Build an Arkism workflow from this request. Decide sensibly without asking.\nUser: ${text}`
            : `Change this Arkism workflow as the user asks. Keep every node id, type, title and setting the request does not touch. Answer with the whole changed workflow as {"graph":{"nodes":[],"edges":[]}} (plus "name" and "role" only if they should change).\nWorkflow: ${cur}\nRequest: ${text}`;
          const a = await kit.assist("build", prompt, { purpose: "workflow" });
          if (a.status !== "done") throw new Error(kit.assistError(a));
          const j = kit.parseJSON(a.output);
          const g = j?.graph;
          if (!Array.isArray(g?.nodes) || !g.nodes.length) throw new Error(j?.ask ? String(j.ask).slice(0, 300) : "AI 답을 읽지 못했어요. 다시 보내 주세요.");
          g.edges = Array.isArray(g.edges) ? g.edges : [];
          d.graph = h.layoutGraph(g);
          d.graph.nodes.forEach((n) => { n.config ||= {}; });
          if (creating) {
            if (!d.name && j.name) { d.name = String(j.name).slice(0, 60); if (!handleTouched) d.handle = h.freeHandle(d.name); }
            if (!d.role && j.role) d.role = String(j.role).slice(0, 200);
          }
          q.value = "";
          notice(creating ? "AI가 단계를 그렸어요. 확인하고 만들어 주세요." : "AI가 고쳤어요. 확인하고 저장해 주세요.");
          pop.select("overview");
          touch();
        } catch (x) {
          err.textContent = x.message;
        } finally {
          go.disabled = false;
          go.textContent = creating ? "초안 만들기" : "고치기";
        }
      });
      s.append(head2, form, err);
      draw();
      return s;
    }

    // 언제 실행: 직접 부르기 is always on; 일정 and 웹훅 share the workflow's one trigger.
    function whenSection() {
      const s = section("clock-counter-clockwise", "언제 실행", link("+ 시작 조건 추가", () => { if (d.trigger?.kind !== "schedule" || !d.enabled) setTrigger("schedule", true); draw(); }));
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
          const text = el("span", "");
          const n = h.nextRun(d.trigger.cron);
          inlineText(text, () => d.trigger.cron, (v) => { if (!h.parseCron(v)) { notice("언제 칸의 형식을 확인해 주세요. 분 시 일 월 요일 (예: 0 9 * * 1-5)"); return; } d.trigger.cron = v; touch(); draw(); }, { maxLength: 64, cls: "mono" });
          const label = el("span", "auto-dim", `${h.cronText(d.trigger.cron)}${d.enabled && n ? ` · 다음 실행 ${h.when(n)}` : ""} · `);
          cronWrap.append(label, text);
        };
        paintCron();
        list.append(valueRow("일정", cronWrap, h.switchEl(on("schedule"), "일정 켜기", (v) => { if (v && trig() === "webhook" && d.enabled && !confirm("웹훅을 끄고 일정으로 바꿀까요? 일정과 웹훅 중 하나만 켤 수 있어요.")) return; setTrigger("schedule", v); draw(); })));
        list.append(valueRow("이벤트", el("span", "auto-dim", "GitHub·메일·폴더에서 일이 생기면 · 준비 중이에요"), (() => { const b = h.switchEl(false, "이벤트 (준비 중)", () => notice("이벤트로 시작하기는 아직 준비 중이에요. 웹훅으로 받을 수 있어요.")); b.classList.add("soon"); return b; })()));
        const hook = el("span", "");
        if (!creating && trig() === "webhook") {
          const cmd = `arkism-crew hook-url ${rec.id}`;
          hook.append("주소와 토큰은 서버에서 이 명령으로 받아요 · ", el("code", "mono", cmd), h.copyButton(cmd));
        } else hook.append(creating ? "만들고 나면 주소가 생겨요" : "이 주소로 요청이 오면 시작해요");
        list.append(valueRow("웹훅", hook, h.switchEl(on("webhook"), "웹훅 켜기", (v) => { if (v && trig() === "schedule" && d.enabled && !confirm("일정을 끄고 웹훅으로 바꿀까요? 일정과 웹훅 중 하나만 켤 수 있어요.")) return; setTrigger("webhook", v); draw(); })));
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
        leave(() => h.openGraph(rec, { graphMode: true }));
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
              const p = byId(n.config?.persona);
              c.append(IS_AI(n) && p ? h.avatarMark(p.crew) : icon(h.NODES[n.type]?.icon || "play"), h.nodeTitle(n));
              grp.append(c);
            }
            strip.append(grp);
          });
          const back = (g.edges || []).filter(h.isBack);
          const nodes = g.nodes;
          const count = (f) => nodes.filter(f).length;
          const info = [`노드 ${nodes.length}개`, count((n) => n.type === "ai") ? `전문가 ${count((n) => n.type === "ai")}` : "", count((n) => n.type === "ai_once") ? `한 번 호출 ${count((n) => n.type === "ai_once")}` : "", count((n) => ["code", "http", "github", "notify"].includes(n.type)) ? `처리 ${count((n) => ["code", "http", "github", "notify"].includes(n.type))}` : ""].filter(Boolean).join(" · ");
          card.append(strip);
          const meta = el("div", "auto-meta");
          if (back.length) meta.append(el("span", "auto-back", `↺ 되돌아가는 선 ${back.length}개 (최대 ${Math.max(...back.map((e) => e.max))}번)`));
          meta.append(el("span", "", info));
          card.append(meta);
        }
        const work = flat(g).filter(h.WORK);
        const ais = work.filter(IS_AI);
        card.append(el("p", "auto-count", creating ? "" : `AI 노드 ${ais.length}개 · 노드를 눌러 바로 고치거나, 여기서 따로 시험해요`));
        (creating ? work : ais).forEach((n, i) => card.append(stepRow(n, i)));
        if (!creating && work.length > ais.length) card.append(el("p", "auto-hint", `외 ${work.length - ais.length}단계 · 그래프에서 봐요`));
        if (!work.length) card.append(el("p", "wf-empty", "아직 할 일이 없어요. 그래프에서 노드를 이어 주세요."));
        if (creating) {
          card.append(el("p", "auto-hint", "단계는 전문가·한 번 호출·처리(코드·HTTP)·사람 확인 중에서 골라요. 둘 이상이면 그래프로 이어져요."), link("+ 단계 추가", () => { addStep(); paint(); touch(); }));
        } else card.append(link("+ 노드 추가 — 그래프에서 이어 붙여요", () => leave(() => h.openGraph(rec, { graphMode: true }))));
      };
      const addStep = () => {
        const g = d.graph;
        const last = flat(g).filter(h.WORK).pop();
        const id = `n${Math.random().toString(36).slice(2, 7)}`;
        g.nodes.push({ id, type: "ai_once", title: "", x: 0, y: 0, config: { prompt: "", access: "read" } });
        const toEnd = g.edges.find((e) => e.from === last?.id && g.nodes.find((n) => n.id === e.to)?.type === "end");
        if (toEnd) { toEnd.to = id; } else if (last) g.edges.push({ from: last.id, to: id });
        const end = g.nodes.find((n) => n.type === "end");
        if (end) g.edges.push({ from: id, to: end.id });
        d.graph = h.layoutGraph(g);
        d.graph.nodes.forEach((n) => { n.config ||= {}; });
      };
      // {변수}: what the step may read from before it. One incoming line is {{input}}; a merge names each line as {{in.<id>}}.
      function varBar(n, ta) {
        const wrap = el("span", "auto-vars");
        const ins = (d.graph.edges || []).filter((e) => e.to === n.id && !h.isBack(e));
        const choices = ins.length > 1 ? ins.map((e) => [`{{in.${e.from}}}`, h.nodeTitle(d.graph.nodes.find((x) => x.id === e.from) || { id: e.from })]) : [["{{input}}", "앞 단계 결과"]];
        const hold = (b) => b.addEventListener("mousedown", (e) => e.preventDefault()); // keep the text box focused so a click inserts instead of closing it
        const chips = el("span", "auto-chips");
        chips.hidden = true;
        for (const [v, label] of choices) {
          const c = el("button", "auto-pill", `${v} · ${label}`); c.type = "button"; hold(c);
          c.addEventListener("click", () => { const a = ta.selectionStart ?? ta.value.length; ta.setRangeText(v, a, ta.selectionEnd ?? a, "end"); ta.dispatchEvent(new Event("input")); ta.focus(); chips.hidden = true; });
          chips.append(c);
        }
        const open = link("+ 변수 추가", () => { chips.hidden = !chips.hidden; });
        hold(open);
        wrap.append(open, chips);
        return wrap;
      }
      function stepRow(n, i) {
        const row = el("div", "auto-step");
        const top = el("div", "auto-step-top");
        top.append(el("span", "mono auto-idx", String(i + 1).padStart(2, "0")));
        const p = byId(n.config?.persona);
        const face = el("span", "auto-face");
        face.append(IS_AI(n) ? (p ? h.avatarMark(p.crew) : h.modelMark(h.modelOf(n, d))) : icon(h.NODES[n.type]?.icon || "play"));
        const who = el("div", "auto-step-who");
        const title = el("strong", "");
        inlineText(title, () => n.title || "", (v) => { n.title = v; touch(); }, { maxLength: 80, placeholder: creating ? "단계 이름" : h.nodeTitle(n) });
        if (!creating && !n.title) title.textContent = h.nodeTitle(n);
        const kind = el("small", "");
        if (creating && IS_AI(n)) {
          const pick = h.select([["", "한 번 호출"], ["agent", "전문가 (직접 쓰기)"], ...h.personas().map((x) => [x.id, `전문가 · ${x.crew.name}`])], n.type === "ai_once" ? "" : n.config.persona || "agent");
          pick.setAttribute("aria-label", "맡을 전문가");
          pick.addEventListener("change", () => {
            const key = goalKey(n);
            const text = n.config[key] || "";
            delete n.config.goal; delete n.config.prompt; delete n.config.persona;
            if (pick.value === "") n.type = "ai_once"; else { n.type = "ai"; if (pick.value !== "agent") n.config.persona = pick.value; }
            n.config[goalKey(n)] = text;
            touch(); paint();
          });
          kind.append(pick);
        } else kind.textContent = [kindText(n), p?.crew.name].filter(Boolean).join(" · ");
        who.append(title, kind);
        top.append(face, who, el("span", "grow"));
        if (IS_AI(n)) {
          const model = h.modelInput(n.config.model || "", d.model || "deepseek-v4-flash");
          model.classList.add("auto-model");
          model.setAttribute("aria-label", "모델");
          model.addEventListener("input", () => { n.config.model = model.value; touch(); });
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
            const ta = h.input(n.config[gk] || "", { tag: "textarea", rows: 2, maxLength: 8000, placeholder: "무엇을 할지 적어요. {{input}}으로 앞 단계 결과를 넣을 수 있어요" });
            ta.addEventListener("input", () => { n.config[gk] = ta.value; touch(); });
            wrap.append(el("small", "", gk === "goal" ? "목표" : "질문"), ta, varBar(n, ta));
          } else {
            const t = el("span", "auto-goal-text");
            const paintG = () => { t.replaceChildren(el("small", "", gk === "goal" ? "목표 · " : "질문 · "), varText(n.config[gk] || "(비어 있어요)")); };
            paintG();
            t.tabIndex = 0;
            t.title = "눌러서 고치기";
            const edit = () => {
              const ta = h.input(n.config[gk] || "", { tag: "textarea", rows: 3, maxLength: 8000 });
              const edit2 = el("div", "auto-goal-edit");
              edit2.append(ta, varBar(n, ta));
              let done = false;
              const end = (c) => { if (done) return; done = true; if (c) { n.config[gk] = ta.value; touch(); } edit2.replaceWith(t); paintG(); };
              ta.addEventListener("blur", () => end(true));
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

    // 진행 알림: where progress goes. Saved with the workflow; bot tokens and connection state live in 설정 › 메신저.
    function notifySection() {
      const s = el("section", "auto-sec");
      const head2 = el("div", "auto-sec-head");
      const t = el("h3", "");
      t.append(icon("chat-text"), "진행 알림");
      head2.append(t, link("+ 채널 연결", () => { (d.notify ||= {}).channels = [...(d.notify.channels || []), { kind: "discord", channel: "", when: "events" }]; touch(); paint(); }), el("span", "grow"), el("small", "auto-dim", "설정 › 메신저에 연결한 곳으로 보내요"));
      const list = el("div", "auto-box");
      const channels = () => d.notify?.channels || [];
      const setNotify = (fn) => { d.notify ||= {}; fn(d.notify); if (!d.notify.channels?.length && !d.notify.moments) delete d.notify; touch(); };
      function paint() {
        list.replaceChildren();
        if (!channels().length) {
          const empty = el("div", "auto-row");
          empty.append(el("span", "auto-dim", "아직 연결한 채널이 없어요 · 디스코드·텔레그램·슬랙"), el("span", "grow"), pill("연결하기", () => { setNotify((n) => { n.channels = [{ kind: "discord", channel: "", when: "events" }]; }); paint(); }));
          list.append(empty);
        }
        channels().forEach((c, i) => {
          const r = el("div", "auto-row notify");
          const kind = h.select(NOTIFY_KINDS, c.kind);
          kind.setAttribute("aria-label", "메신저");
          kind.addEventListener("change", () => { c.kind = kind.value; touch(); paint(); });
          const ch = h.input(c.channel, { maxLength: 80, placeholder: c.kind === "telegram" ? "나에게" : "#채널" });
          ch.setAttribute("aria-label", "채널");
          ch.addEventListener("input", () => { c.channel = ch.value.trim(); touch(); });
          const when = h.select(NOTIFY_WHEN, c.when);
          when.setAttribute("aria-label", "시작 방식");
          when.addEventListener("change", () => { c.when = when.value; touch(); });
          const del = el("button", "icon-btn plain");
          del.type = "button";
          del.setAttribute("aria-label", "채널 빼기");
          del.append(icon("x"));
          del.addEventListener("click", () => { setNotify((n) => { n.channels = channels().filter((_, j) => j !== i); if (!n.channels.length) delete n.channels; }); paint(); });
          r.append(kind, ch, el("span", "grow"), ...(c.kind === "discord" ? [el("small", "auto-dim", "준비 중")] : []), when, del);
          list.append(r);
        });
        const mo = el("div", "auto-row");
        const chips = el("span", "auto-chips");
        const cur = () => d.notify?.moments || DEFAULT_MOMENTS;
        for (const [k, label] of MOMENTS) {
          const b = el("button", `auto-pill toggle${cur().includes(k) ? " on" : ""}`, `${cur().includes(k) ? "✓ " : ""}${label}`);
          b.type = "button";
          b.setAttribute("aria-pressed", String(cur().includes(k)));
          b.addEventListener("click", () => { const next = cur().includes(k) ? cur().filter((x) => x !== k) : MOMENTS.map(([m]) => m).filter((m) => m === k || cur().includes(m)); setNotify((n) => { n.moments = next; if (same(next, DEFAULT_MOMENTS)) delete n.moments; }); paint(); });
          chips.append(b);
        }
        mo.append(el("span", "auto-key", "알림 시점"), el("span", "grow"), chips);
        list.append(mo);
      }
      paint();
      s.append(head2, list);
      return s;
    }

    // 채널 연결 (1052:65139): the bot channels that start this workflow (channel action kind "workflow"). Same data as the expert and team popups;
    // changes save at once (POST /v1/discord/bots/{id}/update with the whole channel list), not with the 저장 bar.
    function channelSection() {
      const s = el("section", "auto-sec");
      const head2 = el("div", "auto-sec-head");
      const t = el("h3", "");
      t.append(icon("chat-text"), "채널 연결");
      const add = link("+ 채널 추가", () => { st.add = st.add ? null : { bot: st.bots?.[0]?.id || "" }; paint(); });
      head2.append(t, ...(creating ? [] : [add]), el("span", "grow"), el("small", "auto-dim", "어디서 이 자동화를 부를지 정해요 · 봇 토큰·연결 상태는 디스코드 봇 설정에서"));
      const list = el("div", "auto-box");
      const st = { bots: creating ? [] : null, add: null, known: {} };
      const botOf = (r) => (r?.discord_bot ? { ...r.discord_bot, id: r.id, revision: r.revision } : r);
      const alive = () => pop?.dialog.isConnected;
      const saveBot = async (b, channels, okText) => {
        try {
          const saved = await request(`/v1/discord/bots/${encodeURIComponent(b.id)}/update`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: b.revision,
            bot: { name: b.name, channels, allowed: b.allowed || { user_ids: [], role_ids: [] }, ...(b.target ? { target: b.target } : {}), ...(b.guild_id ? { guild_id: b.guild_id } : {}) } }) });
          Object.assign(b, saved?.id ? botOf(saved) : { channels, revision: (b.revision || 0) + 1 });
          notice(okText);
        } catch (e) { notice(h.errorText(e)); }
        if (alive()) paint();
      };
      function paint() {
        list.replaceChildren();
        const bots = st.bots || [];
        const mine = bots.flatMap((b) => (b.channels || []).filter((c) => c.action?.kind === "workflow" && c.action.id === rec?.id).map((c) => ({ c, b })));
        for (const { c, b } of mine) {
          const r = el("div", "auto-row notify");
          const mode = pill(c.auto_start ? "글이 올라오면 시작" : `$${rec.crew.handle}로 부를 때만`, () => saveBot(b, b.channels.map((x) => (x === c ? { ...x, auto_start: !x.auto_start } : x)), "바꿨어요."));
          mode.title = "눌러서 바꿔요";
          const warn = ["needs_permission", "token_invalid"].includes(b.state);
          r.append(el("span", "auto-key", `디스코드 · #${c.name || c.channel_id}`), el("small", "auto-dim", b.name || ""), ...(warn ? [el("span", "wf-badge small warn", "확인 필요")] : []), el("span", "grow"), mode,
            pill("빼기", () => { if (confirm(`#${c.name || c.channel_id} 연결을 뺄까요?`)) saveBot(b, b.channels.filter((x) => x !== c), "연결을 뺐어요."); }));
          list.append(r);
        }
        const note = (text) => { const r = el("div", "auto-row"); r.append(el("span", "auto-dim", text)); list.append(r); };
        if (creating) note("자동화를 만든 뒤 채널을 연결해요.");
        else if (st.bots === null) note("불러오는 중…");
        else if (!mine.length) note(bots.length ? "연결된 채널이 없어요." : "연결된 디스코드 봇이 없어요. 디스코드 화면에서 봇을 먼저 추가해 주세요.");
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
          row.append(botSel, chSel, el("span", "grow"), pill("연결", () => {
            const id = chSel.value;
            if (!id) { notice("채널을 골라 주세요."); return; }
            if ((b.channels || []).some((x) => x.channel_id === id) && !confirm("이 채널은 이미 다른 곳에 연결돼 있어요. 이 자동화로 바꿀까요?")) return;
            const ch = { channel_id: id, name: (known || []).find((x) => x.channel_id === id)?.name || "", action: { kind: "workflow", id: rec.id }, auto_start: false };
            st.add = null;
            saveBot(b, [...(b.channels || []).filter((x) => x.channel_id !== id), ch], "채널을 연결했어요.");
          }));
          list.append(row);
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
        const modelText = el("span", "");
        const paintModel = () => { modelText.replaceChildren(el("span", "mono", d.model || "노드마다 정해요"), el("span", "auto-dim", d.model ? " · 모델을 비워 둔 AI 단계가 이걸 써요" : creating ? " · 기본값, 단계마다 바꿀 수 있어요" : " · 단계마다 정해요")); };
        paintModel();
        const backs = (d.graph.edges || []).filter(h.isBack);
        const stops = [d.limits?.max_minutes ? `최대 ${d.limits.max_minutes}분` : "", d.limits?.max_steps ? `단계 ${d.limits.max_steps}번` : "", backs.length ? `되돌아가는 선은 ${Math.max(...backs.map((e) => e.max))}번까지` : ""].filter(Boolean).join(" · ") || "정하지 않음";
        const editRow = (label, valueEl, mk) => {
          const r = valueRow(label, valueEl, pill("바꾸기", () => {
            const form = mk(() => draw());
            r.replaceChildren(el("span", "auto-key", label), form);
          }));
          return r;
        };
        list.append(editRow("모델", modelText, (done) => {
          const m = h.modelInput(d.model || "", "deepseek-v4-flash");
          m.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); m.blur(); } });
          m.addEventListener("blur", () => { d.model = m.value.trim(); touch(); done(); });
          setTimeout(() => m.focus());
          return m;
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

    // 관리: 복제 · 보관 · 지우기 (the old button row), plus the one-line call hint.
    function manageSection() {
      const s = section("gear-six", "관리");
      const rows = [
        ["설정", "템플릿·실행 방식·웹훅을 예전 설정 창에서 바꿔요", () => leave(() => h.openSettings(rec))],
        ["복제", "같은 단계로 새 자동화를 만들어요", () => leave(() => h.duplicate(rec))],
        ["보관", `목록에서 숨기고 $${base.handle} 부르기를 멈춰요`, async () => { if (dirty() && !confirm("저장하지 않은 변경은 사라져요. 계속할까요?")) return; await h.archive(rec); if (!h.byId(rec.id) || h.byId(rec.id).archived) leaveSilently(); }],
        ["지우기", "정의를 영구히 지워요 · 지난 실행 기록은 남아요", async () => { if (await h.remove(rec)) leaveSilently(); }]
      ];
      s.append(box(...rows.map(([label, sub, fn]) => valueRow(label, el("span", "", sub), pill(label, fn)))));
      return s;
    }

    async function doSave() {
      const out = structuredClone(d);
      out.type = "workflow";
      out.name = (out.name || "").trim();
      out.handle = (out.handle || "").trim();
      if (!out.name) { notice("자동화 이름을 적어 주세요."); return; }
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
      who.append(face, el("strong", "", rec.crew.name), el("small", "", `${work.length}단계 중 ${doneN}단계 · ${h.RUN_LABEL[cr.status] || ""}`));
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
      foot.append(el("small", "auto-dim", [h.runTookText(run), h.ago(run.created_at)].filter(Boolean).join(" · ")), el("span", "grow"));
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
      vh.append(el("strong", "", "판 이력"), el("span", "grow"), el("small", "auto-dim", `저장할 때마다 판이 하나 생겨요. 되돌려도 지금 판은 남아요.`));
      const vbox = el("div", "tm-hist");
      ver.append(vh, vbox);
      const pad2 = (n) => String(n).padStart(2, "0");
      const LABELS = CHANGE;
      const what = (a, b) => LABELS.filter(([k]) => !same(a[k], b[k])).map(([k, l]) => (k === "name" ? `이름 ${a.name || "없음"} → ${b.name || "없음"}` : k === "model" ? `모델 ${a.model || "기본"} → ${b.model || "기본"}` : `${l} 바꿈`));
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
          if (cur) acts.append(el("em", "", "지금 쓰는 판"));
          else {
            const lk = (label, cls, fn) => { const b = el("button", `tm-link ${cls}`, label); b.type = "button"; b.addEventListener("click", fn); return b; };
            acts.append(lk("바뀐 점 보기", "quiet", () => { if (!open.delete(x.revision)) open.add(x.revision); paintVersions(); }), lk("이 판으로 되돌리기", "plain", async () => {
              if (!confirm(`v${x.revision}로 되돌릴까요? 새 판으로 저장돼요.`)) return;
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
          if (open.has(x.revision)) { const lines = what(items[0].crew, x.crew); const p = el("p", "tm-hist-diff"); p.append(...(lines.length ? lines : ["바뀐 점을 찾지 못했어요."]).map((t) => el("span", "", t))); vbox.append(p); }
        });
        if (!items?.length) vbox.append(el("p", "auto-hint auto-empty", items ? "판 이력을 불러오지 못했어요." : "불러오는 중…"));
      };
      paintVersions();
      request(`/v1/crew/versions/${encodeURIComponent(rec.id)}?${query()}`).then((r) => { items = r.items || []; }).catch(() => { items = []; }).then(() => { if (pop?.dialog.isConnected) paintVersions(); });
      body.append(ver);
      const sec = el("section", "auto-sec");
      const head2 = el("div", "wf-sec-head");
      const pick = el("select", "wf-select small");
      pick.setAttribute("aria-label", "실행 상태");
      for (const [k, label] of [["all", "모든 상태"], ["failed", "실패"], ["waiting", "확인 대기"], ["running", "도는 중"], ["succeeded", "완료"], ["stopped", "멈춤"]]) { const o = el("option", "", label); o.value = k; pick.append(o); }
      const count = el("small", "", "불러오는 중…");
      const title = el("strong", "", "최근 실행");
      head2.append(title, count, el("span", "grow"), pick);
      head2.append(el("small", "auto-dim", "테스트 탭에서 돌린 것도 여기 남아요"));
      const list = el("div", "wf-runs");
      sec.append(head2, list);
      body.append(sec);
      let all = false;
      const load = async () => {
        count.textContent = "불러오는 중…";
        let items = [];
        try { items = (await request(`/v1/crew/runs?${query({ crew_id: rec.id, ...(pick.value !== "all" ? { status: pick.value } : {}), limit: 30 })}`)).items || []; } catch (_) { /* empty */ }
        if (!pop.dialog.isConnected) return;
        count.textContent = `${items.length}건`;
        list.replaceChildren(...(all ? items : items.slice(0, 10)).map((r) => {
          const row = h.runRow(r, () => leave(() => h.openGraph(rec, { run: r })));
          const t = TRIGGERS[r.crew_run?.trigger];
          if (t) row.querySelector("small")?.prepend(`${t} · `);
          return row;
        }));
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
    pop = popup.open({ title: d.name, sub: "", tabs, initial: startTab, cls: `v4-auto${creating ? " v4-auto-new" : ""}`, onClose: () => { stopLive(); document.querySelector(".auto-menu")?.remove(); state.selected = null; h.afterClose(); } });
    head(pop.dialog);
    void loadStats();
    return pop;
  }

  return { open: (rec, tab) => open(rec, tab), create: () => open(null) };
};
