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
      if (kind === "dest") { box.append(destField(n, key, label)); continue; }
      if (kind === "radio") {
        const opts = n.type === "notify" && n.config[key] === "email" ? [...opt, ["email", "메일"]] : opt;
        const f = group(label, deps.segment(opts, n.config[key] ?? opt[0][0], (v) => { n.config[key] = v; if (n.type === "notify") n.config.to = ""; deps.touch(); deps.redraw(); }), hint);
        if (n.type === "n8n") { pair = deps.el("div", "scope-fields"); pair.append(f); box.append(pair); } else box.append(f);
        continue;
      }
      if (kind === "select") i = deps.select(opt, n.config[key] ?? opt[0][0]);
      else if (kind === "workflow") i = deps.select([["", "고르세요"], ...deps.workflows().filter((w) => w.id !== deps.ed.rec.id).map((w) => [w.crew.handle, `${deps.alias(w.crew)} · ${w.crew.name}`])], (n.config[key] || "").replace(/^\$/, ""));
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

  // 받는 곳: the destinations saved in 설정 › 메신저 for the chosen messenger, instead of a name typed by hand.
  function destField(n, key, label) {
    const f = deps.el("div", "field", label);
    const body = deps.el("div", "g-dest");
    body.append(deps.el("small", "dim", "불러오는 중…"));
    f.append(body);
    const channel = n.config.channel || "telegram";
    const NAME = { telegram: "텔레그램", slack: "슬랙", email: "메일" };
    void deps.messengerDests().then((all) => {
      if (all === null || channel === "email") {
        // An older server without /v1/messengers (or mail, which 설정 › 메신저 does not hold): the name stays a text box.
        const i = deps.input(n.config[key], { placeholder: "default" });
        i.addEventListener("input", () => { n.config[key] = i.value; deps.touch(); });
        body.replaceChildren(i, deps.el("small", "dim", "서버의 알림 설정에 적은 이름이에요."));
        return;
      }
      const list = all.filter((d) => d.channel === channel);
      if (!list.length) {
        // A name saved before is kept, and said to be missing, until a destination is connected.
        body.replaceChildren(deps.el("small", "dim", n.config[key] ? `${n.config[key]} · 설정 › 메신저에 없어요` : `아직 연결한 ${NAME[channel]}이 없어요.`),
          deps.button("메신저 연결하기", "small", "link", () => deps.openMessenger()));
        return;
      }
      const cur = n.config[key] || "";
      if (!cur) n.config[key] = list[0].name; // the first destination, saved with the next 저장
      // Named as in the workflow window's 진행 알림 picker.
      const opts = list.map((d) => [d.name, d.name === "default" ? (channel === "telegram" ? "나에게 (기본)" : "기본 채널") : `#${d.name}`]);
      if (cur && !list.some((d) => d.name === cur)) opts.push([cur, `${cur} · 설정 › 메신저에 없어요`]);
      const sel = deps.select(opts, n.config[key]);
      sel.setAttribute("aria-label", label);
      sel.addEventListener("change", () => { n.config[key] = sel.value; deps.touch(); });
      body.replaceChildren(sel);
    });
    return f;
  }

  // 시작 노드 팝업 (1033:66756): how the workflow starts. It edits a draft of the workflow's trigger that 저장 keeps.
  function triggerFields(box, n) {
    const tr = deps.trigDraft(n);
    // The start card's title follows the way it starts while it still has an automatic one.
    const autoTitle = (t) => (t.kind === "schedule" ? deps.cronText(t.cron) : t.kind === "webhook" ? "웹훅으로 시작" : "직접 부르기");
    const retitle = (was) => { if (n.type === "start" && (!n.title || n.title === autoTitle(was) || n.title === "시작")) { n.title = autoTitle(tr); deps.syncCard(n); } };
    box.append(group("시작", deps.segment([["", "직접 부름"], ["schedule", "정한 요일·시간"], ["webhook", "웹훅"]], tr.kind, (v) => { const was = { ...tr }; tr.kind = v; if (v === "schedule" && !deps.parseCron(tr.cron)) tr.cron = deps.DEFAULT_CRON; retitle(was); deps.touch(); deps.redraw(); })));
    if (tr.kind === "schedule") {
      const f = deps.el("div", "field", "언제");
      f.append(deps.schedulePicker(tr.cron, (v) => { const was = { ...tr }; tr.cron = v; if (n.type === "schedule") n.config.cron = v; retitle(was); deps.touch(); }));
      box.append(f);
    }
    if (tr.kind === "webhook") {
      const label = deps.input(tr.label, { maxLength: 60, placeholder: "GitHub PR 열림" });
      label.addEventListener("input", () => { tr.label = label.value; if (n.type === "webhook") n.config.note = label.value; deps.touch(); });
      box.append(deps.field("웹훅 메모", label));
      const hook = deps.el("div", "field", "주소와 토큰");
      hook.append(deps.ed.rec.crew.trigger?.kind === "webhook" ? deps.hookPanel(deps.ed.rec) : deps.el("small", "dim", "저장하고 나면 여기서 주소와 토큰을 볼 수 있어요."));
      box.append(hook);
    }
    if (tr.kind) box.append(deps.el("p", "meta", "저장하면 바로 켜져요. 목록의 스위치로 끌 수 있어요."));
    else box.append(deps.el("p", "meta", `코딩 에이전트 채팅에 ${deps.alias(deps.ed.rec.crew)}를 치거나 실행을 눌렀을 때 시작해요.`));
  }

  const nodeKind = (n) => (n.type === "team" ? "회의" : deps.HUMAN_KIND[deps.NODES[n.type]?.group] || "노드");

  // AI 작업 · 전문가 불러옴 (1436:8483)
  const nodeLine = (n) => (n.type === "ai" && deps.byId(n.config?.from_expert) ? "AI 작업 · 전문가 불러옴" : deps.IS_AI(n.type) || n.type === "team" ? nodeKind(n) : `${nodeKind(n)} · ${deps.NODES[n.type]?.sub || ""}`);

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
    // A 회의 node saves its meeting (participants·rounds·ending) on 적용; the dialog stays open if that fails.
    left.append(deps.button("적용", "primary", null, async () => { if (n.type === "team" && !(await saveMeeting(n))) return; keep = true; dlg.close(); }), deps.button("취소", "ghost", null, () => dlg.close()));
    const asExpert = deps.button("전문가로 저장", "", null, () => saveAsExpert(n));
    right.append(asExpert);
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
      const ex = deps.byId(n.type === "expert" ? n.config?.expert : n.config?.from_expert || n.config?.persona);
      lead.replaceChildren(ex ? deps.avatarMark(ex.crew) : n.type === "team" ? teamMark(deps.byId(n.config?.team)) : deps.icon(deps.NODES[n.type]?.icon || "stack"));
      lead.classList.toggle("plain", !ex && n.type !== "team");
      h.textContent = deps.nodeTitle(n);
      sub.textContent = nodeLine(n);
      asExpert.hidden = n.type !== "ai"; // follows the kind switch inside the window
    };
    deps.redraw = () => { const restore = globalThis.ArkismKeep.view(body, body); draw(); restore(); deps.render(false); keepDlg(); };
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
        ["@이름", x.handle ? deps.alias(x) : "저장할 때 정해요"],
        ["모델", x.model || "자동"],
        // the start node's unsaved choice first, then what is saved
        ["언제 시작", deps.ed.trigger ? (deps.ed.trigger.kind ? deps.triggerText({ trigger: deps.ed.trigger }) : "직접 부르기만") : x.trigger ? `${deps.triggerText(x)}${x.enabled ? "" : " · 꺼짐"}` : "직접 부르기만"],
        ...(backs.length || x.limits ? [["멈추는 조건", [backs.length ? `되돌아가기 ${Math.max(...backs.map((e) => e.max))}번` : "", x.limits?.max_steps ? `단계 ${x.limits.max_steps}번` : "", x.limits?.max_minutes ? `${x.limits.max_minutes}분` : ""].filter(Boolean).join(" · ")]] : [])
      ]), deps.button("워크플로우 설정", "", "gear-six", () => (deps.ed.rec.id ? deps.openSettings(deps.ed.rec, null, true) : deps.saveGraph())), deps.el("i", "wf-rule"));
      // 처음이라면 (1511:5786): only while the graph is the untouched 새 워크플로우 one; any change brings back the usual help.
      if (deps.isFresh(deps.ed.graph)) {
        const steps = deps.el("ol", "g-first-steps");
        for (const t of ["「AI 작업」 노드에 무엇을 할지 한 줄 적어요.", "실행을 누르면 이름만 정하고 바로 돌려 봐요.", "저장하면 @이름으로 부를 수 있어요."]) steps.append(deps.el("li", "", t));
        box.append(deps.el("h3", "", "처음이라면"), steps, deps.el("p", "dim", "만들어 둔 전문가가 있으면 노드에서 「전문가 불러오기」로 골라요. 잘 되는 노드는 전문가로 저장해 다시 써요."),
          deps.el("p", "dim", "노드를 더하려면 아래 「+ 노드 추가」나 출력 점을 끌어요. 회의도 노드 종류에서 골라요."));
      } else box.append(deps.el("h3", "", "노드 설정"), deps.el("p", "dim", "노드를 누르면 여기서 고쳐요. 출력 점을 끌어 다음 노드에 이어요. 앞 노드로 이으면 되돌아가는 선이 돼요. 선을 누르면 선 설정이 열려요. 휠로 확대·축소하고, 휠 클릭이나 스페이스를 누른 채 끌면 화면이 움직여요."));
      const g = deps.ed.graph;
      const backCount = g.edges.filter(deps.isBack).length;
      box.append(deps.kv([["노드", `${g.nodes.length}개`], ["AI 노드", `${deps.aiNodes(g).length}개`], ...(backCount ? [["되돌아가는 선", `${backCount}개`]] : [])]));
      return box; // 구조 검토 lives in the top bar only
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
    wrap.append(card);
    if (chips.childElementCount) wrap.append(chips);
    return wrap;
  }

  // 이 노드를 전문가로 저장 (1436:8483): a node that works well becomes an expert, loaded into other workflows and called by @이름.
  // Its model·effort·skills go along; the node then shows it as its loaded expert.
  function saveAsExpert(n) {
    const c = n.config;
    const dlg = deps.el("dialog", "crew-dialog ex-skill-dlg");
    const head = deps.el("div", "dialog-head");
    head.append(deps.el("h2", "", "전문가로 저장"));
    const body = deps.el("div", "dialog-body");
    const title = deps.nodeTitle(n) === deps.NODES.ai.label ? "" : deps.nodeTitle(n);
    const name = deps.input(title, { maxLength: 60, placeholder: "예: 릴리스 매니저" });
    const handle = deps.input(title.replace(/\s+/g, "").slice(0, 40), { maxLength: 40, pattern: "[\\p{L}\\p{N}_\\-]{1,40}", placeholder: "띄어쓰기 없이" });
    name.addEventListener("input", () => { if (!handle.dataset.touched) handle.value = name.value.replace(/\s+/g, "").slice(0, 40); });
    handle.addEventListener("input", () => { handle.dataset.touched = handle.value ? "1" : ""; });
    const ins = deps.input(c.instructions || c.goal || "", { tag: "textarea", rows: 4, maxLength: 8000, placeholder: "너는 ○○다. …한다." });
    body.append(deps.field("이름", name), deps.field("@이름", handle), deps.field("지시", ins), deps.el("p", "meta", "모델·추론·스킬은 이 노드 그대로 넣어요. 다른 워크플로우에서 「전문가 불러오기」로 쓰고 @이름으로 불러요."));
    const foot = deps.el("div", "dialog-foot");
    foot.append(deps.button("취소", "ghost", null, () => dlg.close()), deps.button("저장", "primary", null, async () => {
      const nm = name.value.trim(), h = handle.value.trim().replace(/^@/, "");
      if (!nm || !/^[\p{L}\p{N}_-]{1,40}$/u.test(h) || !ins.value.trim()) { deps.notice("이름·@이름·지시를 채워 주세요."); return; }
      if (deps.agents().some((r) => r.crew.handle === h) || deps.teams().some((r) => r.crew.handle === h)) { deps.notice(`@${h}은 이미 있어요. 다른 이름을 써 주세요.`); return; }
      const auto = !c.model || c.model === "auto";
      const crew = deps.workerOut({ ...deps.blankAgent(), name: nm, handle: h, instructions: ins.value, model_mode: auto ? "auto" : "fixed", model: auto ? "" : c.model,
        effort: c.effort || "medium", skills: (c.skills || "").split(/[,\n]/).map((x) => x.trim()).filter(Boolean) });
      try {
        const saved = await deps.save(null, crew);
        deps.state.data.crew.push(saved);
        dlg.close();
        c.from_expert = saved.id;
        if (!title) n.title = nm;
        deps.touch();
        deps.redraw();
        deps.notice(`@${h} 전문가로 저장했어요. 다른 워크플로우에서 불러올 수 있어요.`);
      } catch (e) { deps.notice(deps.errorText(e)); }
    }));
    dlg.append(head, body, foot);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
    name.focus();
  }

  // 회의 노드 (1436:8483): the old team window's 참여자 · 진행 방식 · 최대 라운드 · 결론 sit in the node. They edit the team the node
  // runs (a new one for a new 회의 node), kept as a draft per node until 적용 saves it.
  const ROLES = [["moderator", "진행자"], ["worker", "작업자"], ["reviewer", "리뷰어"], ["scribe", "정리 담당"]];
  const meetings = new WeakMap();
  function meetingDraft(n) {
    let m = meetings.get(n);
    if (!m) {
      const rec = deps.byId(n.config.team);
      const d = structuredClone(rec?.crew || { type: "team", name: "", handle: "", role: "", members: [], mode: "round_robin", context: [], templates: [] });
      d.members = [...(d.members || [])].sort((a, b) => (a.order || 0) - (b.order || 0));
      d.ending = { max_rounds: 4, token_cap: 100000, end_on_all_approved: true, end_on_moderator: true, ...(d.ending || {}) };
      m = { rec: rec || null, d, changed: false };
      meetings.set(n, m);
    }
    return m;
  }

  function meetingFields(L, n) {
    const m = meetingDraft(n), d = m.d;
    const changed = (again) => { m.changed = true; deps.touch(); if (again) deps.redraw(); };
    const renumber = () => d.members.forEach((x, i) => { x.order = i + 1; });
    const list = deps.el("div", "g-workers");
    d.members.forEach((mem, i) => {
      const r = deps.el("div", "g-worker expert");
      const a = deps.byId(mem.agent_id);
      const lead = deps.el("span", "node-pick-lead");
      lead.append(a ? deps.avatarMark(a.crew) : deps.icon("robot"));
      const who = deps.el("span", "g-member-name", a?.crew.name || mem.agent_id);
      who.setAttribute("data-no-i18n", "");
      const role = deps.select(ROLES, mem.role || "worker");
      role.setAttribute("aria-label", "역할");
      role.addEventListener("change", () => { mem.role = role.value; changed(); });
      const del = deps.el("button", "icon-btn plain");
      del.type = "button";
      del.setAttribute("aria-label", "참여자 빼기");
      del.append(deps.icon("x"));
      del.addEventListener("click", () => { d.members.splice(i, 1); renumber(); changed(true); });
      r.append(lead, who, role, del);
      list.append(r);
    });
    const free = deps.agents().filter((a) => !d.members.some((x) => x.agent_id === a.id));
    const add = deps.select([["", "+ 참여자 넣기"], ...free.map((a) => [a.id, a.crew.name])], "");
    for (const o of add.options) if (o.value) o.setAttribute("data-no-i18n", "");
    add.setAttribute("aria-label", "참여자 넣기");
    add.addEventListener("change", () => {
      if (!add.value) return;
      d.members.push({ agent_id: add.value, order: d.members.length + 1, role: d.members.length ? "worker" : "moderator", focus: "" });
      changed(true);
    });
    list.append(add);
    L.append(group("참여자", list, deps.agents().length ? "위에서부터 말해요. 처음 넣은 사람이 진행자예요." : "참여자는 전문가예요. 먼저 전문가를 만들어 주세요."));
    L.append(group("진행 방식", deps.segment(Object.entries(deps.MODES), d.mode || "round_robin", (v) => { d.mode = v; changed(true); })));
    const rounds = deps.input(d.ending.max_rounds, { type: "number", min: 1, max: 10 });
    rounds.addEventListener("input", () => { d.ending.max_rounds = Math.max(1, Math.min(10, Number(rounds.value) || 1)); changed(); });
    const ends = deps.el("div", "g-ends");
    for (const [k, label] of [["end_on_all_approved", "리뷰어가 모두 승인하면 끝"], ["end_on_moderator", "진행자가 끝내면 끝"]]) {
      const cb = deps.el("input");
      cb.type = "checkbox";
      cb.checked = !!d.ending[k];
      cb.addEventListener("change", () => { d.ending[k] = cb.checked; changed(); });
      const lab = deps.el("label", "check-line");
      lab.append(cb, label);
      ends.append(lab);
    }
    const row = deps.el("div", "scope-fields");
    row.append(deps.field("최대 라운드", rounds, "1~10"), group("결론", ends));
    L.append(row);
  }

  // 적용 of a 회의 node: save its team (new or changed), then point the node at it. False keeps the window open.
  async function saveMeeting(n) {
    const m = meetings.get(n);
    if (!m || (!m.changed && n.config.team)) return true;
    const d = m.d;
    if (!d.members.length) { deps.notice("회의에 참여할 전문가를 한 명 이상 넣어 주세요."); return false; }
    if (!d.name) d.name = deps.nodeTitle(n) !== deps.NODES.team.label ? deps.nodeTitle(n) : `${deps.ed.rec.crew.name || "새 워크플로우"} 회의`;
    if (!d.handle) {
      const base = d.name.replace(/[^\p{L}\p{N}_-]/gu, "").slice(0, 36) || "회의";
      let h = base, i = 2;
      while (deps.items().some((r) => r.crew.handle === h)) h = `${base}${i++}`;
      d.handle = h;
    }
    try {
      const saved = await deps.save(m.rec, d);
      if (m.rec) Object.assign(m.rec, saved); else deps.state.data.crew.push(saved);
      m.rec = deps.byId(saved.id) || saved;
      m.d = { ...structuredClone(saved.crew), members: d.members, ending: d.ending };
      m.changed = false;
      if (n.config.team !== saved.id) { n.config.team = saved.id; deps.touch(); }
      return true;
    } catch (e) { deps.notice(deps.errorText(e)); return false; }
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
    // 1436:8483: the kind choice is only AI 작업 / 회의. A saved expert or one-shot node shows as AI 작업 and keeps its type until
    // the person picks 회의, so stored workflows run as they were.
    L.append(group("노드 종류", deps.segment([["ai", "AI 작업"], ["team", "회의"]], n.type === "team" ? "team" : "ai", (k) => {
      if (k === (n.type === "team" ? "team" : "ai")) return;
      const was = n.type, text = c.goal || c.prompt || c.topic || "{{input}}";
      n.type = k;
      if (k === "team") c.topic ||= text;
      else { c.goal ||= c.topic || ""; c.access ||= "read"; c.model ||= "auto"; }
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
      meetingFields(L, n);
    } else {
      const cur = deps.byId(c.from_expert);
      if (deps.agents().length) L.append(pickCard("불러온 전문가 · 안 골라도 돼요", cur, deps.agents(), (r) => {
        named(r, cur?.crew.name);
        Object.assign(c, deps.expertConfig(r));
        deps.touch();
        deps.redraw();
      }));
      p = cur;
      if (!cur) {
        const row = deps.el("div", "scope-fields");
        row.append(deps.field("모델", bind(deps.modelInput(c.model, deps.ed.rec.crew.model || "auto"), "model", true), "auto면 할 일에 맞는 모델을 골라요"),
          deps.field("생각하는 깊이", bind(deps.select(deps.EFFORTS, c.effort || "medium"), "effort")));
        L.append(row);
      }
      // a persona saved before #481 still shows what it says; new nodes put that text in 지시
      const old = deps.byId(c.persona);
      if (old) { const q = deps.el("div", "quote"); q.append(deps.el("div", "", old.crew.instructions || "")); L.append(deps.field("페르소나", q)); p ||= old; }
    }
    // 무엇을 하나
    L.append(deps.el("i", "wf-rule"), deps.el("h3", "", "무엇을 하나"));
    const title = deps.input(n.title, { maxLength: 80 });
    title.addEventListener("input", () => { n.title = title.value; deps.touch(); });
    L.append(deps.field("노드 이름", title));
    // The engine adds the previous step's result after the 할 일 / 질문 (crewrun taskText); {{input}} only places it.
    const slot = "앞 단계 결과는 알아서 뒤에 붙여요";
    if (n.type === "team") L.append(deps.field("회의 주제", bind(deps.input(c.topic, { tag: "textarea", rows: 3, maxLength: 4000 }), "topic", true), "{{input}}이라고 쓴 자리에 앞 단계 결과가 들어가요"));
    else if (n.type === "ai_once") L.append(deps.field("질문", bind(deps.input(c.prompt, { tag: "textarea", rows: 4, maxLength: 8000 }), "prompt", true), slot));
    else L.append(deps.field("할 일", bind(deps.input(c.goal, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "하위 질문마다 출처 3개 이상 모아 줘." }), "goal", true), slot));
    if (n.type !== "team") L.append(deps.field(p ? "더할 지시" : "지시", bind(deps.input(c.instructions, { tag: "textarea", rows: 3, maxLength: 8000, placeholder: "너는 ○○다. …한다." }), "instructions")), skillsField(c));
    if (n.type === "expert" || n.type === "ai" || n.type === "team") {
      // 실행 범위
      R.append(deps.el("h3", "", "실행 범위"),
        deps.field("허용 범위", bind(deps.input(c.fence, { maxLength: 4000, placeholder: "웹 검색과 자료 읽기만 허용" }), "fence")),
        deps.field("완료 기준", bind(deps.input(c.done, { maxLength: 1000, placeholder: "질문마다 출처 3개와 요약" }), "done")),
        group("파일 바꾸기", deps.segment([["read", "안 함 · 읽기만"], ["write", "함 · 한 번에 하나씩"]], c.access || "read", (v) => { c.access = v; deps.touch(); deps.redraw(); })),
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
    if (!on) { R.append(deps.el("p", "meta", "켜면 이 노드가 일을 나눠 다른 전문가에게 맡기고 결과를 합쳐요. 하위 질문이 몇 개일지 미리 모를 때 써요.")); return; }
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
        // One input is "앞 단계 결과"; the {{input}} / in.<id> spelling a goal or code uses stays as the tooltip.
        const tag = ins.length > 1 || n.type === "merge" ? `in.${e.from}` : "{{input}}";
        const k = deps.el("span", ins.length > 1 || n.type === "merge" ? "mono" : "", ins.length > 1 || n.type === "merge" ? tag : "앞 단계 결과");
        k.title = tag;
        r.append(k, deps.el("span", "", `← ${from ? deps.nodeTitle(from) : e.from}${e.port ? ` (${e.port})` : ""}`));
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
    get renderSimple() { return renderSimple; },
    get saveAsExpert() { return saveAsExpert; }
  };
}
