// 개인정보 지우기와 기억 만료일. app.js passes the shared helpers in once.
// Erasure goes through the server's privacy deletion saga (POST /v1/privacy/deletions,
// target_type "source_event"): the source records and everything derived from them
// (memories, candidates, traces) leave every list, search and recall at once, and the
// server finishes the purge in the background. A memory with no source is deleted directly.
window.ArkismPrivacy = (ctx) => {
  "use strict";
  const { state, el, button, request, query, notice, section, afterChange } = ctx;
  const REASON = "user_erasure_request:arkism_desktop";
  const BATCH = 64; // the server's target_ids limit per request
  const SCAN_CAP = 1000;
  const identity = () => JSON.stringify([state.server, state.token, state.scope]);
  const idempotency = () => `desktop:${Date.now().toString(36)}:${crypto.randomUUID()}`;
  const fail = (e) => ({ access_denied: "이 계정은 개인정보를 지울 수 없어요.", conflict: "다른 지우기 요청이 진행 중이거나 그새 바뀌었어요. 잠시 뒤 다시 시도하세요.", not_found: "이미 지워졌거나 찾을 수 없어요." })[e?.message] || `지우지 못했어요 (${e?.message || "오류"}).`;

  function sourceIds(m) {
    const ids = new Set((m.sources || []).map((s) => s.source_id).filter(Boolean));
    if (m.source_event_id) ids.add(m.source_event_id);
    return ids;
  }
  async function scan(path, extra) {
    const items = [];
    let cursor = "";
    do {
      const page = await request(`${path}?${query({ ...extra, limit: 100, ...(cursor ? { cursor } : {}) })}`);
      items.push(...(page.items || []));
      cursor = page.next_cursor || "";
    } while (cursor && items.length < SCAN_CAP);
    return { items, more: Boolean(cursor) };
  }
  // Ask the server to erase these source records (and what came from them), 64 at a time.
  async function eraseSources(ids) {
    const list = [...ids].sort((a, b) => a.localeCompare(b));
    const done = [];
    for (let i = 0; i < list.length; i += BATCH) {
      const r = await request("/v1/privacy/deletions", { method: "POST", body: JSON.stringify({ scope: state.scope, idempotency_key: idempotency(), target_type: "source_event", target_ids: list.slice(i, i + BATCH), reason: REASON }) });
      done.push(r);
    }
    return done;
  }
  async function deleteMemory(m) {
    await request(`/v1/memories/${encodeURIComponent(m.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision }) });
  }

  // ---------------------------------------------------------------- one memory (detail panel)
  async function eraseMemory(m, trigger) {
    const ids = sourceIds(m);
    trigger.disabled = true;
    try {
      const siblings = new Set();
      for (const id of ids) {
        const r = await request(`/v1/sources/${encodeURIComponent(id)}/derived?${query({ collection: "memories", limit: 100 })}`).catch(() => null);
        for (const item of r?.items || []) if (item.id !== m.id) siblings.add(item.id);
      }
      const others = siblings.size;
      const first = ids.size
        ? `이 기억과 근거가 된 원본 기록 ${ids.size}개를 영구히 지울까요?\n\n“${m.text}”${others ? `\n\n같은 원본에서 나온 다른 기억 ${others}개도 함께 지워져요.` : ""}`
        : `이 기억을 영구히 지울까요?\n\n“${m.text}”`;
      if (!confirm(first)) return;
      if (!confirm("정말 지울까요? 모든 에이전트가 더 이상 쓰지 못하고 되돌릴 수 없어요.")) return;
      if (ids.size) await eraseSources(ids);
      else await deleteMemory(m);
      notice("영구히 지웠어요. 남은 정리는 서버가 이어서 해요.");
      state.selected = null;
      await afterChange();
    } catch (e) { notice(fail(e)); } finally { trigger.disabled = false; }
  }

  // ---------------------------------------------------------------- expiry (detail panel)
  const DAY = 86400000;
  const later = (months, days = 0) => { const d = new Date(); d.setMonth(d.getMonth() + months); return new Date(d.getTime() + days * DAY); };
  const fmt = (v) => new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { year: "numeric", month: "long", day: "numeric", hour: "numeric", minute: "2-digit" }).format(new Date(v));
  const dateValue = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 10);
  async function setExpiry(m, at, box) {
    for (const b of box.querySelectorAll("button, input")) b.disabled = true;
    try {
      await request(`/v1/memories/${encodeURIComponent(m.id)}/expiry`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: m.revision, expires_at: at ? at.toISOString() : null }) });
      notice(at ? `${fmt(at)}까지만 써요.` : "만료일을 지웠어요. 계속 써요.");
      await afterChange();
    } catch (e) {
      notice(e.message === "conflict" ? "그새 바뀐 기억이에요. 다시 선택해 주세요." : e.message === "access_denied" ? "이 계정은 만료일을 바꿀 수 없어요." : `만료일을 바꾸지 못했어요 (${e.message}).`);
      for (const b of box.querySelectorAll("button, input")) b.disabled = false;
    }
  }
  function expirySection(m) {
    const s = section("만료일", m.expires_at ? fmt(m.expires_at) : "없음");
    s.append(el("p", "meta", m.expires_at ? "이 시각이 지나면 불러오지도, 검색되지도 않아요." : "정해 두면 그 뒤로는 에이전트가 이 기억을 쓰지 않아요."));
    const box = el("div", "callout-actions expiry-picks");
    const pick = (label, at) => button(label, "small", null, () => setExpiry(m, at(), box));
    box.append(pick("1주", () => later(0, 7)), pick("1개월", () => later(1)), pick("3개월", () => later(3)));
    const date = el("input"); date.type = "date"; date.min = dateValue(new Date(Date.now() + DAY));
    date.setAttribute("aria-label", "만료 날짜");
    const byDate = button("날짜로 정하기", "small", null, () => {
      const at = date.value ? new Date(`${date.value}T00:00`) : null;
      if (!at || !(at > new Date())) { date.setCustomValidity("오늘 이후 날짜를 고르세요."); date.reportValidity(); return; }
      void setExpiry(m, at, box);
    });
    date.addEventListener("input", () => date.setCustomValidity(""));
    box.append(date, byDate);
    if (m.expires_at) box.append(button("만료일 지우기", "ghost small", null, () => setExpiry(m, null, box)));
    s.append(box);
    return s;
  }

  function memorySections(m) {
    const out = [];
    if (m.status === "active") out.push(expirySection(m));
    const danger = section("개인정보 지우기");
    danger.append(el("p", "meta", "이 기억과 근거가 된 원본 기록을 영구히 지워요. 같은 원본에서 나온 다른 기억도 함께 지워져요."));
    const erase = button("영구 삭제", "danger small", "trash", () => eraseMemory(m, erase));
    danger.append(erase);
    out.push(danger);
    return out;
  }

  // ---------------------------------------------------------------- erase by phrase (설정 › 고급)
  let preview = null; // { owner, phrase, memories, sources, ids, more }
  function settingsGroup() {
    const g = el("section", "pg-group");
    g.append(el("h2", "", "개인정보 지우기"), el("p", "pg-hint", "사람 이름이나 메일 주소처럼 지우고 싶은 말을 넣으면, 그 말이 들어간 기억과 원본 기록을 모두 찾아 영구히 지워요. 먼저 몇 개인지 보여 드려요."));
    const form = el("form", "privacy-phrase");
    const label = el("label", "field wide", "지울 말 (이름·메일 주소 등)");
    const input = el("input"); input.type = "search"; input.maxLength = 256; input.required = true; input.placeholder = "예: 홍길동, name@example.com";
    if (preview?.owner === identity()) input.value = preview.phrase;
    label.append(input);
    const find = button("찾아보기", "", null, null); find.type = "submit";
    const status = el("p", "pg-hint"); status.setAttribute("role", "status");
    const actions = el("div", "pg-actions");
    form.append(label, actions);
    actions.append(find);
    g.append(form, status);
    const show = () => {
      for (const b of actions.querySelectorAll(".danger")) b.remove();
      if (!preview || preview.owner !== identity()) { status.textContent = ""; return; }
      const { phrase, memories, sources, ids, more } = preview;
      if (!memories.length && !sources.length) { status.textContent = `“${phrase}”가 들어간 기억이나 원본 기록이 없어요.`; return; }
      status.textContent = `“${phrase}”가 들어간 기억 ${memories.length}개와 원본 기록 ${ids.size}개를 찾았어요.${more ? " 너무 많아 일부만 셌어요. 지운 뒤 다시 찾아보세요." : ""}`;
      const go = button(`모두 영구 삭제 (${memories.length + ids.size})`, "danger", "trash", () => run(go));
      actions.append(go);
    };
    async function look() {
      const phrase = input.value.trim();
      if (!phrase) { input.reportValidity(); return; }
      if (new TextEncoder().encode(phrase).length > 256) { input.setCustomValidity("조금 짧게 입력하세요."); input.reportValidity(); return; }
      if (phrase.length < 2) { input.setCustomValidity("두 글자 이상 입력하세요."); input.reportValidity(); return; }
      const owner = identity();
      find.disabled = true; status.textContent = "찾는 중…"; preview = null; show();
      status.textContent = "찾는 중…";
      try {
        const [mem, src] = await Promise.all([scan("/v1/memories", { q: phrase, include_inactive: "true" }), scan("/v1/sources", { q: phrase })]);
        if (owner !== identity()) return;
        const ids = new Set(src.items.map((s) => s.id));
        for (const m of mem.items) for (const id of sourceIds(m)) ids.add(id);
        preview = { owner, phrase, memories: mem.items, sources: src.items, ids, more: mem.more || src.more };
        show();
      } catch (e) {
        status.textContent = e.message === "access_denied" ? "이 계정은 기억과 원본 기록을 찾을 수 없어요." : "찾지 못했어요. 연결을 확인하고 다시 시도하세요.";
      } finally { find.disabled = false; }
    }
    async function run(go) {
      const p = preview;
      if (!p || p.owner !== identity()) return;
      if (!confirm(`“${p.phrase}”가 들어간 기억 ${p.memories.length}개와 원본 기록 ${p.ids.size}개를 영구히 지울까요?\n\n같은 원본에서 나온 다른 기억도 함께 지워져요.`)) return;
      const typed = prompt(`정말 지우려면 “${p.phrase}”를 그대로 입력하세요. 되돌릴 수 없어요.`);
      if (typed === null) return;
      if (typed.trim() !== p.phrase) { notice("입력한 말이 달라 지우지 않았어요."); return; }
      go.disabled = true; find.disabled = true; status.textContent = "지우는 중…";
      let erased = 0, failed = 0;
      try {
        if (p.ids.size) await eraseSources(p.ids);
        // A memory without a source record is not covered by the source erasure.
        for (const m of p.memories.filter((x) => !sourceIds(x).size)) {
          try { await deleteMemory(m); erased++; } catch (_) { failed++; }
        }
        preview = null;
        notice(failed ? `지웠어요. 기억 ${failed}개는 지우지 못했어요. 다시 찾아보세요.` : "영구히 지웠어요. 남은 정리는 서버가 이어서 해요.");
        status.textContent = failed ? `기억 ${failed}개는 지우지 못했어요.` : `“${p.phrase}”를 지웠어요. 원본 기록 ${p.ids.size}개 · 원본 없는 기억 ${erased}개.`;
        await afterChange().catch(() => {});
      } catch (e) {
        status.textContent = fail(e);
      } finally { go.disabled = false; find.disabled = false; for (const b of actions.querySelectorAll(".danger")) if (!preview) b.remove(); }
    }
    input.addEventListener("input", () => input.setCustomValidity(""));
    form.addEventListener("submit", (event) => { event.preventDefault(); void look(); });
    show();
    return g;
  }

  return { memorySections, settingsGroup };
};
