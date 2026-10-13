// 세션 (magpie Sessions): every agent session the collector counted, newest first, with its
// models·tokens·tools·skills on the right. GET /v1/usage/sessions. Counts only — no prompt or answer.
// 대화 보기 reads the archived dialogue (POST /v1/usage/sessions/transcript: user·assistant text only,
// secrets and <private> never shown); 내보내기 (…/export) downloads Markdown or JSON; 지우기 (…/delete)
// removes the selected sessions and their archived transcripts, never the agent's own files.
window.ArkismSessions = (ctx) => {
  "use strict";
  const { state, $, el, button, segment, render, request, query, ago, detailShell, usage } = ctx;
  const { num, usd, dur, logo, agentName, bars } = usage.fmt;
  const RANGES = [["today", "오늘"], ["7d", "7일"], ["30d", "30일"], ["90d", "90일"], ["all", "전체"]];
  const S = { range: "7d", source: "", q: "", sort: "recent", selecting: false, doc: null, state: "idle", error: "", selected: "", picked: new Set(), busy: false, notice: "", tx: null };
  let epoch = 0, typing = 0, txEpoch = 0;

  async function load(silent = false) {
    const quiet = silent === true;
    if (quiet && S.state !== "ready") return;
    const ticket = ++epoch;
    if (!quiet) { S.state = "loading"; S.error = ""; paint(); }
    try {
      const doc = await request(`/v1/usage/sessions?${query({ range: S.range, limit: 300, ...(S.source ? { source: S.source } : {}), ...(S.q ? { q: S.q } : {}) })}`);
      if (ticket !== epoch) return;
      if (doc?.authority !== "data_only_untrusted" || !Array.isArray(doc.items)) throw new Error("invalid_response");
      if (quiet && JSON.stringify(S.doc) === JSON.stringify(doc)) return;
      S.doc = doc; S.state = "ready";
      const live = new Set(doc.items.map((x) => x.id));
      for (const id of [...S.picked]) if (!live.has(id)) S.picked.delete(id);
    } catch (e) {
      if (ticket !== epoch || quiet) return;
      S.doc = null; S.state = /HTTP 404|not_found/.test(e?.message || "") ? "unsupported" : "error"; S.error = e.message;
    }
    paint();
  }
  const paint = () => { if (state.view === "sessions") render(false); };
  const when = (t) => new Date(t).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });

  // ---------------------------------------------------------------- 대화 보기
  async function transcript(id, more = false) {
    const ticket = ++txEpoch;
    const prev = more && S.tx?.id === id ? S.tx : null;
    S.tx = { id, items: prev ? prev.items : [], hidden: prev ? prev.hidden : 0, state: prev ? prev.state : "", archives: prev ? prev.archives : [],
      archive: prev ? prev.archive : "", next: prev ? prev.next : "", loading: true, error: "" };
    paint();
    try {
      const body = { scope: state.scope, id, limit: 30, ...(prev?.next ? { archive_id: prev.archive, after: prev.next } : {}) };
      const r = await request("/v1/usage/sessions/transcript", { method: "POST", body: JSON.stringify(body) });
      if (ticket !== txEpoch) return;
      if (r?.authority !== "data_only_untrusted" || !Array.isArray(r.items)) throw new Error("invalid_response");
      Object.assign(S.tx, { state: r.state, archives: r.archives || [], archive: r.archive_id || "", next: r.next_cursor || "",
        items: [...S.tx.items, ...r.items], hidden: S.tx.hidden + (r.hidden?.secret_records || 0), loading: false });
    } catch (e) {
      if (ticket !== txEpoch) return;
      S.tx.loading = false;
      S.tx.error = /HTTP 404|not_found/.test(e?.message || "") && !S.tx.items.length ? "unsupported" : e.message;
    }
    paint();
  }

  function transcriptCard(it, card) {
    const tx = S.tx?.id === it.id ? S.tx : null;
    if (!tx) return null;
    const kids = [];
    const toolCount = (it.tools || []).reduce((n, x) => n + x.count, 0);
    if (toolCount) {
      const d = el("details", "ss-tools");
      d.append(el("summary", "", `도구 호출 ${num(toolCount)}회 (접힘)`));
      const ul = el("ul");
      for (const x of it.tools) { const li = el("li", "", `${x.name} × ${x.count}`); li.dataset.noI18n = ""; ul.append(li); }
      d.append(ul);
      kids.push(d);
    }
    if (tx.error) kids.push(el("p", "us-empty", tx.error === "unsupported" ? "이 서버는 아직 대화 보기를 지원하지 않아요." : `대화를 불러오지 못했어요 (${tx.error}).`));
    else if (!tx.loading && tx.state === "none") kids.push(el("p", "us-empty", "이 세션은 보관된 대화가 없어요. 대화 원문은 수집된 뒤 7일 동안만 보관돼요."));
    else if (!tx.loading && tx.state === "expired") kids.push(el("p", "us-empty", "보관된 대화가 만료됐거나 지워졌어요."));
    else if (!tx.loading && tx.state === "unsupported") kids.push(el("p", "us-empty", "이 형식의 대화는 아직 볼 수 없어요."));
    const msgs = el("div", "ss-msgs");
    for (const m of tx.items) {
      const box = el("div", `ss-msg ${m.role === "user" ? "user" : "assistant"}`);
      const text = el("p", "", m.text); text.dataset.noI18n = "";
      box.append(el("b", "", m.role === "user" ? "사용자" : "에이전트"), text);
      if (m.truncated) box.append(el("small", "ss-note", "길어서 앞부분만 보여요."));
      msgs.append(box);
    }
    if (tx.items.length) kids.push(msgs);
    if (tx.state === "active" && !tx.items.length && !tx.next && !tx.loading) kids.push(el("p", "us-empty", "보여 줄 대화가 없어요."));
    if (tx.hidden) kids.push(el("p", "ss-note", `비밀값이 들어 있어 숨긴 메시지 ${tx.hidden}개`));
    if (tx.archives.length > 1) kids.push(el("p", "ss-note", `이 세션에는 보관본이 ${tx.archives.length}개 있어요. 가장 오래된 보관본을 보여 줘요.`));
    if (tx.loading) kids.push(el("p", "us-empty", "불러오는 중…"));
    else if (tx.next) kids.push(button("메시지 더 보기", "", null, () => transcript(it.id, true)));
    return card("대화", ...kids);
  }

  // ---------------------------------------------------------------- 내보내기 · 지우기
  async function exportSessions(ids, format) {
    if (!ids.length || S.busy) return;
    S.busy = true; S.notice = "내보내는 중…"; paint();
    let url = "";
    try {
      const r = await request("/v1/usage/sessions/export", { method: "POST", body: JSON.stringify({ scope: state.scope, ids, format, transcript: true }) });
      if (typeof r?.content !== "string") throw new Error("invalid_response");
      url = URL.createObjectURL(new Blob([r.content], { type: r.content_type || "text/plain" }));
      const link = el("a"); link.href = url; link.download = r.filename || (format === "json" ? "arkism-sessions.json" : "arkism-sessions.md"); link.click();
      S.notice = r.truncated ? `세션 ${r.sessions}개를 내보냈어요. 너무 길어 일부 대화는 잘렸어요.` : `세션 ${r.sessions}개를 내보냈어요.`;
    } catch (e) {
      S.notice = `내보내지 못했어요 (${e.message}).`;
    } finally {
      if (url) setTimeout(() => URL.revokeObjectURL(url), 1000);
      S.busy = false; paint();
    }
  }

  async function deleteSessions(ids) {
    if (!ids.length || S.busy) return;
    if (!confirm(`세션 ${ids.length}개와 보관된 대화를 지울까요? 되돌릴 수 없어요.\n\n에이전트의 원래 파일은 그대로 남아요. 이 대화를 출처로 둔 기억은 내용은 남고 출처 연결만 사라져요.`)) return;
    S.busy = true; S.notice = "지우는 중…"; paint();
    try {
      const r = await request("/v1/usage/sessions/delete", { method: "POST", body: JSON.stringify({ scope: state.scope, ids }) });
      const gone = new Set([...(r.deleted || []), ...(r.missing || [])]);
      for (const id of gone) S.picked.delete(id);
      if (gone.has(S.selected)) { S.selected = ""; S.tx = null; }
      S.notice = `세션 ${(r.deleted || []).length}개를 지웠어요 (보관된 대화 ${r.archives || 0}개).`;
      S.busy = false;
      await load();
      return;
    } catch (e) {
      S.notice = /access_denied/.test(e?.message || "") ? "지울 권한이 없어요." : `지우지 못했어요 (${e.message}).`;
    }
    S.busy = false; paint();
  }

  // ---------------------------------------------------------------- list · detail (Figma 198:888)
  const SORTS = [["recent", "최근 순"], ["tokens", "토큰 많은 순"], ["cost", "비용 큰 순"], ["messages", "메시지 많은 순"]];
  const sorted = (items) => {
    const key = { tokens: (x) => x.tokens, cost: (x) => x.cost_usd || 0, messages: (x) => x.messages }[S.sort];
    return key ? [...items].sort((a, b) => key(b) - key(a)) : items;
  };
  const day = (t) => {
    const d = new Date(t), now = new Date();
    const days = Math.round((new Date(now.toDateString()) - new Date(d.toDateString())) / 86400000);
    const hm = d.toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false });
    return days === 0 ? `오늘 ${hm}` : days === 1 ? "어제" : days < 7 ? `${days}일 전` : d.toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "numeric", day: "numeric" });
  };
  function row(it) {
    const r = el("div", `row v4s-row ss-item${S.selected === it.id ? " selected" : ""}`);
    r.tabIndex = 0; r.setAttribute("role", "button");
    if (S.selecting) {
      const pick = el("input"); pick.type = "checkbox"; pick.checked = S.picked.has(it.id); pick.setAttribute("aria-label", "세션 선택");
      pick.addEventListener("click", (e) => e.stopPropagation());
      pick.addEventListener("change", () => { if (pick.checked) S.picked.add(it.id); else S.picked.delete(it.id); render(false); });
      r.append(pick);
    }
    const t = el("div", "v4s-text");
    const title = el("div", "v4s-title", it.title || "(제목 없음)"); if (it.title) title.dataset.noI18n = "";
    t.append(title, el("div", "v4s-meta", [agentName(it.agent), it.device, it.project, day(it.ended_at), `메시지 ${it.messages}`].filter(Boolean).join(" · ")));
    r.append(t, el("span", "ss-cost", `${num(it.tokens)} · ${usd(it.cost_usd, it.priced).replace("≈", "")}`));
    const open = () => { if (S.selected !== it.id) { S.selected = it.id; S.tx = null; txEpoch++; } render(false); };
    r.addEventListener("click", open);
    r.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
    return r;
  }

  function selectionBar() {
    const items = S.doc?.items || [];
    const bar = el("div", "ss-bar");
    const all = items.length > 0 && items.every((x) => S.picked.has(x.id));
    bar.append(button(all ? "선택 해제" : "모두 선택", "", null, () => {
      if (all) S.picked.clear(); else for (const x of items) S.picked.add(x.id);
      render(false);
    }), el("small", "", S.picked.size ? `${S.picked.size}개 선택` : "지울 세션을 골라요"));
    if (S.picked.size) {
      const ids = [...S.picked];
      bar.append(button("Markdown 내보내기", "", null, () => exportSessions(ids, "markdown")),
        button("JSON 내보내기", "", null, () => exportSessions(ids, "json")),
        button("지우기", "danger", null, () => deleteSessions(ids)));
    }
    bar.append(button("닫기", "ghost", null, () => { S.selecting = false; S.picked.clear(); render(false); }));
    for (const b of bar.querySelectorAll("button")) b.disabled = S.busy;
    return bar;
  }

  function detail() {
    const it = S.doc?.items.find((x) => x.id === S.selected);
    if (!it) { $("#detail").replaceChildren(); return; }
    const body = detailShell([agentName(it.agent), it.device].filter(Boolean).join(" · "));
    const head = el("div", "v4s-head");
    const title = el("h2", "", it.title || "(제목 없음)"); if (it.title) title.dataset.noI18n = "";
    const end = new Date(it.ended_at).toLocaleTimeString(globalThis.ArkismI18n?.locale() || "ko-KR", { hour: "2-digit", minute: "2-digit", hour12: false });
    head.append(title, el("p", "v4s-lead ss-when", [`${day(it.started_at).replace(/^(어제|\d+일 전)$/, when(it.started_at))}–${end}`, it.project, `활동 ${dur(it.active_seconds)}`].filter(Boolean).join(" · ")));
    const sec = (t, aside, ...kids) => { const s = el("section", "v4s-sec"); const h = el("div", "v4s-sec-head"); h.append(el("h3", "", t)); if (aside) h.append(el("span", "mono", aside)); s.append(h, ...kids); return s; };
    const facts = (rows) => { const dl = el("dl", "v4s-facts wide"); for (const [k, v] of rows) if (v) dl.append(el("dt", "", k), el("dd", "", v)); return dl; };
    const open = S.tx?.id === it.id;
    const talk = open ? transcriptCard(it, (t, ...kids) => sec(t, `${it.messages}개`, ...kids, button("대화 닫기", "ghost", null, () => { S.tx = null; txEpoch++; render(false); })))
      : sec("대화", `${it.messages}개`, el("p", "meta", "보관된 대화를 열어 봐요. 비밀값과 <private> 구간은 가려서 보여요."), button("대화 보기", "", null, () => transcript(it.id)));
    const top = (list) => (list || []).slice(0, 3).map((x) => `${x.name} ${num(x.count)}`).join(" · ");
    const use = sec("사용량", "", facts([["토큰", `입력 ${num(it.input)} · 출력 ${num(it.output)}`], ["캐시", `읽기 ${num(it.cache_read)} · 쓰기 ${num(it.cache_write)}`],
      ["요청", `${num(it.requests)}번 · ${usd(it.cost_usd, it.priced).replace("≈", "")}`], ["모델", it.models.map((m) => m.name).join(", ")], ["도구", top(it.tools)], ["스킬", top(it.skills)]]));
    const more = el("details", "v4s-more");
    more.append(el("summary", "", "모델·도구·스킬 전체"), card(`모델 · ${it.models.length}개`, bars(it.models, (m) => m.tokens, (m) => m.name, (m) => usd(m.cost_usd, m.priced))),
      card("도구", bars(it.tools, (x) => x.count, (x) => x.name)), card("스킬", bars(it.skills, (x) => x.count, (x) => x.name)));
    const act = el("div", "v4s-actions");
    act.append(button("Markdown으로 내보내기", "", null, () => exportSessions([it.id], "markdown")), button("JSON", "ghost", null, () => exportSessions([it.id], "json")), button("지우기", "ghost danger", null, () => deleteSessions([it.id])));
    for (const b of act.querySelectorAll("button")) b.disabled = S.busy;
    const manage = sec("관리", "", act, el("p", "meta", "대화 원문은 7일만 보관해요. 비밀값과 <private> 구간은 가려서 보여요."));
    if (S.notice) manage.append(el("p", "meta", S.notice));
    const sid = el("p", "mono ss-id", it.session_id); sid.title = it.session_id;
    body.append(head, talk, use, more, manage, sid);
  }
  const card = (title, ...kids) => { const c = el("section", "v4s-sec"); c.append(el("h3", "", title), ...kids); return c; };

  function renderView() {
    const table = $("#table");
    table.className = "v4s-wrap";
    $("#stats").replaceChildren();
    $("#title-count").textContent = S.doc ? `${num(S.doc.total)}개` : "";
    const bar = el("div", "v4s-bar");
    const box = el("label", "v4s-search ss-search");
    const search = el("input"); search.type = "search"; search.placeholder = "제목 · 프로젝트 · 세션 ID"; search.value = S.q; search.setAttribute("aria-label", "세션 검색");
    search.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(() => { S.q = search.value.trim(); load(); }, 300); });
    box.append(el("span", "ss-glass", "⌕"), search);
    const sel = (label, opts, cur, pick) => { const s = el("select", "v4s-select"); s.setAttribute("aria-label", label); for (const [k, l] of opts) { const o = el("option", "", l); o.value = k; o.selected = k === cur; s.append(o); } s.addEventListener("change", () => pick(s.value)); return s; };
    const pickMode = el("button", `link-btn ss-pick${S.selecting ? " on" : ""}`, S.selecting ? "선택 끝내기" : "선택해서 지우기"); pickMode.type = "button";
    pickMode.addEventListener("click", () => { S.selecting = !S.selecting; if (!S.selecting) S.picked.clear(); render(false); });
    bar.append(box, sel("기간", RANGES.map(([k, l]) => [k, k === "all" ? "전체 기간" : k === "today" ? "오늘" : `최근 ${l}`]), S.range, (k) => { if (S.range !== k) { S.range = k; load(); } }),
      sel("정렬", SORTS, S.sort, (k) => { S.sort = k; render(false); }), pickMode);
    const keep = document.activeElement?.closest?.(".ss-search");
    $("#controls").replaceChildren(bar);
    if (keep) { search.focus(); search.setSelectionRange(search.value.length, search.value.length); }
    const agents = usage.agents();
    const chip = (k, label) => {
      const c = el("button", `chip${S.source === k ? " active" : ""}`); c.type = "button";
      if (k) c.append(logo(k));
      c.append(label);
      if (S.source === k && S.doc) c.append(el("output", "", num(S.doc.total)));
      c.addEventListener("click", () => { if (S.source !== k) { S.source = k; load(); } });
      return c;
    };
    $("#chips").replaceChildren(...(agents.length ? [chip("", "전체"), ...agents.map((a) => chip(a, agentName(a)))] : []));
    $("#chips").classList.add("v4s-cats");
    if (S.state === "idle") { S.state = "loading"; queueMicrotask(load); if (!agents.length) usage.load(); }
    if (S.state !== "ready") {
      const msg = S.state === "unsupported" ? "이 서버는 아직 세션 목록을 지원하지 않아요." : S.state === "error" ? `세션을 불러오지 못했어요 (${S.error}).` : "불러오는 중…";
      table.replaceChildren(el("div", "empty", msg));
      $("#detail").replaceChildren();
      return;
    }
    const parts = [];
    if (S.selecting) parts.push(selectionBar());
    if (S.notice && !S.selected) parts.push(el("p", "ss-note", S.notice));
    const list = el("div", "v4s-list");
    list.append(...sorted(S.doc.items).map(row));
    if (!S.doc.items.length) list.append(el("div", "empty", "조건에 맞는 세션이 없어요."));
    parts.push(list, el("p", "v4s-foot", S.doc.total > S.doc.items.length ? `${num(S.doc.total)}개 중 ${S.doc.items.length}개 표시` : `세션 ${num(S.doc.total)}개`));
    table.replaceChildren(...parts);
    if (!S.selected && S.doc.items[0]) S.selected = S.doc.items[0].id;
    detail();
  }
  // 사용량 › 세션 상세 → 대화 내용 보기: open this session here, widening the range when it is older than the list.
  function open(id) {
    S.selected = id; S.tx = null; txEpoch++;
    if (!S.doc?.items.some((x) => x.id === id)) { S.range = "all"; S.q = ""; S.state = "idle"; }
    ctx.go("sessions");
  }

  return {
    load, open, refresh: () => load(true), count: () => (S.doc ? num(S.doc.total) : ""),
    reset: () => { epoch++; txEpoch++; S.picked.clear(); Object.assign(S, { doc: null, state: "idle", selected: "", tx: null, notice: "", busy: false }); },
    views: { sessions: { title: "세션", sub: () => "에이전트와 일한 기록이에요. 기억과 지식은 여기서 뽑아요.", render: renderView, keepDetail: true,
      head: () => [button("새로고침", "ghost", "arrows-clockwise", load)] } }
  };
};
