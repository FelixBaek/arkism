// @ts-check
// Owns node dialogs and inspectors. Dependencies are explicit live ports, never a global scope.
export function createCrewNodes(deps) {


  // ---------------------------------------------------------------- node window (1024:60724 … 1033:69906)
  // A field whose control is a row of buttons: a <div>, since a <label> would click its first button.
  function group(label, control, hint) {
    const f = deps.el("div", "field", label);
    f.append(control);
    if (hint) f.append(deps.el("small", "dim", hint));
    return f;
  }

  function nodeBody(box, n) {
    if (deps.IS_AI(n.type) || n.type === "team") { aiFields(box, n); return; }
    const title = deps.input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; deps.touch(); deps.syncCard(n); });
    box.append(deps.field("이름", title));
    let pair = null; // n8n: 결과 받기 and 기다릴 최대 시간 share a row (1033:69906)
    const starts = ["start", "schedule", "webhook"].includes(n.type);
    if (starts) triggerFields(box, n);
    // 반복 (목록 위치 · 동시에 도는 개수) and 하위 워크플로우 (부를 워크플로우 · 넘길 일) sit side by side (1033:67xxx)
    const row = ["loop", "subflow"].includes(n.type) ? deps.el("div", "scope-fields") : null;
    if (row) box.append(row);
    for (const [key, label, kind, opt, hint] of starts ? [] : deps.FIELDS[n.type] || []) {
      let i;
      if (kind === "radio") {
        const f = group(label, deps.segment(opt, n.config[key] ?? opt[0][0], (v) => { n.config[key] = v; deps.touch(); deps.redraw(); }), hint);
        if (n.type === "n8n") { pair = deps.el("div", "scope-fields"); pair.append(f); box.append(pair); } else box.append(f);
        continue;
      }
      if (kind === "select") i = deps.select(opt, n.config[key] ?? opt[0][0]);
      else if (kind === "workflow") i = deps.select([["", "고르세요"], ...deps.workflows().filter((w) => w.id !== deps.ed.rec.id).map((w) => [w.crew.handle, `$${w.crew.handle} · ${w.crew.name}`])], (n.config[key] || "").replace(/^\$/, ""));
      else if (kind === "code" || (kind === "area" && n.type !== "subflow")) i = deps.input(n.config[key], { tag: "textarea", rows: kind === "code" ? 12 : 4, cls: kind === "code" ? "code" : "" });
      else i = deps.input(n.config[key], { placeholder: typeof opt === "string" ? opt : "" });
      i.addEventListener("input", () => { n.config[key] = i.value; deps.touch(); });
      i.addEventListener("change", () => {
        n.config[key] = i.value;
        deps.touch();
        if (key === "workflow") { const w = deps.byHandle(i.value); if (w && (!n.title || n.title === deps.NODES.subflow.label)) n.title = w.crew.name; deps.redraw(); } else deps.syncCard(n);
      });
      const fi = ["question", "message", "task"].includes(key) ? deps.aiField(i, n, key, label) : i;
      if (pair) { pair.append(deps.field(label, fi)); if (hint) box.append(deps.el("p", "meta", hint)); pair = null; continue; }
      (row || box).append(deps.field(label, fi, hint));
    }
    sends(box, n);
    if (n.type === "code") box.append(deps.el("p", "meta", "출력이 {\"ok\": true/false, …}면 통과·실패 갈래로 나뉘어요. 처음 한 번은 빌드에 1~2초, 30초 안에 끝나야 해요. 토큰은 들지 않아요."));
    if (n.type === "approval") box.append(deps.el("p", "meta", "배포·게시처럼 되돌릴 수 없는 단계 바로 앞에 두세요. 답이 올 때까지 멈춰요."));
    if (n.type === "subflow") box.append(deps.el("div", "wf-attn small", "자기 자신을 부를 수 없어요. 최대 세 겹까지 연결해요."));
    if (n.type === "n8n") box.append(deps.el("p", "meta", "끝나면 알려줌을 고르면 n8n이 callback_url로 결과를 돌려줄 때까지 멈춰요. 서버에 공개 주소가 있어야 해요."));
    if (deps.WORK(n) && !deps.ownerOf(deps.ed.rec)) box.append(onlyRun(n));
  }

  // 시작 노드 팝업 (1033:66756): how the workflow starts. It edits a draft of the workflow's trigger that 저장 keeps.
  function triggerFields(box, n) {
    const tr = deps.trigDraft(n);
    box.append(group("시작", deps.segment([["", "직접 부름"], ["schedule", "일정"], ["webhook", "웹훅"]], tr.kind, (v) => { tr.kind = v; deps.touch(); deps.redraw(); })));
    if (tr.kind === "schedule") {
      const cron = deps.input(tr.cron, { maxLength: 64, cls: "mono" });
      const hintOf = () => { const x = deps.nextRun(cron.value); return x ? `${deps.cronText(cron.value)} · 다음 실행 ${new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }).format(x)}` : "형식: 분 시 일 월 요일 (예: 0 9 * * 1-5)"; };
      const f = deps.field("언제 (분 시 일 월 요일)", cron);
      const hint = deps.el("small", "dim", hintOf());
      f.append(hint);
      cron.addEventListener("input", () => { tr.cron = cron.value; if (n.type === "schedule") n.config.cron = cron.value; hint.textContent = hintOf(); deps.touch(); });
      box.append(f);
    }
    if (tr.kind === "webhook") {
      const label = deps.input(tr.label, { maxLength: 60, placeholder: "GitHub PR 열림" });
      label.addEventListener("input", () => { tr.label = label.value; if (n.type === "webhook") n.config.note = label.value; deps.touch(); });
      box.append(deps.field("웹훅 메모", label, "주소와 토큰은 서버에서 arkism-crew hook-url로 받아요"));
    }
    if (!tr.kind) box.append(deps.el("p", "meta", `$${deps.ed.rec.crew.handle}로 부르거나 실행을 눌렀을 때 시작해요.`));
  }

  const nodeKind = (n) => (n.type === "team" ? "팀 노드" : deps.HUMAN_KIND[deps.NODES[n.type]?.group] || "노드");

  const nodeLine = (n) => (deps.IS_AI(n.type) || n.type === "team" ? nodeKind(n) : `${nodeKind(n)} · ${deps.NODES[n.type]?.sub || ""}`);

  // The node window edits the node in place; 취소 (or ×, Esc) puts the graph back as it was when the window opened.
  function openNodeDialog(n, before = { graph: structuredClone(deps.ed.graph), dirty: deps.ed.dirty, trigger: structuredClone(deps.ed.trigger) }) {
    if (!n) return;
    deps.nodeDlg?.remove();
    const editorGraph = deps.ed.graph, editorRecord = deps.ed.rec;
    let keep = false;
    const restoreHistory = deps.keepHistory();
    const dlg = (deps.nodeDlg = deps.el("dialog", `crew-dialog wf-dialog node-dialog${deps.IS_AI(n.type) || n.type === "team" ? " wide" : ""}`));
    const head = deps.el("div", "dialog-head");
    const lead = deps.el("span", "g-icon");
    const t = deps.el("div", "");
    const h = deps.el("h2", "");
    const sub = deps.el("p", "");
    t.append(h, sub);
    head.append(lead, t, deps.closeX("노드 설정 닫기", () => dlg.close()));
    const body = deps.el("div", "dialog-body");
    const card = deps.el("div", "node-card");
    body.append(card);
    const foot = deps.el("div", "dialog-foot");
    const left = deps.el("div", ""), right = deps.el("div", "");
    left.append(deps.button("적용", "primary", null, () => { keep = true; dlg.close(); }), deps.button("취소", "ghost", null, () => dlg.close()));
    if (deps.WORK(n) && !deps.ownerOf(deps.ed.rec)) right.append(deps.button("이 노드만 실행", "", null, () => {
      const saved = !deps.ed.dirty && (deps.ed.rec.crew.graph?.nodes || []).some((x) => x.id === n.id);
      if (!saved) { deps.notice("저장한 뒤에 실행할 수 있어요."); return; }
      keep = true;
      dlg.close();
      void deps.startRun(deps.ed.rec, "", n.id);
    }));
    right.append(deps.button("노드 지우기", "ghost", null, async () => {
      if (await deps.confirmBox("노드를 지울까요?", "이 노드와 이어진 선이 함께 지워져요. 저장하지 않고 나가면 원래대로 돌아가요.", "노드 지우기")) { keep = true; dlg.close(); deps.removeNode(n.id); }
    }));
    foot.append(left, right);
    const draw = () => {
      card.replaceChildren();
      nodeBody(card, n);
      const ex = deps.byId(n.type === "expert" ? n.config?.expert : n.config?.persona);
      lead.replaceChildren(ex ? deps.avatarMark(ex.crew) : n.type === "team" ? teamMark(deps.byId(n.config?.team)) : deps.icon(deps.NODES[n.type]?.icon || "stack"));
      lead.classList.toggle("plain", !ex && n.type !== "team");
      h.textContent = deps.nodeTitle(n);
      sub.textContent = nodeLine(n);
    };
    deps.redraw = () => { const y = body.scrollTop; draw(); body.scrollTop = y; deps.render(false); keepDlg(); };
    const keepDlg = () => { if (deps.nodeDlg === dlg && !dlg.isConnected) document.body.append(dlg); };
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => {
      dlg.remove();
      if (deps.nodeDlg === dlg) { deps.nodeDlg = null; deps.redraw = () => deps.render(false); }
      if (deps.ed.graph !== editorGraph || deps.ed.rec !== editorRecord) return;
      if (!keep) { deps.ed.graph.nodes = before.graph.nodes; deps.ed.graph.edges = before.graph.edges; deps.ed.dirty = before.dirty; deps.ed.trigger = before.trigger; restoreHistory(); }
      if (!deps.ed.graph.nodes.some((x) => x.id === deps.ed.sel)) deps.ed.sel = null; // the panel stays on this node
      deps.render(false);
    });
    draw();
    document.body.append(dlg);
    dlg.showModal();
  }

  // ---------------------------------------------------------------- inspector (198:2350 · 321:19936 · 321:21027)
  function inspector() {
    const box = deps.el("aside", "g-right");
    if (deps.ed.selEdge !== null && deps.ed.graph.edges[deps.ed.selEdge]) return edgeInspector(box, deps.ed.graph.edges[deps.ed.selEdge]);
    if (deps.ed.sel && deps.nodeIn(deps.ed.sel)) return deps.nodePanel(box, deps.nodeIn(deps.ed.sel));
    {
      const x = deps.ed.rec.crew;
      const backs = deps.ed.graph.edges.filter(deps.isBack);
      box.append(deps.el("h3", "", "워크플로우"), deps.kv([
        ["$이름", `$${x.handle}`],
        ["워커 기본", x.model || "노드마다"],
        ["멈추는 조건", [backs.length ? `되돌아가기 ${Math.max(...backs.map((e) => e.max))}번` : "", x.limits?.max_steps ? `단계 ${x.limits.max_steps}번` : "", x.limits?.max_minutes ? `${x.limits.max_minutes}분` : ""].filter(Boolean).join(" · ") || "정하지 않음"],
        ["일정·웹훅", x.trigger ? `${deps.triggerText(x)}${x.enabled ? "" : " · 꺼짐"}` : "없어요"]
      ]), deps.button("워크플로우 설정", "", "gear-six", () => deps.openSettings(deps.ed.rec, null, true)), deps.el("i", "wf-rule"));
      box.append(deps.el("h3", "", "노드 설정"), deps.el("p", "dim", "노드를 누르면 여기서 고쳐요. 출력 점을 끌어 다음 노드에 이어요. 앞 노드로 이으면 되돌아가는 선이 돼요. 선을 누르면 선 설정이 열려요. 휠로 확대·축소하고, 휠 클릭이나 스페이스를 누른 채 끌면 화면이 움직여요."));
      const g = deps.ed.graph;
      box.append(deps.kv([["노드", `${g.nodes.length}개`], ["AI 노드", `${deps.aiNodes(g).length}개`], ["되돌아가는 선", `${g.edges.filter(deps.isBack).length}개`]]));
      box.append(deps.button("구조 검토", "", "sparkle", () => { deps.ed.review = true; deps.render(false); }));
      return box;
    }
  }

  // A team's mark: its cover photo, or the members' faces.
  function teamMark(t) {
    const x = t?.crew;
    if (x?.cover?.thumb || x?.cover?.url) {
      const img = deps.el("img", "node-team-photo");
      img.alt = "";
      img.src = x.cover.thumb || x.cover.url;
      return img;
    }
    return x ? teamFaces(x) : deps.icon("users-three");
  }

  function teamFaces(x) {
    const f = deps.el("span", "node-faces");
    for (const m of (x.members || []).slice(0, 5)) { const a = deps.byId(m.agent_id); if (a) f.append(deps.avatarMark(a.crew)); }
    if (!f.childElementCount) f.append(deps.icon("users-three"));
    return f;
  }

  // 전문가·팀 고르기 (1024:60724 · 1024:61783): the picked one as a face card with 바꾸기, the others as chips.
  function pickCard(label, cur, all, onPick) {
    const wrap = deps.el("div", "field");
    wrap.append(label);
    const card = deps.el("div", "node-pick");
    const lead = deps.el("span", "node-pick-lead");
    const txt = deps.el("div", "");
    if (cur) {
      const x = cur.crew;
      lead.append(x.type === "team" ? teamMark(cur) : deps.avatarMark(x));
      const line = x.type === "team"
        ? [`@${x.handle}`, deps.MODES[x.mode] || "", x.ending?.max_rounds ? `최대 ${x.ending.max_rounds}라운드` : ""]
        : [`@${x.handle}`, x.model || "모델 자동", x.skills?.length ? `스킬 ${x.skills.length}개` : ""];
      const name = deps.el("strong", "", x.name); name.setAttribute("data-no-i18n", "");
      txt.append(name, deps.el("small", "dim", line.filter(Boolean).join(" · ")));
      if (x.type === "team") txt.append(teamFaces(x));
    } else txt.append(deps.el("strong", "dim", "고르세요"));
    const change = deps.select([["", cur ? "바꾸기" : "고르기"], ...all.map((r) => [r.id, r.crew.name])], "");
    for (const o of change.options) if (o.value) o.setAttribute("data-no-i18n", ""); // saved names stay as written; the empty first option is UI text
    change.className = "node-pick-change";
    change.setAttribute("aria-label", `${label} 바꾸기`);
    change.addEventListener("change", () => { const r = deps.byId(change.value); if (r) onPick(r); });
    card.append(lead, txt, change);
    const chips = deps.el("div", "chip-row");
    for (const r of all.filter((r) => r !== cur).slice(0, 4)) {
      const b = deps.el("button", "chip node-chip");
      b.type = "button";
      const name = deps.el("span", "", r.crew.name); name.setAttribute("data-no-i18n", "");
      b.append(r.crew.type === "team" ? teamMark(r) : deps.avatarMark(r.crew), name);
      b.addEventListener("click", () => onPick(r));
      chips.append(b);
    }
    const add = deps.el("button", "chip node-chip dim", label === "팀" ? "+ 새 팀" : "+ 새 전문가");
    add.type = "button";
    add.addEventListener("click", () => newMemberPopup(label === "팀", onPick));
    chips.append(add);
    wrap.append(card, chips);
    return wrap;
  }

  // + 새 전문가 · + 새 팀 (1024:60724 · 1024:61783): make one right from the node. A new expert needs a name, @이름 and instruction;
  // a new team starts with the expert picked here as its worker.
  function newMemberPopup(isTeam, onPick) {
    const first = deps.agents()[0];
    if (isTeam && !first) { deps.notice("팀은 전문가로 만들어요. 먼저 전문가를 만들어 주세요."); return; }
    const dlg = deps.el("dialog", "crew-dialog ex-skill-dlg");
    const head = deps.el("div", "dialog-head");
    head.append(deps.el("h2", "", isTeam ? "새 팀" : "새 전문가"));
    const body = deps.el("div", "dialog-body");
    const name = deps.input("", { maxLength: 60, placeholder: isTeam ? "예: 릴리스 팀" : "예: 릴리스 매니저" });
    const handle = deps.input("", { maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" });
    name.addEventListener("input", () => { if (!handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); });
    handle.addEventListener("input", () => { handle.dataset.touched = handle.value ? "1" : ""; });
    const ins = deps.input("", { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." });
    const firstPick = deps.select(deps.agents().map((r) => [r.id, r.crew.name]), first?.id);
    body.append(deps.field("이름", name), deps.field("@이름", handle), ...(isTeam ? [deps.field("첫 전문가", firstPick), deps.el("p", "meta", "팀 화면에서 더 넣거나 고칠 수 있어요.")] : [deps.field("지시", ins)]));
    const foot = deps.el("div", "dialog-foot");
    foot.append(deps.button("취소", "ghost", null, () => dlg.close()), deps.button("만들기", "primary", null, async () => {
      const n = name.value.trim(), h = handle.value.trim().replace(/^@/, "");
      if (!n || !/^[\p{L}\p{N}_-]{1,40}$/u.test(h) || (!isTeam && !ins.value.trim())) { deps.notice(isTeam ? "이름과 @이름(띄어쓰기 없이)을 채워 주세요." : "이름·@이름·지시를 채워 주세요."); return; }
      if (deps.agents().some((r) => r.crew.handle === h) || deps.teams().some((r) => r.crew.handle === h)) { deps.notice(`@${h}은 이미 있어요. 다른 이름을 써 주세요.`); return; }
      const crew = isTeam
        ? { type: "team", name: n, handle: h, role: "", members: [{ agent_id: firstPick.value, order: 1, role: "worker" }], mode: "round_robin",
          ending: { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true }, context: [], templates: [] }
        : deps.workerOut({ ...deps.blankAgent(), name: n, handle: h, instructions: ins.value });
      try {
        const saved = await deps.save(null, crew);
        deps.state.data.crew.push(saved);
        dlg.close();
        onPick(saved);
      } catch (e) { deps.notice(deps.errorText(e)); }
    }));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    name.focus();
  }

  // AI·전문가·팀 노드 (1024:60724 · 1024:61783): 누가 맡나 · 무엇을 하나 on the left, 실행 범위 · 나눠서 맡기기 on the right.
  // 무엇·어디까지(목표·울타리·완료 기준)만 정하고, 어떻게는 AI가 정해요.
  function aiFields(box, n) {
    const c = n.config;
    const bind = (i, key, again) => {
      i.addEventListener("input", () => { c[key] = i.value; deps.touch(); });
      i.addEventListener("change", () => { c[key] = i.value; deps.touch(); if (again) deps.redraw(); });
      return i;
    };
    const cols = deps.el("div", "node-cols");
    const L = deps.el("div", "node-col"), R = deps.el("div", "node-col");
    cols.append(L, R);
    box.append(deps.modelList(), cols);
    // 누가 맡나
    L.append(deps.el("h3", "", "누가 맡나"));
    L.append(group("노드 종류", deps.segment([["expert", "전문가"], ["team", "팀"], ["ai", "AI 에이전트"], ["ai_once", "AI에게 한 번 묻기"]], n.type, (k) => {
      if (k === n.type) return;
      const was = n.type, text = c.goal || c.prompt || c.topic || "{{input}}";
      n.type = k;
      if (k === "team") c.topic ||= text;
      else if (k === "ai_once") c.prompt ||= text;
      else { c.goal ||= text; c.access ||= "read"; }
      if (!n.title || n.title === deps.NODES[was]?.label) n.title = deps.NODES[k].label;
      deps.touch();
      deps.redraw();
    })));
    const named = (r, label) => { if (!n.title || n.title === label || n.title === deps.NODES[n.type].label) n.title = r.crew.name; };
    let p = null;
    if (n.type === "expert") {
      const cur = deps.byId(c.expert);
      L.append(pickCard("전문가", cur, deps.agents(), (r) => { named(r, cur?.crew.name); c.expert = r.id; deps.touch(); deps.redraw(); }));
      p = cur;
    } else if (n.type === "team") {
      const cur = deps.byId(c.team);
      L.append(pickCard("팀", cur, deps.teams(), (r) => { named(r, cur?.crew.name); c.team = r.id; deps.touch(); deps.redraw(); }));
    } else {
      const row = deps.el("div", "scope-fields");
      row.append(deps.field("모델", bind(deps.modelInput(c.model, deps.ed.rec.crew.model || "deepseek-v4-flash"), "model", true), deps.ed.rec.crew.model ? `비우면 ${deps.ed.rec.crew.model}` : ""),
        deps.field("추론", bind(deps.select(deps.EFFORTS, c.effort || "medium"), "effort")));
      L.append(row);
      const persona = bind(deps.select([["", "직접 쓰기"], ...deps.personas().map((x) => [x.id, x.crew.name])], c.persona || ""), "persona", true);
      L.append(deps.field("페르소나", persona, "설정 › 워크플로우에서 만든 페르소나를 고르거나 아래에 직접 써요"));
      p = deps.byId(c.persona);
      if (p) { const q = deps.el("div", "quote"); q.append(deps.el("div", "", p.crew.instructions || "")); L.append(q); }
    }
    // 무엇을 하나
    L.append(deps.el("i", "wf-rule"), deps.el("h3", "", "무엇을 하나"));
    const title = deps.input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; deps.touch(); });
    L.append(deps.field("노드 이름", title));
    const slot = "{{input}} 자리에 앞 단계 결과가 들어가요";
    if (n.type === "team") L.append(deps.field("회의 주제", bind(deps.input(c.topic, { tag: "textarea", rows: 3, maxLength: 4000 }), "topic", true), slot));
    else if (n.type === "ai_once") L.append(deps.field("질문", bind(deps.input(c.prompt, { tag: "textarea", rows: 4, maxLength: 8000 }), "prompt", true), slot));
    else L.append(deps.field("목표", bind(deps.input(c.goal, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "하위 질문마다 출처 3개 이상 모아 줘." }), "goal", true), slot));
    if (n.type !== "team") L.append(deps.field(p ? "더할 지시" : "페르소나 지시", bind(deps.input(c.instructions, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." }), "instructions")), skillsField(c));
    if (n.type === "expert" || n.type === "ai" || n.type === "team") {
      // 실행 범위
      R.append(deps.el("h3", "", "실행 범위"),
        deps.field("허용 범위", bind(deps.input(c.fence, { maxLength: 4000, placeholder: "웹 검색과 자료 읽기만 허용" }), "fence")),
        deps.field("완료 기준", bind(deps.input(c.done, { maxLength: 1000, placeholder: "질문마다 출처 3개와 요약" }), "done")),
        group("하는 일", deps.segment([["read", "읽기만 · 동시"], ["write", "쓰기 · 차례로"]], c.access || "read", (v) => { c.access = v; deps.touch(); deps.redraw(); })),
        deps.field("실패하면 다시", bind(deps.select([["0", "안 함"], ["1", "1번"], ["2", "2번"], ["3", "3번"]], c.retries || "0"), "retries")));
      if (n.type !== "team") collabFields(R, n, bind);
    }
    sends(R, n);
    if (deps.WORK(n) && !deps.ownerOf(deps.ed.rec)) R.append(onlyRun(n));
  }

  // 나눠서 맡기기: this node plans, workers (saved experts) do the parts at the same time, this node merges them.
  function collabFields(R, n, bind) {
    const c = n.config;
    const on = c.collab === "on";
    const first = () => deps.agents()[0];
    const ch = deps.el("div", "g-collab-head");
    ch.append(deps.el("h3", "", "나눠서 맡기기"), deps.switchEl(on, "나눠서 맡기기 켜기", (v) => {
      c.collab = v ? "on" : "";
      if (v && !c.workers) c.workers = JSON.stringify([first() ? { expert: first().id } : { name: "워커", model: deps.ed.rec.crew.model || "deepseek-v4-flash" }]);
      deps.touch();
      deps.redraw();
    }));
    R.append(deps.el("i", "wf-rule"), ch);
    if (!on) { R.append(deps.el("p", "meta", "켜면 이 노드가 일을 나눠 워커에게 맡기고 결과를 합쳐요. 하위 질문이 몇 개일지 미리 모를 때 써요.")); return; }
    let workers = [];
    try { workers = JSON.parse(c.workers || "[]"); } catch { workers = []; }
    const saveWorkers = () => { c.workers = JSON.stringify(workers); deps.touch(); };
    const list = deps.el("div", "g-workers");
    workers.forEach((w, i) => {
      const r = deps.el("div", "g-worker");
      const del = deps.el("button", "icon-btn plain");
      del.type = "button";
      del.setAttribute("aria-label", "워커 빼기");
      del.append(deps.icon("x"));
      del.addEventListener("click", () => { workers.splice(i, 1); saveWorkers(); deps.redraw(); });
      if (w.expert || !w.model) {
        // An expert worker: its model and instructions follow the expert (the runner reads them).
        const a = deps.byId(w.expert);
        const lead = deps.el("span", "node-pick-lead");
        lead.append(a ? deps.avatarMark(a.crew) : deps.icon("robot"));
        const pick = deps.select([["", "전문가 고르기"], ...deps.agents().map((x) => [x.id, x.crew.role ? `${x.crew.name} · ${x.crew.role}` : x.crew.name])], w.expert || "");
        pick.setAttribute("aria-label", "워커 전문가");
        for (const o of pick.options) if (o.value) o.setAttribute("data-no-i18n", ""); // saved names stay as written; the empty first option is UI text
        pick.addEventListener("change", () => { workers[i] = pick.value ? { expert: pick.value } : { name: "", model: "" }; saveWorkers(); deps.redraw(); });
        r.classList.add("expert");
        r.append(lead, pick, del);
      } else {
        // A worker saved before experts: a name and a model.
        const nm = deps.input(w.name, { maxLength: 40, placeholder: "조사원" });
        const md = deps.modelInput(w.model, deps.ed.rec.crew.model || "deepseek-v4-flash");
        nm.addEventListener("input", () => { w.name = nm.value; saveWorkers(); });
        md.addEventListener("input", () => { w.model = md.value; saveWorkers(); });
        r.append(nm, md, del);
      }
      list.append(r);
    });
    R.append(group("워커 — 전문가", list, "워커의 모델은 그 전문가 설정을 따라요"),
      deps.button("워커 더하기", "ghost small", "plus", () => { workers.push(first() ? { expert: first().id } : { name: "", model: deps.ed.rec.crew.model || "" }); saveWorkers(); deps.redraw(); }));
    const row3 = deps.el("div", "scope-fields");
    row3.append(deps.field("최대 개수", bind(deps.input(c.max_workers || "5", { type: "number", min: 1, max: 16 }), "max_workers", true)), deps.field("동시에", bind(deps.input(c.concurrency || "3", { type: "number", min: 1, max: 16 }), "concurrency", true)));
    const row4 = deps.el("div", "scope-fields");
    row4.append(deps.field("필요한 것", bind(deps.input(c.needs, { maxLength: 200, placeholder: "검색 결과" }), "needs")), deps.field("만드는 것", bind(deps.input(c.makes, { maxLength: 200, placeholder: "질문별 자료 묶음" }), "makes")));
    R.append(row3, row4, deps.el("p", "meta", "실행할 때 AI가 일을 몇 개로 나눌지 정하고, 워커 결과를 이 노드가 합쳐요."));
  }

  // 스킬: the hub skills this step must follow (the runner puts their SKILL.md into its instructions).
  // Empty leaves it to the AI, which finds skills through the skill router as usual.
  function skillsField(c) {
    const all = deps.state.data.skills || [];
    const picked = (c.skills || "").split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
    const save = (list) => { c.skills = list.join(","); deps.touch(); deps.redraw(); };
    const row = deps.el("div", "chip-row");
    for (const id of picked) {
      const chip = deps.el("span", "chip", all.find((s) => s.id === id)?.name || id);
      const x = deps.el("button", "icon-btn plain");
      x.type = "button";
      x.setAttribute("aria-label", `${id} 빼기`);
      x.append(deps.icon("x"));
      x.addEventListener("click", () => save(picked.filter((p) => p !== id)));
      chip.append(x);
      row.append(chip);
    }
    const dl = deps.el("datalist");
    dl.id = "crew-skills";
    for (const s of all) if (!picked.includes(s.id)) { const o = deps.el("option", "", (s.description || "").slice(0, 80)); o.value = s.id; dl.append(o); }
    const add = deps.input("", { maxLength: 128, placeholder: "스킬 이름으로 찾아 더하기" });
    add.setAttribute("list", "crew-skills");
    add.addEventListener("change", () => { const v = add.value.trim(); if (all.some((s) => s.id === v) && !picked.includes(v)) save([...picked, v]); });
    const f = deps.el("div", "field", "스킬");
    f.append(row, add, dl, deps.el("small", "dim", "고르면 이 단계는 그 스킬을 꼭 따라요. 비우면 AI가 알아서 찾아 써요."));
    return f;
  }

  // 받는 값 · 보내는 곳: where this node's input comes from and where its branches go.
  function sends(box, n) {
    const ins = deps.ed.graph.edges.filter((e) => e.to === n.id && !deps.isBack(e));
    if (ins.length) {
      const s = deps.el("div", "g-sends");
      s.append(deps.el("h4", "", "받는 값"));
      for (const e of ins) {
        const from = deps.ed.graph.nodes.find((x) => x.id === e.from);
        const r = deps.el("div", "g-send");
        r.append(deps.el("span", "mono", ins.length > 1 || n.type === "merge" ? `in.${e.from}` : "{{input}}"), deps.el("span", "", `← ${from ? deps.nodeTitle(from) : e.from}${e.port ? ` (${e.port})` : ""}`));
        s.append(r);
      }
      box.append(s);
    }
    const outs = deps.ed.graph.edges.filter((e) => e.from === n.id);
    if (outs.length) {
      const s = deps.el("div", "g-sends");
      s.append(deps.el("h4", "", deps.IS_AI(n.type) && new Set(outs.map((e) => e.port || "")).size > 1 ? "보내는 곳 · AI가 하나를 골라요" : "보내는 곳"));
      for (const e of outs) {
        const to = deps.ed.graph.nodes.find((x) => x.id === e.to);
        const r = deps.el("div", "g-send");
        const label = deps.PORTS[n.type]?.find(([p]) => p === e.port)?.[1] || e.port || "다음";
        r.append(deps.el("span", "dim", deps.isBack(e) ? `${label} · ↺ 최대 ${e.max}번` : label), deps.el("span", "", `→ ${to ? deps.nodeTitle(to) : e.to}`));
        s.append(r);
      }
      box.append(s);
    }
  }

  function edgeInspector(box, e) {
    const from = deps.ed.graph.nodes.find((n) => n.id === e.from), to = deps.ed.graph.nodes.find((n) => n.id === e.to);
    const head = deps.el("div", "g-right-head");
    const t = deps.el("div", "");
    t.append(deps.el("strong", "", deps.isBack(e) ? "되돌아가는 선" : "선"), deps.el("small", "", `${deps.nodeTitle(from || {})} → ${deps.nodeTitle(to || {})}`));
    head.append(t, deps.closeX("선 설정 닫기", deps.deselect));
    box.append(head);
    if (from && deps.IS_AI(from.type)) {
      const port = deps.input(e.port, { maxLength: 20, placeholder: "통과, 근거 부족…" });
      port.addEventListener("change", () => { const v = port.value.trim(); if (v) e.port = v; else delete e.port; deps.touch(); deps.render(false); });
      box.append(deps.field("갈래 이름", port, "이름 붙은 갈래가 둘 이상이면 AI가 결과를 보고 하나를 골라요"));
    } else if (from) box.append(edgeKinds(e, from));
    if (from && !deps.isBack(e)) box.append(fanOut(e, from));
    const back = deps.wouldCycle({ ...deps.ed.graph, edges: deps.ed.graph.edges.filter((x) => x !== e) }, e.from, e.to);
    if (back) {
      const max = deps.select(Array.from({ length: 10 }, (_, i) => [String(i + 1), `${i + 1}번`]), String(e.max || 2));
      max.addEventListener("change", () => { e.max = Number(max.value); deps.touch(); deps.render(false); });
      box.append(deps.field("최대 몇 번 되돌아갈까요", max, "넘으면 실행을 멈추고 이유를 남겨요. 성공한 노드 결과는 그대로 두고 대상부터 다시 돌아요."));
    } else box.append(deps.el("p", "meta", "앞으로 가는 선이에요. 앞 노드로 이으면 되돌아가는 선이 돼요."));
    if (!from || !deps.IS_AI(from.type)) {
      const info = deps.el("div", "wf-info");
      info.append(deps.el("strong", "", "AI 노드에서 나가는 선이면"), deps.el("p", "", "갈래 이름(예: more, 근거 부족)을 붙일 수 있어요. 이름 붙은 갈래가 둘 이상이면 AI가 결과를 보고 하나를 골라요."));
      box.append(info);
    }
    box.append(deps.button("선 지우기", "ghost danger", "trash", async () => {
      if (!(await deps.confirmBox("선을 지울까요?", `${deps.nodeTitle(from || {})} → ${deps.nodeTitle(to || {})} 선이 지워져요. 저장하지 않고 나가면 원래대로 돌아가요.`, "선 지우기"))) return;
      deps.ed.graph.edges = deps.ed.graph.edges.filter((x) => x !== e);
      deps.ed.selEdge = null;
      deps.touch();
      deps.render(false);
    }));
    return box;
  }

  // 선 종류: what the engine follows. A plain line (no port) always goes on; approval·condition·code end on a named port.
  // There is no line for "the node failed": a failed node stops the run (code's 실패 is its {"ok": false} output).
  const EDGE_KINDS = [["", "항상", null], ["approve", "승인", "approval"], ["reject", "거절", "approval"], ["yes", "예", "condition"], ["no", "아니오", "condition"], ["pass", "성공 시", "code"], ["fail", "실패 시", "code"]];

  function edgeKinds(e, from) {
    const wrap = deps.el("div", "field");
    wrap.append("선 종류");
    const list = deps.el("div", "g-kinds");
    for (const [port, label, only] of EDGE_KINDS) {
      const b = deps.el("button", `g-kind${(e.port || "") === port ? " active" : ""}`);
      b.type = "button";
      const why = !only || only === from.type ? ""
        : port === "fail" ? "아직 서버 미지원"
        : port === "pass" ? "다른 노드는 항상과 같아요"
        : only === "approval" ? "사람 확인 노드에서만" : "조건 노드에서만";
      b.append(deps.el("span", "", label));
      if (why) { b.disabled = true; b.append(deps.el("small", "dim", why)); }
      b.setAttribute("aria-pressed", String((e.port || "") === port));
      b.addEventListener("click", () => { if (port) e.port = port; else delete e.port; deps.touch(); deps.render(false); });
      list.append(b);
    }
    wrap.append(list);
    return wrap;
  }

  // 동시에 보내기: more lines from the same port; the engine follows every one of them at once.
  function fanOut(e, from) {
    const same = deps.ed.graph.edges.filter((x) => x.from === e.from && (x.port || "") === (e.port || "") && !deps.isBack(x));
    const wrap = deps.el("div", "field");
    wrap.append("동시에 보내기");
    if (same.length > 1) wrap.append(deps.el("small", "dim", `이 갈래는 ${same.length}곳으로 동시에 가요: ${same.map((x) => deps.nodeTitle(deps.nodeIn(x.to) || {})).join(", ")}`));
    const free = deps.ed.graph.nodes.filter((x) => x.id !== from.id && !["start", "schedule", "webhook"].includes(x.type) && !same.some((y) => y.to === x.id));
    const add = deps.select([["", "+ 함께 보낼 노드"], ...free.map((x) => [x.id, deps.nodeTitle(x)])], "");
    add.addEventListener("change", () => { if (add.value) deps.linkTo(from, e.port || "", add.value); });
    wrap.append(add);
    return wrap;
  }

  // 이 노드만 실행: the saved graph with just this node, given the value typed here (as {{input}}).
  function onlyRun(n) {
    const s = deps.el("div", "g-sends");
    s.append(deps.el("h4", "", "이 노드만 실행"));
    const row = deps.el("div", "try-row");
    const q = deps.el("input");
    q.placeholder = "넣을 값 (비워도 돼요)";
    const go2 = deps.button("실행", "small", "play", async () => {
      go2.disabled = true;
      await deps.startRun(deps.ed.rec, q.value.trim(), n.id);
      go2.disabled = false;
    });
    const saved = !deps.ed.dirty && (deps.ed.rec.crew.graph?.nodes || []).some((x) => x.id === n.id);
    go2.disabled = !saved;
    if (!saved) go2.title = "저장한 뒤에 실행할 수 있어요";
    row.append(q, go2);
    s.append(row, deps.el("p", "meta", "앞 단계는 건너뛰고 이 노드에만 값을 넣어 돌려요. 결과는 최근 실행에 남아요."));
    return s;
  }


  // ---------------------------------------------------------------- 단계 하나 — 간단히 (03d)
  function renderSimple(n) {
    // 198:2608: the one step's form on the left; what it takes and makes, a one-node run and 단계 더하기 on the right.
    const page = deps.el("div", "wf-simple");
    const form = deps.el("div", "wf-simple-form g-right");
    const head = deps.el("div", "g-right-head");
    const lead = deps.el("span", "g-icon");
    lead.append(deps.icon(deps.NODES[n.type]?.icon || "stack"));
    const t = deps.el("div", "");
    t.append(deps.el("strong", "", deps.nodeTitle(n)), deps.el("small", "dim", `${deps.NODES[n.type]?.label || n.type} · 단계 하나`));
    head.append(lead, t);
    form.append(head);
    const title = deps.input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; deps.touch(); });
    form.append(deps.field("이름", title));
    aiFields(form, n);
    const side = deps.el("aside", "g-right wf-simple-side");
    const c = n.config || {};
    side.append(deps.el("h3", "", "주고받는 것"), deps.kv([["필요한 것", c.needs || "앞 단계 결과"], ["만드는 것", c.makes || "정하지 않음"], ["실패하면", Number(c.retries) ? `${c.retries}번 다시 → 멈추고 보고` : "멈추고 보고"]]), deps.el("i", "wf-rule"));
    // An owned workflow runs only through its agent (@); the server refuses a manual run.
    const owned = deps.ownerOf(deps.ed.rec);
    if (!owned) side.append(onlyRun(n), deps.el("i", "wf-rule"));
    const run = deps.button("실행", "primary", "play", async () => {
      if (deps.ed.dirty && !(await deps.saveGraph())) return;
      await deps.runFromEditor();
    });
    const more = deps.el("div", "wf-side-block");
    more.append(deps.el("h3", "", "단계 더하기"), deps.el("p", "wf-text", "단계를 더하면 그래프 편집기로 바뀌어요. 지금 내용은 첫 단계로 그대로 남아요."),
      deps.button("단계 추가 — 그래프로 바뀌어요", "wf-full center", null, () => { deps.ed.graphMode = true; deps.ed.menu = true; deps.render(false); }));
    const acts = deps.el("div", "crew-actions wf-run-acts");
    if (!owned) acts.append(run);
    acts.append(deps.button("워크플로우 설정", "ghost", "gear-six", () => deps.openSettings(deps.ed.rec, null, true)));
    side.append(more, deps.el("i", "wf-rule"), acts);
    page.append(form, side);
    deps.graphBody().replaceChildren(page);
  }

  return {
    get inspector() { return inspector; },
    get nodeBody() { return nodeBody; },
    get nodeLine() { return nodeLine; },
    get openNodeDialog() { return openNodeDialog; },
    get renderSimple() { return renderSimple; }
  };
}
