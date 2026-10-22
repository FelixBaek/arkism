// Owns image prompt editing. Dependencies are explicit live ports, never a global scope.
export function createImageViews(deps) {


  // ---------------------------------------------------------------- 이미지
  const ENGINES = { gemini: "Gemini", gpt: "GPT", claude: "Claude", grok: "Grok", comfyui: "ComfyUI", other: "기타" };

  const ENGINE_LOGOS = { gemini: "sym:a-gemini", gpt: "sym:a-codex", claude: "img:claude.svg", grok: "sym:i-x" };

  // Older records said comfyui_api or codex; they read and save as the model they meant.
  const engineOf = (ip) => ({ comfyui_api: "comfyui", codex: "gpt" })[ip.engine] || ip.engine;

  const SCRIPT_CHIP = "\u0000script";

  const variablesOf = (text) => [...new Set((text.match(/\{[^{}\s]{1,30}\}/g) || []))];

  function engineLogo(k) {
    if (!ENGINE_LOGOS[k]) return null;
    const t = deps.agentLogo(ENGINE_LOGOS[k], ENGINES[k]);
    t.classList.add("eng-logo");
    return t;
  }

  // A model name with its logo (tabs, card meta, the editor's model pills).
  function engineLabel(k, text) {
    const s = deps.el("span", "eng-label");
    const logo = engineLogo(k);
    if (logo) s.append(logo);
    s.append(text ?? (ENGINES[k] || k));
    return s;
  }

  // ← 이미지 above the title. render() takes it away again, so only the screen that draws it shows it.
  function pageBack(label, onClick) {
    const b = deps.el("button", "page-back");
    b.type = "button";
    b.append(deps.icon("arrow-left"), label);
    b.addEventListener("click", onClick);
    deps.$(".main > .page-head").prepend(b);
  }

  // 검색 inside the panel: the same query as the top search box, but only the list redraws so typing keeps its place.
  function paneSearch(placeholder, redraw) {
    const l = deps.el("label", "pane-search");
    const i = deps.el("input");
    i.type = "search"; i.placeholder = placeholder; i.value = deps.$("#search").value; i.setAttribute("aria-label", placeholder);
    i.addEventListener("input", () => { deps.$("#search").value = i.value; deps.state.query = i.value.trim().toLowerCase(); redraw(); });
    l.append(deps.icon("magnifying-glass"), i, deps.el("kbd", "", "/"));
    return l;
  }

  function promptTile(p, big) {
    const t = deps.el("div", `prompt-tile${big ? " big" : ""}${p.image_prompt.kind === "slide" ? " slide" : ""}`);
    const src = p.image_prompt.images?.[0];
    if (src) { const img = deps.el("img"); img.src = src; img.alt = ""; t.append(img); return t; }
    t.append(deps.icon(p.image_prompt.kind === "slide" ? "presentation" : "image"));
    return t;
  }

  function promptMeta(ip) {
    const m = deps.el("div", "pc-meta");
    if (ip.kind === "slide") {
      m.classList.add("mono");
      m.append([ip.size, ip.script ? "스크립트 연결됨" : "프롬프트"].filter(Boolean).join(" · "));
      return m;
    }
    m.append(engineLabel(engineOf(ip), deps.raw(deps.el("span", "", [...(ip.tags || []), ENGINES[engineOf(ip)] || ip.engine].join(" · ")))));
    return m;
  }

  function renderImages() {
    const all = deps.state.data.prompts;
    const run = deps.state.promptRun && all.find((p) => p.id === deps.state.promptRun);
    if (deps.state.promptRun && !run) deps.state.promptRun = null;
    if (run) return renderPromptRun(run);
    if (deps.gallery.isOpen("prompts")) { deps.$("#controls").replaceChildren(); deps.$("#chips").replaceChildren(deps.gallery.archiveChip("prompts")); return deps.gallery.renderArchive("prompts", deps.state.imageTab); }
    const ofKind = all.filter((p) => p.image_prompt.kind === deps.state.imageTab);
    // 모델 tabs (only models in use) and 카테고리 chips; a filter whose last prompt left falls back to 전체.
    const models = Object.keys(ENGINES).filter((k) => ofKind.some((p) => engineOf(p.image_prompt) === k));
    if (!models.includes(deps.state.engine)) deps.state.engine = "all";
    deps.$("#controls").replaceChildren(...(models.length > 1 ? [deps.segment([["all", "전체 모델"], ...models.map((k) => [k, engineLabel(k)])], deps.state.engine, (k) => { deps.state.engine = k; deps.render(false); })] : []));
    const cats = [...new Set(ofKind.flatMap((p) => p.image_prompt.tags || []))].sort((a, b) => a.localeCompare(b, "ko"));
    const filters = [["all", "전체"], ...cats.map((c) => [c, c]), ...(ofKind.some((p) => p.image_prompt.script) ? [[SCRIPT_CHIP, "스크립트 있음"]] : [])];
    if (!filters.some(([k]) => k === deps.state.imgCat)) deps.state.imgCat = "all";
    const chips = filters.length > 1 ? filters.map(([k, label]) => {
      const c = deps.el("button", `chip${deps.state.imgCat === k ? " active" : ""}`, label); c.type = "button";
      c.addEventListener("click", () => { deps.state.imgCat = k; deps.render(false); });
      return c;
    }) : [];
    const keep = (p) => {
      const ip = p.image_prompt;
      if (deps.state.engine !== "all" && engineOf(ip) !== deps.state.engine) return false;
      if (deps.state.imgCat === SCRIPT_CHIP) return !!ip.script;
      return deps.state.imgCat === "all" || (ip.tags || []).includes(deps.state.imgCat);
    };
    const grid = deps.$("#table");
    const slide = deps.state.imageTab === "slide";
    const draw = () => {
      const items = sortPrompts(ofKind.filter((p) => keep(p) && deps.matches(`${p.image_prompt.title} ${p.image_prompt.prompt} ${(p.image_prompt.tags || []).join(" ")}`)));
      if (!items.length) {
        grid.replaceChildren(deps.el("div", "empty", all.length ? "조건에 맞는 프롬프트가 없어요." : "아직 프롬프트가 없어요. 오른쪽 위 ‘프롬프트 추가’로 넣어 보세요."));
        return;
      }
      grid.replaceChildren(...items.map((p, i) => {
        const card = deps.el("button", deps.state.selected === "prompt:" + p.id ? "prompt-card selected" : "prompt-card");
        card.type = "button";
        card.dataset.id = `prompt:${p.id}`;
        const head = deps.el("div", "pc-head");
        head.append(deps.raw(deps.el("strong", "", p.image_prompt.title)));
        if (!slide) head.append(deps.el("span", "pc-num", String(i + 1).padStart(2, "0")));
        card.append(promptTile(p), head, promptMeta(p.image_prompt));
        card.addEventListener("click", () => { deps.state.selected = `prompt:${p.id}`; deps.markSelected(); deps.renderDetail(); deps.openDetail(); });
        return deps.gallery.cardShortcut("prompts", p, card);
      }));
    };
    const search = paneSearch(slide ? "프롬프트 이름 · 스크립트 검색" : "프롬프트 이름 · 도구 검색", draw);
    deps.$("#chips").replaceChildren(search, promptSortSelect(draw), ...chips, deps.gallery.archiveChip("prompts"));
    deps.$("#stats").replaceChildren();
    draw();
  }

  // 정렬 (Figma 198:1404 "최근 수정순"): the server lists newest first; this re-orders the cards only.
  const PROMPT_SORTS = [["updated", "최근 수정순"], ["created", "최근 추가순"], ["name", "이름순"]];

  let promptSort = PROMPT_SORTS.some(([k]) => k === deps.stored("arkism.promptSort")) ? deps.stored("arkism.promptSort") : "updated";

  function sortPrompts(list) {
    const t = (x, k) => Date.parse(x[k] || x.updated_at || "") || 0;
    if (promptSort === "name") return list.slice().sort((a, b) => a.image_prompt.title.localeCompare(b.image_prompt.title, "ko"));
    const k = promptSort === "created" ? "created_at" : "updated_at";
    return list.slice().sort((a, b) => t(b, k) - t(a, k));
  }

  function promptSortSelect(redraw) {
    const s = deps.el("select", "v4g-sort");
    s.setAttribute("aria-label", "정렬");
    for (const [v, l] of PROMPT_SORTS) { const o = deps.el("option", "", l); o.value = v; o.selected = v === promptSort; s.append(o); }
    s.addEventListener("change", () => { promptSort = s.value; deps.store("arkism.promptSort", promptSort); redraw(); });
    return s;
  }

  // 복제: a new record with the same fields, named "… 사본"; the copy opens selected so it can be edited right away.
  const copyTitle = (t, max) => { const tail = " 사본"; return [...t].slice(0, max - tail.length).join("") + tail; };

  async function duplicatePrompt(p) {
    const prompt = { ...p.image_prompt, title: copyTitle(p.image_prompt.title, 120) };
    try {
      const saved = await deps.request("/v1/prompts", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, prompt }) });
      deps.state.data.prompts = (await deps.request(`/v1/prompts?${deps.query()}`)).items || [];
      deps.state.promptRun = null;
      deps.state.selected = saved?.id ? `prompt:${saved.id}` : null;
      deps.render();
      deps.notice("복제했어요. 사본을 고쳐서 쓰세요.");
    } catch (err) {
      deps.notice({ secret_detected: "키나 토큰처럼 보이는 값이 있어 복제하지 않았어요.", invalid_request: "복제하지 못했어요. 이름이나 이미지 크기를 확인해 주세요." }[err.message] || `복제하지 못했어요 (${err.message}).`);
    }
  }

  function copyButton(text, label = "복사", cls = "ghost small") {
    const b = deps.button(label, cls, cls.includes("ghost") ? "copy" : null, async () => {
      try {
        await navigator.clipboard.writeText(text);
        b.lastChild.textContent = "복사됨";
      } catch (err) {
        b.lastChild.textContent = "복사 안 됨";
        deps.notice(`복사하지 못했어요 (${err.message || err}).`);
      }
    });
    return b;
  }

  function imagesSummary() {
    const body = deps.detailShell(deps.state.imageTab === "slide" ? "슬라이드" : "이미지");
    body.append(deps.el("h2", "", "프롬프트 보관함"), deps.el("p", "lead", "자주 쓰는 이미지·슬라이드 프롬프트를 예시 이미지와 함께 모아 둬요. 카드를 고르면 프롬프트를 복사하거나 바로 만들 수 있어요."));
  }

  // {변수} values typed on the detail or the 실행 page, kept per prompt until the app closes.
  const promptVals = new Map();

  // Everything a prompt can do: fill its {변수}, hand it to its workflow, send it to ComfyUI, download its script.
  function promptKit(p) {
    const ip = p.image_prompt;
    const vars = variablesOf(ip.prompt);
    if (!promptVals.has(p.id)) promptVals.set(p.id, {});
    const vals = promptVals.get(p.id);
    const filled = () => vars.reduce((t, v) => t.split(v).join((vals[v] || "").trim() || v), ip.prompt);
    // 쓰는 워크플로우: the prompt's first live $이름 runs with the filled prompt as its task.
    const flowRec = (deps.state.data.crew || []).find((r) => r.crew.type === "workflow" && (ip.experts || []).some((e) => e.replace(/^\$/, "") === r.crew.handle || e === r.crew.name));
    const comfy = (ip.engine || "").startsWith("comfyui");
    async function comfyUrl(ask) {
      let url = deps.pages.prefs.comfyUrl;
      if (!url) {
        url = deps.trimSlash(await askText(ask, "http://127.0.0.1:8188") || "");
        if (!url) return "";
        deps.pages.prefs.comfyUrl = url;
        deps.pages.savePrefs();
      }
      return url;
    }
    return {
      ip, vars, vals, filled, flowRec, comfy,
      // 이 프롬프트로 만들기: the workflow that uses this prompt gets it (or it is copied).
      async run(btn, out) {
        const text = filled();
        const say = (cls, t) => (out ? out.replaceChildren(deps.el("p", cls, t)) : deps.notice(t));
        if (!flowRec) {
          try { await navigator.clipboard.writeText(text); } catch (_) { /* clipboard blocked */ }
          deps.notice("쓰는 워크플로우가 없어서 채운 프롬프트를 복사했어요. 쓰는 워크플로우를 정하면 바로 맡길 수 있어요.");
          return;
        }
        const scriptPart = ip.script ? `\n\n참고할 스크립트 (${ip.script_name || "script"}):\n${ip.script}` : "";
        const sizePart = ip.size ? `\n크기: ${ip.size}` : "";
        const countPart = ip.count ? `\n개수: ${ip.count}` : "";
        if (btn) btn.disabled = true;
        const r = await deps.crew.runWorkflow(flowRec, `${text}${scriptPart}${sizePart}${countPart}`);
        if (btn) btn.disabled = false;
        say(r ? "dim" : "warn-text", r ? `$${flowRec.crew.handle}에 맡겼어요. 워크플로우 › 최근 실행에서 결과를 봐요.` : "맡기지 못했어요.");
      },
      // ComfyUI로 만들기: 서버 queues the API-format workflow on the ComfyUI address and keeps the images.
      async comfyMake(btn) {
        const url = await comfyUrl("ComfyUI 주소를 넣어 주세요. 서버에서 열 수 있는 주소여야 해요.");
        if (!url) return;
        if (btn) btn.disabled = true;
        deps.notice("ComfyUI에 맡겼어요. 다 그리면 알려 드려요.");
        try {
          // {prompt} sits inside a JSON string in the workflow: put it in JSON-escaped so quotes and new lines keep it valid.
          const a = await deps.crew.assist("comfy", JSON.stringify({ url, workflow: ip.script.split("{prompt}").join(JSON.stringify(filled()).slice(1, -1)) }));
          deps.notice(a.status === "done" ? a.output.replaceAll("\n", " ") : `만들지 못했어요: ${a.error || "알 수 없음"}`);
        } catch (e) { deps.notice(`만들지 못했어요 (${e.message}).`); } finally { if (btn) btn.disabled = false; }
      },
      async comfyOpen() {
        const url = await comfyUrl("ComfyUI 주소를 넣어 주세요.");
        if (!url) return;
        if (ip.script) { try { await navigator.clipboard.writeText(ip.script.split("{prompt}").join(JSON.stringify(filled()).slice(1, -1))); deps.notice("워크플로를 복사했어요. ComfyUI 화면에 붙여 넣으면 열려요."); } catch (_) { /* clipboard blocked */ } }
        window.open(url, "_blank");
      },
      download() {
        const a = document.createElement("a");
        a.href = URL.createObjectURL(new Blob([ip.script], { type: "text/plain" }));
        a.download = ip.script_name || (ip.kind === "slide" ? "make_slide.py" : "workflow.json");
        a.click();
        URL.revokeObjectURL(a.href);
      }
    };
  }

  // A small label over a control or a read-only value box (the detail's 설정, the 실행 page).
  function labeled(label, node, tag = "div") {
    const f = deps.el(tag, "v4g-field");
    f.append(deps.el("span", "v4g-label", label), node);
    return f;
  }

  const valueBox = (text, cls = "") => deps.raw(deps.el("div", `v4g-box ${cls}`.trim(), text));

  function varInput(kit, v, onChange) {
    const i = deps.el("input");
    i.placeholder = v; i.value = kit.vals[v] || "";
    i.addEventListener("input", () => { kit.vals[v] = i.value; onChange?.(); });
    return i;
  }

  function promptDetail(p) {
    const kit = promptKit(p);
    const ip = kit.ip;
    const body = deps.detailShell(ip.kind === "slide" ? "슬라이드" : "이미지");
    body.classList.add("v4g-detail");
    const top = deps.el("div", "section v4g-top");
    top.append(engineLabel(engineOf(ip), `프롬프트 · ${ENGINES[engineOf(ip)] || ip.engine}${ip.kind === "slide" && ip.script ? " · 스크립트 연결됨" : ""}`), deps.raw(deps.el("h2", "", ip.title)));
    const big = promptTile(p, true);
    top.append(big);
    if ((ip.images || []).length > 1) {
      // The small pictures swap into the big one.
      const strip = deps.el("div", "prompt-thumbs");
      ip.images.forEach((src, i) => {
        const b = deps.el("button", i ? "" : "active"); b.type = "button"; b.setAttribute("aria-label", `이미지 ${i + 1}`);
        const img = deps.el("img"); img.src = src; img.alt = ""; b.append(img);
        b.addEventListener("click", () => { big.querySelector("img").src = src; strip.querySelectorAll("button").forEach((x) => x.classList.toggle("active", x === b)); });
        strip.append(b);
      });
      top.append(strip);
    }
    const acts = deps.el("div", "actions");
    acts.append(deps.button("이 프롬프트로 만들기  ↗", "primary small", null, () => { deps.state.promptRun = p.id; deps.render(); }));
    if (kit.comfy) acts.append(deps.button("ComfyUI에서 열기", "small", null, kit.comfyOpen));
    else if (ip.script) acts.append(deps.button(ip.kind === "slide" ? "스크립트 내려받기 (PPTX)" : "스크립트 내려받기", "small", null, kit.download));
    top.append(acts);
    body.append(top);
    const pr = deps.section("프롬프트");
    pr.querySelector("h3").append(copyButton(ip.prompt, "프롬프트 복사", "small"));
    pr.append(valueBox(ip.prompt, "v4g-quote"));
    body.append(pr);
    const opts = deps.section("설정");
    kit.vars.forEach((v) => opts.append(labeled(`변수 · ${v}`, varInput(kit, v), "label")));
    if (ip.negative) opts.append(labeled("제외", valueBox(ip.negative)));
    if (ip.size || ip.count) {
      const row = deps.el("div", "v4g-grid2");
      if (ip.size) row.append(labeled("크기", valueBox(ip.size)));
      if (ip.count) row.append(labeled("한 번에", valueBox(deps.tr(`${ip.count}장`))));
      opts.append(row);
    }
    if (ip.seed) opts.append(labeled("시드", valueBox(ip.seed)));
    if (opts.children.length > 1) body.append(opts);
    if (ip.script || ip.experts?.length) {
      const sc = deps.section("스크립트");
      if (ip.script) {
        const where = [kit.comfy && "ComfyUI API", kit.flowRec ? `$${kit.flowRec.crew.handle} 워크플로우 연결됨` : "연결된 워크플로우 없음"].filter(Boolean).join(" · ");
        const meta = deps.el("div", "v4g-mono");
        meta.append(deps.raw(deps.el("div", "", ip.script_name || "script")), deps.el("div", "", where));
        sc.append(meta);
      }
      if (ip.experts?.length) {
        const row = deps.el("div", "v4g-chips");
        ip.experts.forEach((e) => row.append(deps.raw(deps.el("span", "v4g-chip", e))));
        sc.append(row);
      }
      if (ip.script) {
        sc.querySelector("h3").append(copyButton(ip.script));
        const code = deps.el("pre", "code-box");
        code.append(deps.raw(deps.el("div", "code-name", ip.script_name || "script")), deps.raw(deps.el("code", "", ip.script)));
        sc.append(code);
        const a = deps.el("div", "actions");
        a.append(kit.comfy
          ? deps.button("ComfyUI로 만들기", "small", null, (e) => kit.comfyMake(e.currentTarget))
          : deps.button("스크립트 내려받기", "small", null, kit.download));
        // 스크립트 실행: the linked workflow gets the filled prompt with this script and runs it (the app itself runs no code).
        const ran = deps.el("div", "v4g-out");
        if (kit.flowRec && !kit.comfy) a.prepend(deps.button("스크립트 실행", "small", null, (e) => kit.run(e.currentTarget, ran)));
        sc.append(a, ran);
        if (!kit.flowRec && !kit.comfy) sc.append(deps.el("p", "v4g-hint", "쓰는 워크플로우($이름)를 정하면 그 워크플로우가 스크립트를 실행해요."));
      }
      body.append(sc);
    }
    const foot = deps.el("div", "v4g-foot");
    const links = deps.el("div", "actions");
    links.append(deps.button("고치기", "ghost", null, () => openPromptEditor(p)), deps.button("복제", "ghost", null, () => duplicatePrompt(p)), deps.button("보관함에서 빼기", "ghost", null, () => archivePrompt(p)), deps.gallery.deleteButton("prompts", p, "ghost"));
    foot.append(links, deps.el("p", "meta", `${deps.shortDate(p.updated_at)} 수정 · ${p.created_by || ""}`));
    body.append(foot);
  }

  // 프롬프트 실행: fill the {변수}, see the finished prompt, then make it here or send it to ComfyUI.
  function renderPromptRun(p) {
    const kit = promptKit(p);
    const ip = kit.ip;
    pageBack(ip.kind === "slide" ? "슬라이드" : "이미지", () => { deps.state.promptRun = null; deps.render(); });
    ["#controls", "#chips", "#stats"].forEach((q) => deps.$(q).replaceChildren());
    const page = deps.$("#table");
    page.className = "v4g-run";
    const vars = deps.el("section", "v4g-card");
    vars.append(deps.el("h3", "", "변수 채우기"), deps.el("p", "v4g-hint", kit.vars.length ? `${kit.vars.join("·")} 자리에 들어갈 값을 적어요.` : "이 프롬프트에는 변수가 없어요. 그대로 써요."));
    const done = valueBox(kit.filled(), "v4g-filled");
    if (kit.vars.length) {
      const grid = deps.el("div", "v4g-grid2");
      kit.vars.forEach((v) => grid.append(labeled(v.slice(1, -1), varInput(kit, v, () => { done.textContent = kit.filled(); }), "label")));
      vars.append(grid);
    }
    vars.append(labeled("완성된 프롬프트", done));
    const out = deps.el("div", "v4g-out");
    out.id = "prompt-run-out";
    vars.append(out);
    const cards = [vars];
    if (kit.comfy) {
      const c = deps.el("section", "v4g-card v4g-comfy");
      const head = deps.el("div", "v4g-card-head");
      const url = deps.pages.prefs.comfyUrl;
      head.append(deps.el("strong", "", "ComfyUI"), deps.el("span", `v4g-badge${url ? " on" : ""}`, url ? "주소 저장됨" : "주소 없음"), deps.button("연결 설정", "small", null, async () => {
        const next = deps.trimSlash(await askText("ComfyUI 주소를 넣어 주세요. 서버에서 열 수 있는 주소여야 해요.", url || "http://127.0.0.1:8188") ?? url ?? "");
        if (next === (url || "")) return;
        deps.pages.prefs.comfyUrl = next;
        deps.pages.savePrefs();
        deps.render(false);
      }));
      const acts = deps.el("div", "actions");
      if (ip.script) acts.append(deps.button("ComfyUI로 만들기", "small", null, (e) => kit.comfyMake(e.currentTarget)));
      acts.append(deps.button("ComfyUI에서 열기", "small", null, kit.comfyOpen));
      c.append(head, deps.el("p", "v4g-hint", url ? `${url} · 워크플로우에 프롬프트를 넣어 대기열에 올려요.` : "ComfyUI 주소를 넣으면 워크플로우에 프롬프트를 넣어 대기열에 올려요."), acts);
      cards.push(c);
    }
    page.replaceChildren(...cards);
  }

  function promptRunDetail(p) {
    const kit = promptKit(p);
    const ip = kit.ip;
    const body = deps.detailShell("실행");
    body.classList.add("v4g-detail");
    const s = deps.section("스크립트와 결과");
    if (ip.script) {
      const a = deps.el("div", "v4g-stack");
      a.append(copyButton(ip.script, "스크립트 복사", "small"), deps.button(ip.kind === "slide" ? "스크립트 내려받기 (PPTX 만들기)" : "스크립트 내려받기", "small", null, kit.download));
      s.append(valueBox(ip.script_name || "script", "mono"), a);
    } else s.append(deps.el("p", "v4g-hint", "이 프롬프트에는 스크립트가 없어요."));
    s.append(deps.el("div", "v4g-note", kit.flowRec ? `$${kit.flowRec.crew.handle} 워크플로우에 맡겨요.` : "연결된 워크플로우가 없으면 프롬프트를 복사해요."));
    const k = deps.section("보관함");
    const acts = deps.el("div", "actions");
    acts.append(deps.button("고치기", "small", null, () => openPromptEditor(p)), deps.button("복제", "small", null, () => duplicatePrompt(p)), deps.button("보관함에서 빼기", "small", null, () => archivePrompt(p)), deps.gallery.deleteButton("prompts", p));
    k.append(acts, deps.el("p", "meta", `수정 날짜 · ${deps.shortDate(p.updated_at)} / 만든 사람 · ${p.created_by || ""}`));
    body.append(s, k);
  }

  // The 이 프롬프트로 만들기 button on top of the 실행 page.
  function runPrompt(btn) {
    const p = deps.state.data.prompts.find((x) => x.id === deps.state.promptRun);
    if (p) void promptKit(p).run(btn, deps.$("#prompt-run-out"));
  }

  const promptDialog = deps.$("#prompt-dialog");

  let editing = null;

  // 이미지: up to four pictures, shrunk here so each data URL stays under the server's 512 KiB.
  const MAX_IMAGES = 4, MAX_IMAGE_CHARS = 512 * 1024;

  let dialogImages = [];

  function drawDialogImages() {
    const box = deps.$("#p-images");
    box.replaceChildren(...dialogImages.map((src, i) => {
      const t = deps.el("div", "img-thumb");
      const img = deps.el("img"); img.src = src; img.alt = `이미지 ${i + 1}`;
      const x = deps.el("button", "icon-btn plain"); x.type = "button"; x.setAttribute("aria-label", "이미지 빼기"); x.append(deps.icon("x"));
      x.addEventListener("click", () => { dialogImages.splice(i, 1); drawDialogImages(); });
      t.append(img, x);
      return t;
    }));
    if (dialogImages.length < MAX_IMAGES) {
      const add = deps.el("button", "img-add"); add.type = "button"; add.append(deps.el("span", "", "파일 선택 또는 이미지 붙여 넣기"));
      add.addEventListener("click", () => deps.$("#p-image-file").click());
      box.append(add);
    }
  }

  async function shrinkImage(file) {
    const bmp = await createImageBitmap(file);
    try {
      for (const side of [1280, 1024, 768, 512]) {
        const k = Math.min(1, side / Math.max(bmp.width, bmp.height));
        const c = document.createElement("canvas");
        c.width = Math.round(bmp.width * k); c.height = Math.round(bmp.height * k);
        c.getContext("2d").drawImage(bmp, 0, 0, c.width, c.height);
        const url = c.toDataURL("image/webp", 0.85);
        if (url.length <= MAX_IMAGE_CHARS) return url;
      }
      throw new Error("too_large");
    } finally { bmp.close(); }
  }

  async function addImages(files) {
    const pics = [...files].filter((f) => f.type.startsWith("image/"));
    // Shrinking is slow: a later pick or another prompt opened meanwhile must not push past four or into it.
    const list = dialogImages;
    deps.$("#prompt-error").textContent = pics.length > MAX_IMAGES - list.length ? `이미지는 ${MAX_IMAGES}장까지 넣을 수 있어요.` : "";
    for (const f of pics) {
      if (list !== dialogImages || list.length >= MAX_IMAGES) return;
      try {
        const url = await shrinkImage(f);
        if (list === dialogImages && list.length < MAX_IMAGES) list.push(url);
      } catch (_) { deps.$("#prompt-error").textContent = `${f.name || "이미지"}를 줄이지 못해 넣지 않았어요.`; }
      drawDialogImages();
    }
  }

  deps.$("#p-image-file").addEventListener("change", (e) => { void addImages(e.target.files); e.target.value = ""; });


  // 반짝이 (이름 옆): AI reads the prompt and the fields already filled, names the prompt, and fills the empty
  // category·what to leave out·size; the size comes from the first image's shape when there is one. Only on click.
  const SIZES = [[1, "1024×1024"], [2 / 3, "1024×1536"], [3 / 2, "1536×1024"], [3 / 4, "768×1024"], [4 / 3, "1024×768"], [9 / 16, "1080×1920"], [16 / 9, "1920×1080"]];

  const empty = (id) => !deps.$(id).value.trim();

  const autoNote = (t) => { deps.$("#prompt-auto").textContent = t; };

  function imageSize(src) {
    return new Promise((done) => {
      const img = new Image();
      img.onload = () => {
        const r = img.naturalWidth / img.naturalHeight;
        done(SIZES.reduce((a, b) => (Math.abs(Math.log(b[0] / r)) < Math.abs(Math.log(a[0] / r)) ? b : a), SIZES[0])[1]);
      };
      img.onerror = () => done("");
      img.src = src;
    });
  }

  let nameSeq = 0;

  async function nameWithAI() {
    const text = deps.$("#p-prompt").value.trim();
    if (!text) { autoNote("프롬프트를 먼저 넣어 주세요."); deps.$("#p-prompt").focus(); return; }
    const btn = deps.$("#p-title-ai");
    const seq = ++nameSeq, titleBefore = deps.$("#p-title").value, firstImage = dialogImages[0];
    btn.disabled = true;
    autoNote("AI가 이름을 정하는 중이에요…");
    const v = (id) => deps.$(id).value.trim();
    const known = [...new Set(deps.state.data.prompts.flatMap((x) => x.image_prompt.tags || []))].slice(0, 60);
    const ask = `An image or slide generation prompt is being saved to a library. Read it, name it, and fill only the empty fields.\n`
      + `Shape: {"title": "Korean name that shows the content, <= 30 chars", "tags": ["1-3 categories"], "negative": "what the prompt says to avoid, as an English comma list, or empty", "size": "a size or ratio given in the prompt as 1024×1536, or empty"}\n`
      + `Prefer existing categories: ${known.join(", ") || "(none)"}\n\n`
      + `Kind: ${v("#p-kind")}\nModel: ${v("#p-engine")}\nCategories: ${v("#p-tags") || "(empty)"}\nAvoid: ${v("#p-negative") || "(empty)"}\nSize: ${v("#p-size") || "(empty)"}\n\nPrompt:\n${text.slice(0, 6000)}`;
    try {
      const [a, size] = await Promise.all([
        deps.crew.assist("json", ask, { persona: { crew: { instructions: "You keep an image prompt library tidy. Read the prompt and fill only the fields to save." } } }),
        firstImage && empty("#p-size") ? imageSize(firstImage) : ""
      ]);
      if (seq !== nameSeq || !promptDialog.open) return; // the dialog was closed or reopened meanwhile
      if (deps.$("#p-prompt").value.trim() !== text) { autoNote("기다리는 동안 프롬프트가 바뀌어 답을 쓰지 않았어요. 다시 눌러 주세요."); return; }
      const out = a.status === "done" ? deps.crew.parseJSON(a.output) : null;
      if (!out?.title) { autoNote(a.status === "done" ? "AI가 이름을 정하지 못했어요. 직접 넣어 주세요." : deps.crew.assistError(a)); return; }
      if (deps.$("#p-title").value === titleBefore) deps.$("#p-title").value = String(out.title).slice(0, 120); // typed meanwhile: keep it
      const put = (id, val) => { if (val && empty(id)) deps.$(id).value = String(val).slice(0, Number(deps.$(id).maxLength) || 200); };
      put("#p-tags", Array.isArray(out.tags) ? out.tags.join(", ") : out.tags);
      put("#p-negative", out.negative);
      put("#p-size", (dialogImages[0] === firstImage && size) || out.size); // the first image changed meanwhile: not its shape
      autoNote("AI가 이름과 빈 칸을 채웠어요. 맞게 고쳐서 저장하세요.");
    } catch (e) {
      if (seq === nameSeq) autoNote(`AI가 답하지 못했어요 (${e.message}). 직접 넣어 주세요.`);
    } finally {
      if (seq === nameSeq) btn.disabled = false;
    }
  }

  deps.$("#p-title-ai").addEventListener("click", nameWithAI);

  promptDialog.addEventListener("paste", (e) => {
    const files = [...(e.clipboardData?.files || [])];
    if (!files.some((f) => f.type.startsWith("image/"))) return;
    e.preventDefault();
    void addImages(files);
  });

  // 종류·모델 as pills over the two selects (the form still reads the selects).
  function pillsFor(select, box, label) {
    const draw = () => box.replaceChildren(...[...select.options].map((o) => {
      const b = deps.el("button", `pill-opt${o.value === select.value ? " active" : ""}`);
      b.type = "button";
      b.setAttribute("role", "radio");
      b.setAttribute("aria-checked", String(o.value === select.value));
      b.append(label(o));
      b.addEventListener("click", () => { select.value = o.value; draw(); });
      return b;
    }));
    select.addEventListener("change", draw);
    return draw;
  }

  const drawKindPills = pillsFor(deps.$("#p-kind"), deps.$("#p-kind-pills"), (o) => o.textContent);

  const drawEnginePills = pillsFor(deps.$("#p-engine"), deps.$("#p-engine-pills"), (o) => engineLabel(o.value, ENGINES[o.value] || o.textContent));

  function openPromptEditor(p) {
    editing = p;
    const ip = p?.image_prompt || { kind: deps.state.imageTab, engine: "gemini" };
    deps.$("#prompt-dialog-title").textContent = p ? "프롬프트 고치기" : "프롬프트 추가";
    deps.$("#prompt-dialog-back span").textContent = deps.state.imageTab === "slide" ? "슬라이드" : "이미지";
    const set = (id, v) => { deps.$(id).value = v ?? ""; };
    set("#p-title", ip.title); set("#p-kind", ip.kind); set("#p-engine", engineOf(ip)); set("#p-tags", (ip.tags || []).join(", "));
    deps.fillList("#p-tag-list", deps.state.data.prompts.flatMap((x) => x.image_prompt.tags || []));
    dialogImages = [...(ip.images || [])];
    drawDialogImages(); set("#p-prompt", ip.prompt); set("#p-negative", ip.negative);
    set("#p-size", ip.size); set("#p-count", ip.count || ""); set("#p-seed", ip.seed); set("#p-script-name", ip.script_name); set("#p-script", ip.script);
    set("#p-experts", (ip.experts || []).join(", "));
    deps.$("#prompt-error").textContent = "";
    nameSeq++; autoNote(""); deps.$("#p-title-ai").disabled = false;
    drawKindPills(); drawEnginePills();
    promptDialog.showModal();
  }

  deps.$("#prompt-form").addEventListener("submit", async (e) => {
    if (e.submitter?.value !== "save") return;
    e.preventDefault();
    const val = (id) => deps.$(id).value.trim();
    const prompt = {
      title: val("#p-title"), kind: val("#p-kind"), engine: val("#p-engine"), prompt: val("#p-prompt"), negative: val("#p-negative"),
      size: val("#p-size"), count: Number(val("#p-count")) || 0, seed: val("#p-seed"), script_name: val("#p-script-name"), script: deps.$("#p-script").value,
      experts: val("#p-experts").split(",").map((x) => x.trim()).filter(Boolean),
      tags: [...new Set(val("#p-tags").split(",").map((x) => x.trim()).filter(Boolean))], images: dialogImages
    };
    try {
      const saved = editing
        ? await deps.request(`/v1/prompts/${encodeURIComponent(editing.id)}`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: editing.revision, prompt }) })
        : await deps.request("/v1/prompts", { method: "POST", body: JSON.stringify({ scope: deps.state.scope, prompt }) });
      promptDialog.close();
      deps.state.data.prompts = (await deps.request(`/v1/prompts?${deps.query()}`)).items || [];
      deps.state.imageTab = saved.image_prompt.kind;
      deps.state.selected = `prompt:${saved.id}`;
      deps.render();
      deps.notice("저장했어요.");
    } catch (err) {
      deps.$("#prompt-error").textContent = { secret_detected: "키나 토큰처럼 보이는 값이 있어 저장하지 않았어요.", invalid_request: "이름·프롬프트·카테고리(40자까지)를 확인해 주세요.", revision_conflict: "다른 곳에서 먼저 고쳤어요. 닫고 다시 열어 주세요." }[err.message] || `저장하지 못했어요 (${err.message}).`;
    }
  });

  async function archivePrompt(p) {
    if (!await ask(`'${p.image_prompt?.title || "이 프롬프트"}'를 보관함에서 뺄까요? 목록에서 사라지지만 기록은 남아요.`)) return;
    try {
      await deps.request(`/v1/prompts/${encodeURIComponent(p.id)}`, { method: "POST", body: JSON.stringify({ scope: deps.state.scope, expected_revision: p.revision, archive: true }) });
      deps.state.data.prompts = (await deps.request(`/v1/prompts?${deps.query()}`)).items || [];
      deps.state.selected = null;
      deps.render();
      deps.notice("보관함에서 뺐어요. 기록은 남아 있어요.");
    } catch (err) {
      deps.notice(`처리하지 못했어요: ${err.message}`);
    }
  }

  return {
    get copyButton() { return copyButton; },
    get copyTitle() { return copyTitle; },
    get imagesSummary() { return imagesSummary; },
    get openPromptEditor() { return openPromptEditor; },
    get pageBack() { return pageBack; },
    get paneSearch() { return paneSearch; },
    get promptDetail() { return promptDetail; },
    get promptRunDetail() { return promptRunDetail; },
    get renderImages() { return renderImages; },
    get runPrompt() { return runPrompt; }
  };
}
