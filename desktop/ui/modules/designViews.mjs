// Owns design library. Dependencies are explicit live ports, never a global scope.
export function createDesignViews(deps) {


  // ---------------------------------------------------------------- 디자인 시스템 라이브러리
  // Arkism's own tokens are not a stored system: they open from the 'Arkism 토큰' button above the list.
  const PURPOSES = ["앱 화면", "웹사이트", "슬라이드", "브랜드", "문서·인쇄"];

  const paintable = (v) => !!v && window.CSS?.supports?.("color", v);

  function renderLibrary() {
    const seg = deps.segment([["product", "제품별"], ["purpose", "목적별"]], deps.state.designGroup, (k) => { deps.state.designGroup = k; deps.store("arkism.designGroup", k); deps.render(false); });
    seg.classList.add("v4g-group-seg");
    ["#chips", "#stats"].forEach((q) => deps.$(q).replaceChildren());
    if (deps.gallery.isOpen("design_systems")) { deps.$("#controls").replaceChildren(...deps.gallery.libraryButtons()); return deps.gallery.renderArchive("design_systems"); }
    const draw = () => {
      const all = (deps.state.data.designSystems || []).filter(({ design_system: ds }) =>
        deps.matches(`${ds.title} ${ds.product || ""} ${ds.purpose || ""} ${ds.description || ""} ${(ds.tags || []).join(" ")}`));
      const page = deps.el("div", "ds-library");
      if (!all.length) {
        const empty = deps.el("div", "empty");
        if (deps.state.data.designSystems?.length) empty.append("조건에 맞는 디자인 시스템이 없어요.");
        else empty.append(deps.el("p", "", "아직 디자인 시스템이 없어요. 제품의 색·글꼴·규칙을 하나씩 추가해 보세요."), deps.button("디자인 시스템 추가", "primary", null, () => openSystemEditor(null)));
        page.append(empty); deps.$("#table").replaceChildren(page); return;
      }
      const groups = new Map();
      for (const r of all) {
        const key = (r.design_system[deps.state.designGroup] || "").trim();
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key).push(r);
      }
      // The group holding Arkism first, then by name, unnamed last.
      const rank = (k) => (groups.get(k).some((r) => r.builtin) ? 0 : k ? 1 : 2);
      const keys = [...groups.keys()].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b, "ko"));
      let n = 0;
      for (const k of keys) {
        const g = deps.el("section", "ds-group");
        const h = deps.el("h2", "", k || (deps.state.designGroup === "product" ? "제품 정하지 않음" : "목적 정하지 않음"));
        h.append(deps.el("small", "", `${groups.get(k).length}개`));
        const grid = deps.el("div", "ds-grid");
        for (const r of groups.get(k)) grid.append(systemCard(r, ++n));
        g.append(h, grid);
        page.append(g);
      }
      if (deps.state.data.designSystems === null) page.append(deps.el("p", "pg-hint", "서버가 아직 디자인 시스템 저장을 몰라요. 서버를 새 버전으로 올리면 여기에 모을 수 있어요."));
      deps.$("#table").replaceChildren(page);
    };
    deps.$("#controls").replaceChildren(deps.paneSearch("디자인 시스템 찾기", draw), seg, ...deps.gallery.libraryButtons());
    draw();
  }

  // "Orbit Mail" → "Orbit Mail." — the gallery's serif names end with a full stop.
  const serifName = (t) => (/[.!?。]$/.test(t) ? t : `${t}.`);

  function swatches(colors, n = 4) {
    const row = deps.el("div", "ds-swatches");
    for (const c of (colors || []).filter((x) => paintable(x.value)).slice(0, n)) { const i = deps.el("i"); i.style.background = c.value; i.title = `${c.name} ${c.value}`; row.append(i); }
    return row;
  }

  function systemCard(r, n) {
    const id = `ds:${r.id}`;
    const ds = r.design_system;
    const card = deps.el("button", deps.state.selected === id ? "ds-card selected" : "ds-card");
    card.type = "button";
    card.dataset.id = id;
    const art = deps.el("div", "ds-art");
    // The darkest #hex colour of the system is the ground under the line art.
    const lum = (v) => { const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(v.trim()); if (!m) return 2; const h = m[1].length === 3 ? [...m[1]].map((x) => x + x).join("") : m[1]; return [0, 2, 4].reduce((s, i, k) => s + parseInt(h.slice(i, i + 2), 16) * [0.2126, 0.7152, 0.0722][k], 0) / 255; };
    const ground = (ds.colors || []).filter((c) => lum(c.value) < 0.35).sort((x, y) => lum(x.value) - lum(y.value))[0];
    if (ground) art.style.background = ground.value;
    const img = deps.el("img"); img.src = "img/arkism-art.svg"; img.alt = ""; art.append(img);
    const meta = [r.builtin ? "이 앱" : "", ds.product, ds.purpose].filter(Boolean).join(" · ");
    card.append(deps.el("span", "ds-num", `COLLECTION ${String(n).padStart(2, "0")}`), deps.raw(deps.el("strong", "ds-name", serifName(r.builtin ? "arkism" : ds.title))), art, swatches(ds.colors), deps.raw(deps.el("span", "ds-meta", meta)));
    card.addEventListener("click", () => {
      if (deps.gallery.pick(r, card)) return;
      if (r.builtin) { deps.go("tokens"); return; }
      deps.state.selected = id; deps.markSelected(); deps.renderDetail(); deps.openDetail();
    });
    if (deps.gallery.picking()) return deps.gallery.markCard(r, card);
    return r.builtin ? card : deps.gallery.cardShortcut("design_systems", r, card);
  }

  function librarySummary() {
    const body = deps.detailShell("디자인 시스템");
    body.classList.add("v4g-detail");
    body.append(deps.el("h2", "", "디자인 시스템 라이브러리"),
      deps.el("p", "lead", "제품별·목적별로 디자인 시스템을 모아 둬요. 오른쪽 위 ‘디자인 시스템 추가’로 넣고, 이 앱의 실제 색·글자·부품은 ‘Arkism 토큰’에서 봐요."));
    if (deps.state.data.designSystems === null) body.append(deps.callout("서버 업데이트가 필요해요", "Arkism 서버를 새 버전으로 올리면 디자인 시스템을 저장할 수 있어요."));
  }

  function systemDetail(r) {
    const ds = r.design_system;
    const body = deps.detailShell("선택한 컬렉션");
    body.classList.add("v4g-detail", "ds-detail");
    const top = deps.el("div", "section v4g-top");
    top.append(deps.el("p", "v4g-kicker", "선택한 컬렉션"), deps.raw(deps.el("h2", "ds-big", serifName(ds.title))));
    const sub = [ds.product, ds.purpose].filter(Boolean).join(" / ");
    if (sub) top.append(deps.raw(deps.el("p", "ds-sub", sub)));
    if (ds.description) top.append(deps.raw(deps.el("p", "lead", ds.description)));
    const acts = deps.el("div", "actions");
    acts.append(deps.button("디자인 시스템 고치기", "primary small", null, () => openSystemEditor(r)), deps.button("복제", "small", null, () => duplicateSystem(r)), deps.gallery.deleteButton("design_systems", r));
    top.append(acts);
    body.append(top);
    if (ds.colors?.length) {
      const s = deps.section("팔레트", `${ds.colors.length}개`);
      s.append(swatches(ds.colors, 6), deps.el("p", "ds-names", ds.colors.slice(0, 6).map((c) => c.name.toUpperCase()).join(" · ")));
      body.append(s);
    }
    if (ds.fonts?.length) {
      const s = deps.section("글꼴");
      ds.fonts.forEach((f, i) => {
        const line = deps.raw(deps.el("p", ["ds-font-a", "ds-font-b", "ds-font-c"][Math.min(i, 2)], [`Aa  ${f}`, `가나다 · ${f}`, `012345 / ${f}`][Math.min(i, 2)]));
        line.style.fontFamily = `"${f.replaceAll('"', "")}", ${["serif", "var(--sans)", "var(--mono)"][Math.min(i, 2)]}`;
        s.append(line);
      });
      body.append(s);
    }
    if (ds.guide) {
      const s = deps.section("쓰는 법·규칙");
      s.querySelector("h3").append(deps.copyButton(ds.guide));
      const ol = deps.el("ol", "ds-rules");
      ds.guide.split("\n").map((x) => x.trim()).filter(Boolean).forEach((line, i) => {
        const li = deps.el("li");
        li.append(deps.el("span", "", String(i + 1).padStart(2, "0")), deps.raw(deps.el("span", "", line)));
        ol.append(li);
      });
      s.append(ol);
      body.append(s);
    }
    if (ds.colors?.length) {
      const s = deps.section("색상 값");
      const dl = deps.el("dl", "ds-values");
      ds.colors.forEach((c) => dl.append(deps.raw(deps.el("dt", "", c.name)), deps.raw(deps.el("dd", "", c.value))));
      s.append(dl);
      body.append(s);
    }
    if (ds.source) {
      const s = deps.el("div", "ds-source-row");
      s.append(deps.raw(deps.el("span", "ds-source", `${deps.tr("출처")} · ${ds.source}`)));
      if (/^https?:\/\//.test(ds.source)) s.append(deps.button("원본에서 열기  ↗", "ghost small", null, () => window.open(ds.source, "_blank")));
      s.append(deps.copyButton(ds.source));
      body.append(s);
    }
    const foot = deps.el("div", "v4g-foot");
    const links = deps.el("div", "actions");
    links.append(deps.button("보관함에서 빼기", "ghost", null, () => archiveSystem(r)));
    foot.append(links, deps.el("p", "meta", `${deps.shortDate(r.updated_at)} 수정 · ${r.created_by || ""}`));
    body.append(foot);
  }

  const systemDialog = deps.$("#ds-dialog");

  let editingSystem = null;

  function fillList(id, values) {
    deps.$(id).replaceChildren(...[...new Set(values.filter(Boolean))].map((v) => { const o = deps.el("option"); o.value = v; return o; }));
  }

  function openSystemEditor(r) {
    if (!deps.state.data.designSystems) { deps.notice("서버를 새 버전으로 올린 뒤에 추가할 수 있어요."); return; }
    editingSystem = r;
    const ds = r?.design_system || { product: "", purpose: "" };
    deps.$("#ds-dialog-title").textContent = r ? "디자인 시스템 고치기" : "디자인 시스템 추가";
    deps.$("#ds-meta").hidden = !r;
    deps.$("#ds-meta").textContent = r ? `수정 날짜 · ${deps.shortDate(r.updated_at)} / 작성자 · ${r.created_by || ""}` : "";
    const set = (id, v) => { deps.$(id).value = v ?? ""; };
    set("#ds-title", ds.title); set("#ds-product", ds.product); set("#ds-purpose", ds.purpose); set("#ds-description", ds.description); set("#ds-source", ds.source);
    set("#ds-colors", (ds.colors || []).map((c) => `${c.name} ${c.value}`).join("\n"));
    set("#ds-fonts", (ds.fonts || []).join(", ")); set("#ds-guide", ds.guide);
    const saved = deps.state.data.designSystems.map((x) => x.design_system);
    fillList("#ds-products", ["Arkism", ...saved.map((x) => x.product)]);
    fillList("#ds-purposes", [...PURPOSES, ...saved.map((x) => x.purpose)]);
    deps.$("#ds-error").textContent = "";
    drawColorChips();
    systemDialog.showModal();
  }

  // Under the 색 box: each line as a chip with its color, so a typo shows right away.
  function drawColorChips() {
    deps.$("#ds-color-chips").replaceChildren(...parseColors(deps.$("#ds-colors").value).slice(0, 64).map((c) => {
      const chip = deps.el("span", "color-chip");
      const dot = deps.el("i");
      if (paintable(c.value)) dot.style.background = c.value; else chip.classList.add("bad");
      chip.append(dot, deps.raw(deps.el("span", "", c.name)));
      chip.title = c.value;
      return chip;
    }));
  }

  deps.$("#ds-colors").addEventListener("input", drawColorChips);

  // One color per line: "name value", "name: value", or just a value.
  function parseColors(text) {
    return text.split("\n").map((l) => l.trim()).filter(Boolean).map((l, i) => {
      const m = !paintable(l) && /^([^\s:=]+)[\s:=]+([^\s:=].*)$/.exec(l);
      return m ? { name: m[1], value: m[2].trim() } : { name: `color-${i + 1}`, value: l };
    });
  }

  deps.$("#ds-form").addEventListener("submit", async (e) => {
    if (e.submitter?.value !== "save") return;
    e.preventDefault();
    const val = (id) => deps.$(id).value.trim();
    const system = {
      title: val("#ds-title"), product: val("#ds-product"), purpose: val("#ds-purpose"), description: val("#ds-description"), source: val("#ds-source"),
      colors: parseColors(deps.$("#ds-colors").value), fonts: val("#ds-fonts").split(",").map((x) => x.trim()).filter(Boolean), guide: deps.$("#ds-guide").value.trim(),
      tags: editingSystem?.design_system.tags || []
    };
    try {
      const saved = editingSystem
        ? await deps.request(`/v1/design_systems/${encodeURIComponent(editingSystem.id)}`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: editingSystem.revision, system }) })
        : await deps.request("/v1/design_systems", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, system }) });
      systemDialog.close();
      deps.state.data.designSystems = (await deps.request(`/v1/design_systems?${deps.query()}`)).items || [];
      deps.state.selected = `ds:${saved.id}`;
      deps.render();
      deps.notice("저장했어요.");
    } catch (err) {
      deps.$("#ds-error").textContent = { secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요.", invalid_request: "이름과 색 줄(이름 값)을 확인해 주세요. 색은 64개까지예요.", revision_conflict: "다른 곳에서 먼저 고쳤어요. 닫고 다시 열어 주세요." }[err.message] || `저장하지 못했어요 (${err.message}).`;
    }
  });

  async function duplicateSystem(r) {
    if (!deps.state.data.designSystems) return;
    const system = { ...r.design_system, title: deps.copyTitle(r.design_system.title, 80) };
    try {
      const saved = await deps.request("/v1/design_systems", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, system }) });
      deps.state.data.designSystems = (await deps.request(`/v1/design_systems?${deps.query()}`)).items || [];
      deps.state.selected = saved?.id ? `ds:${saved.id}` : null;
      deps.render();
      deps.notice("복제했어요. 사본을 고쳐서 쓰세요.");
    } catch (err) {
      deps.notice({ secret_detected: "키나 토큰처럼 보이는 값이 있어 복제하지 않았어요.", invalid_request: "복제하지 못했어요. 이름과 색을 확인해 주세요." }[err.message] || `복제하지 못했어요 (${err.message}).`);
    }
  }

  async function archiveSystem(r) {
    if (!await ask(`'${r.design_system?.title || r.title || "이 디자인 시스템"}'을 보관함에서 뺄까요? 목록에서 사라지지만 기록은 남아요.`)) return;
    try {
      await deps.request(`/v1/design_systems/${encodeURIComponent(r.id)}`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: r.revision, archive: true }) });
      deps.state.data.designSystems = (await deps.request(`/v1/design_systems?${deps.query()}`)).items || [];
      deps.state.selected = null;
      deps.render();
      deps.notice("보관함에서 뺐어요. 기록은 남아 있어요.");
    } catch (err) {
      deps.notice(`처리하지 못했어요: ${err.message}`);
    }
  }

  return {
    get fillList() { return fillList; },
    get librarySummary() { return librarySummary; },
    get openSystemEditor() { return openSystemEditor; },
    get renderLibrary() { return renderLibrary; },
    get systemDetail() { return systemDetail; }
  };
}
