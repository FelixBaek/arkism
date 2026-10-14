// GALLERY 삭제·보관함 (adopt/gallery-delete): prompts (이미지·슬라이드) and design systems.
// 삭제 = DELETE /v1/{prompts|design_systems}/{id} {scope, expected_revision} — gone for good, only after a confirm.
// 보관함 = GET /v1/{col}/archived lists what 보관함에서 빼기 hid; 되살리기 = POST /v1/{col}/{id}/restore.
// 디자인 시스템 list: 선택 → 모두 선택 → 삭제 (one confirm that states the count).
window.ArkismGalleryDelete = (ctx) => {
  "use strict";
  const { state, $, el, button, icon, raw, render, request, query, notice, confirmPop, shortDate } = ctx;
  const COLS = {
    prompts: { key: "prompts", title: (r) => r.image_prompt?.title || "이 프롬프트", also: "삭제하면 되돌릴 수 없어요. 예시 이미지와 스크립트도 함께 지워져요." },
    design_systems: { key: "designSystems", title: (r) => r.design_system?.title || "이 디자인 시스템", also: "삭제하면 되돌릴 수 없어요. 색·글꼴·규칙도 함께 지워져요." }
  };
  const S = { open: null, items: null, loading: false, picking: false, picked: new Set(), busy: false };

  async function reload(col) {
    state.data[COLS[col].key] = (await request(`/v1/${col}?${query()}`)).items || [];
  }
  async function loadArchived(col) {
    S.loading = true;
    try { S.items = (await request(`/v1/${col}/archived?${query()}`)).items || []; } catch (_) { S.items = null; }
    S.loading = false;
  }
  const failText = (err) => ({ revision_conflict: "다른 곳에서 먼저 고쳤어요. 새로 불러온 뒤 다시 해 주세요.", forbidden: "삭제할 권한이 없어요." }[err.message] || `삭제하지 못했어요 (${err.message}).`);
  const del = (col, r) => request(`/v1/${col}/${encodeURIComponent(r.id)}`, { method: "DELETE", body: JSON.stringify({ scope: state.scope, expected_revision: r.revision }) })
    .catch((err) => { if (err.message !== "not_found") throw err; });

  async function remove(col, r) {
    const name = COLS[col].title(r);
    if (!(await confirmPop(`'${name}'을 삭제할까요?`, COLS[col].also, "삭제"))) return;
    try {
      await del(col, r);
      await reload(col);
      if (S.open === col) await loadArchived(col);
      if (state.selected && state.selected.endsWith(":" + r.id)) state.selected = null;
      if (state.promptRun === r.id) state.promptRun = null;
      render(false);
      notice("삭제했어요.");
    } catch (err) { notice(failText(err)); }
  }
  async function removeMany(col, list) {
    if (!list.length || S.busy) return;
    const ask = col === "prompts" ? `프롬프트 ${list.length}개를 삭제할까요?` : `디자인 시스템 ${list.length}개를 삭제할까요?`;
    if (!(await confirmPop(ask, "삭제하면 되돌릴 수 없어요. 고른 항목이 모두 지워져요.", `${list.length}개 삭제`))) return;
    S.busy = true;
    let failed = 0;
    for (const r of list) { try { await del(col, r); } catch (_) { failed++; } }
    S.busy = false;
    S.picking = false; S.picked.clear();
    try { await reload(col); if (S.open === col) await loadArchived(col); } catch (_) { /* the next sync fills it */ }
    state.selected = null;
    render(false);
    notice(failed ? `${list.length - failed}개를 삭제했어요. ${failed}개는 지우지 못했어요.` : `${list.length}개를 삭제했어요.`);
  }
  async function restore(col, r) {
    try {
      await request(`/v1/${col}/${encodeURIComponent(r.id)}/restore`, { method: "POST", body: JSON.stringify({ scope: state.scope, expected_revision: r.revision }) });
      await Promise.all([reload(col), loadArchived(col)]);
      render(false);
      notice("되살렸어요. 목록에 다시 보여요.");
    } catch (err) { notice(`되살리지 못했어요 (${err.message}).`); }
  }

  // The 삭제 button for a detail's action row.
  const deleteButton = (col, r, cls = "small") => button("삭제", `${cls} danger gd-delete`, null, () => remove(col, r));
  // A small trash corner on a card (shown on hover or focus); the card itself is a <button>, so this is a span.
  function cardShortcut(col, r, card) {
    const x = el("span", "gd-card-del");
    x.setAttribute("role", "button");
    x.tabIndex = 0;
    x.setAttribute("aria-label", "삭제");
    x.title = "삭제";
    x.append(icon("trash"));
    const go = (e) => { e.preventDefault(); e.stopPropagation(); void remove(col, r); };
    x.addEventListener("click", go);
    x.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") go(e); });
    card.classList.add("gd-card");
    card.append(x);
    return card;
  }

  // 보관함 toggle: a chip on the 이미지·슬라이드 row, a button on the 디자인 시스템 row.
  const isOpen = (col) => S.open === col;
  function toggle(col) {
    S.open = S.open === col ? null : col;
    S.items = null; S.picking = false; S.picked.clear();
    state.selected = null;
    render(false);
    if (S.open) void loadArchived(col).then(() => { if (S.open === col) render(false); });
  }
  function archiveChip(col) {
    const c = el("button", `chip gd-archive-chip${isOpen(col) ? " active" : ""}`);
    c.type = "button";
    c.append(icon("archive"), "보관함");
    c.addEventListener("click", () => toggle(col));
    return c;
  }
  // The archived list, in place of the cards; kind = "image"|"slide" for prompts.
  function renderArchive(col, kind) {
    ["#stats"].forEach((q) => $(q).replaceChildren());
    const page = el("div", "gd-archive");
    const head = el("div", "gd-archive-head");
    const items = (S.items || []).filter((r) => col !== "prompts" || !kind || r.image_prompt?.kind === kind);
    const h = el("h2", "", "보관함");
    h.append(el("small", "", S.items ? `${items.length}개` : ""));
    head.append(h, el("p", "", "보관함에서 뺀 항목이에요. 되살리면 목록에 다시 보이고, 삭제하면 되돌릴 수 없어요."));
    const bar = el("div", "actions");
    bar.append(button("목록으로", "small", "arrow-left", () => toggle(col)));
    if (items.length) bar.append(button("모두 삭제", "small danger", null, () => removeMany(col, items)));
    head.append(bar);
    page.append(head);
    if (S.loading || !S.items) page.append(el("div", "empty", S.loading ? "불러오는 중…" : "보관함을 불러오지 못했어요."));
    else if (!items.length) page.append(el("div", "empty", "보관함이 비어 있어요."));
    else {
      const list = el("div", "gd-archive-list");
      for (const r of items) {
        const row = el("div", "gd-archive-row");
        const text = el("div", "gd-archive-text");
        const meta = col === "prompts" ? [r.image_prompt?.kind === "slide" ? "슬라이드" : "이미지", r.image_prompt?.engine] : [r.design_system?.product, r.design_system?.purpose];
        text.append(raw(el("strong", "", COLS[col].title(r))), raw(el("span", "", [...meta.filter(Boolean), `${shortDate(r.updated_at)} 보관`].join(" · "))));
        const acts = el("div", "actions");
        acts.append(button("되살리기", "small", null, () => restore(col, r)), deleteButton(col, r));
        row.append(text, acts);
        list.append(row);
      }
      page.append(list);
    }
    $("#table").replaceChildren(page);
  }

  // 디자인 시스템 고르기: 선택 → pick cards → 모두 선택 / 삭제 (n).
  const picking = () => S.picking;
  // Returns true when the click picked the card (so the card does not open its detail).
  function pick(r, card) {
    if (!S.picking || r.builtin) return false;
    if (S.picked.has(r.id)) S.picked.delete(r.id); else S.picked.add(r.id);
    card.classList.toggle("gd-picked", S.picked.has(r.id));
    card.setAttribute("aria-pressed", String(S.picked.has(r.id)));
    paintBulk();
    return true;
  }
  function markCard(r, card) {
    if (!S.picking || r.builtin) return card;
    card.classList.add("gd-pickable");
    card.classList.toggle("gd-picked", S.picked.has(r.id));
    card.setAttribute("aria-pressed", String(S.picked.has(r.id)));
    const box = el("span", "gd-check");
    box.append(icon("check"));
    card.append(box);
    return card;
  }
  let bulkBox = null;
  const deletable = () => (state.data.designSystems || []).filter((r) => !r.builtin);
  function paintBulk() {
    if (!bulkBox) return;
    const all = deletable();
    if (!S.picking) {
      bulkBox.replaceChildren(...(all.length ? [button("선택", "small", "check-circle", () => { S.picking = true; S.picked.clear(); render(false); })] : []));
      return;
    }
    const every = all.length > 0 && all.every((r) => S.picked.has(r.id));
    const chosen = all.filter((r) => S.picked.has(r.id));
    const del = button(chosen.length ? `삭제 (${chosen.length})` : "삭제", "small danger", null, () => removeMany("design_systems", chosen));
    del.disabled = !chosen.length || S.busy;
    bulkBox.replaceChildren(
      button(every ? "선택 풀기" : "모두 선택", "small", null, () => { if (every) S.picked.clear(); else all.forEach((r) => S.picked.add(r.id)); render(false); }),
      del,
      button("취소", "small ghost", null, () => { S.picking = false; S.picked.clear(); render(false); }));
  }
  function bulkBar() {
    bulkBox = el("div", "gd-bulk actions");
    paintBulk();
    return bulkBox;
  }
  function libraryButtons() {
    const b = button("보관함", `small gd-archive-btn${isOpen("design_systems") ? " active" : ""}`, "archive", () => toggle("design_systems"));
    return isOpen("design_systems") ? [b] : [bulkBar(), b];
  }
  // Leaving the gallery closes the 보관함 and the picking.
  function reset() { S.open = null; S.items = null; S.picking = false; S.picked.clear(); }
  return { deleteButton, cardShortcut, archiveChip, isOpen, renderArchive, picking, pick, markCard, libraryButtons, reset, remove, removeMany };
};
