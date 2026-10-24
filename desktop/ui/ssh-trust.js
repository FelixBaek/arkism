// All desktop SSH entry points share this review gate. The native layer enforces
// the pin again, so skipping this UI cannot authorize an unknown server.
(() => {
  "use strict";
  let sequence = 0;
  let dialogQueue = Promise.resolve();
  function reviewDialog(review) {
    const show = () => new Promise((resolve) => {
      const before = document.activeElement;
      const dlg = document.createElement("dialog");
      dlg.className = "cn-confirm ssh-trust";
      const node = (tag, text, cls) => { const el = document.createElement(tag); el.textContent = text; if (cls) el.className = cls; return el; };
      const changed = review.state === "changed";
      const title = node("h2", changed ? "서버의 SSH 키가 바뀌었어요" : "서버의 SSH 지문을 확인하세요");
      title.id = `ssh-trust-title-${++sequence}`;
      const intro = node("p", changed ? "서버를 다시 설치했거나, 다른 서버에 연결되고 있을 수 있어요. 관리자에게 새 지문을 확인하기 전에는 접속하지 마세요." : "서버 관리자에게 다른 연락 수단으로 아래 지문을 확인해 주세요. 확인 전에는 개인 키·비밀번호로 로그인하지 않아요.");
      intro.id = `ssh-trust-description-${sequence}`;
      dlg.setAttribute("aria-labelledby", title.id);
      dlg.setAttribute("aria-describedby", intro.id);
      const details = node("dl", "");
      for (const [label, value] of [["서버", `${review.host}:${review.port}`], ["키 종류", review.key_type], ["새 지문", review.fingerprint], ...(changed ? [["이전 지문", review.previous_fingerprint]] : [])]) {
        const text = node("code", value);
        text.translate = false; text.className = "notranslate";
        const dd = node("dd", ""); dd.append(text); details.append(node("dt", label), dd);
      }
      const label = node("label", "", "ssh-trust-check");
      const check = document.createElement("input"); check.type = "checkbox";
      label.append(check, node("span", "관리자에게 지문을 확인했고, 위 서버가 맞습니다."));
      const row = node("div", "", "cn-confirm-btns");
      const cancel = node("button", "취소", "btn"); cancel.type = "button";
      const approve = node("button", changed ? "새 키로 교체하고 계속" : "이 키를 저장하고 계속", "btn primary"); approve.type = "button"; approve.disabled = true;
      row.append(cancel, approve); dlg.append(title, intro, details, label, row);
      let done = false;
      const finish = (value) => { if (done) return; done = true; dlg.close(); dlg.remove(); if (before?.isConnected) before.focus(); resolve(value); };
      check.addEventListener("change", () => { approve.disabled = !check.checked; });
      cancel.addEventListener("click", () => finish(false));
      approve.addEventListener("click", () => { if (check.checked) finish(true); });
      dlg.addEventListener("cancel", (event) => { event.preventDefault(); finish(false); });
      dlg.addEventListener("close", () => finish(false));
      // The app's global Enter shortcut must never approve this security dialog.
      dlg.addEventListener("keydown", (event) => {
        if (event.key === "Enter") { event.preventDefault(); event.stopPropagation(); if (event.target === cancel) finish(false); else if (event.target === approve && check.checked) finish(true); }
      });
      document.body.append(dlg); dlg.showModal(); cancel.focus({ preventScroll: true }); dlg.scrollTop = 0;
    });
    const next = dialogQueue.then(show);
    dialogQueue = next.catch(() => {});
    return next;
  }
  window.ArkismSSH = (core) => {
    if (!core) return core;
    const pending = new Map();
    function ensure(args, force) {
      const target = args.form ? { form: args.form } : { host: args.host };
      const key = JSON.stringify([target, force]);
      if (pending.has(key)) return pending.get(key);
      const task = (async () => {
        const review = await core.invoke("ssh_host_key_check", { ...target, force });
        if (review.state === "trusted") return;
        if (!review.challenge) throw new Error("ssh_host_key_probe_failed");
        try {
          if (!await reviewDialog(review)) throw new Error("ssh_host_key_cancelled");
          await core.invoke("ssh_host_key_approve", { challenge: review.challenge, replace: review.state === "changed" });
        } finally {
          await core.invoke("ssh_host_key_cancel", { challenge: review.challenge }).catch(() => {});
        }
      })();
      pending.set(key, task);
      task.then(() => pending.delete(key), () => pending.delete(key));
      return task;
    }
    return { ...core, async invoke(command, args = {}) {
      if (!["ssh_connect", "ssh_test", "skill_execution"].includes(command)) return core.invoke(command, args);
      await ensure(args, false);
      try { return await core.invoke(command, args); }
      catch (error) {
        if (String(error?.message || error) !== "ssh_host_key_changed") throw error;
        await ensure(args, true);
        return core.invoke(command, args);
      }
    } };
  };
})();
