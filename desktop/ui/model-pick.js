// @ts-check
// One rule for every model picker (설정 › 역할별 모델·AI 도구, 라우팅 설정, Flow 단계 모델): models of a connected provider
// show; the rest wait behind 「연결 안 된 모델도 보기」, greyed with 연결 필요 and a way to connect. A saved pick whose provider
// is not connected is never hidden: it stays picked, marked 연결 필요.
(() => {
  const MORE = "\u0000more", CONNECT = "\u0000connect"; // values no model name has
  const OFF = "연결 필요";

  // groups: [{ name, models, usable }] (usable missing = connected). valueFor(group, model) is what an option saves.
  // → shown: the groups to draw, each with the models to list and off (not connected); hidden: how many models wait behind the toggle;
  //   currentOff: the saved pick is one of the not connected models.
  function plan(groups, current = "", valueFor = (p, m) => `${p.id}|${m}`, all = false) {
    const shown = [];
    const picks = (p) => (m) => !!current && valueFor(p, m) === current;
    // a name several providers offer is fine while one of them is connected
    const onElsewhere = groups.some((p) => p.usable !== false && p.models.some(picks(p)));
    let hidden = 0, currentOff = false;
    for (const p of groups) {
      const off = p.usable === false;
      const picked = picks(p);
      if (off && !onElsewhere && p.models.some(picked)) currentOff = true;
      const models = off && !all ? p.models.filter((m) => !onElsewhere && picked(m)) : p.models;
      if (off && !all) hidden += p.models.length - models.length;
      if (models.length) shown.push({ ...p, models, off });
    }
    shown.sort((a, b) => Number(a.off) - Number(b.off)); // the not connected ones last, under the connected
    return { shown, hidden, currentOff };
  }

  // Draws the groups into sel after the options the screen already put there (기본값, 자동 …), keeps current picked and, when
  // current is in no list at all (turned off since, saved without a provider), adds it so the screen still shows the saved pick.
  // group(p, value) → <optgroup>: the screen's own drawing (models.modelGroup). onConnect: opens where providers connect;
  // without it (an unsaved editor that a jump would lose) the not connected rows only say 연결 필요.
  // Call before the screen adds its change listener: picking 보기 or 연결하러 가기 is not a pick and never reaches it.
  function fill(sel, groups, { current = "", valueFor = (p, m) => `${p.id}|${m}`, group, onConnect = null }) {
    let all = false, added = [];
    const opt = (text, value) => { const o = document.createElement("option"); o.textContent = text; o.value = value; return o; };
    function draw() {
      for (const n of added) n.remove();
      added = [];
      const { shown, hidden, currentOff } = plan(groups, current, valueFor, all);
      for (const p of shown) {
        const g = group(p, (m) => valueFor(p, m));
        if (p.off) {
          g.label = `${g.label} · ${OFF}`;
          for (const o of [...g.children]) {
            o.dataset.hint = OFF;
            if (currentOff && o.value === current) o.textContent = `${OFF} · ${o.textContent}`; // the saved pick: kept, the warning first so a narrow select still shows it
            else o.disabled = true; // runs would fail until the provider is connected
          }
        }
        added.push(g);
      }
      if (current && ![...sel.options, ...added.flatMap((g) => [...g.children])].some((o) => o.value === current)) added.push(opt(current.split("|").pop() || current, current));
      if (hidden) added.push(opt(`연결 안 된 모델도 보기 (${hidden}개)`, MORE));
      if (onConnect && (currentOff || all && shown.some((p) => p.off))) added.push(opt("모델 연결하러 가기 ›", CONNECT));
      sel.append(...added);
      sel.value = current;
      warn(currentOff);
    }
    function warn(off) {
      sel.classList.toggle("model-off", off);
      sel.title = off ? "이 모델은 제공자가 연결되지 않아 지금은 못 써요. 설정 › 에이전트에서 로그인하거나 키를 넣어요." : "";
    }
    sel.addEventListener("change", (e) => {
      if (sel.value === MORE) {
        e.stopImmediatePropagation(); all = true; draw();
        // the app's drop-down (combo.js) closed on the pick: open it again so the rows just added are in view
        if (globalThis.MouseEvent) setTimeout(() => sel.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true, button: 0 })));
      }
      else if (sel.value === CONNECT) { e.stopImmediatePropagation(); sel.value = current; onConnect?.(); }
      else { current = sel.value; warn(plan(groups, current, valueFor).currentOff); } // the screen saves it; a later redraw keeps it picked
    });
    draw();
    return sel;
  }

  const api = { plan, fill, MORE, CONNECT };
  globalThis.ArkismModelPick = api;
  if (typeof module !== "undefined") module.exports = api;
})();
