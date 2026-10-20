// In-app yes/no and text boxes (Figma 확인 팝업 253:18954) in place of the browser's confirm() and prompt(),
// which the desktop app shows as a "tauri.localhost의 메시지" web window.
// The first line of the message is the title; the rest is the body.
(() => {
  "use strict";
  function box(message, okLabel, field, match) {
    return new Promise((resolve) => {
      const [title, ...rest] = String(message).split("\n");
      const dlg = document.createElement("dialog");
      dlg.className = "cn-confirm";
      const form = document.createElement("form");
      form.method = "dialog";
      const h = document.createElement("h2");
      h.textContent = title;
      form.append(h);
      const body = rest.join("\n").trim();
      if (body) { const p = document.createElement("p"); p.textContent = body; form.append(p); }
      if (field) form.append(field);
      const row = document.createElement("div");
      row.className = "cn-confirm-btns";
      const mk = (label, value, cls) => { const b = document.createElement("button"); b.type = "submit"; b.value = value; b.className = cls; b.textContent = label; return b; };
      const no = mk("취소", "no", "btn");
      const ok = mk(okLabel || "확인", "ok", "btn danger");
      if (match != null) { // 확인 opens only once the field holds exactly this text (지우기처럼 되돌릴 수 없는 일)
        ok.disabled = true;
        field.addEventListener("input", () => { ok.disabled = field.value.trim() !== match; });
        form.addEventListener("submit", (e) => { if (e.submitter?.value !== "no" && field.value.trim() !== match) e.preventDefault(); });
      }
      row.append(no, ok);
      form.append(row);
      dlg.append(form);
      let answer = "no";
      form.addEventListener("submit", (e) => { answer = e.submitter?.value || "ok"; }); // Enter in the field submits as 확인
      dlg.addEventListener("close", () => { dlg.remove(); resolve(answer === "ok"); });
      document.body.append(dlg);
      dlg.showModal();
      (field || no).focus();
    });
  }
  window.ask = (message, okLabel) => box(message, okLabel);
  window.askTyped = (message, match, okLabel) => {
    const input = document.createElement("input");
    input.className = "cn-confirm-input";
    input.placeholder = match;
    input.setAttribute("aria-label", "확인용 이름");
    input.autocomplete = "off";
    return box(message, okLabel, input, match);
  };
  window.askText = async (message, value = "") => {
    const input = document.createElement("input");
    input.className = "cn-confirm-input";
    input.value = value ?? "";
    return (await box(message, "확인", input)) ? input.value : null;
  };
})();
