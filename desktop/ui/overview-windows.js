// MEMORY › 개요 windows (Figma 573:40303 … 573:50266 stage windows, 580:* wide windows, 592/764 improvements).
// open(key): the stage window drawn over the overview; wide(kind): the large window (review · active · sessions · usage · map).
// Everything shown comes from data the app already loaded or a documented read API; a field the server does not have is left out.
window.ArkismOverviewWindows = (ctx, env) => {
  "use strict";
  const { state, el, button, request, query, ago, go, render } = ctx;
  const HOUR = 3600000;
  const num = (n) => Number(n || 0).toLocaleString("ko-KR");
  const time = (v) => { const t = new Date(v || 0).getTime(); return Number.isNaN(t) ? 0 : t; };
  const KIND = { user: "선호", decision: "결정", lesson: "교훈", procedural: "작업 규칙", episodic: "사건", fact: "사실" };
  const kind = (k) => KIND[k] || k || "기억";
  const D = () => state.data;
  const usage = (id) => state.usage?.get(id);
  const memText = (id) => D().memories.find((m) => m.id === id)?.text || id;
  const tag = (m, p) => (m.tags || []).find((t) => t.startsWith(p))?.slice(p.length) || "";
  const clip = (t, n = 80) => (t.length > n ? `${t.slice(0, n)}…` : t);

  // ctx has no reload, so re-read the lists the review/delete changed from the server (same calls as app.js reloadMemories)
  async function listAll(path, extra) {
    const items = []; let cursor = "";
    do { const page = await request(`${path}?${query({ ...extra, limit: 100, ...(cursor ? { cursor } : {}) })}`); items.push(...(page.items || [])); cursor = page.next_cursor || ""; } while (cursor && items.length < 100000);
    return items;
  }
  async function reload() {
    try {
      const [candidates, conflicted, memories] = await Promise.all([listAll("/v1/memory_candidates", { status: "candidate" }),
        listAll("/v1/memory_candidates", { status: "conflicted" }).catch(() => []), listAll("/v1/memories", { status: "active" })]);
      Object.assign(D(), { candidates, conflicted, memories });
    } catch { /* keep the optimistic lists; the overview's own refresh tries again */ }
    render(false);
  }

  let dlg = null;
  function modal(cls) {
    dlg?.close();
    const d = el("dialog", `crew-dialog ov-win ${cls || ""}`);
    d.addEventListener("close", () => { d.remove(); if (dlg === d) dlg = null; });
    document.body.append(d); d.showModal(); dlg = d;
    return d;
  }
  const row = (tagText, title, meta, right, btns = []) => {
    const r = el("div", "ov-win-item"), t = el("div", "ov-win-item-t"), left = el("div", "ov-win-item-l");
    if (tagText) t.append(el("span", "ov-win-tag", tagText));
    t.append(el("span", "ov-win-item-title", title)); left.append(t);
    if (meta) left.append(el("small", "", meta));
    const side = el("div", "ov-win-item-r");
    if (right) side.append(el("span", "", right));
    side.append(...btns); r.append(left, side);
    return r;
  };

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
    recall: ["사용량 보기 ›", () => wide("usage")], trace: ["사용량 보기 ›", () => wide("usage")], judge: ["정리할 기억 전체 보기 ›", () => go("memories", "idle")], dream: ["정리 제안 전체 보기 ›", () => go("memories", "merge")], active: ["넓게 보기 ›", () => wide("active")] };

  function stageRows(key, box, expanded) {
    const sessions = env.sessions(), w = D().tools?.memory_use, cap = expanded ? 60 : 6, out = [];
    const rows = {
      collect: () => sessions.slice(0, cap).map((x) => row("", [x.agent, x.project].filter(Boolean).join(" · ") || x.title || "세션", `메시지 ${num(x.messages)}개 · ${ago(x.ended_at || x.started_at)}`, "", [button("열기", "small", null, () => { dlg.close(); go("sessions"); })])),
      extract: () => [...waiting(), ...D().memories].sort((a, b) => time(b.created_at) - time(a.created_at)).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), `${tag(m, "event_id:") ? "대화 저장 · " : ""}${ago(m.created_at)}`, "")),
      classify: () => waiting().filter((m) => !m.classification).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), ago(m.created_at), "", [button("승인 대기에서 보기", "small", null, () => wide("review", m.id))])),
      review: () => waiting().slice(0, cap).map((m) => reviewRow(m, box)),
      recall: () => [...(state.usage?.entries() || [])].filter(([, u]) => u.last).sort((a, b) => time(b[1].last) - time(a[1].last)).slice(0, cap).map(([id, u]) => row("", clip(memText(id)), ago(u.last), `7일 ${num(u.d7 || 0)}회`)),
      trace: () => (w?.items || []).slice().sort((a, b) => b.used - a.used || b.injected - a.injected).slice(0, cap).map((u) => row("", clip(memText(u.id)), `넣음 ${num(u.injected)}번`, `쓰임 ${num(u.used)}번`)),
      judge: () => D().memories.map((m) => [m, w?.items?.find((x) => x.id === m.id)]).filter(([, u]) => u && u.injected >= 3 && !u.used).slice(0, cap).map(([m, u]) => row("안 쓰임", clip(m.text), `${w.days}일 동안 ${num(u.injected)}번 넣었는데 한 번도 안 쓰였어요`, "", [button("정리하러 가기", "small", null, () => { dlg.close(); go("memories", "idle"); })])),
      dream: () => (D().maintenance || []).slice(0, cap).map((p) => {
        const t = p.maintenance || {}, ev = t.evidence, when = p.created_at ? ago(p.created_at) : "";
        if (t.memory) return row("고치기", clip(memText(t.memory.id)), ev ? (t.type === "lesson_often_failing" ? `실패가 잦은 교훈 · 통과율 ${Math.round((ev.pass_rate || 0) * 100)}% (${num(ev.passed)}/${num(ev.samples)})` : `자주 어기는 규칙 · 통과율 ${Math.round((ev.pass_rate || 0) * 100)}% (${num(ev.passed)}/${num(ev.samples)})`) : t.type === "lesson_often_failing" ? "실패가 잦은 교훈" : "자주 어기는 규칙", when, []);
        return row({ duplicate: "합치기", contradiction: "고치기" }[t.type] || "정리", clip(memText(t.keep?.id)), `→ ${clip(memText(t.retire?.id), 40)}`, when, []);
      }),
      active: () => D().memories.slice().sort((a, b) => (usage(b.id)?.d7 || 0) - (usage(a.id)?.d7 || 0)).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), tag(m, "repo:") || ago(m.created_at), usage(m.id) ? `7일 ${num(usage(m.id).d7 || 0)}번 쓰임` : "", []))
    };
    return (rows[key]?.() || out);
  }
  function reviewRow(c, box) {
    const r = row(kind(c.kind), clip(c.text), `${c.classification?.risk_reasons?.[0] || ""}${c.classification?.risk_reasons?.length ? " · " : ""}${ago(c.created_at)}`, "");
    const side = r.lastChild, note = el("small", "ov-win-err", "");
    const ok = button("승인", "primary small", null, async () => { ok.disabled = no.disabled = true; const res = await decide(c, "approve"); if (res?.error) { note.textContent = res.error; ok.disabled = no.disabled = false; } else { r.remove(); } });
    const no = button("버리기", "small", null, () => askReason("후보를 버릴까요?", async (reason) => { ok.disabled = no.disabled = true; const res = await decide(c, "reject", { reason }); if (res?.error) { note.textContent = res.error; ok.disabled = no.disabled = false; } else r.remove(); }));
    side.append(ok, no); r.firstChild.append(note);
    return r;
  }

  function open(key, expanded = false) {
    const list = env.stages(), i = list.findIndex((x) => x.key === key), s = i >= 0 ? list[i] : activeStage();
    const spec = SPEC[key];
    const d = modal(`ov-stage-win ov-t-${s.badge?.[1] || "n"}`);
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
    if (key === "dream") body.append(note("n", "드리밍 제안도 승인 대기로 가요", "바로 바뀌지 않아요. 승인해야 반영돼요."));
    const rows = stageRows(key, body, expanded), total = expandTotal(key);
    body.append(el("div", "ov-win-list-title", LIST_TITLE[key]));
    const ul = el("div", "ov-win-list");
    if (!rows.length) ul.append(el("p", "ov-win-empty", "지금 보여 줄 항목이 없어요."));
    ul.append(...rows); body.append(ul);
    const foot = el("div", "ov-win-foot");
    if (expanded) { ul.classList.add("ov-win-list-scroll"); foot.append(el("span", "ov-wide-foot-note", `${num(total)}건 중 ${num(rows.length)}건 · 창 안에서 내리면 더 나와요`), button("접기 ⌃", "ghost small", null, () => open(key, false))); }
    else if (total && total > rows.length) foot.append(button(`${num(total)}건 모두 보기 ›`, "ghost small", null, () => open(key, true)));
    else if (FULL[key]) foot.append(button(FULL[key][0], "ghost small", null, FULL[key][1]));
    if (expanded && FULL[key]) foot.append(button(FULL[key][0], "ghost small", null, FULL[key][1]));
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
    const r = el("div", "ov-win-step"); if (hint) r.append(el("span", "", hint)); r.append(button("닫기", "ghost small", null, () => d.close()));
    head.append(t, r); d.append(head);
    return d;
  }
  const chipBar = (items, cur, set) => { const bar = el("div", "ov-wide-chips"); for (const [k, l, n] of items) { const b = el("button", `ov-wide-chip${k === cur ? " on" : ""}`); b.type = "button"; b.append(document.createTextNode(l), ...(n == null ? [] : [el("small", "", ` ${num(n)}`)])); b.addEventListener("click", () => set(k)); bar.append(b); } return bar; };
  function twoPane(d, bar, listBox, detail) {
    const body = el("div", "ov-wide-body"), left = el("div", "ov-wide-left"), right = el("div", "ov-wide-right");
    left.append(bar, listBox); right.append(detail); body.append(left, right); d.append(body);
  }

  function wide(kindKey, focusId) {
    ({ review: wideReview, active: wideActive, sessions: wideSessions, usage: wideUsage, map: wideMap }[kindKey])(focusId);
  }

  // 승인 대기 (580:49518 + improvements 592:51366 / 764:61615)
  function wideReview(focusId) {
    const all = waiting(), d = shell("승인 대기", `${num(all.length)}건`, "AI는 분류만 해요. 승인은 사람이 해요.", () => { open("review"); });
    let q = "", kindF = "all", sel = focusId || all[0]?.id; const checked = new Set();
    const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), more = el("div", "ov-wide-foot-note");
    const kinds = [...new Set(all.map((c) => c.kind))], merges = new Map();
    request(`/v1/merge_proposals?${query({ status: "open" })}`).then((r) => { for (const x of r?.items || []) { const pr = x.merge?.proposal; if (pr?.action === "merge" && pr.candidate?.id) merges.set(pr.candidate.id, x); } if (merges.size && d.isConnected) paint(); }, () => {});
    const chipHost = el("div");
    const search = el("input", "ov-wide-search"); search.placeholder = "후보 찾기"; search.setAttribute("aria-label", "후보 찾기");
    const batch = button("선택 0개 승인", "small", null, async () => { const items = all.filter((c) => checked.has(c.id)); if (!items.length) return; batch.disabled = true; let fail = 0; for (const c of items) { const r = await decide(c, "approve", {}, true); if (r?.error) fail++; else checked.delete(c.id); } await reload(); wideReview(); if (fail) dlg.querySelector(".ov-wide-foot-note").textContent = `${fail}건은 처리하지 못했어요.`; });
    batch.disabled = true;
    const bar = el("div", "ov-wide-bar"); bar.append(search, chipHost, batch);
    const filtered = () => waiting().filter((c) => (kindF === "all" || c.kind === kindF) && (!q || c.text.includes(q)));
    const drawChips = () => chipHost.replaceChildren(chipBar([["all", "전체", all.length], ...kinds.map((k) => [k, kind(k), all.filter((c) => c.kind === k).length])], kindF, (k) => { kindF = k; paint(); }));
    function paint() {
      drawChips(); const items = filtered(); list.replaceChildren();
      if (!items.length) list.append(el("p", "ov-win-empty", "지금 보여 줄 후보가 없어요."));
      for (const c of items.slice(0, 80)) {
        const r = el("div", `ov-wide-row${c.id === sel ? " on" : ""}`), cb = el("input"); cb.type = "checkbox"; cb.checked = checked.has(c.id); cb.setAttribute("aria-label", "선택");
        cb.addEventListener("click", (e) => e.stopPropagation()); cb.addEventListener("change", () => { cb.checked ? checked.add(c.id) : checked.delete(c.id); batch.textContent = `선택 ${checked.size}개 승인`; batch.disabled = !checked.size; });
        const t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t"); l1.append(el("span", "ov-win-tag", kind(c.kind)), el("span", "ov-win-item-title", clip(c.text, 70)));
        t.append(l1, el("small", "", c.classification?.risk_reasons?.[0] || tag(c, "repo:") || (c.conflict_id ? "충돌 중" : "")));
        r.append(cb, t, el("span", "ov-wide-row-r", ago(c.created_at))); r.addEventListener("click", () => { sel = c.id; paint(); }); list.append(r);
      }
      more.textContent = `${Math.min(items.length, 80)} / ${items.length}`;
      const cur = items.find((c) => c.id === sel) || items[0]; sel = cur?.id; list.querySelectorAll(".ov-wide-row").forEach((rw, k) => rw.classList.toggle("on", items[k]?.id === sel)); detailFor(cur);
    }
    search.addEventListener("input", () => { q = search.value.trim(); paint(); });
    function detailFor(c) {
      detail.replaceChildren();
      if (!c) { detail.append(el("p", "ov-win-empty", "고를 후보가 없어요.")); return; }
      const h = el("div", "ov-detail-head"); h.append(el("span", "ov-win-tag", kind(c.kind)), el("small", "", `${c.classification?.by ? "AI 분류 · " : ""}${ago(c.created_at)}`));
      const edit = el("textarea", "ov-detail-edit"); edit.value = c.text; edit.rows = 3; edit.hidden = true; edit.setAttribute("aria-label", "고쳐서 저장할 내용");
      detail.append(h, el("h3", "", c.text), edit);
      const sec = (title, ...nodes) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...nodes); detail.append(s); };
      if (c.summary) sec("요약", el("p", "", c.summary));
      const k = c.classification;
      if (k?.risk_reasons?.length) sec(`AI 분류 · 위험도 ${k.risk}`, el("p", "", k.risk_reasons.join(" · ")));
      if (k?.evidence_span) { const q2 = el("blockquote", "ov-detail-quote", `“${clip(k.evidence_span, 240)}”`); sec("원문 근거", q2); }
      if (k?.dup_hint) sec("비슷한 기억", el("p", "ov-detail-dup", k.dup_hint));
      const rs = c.reach_suggestion;
      if (rs) { const chips = el("div", "ov-wide-chips"); for (const [v, l] of [["personal", "개인"], ["general", "범용"], ["project", "이 프로젝트만"]]) chips.append(el("span", `ov-wide-chip${v === rs.reach ? " on" : ""}`, v === rs.reach ? `${l} · AI 추천` : l)); sec("AI 칸 제안 · 정하는 건 사람이에요", chips, el("p", "", rs.reason || "")); }
      const ev = tag(c, "event_id:"); if (ev) { const b = el("div", "ov-detail-from"); b.append(el("span", "", ev.split(":")[0]), button("세션 보기 ›", "ghost small", null, () => { dlg.close(); go("sessions"); })); sec("어디서 왔나요", b); }
      const mp = merges.get(c.id);
      if (mp) {
        const pr = mp.merge.proposal, mb = el("div", "ov-detail-list");
        for (const t of pr.targets || []) mb.append(row("기존", clip(memText(t.id), 60), "", ""));
        mb.append(button("합치기", "small", null, async () => {
          mb.querySelectorAll("button").forEach((b) => (b.disabled = true));
          try { await request(`/v1/merge_proposals/${encodeURIComponent(mp.id)}/decide`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: mp.revision, decision: "accept" }) }); merges.delete(c.id); sel = null; await reload(); wideReview(); }
          catch (e) { st.textContent = e.message === "revision_conflict" ? "후보나 대상 기억이 바뀌어 이 제안은 낡았어요." : `합치지 못했어요: ${e.message}`; mb.querySelectorAll("button").forEach((b) => (b.disabled = false)); }
        }));
        sec(pr.reason ? `AI 합치기 제안 · ${pr.reason}` : "AI 합치기 제안", mb);
      }
      sec("승인하면", el("p", "", `쓰는 중인 기억에 들어가요${rs ? ` · ${{ personal: "개인", general: "범용", project: "프로젝트" }[rs.reach] || ""} 칸 제안` : ""}`));
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      const go1 = async (action, extra) => { act.querySelectorAll("button").forEach((b) => (b.disabled = true)); const r = await decide(c, action, extra); if (r?.error) { st.textContent = r.error; act.querySelectorAll("button").forEach((b) => (b.disabled = false)); } else { sel = null; wideReview(); } };
      if (c.conflict_id || c.status === "conflicted") { sec("충돌", el("p", "", "기존 기억과 충돌하는 후보예요. 어느 쪽을 남길지는 전체 화면에서 골라요.")); act.append(button("전체 화면으로", "primary small", null, () => { dlg.close(); go("memories", "conflicted"); })); }
      else {
        act.append(button("따로 승인", "primary small", null, () => go1("approve")),
          button("고쳐서 승인", "small", null, () => { if (edit.hidden) { edit.hidden = false; edit.focus(); return; } if (!edit.value.trim()) return; go1("edit_approve", { content: edit.value.trim() }).catch(() => {}); }),
          button("버리기", "small", null, () => askReason("후보를 버릴까요?", (reason) => go1("reject", { reason }))));
      }
      detail.append(st, act);
    }
    twoPane(d, bar, list, detail); d.lastChild.querySelector(".ov-wide-left").append(more); paint();
  }

  // 쓰는 중인 기억 (579:48597)
  function wideActive() {
    const d = shell("쓰는 중인 기억", `${num(D().memories.length)}개`, "승인된 기억만 불러오기에 쓰여요", () => open("active"));
    let q = "", kindF = "all", sel = null, sort = "used";
    const mems = () => D().memories, list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), chipHost = el("div");
    const search = el("input", "ov-wide-search"); search.placeholder = "기억 찾기"; search.setAttribute("aria-label", "기억 찾기");
    const sorter = button("많이 쓰인 순", "ghost small", null, () => { sort = sort === "used" ? "new" : "used"; sorter.textContent = sort === "used" ? "많이 쓰인 순" : "최신 순"; paint(); });
    const bar = el("div", "ov-wide-bar"); bar.append(search, chipHost, sorter);
    const w = D().tools?.memory_use, kinds = [...new Set(mems().map((m) => m.kind))];
    const idle = (m) => { const u = w?.items?.find((x) => x.id === m.id); return u && u.injected >= 3 && !u.used; };
    function paint() {
      chipHost.replaceChildren(chipBar([["all", "전체", mems().length], ...kinds.map((k) => [k, kind(k), mems().filter((m) => m.kind === k).length]), ...(w ? [["idle", "안 쓰임", mems().filter(idle).length]] : [])], kindF, (k) => { kindF = k; paint(); }));
      const items = mems().filter((m) => (kindF === "all" || (kindF === "idle" ? idle(m) : m.kind === kindF)) && (!q || m.text.includes(q)))
        .sort((a, b) => (sort === "used" ? (usage(b.id)?.d7 || 0) - (usage(a.id)?.d7 || 0) : time(b.created_at) - time(a.created_at)));
      list.replaceChildren(); if (!items.length) list.append(el("p", "ov-win-empty", "찾은 기억이 없어요."));
      for (const m of items.slice(0, 80)) {
        const r = el("div", `ov-wide-row${m.id === sel ? " on" : ""}`), t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t"); l1.append(el("span", "ov-win-tag", kind(m.kind)), el("span", "ov-win-item-title", clip(m.text, 70)));
        t.append(l1, el("small", "", [tag(m, "repo:"), ago(m.created_at)].filter(Boolean).join(" · ")));
        const u = usage(m.id); r.append(t, el("span", "ov-wide-row-r", u ? (u.d7 ? `7일 ${num(u.d7)}번 쓰임` : "안 쓰임") : ""));
        r.addEventListener("click", () => { sel = m.id; paint(); }); list.append(r);
      }
      const cur = items.find((m) => m.id === sel) || items[0]; sel = cur?.id; list.querySelectorAll(".ov-wide-row").forEach((rw, k) => rw.classList.toggle("on", items[k]?.id === sel)); detailFor(cur);
    }
    search.addEventListener("input", () => { q = search.value.trim(); paint(); });
    function detailFor(m) {
      detail.replaceChildren(); if (!m) return;
      const h = el("div", "ov-detail-head"); h.append(el("span", "ov-win-tag", kind(m.kind)), el("small", "", `${m.created_by_type === "agent" ? "AI가 모음" : "사람이 만듦"} · ${ago(m.created_at)}`));
      detail.append(h, el("h3", "", m.text));
      const sec = (title, ...nodes) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...nodes); detail.append(s); };
      const u = usage(m.id), wu = w?.items?.find((x) => x.id === m.id), f = el("div", "ov-detail-facts");
      const fact = (a, b) => { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); };
      if (u) { fact("최근 7일", `${num(u.d7 || 0)}번 쓰임`); if (u.last) fact("마지막으로 쓰임", ago(u.last)); }
      if (wu) fact(`최근 ${w.days}일`, `${num(wu.injected)}번 넣음 · ${num(wu.used)}번 실제로 쓰임`);
      if (f.children.length) sec("언제 쓰이나요", f);
      const ev = tag(m, "event_id:"); if (ev) { const b = el("div", "ov-detail-from"); b.append(el("span", "", ev.split(":")[0]), button("세션 보기 ›", "ghost small", null, () => { dlg.close(); go("sessions"); })); sec("어디서 왔나요", b); }
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      act.append(button("고치기", "primary small", null, () => { dlg.close(); go("memories", "active"); }),
        button("지우기", "small", null, () => askConfirm("이 기억을 지울까요?", "지운 기억은 이력에 남지만 더는 쓰이지 않아요.", async () => {
          try { await request(`/v1/memories/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) }); D().memories = D().memories.filter((x) => x.id !== m.id); sel = null; await reload(); wideActive(); }
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

  // 세션 기록 (580:51861)
  function wideSessions() {
    const items = env.sessions(), today = items.filter((x) => time(x.ended_at || x.started_at) >= new Date().setHours(0, 0, 0, 0)).length;
    const d = shell("세션 기록", `오늘 ${num(today)}건`, "저장된 대화예요. 원문은 여기서만 열려요", () => open("collect"));
    let q = "", agent = "all", sel = items[0]?.id; const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), chipHost = el("div");
    const search = el("input", "ov-wide-search"); search.placeholder = "세션 찾기"; search.setAttribute("aria-label", "세션 찾기");
    const bar = el("div", "ov-wide-bar"); bar.append(search, chipHost, button("새로고침", "ghost small", null, async () => { await env.reloadSessions(); wideSessions(); }));
    const agents = [...new Set(items.map((x) => x.agent).filter(Boolean))];
    function paint() {
      chipHost.replaceChildren(chipBar([["all", "전체", items.length], ...agents.map((a) => [a, a, items.filter((x) => x.agent === a).length])], agent, (k) => { agent = k; paint(); }));
      const shown = items.filter((x) => (agent === "all" || x.agent === agent) && (!q || `${x.title} ${x.project}`.includes(q)));
      list.replaceChildren(); if (!shown.length) list.append(el("p", "ov-win-empty", "찾은 세션이 없어요."));
      for (const x of shown.slice(0, 80)) {
        const r = el("div", `ov-wide-row${x.id === sel ? " on" : ""}`), t = el("div", "ov-wide-row-t");
        t.append(el("span", "ov-win-item-title", x.title || x.session_id), el("small", "", [x.agent, x.project, `메시지 ${num(x.messages)}개`].filter(Boolean).join(" · ")));
        r.append(t, el("span", "ov-wide-row-r", ago(x.ended_at || x.started_at))); r.addEventListener("click", () => { sel = x.id; paint(); }); list.append(r);
      }
      const cur = shown.find((x) => x.id === sel) || shown[0]; sel = cur?.id; list.querySelectorAll(".ov-wide-row").forEach((rw, k) => rw.classList.toggle("on", shown[k]?.id === sel));
      detail.replaceChildren(); if (!cur) return;
      const h = el("div", "ov-detail-head"); h.append(el("span", "ov-win-tag", cur.agent || "세션"), el("small", "", `${cur.project || ""} · ${new Date(cur.started_at).toLocaleString("ko-KR", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}`));
      detail.append(h, el("h3", "", cur.title || cur.session_id));
      const f = el("div", "ov-detail-facts"); const fact = (a, b) => { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); };
      fact("메시지", `${num(cur.messages)}개`); if (cur.active_seconds) fact("작업 시간", `${Math.round(cur.active_seconds / 60)}분`); if (cur.tokens) fact("토큰", num(cur.tokens)); if (cur.requests) fact("요청", `${num(cur.requests)}회`); if (cur.device) fact("기기", cur.device);
      const s = el("div", "ov-detail-sec"); s.append(el("small", "", "이 세션"), f); detail.append(s);
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      act.append(button("대화 원문 보기", "primary small", null, () => { dlg.close(); go("sessions"); }),
        button("저장 안 함", "small", null, () => askConfirm("이 세션을 지울까요?", "세션과 보관된 대화가 지워지고 되돌릴 수 없어요. 에이전트의 원래 파일은 그대로 남고, 이 대화를 출처로 둔 기억은 내용은 남고 출처 연결만 사라져요.", async () => {
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

  // 기억 지도 (611:54134 · 611:56374): GET /v1/memory-graph?mode=all, grouped by 칸 (reach), lines between memories
  const REACH = { personal: ["개인", 0], general: ["범용", 1], project: ["프로젝트", 2] };
  const EDGE = { supports: "지지", supersedes: "대체", conflicts: "충돌", human_link: "연결", same_session: "관련", similar: "비슷" };
  async function wideMap() {
    const d = shell("기억 지도", "", "", null); // 611:54134 has no small window, only ✕
    const body = el("div", "ov-wide-body"), left = el("div", "ov-wide-left ov-map-left"), right = el("div", "ov-wide-right ov-wide-detail");
    const status = el("p", "ov-win-empty", "지도를 불러오는 중…"); left.append(status); body.append(left, right); d.append(body);
    let graph; try { graph = await request(`/v1/memory-graph?${query({ mode: "all" })}`); } catch (e) { status.textContent = `지도를 불러오지 못했어요: ${e.message}`; return; }
    const nodes = graph.nodes || [], edges = graph.edges || [];
    if (!nodes.length) { status.textContent = "지도에 그릴 기억이 아직 없어요."; return; }
    status.remove();
    let mode = "all", sel = null, q = "", reachF = new Set(Object.keys(REACH)), topicF = null;
    const topics = (await request(`/v1/digests?${query({ kind: "topic", status: "active" })}`).catch(() => null))?.items || [];
    const inTopic = (id) => !topicF || (topics.find((t) => t.id === topicF)?.members || []).some((r) => r.id === id);
    const byId = new Map(nodes.map((n) => [n.id, n]));
    const bar = el("div", "ov-wide-bar"), seg = el("div", "ov-wide-seg"), search = el("input", "ov-wide-search"); search.placeholder = "기억 찾기"; search.setAttribute("aria-label", "기억 찾기");
    const chipHost = el("div"); bar.append(seg, search, chipHost, button("작업 관계 보기", "ghost small", null, () => { dlg.close(); go("memories", "relations"); }));
    const SVGNS = "http://www.w3.org/2000/svg", canvas = el("div", "ov-map-canvas");
    // layout: one cluster per reach, nodes on a deterministic spiral (index based, no randomness)
    const W = 760, H = 560, centers = { personal: [W * 0.27, H * 0.38], general: [W * 0.7, H * 0.33], project: [W * 0.5, H * 0.78] };
    const pos = new Map(), groups = {}; nodes.forEach((n) => { (groups[n.reach || "general"] ||= []).push(n); });
    for (const [r, list] of Object.entries(groups)) { const [cx, cy] = centers[r] || [W / 2, H / 2], step = Math.min(26, 150 / Math.sqrt(list.length || 1)); list.forEach((n, i) => { const a = i * 2.399963, rad = step * Math.sqrt(i + 0.5); pos.set(n.id, [cx + Math.cos(a) * rad, cy + Math.sin(a) * rad * 0.85]); }); }
    const label = (id) => clip(D().memories.find((m) => m.id === id)?.text || kind(byId.get(id)?.kind), 26);
    function drawMap() {
      canvas.replaceChildren();
      const svg = document.createElementNS(SVGNS, "svg"); svg.setAttribute("viewBox", `0 0 ${W} ${H}`); svg.setAttribute("class", "ov-map-svg");
      const near = new Set(); if (mode === "sel" && sel) { edges.forEach((e) => { if (e.from === sel) near.add(e.to); if (e.to === sel) near.add(e.from); }); near.add(sel); }
      const vis = (n) => reachF.has(n.reach || "general") && inTopic(n.id) && (mode === "all" || near.has(n.id)) && (!q || label(n.id).includes(q));
      for (const [r, [cx, cy]] of Object.entries(centers)) { if (!groups[r] || !reachF.has(r) || mode === "sel") continue; const c = document.createElementNS(SVGNS, "ellipse"); c.setAttribute("cx", cx); c.setAttribute("cy", cy); c.setAttribute("rx", 40 + Math.sqrt(groups[r].length) * 17); c.setAttribute("ry", 34 + Math.sqrt(groups[r].length) * 14); c.setAttribute("class", `ov-map-ring ov-map-${r}`); svg.append(c); const t = document.createElementNS(SVGNS, "text"); t.setAttribute("x", cx); t.setAttribute("y", cy - 46 - Math.sqrt(groups[r].length) * 14); t.setAttribute("class", "ov-map-label"); t.setAttribute("text-anchor", "middle"); t.textContent = `${REACH[r]?.[0] || r} ${groups[r].length}`; svg.append(t); }
      for (const e of edges) { if (mode === "sel" && sel && e.from !== sel && e.to !== sel) continue; const a = pos.get(e.from), b = pos.get(e.to); if (!a || !b || !vis(byId.get(e.from)) || !vis(byId.get(e.to))) continue; const l = document.createElementNS(SVGNS, "line"); l.setAttribute("x1", a[0]); l.setAttribute("y1", a[1]); l.setAttribute("x2", b[0]); l.setAttribute("y2", b[1]); l.setAttribute("class", `ov-map-edge ov-map-e-${e.type}`); svg.append(l); }
      for (const n of nodes) { if (!vis(n)) continue; const p = pos.get(n.id), c = document.createElementNS(SVGNS, "circle"); c.setAttribute("cx", p[0]); c.setAttribute("cy", p[1]); c.setAttribute("r", n.id === sel ? 8 : 2.5 + Math.min(4, Math.sqrt(n.uses || 0))); c.setAttribute("class", `ov-map-dot ov-map-${n.reach || "general"}${n.id === sel ? " on" : ""}`);
        c.addEventListener("click", () => { sel = n.id; mode = "sel"; paint(); }); const tt = document.createElementNS(SVGNS, "title"); tt.textContent = label(n.id); c.append(tt); svg.append(c);
        if (mode === "sel") { const t = document.createElementNS(SVGNS, "text"); t.setAttribute("x", p[0] + 10); t.setAttribute("y", p[1] + 3); t.setAttribute("class", "ov-map-label"); t.textContent = label(n.id); svg.append(t); } }
      canvas.append(svg);
    }
    function paint() {
      seg.replaceChildren(...[["all", "전체 지도"], ["sel", "선택한 기억"]].map(([k, l]) => { const b = el("button", `ov-wide-chip${mode === k ? " on" : ""}`, l); b.type = "button"; b.disabled = k === "sel" && !sel; b.addEventListener("click", () => { mode = k; paint(); }); return b; }));
      chipHost.replaceChildren(chipBar(Object.entries(REACH).map(([k, [l]]) => [k, l, (groups[k] || []).length]).map(([k, l, n]) => [k, `${reachF.has(k) ? "● " : "○ "}${l}`, n]), "", (k) => { reachF.has(k) ? reachF.delete(k) : reachF.add(k); paint(); }));
      drawMap(); drawSide();
    }
    function drawSide() {
      right.replaceChildren(); const sec = (title, ...nodes) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...nodes); right.append(s); };
      if (!sel) {
        right.append(el("h3", "", "지도 요약")); const f = el("div", "ov-detail-facts");
        for (const [a, b] of [["전체 기억", num(nodes.length)], ["이어진 선", num(edges.length)]]) { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); } sec("", f);
        const list = el("div", "ov-detail-list"); for (const [r, [l]] of Object.entries(REACH)) if (groups[r]) list.append(row("", l, "", num(groups[r].length))); sec("칸", list);
        const kinds = {}; nodes.forEach((n) => { kinds[n.kind] = (kinds[n.kind] || 0) + 1; }); const kl = el("div", "ov-detail-list"); Object.entries(kinds).sort((a, b) => b[1] - a[1]).forEach(([k, n]) => kl.append(row("", kind(k), "", num(n)))); sec("종류", kl);
        if (topics.length) { const tl = el("div", "ov-detail-list"); topics.forEach((t) => { const r = row("", t.title || "주제", "", num((t.members || []).length)); r.classList.add("ov-win-click"); r.addEventListener("click", () => { topicF = topicF === t.id ? null : t.id; paint(); }); if (topicF === t.id) r.classList.add("on"); tl.append(r); }); sec("주제", tl); }
        if (graph.truncated) right.append(el("p", "ov-win-empty", "기억이 많아 일부만 그렸어요."));
        right.append(el("p", "ov-win-empty", "점을 누르면 그 기억과 이어진 기억이 보여요."));
        return;
      }
      const m = D().memories.find((x) => x.id === sel), n = byId.get(sel);
      const h = el("div", "ov-detail-head"); h.append(el("span", "ov-win-tag", REACH[n.reach]?.[0] || "범용"), el("span", "ov-win-tag", kind(n.kind)), button("닫기", "ghost small", null, () => { sel = null; mode = "all"; paint(); })); right.append(h, el("h3", "", m?.text || "이 기억의 내용은 지도에 없어요"));
      const u = usage(sel); if (u) { const f = el("div", "ov-detail-facts"); const p = el("p", ""); p.append(el("span", "", "최근 7일"), el("strong", "", `${num(u.d7 || 0)}번 쓰임`)); f.append(p); sec("쓰임", f); }
      const links = edges.filter((e) => e.from === sel || e.to === sel), ul = el("div", "ov-detail-list");
      for (const e of links.slice(0, 12)) { const other = e.from === sel ? e.to : e.from; const r = row(EDGE[e.type] || e.type, label(other), REACH[byId.get(other)?.reach]?.[0] || "", "", []); r.classList.add("ov-win-click"); r.addEventListener("click", () => { sel = other; paint(); }); ul.append(r); }
      sec(`연결된 기억 ${links.length}`, links.length ? ul : el("p", "ov-win-empty", "이어진 기억이 없어요."));
      const act = el("div", "ov-detail-actions"); act.append(button("기억 화면에서 보기", "small", null, () => { dlg.close(); go("memories", "active"); })); right.append(act);
    }
    search.addEventListener("input", () => { q = search.value.trim(); drawMap(); });
    left.append(bar, canvas); paint();
  }

  return { open, wide };
};
