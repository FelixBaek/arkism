// @ts-check
// MEMORY › 기억 › 모아보기 "주제 요약 · 나에 대한 요약" (Figma 382:29313 · 382:29983) and
// 확인해 주세요 "병합 제안" (383:29822), plus their HOME rows (385:30743).
//   /v1/digests?kind=topic|user&status=…   요약은 승인된 기억으로만 만든다. AI가 쓴 새 버전은 늘 후보이고, 사람이 승인해야 쓰인다.
//   /v1/merge_proposals?status=open        새 후보를 기존 기억에 합칠지·따로 저장할지·건너뛸지 AI가 제안한다. 누르기 전엔 아무것도 바뀌지 않는다.
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
  const SORT = [["heat", "열기 높은 순"], ["recent", "최근 바뀐 순"], ["name", "이름 순"]];
  const ACTION = { merge: "합치기", store: "저장", skip: "건너뛰기" };
  const REASON = { new_information: "새 정보", same_meaning: "같은 뜻", more_specific: "더 구체적", adds_detail: "내용 보탬", already_covered: "이미 있음", updates_older: "옛 내용 갱신" };

  // ------------------------------------------------------------ data
  const empty = () => ({ key: "", at: 0, topic: [], topicCand: [], cap: 0, activeN: 0, user: null, userCand: [], userOld: [], merge: null, other: {} });
  let data = empty(), loading = null, filter = { chip: "all", status: "active", sort: "heat", q: "" }, picked = false;
  const texts = new Map(); // "collection/id" → record (member memories, merge candidates and targets)
  const views = new Map(); // merge proposal id → GET /v1/merge_proposals/{id}
  const list = (kind, status) => request(`/v1/digests?${query({ kind, status })}`).catch(() => null);
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
      const [ta, tc, ua, uc, uo, mp] = await Promise.all([list("topic", "active"), list("topic", "candidate"), list("user", "active"), list("user", "candidate"), list("user", "superseded"),
        request(`/v1/merge_proposals?${query({ status: "open" })}`).catch(() => null)]);
      if (key !== identity()) return;
      const before = JSON.stringify([data.topic, data.topicCand, data.user, data.userCand, data.merge]);
      const items = (p) => (Array.isArray(p?.items) ? p.items : []);
      Object.assign(data, { key, at: Date.now(), topic: items(ta), topicCand: items(tc), cap: ta?.cap || tc?.cap || 0, activeN: ta?.active ?? items(ta).length,
        user: items(ua)[0] || null, userCand: items(uc), userOld: items(uo), merge: mp ? items(mp).filter((r) => r.merge?.proposal) : null });
      if (key !== data.other.key) data.other = { key };
      if (before !== JSON.stringify([data.topic, data.topicCand, data.user, data.userCand, data.merge])) repaint();
    })().finally(() => { loading = null; });
    return loading;
  }
  function repaint() {
    const here = state.view === "memories" && ["digests", "merge"].includes(state.memTab);
    const home = state.view === "todo" && !state.selected && !state.todoAll;
    const mem = state.view === "memories" && ["active", "candidate", "conflicted", "idle"].includes(state.memTab);
    if (!here && !home && !mem) return;
    if (document.querySelector("#detail [data-sync-dirty]")) { if (here) paintList(); return; }
    render(false);
    if (here) detail();
  }
  async function reload() { data.at = 0; texts.clear(); views.clear(); await load(); }
  // A record a digest or a proposal points at; read once, kept by id + revision.
  function record(collection, id) {
    const k = `${collection}/${id}`;
    if (texts.has(k)) return texts.get(k);
    const local = collection === "memories" ? state.data.memories.find((m) => m.id === id) : collection === "memory_candidates" ? state.data.candidates.find((m) => m.id === id) : null;
    if (local) { texts.set(k, local); return local; }
    texts.set(k, undefined);
    request(`/v1/${collection}/${encodeURIComponent(id)}?${query()}`).then((r) => { texts.set(k, r?.id === id ? r : null); }, () => texts.set(k, null))
      .then(() => { if (state.view === "memories" && ["digests", "merge"].includes(state.memTab) && !document.querySelector("#detail [data-sync-dirty]")) { paintList(); detail(true); } });
    return undefined;
  }

  // ------------------------------------------------------------ counts (memory screen, HOME)
  function mergeCount() { ensure(); return data.key === identity() && data.merge ? data.merge.length : data.key === identity() ? 0 : null; }
  function digestCounts() { ensure(); return { topic: data.topicCand.length, user: data.userCand.length }; }
  // 메뉴별 확인할 것: "병합 제안 n" joins the 기억 row, 모아보기 gets its own row right under it (Figma 385:30743).
  function menuRows(rows) {
    ensure();
    const at = rows.findIndex((r) => r[1] === "기억");
    const n = data.merge?.length || 0;
    if (at >= 0 && n) rows[at] = [rows[at][0], rows[at][1], [...rows[at][2], { label: `병합 제안 ${n}`, n }], rows[at][3]];
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
    const search = el("label", "panel-search");
    const q = el("input"); q.type = "search"; q.placeholder = "주제 검색"; q.setAttribute("aria-label", "주제 검색"); q.value = filter.q;
    q.addEventListener("input", () => { filter.q = q.value.trim().toLowerCase(); paintList(); });
    search.append(ctx.icon("magnifying-glass"), q);
    tools.append(search,
      ctx.memSelect("상태", STATUS, filter.status, (v) => { filter.status = v; filter.chip = "all"; if (v !== "active") void loadOther(v); paintList(); }),
      ctx.memSelect("정렬", SORT, filter.sort, (v) => { filter.sort = v; paintList(); }));
    $("#controls").replaceChildren(...top, tools);
    $("#stats").replaceChildren();
    paintList();
  }
  async function loadOther(status) {
    if (data.other[status]) return;
    const page = await list("topic", status);
    data.other[status] = Array.isArray(page?.items) ? page.items : [];
    if (state.view === "memories" && state.memTab === "digests" && filter.status === status) paintList();
  }
  function paintList() {
    if (state.view !== "memories") return;
    if (state.memTab === "merge") { paintMerge(); return; }
    if (state.memTab !== "digests") return;
    const all = entries();
    const max = Math.max(0, ...all.map(scoreOf));
    const counts = { all: all.length, hot: 0, candidate: 0, quiet: 0 };
    all.forEach((e) => { counts[classOf(e, max)]++; });
    const chips = filter.status === "active" ? [["all", "전체"], ["hot", "뜨거움"], ["candidate", "새 후보"], ["quiet", "잠잠함"]] : [["all", "전체"]];
    $("#chips").replaceChildren(...chips.map(([k, label]) => {
      const c = el("button", `chip mem-kind${filter.chip === k ? " active" : ""}`); c.type = "button";
      c.append(el("span", "", label), el("output", "", String(counts[k])));
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
      const meta = [`기억 ${d.members?.length || 0}개`, `열기 ${scoreOf(e)}`, ago(d.updated_at)];
      if (e.digest?.outdated) meta.push("바탕 기억이 수정됨");
      if (e.digest?.stale) meta.push("그 뒤 승인된 기억 있음");
      cell.append(raw(el("div", "text", titleOf(e))), el("div", "mem-meta", meta.join(" · ")));
      const cls = classOf(e, max);
      const badge = e.cand ? el("span", "mem-badge warn", e.digest ? "새 후보" : PROPOSAL[e.cand.proposal] || "새 후보")
        : filter.status !== "active" ? el("span", "mem-badge", STATUS.find(([k]) => k === filter.status)[1].replace("상태: ", ""))
        : el("span", "mem-badge", cls === "quiet" ? "잠잠함" : `열기 ${scoreOf(e)}`);
      return row(`digest:${e.id}`, "minmax(0,1fr) auto", [cell, badge], () => detail());
    }));
    if (!data.key) table.append(el("div", "empty", "요약을 불러오는 중…"));
    else if (!items.length) table.append(el("div", "empty", filter.q ? "검색과 맞는 주제가 없어요." : filter.status === "active" ? "아직 주제 요약이 없어요. 승인된 기억이 쌓이면 밤사이 제안돼요." : "여기에 표시할 요약이 없어요."));
    const foot = el("div", "mem-more");
    const capNote = data.cap && data.activeN >= data.cap ? " · 최대 개수에 닿아서 합치기 제안이 나올 수 있어요" : "";
    foot.append(el("span", "", `${all.length}개 중 ${items.length}개 표시 · 열기 = 최근 30일에 참고되거나 쓰인 정도${data.cap ? ` · 최대 ${data.cap}개` : ""}`), capNote);
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
    const id = (state.selected || "").replace(/^digest:/, "");
    if (id === "user") return userDetail();
    const e = entries().find((x) => x.id === id);
    const body = detailShell("모아보기");
    if (!e) { body.append(el("h2", "", "주제를 고르세요"), el("p", "lead", "비슷한 기억을 묶은 요약이에요. 사실이나 지시가 아니라 참고 자료로 쓰여요.")); return; }
    const d = e.digest, c = e.cand;
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", d ? `주제 · 열기 ${scoreOf(e)}` : `주제 후보 · ${PROPOSAL[c.proposal] || "새 후보"}`), raw(el("h2", "", titleOf(e))));
    if (d) head.append(el("p", "meta", `기억 ${d.members?.length || 0}개 · 요약 v${d.revision} · ${day(d.review?.at || d.updated_at)} 요약`));
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
    head.append(el("p", "meta", u ? `기억 ${u.members?.length || 0}개 · 요약 v${u.revision} · ${day(u.review?.at || u.updated_at)}` : "승인하면 새 세션 시작 때 에이전트에게 참고 자료로 전달돼요(지시가 아님)."));
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
  function candidateCard(c, kind, lead) {
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
    const reason = el("textarea", "dg-reason"); reason.rows = 2; reason.maxLength = 500; reason.placeholder = "버리는 이유 (선택)"; reason.hidden = true;
    reason.addEventListener("input", () => reason.toggleAttribute("data-sync-dirty", !!reason.value));
    const btns = el("div", "d-buttons");
    const ok = button("승인", "primary small", null, () => decideDigest(c, kind, "approve", "", btns));
    ok.disabled = !c.fresh;
    const no = button("버리기", "ghost small", null, () => {
      if (reason.hidden) { reason.hidden = false; reason.focus(); return; }
      void decideDigest(c, kind, "reject", reason.value.trim(), btns);
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
      const item = el("button", "dg-item"); item.type = "button";
      const txt = el("div", "dg-item-text");
      txt.append(raw(el("p", "", m ? m.text : m === null ? "읽을 수 없는 기억이에요 (지워졌거나 바뀜)" : "불러오는 중…")),
        el("small", "", m ? [kindLabel(m.kind), day(m.created_at), isNew.has(ref.id) ? "새로 묶임" : ""].filter(Boolean).join(" · ") : ""));
      item.append(txt);
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
  async function decideDigest(c, kind, decision, reason, btns) {
    if (busy.has(c.id)) return;
    busy.add(c.id); btns.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    try {
      await request(`/v1/digests/${encodeURIComponent(c.id)}/decide`, { method: "POST",
        body: JSON.stringify({ scope: state.scope, kind, expected_revision: c.revision, decision, ...(reason ? { reason } : {}) }) });
      notice(decision === "approve" ? (kind === "user" ? "나에 대한 요약을 바꿨어요. 새 세션부터 쓰여요." : "요약을 바꿨어요. 이전 버전은 이력에 남아요.") : "후보를 버렸어요.");
    } catch (e) {
      notice(e.message === "revision_conflict" ? "바탕이 바뀌었거나 주제 수가 꽉 차서 처리하지 못했어요. 새로 불러왔어요. (꽉 찼다면 합치기 후보를 먼저 승인하세요)"
        : e.message === "access_denied" ? "이 계정은 요약을 승인할 수 없어요." : `처리하지 못했어요: ${e.message}`);
    } finally { busy.delete(c.id); }
    await reload();
    render(false); detail();
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
    else if (data.merge === null) table.append(el("div", "empty", "이 서버는 병합 제안을 지원하지 않아요."));
    else if (!items.length) table.append(el("div", "empty", state.query ? "검색과 맞는 제안이 없어요." : "확인할 병합 제안이 없어요."));
    if (items.length) { const foot = el("div", "mem-more"); foot.append(el("span", "", "AI 제안은 바로 적용하지 않아요. 고르면 그때 저장돼요.")); table.append(foot); }
    markSelected();
  }
  function mergeDetail(soft) {
    const id = (state.selected || "").replace(/^merge:/, "");
    const r = (data.merge || []).find((x) => x.id === id);
    if (soft && !r) return;
    const body = detailShell("병합 제안");
    if (!r) { body.append(el("h2", "", "제안을 고르세요"), el("p", "lead", "AI가 새 후보를 이미 있는 기억과 견줘 본 결과예요. 고르기 전엔 아무것도 바뀌지 않아요.")); return; }
    const p = proposal(r);
    const v = views.get(r.id);
    if (v === undefined) {
      views.set(r.id, null);
      request(`/v1/merge_proposals/${encodeURIComponent(r.id)}?${query()}`).then((x) => views.set(r.id, x?.proposal?.id === r.id ? x : false), () => views.set(r.id, false))
        .then(() => { if (state.selected === `merge:${r.id}` && !document.querySelector("#detail [data-sync-dirty]")) mergeDetail(); });
    }
    const cand = v?.candidate || record("memory_candidates", p.candidate?.id);
    const targets = v?.targets?.length ? v.targets : (p.targets || []).map((t) => record("memories", t.id)).filter(Boolean);
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", "병합 제안 · AI 제안"),
      el("h2", "", { merge: "비슷한 기억이 이미 있어요", store: "새 기억으로 저장 제안", skip: "이미 있는 기억과 같아요" }[p.action] || "AI 제안"),
      el("p", "meta", [ago(r.created_at), cand ? originOf(cand) : ""].filter(Boolean).join(" · ")));
    body.append(head);

    const card = el("div", "d-action dg-cand");
    const line = el("div", "dg-badges");
    line.append(el("span", "dg-label", "AI 제안"), el("span", "mem-badge warn", ACTION[p.action] || p.action));
    if (REASON[p.reason_code]) line.append(el("span", "mem-badge", REASON[p.reason_code]));
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
    for (const [op, t] of window.ArkismTextDiff(before, after)) box.append(op === "=" ? document.createTextNode(t) : el(op === "-" ? "del" : "ins", "", t));
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

  function reset() { picked = false; filter = { ...filter, chip: "all", q: "" }; expanded.clear(); }
  function close() {
    state.selected = null; markSelected();
    document.querySelectorAll(".dg-user.selected").forEach((n) => n.classList.remove("selected"));
    detail();
  }
  return { title: () => (data.key ? `주제 ${data.topic.length}개` : ""), draw, drawMerge, detail, close, reset, mergeCount, digestCounts, menuRows, refresh: () => { data.at = 0; ensure(); } };
};
