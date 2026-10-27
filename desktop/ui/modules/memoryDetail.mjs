// Owns memory details and capture. Dependencies are explicit live ports, never a global scope.
export function createMemoryDetail(deps) {

  async function memoryDetail(m) {
    const body = deps.detailShell("기억");
    const u = deps.usageOf(m.id), s = deps.memStat(m.id);
    deps.detailHead(body, deps.kindLabel(m.kind), m.text, `${deps.originOf(m)} · ${deps.shortDate(m.created_at)} 저장 · ${deps.memRepo(m) ? `${deps.memRepo(m).split("/").pop()} 저장소에서만` : "모든 에이전트"}`);
    const contextPanel = deps.renderMemoryContext(m);
    if (contextPanel) body.append(contextPanel);
    const modelPanel = deps.renderModelGeneration(m);
    if (modelPanel) body.append(modelPanel);
    const fix = () => deps.button("고치기", "small", null, () => openMemoryFix(m));
    if (deps.overlapReview(m)) {
      body.append(deps.actionCard(`최근 ${deps.memUse().days}일 동안 에이전트에게 ${s.injected}번 건넸는데 답변에 이 문구가 보인 적이 없어요. 말없이 따른 경우는 셀 수 없어서 실제로 쓰였는지는 몰라요. 아직 맞는 내용인가요?`,
        deps.button("맞아요, 유지", "primary small", null, () => {
          try { localStorage.setItem(deps.KEEP_KEY, JSON.stringify([...deps.keptMemories(), m.id])); } catch (_) { /* private window */ }
          deps.notice("유지할게요. 이 기억은 다시 묻지 않아요.");
          deps.render(false);
          deps.renderDetail();
        }), fix(), deps.button("잊기", "ghost small", null, () => forgetMemory(m))));
    } else if (deps.reviewOverdue(m)) body.append(deps.actionCard("재검토 예정일이 지났어요. 내용이 아직 맞는지 직접 확인해 주세요."));
    else if (deps.idle(m)) body.append(deps.actionCard("최근 30일 동안 검색으로 전달된 기록이 없어요. 실제 활용 여부와 내용의 필요성은 따로 확인해 주세요.", ...(deps.state.caps?.actions?.propose ? [fix()] : []), deps.button("잊기", "ghost small", null, () => forgetMemory(m))));
    else if (deps.state.caps?.actions?.propose) body.append(deps.button("고치기", "small", "pencil-simple", () => openMemoryFix(m)));
    const why = deps.section("어디서 왔나요");
    const quoteBox = deps.el("div", "quote");
    quoteBox.append(deps.el("div", "", "근거를 불러오는 중…"));
    why.append(quoteBox);
    body.append(why);
    const use = deps.section("언제 쓰이나요", u?.last ? `마지막 ${deps.ago(u.last)}` : "");
    const uses = deps.standing(m) ? "대화를 시작할 때마다 들어가서 따로 세지 않아요"
      : s && deps.memUse() ? `${s.injected}번 전달 · 문구 겹침 ${s.used}번` : u ? `${u.label7}번 참고 · 30일 ${u.d30}번` : "불러오는 중…";
    use.append(deps.facts([
      ["들어가는 때", deps.standing(m) ? "대화를 시작할 때마다" : "질문과 관련 있을 때만"],
      ["쓰는 곳", m.scope?.agent_id ? `에이전트 ${m.scope.agent_id}` : "모든 에이전트 · 내 기억"],
      [deps.standing(m) ? "횟수" : `최근 ${deps.memUse()?.days || 7}일`, uses],
      ["저장 방식", deps.savedHow(m)]
    ]));
    if (!deps.standing(m) && u) use.append(deps.el("p", "meta", "질문에 자료로 들어간 횟수예요. 최근 30일만 세요."));
    body.append(use);
    const management = deps.section("기억 관리");
    const approval = window.ArkismMemoryManagement.approval(m);
    management.append(deps.el("p", "", approval.label), deps.el("p", "meta", approval.detail));
    if (m.conflict_id) management.append(deps.el("p", "warn-text", "정정 또는 충돌 검토가 대기 중이에요. 기존 기억이 계속 전달될 수 있으니 먼저 확인해 주세요."));
    management.append(deps.facts([["다음 재검토", m.review_due_at ? deps.shortDate(m.review_due_at) : "예정일 없음"], ["사용 종료", m.expires_at ? deps.shortDate(m.expires_at) : "만료일 없음"]]));
    management.append(deps.el("p", "meta", "재검토 예정일은 삭제·자동 승인·보관 연장과 별개예요."));
    const originalStatus = deps.el("p", "meta", "원본 보관 상태는 별도로 확인할 수 있어요.");
    management.append(originalStatus, deps.button("원본 보관 확인", "ghost small", null, async event => {
      const trigger = event.currentTarget; trigger.disabled = true;
      try {
        const result = await deps.request(`/v1/memories/${encodeURIComponent(m.id)}/management-status?${deps.query()}`);
        originalStatus.textContent = `원본 보관 중 ${result.originals_available}개 · 만료 ${result.originals_expired}개 · 확인 불가 ${result.originals_unknown}개${result.complete ? "" : " · 일부만 확인"}. 원본 만료와 장기 기억의 유효성은 별개예요.`;
      } catch (_) { originalStatus.textContent = "원본 상태를 확인하지 못했어요. 보관 중이라고 간주하지 않아요."; }
      finally { trigger.disabled = false; }
    }));
    if (deps.state.caps?.actions?.review_decide && !m.conflict_id) {
      const field = deps.el("label", "field", "재검토 예정일");
      const date = deps.el("input"); date.type = "date"; date.setAttribute("aria-label", "재검토 예정일");
      date.addEventListener("input", () => date.setCustomValidity(""));
      if (m.review_due_at) date.value = m.review_due_at.slice(0, 10);
      field.append(date); management.append(field);
      const saveDue = value => deps.reviewOnce(`due:${m.id}`, async () => {
        try {
          await deps.request(`/v1/memories/${encodeURIComponent(m.id)}/review-due`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: m.revision, review_due_at: value }) });
          deps.notice("재검토 예정일을 변경했어요."); await deps.reloadMemories();
        } catch (error) { deps.notice(`변경하지 못했어요 (${error.message}).`); }
      });
      management.append(deps.button("예정일 저장", "small", null, () => {
        const when = new Date(`${date.value}T23:59:59`);
        if (!date.value || !Number.isFinite(when.getTime()) || when.getTime() <= Date.now()) { date.setCustomValidity("앞으로의 날짜를 선택해 주세요."); date.reportValidity(); return; }
        date.setCustomValidity(""); void saveDue(when.toISOString());
      }));
      if (m.review_due_at) management.append(deps.button("예정일 해제", "ghost small", null, () => saveDue(null)));
    }
    body.append(management);
    window.ArkismMemoryEvaluation.mount({ container: body, record: m, scope: { ...deps.state.scope },
      request: deps.request, el: deps.el, section: deps.section, facts: deps.facts, button: deps.button });
    // 변경 이력 N건 › (folded) · 이전으로 되돌리기 (Figma 198:630): the lineage walk fills both once it answers.
    const hist = deps.el("details", "d-history");
    const histHead = deps.el("summary", "", "변경 이력 1건");
    const list = deps.el("ul", "plain history");
    list.append(historyItem(m.created_at, "저장", deps.tr(deps.withAgent(m))));
    const histRow = deps.el("div", "d-history-row");
    histRow.append(hist);
    hist.append(histHead, list);
    body.append(histRow);
    body.append(...deps.privacy.memorySections(m));
    const g = deps.generation;
    const [sources, lineage] = await Promise.all([
      deps.request(`/v1/memories/${encodeURIComponent(m.id)}/sources?${deps.query()}`).catch(() => null),
      deps.request(`/v1/memories/${encodeURIComponent(m.id)}/lineage?${deps.query()}`).catch(() => null)
    ]);
    if (g !== deps.generation || deps.state.selected !== m.id) return;
    deps.originals.sourceDetails(quoteBox, m, sources);
    for (const link of lineage?.links || []) {
      if (link.relation === "proposal") continue;
      const label = { supersedes: "옛 기억을 대체함", superseded_by: "새 기억으로 바뀜", conflict: "다른 기억과 부딪힘" }[link.relation] || link.relation;
      list.prepend(historyItem(link.record?.updated_at || link.record?.created_at, label, link.record?.text ? `“${link.record.text}”` : ""));
    }
    histHead.textContent = `변경 이력 ${list.children.length}건`;
    // The newest memory this one replaced; going back saves its sentence again the same way 고치기 does.
    const prev = (lineage?.links || []).filter((l) => l.relation === "supersedes" && l.record?.text)
      .sort((a, b) => new Date(b.record.updated_at || b.record.created_at || 0) - new Date(a.record.updated_at || a.record.created_at || 0))[0];
    if (prev && m.status === "active" && deps.state.caps?.actions?.propose) {
      const back = deps.el("button", "link-btn d-revert", "이전으로 되돌리기"); back.type = "button";
      back.addEventListener("click", () => revertMemory(m, prev.record.text, back));
      histRow.append(back);
    }
  }

  async function finishMemoryCapture(item, savedMessage, pendingMessage) {
    if (["accepted", "approved"].includes(item.status) || item.memory_id) {
      deps.notice(savedMessage);
    } else if (item.status === "merged") {
      deps.notice("이미 수집한 내용이에요. 기억 목록에서 상태를 확인하세요.");
    } else {
      deps.notice(pendingMessage);
    }
    deps.state.selected = null;
    await deps.reloadMemories();
  }

  async function revertMemory(m, text, trigger) {
    if (!await ask(`이전 내용으로 되돌릴까요?\n\n지금: “${m.text}”\n이전: “${text}”\n\n지금 내용은 변경 이력에 남아요.`)) return;
    trigger.disabled = true;
    try {
      const item = await captureMemory(text, m.kind, m, "Arkism 앱에서 사용자가 이전 내용으로 되돌림");
      await finishMemoryCapture(item, "이전 내용으로 되돌렸어요.", "검토 대기에 들어갔어요. 기억 › 확인해 주세요에서 저장하면 되돌려져요.");
    } catch (e) { deps.notice(`되돌리지 못했어요 (${e.message}).`); trigger.disabled = false; }
  }

  async function forgetMemory(m) {
    if (!await ask(`이 기억을 잊을까요?\n\n“${m.text}”\n\n모든 에이전트가 더 이상 쓰지 않아요.`)) return;
    try {
      await deps.request(`/v1/memories/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: deps.state.scope, expected_revision: m.revision }) });
      deps.notice("잊었어요.");
      deps.state.selected = null;
      await deps.reloadMemories();
    } catch (e) { deps.failNotice(e, "이 계정은 기억을 지울 수 없어요.", `잊지 못했어요 (${e.message}).`); }
  }

  // 직접 추가·고치기 go through the same path as a sentence the user said in a chat (explicit_remember):
  // the server applies its own rules, and a fix names the memory it replaces.
  async function captureMemory(text, kind, replaces, why) {
    const claim = { subject: "general", text, kind, basis: "explicit_user", reason: why || (replaces ? "Arkism 앱에서 사용자가 고침" : "Arkism 앱에서 사용자가 직접 추가") };
    if (replaces) claim.supersedes = { collection: "memories", id: replaces.id, revision: replaces.revision };
    const ev = { id: `desktop:${Date.now().toString(36)}:${crypto.randomUUID().slice(0, 8)}`, type: replaces ? "user_corrected_fact" : "explicit_remember",
      origin_product: "arkism_desktop", occurred_at: new Date().toISOString() };
    const r = await deps.request("/v1/capture", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, generator: { model: "user", prompt_version: "desktop-v1", pipeline_version: "arkism-desktop" }, claims: [claim], event: ev }) });
    const item = (r.items || [])[0] || {};
    if (item.status === "rejected") throw new Error(item.reason === "secret_detected" ? "비밀값이 들어 있어요" : item.reason || "저장 거절");
    return item;
  }

  function openMemoryAdd() { openMemoryFix(null); }

  // 고쳐서 저장: the corrected sentence is saved as the user's own, and the candidate is set aside with that reason.
  async function openCandidateFix(c) {
    const text = await askText("고친 문장을 쓰세요. 내가 직접 말한 기억으로 저장돼요.", c.text);
    if (!text?.trim() || text.trim() === c.text) return;
    void (async () => {
      try {  // edit_approve keeps the candidate's sources and kind
        await deps.request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/review`, {
          method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: c.revision, action: "edit_approve", content: text.trim() })
        });
        deps.notice("고쳐서 저장했어요.");
        deps.state.selected = null;
        await deps.reloadMemories();
      } catch (e) { deps.notice(`저장하지 못했어요 (${e.message}).`); }
    })();
  }

  // 고치기 (m) or 직접 추가 (no m).
  function openMemoryFix(m) {
    const dlg = deps.el("dialog", "crew-dialog");
    const form = deps.el("form");
    form.method = "dialog";
    const head = deps.el("div", "dialog-head");
    const t = deps.el("div", "");
    t.append(deps.el("h2", "", m ? "기억 고치기" : "기억 직접 추가"), deps.el("p", "", m ? "고친 문장을 검토 대기에 넣어요. 검토 후 새 기억으로 승인할 수 있어요." : "검토 대기에 추가해요. 승인한 뒤 모든 에이전트가 자료로 써요."));
    head.append(t);
    const ta = deps.el("textarea");
    ta.rows = 4;
    ta.value = m?.text || "";
    ta.maxLength = 2000;
    ta.placeholder = "보고는 결론부터 짧게 쓴다.";
    const f = deps.el("label", "field", "기억");
    f.append(ta);
    const kind = deps.el("select");
    for (const [k, l] of [["user", "선호"], ["procedural", "작업 규칙"], ["decision", "결정"], ["lesson", "교훈"]]) { const o = deps.el("option", "", l); o.value = k; o.selected = k === (m?.kind || "procedural"); kind.append(o); }
    kind.disabled = !!m;
    const kf = deps.el("label", "field", "종류");
    kf.append(kind);
    const body = deps.el("div", "dialog-body");
    body.append(f, kf);
    const foot = deps.el("div", "dialog-foot");
    const cancel = deps.button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = deps.button("저장", "primary");
    ok.type = "submit";
    ok.value = "ok";
    foot.append(cancel, ok);
    form.append(head, body, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "ok" || !ta.value.trim() || ta.value.trim() === m?.text) return;
      e.preventDefault();
      ok.disabled = true;
      try {
        const item = await captureMemory(ta.value.trim(), m?.kind || kind.value, m);
        dlg.close();
        await finishMemoryCapture(item, m ? "고쳤어요. 옛 기억은 바뀐 이력에 남아요." : "저장했어요.",
          "검토 대기에 들어갔어요. 기억 › 검토 대기에서 승인하면 저장돼요.");
      } catch (error_) { deps.notice(`저장하지 못했어요 (${error_.message}).`); ok.disabled = false; }
    });
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }

  function historyItem(when, title, desc) {
    const li = deps.el("li", "");
    const t = deps.el("time", "", deps.shortDate(when));
    const c = deps.el("div", "");
    c.append(deps.el("strong", "", title), deps.raw(deps.el("span", "", desc || ""))); // desc: 스킬 설명·기억 본문 같은 데이터
    li.append(t, c);
    return li;
  }

  // Figma 198:716 (새 후보) · 198:1748 (헷갈림): kind · title · meta → action card → 어디서 왔나요 → 자동 저장 규칙.
  async function candidateDetail(c) {
    const body = deps.detailShell("기억 후보");
    const contextPanel = deps.renderMemoryContext(c);
    if (contextPanel) body.append(contextPanel);
    const modelPanel = deps.renderModelGeneration(c);
    if (modelPanel) body.append(modelPanel);
    const [why] = deps.waitReason(c);
    const originalEvidence = deps.section("실제 근거와 원문");
    originalEvidence.append(deps.el("p", "meta", "AI의 추출 이유와 실제 원문은 별개예요. 원문을 확인하고 승인해 주세요."));
    const sourceBox = deps.el("div", ""); originalEvidence.append(sourceBox);
    const readSources = deps.button("근거 확인", "ghost small", null, async event => {
      const trigger = event.currentTarget; trigger.disabled = true;
      try {
        const response = await deps.request(`/v1/memory_candidates/${encodeURIComponent(c.id)}/sources?${deps.query()}`);
        if (sourceBox.isConnected && deps.state.selected === c.id) deps.originals.sourceDetails(sourceBox, c, response);
      } catch (_) { sourceBox.replaceChildren(deps.el("p", "meta", "원문 근거를 확인하지 못했어요. 확인된 내용으로 간주하지 않아요.")); }
      finally { trigger.disabled = false; }
    });
    originalEvidence.append(readSources); body.append(originalEvidence);
    const rules = deps.el("details", "d-rules");
    rules.append(deps.el("summary", "", "기억 검토 규칙 보기"), deps.facts([["검토 대기", "새로 수집한 모든 기억"], ["AI 역할", "위험도 분류와 근거 정리"], ["사람 결정", "승인·고쳐서 승인·거절"]]));
    const reasonSection = () => {
      if (!c.summary) return null;
      const s = deps.section("AI가 제안한 이유");
      const q = deps.el("div", "quote");
      q.append(deps.raw(deps.el("div", "", c.summary)), deps.el("footer", "", deps.withAgent(c)));
      s.append(q);
      return s;
    };
    const instructionLike = (c.tags || []).includes("review:instruction_like");
    const canDecide = !!deps.state.caps?.actions?.review_decide;
    if (c.conflict_id) {
      // 헷갈림: 지금 기억 → 새로 들어온 내용 → 이유 → 고르기.
      const correction = deps.isCorrection(c);
      if (correction) deps.detailHead(body, `${deps.kindLabel(c.kind)} · 정정 대기`, "기존 기억을 고친 내용이 들어왔어요", "고를 때까지 에이전트는 지금 기억을 그대로 써요.");
      else deps.detailHead(body, `${deps.kindLabel(c.kind)} · 내용이 달라요`, "기존 기억과 내용이 달라요", "어느 쪽이 맞나요?");
      if (instructionLike) body.append(deps.actionCard("AI에게 명령하는 문장처럼 보여요. 붙여 넣은 글이나 웹 문서에서 섞여 들어온 문장일 수 있어요."));
      const cmp = deps.section("지금 기억");
      const oldBox = deps.el("div", correction ? "quote diff-del" : "quote");
      oldBox.append(deps.el("div", "meta", "불러오는 중…"));
      cmp.append(oldBox);
      const fresh = deps.section(correction ? "고친 내용" : "새로 들어온 내용");
      const newBox = deps.el("div", correction ? "quote diff-add" : "quote");
      const foot = deps.el("footer", "");
      foot.append(`${deps.ago(c.created_at)} · ${deps.withAgent(c)}`);
      newBox.append(deps.raw(deps.el("div", "", `“${c.text}”`)), foot);
      fresh.append(newBox);
      const parts = [cmp, fresh];
      if (c.summary) parts.push(deps.raw(deps.el("p", "meta", c.summary)));
      if (canDecide) {
        const act = deps.el("div", "d-buttons");
        act.append(deps.button("새 내용으로 바꾸기", "primary small", null, () => deps.resolveConflict(c, "new")),
          deps.button("지금 기억 유지", "small", null, () => deps.resolveConflict(c, "keep_old")),
          correction ? deps.button("둘 다 지우기", "ghost small", null, async () => { if (await ask("고친 내용과 지금 기억을 모두 지울까요?")) void deps.resolveConflict(c, "drop"); })
            : deps.button("둘 다 맞아요", "ghost small", null, () => deps.resolveConflict(c, "both")));
        parts.push(deps.el("p", "meta", "선택한 내용과 기존 기억의 변경을 함께 저장해요. 응답을 못 받은 경우 같은 선택으로 다시 시도할 수 있어요."), act);
      }
      body.append(...parts, rules);
      deps.request(`/v1/memory_relations/${encodeURIComponent(c.conflict_id)}?${deps.query()}`)
        .then((rel) => deps.request(`/v1/memories/${encodeURIComponent(rel.relations.find((r) => r.collection === "memories").id)}?${deps.query()}`))
        .then((old) => { const f = deps.el("footer", "", `${deps.shortDate(old.created_at)} 저장 · ${deps.withAgent(old)}`); oldBox.replaceChildren(deps.raw(deps.el("div", "", `“${old.text}”`)), f); })
        .catch(() => oldBox.replaceChildren(deps.el("div", "meta", "기존 기억을 불러오지 못했어요.")));
      return;
    }
    deps.detailHead(body, `${deps.kindLabel(c.kind)} · 새 후보`, c.text, `${deps.withAgent(c)}에서 뽑음 · ${deps.ago(c.created_at)}`);
    const text = instructionLike ? "AI에게 명령하는 문장처럼 보여요. 저장하면 다른 에이전트가 이 말을 따를 수 있어요."
      : `${why}. 저장하면 모든 에이전트가 자료로 써요.`;
    if (canDecide) {
      const reason = deps.el("textarea"); reason.id = "reject-reason"; reason.rows = 2; reason.placeholder = "버리는 이유 (버릴 때만 필요해요)"; reason.setAttribute("aria-label", "버리는 이유");
      const ok = deps.button("그대로 저장", instructionLike ? "ghost small" : "primary small", null, () => { ok.disabled = no.disabled = true; void deps.decide(c, "approve", ""); });
      const fix = deps.button("고쳐서 저장", "small", null, () => openCandidateFix(c));
      const no = deps.button("버리기", instructionLike ? "primary small" : "ghost small", null, () => {
        if (!reason.value.trim()) { window.ArkismNeed(reason, "버리는 이유를 적어 주세요."); return; }
        ok.disabled = no.disabled = true; void deps.decide(c, "reject", reason.value.trim());
      });
      const card = deps.actionCard(text, ...(instructionLike ? [no, fix, ok] : [ok, fix, no]));
      card.insertBefore(reason, card.lastChild);
      if (instructionLike) card.setAttribute("role", "note");
      body.append(card);
    } else body.append(deps.actionCard(text));
    const r = reasonSection();
    if (r) body.append(r);
    body.append(rules);
  }

  return {
    openMemoryFix, revertMemory,
    get candidateDetail() { return candidateDetail; },
    get memoryDetail() { return memoryDetail; },
    get openMemoryAdd() { return openMemoryAdd; }
  };
}
