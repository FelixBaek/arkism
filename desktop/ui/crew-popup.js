// @ts-check
// 크루 상세 팝업 틀 (Figma 868:61909 전문가 · 961:60688 팀 · 993:60224 자동화): one window shape for all three.
// Head (cover photo or avatar, name, one line, actions) → tabs (편집 · 테스트 · 기록 …) → the picked tab's body.
// Each tab's render(box) fills its own body; switching tabs keeps the window and only redraws the body.
window.ArkismCrewPopup = ({ el, button }) => {
  function open({ title, sub = "", cover = "", avatar = null, tabs, initial, actions = [], onClose, cls = "", popout = null }) {
    const dlg = el("dialog", `crew-dialog crew-popup ${cls}`.trim());
    if (popout) { dlg.dataset.popout = JSON.stringify(popout); dlg.dataset.popoutTitle = popout.title || title || ""; dlg.setAttribute("aria-keyshortcuts", "Alt+Shift+O"); } // how a separate window reopens it (popup-drag.js)
    const head = el("div", "crew-popup-head");
    if (cover) { head.style.setProperty("--cover", `url("${cover}")`); head.classList.add("has-cover"); }
    const who = el("div", "crew-popup-who");
    if (avatar) who.append(avatar);
    const names = el("div", "");
    names.append(el("h2", "", title), el("p", "", sub));
    who.append(names);
    const side = el("div", "crew-popup-actions");
    side.append(...actions);
    const close = button("", "ghost icon-btn crew-popup-close", "x"); // Figma ✕ (868:61909 닫기)
    close.setAttribute("aria-label", "닫기");
    close.title = "닫기";
    close.addEventListener("click", () => dlg.close());
    side.append(close);
    head.append(who, side);
    const bar = el("div", "crew-popup-tabs");
    bar.setAttribute("role", "tablist");
    const body = el("div", "dialog-body crew-popup-body");
    let current = initial || tabs[0].key;
    const draw = () => {
      bar.replaceChildren(...tabs.map((t) => {
        const b = el("button", `crew-popup-tab${t.key === current ? " on" : ""}`, t.label);
        b.type = "button";
        b.setAttribute("role", "tab");
        b.setAttribute("aria-selected", String(t.key === current));
        b.addEventListener("click", () => { if (t.key !== current) { current = t.key; draw(); } });
        return b;
      }));
      body.replaceChildren();
      tabs.find((t) => t.key === current).render(body);
    };
    dlg.append(head, bar, body);
    // Once the body scrolls, a line and a gap under the head keep the content from running into it.
    dlg.addEventListener("scroll", (e) => { if (e.target instanceof Element && e.target !== dlg) head.classList.toggle("scrolled", e.target.scrollTop > 0); }, true);
    dlg.addEventListener("close", () => { dlg.remove(); onClose?.(); });
    document.body.append(dlg);
    draw();
    dlg.showModal();
    return { dialog: dlg, body, select: (key) => { current = key; draw(); }, setTitle: (t, s) => { names.firstChild.textContent = t; if (s != null) names.lastChild.textContent = s; } };
  }
  return { open };
};
