// 지우기 with a reason: the small picker every memory review surface opens in place (candidate rows, dreaming proposals,
// conflicts, memory detail). Quick reasons plus an optional note; confirm hands {code, note, target} to the caller.
// The codes are the server's removalReasons (internal/memory/removal.go).
(() => {
  "use strict";
  const REASONS = [["stale", "오래된 기억"], ["changed", "이미 바뀜"], ["unused", "사용하지 않음"], ["wrong", "사실과 다름"], ["duplicate", "중복"]];
  const LABEL = Object.fromEntries(REASONS);
  const STALE_DAYS = 10;
  const node = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; };
  const btn = (text, cls, onClick) => { const b = node("button", `btn small ${cls}`.trim(), text); b.type = "button"; b.addEventListener("click", onClick); return b; };
  const time = (v) => { const t = new Date(v || 0).getTime(); return Number.isNaN(t) ? 0 : t; };

  // A row of choices where exactly one is on (aria-pressed); returns the row and a getter.
  function choices(label, items, initial) {
    const row = node("div", "del-reason-chips"); row.setAttribute("role", "group"); row.setAttribute("aria-label", label);
    let cur = initial || "";
    const paint = () => row.querySelectorAll("button").forEach((b) => { const on = b.dataset.value === cur; b.classList.toggle("on", on); b.setAttribute("aria-pressed", String(on)); });
    for (const [value, text] of items) { const b = btn(text, "del-reason-chip", () => { cur = value; paint(); }); b.dataset.value = value; row.append(b); }
    paint();
    return [row, () => cur];
  }

  // targets: [[value, label]] when more than one memory could go (a dreaming pair); onConfirm may be async and
  // return {error} to keep the picker open. 취소 removes the picker and changes nothing.
  function picker({ title = "왜 지우나요?", targets = null, target = "", reason = "", confirmLabel = "지우기", onConfirm, onCancel } = {}) {
    const box = node("div", "del-reason"); box.setAttribute("role", "group"); box.setAttribute("aria-label", title);
    box.append(node("strong", "", title));
    let pickTarget = () => target;
    if (targets?.length > 1) { const [row, get] = choices("무엇을 지울까요", targets, target || targets[0][0]); pickTarget = get; box.append(row); }
    const [codes, pickCode] = choices("지우는 이유", REASONS, reason);
    const note = node("textarea", "del-reason-note"); note.rows = 2; note.placeholder = "메모 (선택)"; note.setAttribute("aria-label", "메모 (선택)");
    const err = node("p", "del-reason-err", ""); err.setAttribute("role", "status");
    const close = () => { box.remove(); onCancel?.(); };
    const cancel = btn("취소", "ghost", close);
    const ok = btn(confirmLabel, "danger", async () => {
      if (!pickCode()) { err.textContent = "이유를 하나 골라 주세요."; codes.querySelector("button")?.focus(); return; }
      ok.disabled = cancel.disabled = true; err.textContent = "";
      try {
        const res = await onConfirm?.({ code: pickCode(), note: note.value.trim(), target: pickTarget() });
        if (res?.error) { err.textContent = res.error; ok.disabled = cancel.disabled = false; } else box.remove();
      } catch (e) { err.textContent = `지우지 못했어요: ${e.message}`; ok.disabled = cancel.disabled = false; }
    });
    const foot = node("div", "del-reason-foot"); foot.append(cancel, ok);
    box.append(codes, note, err, foot);
    box.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.stopPropagation(); e.preventDefault?.(); close(); } });
    return box;
  }

  // Opens the picker right after `anchor` (a button row), replacing one already open there; pressing again closes it.
  function openAfter(anchor, opts) {
    const open = anchor.nextElementSibling;
    if (open?.classList?.contains("del-reason")) { open.remove(); return null; }
    const box = picker(opts);
    anchor.after(box);
    box.querySelector(".del-reason-chips button")?.focus?.();
    return box;
  }

  // "오래된 기억일 수 있어요": the item waited over ten days and a memory it points at changed after it was made.
  // Only a hint; nothing is removed by it.
  function maybeStale(item, related = []) {
    const made = time(item?.created_at);
    if (!made || Date.now() - made < STALE_DAYS * 86400000) return false;
    return related.some((m) => m && time(m.updated_at) > made);
  }
  const staleHint = () => node("small", "del-reason-hint", "오래된 기억일 수 있어요");

  // "지운 이유: 오래된 기억 · 메모" for a removed memory (removal) or a rejected candidate (review.reason_code).
  function describe(r) {
    const code = r?.removal?.code || r?.review?.reason_code, note = r?.removal ? r.removal.note : r?.review?.reason;
    if (!code) return "";
    return `지운 이유: ${LABEL[code] || code}${note ? ` · ${note}` : ""}`;
  }

  // A removed memory leaves recall and stays in history as rejected, with the reason (POST …/transition).
  const removeMemory = (request, scope, m, { code, note }) => request(`/v1/memories/${encodeURIComponent(m.id)}/transition`, { method: "POST",
    body: JSON.stringify({ scope, expected_revision: m.revision, status: "rejected", reason_code: code, ...(note ? { reason: note } : {}) }) });
  // The same reason on a candidate review: reason_code, plus the note when there is one.
  const rejectFields = ({ code, note }) => ({ reason_code: code, ...(note ? { reason: note } : {}) });

  window.ArkismDeleteReason = { REASONS, label: (c) => LABEL[c] || c, picker, openAfter, maybeStale, staleHint, describe, removeMemory, rejectFields };
})();
