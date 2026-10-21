import { candidateGroup } from "./candidateGroups.mjs";
import { collectionFooter } from "./collectionPaging.mjs";
// Owns memory lists, filters and actions. Dependencies are explicit live ports, never a global scope.
export function createMemoryList(deps) {


  // ---------------------------------------------------------------- 기억
  // Figma v4 Brain › 기억 (198:630): 확인해 주세요 card → search + dropdowns → kind chips → list.
  // 확인할 것 (검토 대기·확인 필요·안 쓰임) and 모아보기 (작업 관계·사용자 특성·반성 교훈·과거 시점·대화) are sub-screens with "← 기억".
  const MEM_CHECK = [["candidate", "승인 대기"], ["provenance", "승인 경로"], ["conflicted", "헷갈림"], ["idle", "호출과 문구 검토"], ["merge", "병합 제안"]];
 // merge: digests.js
  const MEM_LOOK = [["digests", "주제 요약"], ["relations", "작업 관계"], ["traits", "사용자 특성"], ["reflections", "반성 교훈"], ["past", "과거 시점"], ["originals", "대화"]];

  const memPanels = () => ({ relations: deps.relations, traits: deps.traits, reflections: deps.reflections, past: deps.memoryTime, originals: deps.originals });

  function checkCounts() {
    const d = deps.state.data;
    return { candidate: d.candidates.length, provenance: d.memories.filter(deps.approvalUnproven).length, conflicted: d.conflicted.length, idle: deps.state.usageDone ? d.memories.filter(deps.idle).length : null, merge: deps.digests.mergeCount() };
  }

  const checkTotal = () => Object.entries(checkCounts()).reduce((a, [key, n]) => key === "provenance" ? a : a + (n || 0), 0);

  // [title, one line under it, count next to the title]
  function memTitle() {
    const t = deps.memory.memTab;
    const check = { provenance: "과거 처리 계정만으로 직접 사용자 확인을 증명할 수 없는 기억이에요. 기존 내용이나 상태는 자동으로 바꾸지 않아요.", candidate: "AI가 대화에서 새로 뽑은 기억이에요. 저장할지 버릴지 정해 주세요.", conflicted: "이미 있는 기억과 다른 내용이 들어왔어요. 어느 쪽이 맞는지 골라 주세요.",
      idle: "재검토 예정일이 지났거나 최근 호출·문구 겹침이 적은 기억이에요. 실제 활용 여부와는 구분해요.", merge: "새로 들어온 기억이 이미 있는 기억과 비슷해요. AI 제안을 보고 저장할지, 합칠지, 건너뛸지 골라 주세요." }[t];
    if (check) return ["확인해 주세요", check, `${deps.catalogQuery.snapshot(catalogName()).total ?? checkTotal()}개`];
    if (t === "digests") return ["모아보기", "비슷한 기억을 주제로 묶고 AI가 요약해요. 요약이 바뀌면 바로 쓰지 않고 먼저 확인을 받아요.", deps.digests.title()];
    const look = { relations: "같은 작업에서 이어서 쓰인 스킬 순서예요. 관찰한 것일 뿐, 실행 허가는 아니에요.", traits: "대화에서 관찰한 사용자 성향이에요. 지시나 실행 승인으로 쓰지 않아요.",
      reflections: "작업하면서 얻은 해석이에요. 사실이나 실행 승인으로 쓰지 않아요.", past: "정한 시각에 쓰이던 기억을 그대로 보여 줘요. 지금 지시로는 쓰지 않아요.",
      originals: "에이전트와 나눈 대화예요. 7일 보관하고, 기억이 실제로 쓰이면 그때부터 다시 7일을 세요." }[t];
    if (look) return [MEM_LOOK.find(([k]) => k === t)[1], look, ""];
    const w = deps.memUse();
    const rate = w?.injections ? Math.round((w.used / w.injections) * 100) : null;
    return ["기억", `AI가 답할 때 참고하는 내용이에요.${rate === null ? "" : ` 전달한 기억 중 ${rate}%에서 답변 문구 겹침이 확인됐어요. 실제 활용 여부는 측정하지 않았어요.`}`, `${deps.catalogQuery.snapshot("memories").total ?? deps.memoryCount()}개`];
  }

  const catalogName = () => ({ candidate: "candidates", conflicted: "conflicted" }[deps.memory.memTab] || "memories");
  function memorySource() { deps.catalogQuery.ensure(catalogName()); return deps.catalogQuery.snapshot(catalogName()).items; }

  // "← 기억" above the title on every sub-screen.
  function memBack() {
    let b = deps.$("#mem-back");
    if (!b) {
      b = deps.el("button", "mem-back"); b.id = "mem-back"; b.type = "button";
      b.append(deps.icon("arrow-left"), "기억");
      b.addEventListener("click", () => deps.go("memories", "active"));
      deps.$(".main > .page-head").prepend(b);
    }
    b.hidden = deps.memory.memTab === "active";
  }

  // One badge per row: how often it is used, or why it needs a look.
  function memBadge(m) {
    const badge = (text, cls = "") => deps.el("span", `mem-badge${cls ? ` ${cls}` : ""}`, text);
    if (deps.reviewOverdue(m)) return badge("재검토 예정일 지남", "warn");
    if (deps.standing(m)) return badge("항상 적용");
    if (deps.state.usageDone && deps.idle(m)) return badge("검토 필요", "warn");
    const fresh = Date.now() - new Date(m.created_at).getTime() < deps.DAY;
    const s = deps.memStat(m.id);
    if (s) return s.injected ? badge(`문구 겹침 ${s.used}번`) : badge(fresh ? "새 기억" : "—", fresh ? "new" : "");
    const u = deps.usageOf(m.id);
    if (!u) return badge("…");
    if (!u.d30) return badge(fresh ? "새 기억" : "안 불림", fresh ? "new" : "");
    return badge(`${u.d30}번 참고`);
  }

  function memCell(m, meta) {
    const cell = deps.el("div", "cell mem-cell");
    cell.append(deps.raw(deps.el("div", "text", m.text)), deps.el("div", "mem-meta", meta.filter(Boolean).join(" · ")));
    return cell;
  }

  // A select that reads "이름: 값 ▾" like the Figma dropdowns.
  function memSelect(label, options, value, onPick) {
    const s = deps.el("select", "mem-select");
    s.setAttribute("aria-label", label);
    for (const [v, l] of options) { const o = deps.el("option", "", l); o.value = v; o.selected = v === value; s.append(o); }
    s.addEventListener("change", () => onPick(s.value));
    return s;
  }

  function renderMemories() {
    memBack();
    if (deps.memory.memTab === "digests") { deps.digests.draw(); return; }
    const panel = memPanels()[deps.memory.memTab];
    if (panel) {
      const seg = deps.segment(MEM_LOOK, deps.memory.memTab, (k) => deps.go("memories", k)); seg.classList.add("mem-seg");
      deps.$("#controls").replaceChildren(seg);
      panel.draw(); return;
    }
    if (MEM_CHECK.some(([k]) => k === deps.memory.memTab)) { renderMemoryCheck(); return; }

    const source = memorySource();
    // 확인해 주세요: what is waiting, and one button that starts with the first non-empty list.
    const n = checkCounts(), total = checkTotal();
    const top = [];
    if (total) {
      const card = deps.el("div", "mem-check");
      const txt = deps.el("div", "mem-check-text");
      const line = deps.el("p", "mem-check-title"); line.append("확인해 주세요", deps.el("strong", "", String(total)));
      txt.append(line, deps.el("p", "mem-check-sub", `승인 대기 ${n.candidate} · 내용이 헷갈림 ${n.conflicted} · 검토 필요 ${n.idle ?? "…"}`));
      if (n.merge) txt.lastChild.append(` · 병합 제안 ${n.merge}`);
      const first = MEM_CHECK.find(([k]) => k !== "provenance" && n[k])?.[0] || "candidate";
      card.append(txt, deps.button("하나씩 확인하기", "primary small", null, () => deps.go("memories", first)));
      top.push(card);
    }
    const tools = deps.el("div", "mem-tools");
    tools.append(memSearch());
    const facetRepos = deps.catalogQuery.snapshot("memories").facets.repositories || {};
    const repos = [...new Set([...Object.keys(facetRepos).filter(Boolean), ...(deps.memory.repo && deps.memory.repo !== "shared" ? [deps.memory.repo] : [])])].sort((a, b) => a.localeCompare(b));
    // Keep a chosen repository while the next server page is loading.
    if (repos.length) {
      const shared = facetRepos[""] || 0;
      tools.append(memSelect("저장소", [["", "저장소: 모두"], ["shared", `저장소: 공용 ${shared}`], ...repos.map((r) => [r, `저장소: ${r.split("/").pop()} ${facetRepos[r] || 0}`])],
        deps.memory.repo || "", (v) => { deps.memory.repo = v; deps.render(false); }));
    }
    tools.append(memSelect("모아보기", [["", "모아보기: 전체"], ...[["provenance", "승인 경로"], ...MEM_LOOK].map(([k, l]) => [k, `모아보기: ${l}`])], "", (v) => { if (v) deps.go("memories", v); }));
    tools.append(memSelect("정렬", [["recent", "최근 순"], ["used", "많이 불린 순"], ["old", "오래된 순"]], deps.memory.sort, (v) => { deps.memory.sort = v; deps.render(false); }));
    tools.append(deps.inject.button());
    deps.$("#controls").replaceChildren(...top, tools);

    const counts = deps.catalogQuery.snapshot("memories").facets.kinds || {};
    const chips = [["all", "전체", Object.values(counts).reduce((sum, n) => sum + n, 0)], ...Object.keys(deps.KINDS).map((k) => [k, deps.kindLabel(k), counts[k] || 0])];
    deps.$("#chips").replaceChildren(...chips.map(([k, label, num]) => {
      const c = deps.el("button", `chip mem-kind${deps.memory.kind === k ? " active" : ""}`); c.type = "button";
      c.append(deps.el("span", "", label), deps.el("output", "", String(num)));
      c.addEventListener("click", () => { deps.memory.kind = k; deps.render(false); });
      return c;
    }));
    deps.$("#stats").replaceChildren(catalogNote());

    const items = source; // Already filtered and ordered across the server catalog.
    const tpl = "minmax(0,1fr) auto";
    const shown = memPage(items);
    drawRows(shown, (m) => deps.row(m.id, tpl, [memCell(m, [deps.kindLabel(m.kind), deps.originOf(m), deps.ago(m.created_at), deps.memRepo(m).split("/").pop()]), memBadge(m)], deps.renderDetail));
    if (!items.length) deps.$("#table").append(deps.el("div", "empty", deps.memory.query ? "검색과 맞는 기억이 없어요." : "여기에 표시할 기억이 없어요."));
    memMore(items.length, shown.length);
  }

  // "380개 중 50개 표시 · 더 보기" (Figma 198:630): long lists draw 50 rows at a time; a new filter starts over.
  const MEM_PAGE = 50;

  let memPaging = { key: "", n: MEM_PAGE };

  function memPage(items) {
    const key = [deps.memory.memTab, deps.memory.kind, deps.memory.repo, deps.memory.sort, deps.memory.query].join("|");
    if (memPaging.key !== key) memPaging = { key, n: MEM_PAGE };
    const n = Math.max(memPaging.n, items.findIndex((m) => m.id === deps.state.selected) + 1);
    return items.slice(0, n);
  }

  function drawRows(items, row) {
    deps.virtualList.mount({ container: deps.$("#table"), items, row, key: JSON.stringify([deps.generation, deps.memory.memTab, deps.memory.kind, deps.memory.repo, deps.memory.sort, deps.memory.query]) });
  }

  function memMore(total, shown) {
    const tab = deps.memory.memTab, generation = deps.generation;
    const collection = catalogName();
    const foot = collectionFooter({ document, name: collection, total, shown,
      snapshot: () => deps.catalogQuery.snapshot(collection),
      reveal: () => { memPaging.n = shown + MEM_PAGE; },
      fetch: () => { memPaging.n = shown + MEM_PAGE; return deps.catalogQuery.more(collection); },
      render: () => deps.render(false),
      isCurrent: () => generation === deps.generation && deps.state.view === "memories" && tab === deps.memory.memTab
    });
    if (foot) deps.$("#table").append(foot);
  }

  // 확인해 주세요 (198:716 · 198:1748 · 198:3984): one 3-way segment, rows with a reason badge, ✓ ✕ on new ones.
  // The list filter (state.query): the top-bar #search is hidden (shell.css), so each memory list draws this box.
  function catalogNote() {
    const snap = deps.catalogQuery.snapshot(catalogName());
    const message = snap.error ? "전체 목록을 불러오지 못했어요. 다시 불러오기를 눌러 주세요." : snap.loading ? "전체 목록에서 찾는 중…" : `전체 검색 결과 ${snap.total ?? 0}개 · 검색·정렬은 서버 전체 기준이에요. 후보 묶음은 불러온 항목 기준이에요.`;
    return deps.el("p", "meta memory-page-note", message);
  }
  function memSearch() {
    const search = deps.el("label", "panel-search");
    const q = deps.el("input"); q.type = "search"; q.placeholder = "기억 검색"; q.setAttribute("aria-label", "기억 검색"); q.value = deps.$("#search").value;
    q.addEventListener("input", () => {
      deps.$("#search").value = q.value; deps.memory.query = q.value.trim().toLowerCase();
      const at = q.selectionStart;
      deps.render(false);
      const again = deps.$(".panel-search input"); again?.focus(); again?.setSelectionRange(at, at);
    });
    search.append(deps.icon("magnifying-glass"), q);
    return search;
  }

  function renderMemoryCheck() {
    const d = deps.state.data, n = checkCounts();
    const seg = deps.segment(MEM_CHECK.map(([k, l]) => [k, l, n[k], true]), deps.memory.memTab, (k) => deps.go("memories", k)); seg.classList.add("mem-seg");
    deps.$("#controls").replaceChildren(seg);
    if (deps.memory.memTab === "merge") { deps.digests.drawMerge(); return; }
    const tools = deps.el("div", "mem-tools"); tools.append(memSearch());
    deps.$("#controls").append(tools);
    deps.$("#stats").replaceChildren(catalogNote());
    const items = memorySource();
    const canDecide = !!deps.state.caps?.actions?.review_decide;
    if (deps.memory.memTab === "candidate" && items.length) {
      const hint = deps.el("div", "mem-hint");
      const p = deps.el("p", "");
      p.append(`${items.length}개 후보 · 비슷한 내용은 묶어서 보여줘요. 펼쳐서 항목별로 결정해 주세요.`);
      hint.append(p);
      if (canDecide && d.candidates.length > 1) hint.append(deps.button("여러 개 한 번에 처리", "small", null, () => { deps.state.selected = "batch"; deps.markSelected(); deps.renderDetail(); deps.openDetail(); }));
      const stale = canDecide ? staleCandidates() : [];
      if (stale.length) hint.append(deps.button(`30일 지난 후보 정리 ${stale.length}`, "ghost small", null, (e) => expireStale(stale.length, e.currentTarget)));
      deps.$("#chips").replaceChildren(hint);
    } else deps.$("#chips").replaceChildren();
    const table = deps.$("#table");
    if (deps.memory.memTab === "candidate") {
      const groups = window.ArkismMemoryManagement.groups(items);
      const shownGroups = memPage(groups);
      const candidateRow = c => deps.row(c.id, "minmax(0,1fr) auto", [
        memCell(c, [deps.kindLabel(c.kind), deps.riskLabel(c), deps.withAgent(c), deps.ago(c.created_at), deps.memRepo(c).split("/").pop()]),
        deps.el("span", "mem-badge", "확인하기")
      ], deps.renderDetail);
      drawRows(shownGroups, group => candidateGroup(group, { el: deps.el, button: deps.button, row: candidateRow, request: deps.request, query: deps.query }));
      if (!groups.length) table.append(deps.el("div", "empty", deps.memory.query ? "검색과 맞는 기억이 없어요." : "확인할 기억이 없어요."));
      memMore(groups.length, shownGroups.length);
      return;
    }
    const shown = memPage(items);
    if (deps.memory.memTab === "provenance") {
      drawRows(shown, m => deps.row(m.id, "minmax(0,1fr) auto", [memCell(m, [deps.kindLabel(m.kind), deps.savedHow(m)]), deps.el("span", "mem-badge warn", "승인 경로 확인")], deps.renderDetail));
    } else if (deps.memory.memTab === "idle") {
      const w = deps.memUse();
      drawRows(shown, (m) => {
        const s = deps.memStat(m.id), often = deps.overlapReview(m);
        const due = deps.reviewOverdue(m);
        const use = due ? "재검토 예정일이 지났어요" : often && w ? `최근 ${w.days}일 ${s.injected}번 전달 · 문구 겹침 ${s.used}번` : "30일 넘게 참고 안 됨";
        return deps.row(m.id, "minmax(0,1fr) auto", [memCell(m, [deps.kindLabel(m.kind), use]), deps.el("span", `mem-badge${often ? " warn" : ""}`, due ? "재검토 예정일 지남" : often ? "문구 겹침 미확인" : "최근 호출 없음")], deps.renderDetail);
      });
    } else {
      drawRows(shown, c => {
        const [why, warn] = deps.waitReason(c);
        return deps.row(c.id, "minmax(0,1fr) auto", [memCell(c, [deps.kindLabel(c.kind), deps.riskLabel(c), deps.withAgent(c), deps.ago(c.created_at), deps.memRepo(c).split("/").pop()]), deps.el("span", `mem-badge${warn ? " warn" : ""}`, why.split(" — ")[0])], deps.renderDetail);
      });
    }
    if (!items.length) table.append(deps.el("div", "empty", deps.memory.query ? "검색과 맞는 기억이 없어요." : "확인할 기억이 없어요."));
    memMore(items.length, shown.length);
  }


  // 30일 지난 후보 정리: the server's own candidate expiry (POST /v1/memory_candidates/expire), at most 100 at a time.
  const STALE_DAYS = 30;

  function staleCandidates() {
    const before = Date.now() - STALE_DAYS * deps.DAY;
    return deps.state.data.candidates.filter((c) => !c.conflict_id && new Date(c.created_at).getTime() <= before && new Date(c.updated_at || c.created_at).getTime() <= before);
  }

  async function expireStale(n, trigger) {
    if (!await ask(`불러온 목록에 오래된 후보 ${n}개가 있어요. 서버에서 ${STALE_DAYS}일 넘게 결정하지 않은 후보를 최대 100개 정리할까요?\n\n저장되지 않고 만료돼요. 에이전트는 쓰지 않아요.`)) return;
    trigger.disabled = true;
    try {
      const before = new Date(Date.now() - STALE_DAYS * deps.DAY).toISOString();
      const r = await deps.request("/v1/memory_candidates/expire", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, before, limit: 100 }) });
      const done = r?.expired?.length ?? 0;
      deps.notice(done ? `후보 ${done}개를 정리했어요.` : "정리할 후보가 없었어요.");
      deps.state.selected = null;
      await reloadMemories();
    } catch (e) { deps.failNotice(e, "이 계정은 후보를 정리할 수 없어요.", `정리하지 못했어요 (${e.message}).`); trigger.disabled = false; }
  }

  async function reloadMemories() {
    const g = deps.generation;
    const [candidates, conflicted, memories, maintenance] = await Promise.all([
      deps.collectionPaging.read("candidates", true), deps.collectionPaging.read("conflicted", true).catch(() => deps.state.data.conflicted), deps.memoryPages.refresh(deps.memoryKey()),
      deps.loadMaintenance()
    ]);
    if (g !== deps.generation) return;
    Object.assign(deps.state.data, { candidates, conflicted, memories, maintenance });
    deps.memoryResults.reset();
    await deps.catalogQuery.refreshVisible();
    void deps.hydrateMaintenance(deps.generation);
    deps.render();
  }

  // Conflict resolution and candidate review commit together on the server.
  const CONFLICT_REASON = "Arkism 앱에서 사용자가 선택";

  // One review action per item at a time: a second click on the same item would only meet the first one's new revision.
  const reviewing = new Set();

  async function reviewOnce(id, work) {
    if (reviewing.has(id)) return;
    reviewing.add(id);
    try { await work(); } catch (e) {
      if (e.message === "revision_conflict") deps.stateNotice("changed", {}, { text: "이미 처리됐거나 다른 곳에서 바뀌었어요. 새로 불러왔어요." });
      else deps.notice(`처리하지 못했어요: ${e.message}`);
      await reloadMemories().catch(() => {});
    } finally { reviewing.delete(id); }
  }

  function resolveConflict(c, choice) {
    return reviewOnce(c.id, async () => {
      const rel = await deps.request(`/v1/memory_relations/${encodeURIComponent(c.conflict_id)}?${deps.query()}`);
      const memory = rel.relations?.find((r) => r.collection === "memories");
      if (!memory) throw new Error("revision_conflict");
      const payload = { scope: deps.state.scope, expected_revision: c.revision,
        action: choice === "keep_old" || choice === "drop" ? "reject" : "approve",
        ...(choice === "keep_old" || choice === "drop" ? { reason: CONFLICT_REASON } : {}),
        conflict: { id: rel.id, expected_revision: rel.revision, memory,
          resolution: { keep_old: "keep_old", drop: "keep_old", both: "keep_both", new: "replace_old" }[choice], reason: CONFLICT_REASON } };
      await deps.request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/review`, {
        method: "POST", body: JSON.stringify(window.ArkismMemoryEvaluation.reviewPayload(c.id, payload)) });
      if (choice === "drop") {
        // Resolving the conflict moved the old memory exactly one revision; anyone else's edit after that makes the delete refuse.
        // The review already committed, so a failed delete must say the old memory is still there.
        try {
          await deps.request(`/v1/memories/${encodeURIComponent(memory.id)}`, { method: "DELETE", body: JSON.stringify({ scope: deps.state.scope, expected_revision: memory.revision + 1 }) });
        } catch {
          deps.notice("고친 내용은 버렸지만 지금 기억은 지우지 못했어요. 기억 목록에서 직접 지워 주세요.");
          deps.state.selected = null;
          await reloadMemories().catch(() => {});
          return;
        }
      }
      deps.notice({ keep_old: "기존 기억을 그대로 뒀어요.", drop: "고친 내용과 지금 기억을 모두 지웠어요.", both: "둘 다 기억으로 남겼어요.", new: "새 내용으로 바꿨어요. 옛 기억은 이력에 남아요." }[choice]);
      deps.state.selected = null;
      await reloadMemories();
    });
  }

  function decide(item, action, reason) {
    return reviewOnce(item.id, async () => {
      await deps.request(`/v1/memory_candidates/${encodeURIComponent(item.id)}/review`, {
        method: "POST", body: JSON.stringify(window.ArkismMemoryEvaluation.reviewPayload(item.id, { scope: deps.state.scope, expected_revision: item.revision, action, ...(reason ? { reason } : {}) }))
      });
      deps.notice(action === "approve" ? "기억으로 저장했어요." : "후보를 버렸어요.");
      deps.state.selected = null;
      await reloadMemories();
    });
  }

  return {
    get decide() { return decide; },
    get memSelect() { return memSelect; },
    get memTitle() { return memTitle; },
    get reloadMemories() { return reloadMemories; },
    get renderMemories() { return renderMemories; },
    get resolveConflict() { return resolveConflict; },
    get reviewOnce() { return reviewOnce; }
  };
}
