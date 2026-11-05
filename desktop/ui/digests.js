// @ts-check
// MEMORY › 기억 › 모아보기 "주제 요약 · 나에 대한 요약" (Figma 382:29313 · 382:29983) and
// 확인해 주세요 "병합 제안" (383:29822), plus their HOME rows (385:30743).
//   /v1/digests?kind=topic|user&status=…   요약은 승인된 기억으로만 만든다. AI가 쓴 새 버전은 늘 후보이고, 사람이 승인해야 쓰인다.
//   /v1/merge_proposals?status=open        새 후보를 기존 기억에 합칠지·따로 저장할지·건너뛸지 AI가 제안한다. 누르기 전엔 아무것도 바뀌지 않는다.
//   /v1/observations?status=proposed, /v1/mental-models, /v1/causal-links?status=candidate   확인해 주세요 "AI 정리":
//     AI가 기억에서 뽑은 정리, 주제 요약 초안, 원인과 결과 제안. 사람이 승인해야 쓰인다.
//   모아보기 also manages what a person set up: 직접 정한 주제 (/v1/mental-models: create, edit, trigger, history, clear, download)
//     and 기억 정리 (/v1/observations: history, scopes, delete, 지금 정리하기 with /v1/observations/consolidate, strategy preview).
// Everything the server returns here is data (authority data_only_untrusted): drawn as plain text, never as markup or instructions.
window.ArkismDigests = (ctx) => {
  "use strict";
  const { state, $, el, raw, button, request, query, detailShell, openDetail, notice, ago, render, row, markSelected, kindLabel, originOf } = ctx;
  const STALE_MS = 60000;
  const day = (v) => (v ? new Date(v).toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }) : "—");
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const PROPOSAL = { new: "새 주제", update: "고침", merge: "두 요약 합치기" };
  const JUDGE = { conflict: "기존과 충돌", new_rule: "새 규칙", minor: "작은 보완 누적", requested: "직접 요청" };
  const JUDGE_LEAD = {
    conflict: "최근 대화에서 바뀐 습관이 보여서 AI가 요약 후보를 만들었어요. 바꿀까요?",
    new_rule: "새 규칙이 생겨서 AI가 요약 후보를 만들었어요. 바꿀까요?",
    minor: "작은 보완이 쌓여서 AI가 요약 후보를 만들었어요. 바꿀까요?",
    requested: "요청하신 대로 요약 후보를 다시 만들었어요. 바꿀까요?"
  };
  const STATUS = [["active", "상태: 사용 중"], ["rejected", "상태: 거절됨"], ["superseded", "상태: 이전 버전"], ["stale", "상태: 못 씀"]];
  const SORT = [["heat", "자주 쓰는 순"], ["recent", "최근 바뀐 순"], ["name", "이름 순"]];
  const ACTION = { merge: "합치기", store: "저장", skip: "건너뛰기" };
  const REASON = { new_information: "새 정보", same_meaning: "같은 뜻", more_specific: "더 구체적", adds_detail: "내용 보탬", already_covered: "이미 있음", updates_older: "옛 내용 갱신" };

  // ------------------------------------------------------------ data
  const empty = () => ({ key: "", at: 0, topic: [], topicCand: [], cap: 0, activeN: 0, user: null, userCand: [], userOld: [], merge: null, notes: null, models: [], other: {} });
  let data = empty(), loading = null, filter = { view: "topics", chip: "all", status: "active", sort: "heat", q: "", obsStatus: "active", obsScope: "" }, picked = false;
  // 기억 정리 lists by status, its scopes, revision histories and the 지금 정리하기 run; dropped with the identity or on reload.
  const freshSide = (key) => ({ key, obs: {}, scopes: undefined, hist: new Map(), run: null, strategies: undefined });
  let side = freshSide(""), editing = null; // editing: the mental model id whose form is open, "new" for a new one
  const texts = new Map(); // "collection/id" → record (member memories, merge candidates and targets)
  const views = new Map(); // merge proposal id → GET /v1/merge_proposals/{id}
  // A route an older server lacks (404) is "not supported"; any other failure is FAILED, shown as "couldn't load", never as empty (#465).
  const FAILED = Object.freeze({ failed: true });
  const absent = (e) => (e?.status === 404 || e?.message === "not_found" ? null : FAILED);
  const list = (kind, status) => request(`/v1/digests?${query({ kind, status })}`).catch(absent);
  const instructionLike = (d) => (d.tags || []).includes("review:instruction_like");

  function ensure() {
    if (loading) return;
    if (data.key === identity() && Date.now() - data.at < STALE_MS) return;
    if (data.key === identity() && ctx.canRefresh && !ctx.canRefresh()) return;
    void load();
  }
  async function load() {
    const key = identity();
    loading = (async () => {
      const [ta, tc, ua, uc, uo, mp, ob, mm, cl, pm] = await Promise.all([list("topic", "active"), list("topic", "candidate"), list("user", "active"), list("user", "candidate"), list("user", "superseded"),
        request(`/v1/merge_proposals?${query({ status: "open" })}`).catch(absent),
        request(`/v1/observations?${query({ status: "proposed", limit: 100 })}`).catch(absent), request(`/v1/mental-models?${query({ limit: 100 })}`).catch(absent),
        request(`/v1/causal-links?${query({ status: "candidate", limit: 100 })}`).catch(absent),
        request(`/v1/mental-models?${query({ status: "proposed", limit: 100 })}`).catch(() => null)]); // an older server refuses status=proposed
      if (key !== identity()) return;
      const before = JSON.stringify([data.topic, data.topicCand, data.user, data.userCand, data.merge, data.failed, data.notes]);
      const items = (p) => (Array.isArray(p?.items) ? p.items : []);
      Object.assign(data, { key, at: Date.now(), topic: items(ta), topicCand: items(tc), cap: ta?.cap || tc?.cap || 0, activeN: ta?.active ?? items(ta).length,
        user: items(ua)[0] || null, userCand: items(uc), userOld: items(uo), failed: [ta, tc, ua, uc, uo].includes(FAILED),
        merge: mp === FAILED ? false : mp ? items(mp).filter((r) => r.merge?.proposal) : null, // merge: false failed, null not supported
        models: items(mm), modelsRead: mm === FAILED ? false : mm ? true : null, notes: [ob, mm, cl].includes(FAILED) ? false : !ob && !mm && !cl ? null
          : [...items(ob).map((o) => ({ id: `obs:${o.id}`, obs: o })), ...items(pm).filter((m) => m.status === "proposed").map((m) => ({ id: `model:${m.id}`, model: m })), ...items(mm).filter((m) => m.draft || m.stale).map((m) => ({ id: `model:${m.id}`, model: m })),
            ...items(cl).filter((l) => l.cause?.id && l.effect?.id).map((l) => ({ id: `causal:${l.id}`, link: l }))] });
      if (key !== data.other.key) data.other = { key };
      if (key !== side.key) side = freshSide(key);
      if (before !== JSON.stringify([data.topic, data.topicCand, data.user, data.userCand, data.merge, data.failed, data.notes])) repaint();
    })().finally(() => { loading = null; });
    return loading;
  }
  function repaint() {
    const here = state.view === "memories" && ["digests", "merge", "notes"].includes(state.memTab);
    const home = state.view === "todo" && !state.selected && !state.todoAll;
    const mem = state.view === "memories" && ["active", "candidate", "conflicted", "idle"].includes(state.memTab);
    if (!here && !home && !mem) return;
    if (document.querySelector("#detail [data-sync-dirty]")) { if (here) paintList(); return; }
    render(false);
    if (here) detail();
  }
  async function reload() { data.at = 0; texts.clear(); views.clear(); side = freshSide(side.key); await load(); }
  // A record a digest or a proposal points at; read once, kept by id + revision.
  function record(collection, id) {
    const k = `${collection}/${id}`;
    if (texts.has(k)) return texts.get(k);
    const local = collection === "memories" ? state.data.memories.find((m) => m.id === id) : collection === "memory_candidates" ? state.data.candidates.find((m) => m.id === id) : null;
    if (local) { texts.set(k, local); return local; }
    texts.set(k, undefined);
    const got = (v) => { texts.set(k, v); if (state.view === "memories" && ["digests", "merge", "notes"].includes(state.memTab) && !document.querySelector("#detail [data-sync-dirty]")) { paintList(); detail(true); } };
    request(`/v1/${collection}/${encodeURIComponent(id)}?${query()}`).then((r) => got(r?.id === id ? r : null), () => got(null));
    return undefined;
  }

  // ------------------------------------------------------------ counts (memory screen, HOME)
  // serverCount() input: null while loading, false when the read failed, else the number (0 also when the server has no proposals)
  function mergeCount() { ensure(); return data.key !== identity() ? null : data.merge === false ? false : data.merge ? data.merge.length : 0; }
  function notesCount() { ensure(); return data.key !== identity() ? null : data.notes === false ? false : data.notes ? data.notes.length : 0; }
  function digestCounts() { ensure(); return { topic: data.topicCand.length, user: data.userCand.length }; }
  // 메뉴별 확인할 것: "병합 제안 n" joins the 기억 row, 모아보기 gets its own row right under it (Figma 385:30743).
  function menuRows(rows) {
    ensure();
    const at = rows.findIndex((r) => r[1] === "기억");
    const n = data.merge?.length || 0;
    if (at >= 0 && n) rows[at] = [rows[at][0], rows[at][1], [...rows[at][2], { label: `합치기 제안 ${n}`, n }], rows[at][3]];
    const { topic, user } = { topic: data.topicCand.length, user: data.userCand.length };
    const parts = [{ label: `주제 요약 바뀜 ${topic}`, n: topic }, { label: `나에 대한 요약 바뀜 ${user}`, n: user }].filter((p) => p.n);
    rows.splice(at >= 0 ? at + 1 : rows.length, 0, ["MEMORY", "모아보기", parts, ["memories", "digests"]]);
  }

  // ------------------------------------------------------------ 모아보기 · 주제 요약
  // One entry per active topic (with its waiting candidate, if any) plus candidates that replace nothing or two (new / merge).
  function entries() {
    const out = [];
    if (filter.status !== "active") {
      for (const d of data.other[filter.status] || []) out.push({ id: d.id, digest: d, cand: null });
      return out;
    }
    const byBase = new Map();
    const loose = [];
    for (const c of data.topicCand) {
      const base = c.diff?.base || c.replaces || [];
      if (base.length === 1 && data.topic.some((t) => t.id === base[0].id)) byBase.set(base[0].id, c); else loose.push(c);
    }
    for (const t of data.topic) out.push({ id: t.id, digest: t, cand: byBase.get(t.id) || null });
    for (const c of loose) out.push({ id: c.id, digest: null, cand: c });
    return out;
  }
  const titleOf = (e) => e.digest?.title || e.cand?.diff?.title_after || e.cand?.title || "제목 없음";
  const scoreOf = (e) => e.digest?.heat?.score || 0;
  function classOf(e, max) {
    if (e.cand) return "candidate";
    return scoreOf(e) < Math.max(1, max * 0.2) ? "quiet" : "hot";
  }
  function draw() {
    ensure();
    // 위키 질문 페이지 › 주제 요약에서 열기
    if (state.openTopic && here()) { filter.view = "models"; filter.q = ""; editing = null; picked = true; state.selected = `mm:${state.openTopic}`; state.openTopic = null; }
    if (filter.view !== "topics") { drawDerived(); return; }
    if (!state.selected && !picked) {
      const first = entries().find((e) => e.cand) || entries()[0];
      if (first) { state.selected = `digest:${first.id}`; picked = true; }
    }
    const top = [];
    const n = { topic: data.topicCand.length, user: data.userCand.length };
    if (n.topic + n.user) {
      const card = el("div", "mem-check");
      const txt = el("div", "mem-check-text");
      const line = el("p", "mem-check-title"); line.append("확인해 주세요", el("strong", "", String(n.topic + n.user)));
      txt.append(line, el("p", "mem-check-sub", `주제 요약 바뀜 ${n.topic} · 나에 대한 요약 바뀜 ${n.user}`));
      card.append(txt, button("하나씩 확인하기", "primary small", null, () => {
        const first = entries().find((e) => e.cand);
        pick(first ? `digest:${first.id}` : "digest:user");
      }));
      top.push(card);
    }
    top.push(userCard());
    const tools = el("div", "mem-tools");
    tools.append(searchBox("주제 검색"),
      ctx.memSelect("상태", STATUS, filter.status, (v) => { filter.status = v; filter.chip = "all"; if (v !== "active") void loadOther(v); paintList(); }),
      ctx.memSelect("정렬", SORT, filter.sort, (v) => { filter.sort = v; paintList(); }));
    $("#controls").replaceChildren(viewSeg(), ...top, tools);
    $("#stats").replaceChildren();
    paintList();
  }
  function searchBox(label) {
    const search = el("label", "panel-search");
    const q = el("input"); q.type = "search"; q.placeholder = label; q.setAttribute("aria-label", label); q.value = filter.q;
    q.addEventListener("input", () => { filter.q = q.value.trim().toLowerCase(); paintList(); });
    search.append(ctx.icon("magnifying-glass"), q);
    return search;
  }
  async function loadOther(status) {
    if (data.other[status] && !data.other[status].failed) return; // a failed read asks again
    const page = await list("topic", status);
    data.other[status] = Array.isArray(page?.items) ? page.items : [];
    if (page === FAILED) data.other[status] = Object.assign([], { failed: true });
    if (state.view === "memories" && state.memTab === "digests" && filter.status === status) paintList();
  }
  function paintList() {
    if (state.view !== "memories") return;
    if (state.memTab === "merge") { paintMerge(); return; }
    if (state.memTab === "notes") { paintNotes(); return; }
    if (state.memTab !== "digests") return;
    if (filter.view === "models") { paintModels(); return; }
    if (filter.view === "observations") { paintObservations(); return; }
    const all = entries();
    const max = Math.max(0, ...all.map(scoreOf));
    const counts = { all: all.length, hot: 0, candidate: 0, quiet: 0 };
    all.forEach((e) => { counts[classOf(e, max)]++; });
    const chips = filter.status === "active" ? [["all", "전체"], ["hot", "요즘 자주 씀"], ["candidate", "새 후보"], ["quiet", "한동안 안 씀"]] : [["all", "전체"]];
    $("#chips").replaceChildren(...chips.map(([k, label]) => {
      const c = el("button", `chip mem-kind${filter.chip === k ? " active" : ""}`); c.type = "button";
      c.append(el("span", "", label)); if (counts[k]) c.append(el("output", "", String(counts[k]))); // no 0 on an empty chip
      c.addEventListener("click", () => { filter.chip = k; paintList(); });
      return c;
    }));
    let items = all.filter((e) => (filter.chip === "all" || classOf(e, max) === filter.chip) && (!filter.q || `${titleOf(e)} ${e.digest?.body || ""} ${e.cand?.body || ""}`.toLowerCase().includes(filter.q)));
    const time = (e) => new Date((e.cand || e.digest).updated_at).getTime();
    if (filter.sort === "heat") items = items.sort((a, b) => scoreOf(b) - scoreOf(a));
    if (filter.sort === "recent") items = items.sort((a, b) => time(b) - time(a));
    if (filter.sort === "name") items = items.sort((a, b) => titleOf(a).localeCompare(titleOf(b)));
    const table = $("#table");
    table.replaceChildren(...items.map((e) => {
      const d = e.digest || e.cand;
      const cell = el("div", "cell mem-cell");
      const meta = [`기억 ${d.members?.length || 0}개`, ago(d.updated_at)];
      if (e.digest?.outdated) meta.push("바탕 기억이 수정됨");
      if (e.digest?.stale) meta.push("그 뒤 승인된 기억 있음");
      cell.append(raw(el("div", "text", titleOf(e))), el("div", "mem-meta", meta.join(" · ")));
      const cls = classOf(e, max);
      const badge = e.cand ? el("span", "mem-badge warn", e.digest ? "새 후보" : PROPOSAL[e.cand.proposal] || "새 후보")
        : filter.status !== "active" ? el("span", "mem-badge", STATUS.find(([k]) => k === filter.status)[1].replace("상태: ", ""))
        : el("span", "mem-badge", cls === "quiet" ? "한동안 안 씀" : "요즘 자주 씀");
      return row(`digest:${e.id}`, "minmax(0,1fr) auto", [cell, badge], () => detail());
    }));
    if (!data.key) table.append(el("div", "empty", "요약을 불러오는 중…"));
    else if ((filter.status === "active" ? data.failed : data.other[filter.status]?.failed) && !items.length) table.append(el("div", "empty", "요약을 불러오지 못했어요. 새로고침하면 다시 불러와요."));
    else if (!items.length) table.append(el("div", "empty", filter.q ? "검색과 맞는 주제가 없어요." : filter.status === "active" ? "아직 주제 요약이 없어요. 승인된 기억이 쌓이면 밤사이 제안돼요." : "여기에 표시할 요약이 없어요."));
    const foot = el("div", "mem-more");
    const capNote = data.cap && data.activeN >= data.cap ? " · 최대 개수에 닿아서 합치기 제안이 나올 수 있어요" : "";
    foot.append(el("span", "", `${all.length}개 중 ${items.length}개 표시 · 자주 씀 = 최근 30일에 자주 참고된 주제${data.cap ? ` · 최대 ${data.cap}개` : ""}`), capNote);
    if (data.key) table.append(foot);
    markSelected();
  }
  function userCard() {
    const u = data.user, c = data.userCand[0];
    const card = el("button", `dg-user${state.selected === "digest:user" ? " selected" : ""}`); card.type = "button"; card.dataset.id = "digest:user";
    const head = el("div", "dg-user-head");
    head.append(el("strong", "", "나에 대한 요약"));
    if (c) head.append(el("span", "mem-badge warn", "새 후보"));
    card.append(head);
    if (u?.fresh && u.body) {
      card.append(raw(el("p", "dg-user-body", u.body)),
        el("p", "dg-user-meta", `기억 ${u.members?.length || 0}개에서 뽑았어요 · 새 세션을 시작할 때 참고 자료로 넣어요 · ${day(u.updated_at)} 고침`));
    } else if (u) {
      card.append(el("p", "dg-user-meta", "바탕 기억이 바뀌거나 지워져 쓸 수 없어요. 다음 실행 때 다시 제안돼요."));
    } else {
      card.append(el("p", "dg-user-meta", c ? "아직 쓰는 요약은 없어요. 후보를 확인해 주세요." : "아직 요약이 없어요. 나에 대한 기억이 승인되면 제안돼요."));
    }
    card.addEventListener("click", () => pick("digest:user"));
    return card;
  }
  function pick(id) {
    state.selected = id; markSelected();
    document.querySelectorAll(".dg-user").forEach((n) => n.classList.toggle("selected", id === "digest:user"));
    detail(); openDetail();
  }

  // ------------------------------------------------------------ detail: a topic or the user summary
  function detail(soft) {
    if (soft && document.querySelector("#detail [data-sync-dirty]")) return;
    if (state.memTab === "merge") return mergeDetail(soft);
    if (state.memTab === "notes") return notesDetail(soft);
    if (filter.view === "models") return modelDetail(soft);
    if (filter.view === "observations") return observationDetail(soft);
    const id = (state.selected || "").replace(/^digest:/, "");
    if (id === "user") return userDetail();
    const e = entries().find((x) => x.id === id);
    const body = detailShell("모아보기");
    if (!e) { body.append(el("h2", "", "주제를 고르세요"), el("p", "lead", "비슷한 기억을 묶은 요약이에요. 사실이나 지시가 아니라 참고 자료로 쓰여요.")); return; }
    const d = e.digest, c = e.cand;
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", d ? "주제" : `주제 후보 · ${PROPOSAL[c.proposal] || "새 후보"}`), raw(el("h2", "", titleOf(e))));
    if (d) head.append(el("p", "meta", `기억 ${d.members?.length || 0}개 · ${day(d.review?.at || d.updated_at)} 요약`));
    if (d?.outdated) head.append(el("span", "mem-badge warn dg-flag", "바탕 기억이 수정됨"));
    if (d?.stale) head.append(el("span", "mem-badge warn dg-flag", "그 뒤 승인된 기억 있음"));
    if (d?.stale_mark) head.append(el("span", "mem-badge warn dg-flag", "바탕 기억이 대체됨"));
    body.append(head);
    if (c) body.append(candidateCard(c, "topic", topicLead(c)));
    if (d) {
      const now = ctx.section("지금 요약", `v${d.revision}`);
      now.append(quote(d.fresh ? d.body : "", d.fresh ? `${day(d.updated_at)} · 기억 ${d.members?.length || 0}개에서` : ""));
      body.append(now);
    }
    body.append(members(c ? orderMembers(c) : d.members || [], c?.diff?.members_added || [], "묶인 기억"));
    if (d) {
      const use = ctx.section("언제 쓰이나요");
      use.append(ctx.facts([["들어가는 때", "질문이 이 주제와 맞을 때"], ["쓰는 곳", "모든 에이전트 · 참고 자료로"], ["최근 30일", `${d.heat?.injected || 0}번 참고 · ${d.heat?.used || 0}번 사용`]]));
      body.append(use);
      if (d.status === "active") body.append(regenerate("topic", d.id));
    }
  }
  function topicLead(c) {
    const added = c.diff?.members_added?.length || 0;
    if (c.proposal === "new") return "승인된 기억으로 AI가 새 주제를 묶었어요. 이 요약을 쓸까요?";
    if (c.proposal === "merge") return "비슷한 두 요약을 하나로 합치자는 후보예요. 합치면 두 요약은 이전 버전으로 남아요.";
    return added ? `새 기억 ${added}개가 들어와서 AI가 요약을 고쳤어요. 이 후보로 바꿀까요?` : "AI가 요약을 고쳤어요. 이 후보로 바꿀까요?";
  }
  function userDetail() {
    const u = data.user, c = data.userCand[0];
    const body = detailShell("모아보기");
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "모아보기 · 나에 대한 요약"), el("h2", "", "나에 대한 요약"));
    head.append(el("p", "meta", u ? `기억 ${u.members?.length || 0}개 · ${day(u.review?.at || u.updated_at)}` : "승인하면 새 세션 시작 때 에이전트에게 참고 자료로 전달돼요(지시가 아님)."));
    body.append(head);
    if (c) body.append(candidateCard(c, "user", JUDGE_LEAD[c.judge] || "AI가 요약 후보를 만들었어요. 바꿀까요?"));
    if (u && !c) {
      const now = ctx.section("지금 요약", `v${u.revision}`);
      now.append(quote(u.fresh ? u.body : "", u.fresh ? `${day(u.updated_at)} · 기억 ${u.members?.length || 0}개에서` : ""));
      body.append(now);
    }
    const basis = c || u;
    if (basis) body.append(members(c ? orderMembers(c) : u.members || [], c?.diff?.members_added || [], "근거가 된 기억"));
    const where = ctx.section("어디에 들어가나요");
    const len = (u?.body || c?.body || "").length;
    where.append(ctx.facts([["들어가는 때", "새 세션을 시작할 때 항상"], ["쓰는 곳", "모든 에이전트 · 참고 자료로"], ["길이", len ? `${len}자` : ""]]));
    where.append(el("p", "meta", "승인하면 새 세션 시작 때 에이전트에게 참고 자료로 전달돼요(지시가 아님)."));
    body.append(where);
    const history = [u, ...data.userOld].filter(Boolean).sort((a, b) => b.revision - a.revision);
    if (history.length) {
      const h = ctx.section("변경 이력");
      const dl = el("dl", "facts");
      for (const v of history.slice(0, 6)) dl.append(el("dt", "", day(v.review?.at || v.updated_at)), el("dd", "", `v${v.revision} · ${v.status === "active" ? "지금 쓰는 중" : "이전 버전"}${v.review?.action === "approve" ? " · 승인" : ""}`));
      h.append(dl);
      body.append(h);
    }
    body.append(regenerate("user"));
  }
  // The candidate card: what changes (line diff), then 승인 / 버리기 (사유는 선택).
  // decide(action, reason, btns) defaults to the digest decision; null shows who may decide instead of the buttons.
  function candidateCard(c, kind, lead, decide = (action, reason, btns) => decideDigest(c, kind, action, reason, btns)) {
    const card = el("div", "d-action dg-cand");
    const badges = el("div", "dg-badges");
    if (kind === "user" && JUDGE[c.judge]) badges.append(el("span", "mem-badge warn", JUDGE[c.judge]));
    if (kind === "topic" && c.proposal && c.proposal !== "update") badges.append(el("span", "mem-badge warn", PROPOSAL[c.proposal] || c.proposal));
    if (instructionLike(c)) badges.append(el("span", "mem-badge warn", "지시처럼 보이는 문장 포함"));
    if (badges.childNodes.length) card.append(badges);
    card.append(el("p", "", lead));
    if (c.proposal === "merge" && c.diff?.title_before) card.append(raw(el("p", "dg-merge-title", `${c.diff.title_before} → ${c.diff.title_after || c.title || ""}`)));
    else if (c.diff?.title_before && c.diff.title_after && c.diff.title_before !== c.diff.title_after) card.append(raw(el("p", "dg-merge-title", `${c.diff.title_before} → ${c.diff.title_after}`)));
    if (!c.fresh) card.append(el("p", "dg-warn", "바탕 기억이 바뀌거나 지워져 쓸 수 없어요. 다음 실행 때 다시 제안돼요."));
    else card.append(diffBox(c.diff?.lines?.length ? c.diff.lines : (c.body || "").split("\n").filter(Boolean).map((text) => ({ op: "+", text }))));
    const removed = c.diff?.members_removed?.length || 0;
    if (removed) card.append(el("p", "meta", `빠진 기억 ${removed}개`));
    const reason = el("textarea", "dg-reason"); reason.rows = 2; reason.maxLength = kind === "note" ? 300 : 500; reason.placeholder = "버리는 이유 (선택)"; reason.hidden = true;
    reason.addEventListener("input", () => reason.toggleAttribute("data-sync-dirty", !!reason.value));
    if (!decide) { card.append(el("p", "meta", "승인과 버리기는 사람만 할 수 있어요.")); return card; }
    const btns = el("div", "d-buttons");
    const ok = button("승인", "primary small", null, () => decide("approve", "", btns));
    ok.disabled = !c.fresh;
    const no = button("버리기", "ghost small", null, () => {
      if (reason.hidden && kind !== "causal" && kind !== "proposal") { reason.hidden = false; reason.focus(); return; }
      void decide("reject", reason.value.trim(), btns);
    });
    btns.append(ok, no);
    card.append(reason, btns);
    return card;
  }
  function diffBox(lines) {
    const box = el("div", "dg-diff");
    for (const l of lines) {
      const r = el("div", `dg-line ${{ "-": "del", "+": "add" }[l.op] || "same"}`);
      r.append(el("span", "dg-op", { "-": "-", "+": "+" }[l.op] || "·"), raw(el("span", "", l.text)));
      box.append(r);
    }
    return box;
  }
  function quote(text, foot) {
    const q = el("div", "quote dg-quote");
    if (text) q.append(raw(el("p", "", text)));
    else q.append(el("p", "dg-warn", "바탕 기억이 바뀌거나 지워져 쓸 수 없어요. 다음 실행 때 다시 제안돼요."));
    if (foot) q.append(el("footer", "", foot));
    return q;
  }
  function orderMembers(c) {
    const added = new Set((c.diff?.members_added || []).map((r) => r.id));
    const all = c.members || [];
    return [...all.filter((r) => added.has(r.id)), ...(c.diff?.members_added || []).filter((r) => !all.some((m) => m.id === r.id)), ...all.filter((r) => !added.has(r.id))];
  }
  // 묶인 기억 / 근거가 된 기억: three rows first, "n개 모두 보기" opens the rest (each memory read once).
  const expanded = new Set();
  function members(refs, added, title) {
    const key = `${state.selected}|${title}`;
    const s = ctx.section(title);
    const h = s.querySelector("h3");
    h.classList.add("dg-head");
    if (refs.length > 3 && !expanded.has(key)) {
      const all = el("button", "link-btn dg-more", `${refs.length}개 모두 보기`); all.type = "button";
      all.addEventListener("click", () => { expanded.add(key); detail(true); });
      h.append(all);
    } else h.append(el("small", "", `${refs.length}개`));
    if (!refs.length) { s.append(el("p", "meta", "묶인 기억이 없어요.")); return s; }
    const box = el("div", "dg-list");
    const isNew = new Set(added.map((r) => r.id));
    for (const ref of refs.slice(0, expanded.has(key) ? 64 : 3)) {
      const m = record("memories", ref.id);
      const item = window.ArkismDigests.memoryItem(ctx, window.ArkismDigests.memoryText(m), m ? [kindLabel(m.kind), day(m.created_at), isNew.has(ref.id) ? "새로 묶임" : ""].filter(Boolean).join(" · ") : "");
      if (isNew.has(ref.id)) item.append(el("span", "mem-badge new", "새"));
      if (m && m.status === "active" && ctx.openRecord) item.addEventListener("click", () => ctx.openRecord(m)); else item.disabled = !m;
      box.append(item);
    }
    s.append(box);
    return s;
  }
  function regenerate(kind, id) {
    return button("다시 만들기", "small", null, async (e) => {
      const b = e.currentTarget; b.disabled = true;
      try {
        await request("/v1/digests/regenerate", { method: "POST", body: JSON.stringify({ scope: state.scope, kind, ...(id ? { id } : {}) }) });
        notice("다음 야간 실행 때 새 후보로 나와요.");
      } catch (err) { notice(err.message === "access_denied" ? "이 계정은 요약을 다시 만들 수 없어요." : `요청하지 못했어요 (${err.message}).`); b.disabled = false; }
    });
  }
  const busy = new Set();
  // One decision at a time per item; the list reloads afterwards whatever happened. say: { ok, conflict, denied }
  async function decideOnce(id, send, say, btns) {
    if (busy.has(id)) return;
    busy.add(id); btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    try { await send(); notice(say.ok); }
    catch (e) { notice(say.codes?.[e.message] || (e.message === "revision_conflict" ? say.conflict : e.message === "access_denied" ? say.denied : `처리하지 못했어요: ${e.message}`)); }
    finally { busy.delete(id); }
    await reload();
    render(false); detail();
  }
  function decideDigest(c, kind, decision, reason, btns) {
    return decideOnce(c.id, () => request(`/v1/digests/${encodeURIComponent(c.id)}/decide`, { method: "POST",
      body: JSON.stringify({ scope: state.scope, kind, expected_revision: c.revision, decision, ...(reason ? { reason } : {}) }) }),
    { ok: decision === "approve" ? (kind === "user" ? "나에 대한 요약을 바꿨어요. 새 세션부터 쓰여요." : "요약을 바꿨어요. 이전 버전은 이력에 남아요.") : "후보를 버렸어요.",
      conflict: "바탕이 바뀌었거나 주제 수가 꽉 차서 처리하지 못했어요. 새로 불러왔어요. (꽉 찼다면 합치기 후보를 먼저 승인하세요)", denied: "이 계정은 요약을 승인할 수 없어요." }, btns);
  }

  // ------------------------------------------------------------ 확인해 주세요 · 병합 제안
  const proposal = (r) => r.merge?.proposal || {};
  function drawMerge() {
    ensure();
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    paintMerge();
  }
  function paintMerge() {
    if (state.view !== "memories" || state.memTab !== "merge") return;
    const items = (data.merge || []).filter((r) => {
      const c = record("memory_candidates", proposal(r).candidate?.id);
      return !state.query || `${c?.text || ""} ${proposal(r).reason || ""}`.toLowerCase().includes(state.query);
    });
    const table = $("#table");
    table.replaceChildren(...items.map((r) => {
      const p = proposal(r), c = record("memory_candidates", p.candidate?.id);
      const t = (p.targets || [])[0] && record("memories", p.targets[0].id);
      const hint = p.action === "store" ? REASON[p.reason_code] || "비슷한 기억 없음" : t ? `대상 “${t.text}”` : `대상 기억 ${(p.targets || []).length}개`;
      const cell = el("div", "cell mem-cell");
      cell.append(raw(el("div", "text", c ? c.text : c === null ? "읽을 수 없는 후보예요" : "불러오는 중…")), raw(el("div", "mem-meta", `${ctx.tr("AI 제안")}: ${ctx.tr(ACTION[p.action] || p.action)} · ${ctx.tr(hint)}`)));
      return row(`merge:${r.id}`, "minmax(0,1fr) auto", [cell, el("span", `mem-badge${p.action === "merge" ? " warn" : p.action === "store" ? " strong" : ""}`, ACTION[p.action] || p.action)], () => mergeDetail());
    }));
    if (!data.key) table.append(el("div", "empty", "제안을 불러오는 중…"));
    else if (data.merge === false) table.append(el("div", "empty", "합치기 제안을 불러오지 못했어요. 새로고침하면 다시 불러와요."));
    else if (data.merge === null) table.append(el("div", "empty", "이 서버는 합치기 제안을 지원하지 않아요."));
    else if (!items.length) table.append(el("div", "empty", state.query ? "검색과 맞는 제안이 없어요." : "확인할 합치기 제안이 없어요."));
    // the note is a meta line under the list card, not a row in it (Figma 383:29822); CSS hides it once the merge rows are gone
    document.querySelector(".dg-merge-note")?.remove();
    if (items.length) table.parentElement.after(el("p", "dg-merge-note", "AI 제안은 바로 적용하지 않아요. 고르면 그때 저장돼요."));
    markSelected();
  }
  function mergeDetail(soft) {
    const id = (state.selected || "").replace(/^merge:/, "");
    const r = (data.merge || []).find((x) => x.id === id);
    if (soft && !r) return;
    const body = detailShell("합치기 제안");
    if (!r) { body.append(el("h2", "", "제안을 고르세요"), el("p", "lead", "AI가 새 후보를 이미 있는 기억과 견줘 본 결과예요. 고르기 전엔 아무것도 바뀌지 않아요.")); return; }
    const p = proposal(r);
    const v = views.get(r.id);
    if (v === undefined) {
      views.set(r.id, null);
      const got = (x) => { views.set(r.id, x); if (state.selected === `merge:${r.id}` && !document.querySelector("#detail [data-sync-dirty]")) mergeDetail(); };
      request(`/v1/merge_proposals/${encodeURIComponent(r.id)}?${query()}`).then((x) => got(x?.proposal?.id === r.id ? x : false), () => got(false));
    }
    const cand = v?.candidate || record("memory_candidates", p.candidate?.id);
    const targets = v?.targets?.length ? v.targets : (p.targets || []).map((t) => record("memories", t.id)).filter(Boolean);
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "합치기 제안 · AI 제안"),
      el("h2", "", { merge: "비슷한 기억이 이미 있어요", store: "새 기억으로 저장 제안", skip: "이미 있는 기억과 같아요" }[p.action] || "AI 제안"),
      el("p", "meta", [ago(r.created_at), cand ? originOf(cand) : ""].filter(Boolean).join(" · ")));
    body.append(head);

    const card = el("div", "d-action dg-cand");
    const line = el("div", "dg-badges");
    line.append(el("span", "dg-label", "AI 제안"), el("span", "mem-badge warn", ACTION[p.action] || p.action));
    const stale = v && v.current === false;
    if (stale) line.append(el("span", "mem-badge warn", "바뀜"));
    card.append(line);
    card.append(el("p", "", { merge: `기존 기억 ${(p.targets || []).length}개를 이 내용으로 대체 제안`, store: "새 기억으로 저장 제안", skip: "이미 있는 기억과 같음 → 건너뛰기 제안" }[p.action] || ""));
    if (p.reason) card.append(raw(el("p", "dg-note", p.reason))); // the model's short note: plain text, data only
    if (stale) card.append(el("p", "dg-warn", "후보나 대상 기억이 그사이 바뀌어서 이 제안은 쓸 수 없어요. 무시하거나 새로 저장 목록에서 직접 골라 주세요."));
    if (v === false) card.append(el("p", "dg-warn", "지금 내용을 읽지 못했어요. 다시 열어 주세요."));
    const edit = el("textarea", "dg-reason"); edit.rows = 3; edit.maxLength = 4000; edit.hidden = true;
    edit.addEventListener("input", () => edit.setAttribute("data-sync-dirty", ""));
    const btns = el("div", "d-buttons");
    const ready = v && v.current === true;
    const ok = button(ACTION[p.action] || "수락", "primary small", null, () => decideMerge(r, "accept", edit.hidden ? "" : edit.value.trim(), btns));
    ok.disabled = !ready;
    btns.append(ok);
    if (p.action === "store" || p.action === "merge") {
      const fix = button("고쳐서 수락", "small", null, () => {
        if (edit.hidden) { edit.hidden = false; edit.value = cand?.text || ""; edit.focus(); ok.textContent = ctx.tr("고친 내용으로 수락"); fix.hidden = true; }
      });
      fix.disabled = !ready;
      btns.append(fix);
    }
    btns.append(button("무시", "ghost small", null, () => decideMerge(r, "dismiss", "", btns)));
    card.append(edit, btns);
    body.append(card);

    const incoming = ctx.section("새로 들어온 내용");
    incoming.append(mergeQuote(cand?.text, cand ? [day(cand.created_at), originOf(cand)].join(" · ") : ""));
    body.append(incoming);
    if (targets.length) {
      const s = ctx.section(p.action === "skip" ? "이미 있는 기억" : "대상 기억");
      for (const t of targets) {
        const q = mergeQuote(t.text, [kindLabel(t.kind), `${day(t.created_at)} 저장`].join(" · "));
        if (t.status === "active" && ctx.openRecord) {
          const open = el("button", "link-btn dg-more", "열기"); open.type = "button";
          open.addEventListener("click", () => ctx.openRecord(t));
          s.querySelector("h3").classList.add("dg-head");
          if (!s.querySelector(".dg-more")) s.querySelector("h3").append(open);
        }
        s.append(q);
      }
      body.append(s);
    }
    if (p.action === "merge" && cand) {
      const after = ctx.section("합친 뒤");
      after.append(wordDiff(targets.map((t) => t.text).join("\n"), edit.hidden ? cand.text : edit.value || cand.text),
        el("p", "meta", "기존 기억에서 빠지는 말은 빨간 취소선, 새로 들어오는 말은 초록으로 보여요."));
      body.append(after);
    }
    const how = ctx.section("이렇게 하면");
    how.append(ctx.facts([[ACTION[p.action] || "수락", { merge: "대상 기억은 이력으로 남고, 새 내용이 그 자리를 대신해요", store: "새 내용을 기억으로 저장해요", skip: "새 후보를 거절해요 · 지우지 않고 기록은 남아요" }[p.action]],
      ["무시", "제안만 닫혀요 · 후보는 새로 저장 목록에 그대로 남아요"]]));
    body.append(how);
    if (v?.notice) body.append(raw(el("p", "meta", v.notice)));
  }
  // existing memory → candidate, word by word (text-diff.js); the texts are data, drawn as plain text
  function wordDiff(before, after) {
    const box = raw(el("p", "dg-diff dg-words"));
    // always word by word (Figma 383:29822): one word per "line", so the line diff never swaps a whole sentence; the marker comes off again below
    const cut = (x) => (String(x || "").match(/\S+\s*|\s+/g) || []).join("\u0001\n"); // a word keeps its trailing space, so spaces alone never match
    for (const [op, t0] of window.ArkismTextDiff(cut(before), cut(after))) { const t = t0.replaceAll("\u0001\n", ""); if (t) box.append(op === "=" ? document.createTextNode(t) : el(op === "-" ? "del" : "ins", "", t)); }
    return box;
  }
  function mergeQuote(text, foot) {
    const q = el("div", "quote dg-quote");
    q.append(raw(el("p", "", text ? `“${text}”` : "불러오는 중…")));
    if (foot) q.append(el("footer", "", foot));
    return q;
  }
  async function decideMerge(r, decision, content, btns) {
    if (busy.has(r.id)) return;
    busy.add(r.id); btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    try {
      await request(`/v1/merge_proposals/${encodeURIComponent(r.id)}/decide`, { method: "POST",
        body: JSON.stringify({ scope: state.scope, expected_revision: r.revision, decision, ...(content ? { content } : {}) }) });
      const a = proposal(r).action;
      notice(decision === "dismiss" ? "제안을 닫았어요. 후보는 새로 저장 목록에 남아요." : { merge: "합쳤어요. 옛 기억은 이력에 남아요.", store: "기억으로 저장했어요.", skip: "후보를 건너뛰었어요." }[a] || "처리했어요.");
      state.selected = null;
    } catch (e) {
      notice(e.message === "revision_conflict" ? "후보나 대상 기억이 바뀌어 이 제안은 낡았어요. 새로 불러왔어요." : e.message === "access_denied" ? "이 계정은 제안을 처리할 수 없어요." : `처리하지 못했어요: ${e.message}`);
    } finally { busy.delete(r.id); }
    await Promise.all([reload(), ctx.reloadMemories?.().catch(() => {})]);
    render();
  }

  // ------------------------------------------------------------ 확인해 주세요 · AI 정리 (observations and mental-model drafts)
  // Both are model-written drafts with their cited memories; nothing is used until a person approves it here.
  const DURABLE = { lasting: "오래 갈 내용", momentary: "잠깐 맞는 내용" };
  // A mental model shows up here with a draft to review, or stale with none yet (then the approved text and a 다시 만들기 button).
  const STALE = { sources_changed: "근거 바뀜", new_memories: "새 기억 있음" };
  const STALE_LEAD = { sources_changed: "이 요약이 근거로 든 기억이 지워졌거나 더는 승인 상태가 아니에요.", new_memories: "이 요약을 만든 뒤에 새로 승인된 기억이 있어요." };
  const modelRev = (m) => m.draft || m.active;
  const proposedModel = (n) => n.model?.status === "proposed";
  const noteText = (n) => (n.obs ? n.obs.content : n.link ? n.link.reason : modelRev(n.model)?.text) || "";
  const noteRefs = (n) => (n.obs ? n.obs.source_memories : n.link ? [n.link.cause, n.link.effect] : modelRev(n.model)?.citations) || [];
  const occurred = (o) => { const w = o?.occurred && window.ArkismMemoryTime?.span(o.occurred); return w ? `일어난 때 ${w}` : ""; };
  const memText = (id) => { const m = record("memories", id); return m ? m.text : m === null ? "읽을 수 없는 기억" : "불러오는 중…"; };
  const noteTitle = (n) => (n.obs ? n.obs.content : n.link ? `${memText(n.link.cause.id)} → ${memText(n.link.effect.id)}` : n.model.title);
  function drawNotes() {
    ensure();
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    paintNotes();
  }
  function paintNotes() {
    if (state.view !== "memories" || state.memTab !== "notes") return;
    const items = (data.notes || []).filter((n) => !state.query || `${n.model?.title || ""} ${noteText(n)}`.toLowerCase().includes(state.query));
    const KIND = (n) => (n.obs ? "기억에서 뽑은 정리" : n.link ? "원인과 결과 제안" : proposedModel(n) ? "에이전트가 제안한 주제" : n.model.draft ? "주제 요약 초안" : "다시 만들 주제 요약");
    const table = $("#table");
    table.replaceChildren(...items.map((n) => {
      const cell = el("div", "cell mem-cell");
      cell.append(raw(el("div", "text", noteTitle(n))),
        el("div", "mem-meta", [KIND(n), !proposedModel(n) && `근거 기억 ${noteRefs(n).length}개`, occurred(n.obs), ago((n.obs || n.link || n.model).updated_at)].filter(Boolean).join(" · ")));
      const stale = n.obs ? !n.obs.fresh : n.link ? false : !!n.model.draft?.withheld;
      const badge = stale ? "근거 바뀜" : proposedModel(n) ? "AI 제안" : n.model && !n.model.draft ? STALE[n.model.stale_reason] || "오래됨" : n.obs?.diff?.length ? "고침" : "새 정리";
      return row(`note:${n.id}`, "minmax(0,1fr) auto", [cell, el("span", "mem-badge warn", badge)], () => notesDetail());
    }));
    if (!data.key) table.append(el("div", "empty", "AI 정리를 불러오는 중…"));
    else if (data.notes === false) table.append(el("div", "empty", "AI 정리를 불러오지 못했어요. 새로고침하면 다시 불러와요."));
    else if (data.notes === null) table.append(el("div", "empty", "이 서버는 AI 정리를 지원하지 않아요."));
    else if (!items.length) table.append(el("div", "empty", state.query ? "검색과 맞는 정리가 없어요." : "확인할 AI 정리가 없어요. 승인된 기억이 쌓이면 AI가 정리해서 여기에 올려요."));
    markSelected();
  }
  function notesDetail(soft) {
    const id = (state.selected || "").replace(/^note:/, "");
    const n = (data.notes || []).find((x) => x.id === id);
    if (soft && !n) return;
    const body = detailShell("AI 정리");
    if (!n) { body.append(el("h2", "", "정리를 고르세요"), el("p", "lead", "AI가 승인된 기억 여러 개를 읽고 쓴 정리예요. 승인하기 전에는 쓰이지 않아요.")); return; }
    if (n.link) { causalDetail(body, n); return; }
    if (proposedModel(n)) { proposedTopic(body, n); return; }
    const o = n.obs, m = n.model, d = m?.draft;
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", o ? "AI 정리 · 기억에서 뽑음" : d ? "AI 정리 · 주제 요약 초안" : "주제 요약 · 다시 만들 때가 됐어요"), raw(el("h2", "", o ? o.content : m.title)));
    if (m) head.append(raw(el("p", "meta", m.query)));
    if (occurred(o)) head.append(el("p", "meta", occurred(o)));
    if (m?.stale) head.append(el("p", "dg-warn", STALE_LEAD[m.stale_reason] || "이 요약이 오래됐어요."));
    body.append(head);
    if (m && !d) { staleModel(body, n, m); return; }
    const human = state.caps?.role === "human";
    const lines = o ? o.diff : m.draft_diff;
    const c = { id: n.id, fresh: o ? o.fresh : !d.withheld, body: noteText(n), tags: o?.tags, diff: lines?.length ? { lines } : null };
    const send = (action, reason, btns) => decideOnce(n.id, () => request(o ? `/v1/observations/${encodeURIComponent(o.id)}/review` : `/v1/mental-models/${encodeURIComponent(m.id)}/review`,
      { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: (o || m).revision, action, ...(reason ? { reason } : {}) }) }),
    { ok: action === "approve" ? "승인했어요. 이제 기억과 함께 쓰여요." : "정리를 버렸어요.", conflict: "그사이 정리나 근거 기억이 바뀌었어요. 새로 불러왔어요.", denied: "이 계정은 AI 정리를 승인할 수 없어요." }, btns);
    const card = candidateCard(c, "note", o ? "AI가 승인된 기억 여러 개에서 이 정리를 뽑았어요. 쓸까요?" : "AI가 주제 요약 초안을 썼어요. 이 초안으로 바꿀까요?", human ? send : null);
    if (d?.withheld) card.querySelector(".dg-warn").textContent = ctx.tr("근거 기억이 지워져서 내용을 가렸어요. 버리고 새 초안을 기다려 주세요.");
    body.append(card);
    if (m?.active) {
      const now = ctx.section("지금 요약", `v${m.active.version}`);
      now.append(quote(m.active.withheld ? "" : m.active.text, day(m.active.at)));
      body.append(now);
    }
    const refs = noteRefs(n), mems = refs.filter((r) => r.collection === "memories");
    body.append(members(mems, [], "근거 기억"));
    if (refs.length > mems.length) body.append(el("p", "meta", `승인된 다른 AI 정리 ${refs.length - mems.length}개도 근거로 썼어요.`));
    const info = [["얼마나 갈 내용", o && (DURABLE[o.durability] || o.durability)], ["AI 확신", o?.confidence != null ? `${Math.round(o.confidence * 100)}%` : ""],
      ["다시 만드는 때", m && modeText(m)], ["쓴 모델", ((o?.generator || d?.generator)?.model) || ""]];
    const more = el("details", "insight-more"); more.append(el("summary", "", "자세히"), ctx.facts(info));
    body.append(more);
  }

  // A stale mental model without a draft: the approved text stays in use; 다시 만들기 asks the model for a new draft,
  // which comes back here for a person to approve (POST /v1/mental-models/{id}/refresh).
  function staleModel(body, n, m) {
    if (m.active) {
      const now = ctx.section("지금 요약", `v${m.active.version}`);
      now.append(quote(m.active.withheld ? "" : m.active.text, day(m.active.at)));
      body.append(now);
    }
    const card = el("div", "d-action dg-cand");
    card.append(el("p", "", "AI에게 새 초안을 쓰게 할까요? 승인하기 전까지는 지금 요약을 그대로 써요."));
    if (state.caps?.role !== "human") card.append(el("p", "meta", "다시 만들기는 사람만 할 수 있어요."));
    else {
      const btns = el("div", "d-buttons");
      btns.append(refreshButton(n.id, m, btns, "primary small"));
      card.append(btns);
    }
    body.append(card);
    const refs = noteRefs(n), mems = refs.filter((r) => r.collection === "memories");
    body.append(members(mems, [], "근거 기억"));
  }

  // An agent may only propose a topic (status proposed): it is never refreshed or read until a person approves it.
  // 승인 saves it as it is (PATCH, which makes it active); 버리기 deletes it.
  function proposedTopic(body, n) {
    const m = n.model;
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "AI 정리 · 에이전트가 제안한 주제"), raw(el("h2", "", m.title)));
    body.append(head);
    const send = (action, _reason, btns) => decideOnce(n.id, () => request(`/v1/mental-models/${encodeURIComponent(m.id)}`, action === "approve"
      ? { method: "PATCH", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision, title: m.title, query: m.query, refresh: m.refresh }) }
      : { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) }),
    { ok: action === "approve" ? "주제를 승인했어요. 다시 만드는 때가 되면 요약을 써요." : "제안을 버렸어요.", conflict: "그사이 제안이 바뀌었어요. 새로 불러왔어요.", denied: "이 계정은 주제를 승인할 수 없어요." }, btns);
    body.append(candidateCard({ id: n.id, fresh: true, body: m.query }, "proposal",
      "에이전트가 이 주제로 요약을 만들자고 제안했어요. 승인하기 전에는 요약을 쓰지 않아요.", state.caps?.role === "human" ? send : null));
    const info = [["다시 만드는 때", modeText(m)], ["만든 날", day(m.created_at)]];
    const more = el("details", "insight-more"); more.append(el("summary", "", "자세히"), ctx.facts(info));
    body.append(more);
  }

  // 원인과 결과 제안: AI가 "이 기억이 저 기억으로 이어졌다"고 본 것. 승인하면 기억을 찾을 때 서로 함께 불려 나와요.
  function causalDetail(body, n) {
    const l = n.link;
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "AI 정리 · 원인과 결과"), raw(el("h2", "", noteTitle(n))));
    body.append(head);
    const send = (action, _reason, btns) => decideOnce(n.id, () => request(`/v1/causal-links/${encodeURIComponent(l.id)}/review`,
      { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: l.revision, action }) }),
    { ok: action === "approve" ? "이어진 기억으로 저장했어요." : "제안을 버렸어요.", conflict: "그사이 기억이 바뀌었거나 지워졌어요. 새로 불러왔어요.", denied: "이 계정은 원인과 결과를 승인할 수 없어요." }, btns);
    body.append(candidateCard({ id: n.id, fresh: true, body: l.reason || "AI가 이유를 적지 않았어요." }, "causal",
      "AI가 위 기억 때문에 아래 기억이 생겼다고 봤어요. 맞나요?", state.caps?.role === "human" ? send : null));
    body.append(members([l.cause], [], "원인 기억"), members([l.effect], [], "결과 기억"));
    const info = [["AI 확신", l.confidence != null ? `${Math.round(l.confidence * 100)}%` : ""], ["쓴 모델", l.generator?.model || ""]];
    const more = el("details", "insight-more"); more.append(el("summary", "", "자세히"), ctx.facts(info));
    body.append(more);
  }

  // ------------------------------------------------------------ 모아보기 · 직접 정한 주제 (mental models) · 기억 정리 (observations)
  // A person writes the question and when it is remade; whatever the model writes is a draft a person approves in 확인해 주세요 › AI 정리.
  const VIEWS = [["topics", "주제 요약"], ["models", "직접 정한 주제"], ["observations", "기억 정리"]];
  const MODE = { manual: "직접 요청할 때", on_change: "기억이 바뀔 때", daily: "매일" };
  const modeText = (m) => { const r = window.ArkismDigests.refreshOf(m); return MODE[r.mode] || r.mode; };
  const OUTCOME = { content_written: "새 초안을 썼어요", content_unchanged: "바뀐 내용이 없었어요", content_preserved_no_new_facts: "새 기억이 없어 그대로 뒀어요",
    refresh_failed_empty_candidate: "실패 · AI 답이 비었어요", refresh_failed_delta_not_applied: "실패 · 고친 부분을 붙이지 못했어요",
    refresh_failed_structured_output: "실패 · 정한 형식과 달랐어요", refresh_failed_error: "실패 · 오류가 났어요" };
  const TRIGGER = { initial: "처음", manual: "직접 요청", daily: "매일", on_change: "기억이 바뀜", after_consolidation: "기억 정리 뒤", cron: "정한 일정", full_refresh_cron: "전체 다시 쓰기 일정" };
  const REVISION = { propose: "AI 제안", refresh: "AI 초안", approve: "승인", edit_approve: "고쳐서 승인", reject: "버림", stale: "근거 바뀜", superseded: "이전 버전", cleared: "비움", delete: "지움" };
  const OBS_STATUS = [["active", "상태: 사용 중"], ["proposed", "상태: 확인 대기"], ["stale", "상태: 근거 바뀜"], ["rejected", "상태: 거절됨"]];
  const BUDGET = [["", "참고할 기억: 기본"], ["low", "참고할 기억: 적게 (10개)"], ["mid", "참고할 기억: 보통 (20개)"], ["high", "참고할 기억: 많이 (40개)"]];
  const TAG_MATCH = [["", "태그 맞추는 방법: 하나라도 있는 기억"], ["all_strict", "태그 맞추는 방법: 모두 있는 기억"], ["any", "태그 맞추는 방법: 하나라도 있거나 태그 없는 기억"],
    ["all", "태그 맞추는 방법: 모두 있거나 태그 없는 기억"], ["exact", "태그 맞추는 방법: 태그가 똑같은 기억"]];
  const FACT_TYPES = [["", "읽을 종류: 기억과 정리 모두"], ["memory", "읽을 종류: 기억만"], ["observation", "읽을 종류: 정리된 관찰만"]];
  const NO_FOLDER = "\u0000none";
  const MODEL_CODES = { model_not_configured: "서버에 요약을 쓸 모델이 없어요. 서버 설정에서 모델을 연결해 주세요.", model_budget_exceeded: "오늘 쓸 수 있는 모델 호출을 다 썼어요. 내일 다시 해 주세요." };
  const human = () => state.caps?.role === "human";
  const when = (v) => (v ? new Date(v).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "");
  const outcomeText = (o) => (o ? [OUTCOME[o.code] || o.code, TRIGGER[o.trigger] || o.trigger, when(o.at)].filter(Boolean).join(" · ") : "");
  const here = () => state.view === "memories" && state.memTab === "digests";

  function viewSeg() {
    const seg = ctx.segment(VIEWS, filter.view, (k) => {
      if (k === filter.view) return;
      filter.view = k; filter.q = ""; state.selected = null; picked = k !== "topics"; editing = null;
      render(false); detail();
    });
    seg.classList.add("mem-seg");
    return seg;
  }
  function drawDerived() {
    const tools = el("div", "mem-tools");
    if (filter.view === "models") {
      tools.append(searchBox("주제 검색"));
      if (human()) tools.append(button("새 주제 만들기", "small", "plus", () => { editing = "new"; pick("mm:new"); }));
    } else {
      void loadObservations();
      const scopes = Array.isArray(side.scopes) ? side.scopes : [];
      tools.append(searchBox("정리 검색"),
        ctx.memSelect("상태", OBS_STATUS, filter.obsStatus, (v) => { filter.obsStatus = v; state.selected = null; render(false); detail(); }));
      if (scopes.length > 1) tools.append(ctx.memSelect("묶음", [["", "묶음: 모두"], ...scopes.map((x) => [x.tags.join("\u0000") || "-", `묶음: ${x.tags.join(", ") || "태그 없음"} ${x.active + x.proposed}`])],
        filter.obsScope, (v) => { filter.obsScope = v; paintList(); }));
      if (human()) tools.append(button("지금 정리하기", "small", null, () => pick("ob:run")));
    }
    $("#controls").replaceChildren(viewSeg(), tools);
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    paintList();
  }

  // 직접 정한 주제: the approved ones (proposed and drafted ones also wait in AI 정리)
  function paintModels() {
    const items = data.models.filter((m) => !filter.q || `${m.title} ${m.query}`.toLowerCase().includes(filter.q));
    const table = $("#table");
    table.replaceChildren(...items.map((m) => {
      const cell = el("div", "cell mem-cell");
      const next = m.next_refresh_at ? `다음 ${when(m.next_refresh_at)}` : "";
      cell.append(raw(el("div", "text", m.title)), el("div", "mem-meta", [modeText(m), next, m.last_outcome && OUTCOME[m.last_outcome.code]].filter(Boolean).join(" · ")));
      const [label, cls] = m.draft ? ["초안 확인 대기", " warn"] : m.stale ? [STALE[m.stale_reason] || "오래됨", " warn"] : m.active ? ["사용 중", ""] : ["비어 있음", ""];
      return row(`mm:${m.id}`, "minmax(0,1fr) auto", [cell, el("span", `mem-badge${cls}`, label)], () => detail());
    }));
    if (!data.key) table.append(el("div", "empty", "주제를 불러오는 중…"));
    else if (data.modelsRead === false) table.append(el("div", "empty", "주제를 불러오지 못했어요. 새로고침하면 다시 불러와요."));
    else if (data.modelsRead === null) table.append(el("div", "empty", "이 서버는 직접 정한 주제를 지원하지 않아요."));
    else if (!items.length) table.append(el("div", "empty", filter.q ? "검색과 맞는 주제가 없어요." : "아직 직접 정한 주제가 없어요. 「새 주제 만들기」로 궁금한 것을 적으면 AI가 승인된 기억으로 요약 초안을 써요."));
    markSelected();
  }
  function modelDetail() {
    const id = (state.selected || "").replace(/^mm:/, "");
    if (editing && (editing === id || (editing === "new" && id === "new"))) return modelForm(id === "new" ? null : data.models.find((x) => x.id === id));
    const m = data.models.find((x) => x.id === id);
    const body = detailShell("직접 정한 주제");
    if (!m) { body.append(el("h2", "", "주제를 고르세요"), el("p", "lead", "궁금한 것을 질문으로 적어 두면, AI가 승인된 기억만 읽고 요약 초안을 써요. 초안은 사람이 승인해야 쓰여요.")); return; }
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "직접 정한 주제"), raw(el("h2", "", m.title)), raw(el("p", "meta", m.query)));
    if (m.stale) head.append(el("p", "dg-warn", STALE_LEAD[m.stale_reason] || "이 요약이 오래됐어요."));
    body.append(head);
    if (m.draft) {
      const card = el("div", "d-action dg-cand");
      card.append(el("p", "", "AI가 새 초안을 썼어요. 확인해 주세요 › AI 정리에서 승인하거나 버려 주세요."));
      if (ctx.go) { const b = el("div", "d-buttons"); b.append(button("초안 확인하기", "primary small", null, () => { ctx.go("memories", "notes"); state.selected = `note:model:${m.id}`; markSelected(); detail(); openDetail(); })); card.append(b); }
      body.append(card);
    }
    const now = ctx.section("지금 요약", m.active ? `v${m.active.version}` : "");
    if (m.active) now.append(quote(m.active.withheld ? "" : m.active.text, day(m.active.at)));
    else now.append(el("p", "meta", "아직 승인된 요약이 없어요. 「다시 만들기」를 누르면 AI가 초안을 써요."));
    body.append(now);
    const r = window.ArkismDigests.refreshOf(m);
    const trig = ctx.section("다시 만드는 때");
    trig.append(ctx.facts([["방식", modeText(m)], ["일정 (UTC)", r.cron || ""], ["기억 정리 뒤", r.after_consolidation ? "다시 만들어요" : ""], ["최소 간격", r.min_interval || ""],
      ["고치는 방식", r.strategy === "delta" ? "바뀐 부분만 고쳐요" : ""], ["전체 다시 쓰기 (UTC)", r.full_refresh_cron || ""],
      ["다음 실행", when(m.next_refresh_at) || (r.mode === "manual" && !r.cron && !r.after_consolidation ? "직접 요청할 때만" : "")], ["지난 결과", outcomeText(m.last_outcome)]]));
    body.append(trig);
    if (human()) {
      const btns = el("div", "d-buttons");
      btns.append(button("고치기", "small", null, () => { editing = m.id; detail(); }), refreshButton(`mm:${m.id}`, m, btns, "small"),
        button("내려받기", "small", null, () => void downloadModel(m)));
      const danger = el("div", "d-buttons");
      if (m.active || m.draft) danger.append(button("비우기", "ghost small", null, () => confirmRow(danger, "승인된 요약과 초안을 비울까요? 주제와 이력은 남아요.", "비우기",
        (b) => decideOnce(`mm:${m.id}`, () => request(`/v1/mental-models/${encodeURIComponent(m.id)}/clear`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) }),
          { ok: "요약을 비웠어요. 이력에는 남아요.", conflict: "그사이 주제가 바뀌었어요. 새로 불러왔어요.", denied: "이 계정은 요약을 비울 수 없어요." }, b))));
      danger.append(button("지우기", "ghost small", null, () => confirmRow(danger, "이 주제를 지울까요? 승인된 요약도 더는 쓰이지 않아요.", "지우기",
        (b) => decideOnce(`mm:${m.id}`, () => request(`/v1/mental-models/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) }),
          { ok: "주제를 지웠어요.", conflict: "그사이 주제가 바뀌었어요. 새로 불러왔어요.", denied: "이 계정은 주제를 지울 수 없어요." }, b))));
      body.append(btns);
      body.append(history(`/v1/mental-models/${encodeURIComponent(m.id)}/history`, `mm:${m.id}`), danger);
    } else body.append(el("p", "meta", "고치기와 다시 만들기는 사람만 할 수 있어요."), history(`/v1/mental-models/${encodeURIComponent(m.id)}/history`, `mm:${m.id}`));
    if (m.active?.citations?.length) body.append(members(m.active.citations.filter((c) => c.collection === "memories"), [], "근거 기억"));
  }
  // 다시 만들기: the model writes a new draft that waits in AI 정리 (POST /v1/mental-models/{id}/refresh).
  function refreshButton(id, m, btns, cls) {
    return button("다시 만들기", cls, null, () => decideOnce(id, () => request(`/v1/mental-models/${encodeURIComponent(m.id)}/refresh`, { method: "POST", body: JSON.stringify({ scope: state.scope }) }),
      { ok: "새 초안을 만들었어요. 읽어 보고 승인해 주세요.", conflict: "그사이 요약이 바뀌었어요. 새로 불러왔어요.", denied: "이 계정은 요약을 다시 만들 수 없어요.", codes: MODEL_CODES }, btns));
  }
  // A second, explicit step before something is cleared or deleted.
  function confirmRow(after, question, label, run) {
    after.parentElement?.querySelector(".dg-confirm")?.remove();
    const box = el("div", "d-action dg-cand dg-confirm"), btns = el("div", "d-buttons");
    btns.append(button(label, "primary small", null, () => run(btns)), button("취소", "ghost small", null, () => box.remove()));
    box.append(el("p", "dg-warn", question), btns);
    after.after(box);
  }
  // 바뀐 이력: every revision with its line diff against the one before (GET …/history), read once per open item.
  function history(path, key) {
    const s = ctx.section("바뀐 이력");
    const h = side.hist.get(key);
    if (h === undefined) {
      side.hist.set(key, null);
      request(`${path}?${query()}`).then((v) => v, () => false).then((v) => { side.hist.set(key, v); if (here() && state.selected === key && !document.querySelector("#detail [data-sync-dirty]")) detail(); });
    }
    if (!h) { s.append(el("p", "meta", h === false ? "이력을 불러오지 못했어요." : "이력을 불러오는 중…")); return s; }
    const revs = [...(h.revisions || [])].sort((a, b) => b.version - a.version || new Date(b.at) - new Date(a.at));
    if (!revs.length) { s.append(el("p", "meta", "아직 이력이 없어요.")); return s; }
    for (const [i, v] of revs.slice(0, 20).entries()) {
      const d = el("details", "insight-more dg-rev");
      if (!i) d.open = true;
      d.append(el("summary", "", [`v${v.version}`, REVISION[v.action] || v.action, when(v.at), v.by].filter(Boolean).join(" · ")));
      d.append(v.diff?.length ? diffBox(v.diff) : el("p", "meta", v.action === "cleared" ? "요약을 비웠어요." : "글은 그대로예요."));
      s.append(d);
    }
    return s;
  }
  // The approved summary as a Markdown file, with the text of each cited memory.
  async function downloadModel(m) {
    const cites = (m.active?.citations || []).filter((c) => c.collection === "memories").slice(0, 64);
    const got = await Promise.all(cites.map((c) => (texts.get(`memories/${c.id}`) ? texts.get(`memories/${c.id}`) : request(`/v1/memories/${encodeURIComponent(c.id)}?${query()}`).catch(() => null))));
    const md = window.ArkismDigests.modelMarkdown(m, got.map((r, i) => r?.text || cites[i].id), ctx.tr || ((x) => x));
    const url = URL.createObjectURL(new Blob([md], { type: "text/markdown" }));
    try { const a = el("a"); a.href = url; a.download = `${(m.title || "topic").replace(/[\\/:*?"<>|]+/g, "_").slice(0, 80)}.md`; a.click(); } finally { setTimeout(() => URL.revokeObjectURL(url), 1000); }
  }
  // 새 주제 만들기 / 고치기: title, question and when to remake; schedules and the rest sit under 자세히 (empty = server default).
  function modelForm(m) {
    const body = detailShell("직접 정한 주제");
    const r = m ? window.ArkismDigests.refreshOf(m) : { mode: "manual" };
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", m ? "직접 정한 주제 · 고치기" : "직접 정한 주제 · 새로 만들기"), el("h2", "", m ? m.title : "새 주제 만들기"),
      el("p", "lead", "질문을 적으면 AI가 승인된 기억만 읽고 요약 초안을 써요. 초안은 사람이 승인해야 쓰여요."));
    body.append(head);
    const dirty = (n) => { n.addEventListener("input", () => n.setAttribute("data-sync-dirty", "")); return n; };
    const input = (value, max, placeholder = "") => { const i = dirty(el("input")); i.value = value || ""; i.maxLength = max; i.placeholder = placeholder; return i; };
    const field = (label, n) => { const l = el("label", "insight-field"); l.append(el("span", "", label), n); return l; };
    const title = input(m?.title, 120, "예: 배포 전에 확인할 것");
    const q = dirty(el("textarea")); q.rows = 3; q.maxLength = 1000; q.value = m?.query || ""; q.placeholder = "예: 배포할 때 늘 확인하는 순서는?";
    const mode = ctx.memSelect("다시 만드는 때", Object.entries(MODE).map(([k, v]) => [k, `다시 만드는 때: ${v}`]), r.mode || "manual", () => {});
    const cron = input(r.cron, 100, "예: 0 9 * * 1");
    const after = el("input"); after.type = "checkbox"; after.checked = !!r.after_consolidation;
    const gap = input(r.min_interval, 20, "예: 6h");
    const strategy = ctx.memSelect("고치는 방식", [["", "고치는 방식: 처음부터 다시 쓰기"], ["delta", "고치는 방식: 바뀐 부분만 고치기"]], r.strategy === "delta" ? "delta" : "", () => {});
    const full = input(r.full_refresh_cron, 100, "예: 0 3 1 * *");
    const budget = ctx.memSelect("참고할 기억", BUDGET, r.budget || "", () => {});
    // 읽을 기억: tags and the wiki folder are on the form; the finer recall options sit under 자세히
    const D = window.ArkismDigests;
    const tags = input((r.tags || []).join(", "), 400, "예: 배포, 운영");
    const match = ctx.memSelect("태그 맞추는 방법", TAG_MATCH, r.tags_match || "", () => {});
    const groups = dirty(el("textarea")); groups.rows = 2; groups.maxLength = 2000; groups.value = D.tagGroupsText(r.tag_groups); groups.placeholder = "예: 배포+운영 | -연습";
    const facts = ctx.memSelect("읽을 종류", FACT_TYPES, r.fact_types?.length === 1 ? r.fact_types[0] : "", () => {});
    const chunks = el("input"); chunks.type = "checkbox"; chunks.checked = !!r.include_chunks;
    const chunkCheck = el("label", "insight-check"); chunkCheck.append(chunks, el("span", "", "기억의 원문 조각도 같이 읽기"));
    const recall = input(r.recall_max_tokens ? String(r.recall_max_tokens) : "", 6, "예: 4000"); recall.inputMode = "numeric";
    const schema = dirty(el("textarea")); schema.rows = 3; schema.maxLength = 8000; schema.spellcheck = false; schema.className = "mono";
    schema.value = r.response_schema ? JSON.stringify(r.response_schema, null, 2) : ""; schema.placeholder = '{"type": "object", "properties": {"할 일": {"type": "string"}}}';
    const inWiki = m ? m.folder ?? null : null;
    const folder = ctx.memSelect("위키에 두기", [[NO_FOLDER, "위키에 두기: 두지 않기"], ["", "위키에 두기: 맨 위"], ...(inWiki ? [[inWiki, `위키에 두기: ${inWiki}`]] : [])], inWiki ?? NO_FOLDER, () => {});
    void wikiFolders(folder);
    const preview = el("div", "dg-cron");
    const check = el("label", "insight-check");
    check.append(after, el("span", "", "기억 정리가 끝나면 다시 만들기"));
    const more = el("details", "insight-more");
    more.open = !!(r.cron || r.after_consolidation || r.min_interval || r.strategy || r.full_refresh_cron || r.budget || r.tag_groups?.length || r.fact_types?.length || r.include_chunks || r.recall_max_tokens || r.response_schema);
    more.append(el("summary", "", "자세히"), field("일정 (분 시 일 월 요일, UTC)", cron),
      button("일정 미리보기", "small", null, () => void previewCron(cron.value.trim(), preview)), preview, check, field("최소 간격", gap), strategy, field("전체 다시 쓰기 일정 (UTC)", full), budget,
      match, field("태그 조건 (한 줄에 하나, 모두 맞아야 해요. + 는 모두, | 는 하나라도, - 는 빼기)", groups), facts, chunkCheck,
      field("읽을 기억의 최대 양 (토큰, 비우면 제한 없음)", recall), field("요약 모양 (JSON Schema, 비우면 글로 써요)", schema));
    const form = el("div", "insight-edit");
    form.append(field("제목", title), field("질문", q), field("이 태그가 붙은 기억만 읽기 (쉼표로 나눠요)", tags), mode, folder, more);
    body.append(form);
    const btns = el("div", "d-buttons");
    btns.append(button("저장", "primary small", null, () => {
      let shape;
      try { shape = schema.value.trim() ? JSON.parse(schema.value) : undefined; } catch { notice("요약 모양 JSON을 읽을 수 없어요. 중괄호로 감싼 JSON으로 적어 주세요."); return; }
      const tagList = [...new Set(tags.value.split(",").map((x) => x.trim()).filter(Boolean))];
      void saveModel(m, btns, { title: title.value.trim(), query: q.value.trim(), ...D.folderBody(inWiki, folder.value),
        refresh: D.refreshBody(r, { mode: mode.value, cron: cron.value, after_consolidation: after.checked, min_interval: gap.value, strategy: strategy.value, full_refresh_cron: full.value, budget: budget.value,
          tags: tagList, tags_match: tagList.length ? match.value : "", tag_groups: D.tagGroups(groups.value), fact_types: facts.value ? [facts.value] : [], include_chunks: chunks.checked,
          recall_max_tokens: Number.parseInt(recall.value, 10) > 0 ? Number.parseInt(recall.value, 10) : undefined, response_schema: shape }) });
    }),
    button("취소", "ghost small", null, () => { editing = null; if (!m) state.selected = null; detail(); }));
    body.append(btns);
  }
  // The wiki folders a topic can sit in (GET /v1/wiki/tree), added to the open form's list; an older server has none.
  async function wikiFolders(select) {
    const tree = await request(`/v1/wiki/tree?${query()}`).catch(() => null);
    const have = new Set([...select.querySelectorAll("option")].map((o) => o.value));
    for (const f of Array.isArray(tree?.folders) ? tree.folders : []) {
      if (typeof f?.path !== "string" || have.has(f.path)) continue;
      const o = el("option", "", `위키에 두기: ${f.path}`); o.value = f.path; select.append(o);
    }
  }
  async function previewCron(cron, box) {
    if (!cron) { box.replaceChildren(el("p", "meta", "일정을 적으면 다음 실행 시각을 보여 줘요.")); return; }
    try {
      const v = await request("/v1/mental-models/cron-preview", { method: "POST", body: JSON.stringify({ scope: state.scope, cron }) });
      box.replaceChildren(el("p", "meta", "다음 실행 (이 PC 시각)"), ...(v?.next || []).map((t) => el("p", "dg-note", when(t))));
    } catch (e) { box.replaceChildren(el("p", "dg-warn", e.message === "invalid" ? "일정 형식이 맞지 않아요. 분 시 일 월 요일, 다섯 칸으로 적어 주세요." : `미리 보지 못했어요 (${e.message}).`)); }
  }
  async function saveModel(m, btns, v) {
    if (!v.title || !v.query) { notice("제목과 질문을 적어 주세요."); return; }
    btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    try {
      const saved = await request(m ? `/v1/mental-models/${encodeURIComponent(m.id)}` : "/v1/mental-models",
        { method: m ? "PATCH" : "POST", body: JSON.stringify({ scope: state.scope, ...(m ? { expected_revision: m.revision } : {}), ...v }) });
      notice(m ? "주제를 고쳤어요." : "주제를 만들었어요. 「다시 만들기」를 누르거나 정한 때가 되면 AI가 초안을 써요.");
      editing = null;
      if (saved?.id) state.selected = `mm:${saved.id}`;
    } catch (e) {
      notice(e.message === "invalid" ? "입력한 값을 확인해 주세요. 일정은 다섯 칸, 최소 간격은 6h처럼 적어요." : e.message === "revision_conflict" ? "그사이 주제가 바뀌었어요. 새로 불러왔어요."
        : e.message === "access_denied" ? "이 계정은 주제를 만들거나 고칠 수 없어요." : `저장하지 못했어요: ${e.message}`);
      btns.querySelectorAll("button").forEach((b) => { b.disabled = false; });
      if (e.message !== "revision_conflict") return;
      editing = null;
    }
    await reload();
    render(false); detail();
  }

  // 기억 정리: observations by status and scope, each with its sources and history
  async function loadObservations() {
    const status = filter.obsStatus, key = side.key;
    const want = [];
    if (side.obs[status] === undefined) { side.obs[status] = null; want.push(request(`/v1/observations?${query({ status, limit: 100 })}`).then((p) => { if (side.key === key) side.obs[status] = Array.isArray(p?.items) ? p.items : []; }, (e) => { if (side.key === key) side.obs[status] = absent(e) || false; })); }
    if (side.scopes === undefined) { side.scopes = null; want.push(request(`/v1/observations/scopes?${query()}`).then((p) => { if (side.key === key) side.scopes = Array.isArray(p?.items) ? p.items : []; }, () => { if (side.key === key) side.scopes = []; })); }
    if (!want.length) return;
    await Promise.all(want);
    if (here() && filter.view === "observations" && side.key === key) { render(false); detail(true); }
  }
  const scopeOf = (o) => [...new Set((o.tags || []).filter((t) => !t.startsWith("review:")))].sort().join("\u0000") || "-";
  function paintObservations() {
    const got = side.obs[filter.obsStatus];
    const items = (Array.isArray(got) ? got : []).filter((o) => (!filter.obsScope || scopeOf(o) === filter.obsScope) && (!filter.q || (o.content || "").toLowerCase().includes(filter.q)));
    const table = $("#table");
    table.replaceChildren(...items.map((o) => {
      const cell = el("div", "cell mem-cell");
      cell.append(raw(el("div", "text", o.content)), el("div", "mem-meta", [DURABLE[o.durability] || o.durability, `근거 기억 ${(o.source_memories || o.source_memory_ids || []).length}개`, occurred(o), ago(o.updated_at)].filter(Boolean).join(" · ")));
      return row(`ob:${o.id}`, "minmax(0,1fr) auto", [cell, el("span", `mem-badge${o.status === "active" ? "" : " warn"}`, (OBS_STATUS.find(([k]) => k === o.status)?.[1] || o.status || "").replace("상태: ", ""))], () => detail());
    }));
    if (got === undefined || got === null) table.append(el("div", "empty", "정리를 불러오는 중…"));
    else if (got === false) table.append(el("div", "empty", "정리를 불러오지 못했어요. 새로고침하면 다시 불러와요."));
    else if (!items.length) table.append(el("div", "empty", filter.q || filter.obsScope ? "검색과 맞는 정리가 없어요." : "여기에 표시할 정리가 없어요. 「지금 정리하기」로 AI에게 정리를 맡길 수 있어요."));
    markSelected();
  }
  function observationDetail() {
    const id = (state.selected || "").replace(/^ob:/, "");
    if (id === "run") return runPanel();
    const list = side.obs[filter.obsStatus];
    const o = Array.isArray(list) ? list.find((x) => x.id === id) : null;
    const body = detailShell("기억 정리");
    if (!o) { body.append(el("h2", "", "정리를 고르세요"), el("p", "lead", "AI가 승인된 기억 여러 개를 읽고 쓴 정리예요. 승인된 것만 기억과 함께 쓰여요.")); return; }
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "기억 정리"), raw(el("h2", "", o.content)),
      el("p", "meta", [(OBS_STATUS.find(([k]) => k === o.status)?.[1] || "").replace("상태: ", ""), DURABLE[o.durability] || o.durability, occurred(o)].filter(Boolean).join(" · ")));
    const tags = (o.tags || []).filter((t) => !t.startsWith("review:"));
    if (tags.length) head.append(raw(el("p", "meta", `묶음: ${tags.join(", ")}`)));
    body.append(head);
    const refs = o.source_memories || (o.source_memory_ids || []).map((x) => ({ collection: "memories", id: x }));
    body.append(members(refs.filter((r) => r.collection === "memories"), [], "근거 기억"), history(`/v1/observations/${encodeURIComponent(o.id)}/history`, `ob:${o.id}`));
    if (human()) {
      const btns = el("div", "d-buttons");
      btns.append(button("지우기", "ghost small", null, () => confirmRow(btns, "이 정리를 지울까요? 이력은 남고, 다음 정리 때 다시 제안될 수 있어요.", "지우기",
        (b) => decideOnce(`ob:${o.id}`, () => request("/v1/observations", { method: "DELETE", body: JSON.stringify({ scope: state.scope, ids: [o.id] }) }),
          { ok: "정리를 지웠어요.", conflict: "그사이 정리가 바뀌었어요. 새로 불러왔어요.", denied: "이 계정은 정리를 지울 수 없어요." }, b).then(() => { state.selected = null; detail(); }))));
      body.append(btns);
    }
  }

  // 지금 정리하기: first what a run would do (plan_only: no model call, no write), then the run itself after a click.
  // The run is one request, so the progress is its steps and the time it has taken; its drafts land in AI 정리.
  function runPanel() {
    const body = detailShell("기억 정리");
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "기억 정리 · 지금 실행"), el("h2", "", "지금 정리하기"),
      el("p", "lead", "AI가 승인된 기억을 다시 읽고 새 정리나 고친 정리를 제안해요. 제안은 확인해 주세요 › AI 정리에서 승인해야 쓰여요."));
    body.append(head);
    if (!side.run) startPlan();
    const run = side.run, p = run.plan?.plan;
    const steps = ctx.section("진행");
    const planLine = run.phase === "plan" ? "살펴보는 중…" : run.phase === "plan_failed" ? "" : p ? window.ArkismDigests.planText(p) : "";
    const res = run.result;
    steps.append(ctx.facts([["1. 살펴보기", planLine], ["2. 정리", run.phase === "running" ? "정리하는 중…" : res ? `${Math.max(1, Math.round(run.took / 1000))}초 걸렸어요` : run.phase === "failed" ? "멈췄어요" : "기다리는 중"],
      ["3. 결과", res ? window.ArkismDigests.runText(res) : ""]]));
    if (run.phase === "running") { const t = el("p", "meta", elapsed(run)); steps.append(t); tick(t, run); }
    if (run.error) steps.append(el("p", "dg-warn", run.error));
    body.append(steps);
    const btns = el("div", "d-buttons");
    if (run.phase === "planned" && human()) {
      const go = button("정리 시작", "primary small", null, () => void startRun());
      go.disabled = !!p && window.ArkismDigests.planEmpty(p);
      btns.append(go);
    }
    if (res && ctx.go) btns.append(button("AI 정리에서 확인하기", "primary small", null, () => { ctx.go("memories", "notes"); }));
    if (["done", "failed", "plan_failed"].includes(run.phase)) btns.append(button("다시 살펴보기", "small", null, () => { side.run = null; detail(); }));
    body.append(btns, el("p", "meta", "AI를 부르기 때문에 사용량에 잡혀요. 한 번에 정해진 묶음 수까지만 정리해요."));
    const more = el("details", "insight-more");
    more.append(el("summary", "", "자세히"), strategyBox(), scopeBox());
    body.append(more);
  }
  const elapsed = (run) => `${Math.round((Date.now() - run.started) / 1000)}초 지났어요`;
  function tick(node, run) {
    const t = setInterval(() => {
      if (!node.isConnected || run.phase !== "running") { clearInterval(t); return; }
      node.textContent = elapsed(run);
    }, 1000);
  }
  const runError = (e) => MODEL_CODES[e.message] || (e.message === "access_denied" ? "이 계정은 정리를 실행할 수 없어요." : `처리하지 못했어요: ${e.message}`);
  function startPlan() {
    const run = side.run = { phase: "plan" };
    request("/v1/observations/consolidate", { method: "POST", body: JSON.stringify({ scope: state.scope, plan_only: true }) })
      .then((v) => { run.plan = v; run.phase = "planned"; }, (e) => { run.phase = "plan_failed"; run.error = runError(e); })
      .then(() => { if (side.run === run && here() && state.selected === "ob:run") detail(); });
  }
  async function startRun() {
    const run = side.run;
    if (!run || run.phase !== "planned") return;
    run.phase = "running"; run.started = Date.now(); detail();
    try { run.result = await request("/v1/observations/consolidate", { method: "POST", body: JSON.stringify({ scope: state.scope }) }); run.phase = "done"; notice("정리를 마쳤어요. AI 정리에서 확인해 주세요."); }
    catch (e) { run.phase = "failed"; run.error = runError(e); }
    run.took = Date.now() - run.started;
    const keep = side.run; await reload(); side.run = keep; // the result stays on screen; the lists and counts are read again
    render(false); if (here() && state.selected === "ob:run") detail();
  }
  // How the space's consolidation strategies split the approved memories (no model, no write).
  function strategyBox() {
    const s = ctx.section("묶는 방식 미리보기");
    const v = side.strategies;
    if (v === undefined) s.append(button("미리보기", "small", null, () => {
      side.strategies = null; detail();
      request("/v1/observations/strategies/preview", { method: "POST", body: JSON.stringify({ scope: state.scope }) })
        .then((x) => x, (e) => ({ error: e.message })).then((x) => { side.strategies = x; if (here() && state.selected === "ob:run") detail(); });
    }));
    else if (v === null) s.append(el("p", "meta", "불러오는 중…"));
    else if (v.error) s.append(el("p", "dg-warn", `미리 보지 못했어요 (${v.error}).`));
    else {
      const rows = (v.strategies || []).map((x) => [x.name, `묶음 ${x.claimed}개${x.rules?.length ? ` · 규칙 ${x.rules.length}개` : ""}`]);
      rows.push(["기본 방식", `묶음 ${v.unclaimed}개`]);
      s.append(ctx.facts(rows), el("p", "meta", `기억 묶음 ${v.scopes}개 · 먼저 맞는 방식이 그 묶음을 맡아요.`));
    }
    return s;
  }
  // The tag scopes observations are kept under, and 모두 지우기 (people only; the history stays).
  function scopeBox() {
    const s = ctx.section("묶음");
    const list = Array.isArray(side.scopes) ? side.scopes : [];
    if (side.scopes === undefined) void loadObservations();
    if (list.length) s.append(ctx.facts(list.slice(0, 30).map((x) => [x.tags.join(", ") || "태그 없음", `사용 중 ${x.active} · 확인 대기 ${x.proposed}`])));
    else s.append(el("p", "meta", side.scopes === null ? "불러오는 중…" : "아직 정리가 없어요."));
    if (human() && list.length) {
      const btns = el("div", "d-buttons");
      btns.append(button("정리 모두 지우기", "ghost small", null, () => confirmRow(btns, "정리를 모두 지울까요? 이력은 남고, 다음 정리 때 다시 제안될 수 있어요.", "모두 지우기", (b) => void clearAll(b))));
      s.append(btns);
    }
    return s;
  }
  async function clearAll(btns) {
    btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    let n = 0;
    try {
      for (let i = 0; i < 20; i++) { // the server deletes up to 500 a call and says when more remain
        const v = await request("/v1/observations", { method: "DELETE", body: JSON.stringify({ scope: state.scope, all: true }) });
        n += v?.deleted?.length || 0;
        if (!v?.more) break;
      }
      notice(`정리 ${n}개를 지웠어요.`);
    } catch (e) { notice(e.message === "access_denied" ? "이 계정은 정리를 지울 수 없어요." : `지우지 못했어요: ${e.message}`); }
    await reload();
    render(false); detail();
  }

  function reset() { picked = false; editing = null; filter = { ...filter, view: "topics", chip: "all", q: "" }; expanded.clear(); }
  function close() {
    state.selected = null; markSelected();
    document.querySelectorAll(".dg-user.selected").forEach((n) => n.classList.remove("selected"));
    detail();
  }
  const title = () => (!data.key ? "" : filter.view === "models" ? `주제 ${data.models.length}개` : filter.view === "observations" ? (Array.isArray(side.obs[filter.obsStatus]) ? `정리 ${side.obs[filter.obsStatus].length}개` : "") : `주제 ${data.topic.length}개`);
  return { title, draw, drawMerge, drawNotes, detail, close, reset, mergeCount, notesCount, digestCounts, menuRows, refresh: () => { data.at = 0; ensure(); } };
};
// One memory row for 모아보기 and 물어보기 · 자주 나온 이름 (memory-insight.js): its text and an optional meta line.
// memoryText: m is the record, null when it can't be read, undefined while it loads.
window.ArkismDigests.memoryText = (m) => (m ? m.text : m === null ? "읽을 수 없는 기억이에요 (지워졌거나 바뀜)" : "불러오는 중…");
window.ArkismDigests.memoryItem = ({ el, raw }, text, meta = "") => {
  const item = el("button", "dg-item"); item.type = "button";
  const box = el("div", "dg-item-text"); box.append(raw(el("p", "", text)));
  if (meta) box.append(el("small", "", meta));
  item.append(box);
  return item;
};
// A mental model's refresh rule as an object (an old server sends the bare mode string).
window.ArkismDigests.refreshOf = (m) => (typeof m?.refresh === "string" ? { mode: m.refresh } : { mode: "manual", ...(m?.refresh || {}) });
// The rule to save: base keeps what this form does not show (kinds, tags, schema …); an empty field drops its key so the server default applies.
window.ArkismDigests.refreshBody = (base, v) => {
  const out = { ...base, mode: v.mode || base.mode || "manual" };
  for (const [k, raw] of Object.entries(v)) {
    if (k === "mode") continue;
    const x = typeof raw === "string" ? raw.trim() : raw;
    if (x === "" || x == null || x === false || (Array.isArray(x) && !x.length)) delete out[k]; else out[k] = x;
  }
  return out;
};
// The folder part of a save: keep it (nothing), place it, or take it out of the wiki (no_folder, only when it was in).
window.ArkismDigests.folderBody = (was, picked) => (picked === "\u0000none" ? (was == null ? {} : { no_folder: true }) : picked === was ? {} : { folder: picked });
// Tag conditions as text, one line per group: "a+b | -c" = (a and b) or not c. Parsed into search tag_groups and back.
// shortcut: a stored test with match any or exact over several tags reads back as all_strict; edit as JSON through the API to keep those.
window.ArkismDigests.tagGroups = (text) => String(text || "").split("\n").map((line) => line.split("|").map((t) => t.trim()).filter(Boolean).map((t) => {
  const not = t.startsWith("-"), tags = [...new Set(t.replace(/^-\s*/, "").split("+").map((x) => x.trim()).filter(Boolean))];
  return { tags, ...(tags.length > 1 ? { match: "all_strict" } : {}), ...(not ? { not: true } : {}) };
}).filter((g) => g.tags.length)).filter((g) => g.length);
window.ArkismDigests.tagGroupsText = (groups) => (Array.isArray(groups) ? groups : []).map((g) => (Array.isArray(g) ? g : []).map((t) => `${t?.not ? "-" : ""}${(t?.tags || []).join("+")}`).join(" | ")).join("\n");
// 지금 정리하기, step 1: what a plan_only run found.
window.ArkismDigests.planEmpty = (p) => !(p.stale_observations?.length || p.groups?.length || p.mental_models?.length);
window.ArkismDigests.planText = (p) => (window.ArkismDigests.planEmpty(p) ? "지금은 정리할 것이 없어요"
  : `근거 바뀐 정리 ${p.stale_observations?.length || 0}개 · 새로 묶을 기억 ${p.groups?.length || 0}묶음 · 다시 만들 주제 ${p.mental_models?.length || 0}개`);
// step 3: what the run proposed (all of it waits for a person in AI 정리).
window.ArkismDigests.runText = (r) => `새 정리 ${r.proposed?.length || 0}개 · 고친 정리 ${r.refreshed?.length || 0}개 · 주제 초안 ${r.mental_models?.length || 0}개 · 못 쓴 답 ${r.rejected?.length || 0}개 · AI 호출 ${r.calls || 0}번`;
// 내려받기: the approved summary as Markdown; texts are the cited memories' texts in order. tr translates the two headings.
window.ArkismDigests.modelMarkdown = (m, texts = [], tr = (x) => x) => [`# ${m.title}`, "", `> ${m.query}`, "", m.active?.text || tr("아직 승인된 요약이 없어요."),
  ...(texts.length ? ["", `## ${tr("근거 기억")}`, "", ...texts.map((t) => `- ${String(t).replace(/\s*\n\s*/g, " ")}`)] : []), ""].join("\n");
