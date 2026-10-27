// MEMORY › 개요 windows (Figma 573:40303 … 573:50266 stage windows, 580:* wide windows, 592/764 improvements).
// open(key): the stage window drawn over the overview; wide(kind): the large window (review · active · sessions · usage · map).
// 세션 기록 is only this window now (the separate 세션 screen is gone): its detail reads the archived dialogue itself.
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
  const openSession = (ev, quote) => wide("sessions", ev.split(":")[1], quote); // the 세션 기록 window, that session's dialogue open
  const D = () => state.data;
  const tr = (x) => window.ArkismI18n?.translate(x) || x;
  const usage = (id) => state.usage?.get(id);
  // read: memories outside the loaded first page (most-used ones are often older), fetched once each; false = gone or unreadable
  const read = new Map();
  let redraw = null, batch = [], readGen = state.connection.generation;
  const memOf = (id) => {
    if (readGen !== state.connection.generation) { read.clear(); readGen = state.connection.generation; } // names from another server or scope, and reads it dropped, start over
    return D().memories.find((m) => m.id === id) || ctx.maintenanceRecords?.get(id) || read.get(id) || undefined; // proposals point past the loaded first page too
  };
  // one POST /v1/memories/query {ids} per drawing pass (100 ids a call); a server without ids answers 400, so those fall back to one GET each
  function readMemory(id) {
    read.set(id, null); batch.push(id);
    if (batch.length > 1) return;
    queueMicrotask(() => { // one redraw per drawing pass, after every name it asked for has come back
      const g = state.connection.generation, ids = batch; batch = [];
      const keep = (i, v) => { if (g === state.connection.generation) read.set(i, v); };
      const one = (i) => request(`/v1/memories/${encodeURIComponent(i)}?${query()}`).then((r) => keep(i, r?.text && (r.status ?? "active") === "active" ? r : false), () => keep(i, false));
      const part = (ps) => request("/v1/memories/query", { method: "POST", body: JSON.stringify({ scope: state.scope, ids: ps, limit: 100 }) }).then((r) => {
        const got = new Map((r?.items || []).filter((m) => m?.text).map((m) => [m.id, m]));
        for (const i of ps) keep(i, got.get(i) || false);
      }, () => Promise.allSettled(ps.map(one)));
      const parts = []; for (let k = 0; k < ids.length; k += 100) parts.push(part(ids.slice(k, k + 100)));
      void Promise.allSettled(parts).then(() => { if (dlg?.isConnected) redraw?.(); }); // whichever window is open now: a redraw while waiting asks for no new reads
    });
  }
  const memText = (id) => {
    const m = memOf(id); if (m) return m.text;
    if (id && !read.has(id)) readMemory(id);
    return read.get(id) === null ? "불러오는 중…" : "(지워졌거나 아직 못 읽은 기억)";
  };
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
    document.body.append(d); dlg = d; redraw = null;
    if (!beside) { d.showModal(); return d; }
    d.show(); rail?.addEventListener("click", shut);
    d.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && !ev.defaultPrevented) { ev.preventDefault(); d.close(); } });
    return d;
  }
  // how a separate window reopens this one when it is dragged out of the app (popup-drag.js; overview.js listens)
  const recipe = (d, detail) => { if (d) { Object.assign(d.dataset, { popout: JSON.stringify({ event: "arkism:open-overview-window", detail }), popoutTitle: tr("기억 처리") }); d.setAttribute("aria-keyshortcuts", "Alt+Shift+O"); } };
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
      // an empty list already says 불러오는 중… itself, so the note would repeat it (#182)
      if (!snap.loading || snap.items.length) host.append(el("p", "ov-wide-foot-note", snap.loading ? "불러오는 중…" : snap.error ? `불러오지 못했어요 (${snap.error}).` : `${num(snap.items.length)} / ${num(snap.total ?? 0)} · 전체에서 검색·정렬`));
      if (snap.cursor || snap.error) { const b = button(snap.error ? "다시 시도" : "더 보기", "small", null, () => load(true)); b.disabled = snap.loading; host.append(b); }
    }
    d.addEventListener("close", () => { ticket++; pages.reset(); });
    return { snapshot, footer };
  }

  // ------------------------------------------------------------ where a memory came from: its quote and the lines around it
  // one message of an archived dialogue; mark: words to underline (returns the line and the underline, if any)
  // one message: the person on the right, the agent (by its name when known) on the left, anything else muted; its own time when recorded
  function msgLine(m, mark, agent = "", prev = null) {
    const kind = m.role === "user" ? "user" : m.role === "assistant" ? "assistant" : "sys";
    const b = el("div", `ss-msg ${kind}`), p = el("p", "", m.text); p.dataset.noI18n = "";
    const at = mark ? m.text.indexOf(mark) : -1; let hit = null;
    if (at >= 0) { hit = el("mark", "ss-hit", mark); hit.dataset.noI18n = ""; p.replaceChildren(m.text.slice(0, at), hit, m.text.slice(at + mark.length)); }
    const who = el("b", "", kind === "user" ? "사용자" : kind === "sys" ? m.role || "기타" : agent ? SL.label(agent) : "에이전트"), stamp = SL.msgTime(m.at, prev?.at, loc());
    if (stamp) { const tm = el("time", "", stamp); tm.dateTime = m.at; tm.dataset.noI18n = ""; who.append(tm); }
    b.append(who, p); if (m.truncated) b.append(el("small", "ss-note", "길어서 앞부분만 보여요."));
    return [b, hit];
  }
  // the source session's dialogue, read the way 세션 기록 reads it (row id from the session list, then the transcript pages), once per session and quote
  const talks = new Map();
  function sourceTalk(sid, mark) {
    const key = `${state.connection.generation}|${sid}|${mark}`;
    if (!talks.has(key)) talks.set(key, (async () => {
      const own = (x) => x.session_id === sid || x.id === sid;
      const ses = env.sessions().find(own) || (await request(`/v1/usage/sessions?${query({ range: "all", sort: "recent", q: sid, limit: 20 })}`))?.items?.find(own);
      if (!ses) return { items: [], state: "missing" };
      let items = [], next = "", archive = "", st = "";
      // shortcut: looks at most 10 pages (300 messages) for the quoted words, as 세션 기록 does
      for (let page = 0; page < 10; page++) {
        const r = await request("/v1/usage/sessions/transcript", { method: "POST", body: JSON.stringify({ scope: state.scope, id: ses.id, limit: 30, ...(next ? { archive_id: archive, after: next } : {}) }) });
        if (r?.authority !== "data_only_untrusted" || !Array.isArray(r.items)) throw new Error("invalid_response");
        items = [...items, ...r.items]; st = r.state; archive = r.archive_id || ""; next = r.next_cursor || "";
        if (!next || !mark || items.some((x) => x.text.includes(mark))) break;
      }
      return { items, state: st };
    })().catch((e) => { talks.delete(key); throw e; }));
    return talks.get(key);
  }
  // the quote, then two messages before and after it in the source session, with 세션 보기 › opening the dialogue at that spot
  function contextBox(item, quote = "") {
    const ev = tag(item, "event_id:"); if (!ev && !quote) return null;
    const box = el("div", "ov-context");
    if (quote) box.append(el("blockquote", "ov-detail-quote", `“${clip(quote, 240)}”`));
    if (!ev) return box;
    const [agent, sid] = ev.split(":"), ses = env.sessions().find((x) => x.session_id === sid), from = el("div", "ov-detail-from"), msgs = el("div", "ss-msgs");
    from.append(el("span", "", ses ? `${agent} · ${ses.title || ses.project || ""}` : agent), button("세션 보기 ›", "ghost small", null, () => openSession(ev, quote)));
    msgs.append(el("p", "ov-win-empty", "대화를 불러오는 중…")); box.append(from, msgs);
    const mark = quote.trim();
    sourceTalk(sid, mark).then(({ items, state: st }) => {
      if (!items.length) { msgs.replaceChildren(el("p", "ov-win-empty", st === "missing" ? "이 세션을 찾지 못했어요. 지워졌거나 보관 기간이 지났을 수 있어요." : { none: "이 세션은 보관된 대화가 없어요. 대화 원문은 수집된 뒤 7일 동안만 보관돼요.", expired: "보관된 대화가 만료됐거나 지워졌어요.", unsupported: "이 형식의 대화는 아직 볼 수 없어요." }[st] || "보여 줄 대화가 없어요.")); return; }
      const at = mark ? items.findIndex((x) => x.text.includes(mark)) : -1, from2 = at >= 0 ? Math.max(0, at - 2) : 0;
      msgs.replaceChildren(...items.slice(from2, at >= 0 ? at + 3 : 4).map((m, i, all) => msgLine(m, mark, agent, all[i - 1])[0]));
    }, (e) => msgs.replaceChildren(el("p", "ov-win-empty", /HTTP 404|not_found/.test(e?.message || "") ? "이 서버는 아직 대화 보기를 지원하지 않아요." : `대화를 불러오지 못했어요 (${e.message}).`)));
    return box;
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
    collect: ["에이전트 대화를 세션 단위로 모아 저장해요", "Claude Code · Codex · BULTUK 등", "대화가 끝날 때마다", "자동 (수집 훅)"],
    extract: ["저장된 대화에서 교훈·결정 후보를 뽑아요", "대화 저장", "대화가 끝난 뒤", "AI (서버 Claude)"],
    classify: ["후보에 위험도와 중복 힌트를 붙여요", "분석", "후보가 생기면 바로", "AI (분류만, 승인 안 함)"],
    review: ["사람이 승인해야 쓰는 기억이 돼요", "분류 · 드리밍", "언제든", "사람만"],
    recall: ["질문과 관련 있는 기억을 찾아 답에 넣어요", "쓰는 중인 기억", "에이전트가 질문할 때마다", "자동 (검색)"],
    trace: ["어떤 기억이 어느 세션에 들어갔는지 남겨요", "불러오기", "불러올 때마다", "자동"],
    judge: ["에이전트에게 건넨 기억이 실제 답에 쓰였는지 확인해요", "사용 기록", "세션이 끝난 뒤", "자동 집계 + 사람 확인"],
    dream: ["겹치거나 낡은 기억을 찾아 정리를 제안해요", "쓰는 중인 기억 · 쓰임 확인 결과", "주기적으로", "AI 제안 → 사람 승인"],
    active: ["승인된 기억이에요. 불러오기가 여기서 찾아요", "승인 대기", "승인되면 바로 추가", "사람이 승인한 것만"]
  };
  const LABEL = { active: "쓰는 중인 기억" };
  const LIST_TITLE = { extract: "최근 추출된 후보·기억", classify: "분류 기다리는 후보", review: "승인 기다리는 후보",
    judge: "확인 기다리는 기억", dream: "정리 제안", active: "많이 쓰인 순" };
  // a stage window does its own work in place; a footer only widens the same list (2026-10-25)
  const FULL = { extract: ["세션 기록 보기 ›", () => wide("sessions")], review: ["넓게 보기 ›", () => wide("review")], active: ["넓게 보기 ›", () => wide("active")] };

  function stageRows(key, box, expanded) {
    const w = D().tools?.memory_use, cap = expanded ? (+expanded > 1 ? +expanded : 60) : 6, out = []; // expanded: true (60 rows) or a row count
    const rows = {
      extract: () => [...waiting(), ...D().memories].sort((a, b) => time(b.created_at) - time(a.created_at)).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), `${tag(m, "event_id:") ? "대화 저장 · " : ""}${ago(m.created_at)}`, "")),
      classify: () => byKind(waiting().filter((m) => !m.classification)),
      review: () => waiting().slice(0, cap).map((m) => reviewRow(m)),
      judge: () => toCheck().slice(0, cap).map(([m, u]) => {
        const r = row("안 쓰임", clip(m.text), u.unused ? `쓰였다는 보고 없이 ${num(u.unreceipted)}번 건넸어요` : `${w.days}일 동안 ${num(u.injected)}번 건넸는데 답에 쓰였다는 표시가 없어요`, ""), err = el("small", "ov-win-err", "");
        r.lastChild.append(button("맞아요, 유지", "small", null, () => { ctx.keepMemory?.(m.id); r.remove(); }),
          button("고치기", "small", null, () => editMemory(m, m.id)), button("잊기", "small", null, () => askConfirm("이 기억을 잊을까요?", "에이전트에게 더는 건네지 않아요. 바뀐 이력에는 남아요.", async () => {
            try { await deleteMemory(m); r.remove(); await reload(); } catch (e) { err.textContent = `잊지 못했어요: ${e.message}`; }
          }, "잊기"))); r.firstChild.append(err);
        return r;
      }),
      dream: () => dreamItems().slice(0, cap).map((c) => {
        const t = c.dream.maintenance || {}, ev = t.evidence, wrap = el("div", "ov-win-proposal"), panel = el("div", "ov-win-proposal-panel"); panel.hidden = true;
        const meta = !t.memory ? `→ ${clip(memText(t.retire?.id), 40)}` : ev ? (t.type === "lesson_often_failing" ? `실패가 잦은 교훈 · 통과율 ${Math.round((ev.pass_rate || 0) * 100)}% (${num(ev.passed)}/${num(ev.samples)})` : `자주 어기는 규칙 · 통과율 ${Math.round((ev.pass_rate || 0) * 100)}% (${num(ev.passed)}/${num(ev.samples)})`) : t.type === "lesson_often_failing" ? "실패가 잦은 교훈" : "자주 어기는 규칙";
        const toggle = button("결정하기 ⌄", "small", null, () => { panel.hidden = !panel.hidden; toggle.textContent = panel.hidden ? "결정하기 ⌄" : "접기 ⌃"; if (!panel.hidden && !panel.childElementCount) panel.append(...dreamDecision(c, () => redraw?.())); });
        wrap.append(row(c.label, clip(c.text), meta, "", [toggle]), panel);
        return wrap;
      }),
      active: () => D().memories.slice().sort((a, b) => (usage(b.id)?.d7 || 0) - (usage(a.id)?.d7 || 0) || time(b.created_at) - time(a.created_at)).slice(0, cap).map((m) => row(kind(m.kind), clip(m.text), tag(m, "repo:") || ago(m.created_at), usage(m.id) ? `7일 ${num(usage(m.id).d7 || 0)}번 쓰임` : "", []))
    };
    return (rows[key]?.() || out);
  }
  // 분류: the candidates under one header per kind, in the KIND order, each header with its count
  function byKind(list) {
    const groups = new Map(Object.keys(KIND).map((k) => [k, []]));
    for (const m of list) { if (!groups.has(m.kind)) groups.set(m.kind, []); groups.get(m.kind).push(m); }
    return [...groups].filter(([, ms]) => ms.length).flatMap(([k, ms]) => {
      const h = el("div", "ov-win-group"); h.append(tagEl(kind(k)), el("small", "", `${num(ms.length)}건`));
      return [h, ...ms.map((m) => reviewRow(m))];
    });
  }
  const itemCount = (rows) => rows.filter((r) => !r.classList.contains("ov-win-group")).length;
  function reviewRow(c) {
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
  // lanes, not the question, what fell below the cut or how long it took, so those parts of the design are not drawn.
  const LANE = { exact: "이름으로 바로 찾았어요", keyword: "같은 낱말이 있어요", vector: "뜻이 비슷해요", relation: "이어진 기억이에요" };
  const LANE_SHORT = { exact: "이름", keyword: "낱말", vector: "뜻", relation: "연결" };
  // why one memory was handed out, in words: a pinned one has no score; otherwise the lanes that found it
  const recallWhy = (it) => (it.score == null ? "늘 넣는 기억이에요 (고정)" : (it.lanes || []).map((l) => LANE[l] || l).join(" · ") || "검색으로 찾았어요");
  const recallWho = (x) => `${x.requested_by || "에이전트"}${x.project_key ? ` · ${x.project_key} 프로젝트` : ""}`;
  let recalls = null, redrawing = false; // one GET /v1/recalls per opened window; a redraw after the names arrive reuses it
  // 불러오기 opens wide: the receipts on the left (who, which project, when, how many), the picked one's memories on the right
  let recallWin = null;
  function wideRecall(sel = null, agent = "all") {
    const d = shell("불러오기", "", "에이전트가 질문할 때 넣어 준 기억이에요. 질문 내용은 저장하지 않아요");
    recipe(d, { wide: "recall", args: [sel, agent] });
    recallWin = d; redraw = () => wideRecall(sel, agent);
    const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), chipHost = el("div"), top = el("div", "ov-wide-sum");
    const g = state.connection.generation;
    if (recalls?.g !== g) { // one GET /v1/recalls per open (open() drops it); chips and picks reuse it
      const rc = recalls = { g };
      rc.p = request(`/v1/recalls?${query({})}`).then((r) => { rc.v = r; }, (e) => { rc.err = e; }).then(() => { if (dlg && dlg === recallWin) redraw?.(); });
    }
    twoPane(d, filterBar([], el("span"), chipHost), list, detail); list.before(top);
    if (recalls.err) { list.append(el("p", "ov-win-empty", "불러오기 기록을 읽지 못했어요.")); return; }
    if (!recalls.v) { list.append(el("p", "ov-win-empty", "불러오는 중…")); return; }
    const all = (recalls.v.items || []).filter((x) => x.items?.length), agents = [...new Set(all.map((x) => x.requested_by).filter(Boolean))];
    if (!all.length) { list.append(el("p", "ov-win-empty", "아직 불러온 기록이 없어요.")); return; }
    chipHost.append(chipBar([["all", "전체", all.length], ...agents.map((a) => [a, a, all.filter((x) => x.requested_by === a).length])], agent, (k) => wideRecall(null, k)));
    const shown = all.filter((x) => agent === "all" || x.requested_by === agent);
    // what a recall hands out on average, by kind
    const count = {}; for (const x of shown) for (const it of x.items) { const m = memOf(it.memory.id); if (m) count[m.kind] = (count[m.kind] || 0) + 1; }
    const kinds = el("div", "ov-kinds"); kinds.append(el("span", "", `${num(shown.length)}번 불러올 때 평균`));
    for (const [k2, n] of Object.entries(count)) { const t = tagEl(kind(k2)); t.textContent += ` ${num(Math.round(n / shown.length * 10) / 10)}개`; kinds.append(t); }
    top.append(kinds);
    const cur = shown.find((x) => x.id === sel) || shown[0];
    for (const x of shown) {
      const r = el("div", `ov-wide-row${x === cur ? " on" : ""}`), t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t");
      r.tabIndex = 0; r.setAttribute("role", "button"); r.setAttribute("aria-pressed", String(x === cur));
      l1.append(el("span", "ov-win-item-title", recallWho(x))); t.append(l1);
      t.append(el("small", "", x.items.slice(0, 2).map((it) => clip(memText(it.memory.id).split("\n")[0], 28)).join(" · ")));
      const right = el("div", "ov-wide-row-r"); right.append(el("span", "", `기억 ${num(x.items.length)}개`), el("span", "", ago(x.created_at)));
      r.append(t, right);
      const pick = () => wideRecall(x.id, agent);
      r.addEventListener("click", pick); r.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pick(); } });
      list.append(r);
    }
    const h = el("div", "ov-detail-head"); h.append(el("span", "ov-win-tag", cur.requested_by || "에이전트"), el("small", "", new Date(cur.created_at).toLocaleString(loc(), { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })));
    detail.append(h, el("h3", "", `${recallWho(cur)}에서 기억 ${num(cur.items.length)}개를 받았어요`));
    const items = el("div", "ov-detail-list");
    for (const it of cur.items) {
      const m = memOf(it.memory.id), b = el("div", "ov-recall-item"), head = el("div", "ov-detail-head");
      head.append(el("b", "", `${num(it.rank || 0)}위`), ...(m ? [tagEl(kind(m.kind))] : []), el("small", "", recallWhy(it)));
      b.append(head, el("p", "", clip(memText(it.memory.id).split("\n")[0], 160)));
      if (m) { b.classList.add("ov-win-click"); b.tabIndex = 0; b.setAttribute("role", "button"); b.title = "기억 열기"; b.addEventListener("click", () => editMemory(m, m.id)); }
      items.append(b);
    }
    const sec = el("div", "ov-detail-sec"); sec.append(el("small", "", "넣어 준 기억 (높은 순위부터)"), items); detail.append(sec);
    // the raw numbers stay one click away: score 0–1 (higher = closer to the question) and the search lanes
    const more = el("details", "ov-recall-more"), sum = el("summary", "", "점수 보기");
    more.append(sum, el("p", "", "점수는 0~1이에요. 1에 가까울수록 질문과 가깝다고 봤어요."), ...cur.items.map((it) => el("p", "", `${num(it.rank || 0)}위 · ${it.score == null ? "고정 (점수 없음)" : `점수 ${it.score.toFixed(2)}`} · ${(it.lanes || []).map((l) => LANE_SHORT[l] || l).join(", ") || "—"}`)));
    detail.append(more);
  }

  // ------------------------------------------------------------ 대화 저장 · 분석 summaries (read-only; each read once per connection)
  const once = new Map(); // key → { g, v, err }: a failed read stays failed until the connection or scope changes (no retry loop on redraw)
  function readOnce(k, get) {
    const g = state.connection.generation, hit = once.get(k);
    if (hit?.g === g) return hit;
    const e = { g, v: undefined, err: null }; once.set(k, e);
    e.p = get().then((v) => { e.v = v; }, (er) => { e.err = er; }).then(() => { if (dlg?.isConnected) redraw?.(); });
    return e;
  }
  const barList = (pairs, max = Math.max(1, ...pairs.map(([, n]) => n))) => { const box = el("div", "ov-detail-bars"); for (const [a, n, tagged] of pairs) { const p = el("div", "ov-detail-bar"), track = el("i"), fill = el("b"), line = el("p", ""); fill.style.width = `${Math.round((n / max) * 100)}%`; if (tagged) fill.style.background = KIND_COLOR[tagged] || ""; track.append(fill); line.append(el("span", "", a), el("strong", "", `${num(n)}`)); p.append(line, track); box.append(p); } return box; };
  const block = (title, ...nodes) => { const b = el("div", "ov-win-chart ov-sum"); b.append(el("div", "ov-win-chart-title", title), ...nodes); return b; };
  const cellRow = (pairs) => { const c = el("div", "ov-win-cells ov-sum-cells"); for (const [l, v] of pairs) { const x = el("div", "ov-win-cell"); x.append(el("small", "", l), el("strong", "", v)); c.append(x); } return c; };
  // 분석: what went in (the 7-day sessions and their messages) and what came out (candidates by kind, and how each ended)
  function analysisBox() {
    const ses = env.sessions(), st = ses.map(sessionState), wk = Date.now() - 7 * 24 * HOUR;
    const n = { ok: st.filter((x) => x.tone === "ok").length, wait: st.filter((x) => x.tone === "wait").length };
    const inBox = block("분석한 대화", el("p", "ov-sum-line", `최근 대화 ${num(ses.length)}개, 메시지 ${num(ses.reduce((a, x) => a + (x.messages || 0), 0))}개를 봤어요.`),
      barList([["후보가 나온 대화", n.ok], ...(n.wait ? [["분석 기다리는 대화", n.wait]] : []), ["후보가 안 나온 대화", ses.length - n.ok - n.wait]], Math.max(1, ses.length)));
    const rej = readOnce("rejected", () => request(`/v1/memory_candidates?${query({ status: "rejected", created_from: new Date(wk).toISOString(), limit: 100 })}`));
    const rejected = rej.v?.items || [], pending = waiting(), approved = D().memories.filter((m) => tag(m, "event_id:") && time(m.created_at) >= wk);
    const kinds = {}; for (const m of [...pending, ...approved, ...rejected]) kinds[m.kind] = (kinds[m.kind] || 0) + 1;
    const out = [...pending, ...approved, ...rejected].length, dup = pending.filter((c) => c.classification?.dup_hint).length, clash = pending.filter((c) => c.conflict_id || c.status === "conflicted").length;
    const outBox = block("나온 것", ...(out ? [barList(Object.entries(kinds).sort((a, b) => b[1] - a[1]).map(([k, c]) => [kind(k), c, k]))] : [el("p", "ov-win-empty", "아직 나온 후보가 없어요.")]),
      cellRow([["승인 대기", `${num(pending.length)}건`], ["승인됨 (7일)", `${num(approved.length)}건`], ["중복 의심", `${num(dup)}건`], ["충돌", `${num(clash)}건`], ["버림 (7일)", rej.v ? `${num(rejected.length)}건` : rej.err ? "—" : "…"]]));
    const why = {}; for (const c of rejected) { const r = (c.review?.reason || "이유 없음").trim(); why[r] = (why[r] || 0) + 1; }
    const top = Object.entries(why).sort((a, b) => b[1] - a[1]).slice(0, 3);
    if (top.length) { const l = el("div", "ov-sum-why"); l.append(el("small", "", "버린 이유")); for (const [r, c] of top) { const p = el("p", ""); p.append(el("span", "", clip(r, 60)), el("strong", "", `${num(c)}건`)); l.append(p); } outBox.append(l); }
    const box = el("div", "ov-sum-grid"); box.append(inBox, outBox);
    return box;
  }
  // one POST /v1/usage/sessions/archives per set of shown sessions (≤50); archiveOf reads the last answer back by row id
  let lastArc = null;
  function archives(ids) {
    ids = ids.filter(Boolean);
    lastArc = ids.length ? readOnce(`archives|${ids.join(",")}`, () => request("/v1/usage/sessions/archives", { method: "POST", body: JSON.stringify({ scope: state.scope, ids }) })) : { v: { items: [] } };
    return lastArc;
  }
  const day = (v) => new Date(v).toLocaleDateString(loc(), { month: "numeric", day: "numeric" });
  const ARC = { expired: ["원문 만료", "만료됐어요"], unsupported: ["원문 못 읽음", "이 형식은 아직 볼 수 없어요"], none: ["원문 없음", "보관된 원문이 없어요"] };
  function archiveOf(id) {
    const x = lastArc?.v?.items?.find((y) => y.id === id);
    if (!x) return lastArc?.v?.missing?.includes(id) ? { short: ARC.none[0], long: ARC.none[1] } : null;
    const exp = x.archives?.find((a) => a.state === "active")?.expires_at, left = exp ? Math.max(0, Math.ceil((time(exp) - Date.now()) / (24 * HOUR))) : null;
    if (x.state === "active") return { short: left == null ? "원문 보관 중" : `원문 D-${left}`, long: exp ? `${day(exp)}까지 (${num(left)}일 남음)` : "보관 중" };
    const [short, long] = ARC[x.state] || ARC.none; return { short, long };
  }
  // 대화 저장: how many dialogues came in, how many led to memories, and whether their text is still kept (POST /v1/usage/sessions/archives).
  // Shown above the session list when 대화 저장 opens (wideSessions with stage).
  function collectBox() {
    const ses = env.sessions(), st = ses.map(sessionState), arc = archives(ses.slice(0, 50).map((x) => x.id));
    const states = {}; for (const x of arc.v?.items || []) states[x.state] = (states[x.state] || 0) + 1;
    const soon = (arc.v?.items || []).flatMap((x) => x.archives || []).map((a) => time(a.expires_at)).filter(Boolean).sort((a, b) => a - b)[0];
    const left = soon ? Math.max(0, Math.ceil((soon - Date.now()) / (24 * HOUR))) : null;
    const savedBox = block("저장한 대화", el("p", "ov-sum-line", `최근 대화 ${num(ses.length)}개, 메시지 ${num(ses.reduce((a, x) => a + (x.messages || 0), 0))}개, 토큰 ${num(ses.reduce((a, x) => a + (x.tokens || 0), 0))}개를 저장했어요.`),
      barList([["기억·후보가 나온 대화", st.filter((x) => x.tone === "ok").length], ["아직 안 나온 대화", st.filter((x) => x.tone !== "ok").length]], Math.max(1, ses.length)));
    const keptBox = block("원문 보관", ...(arc.err ? [el("p", "ov-win-empty", /HTTP 4(00|04)|not_found|invalid/.test(arc.err.message || "") ? "이 서버는 원문 보관 상태를 아직 알려 주지 않아요." : `보관 상태를 읽지 못했어요 (${arc.err.message}).`)]
      : !arc.v ? [el("p", "ov-win-empty", "불러오는 중…")]
      : [cellRow([["보관 중", `${num(states.active || 0)}개`], ["만료됨", `${num(states.expired || 0)}개`], ["원문 없음", `${num((states.none || 0) + (states.unsupported || 0) + (arc.v.missing?.length || 0))}개`]]),
        el("p", "ov-sum-line", `원문은 저장 뒤 7일 동안만 보관돼요.${left == null ? "" : left ? ` 가장 먼저 지워지는 원문은 ${num(left)}일 남았어요.` : " 오늘 지워지는 원문이 있어요."}`)]));
    const box = el("div", "ov-sum-grid"); box.append(savedBox, keptBox);
    return box;
  }

  // stages with a list to read open straight into a wide window; the others fit the stage window
  const WIDE_STAGE = { recall: () => wideRecall(), trace: () => wideTrace(), collect: () => wideSessions("", "", true) };
  function open(key, expanded = false) {
    if (!redrawing) { recalls = null; once.clear(); } // a fresh open reads again; a redraw after names arrive reuses what it read
    redrawing = false;
    if (WIDE_STAGE[key]) return WIDE_STAGE[key]();
    const list = env.stages(), i = list.findIndex((x) => x.key === key), s = i >= 0 ? list[i] : activeStage();
    const spec = SPEC[key];
    const d = modal(`ov-stage-win ov-t-${s.badge?.[1] || "n"} ov-win-${key}${expanded ? " ov-win-expanded" : ""}`); // 모두 보기: the window becomes the list
    recipe(d, { key, expanded });
    redraw = () => { redrawing = true; open(key, expanded); };
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
    body.append(tiles, how);
    if (s.counts) {
      const chart = el("div", "ov-win-chart"), max = Math.max(1, ...s.counts), bars = el("div", "ov-win-bars");
      chart.append(el("div", "ov-win-chart-title", "최근 24시간"));
      s.counts.forEach((n, k) => { const b = el("i", n ? (s.row?.[k] === "r" ? "r" : s.row?.[k] === "a" ? "a" : "") : "none"); b.style.height = `${6 + (n / max) * 54}px`; b.title = `${n}`; bars.append(b); });
      chart.append(bars, el("div", "ov-win-axis", "24시간 전 ··· 지금")); body.append(chart);
    }
    if (key === "extract" && s.badge?.[1] === "r") body.append(note("r", `${s.badge[0]}이에요`, "세션은 계속 들어오는데 새 후보가 만들어지지 않았어요. 서버의 분석 로그인·연결을 확인해 주세요."));
    if (key === "judge") body.append(note("a", "여러 번 건넸는데 답에 쓰였다는 표시가 없는 기억이에요", "에이전트가 썼다고 알려 온 적도, 답변에 그 문구가 보인 적도 없어요. 말없이 따른 경우는 셀 수 없어서, 틀렸는지·이제 필요 없는지는 직접 봐 주세요.",
      "맞아요, 유지: 그대로 두고 다시 묻지 않아요 · 고치기: 고친 문장을 승인 대기에 넣어요 · 잊기: 에이전트에게 더는 건네지 않아요")); // nothing changes on its own
    if (key === "dream") body.append(note("n", "드리밍 제안은 여기서 바로 골라요", "바로 바뀌지 않아요. 사람이 고르면 반영되고, 지운 기억도 기억 이력에 남아요. 승인 대기에도 [정리]로 같이 떠요."));
    if (key === "extract") body.append(analysisBox());
    const rows = stageRows(key, body, expanded), total = expandTotal(key);
    const ul = el("div", "ov-win-list");
    const listHead = el("div", "ov-win-list-title", LIST_TITLE[key]);
    if (expanded) listHead.append(button("접기 ⌃", "ghost small", null, () => open(key, false)));
    else if (total && total > itemCount(rows)) listHead.append(button(`${num(total)}건 모두 보기 ›`, "ghost small", null, () => open(key, true)));
    body.append(listHead);
    if (!rows.length) ul.append(el("p", "ov-win-empty", "지금 보여 줄 항목이 없어요."));
    ul.append(...rows); body.append(ul);
    const foot = el("div", "ov-win-foot");
    if (expanded) {
      ul.classList.add("ov-win-list-scroll");
      const left = el("span", "ov-wide-foot-note"), say = (n) => { left.textContent = n < total ? `${num(total)}건 중 ${num(n)}건 · 창 안에서 내리면 더 나와요` : `${num(total)}건 모두 보는 중`; };
      let cap = itemCount(rows); say(cap);
      ul.addEventListener("scroll", () => { // 60 more each time the list reaches its end
        if (cap >= total || ul.scrollTop + ul.clientHeight < ul.scrollHeight - 40) return;
        cap += 60; const more = stageRows(key, body, cap), n = itemCount(more); ul.replaceChildren(...more); say(n); if (n < cap) cap = total;
      });
      foot.append(left);
    }
    if (FULL[key]) foot.append(button(FULL[key][0], "ghost small", null, FULL[key][1]));
    d.append(head, body, foot);
  }
  // Figma 펼침 580:63994 분석 · 580:66175 분류 · 580:68368 불러오기 · 580:70537 검증 · 580:72743 드리밍; other stages open a wide window instead
  function expandTotal(key) {
    return { extract: waiting().length + D().memories.length,
      judge: toCheck().length,
      dream: dreamItems().length }[key] || 0;
  }
  // 안 쓰임: returned unused_after times since its last record_memory_usage receipt (host memory_use), or injected 3+ times
  // with neither a receipt nor an answer restating it (the estimate). A person decides; nothing is demoted on its own.
  const idle = (u) => !!u && (u.unused || (u.injected >= 3 && !u.used && !u.estimated));
  // 쓰임 확인's list: idle memories the person has not already kept (맞아요, 유지)
  const toCheck = () => { const w = D().tools?.memory_use, kept = ctx.kept?.() || []; return D().memories.map((m) => [m, w?.items?.find((x) => x.id === m.id)]).filter(([m, u]) => idle(u) && !kept.includes(m.id)); };
  const note = (tone, a, b, c) => { const n = el("div", `ov-win-note ov-win-note-${tone}`); n.append(el("strong", "", a), el("span", "", b)); if (c) n.append(el("small", "", c)); return n; };
  function activeStage() {
    const mems = D().memories, week = mems.filter((m) => time(m.created_at) > Date.now() - 7 * 24 * HOUR).length;
    return { key: "active", label: "쓰는 중인 기억", badge: [`${num(mems.length)}개`, "n"], tiles: [[`${num(mems.length)}개`, "쓰는 중"], [state.usageDone ? `${num([...(state.usage?.values() || [])].filter((u) => u.d30 > 0 || u.d7 > 0).length)}개` : "—", "최근 쓰임"], [`${num(week)}개`, "이번 주 새로"]] };
  }

  // ------------------------------------------------------------ wide windows
  function shell(titleText, badgeText, hint, back) {
    const d = modal("ov-wide-win");
    const head = el("div", "ov-win-head"), t = el("div", "ov-win-title");
    if (back) t.append(button("‹ 작게 보기", "ghost small", null, back));
    t.append(el("i", "ov-dot"), el("h2", "", titleText));
    if (badgeText != null) t.append(el("span", "ov-win-badge", badgeText)); // "" waits for a server total (hidden while empty)
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
    ({ review: wideReview, active: wideActive, sessions: wideSessions, usage: wideUsage, map: wideMap, trace: wideTrace, recall: wideRecall }[kindKey])(...args); // each sets its own recipe, so a redraw can still pop out
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

  // A dreaming proposal: what stays, what goes to history, and the person's choices (nothing is hard-deleted); 승인 대기 and the 드리밍 window share it
  function dreamDecision(c, done) { // done: after a decision, once the data is reloaded
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
      try { await request(`/v1/maintenance_proposals/${encodeURIComponent(p.id)}/decide`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: p.revision, decision }) }); await reload(); done(); }
      catch (er) { st.textContent = er.message === "revision_conflict" ? "기억이 바뀌어 이 제안은 낡았어요." : `처리하지 못했어요: ${er.message}`; act.querySelectorAll("button").forEach((b) => (b.disabled = false)); } };
    if (signal) act.append(button("다시 볼게요", "small primary", null, () => go2("accept")), button("괜찮아요", "small", null, () => go2("dismiss")));
    else act.append(button(merge ? "제안대로 합치기" : "제안대로 바꾸기", "small primary", null, () => go2("accept")), button("반대로 남기기", "small", null, () => go2("swap")), button("둘 다 두기", "small", null, () => go2("dismiss")));
    if (!signal && !merge) act.append(button("둘 다 지우기", "small danger", null, () => askConfirm("두 기억을 다 지울까요?", "둘 다 더는 쓰이지 않아요. 기억 이력에는 남아요.", () => go2("forget_both"))));
    g.append(cmp, ...(why ? [el("p", "ov-detail-why", why)] : []), act, el("small", "ov-detail-hint", signal ? "어느 쪽을 골라도 기억은 그대로예요. 다시 볼게요를 누르면 사람이 보기로 한 것만 남아요." : "아주 지우지는 않아요. 넘기거나 지운 기억은 기억 이력에 남아요."), st);
    return [head, g];
  }
  // 승인 대기 (580:49518 + improvements 592:51366 / 764:61615)
  // 드리밍 정리 제안 (maintenance proposals) wait here too, as [정리] rows with their own decide
  const DREAM_LABEL = { duplicate: "합치기", contradiction: "고치기" };
  const known = (ref) => { const m = ref && memOf(ref.id); return !!m && (ref.revision == null || m.revision === ref.revision); };
  // a proposal whose memory is gone or changed since is hidden, as on the 기억 screen: deciding it would only go stale
  const dreamItems = () => (D().maintenance || []).filter((p) => { const t = p.maintenance || {}; return t.memory ? known(t.memory) : known(t.keep) && known(t.retire); }).map((p) => { const t = p.maintenance || {};
    return { dream: p, id: p.id, created_at: p.created_at, text: memText(t.memory?.id || t.keep?.id), label: t.memory ? "고치기" : DREAM_LABEL[t.type] || "정리" }; });
  // 충돌: the existing memory beside the new candidate, resolved here with the 기억 screen's resolveConflict (its notices and guards);
  // AI 제안 only lights one button and says why, the person still clicks
  const CONFLICT_PICK = { new: "새 내용으로 바꾸기", both: "둘 다 남기기", keep_old: "기존 것 유지" };
  function conflictPanel(c, into, act, st, sec, done) { // done: after a choice, once resolveConflict has reloaded
    const cmp = el("div", "ov-detail-list"), oldCard = el("div", "ov-cmp diff-del"), newCard = el("div", "ov-cmp diff-add"), oldFrom = el("div", "ov-detail-sec");
    oldCard.append(el("small", "", "기존 기억"), el("span", "", "불러오는 중…"));
    const agentOf = (m) => tag(m, "event_id:").split(":")[0];
    newCard.append(el("small", "", ["새 후보", ago(c.created_at), agentOf(c)].filter(Boolean).join(" · ")), el("span", "", c.text));
    cmp.append(oldCard, newCard); sec(into, "충돌하는 두 기억", cmp, oldFrom);
    let old = null;
    request(`/v1/memory_relations/${encodeURIComponent(c.conflict_id)}?${query()}`)
      .then((rel) => { const id = rel?.relations?.find((r) => r.collection === "memories")?.id; return memOf(id) || request(`/v1/memories/${encodeURIComponent(id)}?${query()}`); })
      .then((m) => {
        old = m; const u = usage(m.id), n = u ? u.d30 || u.d7 || 0 : 0;
        oldCard.replaceChildren(el("small", "", ["기존 기억", `${new Date(m.created_at).toLocaleDateString(loc(), { month: "numeric", day: "numeric" })} 저장`, agentOf(m), n ? `${num(n)}번 쓰임` : "아직 안 쓰임"].filter(Boolean).join(" · ")), el("span", "", m.text));
        const box = contextBox(m); if (box) oldFrom.append(el("small", "", "기존 기억은 어디서 왔나요"), box);
      }, () => oldCard.replaceChildren(el("small", "", "기존 기억"), el("span", "", "기존 기억을 불러오지 못했어요.")));
    if (!state.caps?.actions?.review_decide) return; // the same guard as the 기억 screen: no buttons without the right to decide
    const advice = el("div", "ov-verdict"); advice.hidden = true;
    const picks = Object.entries(CONFLICT_PICK).map(([choice, label]) => { const b = button(label, "small", null, async () => {
      act.querySelectorAll("button").forEach((x) => (x.disabled = true));
      // it reports success or failure itself and reloads the memories; on a failure the notice stays and the buttons come back
      if (await ctx.resolveConflict(c, choice)) done();
      else act.querySelectorAll("button").forEach((x) => (x.disabled = false));
    }); b.dataset.choice = choice; return b; });
    const ai = button("AI 제안", "small", null, async () => {
      if (!old) { st.textContent = "기존 기억을 불러온 뒤에 AI 제안을 받을 수 있어요."; return; }
      const kit = window.ArkismAIChat?.kit; if (!kit) { st.textContent = "AI 도움을 쓸 수 없어요."; return; }
      ai.disabled = true; st.textContent = "";
      try {
        const prompt = `Two memories an AI agent is given conflict. Decide which the person should keep.\nExisting memory: ${old.text}\nNew candidate: ${c.text}\nAnswer with ONE JSON object: {"pick": "new" | "both" | "keep_old", "reason": "one short Korean sentence"}.`;
        const a = await kit.assist("json", prompt, { persona: { crew: { instructions: "You advise on one memory conflict. Reply with the JSON object only." } } });
        if (a.status !== "done") throw new Error(kit.assistError(a));
        const j = kit.parseJSON(a.output || ""); if (!CONFLICT_PICK[j?.pick]) throw new Error(tr("AI가 고르지 못했어요."));
        for (const b of picks) b.classList.toggle("primary", b.dataset.choice === j.pick);
        advice.replaceChildren(el("strong", "", `AI 제안 · ${tr(CONFLICT_PICK[j.pick])}`), ...(j.reason ? [el("p", "", String(j.reason).slice(0, 200))] : [])); advice.hidden = false;
      } catch (e) { st.textContent = `AI 제안을 받지 못했어요: ${e.message}`; }
      finally { ai.disabled = false; }
    });
    into.append(advice); act.append(...picks, ai);
  }
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
    // 필터: the kinds sit in a small bubble under this button, not a second chip row; it closes on a click outside or Esc
    const filt = el("div", "ov-filter"), pop = el("div", "ov-filter-pop"), filtBtn = button("필터 ⌄", "ghost small", null, (e) => { e.stopPropagation(); pop.hidden = !pop.hidden; filtBtn.setAttribute("aria-expanded", String(!pop.hidden)); });
    pop.hidden = true; filtBtn.setAttribute("aria-expanded", "false"); filt.append(filtBtn, pop);
    const shut = () => { if (pop.hidden) return false; pop.hidden = true; filtBtn.setAttribute("aria-expanded", "false"); return true; };
    pop.addEventListener("click", (e) => e.stopPropagation()); d.addEventListener("click", shut);
    d.addEventListener("keydown", (e) => { if (e.key === "Escape" && shut()) { e.preventDefault(); filtBtn.focus(); } });
    const bar = filterBar([filt, search, batch], sorter, chipHost);
    const RISK_RANK = { high: 0, medium: 1, low: 2 }, rank = (c) => c.dream ? 2 : RISK_RANK[c.classification?.risk] ?? (confusing(c, merges) ? 1 : 3); // no AI risk yet: 헷갈림 counts as medium
    const byOrder = (a, b) => order === "new" ? time(b.created_at) - time(a.created_at)
      : order === "risk" ? rank(a) - rank(b) || time(a.created_at) - time(b.created_at)
      : time(a.created_at) - time(b.created_at);
    const remote = pagedWindow(d, "/v1/memory_candidates/query", () => ({ q, state: "all", category: ["new", "confusing"].includes(kindF) ? kindF : "", kind: typeF, sort: order === "new" ? "recent" : order }), paint);
    const filtered = () => [...(kindF === "dream" ? [] : remote.snapshot().items), ...((kindF === "all" && !typeF) || kindF === "dream" ? dreamItems().filter(c => !q || c.text.toLowerCase().includes(q.toLowerCase())).sort(byOrder) : [])];
    const drawChips = () => { const snap = remote.snapshot(), counts = snap.facets.states || {}, sum = Object.values(counts).reduce((a, b) => a + b, 0);
      const every = (Object.keys(counts).length ? sum : snap.total ?? 0) + dreamItems().length; // 전체 = what the title counts: every waiting item and the proposals
      // a state with nothing in it is not offered, unless it is the one picked
      chipHost.replaceChildren(chipBar([["all", "전체", every], ["new", "새로 저장", counts.new || 0], ["confusing", "헷갈림", counts.confusing || 0], ["dream", "정리 제안", dreamItems().length]].filter(([k, , n]) => k === "all" || n || k === kindF), kindF, (k) => { kindF = k; if (k === "dream") typeF = ""; paint(); }));
      // 종류 in the 필터 bubble: a kind with nothing in it is not offered; the picked one again clears it
      const kinds = Object.entries(snap.facets.kinds || {}).filter(([k, n]) => n || k === typeF);
      pop.replaceChildren(el("small", "ov-filter-label", "종류"), chipBar(kinds.map(([k, n]) => [k, kind(k), n]), typeF, (k) => { typeF = k === typeF ? "" : k; paint(); }));
      filtBtn.textContent = typeF ? "필터 1 ⌄" : "필터 ⌄"; filtBtn.classList.toggle("on", !!typeF);
      filt.hidden = kindF === "dream" || (kinds.length < 2 && !typeF);
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
    function dreamDetail(c) { detail.append(...dreamDecision(c, () => { sel = null; wideReview(null, kindF); })); }
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
          catch (_) { again.disabled = false; again.title = tr("다시 보게 하지 못했어요. 한 번 더 눌러 보세요."); }
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
      if (c.conflict_id || c.status === "conflicted") conflictPanel(c, decide1, act, st, sec, () => { sel = null; wideReview(); });
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
      if (k?.evidence_span) { const basis = tag(c, "basis:"); sec(basisG, ["원문 근거", basis === "explicit_user" ? "사용자 말" : basis === "inferred" ? "AI가 짐작" : "", new Date(c.created_at).toLocaleDateString(loc(), { month: "numeric", day: "numeric" })].filter(Boolean).join(" · "), contextBox(c, k.evidence_span)); }
      if (basisG.children.length > 1) detail.append(basisG);
      if (!k?.evidence_span && tag(c, "event_id:")) sec(metaG, "어디서 왔나요", contextBox(c));
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
    const d = shell("쓰는 중인 기억", state.memoryTotals?.active == null ? "" : `${num(state.memoryTotals.active)}개`, "승인된 기억만 불러오기에 쓰여요", () => open(filter === "idle" ? "judge" : "active"));
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
      chipHost.replaceChildren(chipBar([["all", "전체", snap.facets.kinds ? Object.values(snap.facets.kinds).reduce((a,b)=>a+b,0) : snap.total ?? null], ...kinds.map(([k,n]) => [k,kind(k),n]), ["idle", "안 쓰임", kindF === "idle" ? snap.total ?? null : null]], kindF, k => { kindF=k;paint(); }));
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
      if (wu) fact(`최근 ${w.days}일`, wu.estimated ? `${num(wu.injected)}번 넣음 · ${num(wu.used)}번 실제로 쓰임 · 추정 ${num(wu.estimated)}번` : `${num(wu.injected)}번 넣음 · ${num(wu.used)}번 실제로 쓰임`);
      if (f.children.length) sec("언제 쓰이나요", f);
      const ev = tag(m, "event_id:"); if (ev) { const b = el("div", "ov-detail-from"); b.append(el("span", "", ev.split(":")[0]), button("세션 보기 ›", "ghost small", null, () => openSession(ev, m.text))); sec("어디서 왔나요", b); }
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      act.append(button("고치기", "primary small", null, () => editMemory(m, m.id)),
        button("지우기", "small", null, () => askConfirm("이 기억을 지울까요?", "지운 기억은 이력에 남지만 더는 쓰이지 않아요.", async () => {
          try { await deleteMemory(m); sel = null; await reload(); wideActive(filter); }
          catch (e) { st.textContent = `지우지 못했어요: ${e.message}`; }
        })));
      detail.append(st, act);
    }
    twoPane(d, bar, list, detail); paint();
  }
  async function deleteMemory(m) {
    await request(`/v1/memories/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) }); D().memories = D().memories.filter((x) => x.id !== m.id);
  }
  function askConfirm(title, text, onOk, ok = "지우기") {
    const d = el("dialog", "crew-dialog ov-ask"), body = el("div", "dialog-body"), foot = el("div", "ov-win-foot");
    foot.append(button("취소", "ghost small", null, () => d.close()), button(ok, "primary small", null, () => { d.close(); onOk(); }));
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
  // 세션 기록 (580:51861): every agent's sessions in one list, newest first; the agent chips and the project choice only filter.
  // Opened by wide("sessions", focus, quote) and by the arkism:open-overview-window event {wide: "sessions", args: [focus, quote]}.
  // focus: a session_id (or row id) to open with its dialogue, searched over every day; quote: words to underline in it.
  // 재개 and 이어가기 only copy a command; nothing here starts an agent.
  const SL = window.ArkismSessionList;
  const RANGES = [["today", "오늘"], ["7d", "최근 7일"], ["30d", "최근 30일"], ["all", "전체 기간"]];
  const inRange = (x, r) => r === "all" || time(x.ended_at || x.started_at) >= (r === "today" ? new Date().setHours(0, 0, 0, 0) : Date.now() - (r === "7d" ? 7 : 30) * 24 * HOUR);
  const when = (v) => (time(v) ? new Date(v).toLocaleString(loc(), { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }) : SL.NONE);
  const copyText = (text, from, done) => { Promise.resolve().then(() => from.ownerDocument.defaultView.navigator.clipboard.writeText(text)).then(() => done(true), () => done(false)); }; // a popout copies in its own window
  // stage: opened as the 대화 저장 step, which adds the pipeline summary and each session's archive state to the same list
  function wideSessions(focus = "", quote = "", stage = false) {
    const cached = env.sessions(); // the overview's last 7 days: shown at once while the window's own page loads
    const d = shell(stage ? "대화 저장" : "세션 기록", "", "저장된 대화예요. 원문은 여기서만 열려요"), badge = d.querySelector(".ov-win-badge");
    recipe(d, { wide: "sessions", args: [focus, quote, stage] });
    const sum = el("div", "ov-wide-sum"); if (stage) redraw = () => paint(); // archive states and memory names arrive later
    let q = focus, agent = "all", project = "", range = focus ? "all" : "7d", opened = "", menuFor = "", pending = focus, talk = null, find = null, facets = {};
    const xp = { busy: false, id: "", note: "" }, notes = new Map(), handoffs = new Map();
    const list = el("div", "ov-wide-list"), chipHost = el("div", "ov-wide-filter"), projHost = el("span", "ss-proj");
    const body = el("div", "ov-wide-body ss-body ss-solo"), left = el("div", "ov-wide-left"), right = el("div", "ov-wide-right ov-wide-detail");
    const search = el("input", "ov-wide-search"); search.placeholder = "제목·프로젝트로 찾기, Enter는 대화에서 찾기"; search.setAttribute("aria-label", "세션 찾기"); search.value = q;
    const findBtn = button("대화에서 찾기", "small", "magnifying-glass", () => { if (search.value.trim()) void runFind(); else search.focus(); });
    const rangeSel = sortSelect(RANGES, range, (k) => { range = k; paint(); }); rangeSel.setAttribute("aria-label", "기간");
    const bar = el("div", "ov-wide-bar"); bar.append(search, findBtn, rangeSel, projHost, button("새로고침", "ghost small", null, async () => { await env.reloadSessions(); wideSessions(); }));
    const remote = pagedWindow(d, "/v1/usage/sessions", () => ({ range, sort: "recent", q, source: agent === "all" ? "" : agent, project }), paint, true);
    const say = (id, text, code = "") => { notes.set(id, { text, code }); if (d.isConnected) paint(); };

    // the archived dialogue (POST /v1/usage/sessions/transcript: user·assistant text only, secrets and <private> never shown), 30 messages a page
    async function readTalk(x, mark = "", more = false) {
      const id = x.id, t = talk = more && talk?.id === id ? { ...talk, loading: true, error: "" } : { id, session: x, mark, items: [], next: "", archive: "", state: "", hidden: 0, pages: 0, loading: true, error: "" };
      paint(); if (!more) right.scrollTop = 0; // a new dialogue starts at its first message
      try {
        const r = await request("/v1/usage/sessions/transcript", { method: "POST", body: JSON.stringify({ scope: state.scope, id, limit: 30, ...(t.next ? { archive_id: t.archive, after: t.next } : {}) }) });
        if (talk !== t) return;
        if (r?.authority !== "data_only_untrusted" || !Array.isArray(r.items)) throw new Error("invalid_response");
        Object.assign(t, { state: r.state, archive: r.archive_id || "", next: r.next_cursor || "", items: [...t.items, ...r.items], hidden: t.hidden + (r.hidden?.secret_records || 0), pages: t.pages + 1, loading: false });
        // shortcut: looks at most 10 pages (300 messages) for the quoted words; ask the transcript for a position if sessions get longer
        if (t.mark && t.next && t.pages < 10 && !t.items.some((m) => m.text.includes(t.mark))) { void readTalk(x, t.mark, true); return; }
      } catch (e) {
        if (talk !== t) return;
        t.loading = false; t.error = /HTTP 404|not_found/.test(e?.message || "") && !t.items.length ? "이 서버는 아직 대화 보기를 지원하지 않아요." : `대화를 불러오지 못했어요 (${e.message}).`;
      }
      if (d.isConnected) paint();
    }
    function talkBox() {
      const box = el("div", "ov-detail-sec"), msgs = el("div", "ss-msgs"), empty = (t) => box.append(el("p", "ov-win-empty", t));
      const head = el("div", "ov-detail-head"), x = talk.session; head.append(button("‹ 세션 정보", "ghost small", null, closeTalk), el("span", "ov-win-tag", SL.label(x.agent)), el("small", "", when(x.started_at)));
      const h = el("h3", "", x.title || "제목 없음"); h.dataset.noI18n = ""; box.append(head, h);
      for (const [i, m] of talk.items.entries()) {
        const [b, hit] = msgLine(m, talk.mark, x.agent, talk.items[i - 1]);
        if (hit && !talk.scrolled) { talk.scrolled = true; requestAnimationFrame(() => hit.scrollIntoView({ block: "center" })); } // from a memory or a search hit: bring the words into view once
        msgs.append(b);
      }
      if (talk.items.length) box.append(msgs);
      if (talk.error) empty(talk.error);
      else if (!talk.loading && !talk.items.length) empty({ none: "이 세션은 보관된 대화가 없어요. 대화 원문은 수집된 뒤 7일 동안만 보관돼요.", expired: "보관된 대화가 만료됐거나 지워졌어요.", unsupported: "이 형식의 대화는 아직 볼 수 없어요." }[talk.state] || "보여 줄 대화가 없어요.");
      if (talk.hidden) box.append(el("p", "ss-note", `비밀값이 들어 있어 숨긴 메시지 ${talk.hidden}개`));
      if (talk.loading) empty("불러오는 중…"); else if (talk.next) box.append(button("메시지 더 보기", "small", null, () => readTalk(x, talk.mark, true)));
      return box;
    }

    // back to the list with this session's details (or the search hits) where they were
    function closeTalk() { talk = null; paint(); list.querySelector(".ss-detail")?.scrollIntoView({ block: "nearest" }); }

    // 세션 찾기 in the dialogue itself (POST /v1/usage/sessions/search), hits grouped by session; the period applies here, the server has no date filter
    async function runFind(more = false) {
      const f = more && find ? find : (find = { q: search.value.trim(), items: [], next: "", hidden: 0, secrets: 0, loading: false, error: "" });
      f.loading = true; f.error = ""; paint();
      try {
        const r = await request("/v1/usage/sessions/search", { method: "POST", body: JSON.stringify({ scope: state.scope, q: f.q, limit: 20, ...(agent === "all" ? {} : { source: agent }), ...(project ? { project } : {}), ...(f.next ? { after: f.next } : {}) }) });
        if (find !== f) return;
        if (r?.authority !== "data_only_untrusted" || !Array.isArray(r.items)) throw new Error("invalid_response");
        for (const g of r.items) { const have = f.items.find((y) => y.session.id === g.session.id); if (have) have.hits.push(...g.hits); else f.items.push(g); } // a session can come back on the next page too
        Object.assign(f, { next: r.next_cursor || "", hidden: f.hidden + (r.hidden || 0), secrets: f.secrets + (r.secret_records || 0), loading: false });
      } catch (e) {
        if (find !== f) return;
        f.loading = false; f.error = /HTTP 404/.test(e?.message || "") ? "이 서버는 아직 대화 찾기를 지원하지 않아요." : `찾지 못했어요 (${e.message}).`;
      }
      if (d.isConnected) paint();
    }
    function paintFind() {
      const f = find, head = el("div", "ss-find-head"), groups = f.items.filter((g) => inRange(g.session, range));
      head.append(el("span", "", `대화에서 "${f.q}" 찾기`), button("목록으로", "ghost small", "arrow-left", () => { find = null; paint(); }));
      list.append(head);
      for (const g of groups) {
        const hits = el("div", "ss-hits");
        for (const h of g.hits) {
          const [a, w, b] = SL.hitParts(h.snippet, h.match, f.q), line = el("button", "ss-hit-line"), p = el("p", ""); line.type = "button"; p.dataset.noI18n = "";
          if (w) { const m = el("mark", "ss-hit", w); p.append(a, m, b); } else p.append(a);
          line.append(el("b", "", h.role === "user" ? "사용자" : "에이전트"), p);
          line.addEventListener("click", () => { opened = g.session.id; void readTalk(g.session, f.q); });
          hits.append(line);
        }
        const wrap = rowEl(g.session); wrap.append(hits); list.append(wrap);
      }
      if (!groups.length && !f.loading) list.append(el("p", "ov-win-empty", f.error || (f.next ? "아직 못 찾았어요. 더 찾아볼 수 있어요." : "이 말이 들어간 대화가 없어요.")));
      if (f.items.length > groups.length) list.append(el("p", "ov-wide-foot-note", `기간 밖이라 뺀 세션 ${num(f.items.length - groups.length)}개. 기간을 넓히면 보여요.`));
      const skipped = [f.hidden && `비공개 부분에만 있어 뺀 메시지 ${num(f.hidden)}개`, f.secrets && `비밀값이 있어 건너뛴 메시지 ${num(f.secrets)}개`].filter(Boolean).join(" · ");
      if (skipped) list.append(el("p", "ov-wide-foot-note", skipped));
      if (f.loading) list.append(el("p", "ov-wide-foot-note", "대화를 찾는 중…"));
      else if (f.next) list.append(button("더 찾기", "small", null, () => runFind(true)));
    }

    // 이어가기: what the server can hand over (saved work state or archived dialogue), turned into the other agent's start command and copied
    async function continueIn(x, target, from) {
      menuFor = ""; say(x.id, `${SL.label(target)}에 넘길 내용을 읽는 중…`);
      let h = handoffs.get(x.id);
      try {
        if (!h) {
          h = await request("/v1/usage/sessions/handoff", { method: "POST", body: JSON.stringify({ scope: state.scope, id: x.id }) });
          if (h?.authority !== "data_only_untrusted") throw new Error("invalid_response");
          handoffs.set(x.id, h);
        }
      } catch (e) { say(x.id, /HTTP 404|not_found/.test(e?.message || "") ? "이어갈 내용을 찾지 못했어요. 서버가 아직 이어가기를 지원하지 않을 수도 있어요." : `이어갈 내용을 읽지 못했어요 (${e.message}).`); return; }
      const cmd = SL.continueCommand(target, h);
      if (!cmd) { say(x.id, "넘겨줄 작업 상태나 보관된 대화가 없어요."); return; }
      const what = h.work ? `저장된 작업 "${h.work.title || h.work.id}"` : "보관된 대화";
      copyText(cmd, from, (ok) => say(x.id, ok ? `${SL.label(target)} 시작 명령을 복사했어요. ${what}를 읽고 이어가요.` : "복사하지 못했어요. 아래 명령을 직접 복사해 주세요.", cmd));
    }
    function continueMenu(x, seen) {
      const m = el("div", "cat-menu ss-menu"); m.setAttribute("role", "menu");
      const targets = SL.continueTargets(x.agent, seen);
      for (const a of targets) {
        const b = el("button", "cat-menu-item", `${SL.label(a)}에서 이어가기`); b.type = "button"; b.setAttribute("role", "menuitem");
        b.addEventListener("click", (ev) => { ev.stopPropagation(); void continueIn(x, a, ev.currentTarget); }); m.append(b);
      }
      if (!targets.length) { const b = el("button", "cat-menu-item", "이어갈 다른 에이전트가 없어요"); b.type = "button"; b.disabled = true; m.append(b); }
      m.append(el("small", "ss-menu-note", "명령만 복사해요. 실행하지 않아요."));
      requestAnimationFrame(() => m.querySelector("button:not(:disabled)")?.focus());
      return m;
    }

    function factList(x) {
      const f = el("div", "ov-detail-facts ss-facts");
      const fact = (k, v, copy = "") => {
        const p = el("p", ""), val = el("strong", "", v); val.dataset.noI18n = "";
        p.append(el("span", "", k), val);
        if (copy) { const b = button("", "ghost small ss-copy", "copy", (ev) => copyText(copy, ev.currentTarget, (ok) => say(x.id, ok ? `${k}을(를) 복사했어요.` : "복사하지 못했어요."))); b.setAttribute("aria-label", `${k} 복사`); b.title = `${k} 복사`; val.append(b); }
        f.append(p);
      };
      fact("시간", time(x.started_at) ? `${when(x.started_at)} – ${when(x.ended_at)}` : SL.NONE);
      fact("작업 시간", x.active_seconds ? `${Math.max(1, Math.round(x.active_seconds / 60))}분` : SL.NONE);
      fact("기기", x.device || SL.NONE);
      const kept = stage && archiveOf(x.id); if (kept) fact("원문 보관", kept.long);
      fact("폴더", x.folder || SL.NONE, x.folder);
      fact("세션 ID", x.session_id || SL.NONE, x.session_id);
      const cmd = SL.resumeCommand(x);
      fact("재개 명령", cmd || "이 에이전트의 재개 명령은 몰라요.", cmd);
      fact("토큰", x.tokens > 0 ? `${SL.tokens(x)} (${SL.split(x)})` : SL.NONE);
      fact("비용", SL.cost(x));
      const ms = SL.models(x);
      fact("모델", ms.length ? ms.map(SL.modelLine).join("\n") : SL.NONE);
      fact("파일", x.file || SL.NONE, x.file);
      return f;
    }
    function detailEl(x, seen) {
      const box = el("div", "ss-detail");
      box.append(factList(x));
      const cont = el("div", "ov-detail-sec"); cont.append(el("small", "", "다른 에이전트로 이어가기"));
      const ca = el("div", "ov-detail-actions"), targets = SL.continueTargets(x.agent, seen);
      for (const a of targets) ca.append(button(SL.label(a), "small", "copy", (ev) => continueIn(x, a, ev.currentTarget)));
      cont.append(targets.length ? ca : el("p", "ov-win-empty", "이어갈 다른 에이전트가 없어요."), el("small", "ov-detail-hint", "저장된 작업 상태가 있으면 그걸, 없으면 보관된 대화를 읽도록 시작 명령을 만들어 복사해요."));
      box.append(cont);
      const ss = sessionState(x), fl = el("div", "ov-flow");
      FLOW.forEach((n, k) => { if (k) fl.append(el("i", "", "›")); fl.append(el("span", `ov-flow-step${k + 1 < ss.step ? " done" : k + 1 === ss.step ? " now" : ""}`, n)); });
      const fs = el("div", "ov-detail-sec"); fs.append(el("small", "", "이 세션의 흐름"), fl); box.append(fs);
      const cs = el("div", "ov-detail-sec"); cs.append(el("small", "", "여기서 나온 후보"));
      for (const m of [...ss.cands, ...ss.approved]) { const rw = el("div", "ov-detail-cand"); rw.append(el("span", "", clip(m.text, 60)), el("small", "", ss.cands.includes(m) ? "승인 대기" : "승인됨")); if (ss.cands.includes(m)) { rw.classList.add("link"); rw.addEventListener("click", () => wide("review", m.id)); } cs.append(rw); }
      if (!ss.cands.length && !ss.approved.length) cs.append(el("p", "ov-win-empty", "이 세션에서 나온 후보가 없어요."));
      box.append(cs);
      const st = el("p", "ov-win-err", ""), act = el("div", "ov-detail-actions");
      // the export's progress and result live on the window, so a repaint mid-request keeps them
      const exportAs = async (format) => {
        if (xp.busy) return;
        Object.assign(xp, { busy: true, id: x.id, note: "" }); paint();
        let url = "";
        try {
          const r = await request("/v1/usage/sessions/export", { method: "POST", body: JSON.stringify({ scope: state.scope, ids: [x.id], format, transcript: true }) });
          if (typeof r?.content !== "string") throw new Error("invalid_response");
          url = URL.createObjectURL(new Blob([r.content], { type: r.content_type || "text/plain" }));
          const link = el("a"); link.href = url; link.download = r.filename || (format === "json" ? "arkism-session.json" : "arkism-session.md"); link.click();
          xp.note = r.truncated ? "다운로드를 시작했어요. 너무 길어 일부 대화는 잘렸어요." : "세션 다운로드를 시작했어요."; // a link click cannot tell whether the save went through
        } catch (e) { xp.note = `내보내지 못했어요 (${e.message}).`; }
        finally { if (url) setTimeout(() => URL.revokeObjectURL(url), 1000); xp.busy = false; if (d.isConnected) paint(); }
      };
      if (xp.id === x.id) st.textContent = xp.busy ? "내보내는 중…" : xp.note;
      const md = button("Markdown 내보내기", "small", null, () => exportAs("markdown")), js = button("JSON", "small", null, () => exportAs("json"));
      md.disabled = js.disabled = xp.busy;
      act.append(talk?.id === x.id ? button("대화 닫기", "small", null, closeTalk) : button("대화 보기", "primary small", "chat-text", () => readTalk(x)), md, js,
        button("세션 지우기", "small", "trash", () => askConfirm("이 세션을 지울까요?", "세션과 보관된 대화가 지워지고 되돌릴 수 없어요. 에이전트의 원래 파일은 그대로 남고, 이 대화를 출처로 둔 기억은 내용은 남고 출처 연결만 사라져요.", async () => {
          try { await request("/v1/usage/sessions/delete", { method: "POST", body: JSON.stringify({ scope: state.scope, ids: [x.id] }) }); await env.reloadSessions(); wideSessions(); }
          catch (e) { st.textContent = /access_denied/.test(e?.message || "") ? "지울 권한이 없어요." : `지우지 못했어요: ${e.message}`; }
        })));
      const cv = el("div", "ov-detail-sec"); cv.append(el("small", "", "대화"), act, st); box.append(cv);
      return box;
    }
    function rowEl(x, seen = agentsSeen()) {
      const wrap = el("div", "ss-row"), r = el("div", `ov-wide-row${x.id === opened ? " on" : ""}`), t = el("div", "ov-wide-row-t");
      r.setAttribute("role", "button"); r.tabIndex = 0; r.setAttribute("aria-expanded", String(x.id === opened));
      const title = el("span", "ov-win-item-title", x.title || "제목 없음"); title.dataset.noI18n = "";
      t.append(title, el("small", "", [SL.label(x.agent), x.project, ago(x.ended_at || x.started_at), SL.mainModel(x), `메시지 ${num(x.messages)}개`].filter(Boolean).join(" · ")));
      const st = sessionState(x), rr = el("span", "ov-wide-row-r"), tok = el("span", "ss-num", x.tokens > 0 ? `${SL.tokens(x)} 토큰` : `토큰 ${SL.NONE}`);
      if (x.tokens > 0) tok.title = SL.split(x);
      const keep = stage && archiveOf(x.id);
      rr.append(tok, el("span", "ss-num", SL.cost(x)), el("span", `ov-pill ov-pill-${st.tone}`, st.label), ...(keep ? [el("span", "ss-num", keep.short)] : []));
      const cmd = SL.resumeCommand(x), resume = button("재개", "small", "copy", (ev) => { ev.stopPropagation(); copyText(cmd, ev.currentTarget, (ok) => say(x.id, ok ? "재개 명령을 복사했어요. 터미널에 붙여 넣으세요." : "복사하지 못했어요. 아래 명령을 직접 복사해 주세요.", cmd)); });
      resume.disabled = !cmd; resume.title = cmd || "이 에이전트의 재개 명령은 몰라요.";
      const more = button("이어가기", "small ss-more", "caret-down", (ev) => { ev.stopPropagation(); menuFor = menuFor === x.id ? "" : x.id; paint(); });
      more.setAttribute("aria-haspopup", "menu"); more.setAttribute("aria-expanded", String(menuFor === x.id));
      rr.append(resume, more);
      r.append(t, rr);
      const toggle = () => { opened = opened === x.id ? "" : x.id; menuFor = ""; paint(); };
      r.addEventListener("click", toggle); r.addEventListener("keydown", (ev) => { if ((ev.key === "Enter" || ev.key === " ") && ev.target === r) { ev.preventDefault(); toggle(); } });
      wrap.append(r);
      if (menuFor === x.id) wrap.append(continueMenu(x, seen));
      const n = notes.get(x.id);
      if (n) { const p = el("div", "ss-say"); p.append(el("span", "", n.text)); if (n.code) { const c = el("code", "", n.code); c.dataset.noI18n = ""; p.append(c); } wrap.append(p); }
      if (opened === x.id) wrap.append(detailEl(x, seen));
      return wrap;
    }
    const agentsSeen = () => [...Object.keys(facets.agents || {}), ...cached.map((x) => x.agent)].filter(Boolean);

    function paint() {
      const snap = remote.snapshot();
      if (snap.ready) facets = snap.facets || {};
      const early = !snap.ready && !snap.error && !snap.items.length && !q && !project && range === "7d"; // the cached page answers the default filters only
      const rows = early ? cached.filter((x) => agent === "all" || x.agent === agent) : snap.items;
      if (stage) { sum.replaceChildren(collectBox()); archives(rows.slice(0, 50).map((x) => x.id)); } // the rows read the shown page's states
      badge.textContent = snap.total != null ? `${num(snap.total)}건` : early ? `${num(cached.length)}건` : "";
      // counts beside each agent come from the server (every page, this period and project); before it answers, from the cached page
      // an older server sends no counts: its agents still get a chip, without a number
      const counts = snap.ready && facets.agents ? facets.agents : early ? cached.reduce((m, x) => ({ ...m, [x.agent]: (m[x.agent] || 0) + 1 }), {}) : null;
      const known = counts ? Object.entries(counts).filter(([a]) => a).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])) : [...new Set(agentsSeen().concat(rows.map((x) => x.agent)))].filter(Boolean).map((a) => [a, null]);
      if (agent !== "all" && !known.some(([a]) => a === agent)) known.push([agent, counts ? 0 : null]);
      chipHost.replaceChildren(chipBar([["all", "전체", counts ? known.reduce((n, [, c]) => n + c, 0) : null], ...known.map(([a, c]) => [a, SL.label(a), c])], agent, (k) => { agent = k; if (find) void runFind(); paint(); }));
      const projects = Object.entries(facets.projects || {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
      if (project && !projects.some(([p]) => p === project)) projects.unshift([project, 0]);
      const ps = sortSelect([["", "모든 프로젝트"], ...projects.map(([p, c]) => [p, `${p} ${num(c)}`])], project, (k) => { project = k; if (find) void runFind(); paint(); }); ps.setAttribute("aria-label", "프로젝트");
      projHost.replaceChildren(ps);
      list.replaceChildren();
      if (find) paintFind();
      else {
        if (pending && !snap.loading && snap.ready) { // the asked-for session, once the search answered; a missing one must not look like the first row
          const hit = rows.find((x) => x.session_id === pending || x.id === pending); pending = "";
          if (hit) { opened = hit.id; void readTalk(hit, quote.trim()); return; } // readTalk paints again with it open
          list.append(el("p", "ov-win-empty", "이 세션을 찾지 못했어요. 지워졌거나 보관 기간이 지났을 수 있어요."));
        }
        if (!rows.length) list.append(el("p", "ov-win-empty", snap.loading || !snap.ready ? "불러오는 중…" : snap.error ? `불러오지 못했어요 (${snap.error}).` : "찾은 세션이 없어요."));
        const seen = agentsSeen();
        for (const x of rows) list.append(rowEl(x, seen));
        if (early) { if (rows.length) list.append(el("p", "ov-wide-foot-note", "최근 목록을 먼저 보여 줘요. 새 목록을 불러오는 중…")); }
        else remote.footer(list);
      }
      body.classList.toggle("ss-solo", !talk); body.classList.toggle("ss-talk", !!talk);
      right.replaceChildren(...(talk ? [talkBox()] : []));
    }
    search.addEventListener("input", () => { q = search.value.trim(); if (!find) paint(); });
    search.addEventListener("keydown", (ev) => { if (ev.key === "Enter" && search.value.trim()) { ev.preventDefault(); void runFind(); } });
    d.addEventListener("click", () => { if (menuFor) { menuFor = ""; paint(); } }); // a click outside the open 이어가기 menu closes it
    d.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && menuFor) { ev.preventDefault(); menuFor = ""; paint(); } });
    left.append(bar, chipHost, ...(stage ? [sum] : []), list); body.append(left, right); d.append(body); paint();
  }

  // 사용량 (580:54204)
  let usageWin = null; // redrawn when GET /v1/memory_usage settles while it is open (usageChanged)
  function wideUsage() {
    const items = env.sessions(), w = D().tools?.memory_use, recalls = [...(state.usage?.entries() || [])];
    const known = (v) => (state.usageDone ? v : "—"); // an unread /v1/memory_usage is not 0 recalls (A2)
    const d = usageWin = shell("사용량", known(`7일 ${num(recalls.reduce((a, [, u]) => a + (u.d7 || 0), 0))}회`), "어떤 기억이 어디에 쓰였는지 보여요");
    recipe(d, { wide: "usage", args: [] });
    redraw = wideUsage;
    const body = el("div", "ov-wide-body"), left = el("div", "ov-wide-left"), right = el("div", "ov-wide-right ov-wide-detail");
    const tiles = el("div", "ov-win-tiles ov-win-tiles-4"); const rate = w?.injections ? Math.round((w.used / w.injections) * 100) : null;
    for (const [v, l] of [[known(`${num(recalls.reduce((a, [, u]) => a + (u.d7 || 0), 0))}회`), "7일 불러온 횟수"], [rate === null ? "—" : `${rate}%`, "실제로 쓰임"], [`${num(items.length)}건`, "7일 세션"], [known(`${num(recalls.filter(([, u]) => u.d7 > 0).length)}개`), "불러온 기억"]]) { const t = el("div", "ov-win-tile"); t.append(el("strong", "", v), el("span", "", l)); tiles.append(t); }
    const hours = new Array(24).fill(0), now = new Date().setMinutes(0, 0, 0);
    for (const x of items) { const t = time(x.ended_at || x.started_at), dd = Math.ceil((now - t) / HOUR); if (t && dd >= 0 && dd < 24) hours[23 - dd]++; else if (t >= now && t <= Date.now()) hours[23]++; }
    const chart = el("div", "ov-win-chart"), bars = el("div", "ov-win-bars"), max = Math.max(1, ...hours); chart.append(el("div", "ov-win-chart-title", "시간대별 세션 (최근 24시간)"));
    hours.forEach((n) => { const b = el("i", n ? "" : "none"); b.style.height = `${6 + (n / max) * 54}px`; b.title = `${n}`; bars.append(b); }); chart.append(bars, el("div", "ov-win-axis", "24시간 전 ··· 지금"));
    left.append(tiles, chart, el("div", "ov-win-list-title", "최근 사용 기록"));
    const ul = el("div", "ov-win-list"); ul.append(...recalls.filter(([, u]) => u.last).sort((a, b) => time(b[1].last) - time(a[1].last)).slice(0, 30).map(([id, u]) => row("", clip(memText(id)), ago(u.last), `7일 ${num(u.d7 || 0)}회`))); if (!ul.children.length) ul.append(el("p", "ov-win-empty", state.usageFailed ? "서버가 불러오기 집계를 주지 못했어요. 잠시 뒤 다시 열어 보세요." : state.usageDone ? "지금 보여 줄 항목이 없어요." : "불러오는 중…")); left.append(ul);
    const sec = (title, ...nodes) => { const s = el("div", "ov-detail-sec"); s.append(el("small", "", title), ...nodes); right.append(s); };
    right.append(el("h3", "", "한눈에"));
    { const f = el("div", "ov-detail-facts"); for (const [a, b] of [["불러온 횟수", known(`${num(recalls.reduce((n, [, u]) => n + (u.d30 || 0), 0))}회`)], ["불러온 기억", known(`${num(recalls.filter(([, u]) => u.d30 > 0).length)}개`)]]) { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); } sec("최근 30일", f); }
    const byAgent = {}; items.forEach((x) => { byAgent[x.agent || "기타"] = (byAgent[x.agent || "기타"] || 0) + 1; });
    const agentBox = el("div", "ov-detail-bars"); for (const [a, n] of Object.entries(byAgent).sort((x, y) => y[1] - x[1])) { const p = el("div", "ov-detail-bar"), pct = Math.round((n / Math.max(1, items.length)) * 100); const track = el("i"); const fill = el("b"); fill.style.width = `${pct}%`; track.append(fill); const line = el("p", ""); line.append(el("span", "", a), el("strong", "", `${pct}%`)); p.append(line, track); agentBox.append(p); }
    if (items.length) sec("에이전트별 (7일 세션)", agentBox);
    const top = recalls.filter(([, u]) => u.d7 > 0).sort((a, b) => b[1].d7 - a[1].d7).slice(0, 3); if (top.length) { const b = el("div", "ov-detail-list"); top.forEach(([id, u]) => b.append(row("", clip(memText(id), 40), "", `${num(u.d7)}번`))); sec("많이 쓰인 기억", b); }
    const idles = (w?.items || []).filter(idle).slice(0, 3); if (idles.length) { const b = el("div", "ov-detail-list"); idles.forEach((u) => b.append(row("", clip(memText(u.id), 40), "", `${num(u.injected)}번 중 ${num(u.used)}번`))); sec("넣었는데 안 쓰인 기억", b); }
    const act = el("div", "ov-detail-actions"); act.append(button("쓰임 확인 보기", "primary small", null, () => open("judge")), button("사용량 화면으로", "small", null, () => { dlg.close(); go("usage"); })); right.append(act);
    body.append(left, right); d.append(body);
  }

  // 사용 기록 (SCREEN-008: 기억이 실제로 도움이 됐는지 본다). One row per memory: 넣음·쓰임 from the host's memory_use report
  // (its days window), 불러옴 and the last use from GET /v1/memory_usage (7·30 days). Nothing here writes.
  const TRACE_SORT = [["used", "답에 쓰인 순"], ["injected", "넣은 순"], ["last", "최근 불러온 순"], ["trend", "요즘 늘어난 순"]];
  // 추세: this week's recalls against the weekly average of the 23 days before it; null while /v1/memory_usage has no row
  function traceTrend(r) {
    if (r.d30 == null) return null;
    const before = Math.max(0, r.d30 - r.d7) * 7 / 23, delta = r.d7 - before;
    const label = !r.d30 ? "안 불러옴" : !before ? (r.d7 ? "새로 쓰임" : "안 불러옴") : r.d7 >= before * 1.5 ? "늘었어요 ↑" : r.d7 <= before * 0.5 ? "줄었어요 ↓" : "비슷해요";
    return { before, delta, label };
  }
  function traceRows() {
    const by = new Map(), blank = (id) => ({ id, injected: 0, used: 0, estimated: 0, host: null });
    for (const u of D().tools?.memory_use?.items || []) by.set(u.id, { ...blank(u.id), injected: u.injected || 0, used: u.used || 0, estimated: u.estimated || 0, host: u });
    for (const [id, u] of state.usage || []) by.set(id, { ...(by.get(id) || blank(id)), d7: u.d7 || 0, d30: u.d30 || 0, last: u.last || "" });
    return [...by.values()].map((r) => ({ ...r, trend: traceTrend(r) }));
  }
  const TRACE_ORDER = { used: (a, b) => b.used - a.used || b.injected - a.injected, injected: (a, b) => b.injected - a.injected || b.used - a.used,
    last: (a, b) => time(b.last) - time(a.last), trend: (a, b) => (b.trend?.delta ?? -1e9) - (a.trend?.delta ?? -1e9) };
  let traceWin = null;
  function wideTrace(filter = "all", focusId = null, sort = "used") {
    const w = D().tools?.memory_use, all = traceRows(), days = w?.days || 30;
    const d = traceWin = shell("사용 기록", w ? `${num(days)}일 · 넣음 ${num(w.injections || 0)}번` : "", "기억이 실제로 답에 도움이 됐는지 봐요");
    recipe(d, { wide: "trace", args: [filter, focusId, sort] });
    redraw = () => wideTrace(filter, sel, sort);
    let sel = focusId;
    const list = el("div", "ov-wide-list"), detail = el("div", "ov-wide-detail"), chipHost = el("div");
    const keep = (r) => filter === "all" || (filter === "used" ? r.used > 0 : idle(r.host));
    const chips = [["all", "전체", all.length], ["used", "답에 쓰임", all.filter((r) => r.used > 0).length], ["idle", "안 쓰임", all.filter((r) => idle(r.host)).length]];
    chipHost.append(chipBar(chips, filter, (k) => wideTrace(k, null, sort)));
    const bar = filterBar([], sortSelect(TRACE_SORT, sort, (k) => wideTrace(filter, sel, k)), chipHost);
    const rows = all.filter(keep).sort(TRACE_ORDER[sort] || TRACE_ORDER.used);
    if (!rows.some((r) => r.id === sel)) sel = rows[0]?.id || null;
    for (const r of rows) {
      const m = memOf(r.id), x = el("div", `ov-wide-row${r.id === sel ? " on" : ""}`), t = el("div", "ov-wide-row-t"), l1 = el("div", "ov-win-item-t");
      x.tabIndex = 0; x.setAttribute("role", "button"); x.setAttribute("aria-pressed", String(r.id === sel));
      if (m) l1.append(tagEl(kind(m.kind)));
      l1.append(el("span", "ov-win-item-title", clip(memText(r.id).split("\n")[0], 70))); t.append(l1);
      t.append(el("small", "", [r.last ? `마지막 불러옴 ${ago(r.last)}` : "", r.trend?.label].filter(Boolean).join(" · ")));
      const right = el("div", "ov-wide-row-r"); right.append(el("span", "", `넣음 ${num(r.injected)}`), el("span", r.used ? "" : "ov-r-a", `쓰임 ${num(r.used)}`));
      x.append(t, right);
      const pick = () => wideTrace(filter, r.id, sort);
      x.addEventListener("click", pick); x.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pick(); } });
      list.append(x);
    }
    if (!rows.length) list.append(el("p", "ov-win-empty", state.usageDone || w ? "이 조건에 맞는 기억이 없어요." : "불러오는 중…"));
    const r = rows.find((y) => y.id === sel);
    if (r) {
      const m = memOf(r.id), h = el("div", "ov-detail-head");
      if (m) h.append(tagEl(kind(m.kind)));
      h.append(el("small", "", r.last ? `마지막 불러옴 ${ago(r.last)}` : "최근 불러온 기록 없음"));
      const sec = (title, ...nodes) => { const s2 = el("div", "ov-detail-sec"); s2.append(el("small", "", title), ...nodes); detail.append(s2); };
      detail.append(h, el("h3", "", memText(r.id)));
      const facts = el("div", "ov-detail-facts"), fact = (a, b) => { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); facts.append(p); };
      fact("답에 넣은 횟수", `${num(r.injected)}번`); fact("답에 쓰였다고 남긴 횟수", `${num(r.used)}번`);
      if (r.estimated) fact("답 내용과 겹침 (추정)", `${num(r.estimated)}번`);
      fact("쓰인 비율", r.injected ? `${Math.round((r.used / r.injected) * 100)}%` : "—");
      sec(`최근 ${num(days)}일`, facts);
      if (r.trend) {
        const bars = el("div", "ov-detail-bars"), max = Math.max(1, r.d7, r.trend.before);
        for (const [a, n] of [["이번 주", r.d7], ["그전 주 평균", Math.round(r.trend.before * 10) / 10]]) { const p = el("div", "ov-detail-bar"), track = el("i"), fill = el("b"), line = el("p", ""); fill.style.width = `${Math.round((n / max) * 100)}%`; track.append(fill); line.append(el("span", "", a), el("strong", "", `${num(n)}번`)); p.append(line, track); bars.append(p); }
        sec(`불러온 횟수 · ${r.trend.label}`, bars);
      }
      if (idle(r.host)) detail.append(note("a", "넣었는데 답에 안 쓰였어요", "내용이 낡았거나 너무 넓을 수 있어요. 고치거나 검증에서 정리해 보세요."));
      const act = el("div", "ov-detail-actions"); act.append(button("고치기", "primary small", null, () => editMemory(m, r.id)), button("검증 보기", "small", null, () => open("judge"))); detail.append(act);
    } else detail.append(el("p", "ov-win-empty", "왼쪽에서 기억을 고르면 자세히 보여요."));
    twoPane(d, bar, list, detail);
  }

  // 기억 지도 (611:54134 · 611:56374): GET /v1/memory-graph?mode=all, grouped by 칸 (reach), curved lines between memories.
  // The map has no AI-suggestion write API (POST /v1/knowledge-graph/relations writes a separate store the map never reads), so 연결/아니요 are not drawn.
  const REACH = { personal: ["개인", 0], general: ["범용", 1], project: ["프로젝트", 2] };
  const EDGE = { supports: "지지", supersedes: "대체", conflicts: "충돌", human_link: "연결", same_session: "관련", similar: "비슷" };
  const REACH_COLOR = { personal: "#7fc79c", general: "#6aa3ff", project: "#f5b955" }; // same colours as the 칸 chips (v4-overview.css .ov-map-personal …)
  // 고치기: the edit popup over this window (it stays open), reading the memory first when the map has only its label
  async function editMemory(m, id) {
    m = m || await request(`/v1/memories/${encodeURIComponent(id)}?${query()}`).catch(() => null);
    if (m && (m.status ?? "active") === "active" && ctx.fixMemory) { ctx.fixMemory(m); return; }
    dlg?.close(); go("memories", "active");
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
      // the note takes its own top line, and the zoom buttons wrap as one group, so ⤢ never drops alone (#181)
      const note = bubbles.groupLinesOnly() ? [el("span", "ov-map-lg-note", "점이 많아 무리 사이 선만 그려요")] : [], zoom = el("span", "ov-map-lg-zoom");
      const tog = el("div", "ov-map-lg-tog"); for (const [v, l] of [[true, "전체 관계"], [false, "선택한 연결"]]) { const on = mode === "all" ? v : v === allEdges, b = el("button", on ? "on" : "", l); b.type = "button"; b.disabled = mode === "all"; b.setAttribute("aria-pressed", String(on)); b.addEventListener("click", () => { allEdges = v; drawMap(); drawLegend(); }); tog.append(b); }
      zoom.append(zb("−", "축소", () => bubbles.zoomBy(0.8)), zoomLab, zb("+", "확대", () => bubbles.zoomBy(1.25)), zb("⤢", "화면에 맞추기", () => bubbles.fitView()));
      legend.replaceChildren(...note, items, el("em", ""), tog, el("em", ""), zoom);
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
        for (const [a, b] of [["전체 기억", `${num(nodes.length)}${("nodes_truncated" in graph ? graph.nodes_truncated : graph.truncated) ? "+" : ""}`], ["이어진 선", num(edges.filter((e) => e.type !== "similar").length)]]) { const p = el("p", ""); p.append(el("span", "", a), el("strong", "", b)); f.append(p); } sec("", f);
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

  return { open, wide, reset() { dlg?.close(); read.clear(); recalls = null; once.clear(); }, usageChanged() { if (usageWin && usageWin === dlg) wideUsage(); if (traceWin && traceWin === dlg) wideTrace(); } };
};
