// @ts-check
"use strict";
// Catalog: install a skill from GitHub (agent_tools request kind skill_install, applied by the host)
// and one-click MCP server presets for the "MCP 서버 추가" dialog.
(() => {
  // Well-known MCP servers, each checked against its official README (2026-10).
  const PRESETS = [
    { id: "filesystem", name: "filesystem", how: "stdio", command: "npx -y @modelcontextprotocol/server-filesystem /path/to/allowed/dir", edit: "/path/to/allowed/dir",
      desc: "정한 폴더 안의 파일을 읽고 써요. 마지막 경로를 열어 줄 폴더로 바꿔 주세요.", link: "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem", needs: "Node.js (npx)" },
    { id: "fetch", name: "fetch", how: "stdio", command: "uvx mcp-server-fetch",
      desc: "웹 페이지를 가져와 마크다운으로 읽어요.", link: "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch", needs: "uv (uvx)" },
    { id: "memory", name: "memory", how: "stdio", command: "npx -y @modelcontextprotocol/server-memory",
      desc: "지식 그래프 형태의 간단한 기억 저장소예요.", link: "https://github.com/modelcontextprotocol/servers/tree/main/src/memory", needs: "Node.js (npx)" },
    { id: "playwright", name: "playwright", how: "stdio", command: "npx @playwright/mcp@latest",
      desc: "Playwright로 브라우저를 열고 페이지를 조작해요.", link: "https://github.com/microsoft/playwright-mcp", needs: "Node.js (npx)" },
    { id: "github", name: "github", how: "http", url: "https://api.githubcopilot.com/mcp/",
      desc: "GitHub 공식 원격 서버예요. 추가한 뒤 에이전트에서 로그인해요.", link: "https://github.com/github/github-mcp-server" },
    { id: "context7", name: "context7", how: "http", url: "https://mcp.context7.com/mcp",
      desc: "라이브러리의 최신 문서와 예제를 찾아 줘요.", link: "https://github.com/upstash/context7" },
  ];
  // The skill folders the host may install into (internal/host/skills/skillinstall.go).
  const TARGETS = [["claude_code", "Claude Code", "~/.claude/skills"], ["codex", "Codex", "~/.codex/skills"], ["agents", "공유 폴더", "~/.agents/skills"], ["zcode", "ZCode", "~/.zcode/skills"]];

  const SEG = /^[A-Za-z0-9_.-]{1,100}$/;
  const REF = /^[A-Za-z0-9_./-]{1,100}$/;
  // parseSource turns owner/repo[/path][@ref] or a github.com address into "owner/repo[/path][@ref]", or "" when invalid.
  function parseSource(raw) {
    let s = String(raw || "").trim().replace(/^https?:\/\//, "");
    let ref = "";
    const url = /^(www\.)?github\.com\//.test(s);
    if (url) s = s.slice(s.indexOf("/") + 1).split(/[?#]/)[0];
    else if (s.includes("@")) { ref = s.slice(s.lastIndexOf("@") + 1); s = s.slice(0, s.lastIndexOf("@")); }
    let parts = s.replace(/^\/+/, "").split("/");
    while (parts.length > 1 && parts.at(-1) === "") parts.pop(); // trailing slashes, without a backtracking /\/+$/
    if (parts.length < 2) return "";
    const owner = parts[0], repo = parts[1].replace(/\.git$/, "");
    let rest = parts.slice(2);
    if (url && rest.length >= 2 && (rest[0] === "tree" || rest[0] === "blob")) {
      ref = rest[1]; rest = rest.slice(2);
      if (rest.at(-1) === "SKILL.md") rest = rest.slice(0, -1);
    } else if (url && rest.length) return "";
    if (!/^[A-Za-z0-9_.-]{1,39}$/.test(owner) || ref && (!REF.test(ref) || ref.includes(".."))) return "";
    if ([repo, ...rest].some((x) => !SEG.test(x) || x === "." || x === "..")) return "";
    return [owner, repo, ...rest].join("/") + (ref ? `@${ref}` : "");
  }
  const size = (n) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)}MB` : n >= 1024 ? `${Math.round(n / 1024)}KB` : `${n}B`);
  const readDetail = (q) => { try { return JSON.parse(q?.detail || "null"); } catch (_) { return null; } };

  function factory({ state, el, button, request, notice, agentLogo, reload, pollTools }) {
    const post = (body) => request("/v1/agent_tools/requests", { method: "POST", body: JSON.stringify({ scope: state.scope, enabled: true, ...body }) });
    // settle waits for the host (it applies requests about once a minute) until every id is finished; null on timeout.
    async function settle(ids, alive, tries = 30) {
      for (let i = 0; i < tries && alive(); i++) {
        await new Promise((r) => setTimeout(r, 5000));
        const t = (await request(`/v1/agent_tools?${new URLSearchParams(Object.entries(state.scope).filter(([, v]) => v !== "")).toString()}`).catch(() => null))?.agent_tools;
        const mine = (t?.requests || []).filter((q) => ids.includes(q.id));
        if (mine.length === ids.length && mine.every((q) => q.status !== "pending")) { if (state.data.tools) state.data.tools.requests = t.requests; return mine; }
      }
      return null;
    }
    const latest = (r, kind, name) => (r.agent_tools?.requests || []).findLast((q) => q.kind === kind && q.name === name);

    function openInstall() {
      const dlg = el("dialog", "crew-dialog");
      const form = el("form");
      form.method = "dialog";
      const head = el("div", "dialog-head"), t = el("div", "");
      t.append(el("h2", "", "GitHub에서 스킬 설치"), el("p", "", "owner/repo 또는 owner/repo/경로, GitHub 주소를 넣어요. 받은 파일은 실행하지 않아요."));
      head.append(t);
      const src = el("input", "mono");
      src.placeholder = "anthropics/skills/skills/pdf";
      const field = (label, input) => { const l = el("label", "field", label); l.append(input); return l; };
      const status = el("p", "meta", "");
      const preview = el("div", "");
      const body = el("div", "dialog-body");
      body.append(field("저장소", src), status, preview);
      const foot = el("div", "dialog-foot");
      const cancel = button("닫기", "ghost"); cancel.type = "submit"; cancel.value = "cancel";
      const look = button("미리보기", ""); look.type = "button";
      const ok = button("설치", "primary"); ok.type = "button"; ok.hidden = true;
      foot.append(cancel, look, ok);
      form.append(head, body, foot);
      let found = null;
      const picked = new Set(["claude_code"]);
      const alive = () => dlg.open;
      look.addEventListener("click", async () => {
        const name = parseSource(src.value);
        if (!name) { window.ArkismNeed(src, "GitHub 주소를 owner/repo 또는 owner/repo/경로 형태로 넣어 주세요."); return; }
        look.disabled = true; ok.hidden = true; found = null; preview.replaceChildren();
        status.textContent = "서버가 저장소를 내려받아 확인하고 있어요. 1분쯤 걸려요…";
        try {
          const r = await post({ agent: "shared", kind: "skill_install", name, enabled: false });
          const mine = latest(r, "skill_install", name);
          const q = mine && (await settle([mine.id], alive))?.[0];
          if (!alive()) return;
          if (!q) { status.textContent = "아직 답이 없어요. 에이전트 동기화가 켜져 있는지 확인해 주세요."; return; }
          if (q.status === "failed") { status.textContent = `확인하지 못했어요: ${q.error || ""}`; return; }
          found = readDetail(q);
          if (!found?.skills?.length) { status.textContent = "이 경로에서 SKILL.md를 찾지 못했어요."; return; }
          showPreview();
        } catch (err) {
          status.textContent = `요청하지 못했어요 (${err.message}).`;
        } finally { look.disabled = false; }
      });
      function showPreview() {
        const files = found.skills.reduce((n, s) => n + s.files, 0), bytes = found.skills.reduce((n, s) => n + s.bytes, 0);
        status.textContent = `${found.source} · 커밋 ${found.commit.slice(0, 7)} · 파일 ${files}개 · ${size(bytes)}`;
        const list = el("div", "");
        for (const s of found.skills) {
          const row = el("div", "field");
          row.append(el("strong", "mono", s.name), el("p", "meta", s.description || "설명 없음"),
            el("p", "meta", `파일 ${s.files}개 · ${size(s.bytes)}${s.installed?.length ? ` · 이미 있음: ${s.installed.map((k) => TARGETS.find(([t]) => t === k)?.[1] || k).join(", ")}` : ""}`));
          list.append(row);
        }
        const agents = el("div", "crew-callers");
        for (const [k, n, dir] of TARGETS) {
          const b = el("button", `crew-caller${picked.has(k) ? " on" : ""}`);
          b.type = "button"; b.title = dir;
          const spec = { claude_code: "img:claude.svg", codex: "sym:a-codex", zcode: "sym:a-zcode" }[k] || "sym:i-stack";
          b.append(agentLogo(spec, n), n);
          b.addEventListener("click", () => { picked.has(k) ? picked.delete(k) : picked.add(k); b.classList.toggle("on", picked.has(k)); });
          agents.append(b);
        }
        const update = el("input"); update.type = "checkbox";
        const updateL = el("label", "meta"); updateL.append(update, " 이미 있는 스킬은 이 버전으로 바꾸기 (업데이트)");
        const hasOld = found.skills.some((s) => s.installed?.length);
        updateL.hidden = !hasOld;
        const where = el("label", "field", "넣을 에이전트"); where.append(agents);
        preview.replaceChildren(list, where, updateL);
        ok.hidden = false;
        ok.onclick = async () => {
          if (!picked.size) { notice("넣을 에이전트를 골라 주세요."); return; }
          const name = `${found.source}@${found.commit}`;
          ok.disabled = true;
          status.textContent = "설치를 요청했어요. 서버가 1분 안에 설치해요…";
          try {
            const r = await post({ agent: "shared", kind: "skill_install", name, targets: TARGETS.map(([k]) => k).filter((k) => picked.has(k)), update: update.checked });
            const mine = latest(r, "skill_install", name);
            const q = mine && (await settle([mine.id], () => true))?.[0];
            if (!q) { notice("설치 결과를 아직 받지 못했어요. 잠시 뒤 스킬 목록을 확인해 주세요."); return; }
            if (q.status === "failed") { if (alive()) status.textContent = `설치하지 못했어요: ${q.error || ""}`; else notice(`설치하지 못했어요: ${q.error || ""}`); return; }
            const done = readDetail(q)?.installed || [];
            notice(`스킬을 설치했어요: ${done.join(", ")}. 목록에는 몇 분 안에 보여요.`);
            if (alive()) dlg.close();
            reload?.();
          } catch (err) {
            status.textContent = `요청하지 못했어요 (${err.message}).`;
          } finally { ok.disabled = false; }
        };
      }
      dlg.append(form);
      dlg.addEventListener("close", () => dlg.remove());
      document.body.append(dlg);
      dlg.showModal();
      src.focus();
    }

    // installPage: the same preview → install flow as openInstall, drawn as a page (Figma 290:18908).
    // side is the right-hand detail: the picked skill and the agents it goes to. alive() is false once the page is gone.
    function installPage(side, alive) {
      const page = el("div", "v4s-install");
      const top = el("form", "v4s-install-bar");
      const src = el("input", "v4s-input mono"); src.placeholder = "github.com/owner/repo 또는 owner/repo/경로"; src.setAttribute("aria-label", "저장소");
      const look = button("불러오기", ""); look.type = "submit";
      // A zip goes to the host inside the request (base64), so it must fit the server's 160KB cap.
      let zip = null;
      const pickZip = button("zip 파일", "ghost", null, () => {
        const input = el("input"); input.type = "file"; input.accept = ".zip,application/zip";
        input.addEventListener("change", async () => {
          const f = input.files?.[0];
          if (!f) return;
          if (f.size > 160 * 1024) { status.textContent = "zip이 160KB보다 커요. GitHub에 올려 주소로 설치해 주세요."; return; }
          const bytes = new Uint8Array(await f.arrayBuffer());
          let bin = "";
          for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
          const name = f.name.trim();
          zip = { name: name && new TextEncoder().encode(name).length <= 128 ? name : "skill.zip", data: btoa(bin) };
          void preview();
        });
        input.click();
      });
      top.append(src, look, pickZip);
      const status = el("p", "v4s-status", "");
      const list = el("div", "v4s-list"); list.hidden = true;
      const updateL = el("label", "v4s-check"); const update = el("input"); update.type = "checkbox";
      updateL.append(update, " 이미 있는 스킬은 이 버전으로 바꾸기 (업데이트)"); updateL.hidden = true;
      const foot = el("div", "v4s-install-foot"); foot.hidden = true;
      const count = el("strong", "");
      const cancel = button("취소", "ghost", null, () => { found = null; pick = 0; update.checked = false; list.hidden = foot.hidden = updateL.hidden = true; status.textContent = ""; list.replaceChildren(); drawSide(); });
      const ok = button("설치", "primary");
      foot.append(count, cancel, ok);
      page.append(top, status, list, updateL, foot);
      let found = null, pick = 0;
      const picked = new Set(["claude_code"]);
      const where = (k) => TARGETS.find(([t]) => t === k);
      function drawSide() {
        const s = found?.skills?.[pick];
        if (!s) {
          side.replaceChildren(el("p", "v4s-lead", "owner/repo, owner/repo/경로 또는 GitHub 주소를 넣거나 zip 파일을 골라요. 서버가 SKILL.md를 찾아요. 받은 파일은 실행하지 않아요."));
          return;
        }
        const head = el("div", "v4s-head");
        head.append(el("span", "v4s-kind mono", s.name), el("h2", "", s.description || s.name));
        const facts = el("dl", "v4s-facts");
        for (const [k, v] of [["파일", `${s.files}개 · ${size(s.bytes)}`], [zip ? "SHA-256" : "커밋", found.commit.slice(0, 7)], ["이미 있음", (s.installed || []).map((k) => where(k)?.[1] || k).join(", ") || "없음"]]) facts.append(el("dt", "", k), el("dd", "", v));
        const check = el("section", "v4s-sec"); check.append(el("h3", "", "설치 전 확인"), facts);
        const agents = el("div", "v4s-agent-chips");
        for (const [k, n] of TARGETS) {
          const b = el("button", `v4s-agent${picked.has(k) ? " on" : " off"}`); b.type = "button"; b.title = where(k)[2];
          const spec = { claude_code: "img:claude.svg", codex: "sym:a-codex", zcode: "sym:a-zcode" }[k] || "sym:i-stack";
          b.append(agentLogo(spec, n), el("span", "", n));
          b.addEventListener("click", () => { picked.has(k) ? picked.delete(k) : picked.add(k); drawSide(); });
          agents.append(b);
        }
        const to = el("section", "v4s-sec"); to.append(el("h3", "", "설치할 에이전트"), agents, el("p", "meta", "저장소에 든 스킬을 모두 같은 곳에 넣어요."));
        const loc = el("section", "v4s-sec"); loc.append(el("h3", "", "위치"));
        for (const k of TARGETS.map(([t]) => t).filter((t) => picked.has(t))) loc.append(el("p", "mono v4s-loc", `${where(k)[2]}/${s.name}`));
        side.replaceChildren(head, check, to, loc);
      }
      function drawList() {
        const files = found.skills.reduce((n, s) => n + s.files, 0), bytes = found.skills.reduce((n, s) => n + s.bytes, 0);
        const n = found.skills.length, pin = found.commit.slice(0, 7);
        status.textContent = zip ? `스킬 ${n}개 찾음 · ${found.source} · SHA-256 ${pin} · 파일 ${files}개 · ${size(bytes)}` : `스킬 ${n}개 찾음 · ${found.source} · 커밋 ${pin} · 파일 ${files}개 · ${size(bytes)}`;
        list.replaceChildren(...found.skills.map((s, i) => {
          const r = el("button", `row v4s-row${i === pick ? " selected" : ""}`); r.type = "button";
          const text = el("div", "v4s-text");
          text.append(el("div", "v4s-title v4s-name", s.name), el("div", "v4s-meta", s.description || "설명 없음"));
          r.append(text, s.installed?.length ? el("span", "v4s-uses", "이미 있음") : el("span", "v4s-badge", "새 스킬"));
          r.addEventListener("click", () => { pick = i; drawList(); drawSide(); });
          return r;
        }));
        list.hidden = foot.hidden = false;
        updateL.hidden = !found.skills.some((s) => s.installed?.length);
        count.textContent = `스킬 ${found.skills.length}개`;
      }
      top.addEventListener("submit", (e) => {
        e.preventDefault();
        // A bad address leaves a zip preview as it was; only a valid one replaces it.
        if (!parseSource(src.value)) { window.ArkismNeed(src, "GitHub 주소를 owner/repo 또는 owner/repo/경로 형태로 넣어 주세요."); return; }
        zip = null;
        void preview();
      });
      async function preview() {
        const name = zip ? zip.name : parseSource(src.value);
        if (!name) { window.ArkismNeed(src, "GitHub 주소를 owner/repo 또는 owner/repo/경로 형태로 넣어 주세요."); return; }
        look.disabled = pickZip.disabled = true; found = null; pick = 0; update.checked = false; list.hidden = foot.hidden = updateL.hidden = true; drawSide();
        status.textContent = zip ? "서버가 zip을 확인하고 있어요. 1분쯤 걸려요…" : "서버가 저장소를 내려받아 확인하고 있어요. 1분쯤 걸려요…";
        try {
          const r = await post({ agent: "shared", kind: "skill_install", name, enabled: false, ...(zip ? { zip: zip.data } : {}) });
          const mine = latest(r, "skill_install", name);
          const q = mine && (await settle([mine.id], alive))?.[0];
          if (!alive()) return;
          if (!q) { status.textContent = "아직 답이 없어요. 에이전트 동기화가 켜져 있는지 확인해 주세요."; return; }
          if (q.status === "failed") { status.textContent = `확인하지 못했어요: ${q.error || ""}`; return; }
          found = readDetail(q);
          if (!found?.skills?.length) { found = null; status.textContent = "이 경로에서 SKILL.md를 찾지 못했어요."; return; }
          drawList(); drawSide();
        } catch (err) {
          status.textContent = `요청하지 못했어요 (${err.message}).`;
        } finally { look.disabled = pickZip.disabled = false; }
      }
      ok.addEventListener("click", async () => {
        if (!found) return;
        if (!picked.size) { notice("넣을 에이전트를 골라 주세요."); return; }
        const name = zip ? zip.name : `${found.source}@${found.commit}`;
        ok.disabled = true;
        status.textContent = "설치를 요청했어요. 서버가 1분 안에 설치해요…";
        try {
          const r = await post({ agent: "shared", kind: "skill_install", name, targets: TARGETS.map(([k]) => k).filter((k) => picked.has(k)), update: !updateL.hidden && update.checked, ...(zip ? { zip: zip.data } : {}) });
          const mine = latest(r, "skill_install", name);
          const q = mine && (await settle([mine.id], () => true))?.[0];
          if (!q) { notice("설치 결과를 아직 받지 못했어요. 잠시 뒤 스킬 목록을 확인해 주세요."); return; }
          if (q.status === "failed") { if (alive()) status.textContent = `설치하지 못했어요: ${q.error || ""}`; else notice(`설치하지 못했어요: ${q.error || ""}`); return; }
          const done = readDetail(q)?.installed || [];
          notice(`스킬을 설치했어요: ${done.join(", ")}. 목록에는 몇 분 안에 보여요.`);
          reload?.();
        } catch (err) {
          status.textContent = `요청하지 못했어요 (${err.message}).`;
        } finally { ok.disabled = false; }
      });
      drawSide();
      return page;
    }

    // presetField: a picker that fills the MCP dialog's name, method and program/address in one click.
    function presetField(inputs) {
      const { name, how, cmd, url, sync } = inputs;
      const pick = el("select");
      const none = el("option", "", "직접 입력"); none.value = ""; pick.append(none);
      for (const p of PRESETS) { const o = el("option", "", `${p.name} — ${p.how === "http" ? "주소" : "프로그램"}`); o.value = p.id; pick.append(o); }
      const about = el("p", "meta", "");
      const link = el("a", "", "");
      link.target = "_blank"; link.rel = "noreferrer";
      pick.addEventListener("change", () => {
        const p = PRESETS.find((x) => x.id === pick.value);
        about.replaceChildren();
        if (!p) return;
        name.value = p.name;
        how.value = p.how;
        if (p.how === "http") url.value = p.url; else cmd.value = p.command;
        sync();
        link.href = p.link; link.textContent = p.link.replace("https://", "");
        about.append(p.desc, p.needs ? ` 필요: ${p.needs}.` : "", " ", link);
        if (p.edit) { cmd.focus(); cmd.setSelectionRange(cmd.value.indexOf(p.edit), cmd.value.length); }
      });
      const l = el("label", "field", "자주 쓰는 서버");
      l.append(pick, about);
      return l;
    }

    // watchAdd: after an mcp_add, tell the owner when the host could not find the program (npx, uvx, docker…).
    // r is the answer to the last mcp_add POST: it lists every request still waiting, the new ones included.
    async function watchAdd(r, name) {
      const ids = (r?.agent_tools?.requests || []).filter((q) => q.kind === "mcp_add" && q.name === name && q.status === "pending").map((q) => q.id);
      const done = ids.length ? await settle(ids, () => true) : null;
      if (!done) return;
      const missing = [...new Set(done.filter((q) => q.status === "done").map(readDetail).filter((d) => d?.warning === "command_missing").map((d) => d.command))];
      if (missing.length) notice(`${name}을(를) 추가했지만 서버에서 ${missing.join(", ")} 프로그램을 찾지 못했어요. 설치해야 동작해요.`);
      pollTools?.();
    }

    return { openInstall, installPage, installButton: () => button("GitHub에서 설치", "", "plus", openInstall), presetField, watchAdd };
  }
  factory.parseSource = parseSource;
  factory.PRESETS = PRESETS;
  factory.TARGETS = TARGETS;
  factory.size = size;
  window.ArkismCatalog = factory;
})();
