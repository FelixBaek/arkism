// @ts-check
window.ArkismIntelligence = (ctx, tab) => {
  "use strict";
  tab ||= "reflections";
  const { state, $, el, icon, button, request, query, detailShell, openDetail } = ctx;
  const twin = tab === "traits", edges = tab === "relations", noun = edges ? "관계" : twin ? "특성" : "교훈";
  const title = edges ? "작업 관계" : twin ? "사용자 특성" : "반성 교훈";
  const runs = item => edges ? item.occurrence_count : item.distinct_runs;
  const types = { preference: "선호", working_style: "작업 방식", stable_goal: "목표", constraint: "제약", interaction_pattern: "상호작용 방식" };
  const text = item => edges ? `${item.source} → ${item.target}` : twin ? item.claim : item.interpretation;
  const active = () => state.view === "memories" && state.memTab === tab;
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const labels = edges ? { candidate: "관측됨", inferred: "추정됨", confirmed: "확인됨" } : twin ? { candidate: "검토 대기", inferred: "추정됨", confirmed: "확인됨", superseded: "대체됨", rejected: "제외됨" } : { candidate: "검토 대기", validated: "검증됨", rejected: "제외됨", expired: "만료됨" };
  const loads = globalThis.ArkismKeep.latest();
  let key = "", subject = "", rows = [], selected = null, busy = false, error = "", searched = false;
  let detailHost = null, detailItem = null, detailOwner = "";
  function reset() { detailHost = null; loads.cancel(); key = ""; subject = ""; rows = []; selected = null; busy = false; error = ""; searched = false; }
  const evidenceCollections = new Set(["agent_runs", "execution_traces", "evaluations", "reflections", "sources", "memories", "memory_candidates", "tool_calls", "skill_versions"]);
  async function readRecord(collection, id, revision) {
    if (!evidenceCollections.has(collection) || typeof id !== "string" || !id || id.length > 256 || id === "." || id === "..") throw new Error("invalid_reference");
    const value = await request(`/v1/${collection}/${encodeURIComponent(id)}?${query()}`);
    if (value.id !== id || (revision != null && value.revision !== revision) || !Number.isInteger(value.revision) || value.revision < 1 ||
        !["workspace_id", "project_id", "user_id", "agent_id"].every(k => (value.scope?.[k] || "") === (state.scope[k] || "")) ||
        value.content_erased || value.privacy_quarantined || typeof value.text !== "string" || typeof value.status !== "string") throw new Error("unavailable_evidence");
    if (collection === "sources" && (value.status !== "active" || (value.expires_at != null && !(Date.parse(value.expires_at) > Date.now())) || (value.effective_at != null && !(Date.parse(value.effective_at) <= Date.now())))) throw new Error("inactive_source");
    return value;
  }
  function reader(title, fetchContent) {
    const panel = el("details", "original-reader evidence-reader"), summary = el("summary", "", title), content = el("div");
    const owner = identity(), item = selected;
    const reads = globalThis.ArkismKeep.latest();
    panel.append(summary, content);
    panel.addEventListener("toggle", async () => {
      const ticket = reads.begin();
      content.replaceChildren(); content.setAttribute("aria-busy", "false");
      if (!panel.open) return;
      const live = () => panel.isConnected && panel.open && ticket.live() && active() && identity() === owner && selected === item;
      content.setAttribute("aria-busy", "true");
      const notice = el("p", "meta", "근거를 불러오는 중…"); notice.setAttribute("role", "status"); content.append(notice);
      try {
        const nodes = await fetchContent(live);
        if (live()) content.replaceChildren(...nodes);
      } catch (_) {
        if (live()) { notice.textContent = "근거를 읽을 수 없어요. 권한·삭제·기록 변경 여부를 확인하고 접었다 다시 펼쳐 주세요."; content.replaceChildren(notice); }
      } finally { if (live()) content.setAttribute("aria-busy", "false"); }
    });
    return panel;
  }
  function evidenceReader(ref) {
    return reader(`${ref.collection} · ${ref.id} · 판 ${ref.revision}`, async () => {
      const value = await readRecord(ref.collection, ref.id, ref.revision);
      if (!Array.isArray(value.sources) || value.sources.length > 64) throw new Error("invalid_sources");
      const nodes = [el("p", "original-text", value.text || "저장된 본문이 없어요."), el("p", "meta", `기록 상태 · ${value.status}`)];
      if (value.run) nodes.push(el("p", "meta", `작업 · ${value.run.task_type || "미기록"} / 결과 · ${value.run.outcome || "미기록"}`));
      for (const source of value.sources) {
        nodes.push(reader(`연결 출처 · ${source.source_id}`, async live => {
          const current = await readRecord("sources", source.source_id);
          let excerpt = current.text;
          if (source.span) {
            const { start, end, content_hash: hash } = source.span, points = Array.from(current.text);
            if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end <= start || end > points.length || end > 64000) throw new Error("invalid_span");
            const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(current.text))), byte => byte.toString(16).padStart(2, "0")).join("");
            if (digest !== hash) throw new Error("source_changed");
            excerpt = points.slice(start, end).join("");
          }
          if (!live()) throw new Error("stale_reader");
          await readRecord(ref.collection, ref.id, ref.revision);
          return [el("p", "meta", source.span ? "기록된 원문 구간" : "당시 본문 위치가 없어 현재 출처를 표시해요."), el("p", "original-text", excerpt)];
        }));
      }
      return nodes;
    });
  }
  function confirmRelation(body, item) {
    if (item.status === "confirmed" || state.caps?.role !== "human") return;
    const owner = identity(), id = `${item.source}|${item.relation_type}|${item.target}`;
    const note = el("p", "meta", "근거를 검토한 뒤 확인 조건을 검사하세요. 확인해도 자동 실행되지는 않아요.");
    note.setAttribute("role", "status");
    const reasons = { insufficient_distinct_runs: "서로 다른 실행 3건 이상이 필요해요.", low_confidence: "관측 횟수 기반 점수가 기준보다 낮아요.", not_promotable_status: "현재 상태에서는 확인할 수 없어요. 목록을 다시 조회해 주세요." };
    const check = button("확인 조건 검사", "small", null, () => submit(true));
    const confirm = button("관계 확인", "primary small", null, () => submit(false)); confirm.hidden = true;
    const live = () => check.isConnected && active() && identity() === owner && selected === item;
    async function submit(dryRun) {
      check.disabled = confirm.disabled = true; confirm.hidden = true;
      try {
        const result = await request("/v1/improvements", { method: "POST", body: JSON.stringify({ scope: state.scope, target: "tacit_relation", id, dry_run: dryRun }) });
        if (!live()) return;
        if (result.target !== "tacit_relation" || result.id !== id || typeof result.promotable !== "boolean" || typeof result.promoted !== "boolean" || (dryRun && result.promoted)) throw new Error("invalid_response");
        if (!dryRun && result.promoted) { ctx.notice("관계를 확인했어요."); await load(); return; }
        note.textContent = result.promotable && dryRun ? "확인 조건을 충족했어요. 이 관계를 확인하려면 ‘관계 확인’을 눌러 주세요." : reasons[result.reason] || "확인되지 않았어요. 목록을 다시 조회해 주세요.";
        confirm.hidden = !(dryRun && result.promotable);
      } catch (_) { if (live()) note.textContent = "처리 결과를 확인하지 못했어요. 권한이나 근거가 바뀌었을 수 있으니 목록을 다시 조회해 주세요."; }
      finally { if (live()) check.disabled = confirm.disabled = false; }
    }
    const actions = el("div", "actions"); actions.append(check, confirm);
    body.append(note, actions);
  }
  function reviewLink(body, item) {
    if (edges) { confirmRelation(body, item); return; }
    const memory = twin && item.status === "confirmed";
    if (!(memory || item.status === "candidate" || (twin && item.status === "inferred"))) return;
    const owner = identity(), collection = memory ? "memories" : "memory_candidates";
    const note = el("p", "meta", twin ? "내용을 확인한 뒤 기존 화면에서 검토하거나 고칠 수 있어요." : "여러 관측을 묶은 교훈이에요. 대표 후보 하나만 검토하며, 묶음 전체를 승인하지 않아요.");
    note.setAttribute("role", "status");
    const action = button(memory ? "기억 확인·정정" : twin ? "후보 검토" : "대표 후보 검토", "small", null, async () => {
      action.disabled = true;
      const live = () => action.isConnected && active() && identity() === owner && selected === item;
      try {
        const record = await readRecord(collection, item.id, twin ? item.revision : undefined);
        if (!live()) return;
        if (record.status !== (memory ? "active" : "candidate") || record.text !== text(item) || record.kind !== (twin ? "user" : "lesson")) throw new Error("changed_record");
        ctx.openRecord(record);
      } catch (_) {
        if (live()) note.textContent = "기록을 열 수 없어요. 권한이나 내용이 바뀌었을 수 있으니 목록을 다시 조회해 주세요.";
      } finally { if (live()) action.disabled = false; }
    });
    body.append(note, action);
  }
  function detail() {
    if (selected && detailItem === selected && detailOwner === identity() && detailHost?.isConnected) return;
    const body = detailShell(title);
    detailHost = body; detailItem = selected; detailOwner = identity();
    const lead = edges ? "같은 작업에서 이어서 사용한 스킬의 관측이에요. 인과관계나 실행 허가를 뜻하지 않아요." : twin ? "사용자에 대한 관측과 추정이에요. 현재 지시나 실행 승인을 뜻하지 않아요." : "작업에서 얻은 해석이에요. 사실이나 실행 승인을 뜻하지 않아요.";
    if (!selected) { body.append(el("h2", "", `${noun}을 선택하세요`), el("p", "lead", lead)); return; }
    const head = el("div", "d-head");
    head.append(el("p", "d-kind", `${edges ? "작업 관계" : twin ? types[selected.type] : `교훈 · ${selected.subject}`} · ${labels[selected.status]}`), el("h2", "", text(selected)), el("p", "lead", lead));
    body.append(head);
    const part = (title, ...nodes) => { const s = el("section", "section"); s.append(el("h3", "", title), ...nodes); body.append(s); return s; };
    if (!twin && !edges) part("무슨 일이 있었나요", el("p", "original-text", selected.observation || "기록된 관측 설명이 없어요."));
    const facts = el("dl", "facts");
    const fact = (k, v) => facts.append(el("dt", "", k), el("dd", "", v));
    fact("관측", `독립 ${runs(selected)}회 · ${twin ? "신뢰도" : "점수"} ${Math.round(selected.confidence * 100)}%`);
    if (edges) fact("마지막", new Date(selected.last_seen).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR"));
    if (edges && selected.confirmed_at) fact("확인", new Date(selected.confirmed_at).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR"));
    if (twin && selected.last_confirmed) fact("기억 저장", new Date(selected.last_confirmed).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR"));
    const refs = el("div", "d-refs");
    if (!selected.evidence_refs.length) refs.append(el("p", "meta", "연결된 근거가 없어요."));
    for (const ref of selected.evidence_refs) refs.append(evidenceReader(ref));
    part("근거", facts, el("p", "meta", `${twin ? "기록된 신뢰도" : "관측 횟수 기반 점수"}예요. 정확도를 뜻하지 않아요.`), refs);
    reviewLink(body, selected);
  }
  function paint() {
    const table = $("#table"); table.replaceChildren(); table.setAttribute("aria-busy", String(busy));
    const status = el("p", "meta original-notice", busy ? `${noun}을 불러오는 중…` : error || (!searched ? (edges ? "관계 조회를 눌러 현재 범위의 사용 순서를 확인하세요." : twin ? "특성 조회를 눌러 저장된 관측을 확인하세요." : "확인할 작업 주제를 입력하세요.") : rows.length ? `${rows.length}개 ${noun}` : (edges ? "표시할 작업 관계가 없어요." : twin ? "표시할 사용자 특성이 없어요." : "이 주제에 저장된 교훈이 없어요.")));
    status.setAttribute("role", "status"); table.append(status);
    for (const item of rows) {
      const row = el("button", `original-row${selected === item ? " selected" : ""}`); row.type = "button";
      const body = el("span", "original-body");
      body.append(el("strong", "", text(item)), el("span", "meta", `독립 관측 ${runs(item)}회${edges ? ` · 마지막 ${new Date(item.last_seen).toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" })}` : ""}`));
      row.append(body, el("span", `mem-badge${item.status === "confirmed" || item.status === "validated" ? " strong" : item.status === "candidate" && !edges ? " warn" : ""}`, labels[item.status]));
      row.addEventListener("click", () => { selected = item; paint(); openDetail(); detail(); const heading = $("#detail h2"); heading.tabIndex = -1; heading.focus(); });
      table.append(row);
    }
    if (error) table.append(button("다시 조회", "small", null, load));
    $("#chips button[type=submit]")?.toggleAttribute("disabled", busy);
  }
  async function load(silent = false) {
    const quiet = silent === true;
    if (quiet && !searched) return;
    if (busy || !active() || (!twin && !edges && !subject)) return;
    const ticket = loads.begin(), current = identity();
    let changed = !quiet;
    busy = true;
    if (!quiet) { rows = []; selected = null; error = ""; detail(); paint(); }
    const live = () => active() && ticket.live() && current === identity();
    try {
      const page = await request(edges ? `/v1/tacit/edges?${query()}` : twin ? `/v1/twin/claims?${query()}` : `/v1/reflections?${query({ subject })}`);
      if (!live() || (quiet && ctx.canRefresh?.() === false)) return;
      if (page?.authority !== "data_only_untrusted" || !Array.isArray(page.items) || page.items.some(item =>
        !item || (edges ? (typeof item.source !== "string" || typeof item.target !== "string" || item.source.includes("|") || item.target.includes("|") || item.relation_type !== "often_followed_by" || !Number.isFinite(Date.parse(item.last_seen)) || (item.confirmed_at != null && !Number.isFinite(Date.parse(item.confirmed_at))) || !["workspace_id", "project_id", "user_id", "agent_id"].every(k => (item.scope?.[k] || "") === (state.scope[k] || ""))) : typeof item.id !== "string" || (twin ? (typeof item.claim !== "string" || !Number.isInteger(item.revision) || item.revision < 1 || !Object.hasOwn(types, item.type) || (item.last_confirmed != null && (typeof item.last_confirmed !== "string" || !Number.isFinite(Date.parse(item.last_confirmed))))) : (item.subject !== subject || typeof item.interpretation !== "string" || typeof item.observation !== "string"))) || !Object.hasOwn(labels, item.status) ||
        !Number.isInteger(runs(item)) || runs(item) < 0 || !Number.isFinite(item.confidence) || item.confidence < 0 || item.confidence > 1 ||
        !Array.isArray(item.evidence_refs) || item.evidence_refs.some(ref => !ref || typeof ref.collection !== "string" || typeof ref.id !== "string" || !Number.isInteger(ref.revision)))) throw new Error("invalid_response");
      const next = (twin || edges) ? page.items.filter(item => text(item).toLocaleLowerCase().includes(subject.toLocaleLowerCase())) : page.items;
      if (quiet && JSON.stringify(rows) === JSON.stringify(next)) return;
      if (quiet && selected) selected = next.find(item => edges ? item.source === selected.source && item.target === selected.target : item.id === selected.id) || null;
      rows = next; searched = true; changed = true; error = "";
    } catch (_) {
      if (!live() || quiet) return;
      error = "불러오지 못했어요. 연결과 접근 권한을 확인하고 다시 조회하세요.";
    } finally { if (live()) { busy = false; if (changed) { paint(); if (quiet) detail(); } } }
  }
  function draw() {
    const changed = key !== identity();
    if (changed) { reset(); key = identity(); }
    if (!changed && $("#chips .reflection-form")) { paint(); return; }
    $("#chips").replaceChildren(); $("#stats").replaceChildren();
    const form = el("form", "original-search reflection-form");
    const label = el("label", "panel-search");
    const input = el("input"); input.type = "text"; input.required = !twin && !edges; input.maxLength = 128; input.value = subject;
    input.placeholder = edges ? "스킬 이름으로 찾기" : twin ? "특성 찾기" : "작업 주제 (예: figma)";
    input.setAttribute("aria-label", edges ? "관계 검색 (선택)" : twin ? "특성 검색 (선택)" : "작업 주제");
    label.append(icon("magnifying-glass"), input);
    const submit = button(`${noun} 조회`, "primary small"); submit.type = "submit"; form.append(label, submit);
    form.addEventListener("submit", event => {
      event.preventDefault(); if (busy) return;
      const value = input.value.trim();
      if ((!twin && !edges && !value) || new TextEncoder().encode(value).length > 128) { input.setCustomValidity((twin || edges) ? "검색어를 조금 짧게 줄여주세요." : "주제를 입력하거나 조금 짧게 줄여주세요."); input.reportValidity(); return; }
      subject = value; void load();
    });
    input.addEventListener("input", () => input.setCustomValidity(""));
    $("#chips").append(form); paint();
  }
  function close() { const index = rows.indexOf(selected); selected = null; paint(); detail(); if (index >= 0) $("#table").querySelectorAll(".original-row")[index]?.focus(); }
  return { draw, reset, detail, close, refresh: () => load(true) };
};
