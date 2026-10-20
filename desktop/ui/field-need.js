// 저장·만들기가 막히면 어디를 채워야 하는지 그 칸에서 보여 준다: red line, a short shake, the reason under it, and the cursor there.
// window.ArkismNeed(target, message) — target is the field (input, select, textarea, or an inline text that opens one on click),
// or a field label ("이름") looked up in the open popup. Returns false so callers can `return need(...)`.
window.ArkismNeed = (() => {
  const tr = (x) => window.ArkismI18n?.translate(x) || x;
  const FIELD = "input, select, textarea, [contenteditable], [role=button]";
  function find(label) {
    const root = [...document.querySelectorAll("dialog[open]")].pop() || document;
    const want = tr(label);
    for (const l of root.querySelectorAll("label, .field > span, .field > b, .field-label, small, .auto-key")) {
      if (l.textContent.trim() !== want) continue;
      const box = l.closest(".field, .auto-row, label") || l.parentElement;
      const f = box?.querySelector(FIELD);
      if (f) return f;
    }
    return root.querySelector(`[aria-label="${CSS.escape(want)}"]`);
  }
  function clear(f) {
    f.classList.remove("field-need", "field-shake");
    if (f._needMsg?.isConnected) f._needMsg.remove();
  }
  return function need(target, message) {
    const f = typeof target === "string" ? find(target) : target;
    if (message) window.ArkismNotice?.(message);
    if (!f?.isConnected) return;
    clear(f);
    f.getBoundingClientRect(); // reflow restarts the shake
    f.classList.add("field-need", "field-shake");
    if (message) {
      const m = document.createElement("small");
      m.className = "field-need-msg";
      m.setAttribute("role", "alert");
      m.textContent = tr(message);
      f.after(m);
      f._needMsg = m;
    }
    f.scrollIntoView({ block: "center", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
    if (f.matches("input, select, textarea")) f.focus({ preventScroll: true });
    const off = () => { clear(f); f.removeEventListener("input", off); f.removeEventListener("change", off); f.removeEventListener("click", off); };
    f.addEventListener("input", off); f.addEventListener("change", off); f.addEventListener("click", off);
    setTimeout(() => f.classList.remove("field-shake"), 400);
  };
})();
