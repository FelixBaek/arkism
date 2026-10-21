// MEMORY › 개요 windows (Figma 573:40303 … 573:50266 stage windows, 580:* wide windows, 592/764 improvements).
// open(key): the stage window drawn over the overview; wide(kind): the large window (review · active · sessions · usage · map).
// Everything shown comes from data the app already loaded or a documented read API; a field the server does not have is left out.
window.ArkismOverviewWindows = (ctx, env) => {
  "use strict";
  const { state, el, button, icon, request, query, ago, go, render } = ctx;
  const HOUR = 3600000;
  const num = (n) => Number(n || 0).toLocaleString("ko-KR");
  const loc = () => globalThis.ArkismI18n?.locale() || "ko-KR"; // dates follow the screen language
  const time = (v) => { const t = new Date(v || 0).getTime(); return Number.isNaN(t) ? 0 : t; };
  const KIND = { user: "선호", decision: "결정", lesson: "교훈", procedural: "작업 규칙", episodic: "사건", fact: "사실" };
  const kind = (k) => KIND[k] || k || "기억";
  // one colour per kind (tags here and the 종류별 map); the label decides, so a row helper given only text still colours it
  const KIND_COLOR = { user: "#c48ef0", decision: "#7c9cff", lesson: "#e8a65d", procedural: "#5fc6a8", episodic: "#6cc3e0", fact: "#b9c56b" };
  const KIND_BY_LABEL = Object.fromEntries(Object.entries(KIND).map(([k, l]) => [l, k]));
  const DREAM_TAGS = new Set(["합치기", "고치기", "정리"]);
  function tagEl(text, cls = "") {
    const t = el("span", `ov-win-tag ${cls}`.trim(), text), k = KIND_BY_LABEL[text];
    if (k) { t.classList.add("ov-tag-k"); t.style.setProperty("--tag", KIND_COLOR[k]); } else if (!cls && DREAM_TAGS.has(text)) t.classList.add("ov-tag-p");
    return t;
  }
  const openSession = (ev, quote) => { const [, sid] = ev.split(":"); dlg?.close(); window.dispatchEvent(new CustomEvent("arkism:open-session", { detail: { session_id: sid, quote } })); };
  const D = () => state.data;
  const tr = (x) => window.ArkismI18n?.translate(x) || x;
  const usage = (id) => state.usage?.get(id);
  const memOf = (id) => D().memories.find((m) => m.id === id) || ctx.maintenanceRecords?.get(id); // proposals point past the loaded first page too
  const memText = (id) => memOf(id)?.text || "(지워졌거나 아직 못 읽은 기억)";
  const tag = (m, p) => (m.tags || []).find((t) => t.startsWith(p))?.slice(p.length) || "";
  const clip = (t, n = 80) => (t.length > n ? `${t.slice(0, n)}…` : t);
  const waitFor = (v) => { const d = Math.floor((Date.now() - time(v)) / (24 * HOUR)); return d >= 1 ? `${d}일 대기` : ago(v).replace(/ 전$/, " 대기"); }; // ago() turns old ones into a date
  const REACH_WHERE = { personal: "나만 (개인)", general: "모든 프로젝트 (범용)", project: "이 프로젝트만" };
  // 승인 대기 list kinds (Figma 592:51366): a candidate that repeats or contradicts an existing memory is 헷갈림
  const confusing = (c, merges) => !!(c.conflict_id || c.status === "conflicted" || merges?.has(c.id) || c.classification?.dup_hint);
  const VERDICT = { keep: "그대로 저장 추천", fix: "고쳐서 저장 추천", merge: "기존 기억에 합치기 추천", discard: "버리기 추천" };
  const SEMANTIC = { same_meaning: ["뜻이 원문과 같아요", "뜻이 원문과 달라요"], missing_content: ["빠진 내용이 없어요", "빠진 내용이 있어요"], no_additions: ["원문에 없는 말을 더하지 않았어요", "원문에 없는 말을 더했어요"] };

  // Share the app's loaded-window refresh; review must not walk every memory.
  async function reload() {
    try { await ctx.reloadMemories(); }
    catch { /* preserve the visible lists and let the next refresh retry */ }
    render(false);
  }

  let dlg = null;
  // beside: a non-modal window over the content area only, so the left menu stays usable (a menu click closes it)
  function modal(cls, beside = false) {
    dlg?.close();
    const d = el("dialog", `crew-dialog ov-win ${cls || ""}`), rail = document.querySelector(".sidebar"), shut = () => d.close();
    d.addEventListener("close", () => { d.remove(); rail?.removeEventListener("click", shut); if (dlg === d) dlg = null; });
    document.body.append(d); dlg = d;
    if (!beside) { d.showModal(); return d; }
    d.show(); rail?.addEventListener("click", shut);
    d.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && !ev.defaultPrevented) { ev.preventDefault(); d.close(); } });
    return d;
  }
  // how a separate window reopens this one when it is dragged out of the app (popup-drag.js; overview.js listens)
  const recipe = (d, detail) => { if (d) Object.assign(d.dataset, { popout: JSON.stringify({ event: "arkism:open-overview-window", detail }), popoutTitle: tr("기억 처리") }); };
  const row = (tagText, title, meta, right, btns = []) => {
    const r = el("div", "ov-win-item"), t = el("div", "ov-win-item-t"), left = el("div", "ov-win-item-l");
    if (tagText) t.append(tagEl(tagText));
    t.append(el("span", "ov-win-item-title", title)); left.append(t);
    if (meta) left.append(el("small", "", meta));
    const side = el("div", "ov-win-item-r");
    if (right) side.append(el("span", "", right));
    side.append(...btns); r.append(left, side);
    return r;
  };

  // Each wide window owns its query/cursor. Closing it or changing connection
  // invalidates publication; "more" is the only operation that opens another page.
  function pagedWindow(d, path, filters, paint, sessions = false) {
    const generation = state.connection.generation;
    const pages = ctx.remoteCollection((body, page) => sessions
      ? request(`${path}?${query({ ...body, limit: page.limit, ...(page.cursor ? { cursor: page.cursor } : {}) })}`)
      : request(path, { method: "POST", body: JSON.stringify({ scope: state.scope, ...body, limit: page.limit, ...(page.cursor ? { cursor: page.cursor } : {}) }) }));
    let active = "", ticket = 0;
    async function load(more = false) {
      const epoch = ++ticket;
      try { await pages.load(filters(), more); } catch { /* snapshot retains the rows and retry state */ }
      if (epoch === ticket && d.isConnected && generation === state.connection.generation) paint();
    }
    function snapshot() {
      const body = filters(), key = JSON.stringify(body);
      if (key !== active) { active = key; void load(); }
      return pages.snapshot(body);
    }
    function footer(host) {
      const snap = snapshot();
      host.append(el("p", "ov-wide-foot-note", snap.loading ? "불러오는 중…" : snap.error ? `불러오지 못했어요 (${snap.error}).` : `${snap.items.length} / ${snap.total ?? 0} · 전체에서 검색·정렬`));
      if (snap.cursor || snap.error) { const b = button(snap.error ? "다시 시도" : "더 보기", "small", null, () => load(true)); b.disabled = snap.loading; host.append(b); }
    }
    d.addEventListener("close", () => { ticket++; pages.reset(); });
    return { snapshot, footer };
  }

  // ------------------------------------------------------------ in-place review (the same API the 기억 screen calls)
  const reviewing = new Set();
  async function decide(c, action, extra = {}, quiet = false) { // quiet: the caller reloads once after a batch
    if (reviewing.has(c.id)) return null;
    reviewing.add(c.id);
    try {
      const payload = window.ArkismMemoryEvaluation.reviewPayload(c.id, { scope: state.scope, expected_revision: c.revision, action, ...extra });
      await request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/review`, { method: "POST", body: JSON.stringify(payload) });
      D().candidates = D().candidates.filter((x) => x.id !== c.id); D().conflicted = (D().conflicted || []).filter((x) => x.id !== c.id);
      if (!quiet) await reload();
      return { ok: true };
    } catch (e) { if (e.message === "revision_conflict") await reload(); return { error: e.message === "revision_conflict" ? "이미 처리됐거나 다른 곳에서 바뀌었어요. 개요를 새로 고쳐 주세요." : `처리하지 못했어요: ${e.message}` }; }
    finally { reviewing.delete(c.id); }
  }
  function askReason(title, onOk) {
    const d = el("dialog", "crew-dialog ov-ask"), body = el("div", "dialog-body"), why = el("textarea"); why.rows = 3; why.placeholder = "버리는 이유 (버릴 때만 필요해요)"; why.setAttribute("aria-label", "버리는 이유");
    const err = el("p", "meta", ""), foot = el("div", "ov-win-foot");
    foot.append(button("취소", "ghost small", null, () => d.close()), button("버리기", "primary small", null, () => { if (!why.value.trim()) { err.textContent = "버리는 이유를 적어 주세요."; why.focus(); return; } d.close(); onOk(why.value.trim()); }));
    body.append(el("h2", "", title), why, err); d.append(body, foot); d.addEventListener("close", () => d.remove()); document.body.append(d); d.showModal(); why.focus();
  }
  const waiting = () => [...D().candidates, ...(D().conflicted || [])].sort((a, b) => time(a.created_at) - time(b.created_at));

  // ------------------------------------------------------------ stage window
  const SPEC = {
    collect: ["에이전트 대화를 세션 단위로 모아 저장해요", "Claude Code · Codex · BULTUK 등", "대화가 끝날 때마다", "자동 (수집 훅)", "extract"],
    extract: ["저장된 대화에서 교훈·결정 후보를 뽑아요", "대화 저장", "대화가 끝난 뒤", "AI (서버 Claude)", "classify"],
    classify: ["후보에 위험도와 중복 힌트를 붙여요", "분석", "후보가 생기면 바로", "AI (분류만, 승인 안 함)", "review"],
    review: ["사람이 승인해야 쓰는 기억이 돼요", "분류 · 드리밍", "언제든", "사람만", "active"],
    recall: ["질문과 관련 있는 기억을 찾아 답에 넣어요", "쓰는 중인 기억", "에이전트가 질문할 때마다", "자동 (검색)", "trace"],
    trace: ["어떤 기억이 어느 세션에 들어갔는지 남겨요", "불러오기", "불러올 때마다", "자동", "judge"],
    judge: ["넣은 기억이 실제 답에 쓰였는지 판정해요", "사용 기록", "세션이 끝난 뒤", "AI 판정 + 사람 확인", "dream"],
    dream: ["겹치거나 낡은 기억을 찾아 정리를 제안해요", "쓰는 중인 기억 · 검증 결과", "주기적으로", "AI 제안 → 사람 승인", "review"],
    active: ["승인된 기억이에요. 불러오기가 여기서 찾아요", "승인 대기", "승인되면 바로 추가", "사람이 승인한 것만", "recall"]
  };
  const LABEL = { active: "쓰는 중인 기억" };
  const LIST_TITLE = { collect: "최근 저장된 대화", extract: "최근 추출된 후보·기억", classify: "분류 기다리는 후보", review: "승인 기다리는 후보", recall: "최근 불러온 기억 (질문 내용은 저장하지 않아요)",
    trace: "기억별 넣음·쓰임", judge: "확인 기다리는 기억", dream: "정리 제안", active: "많이 쓰인 순" };
  const FULL = { collect: ["세션 기록 전체 보기 ›", () => wide("sessions")], extract: ["세션 기록 보기 ›", () => wide("sessions")], classify: ["승인 대기 열기 ›", () => wide("review")], review: ["넓게 보기 ›", () => wide("review")],
    recall: ["사용량 보기 ›", () => wide("usage")], trace: ["사용량 보기 ›", () => wide("usage")], judge: ["정리할 기억 전체 보기 ›", () => wide("active", "idle")], dream: ["정리 제안 전체 보기 ›", () => wide("review", null, "dream")], active: ["넓게 보기 ›", () => wide("active")] };

  function stageRows(key, box, expanded) {
    const sessions = env.sessions(), w = D().tools?.memory_use, cap = expanded ? (+expanded > 1 ? +expanded : 60) : 6, out = []; // expanded: true (60 rows) or a row count
    const rows = {
      collect: () => sessions.slice(0, cap).map((x) => row("", [x.agent, x.project].filter(Boolean).join(" · ") || x.title || "세션", `메시지 ${num(x.messages)}개 · ${ago(x.ended_at || x.started_at)}`, "", [button("열기", "small", null, () => openSession(`:${x.session_id}`, ""))])),
      extract: () => [...waiting(), ...D().memories].sort((a, b) => time(b.created_at) - time(a.created_at)).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), `${tag(m, "event_id:") ? "대화 저장 · " : ""}${ago(m.created_at)}`, "")),
      classify: () => waiting().filter((m) => !m.classification).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), ago(m.created_at), "", [button("승인 대기에서 보기", "small", null, () => wide("review", m.id))])),
      review: () => waiting().slice(0, cap).map((m) => reviewRow(m, box)),
      recall: () => [...(state.usage?.entries() || [])].filter(([, u]) => u.last).sort((a, b) => time(b[1].last) - time(a[1].last)).slice(0, cap).map(([id, u]) => row("", clip(memText(id)), ago(u.last), `7일 ${num(u.d7 || 0)}회`)),
      trace: () => (w?.items || []).slice().sort((a, b) => b.used - a.used || b.injected - a.injected).slice(0, cap).map((u) => row("", clip(memText(u.id)), `넣음 ${num(u.injected)}번`, `쓰임 ${num(u.used)}번`)),
      judge: () => D().memories.map((m) => [m, w?.items?.find((x) => x.id === m.id)]).filter(([, u]) => u && u.injected >= 3 && !u.used).slice(0, cap).map(([m, u]) => row("안 쓰임", clip(m.text), `${w.days}일 동안 ${num(u.injected)}번 넣었는데 한 번도 안 쓰였어요`, "", [button("정리하러 가기", "small", null, () => wide("active", "idle", m.id))])),
      dream: () => (D().maintenance || []).slice(0, cap).map((p) => {
        const t = p.maintenance || {}, ev = t.evidence;
        if (t.memory) return row("고치기", clip(memText(t.memory.id)), ev ? (t.type === "lesson_often_failing" ? `실패가 잦은 교훈 · 통과율 ${Math.round((ev.pass_rate || 0) * 100)}% (${num(ev.passed)}/${num(ev.samples)})` : `자주 어기는 규칙 · 통과율 ${Math.round((ev.pass_rate || 0) * 100)}% (${num(ev.passed)}/${num(ev.samples)})`) : t.type === "lesson_often_failing" ? "실패가 잦은 교훈" : "자주 어기는 규칙", "승인 대기", []);
        return row({ duplicate: "합치기", contradiction: "고치기" }[t.type] || "정리", clip(memText(t.keep?.id)), `→ ${clip(memText(t.retire?.id), 40)}`, "승인 대기", []);
      }),
      active: () => D().memories.slice().sort((a, b) => (usage(b.id)?.d7 || 0) - (usage(a.id)?.d7 || 0) || time(b.created_at) - time(a.created_at)).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), tag(m, "repo:") || ago(m.created_at), usage(m.id) ? `7일 ${num(usage(m.id).d7 || 0)}번 쓰임` : "", []))
    };
    return (rows[key]?.() || out);
  }
  function reviewRow(c, box) {
    // the same tags and reason line as the wide window's row, so both show one candidate alike
    const dupText = c.classification?.dup_hint && D().memories.find((m) => m.id === c.classification.dup_hint)?.text;
    const r = row(kind(c.kind), clip(c.text), [dupText ? `“${clip(dupText, 24)}”와 같은 뜻` : c.conflict_id ? "충돌 중" : c.classification?.risk_reasons?.[0] || "", ago(c.created_at)].filter(Boolean).join(" · "), "");
    if (confusing(c)) r.querySelector(".ov-win-item-t").prepend(tagEl("헷갈림", "ov-tag-r"));
    const side = r.lastChild, note = el("small", "ov-win-err", "");
    const ok = button("승인", "primary small", null, async () => { ok.disabled = no.disabled = true; const res = await decide(c, "approve"); if (res?.error) { note.textContent = res.error; ok.disabled = no.disabled = false; } else { r.remove(); } });
    const no = button("버리기", "small", null, () => askReason("후보를 버릴까요?", async (reason) => { ok.disabled = no.disabled = true; const res = await decide(c, "reject", { reason }); if (res?.error) { note.textContent = res.error; ok.disabled = no.disabled = false; } else r.remove(); }));
    side.append(ok, no); r.firstChild.append(note);
    return r;
  }

  // 불러오기 (592:53571): the newest context receipts from GET /v1/recalls. A receipt keeps what was handed out with its score and
  // lanes, not what fell below the cut or how long it took, so those parts of the design are not drawn.
  const LANE = { exact: "정확", keyword: "키워드", vector: "벡터", relation: "관계" };
  function recallBox() {
    const box = el("div", "ov-recall-box"), host = el("div", "ov-detail-list");
    box.append(el("div", "ov-win-list-title", "최근 불러오기 · 점수와 고른 이유 (질문 내용은 저장하지 않아요)"), host);
    host.append(el("p", "ov-win-empty", "불러오는 중…"));
    request(`/v1/recalls?${query({})}`).then((r) => {
      const list = (r?.items || []).filter((x) => x.items?.length).slice(0, 6); host.replaceChildren();
      if (!list.length) { host.append(el("p", "ov-win-empty", "아직 불러온 기록이 없어요.")); return; }
      const count = {}; for (const x of list) for (const it of x.items) { const m = D().memories.find((y) => y.id === it.memory.id); if (m) count[m.kind] = (count[m.kind] || 0) + 1; }
      const kinds = el("div", "ov-kinds"); kinds.append(el("span", "", "한 번에 넣는 양")); for (const [k2, n] of Object.entries(count)) { const t = tagEl(kind(k2)); t.textContent += ` ${num(Math.round(n / list.length * 10) / 10)}`; kinds.append(t); } box.insertBefore(kinds, host);
      for (const x of list) {
        const c = el("div", "ov-recall"), h = el("div", "ov-recall-head"); h.append(el("strong", "", [x.requested_by, x.project_key].filter(Boolean).join(" · ")), el("span", "", `${ago(x.created_at)} · 넣음 ${num(x.items.length)}`)); c.append(h);
        for (const it of x.items) {
          const m = D().memories.find((y) => y.id === it.memory.id), r2 = el("div", "ov-recall-row");
          r2.append(el("b", "", it.score ? it.score.toFixed(2) : "고정"), ...(it.lanes || []).map((l) => el("span", "ov-lane", LANE[l] || l)), ...(m ? [tagEl(kind(m.kind))] : []), el("span", "t", clip(m?.text || it.memory.id, 60)));
          c.append(r2);
        }
        host.append(c);
      }
    }, () => { host.replaceChildren(el("p", "ov-win-empty", "불러오기 기록을 읽지 못했어요.")); });
    return box;
  }

  function open(key, expanded = false) {
    const list = env.stages(), i = list.findIndex((x) => x.key === key), s = i >= 0 ? list[i] : activeStage();
    const spec = SPEC[key];
    const d = modal(`ov-stage-win ov-t-${s.badge?.[1] || "n"} ov-win-${key}${expanded ? " ov-win-expanded" : ""}`); // 모두 보기: the window becomes the list
    recipe(d, { key, expanded });
    const nav = (n) => () => open(list[(i + n + list.length) % list.length].key);
    const head = el("div", "ov-win-head"), title = el("div", "ov-win-title");
    title.append(el("span", "ov-win-crumb", "기억 처리"), el("span", "ov-win-crumb", "›"), el("i", "ov-dot"), el("h2", "", LABEL[key] || s.label));
    if (s.badge?.[0]) title.append(el("span", "ov-win-badge", s.badge[0]));
    const step = el("div", "ov-win-step");
    if (i >= 0) { const p = button("‹", "ghost small", null, nav(-1)), n = button("›", "ghost small", null, nav(1)); p.setAttribute("aria-label", "이전 단계"); n.setAttribute("aria-label", "다음 단계"); step.append(p, el("span", "", `${i + 1} / ${list.length} 단계`), n); }
    step.append(button("닫기", "ghost small", null, () => d.close())); head.append(title, step);
    const body = el("div", "dialog-body ov-win-body"), tiles = el("div", "ov-win-tiles");
    for (const [v, l] of s.tiles) { const t = el("div", "ov-win-tile"); t.append(el("strong", "", v), el("span", "", l)); tiles.append(t); }
    const how = el("div", "ov-win-how"), cells = el("div", "ov-win-cells");
    how.append(el("p", "", ""), cells); how.firstChild.append(el("span", "", "이 단계는 "), el("strong", "", spec[0]));
    for (const [l, v] of [["받는 곳", spec[1]], ["언제", spec[2]], ["누가", spec[3]]]) { const c = el("div", "ov-win-cell"); c.append(el("small", "", l), el("strong", "", v)); cells.append(c); }
    const nx = el("button", "ov-win-cell ov-win-next"); nx.type = "button"; nx.append(el("small", "", "다음 단계"), el("strong", "", `${LABEL[spec[4]] || list.find((x) => x.key === spec[4])?.label} ›`));
    nx.addEventListener("click", () => (spec[4] === "active" ? open("active") : open(spec[4]))); cells.append(nx);
    body.append(tiles, how);
    if (s.counts) {
      const chart = el("div", "ov-win-chart"), max = Math.max(1, ...s.counts), bars = el("div", "ov-win-bars");
      chart.append(el("div", "ov-win-chart-title", "최근 24시간"));
      s.counts.forEach((n, k) => { const b = el("i", n ? (s.row?.[k] === "r" ? "r" : s.row?.[k] === "a" ? "a" : "") : "none"); b.style.height = `${6 + (n / max) * 54}px`; b.title = `${n}`; bars.append(b); });
      chart.append(bars, el("div", "ov-win-axis", "24시간 전 ··· 지금")); body.append(chart);
    }
    if (key === "extract" && s.badge?.[1] === "r") body.append(note("r", `${s.badge[0]}이에요`, "세션은 계속 들어오는데 새 후보가 만들어지지 않았어요. 서버의 분석 로그인·연결을 확인해 주세요."));
    if (key === "judge") body.append(note("a", "AI가 “실제로 쓰였는지” 판정했어요", "판정이 맞는지 확인하고, 틀림이 쌓이면 그 기억을 다시 봐요."));
    if (key === "dream") body.append(note("n", "드리밍 제안은 승인 대기에 [정리]로 같이 떠요", "바로 바뀌지 않아요. 사람이 고르면 반영되고, 지우는 일은 없어요."));
    if (key === "recall") body.append(recallBox());
    const rows = stageRows(key, body, expanded), total = expandTotal(key);
    const ul = el("div", "ov-win-list");
    const listHead = el("div", "ov-win-list-title", LIST_TITLE[key]);
    if (expanded) listHead.append(button("접기 ⌃", "ghost small", null, () => open(key, false)));
    else if (total && total > rows.length) listHead.append(button(`${num(total)}건 모두 보기 ›`, "ghost small", null, () => open(key, true)));
    if (key !== "recall" || rows.length) body.append(listHead); // 불러오기: 영수증 상자가 빈 목록을 대신해요
    if (!rows.length && key !== "recall") ul.append(el("p", "ov-win-empty", "지금 보여 줄 항목이 없어요."));
    ul.append(...rows); body.append(ul);
    const foot = el("div", "ov-win-foot");
    if (expanded) {
      ul.classList.add("ov-win-list-scroll");
      const left = el("span", "ov-wide-foot-note"), say = (n) => { left.textContent = n < total ? `${num(total)}건 중 ${num(n)}건 · 창 안에서 내리면 더 나와요` : `${num(total)}건 모두 보는 중`; };
      let cap = rows.length; say(cap);
      ul.addEventListener("scroll", () => { // 60 more each time the list reaches its end
        if (cap >= total || ul.scrollTop + ul.clientHeight < ul.scrollHeight - 40) return;
        cap += 60; const more = stageRows(key, body, cap); ul.replaceChildren(...more); say(more.length); if (more.length < cap) cap = total;
      });
      foot.append(left);
    }
    if (FULL[key]) foot.append(button(FULL[key][0], "ghost small", null, FULL[key][1]));
    d.append(head, body, foot);
  }
  // Figma 펼침 580:63994 분석 · 580:66175 분류 · 580:68368 불러오기 · 580:70537 검증 · 580:72743 드리밍; other stages open a wide window instead
  function expandTotal(key) {
    const w = D().tools?.memory_use;
    return { extract: waiting().length + D().memories.length, classify: waiting().filter((m) => !m.classification).length,
      recall: [...(state.usage?.values() || [])].filter((u) => u.last).length, judge: D().memories.filter((m) => { const u = w?.items?.find((x) => x.id === m.id); return u && u.injected >= 3 && !u.used; }).length,
      dream: (D().maintenance || []).length }[key] || 0;
  }
  const note = (tone, a, b) => { const n = el("div", `ov-win-note ov-win-note-${tone}`); n.append(el("strong", "", a), el("span", "", b)); return n; };
  function activeStage() {
    const mems = D().memories, week = mems.filter((m) => time(m.created_at) > Date.now() - 7 * 24 * HOUR).length;
    return { key: "active", label: "쓰는 중인 기억", badge: [`${num(mems.length)}개`, "n"], tiles: [[`${num(mems.length)}개`, "쓰는 중"], [`${num([...(state.usage?.values() || [])].filter((u) => u.d30 > 0 || u.d7 > 0).length)}개`, "최근 쓰임"], [`${num(week)}개`, "이번 주 새로"]] };
  }

  // ------------------------------------------------------------ wide windows
  function shell(titleText, badgeText, hint, back) {
    const d = modal("ov-wide-win");
    const head = el("div", "ov-win-head"), t = el("div", "ov-win-title");
    if (back) t.append(button("‹ 작게 보기", "ghost small", null, back));
    t.append(el("i", "ov-dot"), el("h2", "", titleText));
    if (badgeText) t.append(el("span", "ov-win-badge", badgeText));
    const r = el("div", "ov-win-step"); if (hint) r.append(el("span", "", hint)); const x = button("✕", "ghost small ov-wide-x", null, () => d.close()); x.setAttribute("aria-label", "닫기"); r.append(x);
    head.append(t, r); d.append(head);
    return d;
  }
  const chipBar = (items, cur, set) => { const bar = el("div", "ov-wide-chips"); for (const [k, l, n] of items) { const b = el("button", `ov-wide-chip${k === cur ? " on" : ""}`); b.type = "button"; b.append(document.createTextNode(l), ...(n == null ? [] : [el("small", "", ` ${num(n)}`)])); b.addEventListener("click", () => set(k)); bar.append(b); } requestAnimationFrame(() => bar.querySelector(".on")?.scrollIntoView({ block: "nearest", inline: "nearest" })); return bar; }; // the picked chip stays in view when the row scrolls
  function twoPane(d, bar, listBox, detail) {
    const body = el("div", "ov-wide-body"), left = el("div", "ov-wide-left"), right = el("div", "ov-wide-right");
    left.append(bar, listBox); right.append(detail); body.append(left, right); d.append(body);
  }

  function wide(kindKey, ...args) {
    ({ review: wideReview, active: wideActive, sessions: wideSessions, usage: wideUsage, map: wideMap }[kindKey])(...args); // each sets its own recipe, so a redraw can still pop out
  }
  // Search on its own line; under it the sort control at the far left, a divider, then the filter chips on one line that scrolls sideways (never wraps).
  // wide windows: sort first, then the search, then the kind chips on their own line (they wrap, never scroll sideways)
  function filterBar(first, sorter, chipHost) {
    const bar = el("div", "ov-wide-bar"); chipHost.className = "ov-wide-filter";
    bar.append(sorter, ...first, chipHost);
    return bar;
  }
  // A real list of sort orders ([key, label]); onPick gets the key.
  function sortSelect(orders, cur, onPick) {
    const s = el("select", "ov-wide-sort"); s.setAttribute("aria-label", "정렬");
    for (const [k, label] of orders) { const o = el("option", "", label); o.value = k; o.selected = k === cur; s.append(o); }
    s.addEventListener("change", () => onPick(s.value));
    return s;
  }

  // 승인 대기 (580:49518 + improvements 592:51366 / 764:61615)
  // 드리밍 정리 제안 (maintenance proposals) wait here too, as [정리] rows with their own decide
  const DREAM_LABEL = { duplicate: "합치기", contradiction: "고치기" };
  const known = (ref) => { const m = ref && memOf(ref.id); return !!m && (ref.revision == null || m.revision === ref.revision); };
  // a proposal whose memory is gone or changed since is hidden, as on the 기억 screen: deciding it would only go stale
  const dreamItems = () => (D().maintenance || []).filter((p) => { const t = p.maintenance || {}; return t.memory ? known(t.memory) : known(t.keep) && known(t.retire); }).map((p) => { const t = p.maintenance || {};
    return { dream: p, id: p.id, created_at: p.created_at, text: memText(t.memory?.id || t.keep?.id), label: t.memory ? "고치기" : DREAM_LABEL[t.type] || "정리" }; });
  function wideReview(focusId, startKind) {
    const all = waiting(), dreams = dreamItems(), d = shell("승인 대기", `${num(all.length + dreams.length)}건`, "AI는 분류만 해요. 승인은 사람이 해요.", () => { open("review"); });
    recipe(d, { wide: "review", args: [focusId, startKind] });
    let q = "", kindF = startKind || "all", typeF = "", sel = focusId || (startKind === "dream" ? dreams[0]?.id : all[0]?.id), order = "old"; const checked = new Set();
    const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), more = el("div", "ov-wide-foot-note");
    const merges = new Map();
    request(`/v1/merge_proposals?${query({ status: "open" })}`).then((r) => { for (const x of r?.items || []) { const pr = x.merge?.proposal; if (pr?.action === "merge" && pr.candidate?.id) merges.set(pr.candidate.id, x); } if (merges.size && d.isConnected) paint(); }, () => {});
    const chipHost = el("div");
    const search = el("input", "ov-wide-search"); search.placeholder = "후보 찾기"; search.setAttribute("aria-label", "후보 찾기");
    const batch = button("선택 0개 승인", "small", null, async () => { const items = remote.snapshot().items.filter((c) => checked.has(c.id)); if (!items.length) return; batch.disabled = true; let fail = 0; for (const c of items) { const r = await decide(c, "approve", {}, true); if (r?.error) fail++; else checked.delete(c.id); } await reload(); wideReview(); if (fail) dlg.querySelector(".ov-wide-foot-note").textContent = `${fail}건은 처리하지 못했어요.`; });
    batch.disabled = true;
    // 정렬 (764:61615): oldest first by default, so nothing waits forever
    const ORDERS = [["old", "오래된 순"], ["new", "최신 순"], ["risk", "위험도 높은 순"]];
    const sorter = sortSelect(ORDERS, order, (k) => { order = k; paint(); });
    const bar = filterBar([search, batch], sorter, chipHost);
    const RISK_RANK = { high: 0, medium: 1, low: 2 }, rank = (c) => c.dream ? 2 : RISK_RANK[c.classification?.risk] ?? (confusing(c, merges) ? 1 : 3); // no AI risk yet: 헷갈림 counts as medium
    const byOrder = (a, b) => order === "new" ? time(b.created_at) - time(a.created_at)
      : order === "risk" ? rank(a) - rank(b) || time(a.created_at) - time(b.created_at)
      : time(a.created_at) - time(b.created_at);
    const remote = pagedWindow(d, "/v1/memory_candidates/query", () => ({ q, state: "all", category: ["new", "confusing"].includes(kindF) ? kindF : "", kind: typeF, sort: order === "new" ? "recent" : order }), paint);
    const filtered = () => [...(kindF === "dream" ? [] : remote.snapshot().items), ...((kindF === "all" && !typeF) || kindF === "dream" ? dreamItems().filter(c => !q || c.text.toLowerCase().includes(q.toLowerCase())).sort(byOrder) : [])];
    const drawChips = () => { const snap = remote.snapshot(), counts = snap.facets.states || {}, sum = Object.values(counts).reduce((a, b) => a + b, 0);
      const every = (Object.keys(counts).length ? sum : snap.total ?? 0) + dreamItems().length; // 전체 = what the title counts: every waiting item and the proposals
      chipHost.replaceChildren(chipBar([["all", "전체", every], ["new", "새로 저장", counts.new || 0], ["confusing", "헷갈림", counts.confusing || 0], ...((n) => n ? [["dream", "정리 제안", n]] : [])(dreamItems().length)], kindF, (k) => { kindF = k; if (k === "dream") typeF = ""; paint(); }));
      const kinds = Object.entries(snap.facets.kinds || {});
      if (kindF !== "dream" && kinds.length > 1) chipHost.append(chipBar([["", "모든 종류", null], ...kinds.map(([k, n]) => [k, kind(k), n])], typeF, (k) => { typeF = k; paint(); }));
      if (!snap.loading) badge.textContent = `${num(every)}건`; }; // the title counts every waiting item, not only the loaded page
    const badge = d.querySelector(".ov-win-badge");
    function paint() {
      drawChips(); const items = filtered(); list.replaceChildren();
      if (!items.length) list.append(el("p", "ov-win-empty", remote.snapshot().loading ? "불러오는 중…" : "지금 보여 줄 후보가 없어요."));
      for (const c of items) {
        if (c.dream) { list.append(dreamRow(c)); continue; }
        const r = el("div", `ov-wide-row${c.id === sel ? " on" : ""}`), cb = el("input"); cb.type = "checkbox"; cb.checked = checked.has(c.id); cb.setAttribute("aria-label", "선택");
        cb.addEventListener("click", (e) => e.stopPropagation()); cb.addEventListener("change", () => { cb.checked ? checked.add(c.id) : checked.delete(c.id); batch.textContent = `선택 ${checked.size}개 승인`; batch.disabled = !checked.size; });
        const t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t"), conf = confusing(c, merges); l1.append(...(conf ? [tagEl("헷갈림", "ov-tag-r")] : []), tagEl(kind(c.kind)), el("span", "ov-win-item-title", clip(c.text, 70)));
        const dupText = c.classification?.dup_hint && D().memories.find((m) => m.id === c.classification.dup_hint)?.text;
        t.append(l1, el("small", "", [dupText ? `“${clip(dupText, 24)}”와 같은 뜻` : c.conflict_id ? "충돌 중" : c.classification?.risk_reasons?.[0] || "", ago(c.created_at)].filter(Boolean).join(" · ")));
        r.append(cb, t, el("span", `ov-wide-row-r ${time(c.created_at) < Date.now() - 24 * HOUR ? "ov-r-a" : ""}`, waitFor(c.created_at))); r.addEventListener("click", () => { sel = c.id; paint(); }); list.append(r);
      }
      more.replaceChildren(); remote.footer(more);
      const cur = items.find((c) => c.id === sel) || items[0]; sel = cur?.id; list.querySelectorAll(".ov-wide-row").forEach((rw, k) => rw.classList.toggle("on", items[k]?.id === sel)); detailFor(cur);
    }
    search.addEventListener("input", () => { q = search.value.trim(); paint(); });
    // reading order (2026-10-21): what it says → AI 판단 → final text + buttons; then 판단 근거; then 어디서 왔나요·승인하면 as compact meta
    function dreamRow(c) {
      const r = el("div", `ov-wide-row ov-wide-row-dream${c.id === sel ? " on" : ""}`), t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t"), x = c.dream.maintenance || {};
      l1.append(tagEl("정리"), tagEl(c.label), el("span", "ov-win-item-title", clip(c.text, 70)));
      t.append(l1, el("small", "", [x.retire?.id ? `→ ${clip(memText(x.retire.id), 30)}` : x.evidence ? `통과율 ${Math.round((x.evidence.pass_rate || 0) * 100)}%` : "", "드리밍"].filter(Boolean).join(" · ")));
      r.append(el("span", "ov-wide-row-cb"), t, el("span", "ov-wide-row-r", c.created_at ? waitFor(c.created_at) : "")); r.addEventListener("click", () => { sel = c.id; paint(); });
      return r;
    }
    // A dreaming proposal: what stays, what goes to history, and the person's three choices (nothing is deleted)
    function dreamDetail(c) {
      const p = c.dream, x = p.maintenance || {}, signal = !!x.memory, merge = x.type !== "contradiction";
      const h = el("div", "ov-detail-head"); h.append(tagEl("정리"), tagEl(c.label), el("small", "", `드리밍 제안${c.created_at ? ` · ${ago(c.created_at)}` : ""}`));
      const head = el("div", "ov-detail-group"); head.append(h, el("h3", "", signal ? (x.type === "lesson_often_failing" ? "이 교훈이 자주 실패해요. 다시 볼까요?" : "이 규칙을 자주 어겨요. 다시 볼까요?") : merge ? "비슷한 기억 2개, 하나로 합칠까요?" : "서로 다른 말을 하는 기억, 새 것으로 바꿀까요?"));
      const g = el("div", "ov-detail-group"), cmp = el("div", "ov-detail-list");
      const card = (cls, label, id) => { const b = el("div", `ov-cmp ${cls}`); b.append(el("small", "", label), el("span", "", memText(id))); cmp.append(b); };
      if (signal) { card("", "대상 기억", x.memory.id); const ev = x.evidence; if (ev) cmp.append(el("p", "", `최근 ${num(ev.samples)}번 중 ${num(ev.passed)}번만 지켜졌어요 (통과율 ${Math.round((ev.pass_rate || 0) * 100)}%).`)); }
      else { card("diff-add", "남길 기억", x.keep?.id); card("diff-del", "이력으로 넘길 기억", x.retire?.id); }
      const why = { same_meaning: "두 기억이 같은 말을 해요.", more_specific: "남길 기억이 더 자세해요. 다른 하나의 내용이 거기에 들어 있다고 봤어요.", newer_replaces_older: "새 기억이 예전 기억과 반대예요. 지금 생각은 새 기억이라고 봤어요." }[x.reason];
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      const go2 = async (decision) => { act.querySelectorAll("button").forEach((b) => (b.disabled = true));
        try { await request(`/v1/maintenance_proposals/${encodeURIComponent(p.id)}/decide`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: p.revision, decision }) }); sel = null; await reload(); wideReview(null, kindF); }
        catch (er) { st.textContent = er.message === "revision_conflict" ? "기억이 바뀌어 이 제안은 낡았어요." : `처리하지 못했어요: ${er.message}`; act.querySelectorAll("button").forEach((b) => (b.disabled = false)); } };
      if (signal) act.append(button("다시 볼게요", "small primary", null, () => go2("accept")), button("괜찮아요", "small", null, () => go2("dismiss")));
      else act.append(button(merge ? "제안대로 합치기" : "제안대로 바꾸기", "small primary", null, () => go2("accept")), button("반대로 남기기", "small", null, () => go2("swap")), button("둘 다 두기", "small", null, () => go2("dismiss")));
      g.append(cmp, ...(why ? [el("p", "ov-detail-why", why)] : []), act, el("small", "ov-detail-hint", signal ? "어느 쪽을 골라도 기억은 그대로예요. 다시 볼게요를 누르면 사람이 보기로 한 것만 남아요." : "어느 쪽을 골라도 지우지 않아요. 넘긴 기억은 기억 이력에 남아요."), st);
      detail.append(head, g);
    }
    function detailFor(c) {
      detail.replaceChildren();
      if (!c) { detail.append(el("p", "ov-win-empty", "고를 후보가 없어요.")); return; }
      if (c.dream) { dreamDetail(c); return; }
      const k = c.classification, conf = confusing(c, merges), mp = merges.get(c.id), v = k?.verdict;
      const h = el("div", "ov-detail-head"); h.append(...(conf ? [tagEl("헷갈림", "ov-tag-r")] : []), tagEl(kind(c.kind)), el("small", "", `${k?.by ? "AI 분류 · " : ""}${ago(k?.at || c.created_at)}`));
      const edit = el("textarea", "ov-detail-edit"); edit.value = v?.action === "fix" && v.text ? v.text : c.text; edit.rows = 3; edit.hidden = !(v?.action === "fix" && v.text); edit.setAttribute("aria-label", "고쳐서 저장할 내용");
      const head = el("div", "ov-detail-group"); head.append(h, el("h3", "", c.text)); detail.append(head);
      const decide1 = el("div", "ov-detail-group"), basisG = el("div", "ov-detail-group ov-detail-group-sub"), metaG = el("div", "ov-detail-group ov-detail-group-sub");
      basisG.append(el("h4", "ov-detail-group-t", "판단 근거")); metaG.append(el("h4", "ov-detail-group-t", "자세한 정보"));
      const sec = (into, title, ...nodes) => { const s2 = el("div", "ov-detail-sec"); s2.append(el("small", "", title), ...nodes); into.append(s2); return s2; };
      // AI 판단 (764:61615): advice only, the buttons below stay the person's
      if (v && VERDICT[v.action]) {
        const box = el("div", "ov-verdict"); box.append(el("strong", `ov-verdict-${v.action}`, VERDICT[v.action])); if (v.reason) box.append(el("p", "", v.reason));
        // 다시 보기: a person asks for a fresh judgement; the hourly AI review writes it, the old one stays until then
        const again = button("", "small ghost icon-btn ov-verdict-again", null, async () => {
          again.disabled = true;
          try { const r = await request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/reclassify`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: c.revision }) }); Object.assign(c, r || {}); if (c.classification) c.classification.by = ""; detailFor(c); }
          catch (e) { again.disabled = false; again.title = tr("다시 보게 하지 못했어요. 한 번 더 눌러 보세요."); }
        });
        again.append(icon("sparkle")); again.title = tr("AI에게 다시 보게 하기"); again.setAttribute("aria-label", again.title);
        // AI 의미 검사 (592:51366) belongs to this judgement: marks only, a person decides; the note indented under its item
        const checks = el("ul", "ov-checks");
        for (const x of k?.semantic || []) { const lab = SEMANTIC[x.check]; if (!lab) continue; const li = el("li", `ov-check ${x.ok ? "ok" : "no"}`); const ic = el("span", "ov-check-i", x.ok ? "✓" : "!"); ic.setAttribute("aria-hidden", "true"); li.append(ic, el("span", "", lab[x.ok ? 0 : 1])); if (!x.ok && x.note) li.append(el("small", "ov-check-note", clip(x.note, 120))); checks.append(li); }
        if (checks.children.length) box.append(checks);
        if (!k.by) { again.disabled = true; box.append(el("small", "ov-verdict-wait", "다시 보는 중이에요. 다음 AI 검토(매시 정각)에 바뀌어요.")); }
        const s3 = sec(decide1, `AI 판단${k.at ? ` · ${new Date(k.at).toLocaleDateString(loc(), { month: "numeric", day: "numeric" })}` : ""}`, box); s3.classList.add("ov-detail-sec-act", "ov-detail-sec-box"); s3.firstChild.after(again);
      }
      const rs = c.reach_suggestion;
      if (rs) { const chips = el("div", "ov-wide-chips"); for (const [val, l] of [["personal", "개인"], ["general", "범용"], ["project", "이 프로젝트만"]]) chips.append(el("span", `ov-wide-chip${val === rs.reach ? " on" : ""}`, val === rs.reach ? `${l} · AI 추천` : l)); sec(decide1, "AI 칸 제안 · 정하는 건 사람이에요", chips, ...(rs.reason ? [el("p", "", rs.reason)] : [])); }
      const oldId = mp?.merge?.proposal?.targets?.[0]?.id || k?.dup_hint, oldMem = oldId && D().memories.find((m) => m.id === oldId);
      if (oldMem) {
        const cmp = el("div", "ov-detail-list"), nw = el("div", "ov-cmp diff-add"), od = el("div", "ov-cmp diff-del"), u = usage(oldId), n = u ? u.d30 || u.d7 || 0 : 0;
        nw.append(el("small", "", "새 후보"), el("span", "", c.text)); od.append(el("small", "", n ? `기존 기억 · ${num(n)}번 쓰임` : "기존 기억"), el("span", "", oldMem.text)); cmp.append(od, nw); sec(decide1, "비교", cmp);
      }
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions"); let editing = null;
      const go1 = async (action, extra) => { act.querySelectorAll("button").forEach((b) => (b.disabled = true)); const r = await decide(c, action, extra); if (r?.error) { st.textContent = r.error; act.querySelectorAll("button").forEach((b) => (b.disabled = false)); } else { sel = null; wideReview(); } };
      if (c.conflict_id || c.status === "conflicted") { sec(decide1, "충돌", el("p", "", "기존 기억과 충돌하는 후보예요. 어느 쪽을 남길지는 전체 화면에서 골라요.")); act.append(button("전체 화면으로", "primary small", null, () => { dlg.close(); go("memories", "conflicted"); })); }
      else {
        const pick = v?.action === "fix" ? "fix" : v?.action === "merge" && mp ? "merge" : "approve"; // the AI's pick only takes the accent; nothing runs on its own
        const mk = (key, label, fn) => button(label, `small${pick === key ? " primary" : ""}`, null, fn);
        if (mp) act.append(mk("merge", "기존 기억에 합치기", async (e) => {
          const mb = e.currentTarget; act.querySelectorAll("button").forEach((b) => (b.disabled = true));
          try { await request(`/v1/merge_proposals/${encodeURIComponent(mp.id)}/decide`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: mp.revision, decision: "accept" }) }); merges.delete(c.id); sel = null; await reload(); wideReview(); }
          catch (er) { st.textContent = er.message === "revision_conflict" ? "후보나 대상 기억이 바뀌어 이 제안은 낡았어요." : `합치지 못했어요: ${er.message}`; act.querySelectorAll("button").forEach((b) => (b.disabled = false)); mb.focus(); }
        }));
        const choices = [...act.children, mk("approve", mp || oldMem ? "새 기억으로 승인" : "그대로 승인", () => go1("approve")),
          mk("fix", "고쳐서 승인", () => editing(true)), button("버리기", "small", null, () => askReason("후보를 버릴까요?", (reason) => go1("reject", { reason })))];
        const save = button("고친 내용으로 승인", "primary small", null, () => { if (edit.value.trim()) go1("edit_approve", { content: edit.value.trim() }).catch(() => {}); });
        const cancel = button("취소", "small", null, () => { edit.value = c.text; editing(false); });
        editing = (on) => { editSec.hidden = !on; act.replaceChildren(...(on ? [save, cancel] : choices)); if (on) edit.focus(); };
      }
      const editSec = sec(decide1, "저장할 내용 · 고칠 수 있어요", edit); editSec.hidden = true;
      window.ArkismAIChat?.field(edit, { state: {}, title: "기억 다듬기", preset: "You polish one memory an AI agent will be given later: one clear Korean statement of the rule, decision or fact, keeping its meaning and its applicability conditions, nothing added that the text does not say. At most 1000 characters.", context: () => JSON.stringify({ kind: c.kind, original: c.text, summary: c.summary || "" }) });
      editing?.(!edit.hidden);
      decide1.append(act, ...(mp || oldMem ? [el("small", "ov-detail-hint", "합치기는 위 기존 기억에 이 내용을 더해 하나로 만들어요. 새 기억으로 승인하면 둘 다 따로 남아요.")] : []), st); detail.append(decide1);
      // 판단 근거: why it was flagged, the source quote, the meaning checks
      if (conf && (c.summary || k?.risk_reasons?.length)) { const why = el("p", "ov-detail-why"); why.append(tagEl("헷갈림", "ov-tag-r"), el("span", "", c.summary || k.risk_reasons.join(" · "))); sec(basisG, "AI가 본 이유", why); }
      else if (c.summary) sec(basisG, "요약", el("p", "", c.summary));
      if (!conf && k?.risk_reasons?.length) sec(basisG, `AI 분류 · 위험도 ${k.risk}`, el("p", "", k.risk_reasons.join(" · ")));
      if (k?.evidence_span) { const q2 = el("blockquote", "ov-detail-quote", `“${clip(k.evidence_span, 240)}”`); const basis = tag(c, "basis:"); sec(basisG, ["원문 근거", basis === "explicit_user" ? "사용자 말" : basis === "inferred" ? "AI가 짐작" : "", new Date(c.created_at).toLocaleDateString(loc(), { month: "numeric", day: "numeric" })].filter(Boolean).join(" · "), q2); }
      if (basisG.children.length > 1) detail.append(basisG);
      const ev = tag(c, "event_id:");
      if (ev) {
        const [agent, sid] = ev.split(":"), ses = env.sessions().find((x) => x.session_id === sid), b = el("div", "ov-detail-from");
        b.append(el("span", "", ses ? `${agent} · ${ses.title || ses.project || ""}` : agent), button("세션 보기 ›", "ghost small", null, () => openSession(ev, k?.evidence_span || ""))); sec(metaG, "어디서 왔나요", b);
      }
      const f = el("div", "ov-detail-facts"), fact = (a2, b2) => { const p = el("p", ""); p.append(el("span", "", a2), el("strong", "", b2)); f.append(p); };
      // approve takes no reach: an empty one becomes personal for a user memory, general otherwise (the suggestion stays advice)
      const reachOnApprove = c.reach || (c.kind === "user" ? "personal" : "general");
      fact("들어갈 곳", "쓰는 중인 기억"); fact("쓰는 곳", REACH_WHERE[reachOnApprove] || REACH_WHERE.general); sec(metaG, "승인하면", f);
      detail.append(metaG);
    }
    twoPane(d, bar, list, detail); d.lastChild.querySelector(".ov-wide-left").append(more); paint();
  }

  // 쓰는 중인 기억 (579:48597)
  // filter "idle" (검증 › 정리할 기억): the same 안 쓰임 rule as the 검증 step, opened in place; ‹ 작게 보기 goes back to 검증
  function wideActive(filter, focusId) {
    const d = shell("쓰는 중인 기억", `${num(D().memories.length)}개`, "승인된 기억만 불러오기에 쓰여요", () => open(filter === "idle" ? "judge" : "active"));
    recipe(d, { wide: "active", args: [filter, focusId] });
    let q = "", kindF = filter || "all", sel = focusId || null, sort = "used";
    const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), chipHost = el("div");
    const search = el("input", "ov-wide-search"); search.placeholder = "기억 찾기"; search.setAttribute("aria-label", "기억 찾기");
    const sorter = sortSelect([["used", "많이 쓰인 순"], ["new", "최신 순"], ["old", "오래된 순"]], sort, (k) => { sort = k; paint(); });
    const bar = filterBar([search], sorter, chipHost);
    const w = D().tools?.memory_use;
    const remote = pagedWindow(d, "/v1/memories/query", () => ({ q, state: kindF === "idle" ? "idle" : "active", kind: ["all", "idle"].includes(kindF) ? "" : kindF, sort: sort === "new" ? "recent" : sort }), paint);
    function paint() {
      const snap = remote.snapshot(), kinds = Object.entries(snap.facets.kinds || {});
      if (!snap.loading && kindF === "all" && !q && snap.total != null) d.querySelector(".ov-win-badge").textContent = `${num(snap.total)}개`; // server total, not the loaded page
      chipHost.replaceChildren(chipBar([["all", "전체", snap.facets.kinds ? Object.values(snap.facets.kinds).reduce((a,b)=>a+b,0) : snap.total ?? null], ...kinds.map(([k,n]) => [k,kind(k),n]), ["idle", "안 쓰임", kindF === "idle" ? snap.total ?? 0 : null]], kindF, k => { kindF=k;paint(); }));
      const items = snap.items;
      list.replaceChildren(); if (!items.length) list.append(el("p", "ov-win-empty", snap.loading ? "불러오는 중…" : "찾은 기억이 없어요."));
      for (const m of items) {
        const r = el("div", `ov-wide-row${m.id === sel ? " on" : ""}`), t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t"); l1.append(tagEl(kind(m.kind)), el("span", "ov-win-item-title", clip(m.text, 70)));
        t.append(l1, el("small", "", [tag(m, "repo:"), ago(m.created_at)].filter(Boolean).join(" · ")));
        const u = usage(m.id); r.append(t, el("span", "ov-wide-row-r", u ? (u.d7 ? `7일 ${num(u.d7)}번 쓰임` : "안 쓰임") : ""));
        r.addEventListener("click", () => { sel = m.id; paint(); }); list.append(r);
      }
      remote.footer(list);
      const cur = items.find((m) => m.id === sel) || items[0]; sel = cur?.id; list.querySelectorAll(".ov-wide-row").forEach((rw, k) => rw.classList.toggle("on", items[k]?.id === sel)); detailFor(cur);
    }
    search.addEventListener("input", () => { q = search.value.trim(); paint(); });
    function detailFor(m) {
      detail.replaceChildren(); if (!m) return;
      const h = el("div", "ov-detail-head"); h.append(tagEl(kind(m.kind)), el("small", "", `${m.created_by_type === "agent" ? "AI가 모음" : "사람이 만듦"} · ${ago(m.created_at)}`));
      detail.append(h, el("h3", "", m.text));
      const sec = (title, ...nodes) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...nodes); detail.append(s); };
      const u = usage(m.id), wu = w?.items?.find((x) => x.id === m.id), f = el("div", "ov-detail-facts");
      const fact = (a, b) => { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); };
      if (u) { fact("최근 7일", `${num(u.d7 || 0)}번 쓰임`); if (u.last) fact("마지막으로 쓰임", ago(u.last)); }
      if (wu) fact(`최근 ${w.days}일`, `${num(wu.injected)}번 넣음 · ${num(wu.used)}번 실제로 쓰임`);
      if (f.children.length) sec("언제 쓰이나요", f);
      const ev = tag(m, "event_id:"); if (ev) { const b = el("div", "ov-detail-from"); b.append(el("span", "", ev.split(":")[0]), button("세션 보기 ›", "ghost small", null, () => openSession(ev, m.text))); sec("어디서 왔나요", b); }
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      act.append(button("고치기", "primary small", null, () => editMemory(m, m.id)),
        button("지우기", "small", null, () => askConfirm("이 기억을 지울까요?", "지운 기억은 이력에 남지만 더는 쓰이지 않아요.", async () => {
          try { await request(`/v1/memories/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) }); D().memories = D().memories.filter((x) => x.id !== m.id); sel = null; await reload(); wideActive(filter); }
          catch (e) { st.textContent = `지우지 못했어요: ${e.message}`; }
        })));
      detail.append(st, act);
    }
    twoPane(d, bar, list, detail); paint();
  }
  function askConfirm(title, text, onOk) {
    const d = el("dialog", "crew-dialog ov-ask"), body = el("div", "dialog-body"), foot = el("div", "ov-win-foot");
    foot.append(button("취소", "ghost small", null, () => d.close()), button("지우기", "primary small", null, () => { d.close(); onOk(); }));
    body.append(el("h2", "", title), el("p", "meta", text)); d.append(body, foot); d.addEventListener("close", () => d.remove()); document.body.append(d); d.showModal();
  }

  // 세션 → 후보: 후보의 event_id:<agent>:<session_id>:<kind>:<end> 태그가 유일한 연결. 서버에 분석 실패 기록은 없어 "후보 없음"으로만 보여요.
  const FLOW = ["저장", "분석", "분류", "승인"];
  function sessionState(x) {
    const mine = (m) => tag(m, "event_id:").split(":")[1] === x.session_id;
    const cands = waiting().filter(mine), approved = D().memories.filter(mine);
    const fresh = Date.now() - time(x.ended_at || x.started_at) < 30 * 60000;
    const step = approved.length ? 4 : cands.some((m) => m.classification) ? 3 : cands.length ? 2 : 1;
    return { cands, approved, step, ...(cands.length || approved.length ? { tone: "ok", label: "분석 끝" } : fresh ? { tone: "wait", label: "분석 대기" } : { tone: "none", label: "후보 없음" }) };
  }
  // 세션 기록 (580:51861)
  function wideSessions() {
    const items = env.sessions(), today = items.filter((x) => time(x.ended_at || x.started_at) >= new Date().setHours(0, 0, 0, 0)).length;
    const d = shell("세션 기록", `${num(items.length)}건${today ? ` · 오늘 ${num(today)}` : ""}`, "저장된 대화예요. 원문은 여기서만 열려요", () => open("collect"));
    recipe(d, { wide: "sessions", args: [] });
    let q = "", agent = "all", sel = items[0]?.id; const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), chipHost = el("div");
    const search = el("input", "ov-wide-search"); search.placeholder = "세션 찾기"; search.setAttribute("aria-label", "세션 찾기");
    const bar = el("div", "ov-wide-bar"); bar.append(search, chipHost, button("새로고침", "ghost small", null, async () => { await env.reloadSessions(); wideSessions(); }));
    const agents = [...new Set(items.map((x) => x.agent).filter(Boolean))];
    const remote = pagedWindow(d, "/v1/usage/sessions", () => ({ range: "7d", sort: "recent", q, source: agent === "all" ? "" : agent }), paint, true);
    function paint() {
      chipHost.replaceChildren(chipBar([["all", "전체", items.length], ...agents.map((a) => [a, a, items.filter((x) => x.agent === a).length])], agent, (k) => { agent = k; paint(); }));
      const shown = remote.snapshot().items;
      list.replaceChildren(); if (!shown.length) list.append(el("p", "ov-win-empty", "찾은 세션이 없어요."));
      for (const x of shown) {
        const r = el("div", `ov-wide-row${x.id === sel ? " on" : ""}`), t = el("div", "ov-wide-row-t");
        t.append(el("span", "ov-win-item-title", x.title || x.session_id), el("small", "", [x.agent, x.project, `메시지 ${num(x.messages)}개`].filter(Boolean).join(" · ")));
        const st = sessionState(x), rr = el("span", "ov-wide-row-r"); rr.append(el("span", `ov-pill ov-pill-${st.tone}`, st.label), ...(st.cands.length ? [el("span", "ov-wide-row-n", `후보 ${num(st.cands.length)}개`)] : []), el("span", "", ago(x.ended_at || x.started_at)));
        r.append(t, rr); r.addEventListener("click", () => { sel = x.id; paint(); }); list.append(r);
      }
      remote.footer(list);
      const cur = shown.find((x) => x.id === sel) || shown[0]; sel = cur?.id; list.querySelectorAll(".ov-wide-row").forEach((rw, k) => rw.classList.toggle("on", shown[k]?.id === sel));
      detail.replaceChildren(); if (!cur) return;
      const h = el("div", "ov-detail-head"); h.append(el("span", "ov-win-tag", cur.agent || "세션"), el("small", "", `${cur.project || ""} · ${new Date(cur.started_at).toLocaleString(loc(), { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}`));
      detail.append(h, el("h3", "", cur.title || cur.session_id));
      const f = el("div", "ov-detail-facts"); const fact = (a, b) => { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); };
      fact("메시지", `${num(cur.messages)}개`); if (cur.active_seconds) fact("작업 시간", `${Math.round(cur.active_seconds / 60)}분`); if (cur.tokens) fact("토큰", num(cur.tokens)); if (cur.requests) fact("요청", `${num(cur.requests)}회`); if (cur.device) fact("기기", cur.device);
      const ss = sessionState(cur), fl = el("div", "ov-flow");
      FLOW.forEach((n, k) => { if (k) fl.append(el("i", "", "›")); fl.append(el("span", `ov-flow-step${k + 1 < ss.step ? " done" : k + 1 === ss.step ? " now" : ""}`, n)); });
      const fs = el("div", "ov-detail-sec"); fs.append(el("small", "", "이 세션의 흐름"), fl); detail.append(fs);
      const s = el("div", "ov-detail-sec"); s.append(el("small", "", "이 세션"), f); detail.append(s);
      const cs = el("div", "ov-detail-sec"); cs.append(el("small", "", "여기서 나온 후보"));
      for (const m of [...ss.cands, ...ss.approved]) { const rw = el("div", "ov-detail-cand"); rw.append(el("span", "", clip(m.text, 60)), el("small", "", ss.cands.includes(m) ? "승인 대기" : "승인됨")); if (ss.cands.includes(m)) { rw.classList.add("link"); rw.addEventListener("click", () => wide("review", m.id)); } cs.append(rw); }
      if (!ss.cands.length && !ss.approved.length) cs.append(el("p", "ov-win-empty", "이 세션에서 나온 후보가 없어요."));
      detail.append(cs);
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      act.append(button("대화 원문 보기", "primary small", null, () => { dlg.close(); go("sessions"); }),
        button("세션 지우기", "small", null, () => askConfirm("이 세션을 지울까요?", "세션과 보관된 대화가 지워지고 되돌릴 수 없어요. 에이전트의 원래 파일은 그대로 남고, 이 대화를 출처로 둔 기억은 내용은 남고 출처 연결만 사라져요.", async () => {
          try { await request("/v1/usage/sessions/delete", { method: "POST", body: JSON.stringify({ scope: state.scope, ids: [cur.id] }) }); sel = null; await env.reloadSessions(); wideSessions(); }
          catch (e) { st.textContent = /access_denied/.test(e?.message || "") ? "지울 권한이 없어요." : `지우지 못했어요: ${e.message}`; }
        })));
      detail.append(st, act);
    }
    search.addEventListener("input", () => { q = search.value.trim(); paint(); });
    twoPane(d, bar, list, detail); d.lastChild.querySelector(".ov-wide-left").append(el("div", "ov-wide-foot-note", `최근 7일 ${num(items.length)}건`)); paint();
  }

  // 사용량 (580:54204)
  function wideUsage() {
    const items = env.sessions(), w = D().tools?.memory_use, recalls = [...(state.usage?.entries() || [])];
    const d = shell("사용량", `7일 ${num(recalls.reduce((a, [, u]) => a + (u.d7 || 0), 0))}회`, "어떤 기억이 어디에 쓰였는지 보여요", () => open("recall"));
    recipe(d, { wide: "usage", args: [] });
    const body = el("div", "ov-wide-body"), left = el("div", "ov-wide-left"), right = el("div", "ov-wide-right ov-wide-detail");
    const tiles = el("div", "ov-win-tiles ov-win-tiles-4"); const rate = w?.injections ? Math.round((w.used / w.injections) * 100) : null;
    for (const [v, l] of [[`${num(recalls.reduce((a, [, u]) => a + (u.d7 || 0), 0))}회`, "7일 불러온 횟수"], [rate === null ? "—" : `${rate}%`, "실제로 쓰임"], [`${num(items.length)}건`, "7일 세션"], [`${num(recalls.filter(([, u]) => u.d7 > 0).length)}개`, "불러온 기억"]]) { const t = el("div", "ov-win-tile"); t.append(el("strong", "", v), el("span", "", l)); tiles.append(t); }
    const hours = new Array(24).fill(0), now = new Date().setMinutes(0, 0, 0);
    for (const x of items) { const t = time(x.ended_at || x.started_at), dd = Math.ceil((now - t) / HOUR); if (t && dd >= 0 && dd < 24) hours[23 - dd]++; else if (t >= now && t <= Date.now()) hours[23]++; }
    const chart = el("div", "ov-win-chart"), bars = el("div", "ov-win-bars"), max = Math.max(1, ...hours); chart.append(el("div", "ov-win-chart-title", "시간대별 세션 (최근 24시간)"));
    hours.forEach((n) => { const b = el("i", n ? "" : "none"); b.style.height = `${6 + (n / max) * 54}px`; b.title = `${n}`; bars.append(b); }); chart.append(bars, el("div", "ov-win-axis", "24시간 전 ··· 지금"));
    left.append(tiles, chart, el("div", "ov-win-list-title", "최근 사용 기록"));
    const ul = el("div", "ov-win-list"); ul.append(...recalls.filter(([, u]) => u.last).sort((a, b) => time(b[1].last) - time(a[1].last)).slice(0, 30).map(([id, u]) => row("", clip(memText(id)), ago(u.last), `7일 ${num(u.d7 || 0)}회`))); if (!ul.children.length) ul.append(el("p", "ov-win-empty", "지금 보여 줄 항목이 없어요.")); left.append(ul);
    const sec = (title, ...nodes) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...nodes); right.append(s); };
    right.append(el("h3", "", "한눈에"));
    { const f = el("div", "ov-detail-facts"); for (const [a, b] of [["불러온 횟수", `${num(recalls.reduce((n, [, u]) => n + (u.d30 || 0), 0))}회`], ["불러온 기억", `${num(recalls.filter(([, u]) => u.d30 > 0).length)}개`]]) { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); } sec("최근 30일", f); }
    const byAgent = {}; items.forEach((x) => { byAgent[x.agent || "기타"] = (byAgent[x.agent || "기타"] || 0) + 1; });
    const agentBox = el("div", "ov-detail-bars"); for (const [a, n] of Object.entries(byAgent).sort((x, y) => y[1] - x[1])) { const p = el("div", "ov-detail-bar"), pct = Math.round((n / Math.max(1, items.length)) * 100); const track = el("i"); const fill = el("b"); fill.style.width = `${pct}%`; track.append(fill); const line = el("p", ""); line.append(el("span", "", a), el("strong", "", `${pct}%`)); p.append(line, track); agentBox.append(p); }
    if (items.length) sec("에이전트별 (7일 세션)", agentBox);
    const top = recalls.filter(([, u]) => u.d7 > 0).sort((a, b) => b[1].d7 - a[1].d7).slice(0, 3); if (top.length) { const b = el("div", "ov-detail-list"); top.forEach(([id, u]) => b.append(row("", clip(memText(id), 40), "", `${num(u.d7)}번`))); sec("많이 쓰인 기억", b); }
    const idle = (w?.items || []).filter((u) => u.injected >= 3 && !u.used).slice(0, 3); if (idle.length) { const b = el("div", "ov-detail-list"); idle.forEach((u) => b.append(row("", clip(memText(u.id), 40), "", `${num(u.injected)}번 중 0번`))); sec("넣었는데 안 쓰인 기억", b); }
    const act = el("div", "ov-detail-actions"); act.append(button("검증 보기", "primary small", null, () => open("judge")), button("사용량 화면으로", "small", null, () => { dlg.close(); go("usage"); })); right.append(act);
    body.append(left, right); d.append(body);
  }

  // 기억 지도 (611:54134 · 611:56374): GET /v1/memory-graph?mode=all, grouped by 칸 (reach), curved lines between memories.
  // The map has no AI-suggestion write API (POST /v1/knowledge-graph/relations writes a separate store the map never reads), so 연결/아니요 are not drawn.
  const REACH = { personal: ["개인", 0], general: ["범용", 1], project: ["프로젝트", 2] };
  const EDGE = { supports: "지지", supersedes: "대체", conflicts: "충돌", human_link: "연결", same_session: "관련", similar: "비슷" };
  const REACH_COLOR = { personal: "#7fc79c", general: "#6aa3ff", project: "#f5b955" }; // same colours as the 칸 chips (v4-overview.css .ov-map-personal …)
  // opens that memory in the memory list (its detail on the right), reading it first when the map has only its label
  async function editMemory(m, id) {
    m = m || await request(`/v1/memories/${encodeURIComponent(id)}?${query()}`).catch(() => null);
    dlg?.close();
    if (m?.status === "active" && ctx.openRecord) ctx.openRecord(m); else go("memories", "active");
  }
  async function wideMap() {
    const d = modal("ov-wide-win ov-map-win", true);
    recipe(d, { wide: "map", args: [] });
    const head = el("div", "ov-win-head ov-map-head"), ttl = el("div", "ov-win-title");
    ttl.append(button("‹ 개요", "ghost small", null, () => d.close()), el("i", "ov-dot"), el("h2", "", "기억 지도"));
    const x = button("✕", "ghost small", null, () => d.close()); x.setAttribute("aria-label", "닫기"); head.append(ttl, x); d.append(head);
    const body = el("div", "ov-map-body"), left = el("div", "ov-map-main"), right = el("div", "ov-map-side ov-wide-detail");
    // loading: a spinner in the middle of the canvas area and a skeleton summary on the right
    const status = el("div", "ov-map-loading"), msg = el("p", "", "지도를 불러오는 중…"); status.append(el("i", "ov-map-spin"), msg); left.append(status);
    const sk = el("div", "ov-map-sidehead"); sk.append(el("h4", "", "지도 요약")); right.append(sk, ...Array.from({ length: 4 }, () => el("div", "skeleton ov-map-sk")));
    body.append(left, right); d.append(body);
    let graph, topics;
    try { [graph, topics] = await Promise.all([request(`/v1/memory-graph?${query({ mode: "all" })}`), request(`/v1/digests?${query({ kind: "topic", status: "active" })}`).then((r) => r.items || [], () => [])]); }
    catch (e) { if (!d.isConnected) return; status.classList.add("off"); msg.textContent = /access_denied/.test(e?.message || "") ? "지도를 볼 권한이 없어요." : "지도를 불러오지 못했어요. 잠시 뒤 다시 시도해 주세요."; status.append(button("다시 시도", "small", null, () => { d.close(); void wideMap(); })); right.replaceChildren(); return; }
    if (!d.isConnected) return;
    const nodes = graph.nodes || [], edges = graph.edges || [];
    if (!nodes.length) { status.classList.add("off"); msg.textContent = "지도에 그릴 기억이 아직 없어요."; right.replaceChildren(); return; }
    status.remove();
    let mode = "all", sel = null, lastSel = null, q = "", reachF = new Set(Object.keys(REACH)), topicF = null, allEdges = false, moreOpen = false;
    let topicSet = null; // members of the picked topic
    const inTopic = (id) => !topicF || topicSet.has(id);
    const byId = new Map(nodes.map((n) => [n.id, n])), reachOf = (n) => n?.reach || "general";
    const bar = el("div", "ov-map-bar"), seg = el("div", "ov-wide-seg ov-map-seg"), searchBox = el("label", "ov-map-search"), search = el("input", "ov-wide-search"); search.placeholder = "기억·주제 찾기"; search.setAttribute("aria-label", "기억·주제 찾기"); searchBox.append(search);
    const chipHost = el("div", "ov-map-reach"); bar.append(seg, searchBox, el("small", "", "칸"), chipHost);
    const canvas = el("div", "ov-map-canvas");
    const groups = {}; nodes.forEach((n) => { (groups[reachOf(n)] ||= []).push(n); });
    const proj = state.scope?.project_id && state.scope.project_id !== "default" ? state.scope.project_id : "";
    // node labels come with the map (first 40 characters); the full record is read when a memory is picked
    const fetched = new Map(), memRec = (id) => fetched.get(id) || memOf(id);
    const label = (id) => clip(byId.get(id)?.label || memRec(id)?.text || kind(byId.get(id)?.kind), 22);
    const readMemory = (id) => { if (fetched.has(id) || memOf(id)) return; fetched.set(id, null); request(`/v1/memories/${encodeURIComponent(id)}?${query()}`).then((r) => { fetched.set(id, r); }, () => { fetched.set(id, false); }).then(() => { if (d.isConnected && sel === id) drawSide(); }); };
    const rel = (id) => edges.filter((e) => e.type !== "similar" && (e.from === id || e.to === id));
    const fmt = (v) => new Date(v).toLocaleDateString(loc(), { year: "numeric", month: "numeric", day: "numeric" });
    // Figma 611:54134: one area per topic digest; memories in no topic sit in a grey "분류 대기" area
    const TOPIC_COLORS = ["#7c9cff", "#5fc6a8", "#e8a65d", "#c48ef0", "#e57b8f", "#6cc3e0", "#b9c56b", "#d98b5f"];
    const topicOf = new Map(); topics.forEach((t, i) => (t.members || []).forEach((r) => { if (!topicOf.has(r.id)) topicOf.set(r.id, { key: t.id, name: t.title || "주제", ord: i, color: TOPIC_COLORS[i % TOPIC_COLORS.length] }); }));
    const UNSORTED = { key: "_unsorted", name: "분류 대기", ord: 999, color: "#8a8f98" };
    const bubbles = window.ArkismMemoryBubbles.create({
      tabs: [...(topics.length ? [["topic", "주제별"]] : []), ["reach", "칸별"], ["kind", "종류별"]],
      groupOf: (m, p) => m === "reach" ? { key: p.reach, name: REACH[p.reach][0] + (p.reach === "project" && proj ? ` · ${proj}` : ""), ord: REACH[p.reach][1], color: REACH_COLOR[p.reach] } : m === "kind" ? { key: p.kind, name: kind(p.kind), color: KIND_COLOR[p.kind] } : topicOf.get(p.id) || UNSORTED,
      tip: (id) => { const n = byId.get(id); return n ? { text: label(id), meta: [kind(n.kind), n.uses ? `쓰임 ${num(n.uses)}회` : ""].filter(Boolean).join(" · ") } : null; },
      card: (id) => { const n = byId.get(id); return n ? { title: label(id), place: (REACH[reachOf(n)] || REACH.general)[0], meta: [kind(n.kind), n.uses ? `쓰임 ${num(n.uses)}회` : ""].filter(Boolean).join(" · ") } : null; },
      onSelect: (id) => { sel = lastSel = id; mode = "sel"; moreOpen = false; paint(); },
      onClear: () => { sel = lastSel = null; mode = "all"; paint(); },
      onZoom: (z) => { zoomLab.textContent = `${Math.round(z * 100)}%`; },
    });
    canvas.append(bubbles.el); d.addEventListener("close", () => bubbles.destroy());
    const mapEdges = edges.filter((e) => e.type !== "similar").map((e) => ({ from: e.from, to: e.to, t: e.type === "human_link" ? "supports" : e.type })); // one array: the map skips relinking while it is the same
    // a cut map says so on the canvas; older servers send only truncated
    const cut = "nodes_truncated" in graph ? [graph.nodes_truncated && `기억이 많아 ${num(nodes.length)}개까지만 그렸어요`, graph.edges_truncated && "선이 많아 일부 선은 빠졌어요"].filter(Boolean).join(" · ") : graph.truncated ? "기억이 많아 일부만 그렸어요." : "";
    if (cut) canvas.append(el("p", "ov-map-cut", cut));
    function drawMap() {
      const qq = q.toLowerCase(), topicHit = new Set(qq ? topics.filter((t) => (t.title || "").toLowerCase().includes(qq)).flatMap((t) => (t.members || []).map((r) => r.id)) : []);
      const vis = (n) => n && reachF.has(reachOf(n)) && inTopic(n.id) && (!qq || (n.label || label(n.id)).toLowerCase().includes(qq) || topicHit.has(n.id));
      const shown = nodes.filter(vis);
      if (sel && !shown.some((n) => n.id === sel)) { sel = lastSel = null; mode = "all"; moreOpen = false; }
      empty.hidden = shown.length > 0;
      bubbles.setItems(shown.map((n) => ({ id: n.id, v: n.uses || 1, color: REACH_COLOR[reachOf(n)] || REACH_COLOR.general, reach: reachOf(n) in REACH ? reachOf(n) : "general", kind: n.kind || "" })));
      bubbles.setEdges(mapEdges);
      bubbles.setEdgeAll(mode === "all" || allEdges); bubbles.setSelected(sel);
    }
    // floating bar: 4 line styles · 전체 관계/선택한 연결 · zoom
    const legend = el("div", "ov-map-legend"), zoomLab = el("span", "ov-map-zoom", "100%");
    const zb = (txt, label, fn) => { const b = el("button", "ov-map-zbtn", txt); b.type = "button"; b.setAttribute("aria-label", label); b.addEventListener("click", fn); return b; };
    function drawLegend() {
      const items = el("div", "ov-map-lg-items"); for (const t of ["supports", "same_session", "conflicts", "supersedes"]) { const i = el("span", ""); i.append(el("i", `ov-map-e-${t}`), document.createTextNode(EDGE[t])); items.append(i); }
      if (bubbles.groupLinesOnly()) items.append(el("span", "ov-map-lg-note", "점이 많아 무리 사이 선만 그려요"));
      const tog = el("div", "ov-map-lg-tog"); for (const [v, l] of [[true, "전체 관계"], [false, "선택한 연결"]]) { const on = mode === "all" ? v : v === allEdges, b = el("button", on ? "on" : "", l); b.type = "button"; b.disabled = mode === "all"; b.setAttribute("aria-pressed", String(on)); b.addEventListener("click", () => { allEdges = v; drawMap(); drawLegend(); }); tog.append(b); }
      legend.replaceChildren(items, el("em", ""), tog, el("em", ""), zb("−", "축소", () => bubbles.zoomBy(0.8)), zoomLab, zb("+", "확대", () => bubbles.zoomBy(1.25)), zb("⤢", "화면에 맞추기", () => bubbles.fitView()));
    }
    function paint() {
      drawMap(); // first: it drops a selection the filters now hide
      seg.replaceChildren(...[["all", "전체 지도"], ["sel", "선택한 기억"]].map(([k, l]) => { const b = el("button", `ov-map-tab${mode === k ? " on" : ""}`, l); b.type = "button"; b.disabled = k === "sel" && !lastSel; b.setAttribute("aria-pressed", String(mode === k)); b.addEventListener("click", () => { mode = k; sel = k === "sel" ? lastSel : null; moreOpen = false; paint(); }); return b; }));
      chipHost.replaceChildren(...Object.entries(REACH).map(([k, [l]]) => { const b = el("button", `ov-map-chip ov-map-${k}${reachF.has(k) ? " on" : ""}`); b.type = "button"; b.setAttribute("aria-pressed", String(reachF.has(k))); b.append(el("i", ""), document.createTextNode(l)); b.addEventListener("click", () => { reachF.has(k) ? reachF.delete(k) : reachF.add(k); paint(); }); return b; }));
      drawLegend(); drawSide();
    }
    function drawSide() {
      right.replaceChildren(); const sec = (title, ...kids) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...kids); right.append(s); return s; };
      const top = el("div", "ov-map-sidehead"); top.append(el("h4", "", sel ? "기억 상세" : "지도 요약")); if (sel) { const c = el("button", "ov-map-x", "✕"); c.type = "button"; c.setAttribute("aria-label", "닫기"); c.addEventListener("click", () => { sel = lastSel = null; mode = "all"; paint(); }); top.append(c); } right.append(top);
      if (!sel) {
        const f = el("div", "ov-detail-facts");
        for (const [a, b] of [["전체 기억", num(nodes.length)], ["이어진 선", num(edges.filter((e) => e.type !== "similar").length)]]) { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); } sec("", f);
        const list = el("div", "ov-detail-list"); for (const [r, [l]] of Object.entries(REACH)) if (groups[r]) list.append(row("", l, "", num(groups[r].length))); sec("칸", list);
        const kinds = {}; nodes.forEach((n) => { kinds[n.kind] = (kinds[n.kind] || 0) + 1; }); const kl = el("div", "ov-detail-list"); Object.entries(kinds).sort((a, b) => b[1] - a[1]).forEach(([k, n]) => kl.append(row("", kind(k), "", num(n)))); sec("종류", kl);
        if (topics.length) { const tl = el("div", "ov-detail-list"); topics.forEach((t) => { const r = row("", t.title || "주제", "", num((t.members || []).filter((m) => byId.has(m.id)).length)); r.classList.add("ov-win-click"); r.tabIndex = 0; r.setAttribute("role", "button"); r.setAttribute("aria-pressed", String(topicF === t.id));
          const pick = () => { topicF = topicF === t.id ? null : t.id; topicSet = new Set(topicF ? (t.members || []).map((m) => m.id) : []); paint(); }; r.addEventListener("click", pick); r.addEventListener("keydown", (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); pick(); } }); if (topicF === t.id) r.classList.add("on"); tl.append(r); }); sec("주제", tl); }
        right.append(el("p", "ov-win-empty", "점을 누르면 그 기억과 이어진 기억이 보여요."));
        return;
      }
      readMemory(sel);
      const m = memRec(sel) || null, n = byId.get(sel), ev = m && tag(m, "event_id:"), basis = m && tag(m, "basis:"), wait = fetched.get(sel) === null;
      const chips = el("div", "ov-map-tags"), rc = el("span", `ov-map-chip on ov-map-${reachOf(n)}`); rc.append(el("i", ""), document.createTextNode(REACH[reachOf(n)]?.[0] || "범용")); chips.append(rc, tagEl(kind(n.kind))); right.append(chips);
      right.append(el("h3", "ov-map-title", m ? clip(m.text, 60) : n.label || (wait ? "기억을 불러오는 중…" : "이 기억은 읽을 수 없어요")));
      if (n.status === "superseded") right.append(el("small", "ov-detail-basis", "● 새 기억으로 대체됨"));
      if (basis) right.append(el("small", "ov-detail-basis", basis === "explicit_user" ? "● 직접 말한 기억" : basis === "inferred" ? "● AI가 짐작한 기억" : "● 대화 원문 기반"));
      if (m) right.append((() => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", "기억 내용"), el("p", "ov-map-body-text", m.text)); return s; })());
      const u = usage(sel), used = n.uses || u?.d7;
      if (used || u?.last) { const p = el("p", "ov-map-use"); p.append(el("span", "", `${num(used || 0)}회 쓰임`), ...(u?.last ? [el("span", "", ` · ${ago(u.last)}`)] : [])); right.append(p); }
      if (ev) { const b = el("div", "ov-map-quote"), [agent, sid] = ev.split(":"), ses = env.sessions().find((x) => x.session_id === sid);
        b.append(el("small", "", `대화 · ${ses ? ses.title || ses.project || agent : agent} · ${fmt(m.created_at)}`), button("원문 대화 보기 ›", "ghost small", null, () => openSession(ev, ""))); sec("출처", b); }
      const real = rel(sel), shown = moreOpen ? real : real.slice(0, 3), ul = el("div", "ov-map-rels");
      for (const e of shown) { const other = e.from === sel ? e.to : e.from, o = byId.get(other), r = el("button", "ov-map-rel"); r.type = "button"; const tx = el("span", "ov-map-rel-t"), pr = el("span", `ov-map-chip on ov-map-${reachOf(o)}`); pr.append(el("i", ""), document.createTextNode(REACH[reachOf(o)]?.[0] || "")); tx.append(el("b", "", label(other)), pr); r.append(el("span", `ov-map-tagpill ov-map-e-${e.type === "human_link" ? "supports" : e.type}`, EDGE[e.type] || e.type), tx, el("span", "ov-map-chev", "›")); r.addEventListener("click", () => { sel = lastSel = other; moreOpen = false; paint(); }); ul.append(r); }
      const lh = el("div", "ov-map-relhead"); lh.append(el("small", "", "연결된 기억"), el("small", "", num(real.length))); right.append(lh);
      if (real.length) { right.append(ul); if (real.length > 3) { const mb = el("button", "ov-map-more", moreOpen ? "접기" : `나머지 ${real.length - 3}개 펼치기`); mb.type = "button"; mb.addEventListener("click", () => { moreOpen = !moreOpen; drawSide(); }); right.append(mb); } } else right.append(el("p", "ov-win-empty", "이어진 기억이 없어요."));
      const sim = edges.find((e) => e.type === "similar" && (e.from === sel || e.to === sel));
      if (sim) { const other = sim.from === sel ? sim.to : sim.from, box = el("div", "ov-map-suggest"); box.append(el("small", "", "AI 관계 제안"), el("p", "", `“${label(other)}”와 비슷한 기억이에요. 같은 뜻이면 기억 화면에서 합치거나 연결해 주세요.`)); right.append(box); }
      const act = el("div", "ov-map-foot"); if (m?.updated_at) act.append(el("small", "", `수정 ${fmt(m.updated_at)}`)); act.append(button("기억 수정", "small", null, () => editMemory(m, sel))); right.append(act);
    }
    let typing; search.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(() => { q = search.value.trim(); paint(); }, 150); });
    const empty = el("div", "ov-map-loading off"); empty.hidden = true; empty.append(el("p", "", "조건에 맞는 기억이 없어요."), button("조건 지우기", "small", null, () => { q = search.value = ""; topicF = null; reachF = new Set(Object.keys(REACH)); paint(); }));
    left.append(bar, canvas); canvas.append(legend, empty); paint();
  }

  return { open, wide, reset() { dlg?.close(); } };
};
