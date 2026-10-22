// Git (repositories of the ARKISM_GIT_ORG owner): GitKraken-style branch graph, commit/PR detail and bounded GitHub
// changes. The server runs git and gh over the owner's SSH; this file only draws.
window.ArkismGit = (ctx) => {
  "use strict";
  const { state, $, el, button, tauri, ago, notice, go, headExtras, openReview, codegraph } = ctx;
  const HOST_KEY = "arkism.gitHost", REPO_KEY = "arkism.gitRepo";
  const read = (k, d) => { try { return localStorage.getItem(k) || d; } catch { return d; } };
  const keep = (k, v) => { try { localStorage.setItem(k, v); } catch { /* per-viewer convenience only */ } };
  const LANE = 14, ROW = 36, COLORS = ["#f5b82e", "#4cc38a", "#58a6ff", "#d872e6", "#39c5cf", "#ff8a5c", "#a5d64c", "#f778ba"];
  const MESSAGES = {
    git_state_changed: "그사이 브랜치가 바뀌었어요. 새로고침 후 다시 확인하세요.", git_conflict: "충돌이 나서 아무것도 올리지 않았어요.",
    git_not_mergeable: "지금은 병합할 수 없어요(충돌·초안·닫힘).", git_branch_not_merged: "기본 브랜치에 아직 합쳐지지 않은 브랜치는 지우지 않아요.",
    git_branch_has_open_pr: "열린 PR이 있는 브랜치예요. PR을 먼저 닫거나 병합하세요.", git_default_branch: "기본 브랜치는 지울 수 없어요.",
    git_branch_exists: "같은 이름의 브랜치가 이미 있어요.", git_invalid_branch: "브랜치 이름을 확인하세요.", git_timeout: "시간이 너무 걸려 멈췄어요. 다시 시도하세요.",
    git_org_unset: "서버에 보여 줄 GitHub 계정이 정해지지 않았어요. SSH로 실행되는 셸에 ARKISM_GIT_ORG(GitHub 사용자·조직 이름)를 넣어 주세요 (예: ~/.bashrc 맨 위에 export).",
  };
  const S = { host: read(HOST_KEY, ""), repos: null, repo: read(REPO_KEY, ""), graph: null, prs: null, runs: null, query: "",
    sel: null, detail: null, diff: null, busy: "", error: "", open: { branches: true, tags: false, prs: true, checks: true }, prGroup: "open", mode: "graph", prQuery: "" };
  let epoch = 0;

  async function call(action, extra = {}) {
    if (!tauri) throw new Error("desktop_only");
    try {
      const reply = await tauri.invoke("git_op", { host: S.host || state.defaults.ssh_host, request: { action, ...extra } });
      if (!reply || reply.authority !== "data_only_untrusted") throw new Error("git_unavailable");
      return reply;
    } catch (e) {
      const code = String(e?.message || e || "");
      throw new Error(MESSAGES[code] || (code.startsWith("git_") ? `요청을 처리하지 못했어요 (${code}).` : "서버에 연결하지 못했어요. SSH 연결을 확인하세요."));
    }
  }
  const live = (ticket) => ticket === epoch;
  function paint() { if (state.view === "git") render(); }

  async function load(refresh = false) {
    const ticket = ++epoch;
    S.busy = "불러오는 중…"; S.error = ""; paint();
    try {
      if (!S.repos || refresh) {
        const repos = (await call("repos")).repos;
        if (!live(ticket)) return;
        S.repos = repos;
      }
      if (!live(ticket)) return;
      if (!S.repos.some((r) => r.name === S.repo)) S.repo = (S.repos.find((r) => !r.archived) || S.repos[0] || {}).name || "";
      if (!S.repo) { S.busy = ""; paint(); return; }
      // The graph draws as soon as it arrives; PRs and checks (GitHub, slower) fill in after.
      const repo = S.repo;
      const side = (action, key) => call(action, { repo, refresh }).then((v) => { if (live(ticket) && S.repo === repo) { S[key] = v; paint(); } },
        () => { if (live(ticket) && S.repo === repo) { S[key] = { failed: true }; paint(); } });
      S.prs = null; S.runs = null;
      void side("prs", "prs"); void side("runs", "runs");
      const graph = await call("graph", { repo, limit: 300, refresh, query: S.query });
      if (!live(ticket)) return;
      Object.assign(S, { graph, busy: "" });
    } catch (e) {
      if (live(ticket)) { S.busy = ""; S.error = e.message; }
    }
    paint();
  }
  async function more() {
    const ticket = epoch;
    S.busy = "더 불러오는 중…"; paint();
    try {
      const next = await call("graph", { repo: S.repo, limit: 300, skip: S.graph.commits.length, query: S.query });
      if (!live(ticket)) return;
      S.graph = { ...S.graph, commits: S.graph.commits.concat(next.commits), more: next.more };
    } catch (e) { notice(e.message); }
    S.busy = ""; paint();
  }

  // ------------------------------------------------------------ lanes (computed from parents, like GitKraken)
  function lanes(commits) {
    const open = [], out = [];
    for (const c of commits) {
      let col = open.indexOf(c.sha);
      if (col < 0) { col = open.indexOf(null); if (col < 0) { col = open.length; open.push(null); } }
      const before = open.slice();
      const incoming = before.flatMap((s, i) => (s === c.sha ? [i] : []));
      incoming.forEach((i) => { open[i] = null; });
      const parents = c.parents.map((p, k) => {
        let pc = open.indexOf(p);
        if (pc < 0) { pc = k === 0 ? col : open.indexOf(null); if (pc < 0) pc = open.length; open[pc] = p; }
        return pc;
      });
      while (open.length && open[open.length - 1] === null) open.pop();
      out.push({ col, before, after: open.slice(), incoming, parents });
    }
    return out;
  }
  const laneColor = (i) => COLORS[i % COLORS.length];
  // Lanes stretch with the row (a row with branch labels is taller); the commit dot sits on top at the row's middle.
  function graphCell(row, width, initial, tipped) {
    const ns = "http://www.w3.org/2000/svg", svg = document.createElementNS(ns, "svg");
    svg.setAttribute("width", width); svg.setAttribute("viewBox", `0 0 ${width} ${ROW}`); svg.setAttribute("preserveAspectRatio", "none"); svg.setAttribute("aria-hidden", "true");
    const x = (i) => LANE / 2 + i * LANE, mid = ROW / 2, color = laneColor(row.col);
    const line = (x1, y1, x2, y2, c) => {
      const p = document.createElementNS(ns, "path");
      p.setAttribute("d", x1 === x2 ? `M${x1} ${y1}V${y2}` : `M${x1} ${y1}C${x1} ${(y1 + y2) / 2} ${x2} ${(y1 + y2) / 2} ${x2} ${y2}`);
      p.setAttribute("stroke", c); p.setAttribute("stroke-width", "1.5"); p.setAttribute("stroke-opacity", ".55"); p.setAttribute("fill", "none");
      p.setAttribute("vector-effect", "non-scaling-stroke"); svg.append(p);
    };
    row.before.forEach((sha, i) => { if (sha && !row.incoming.includes(i)) line(x(i), 0, x(i), ROW, laneColor(i)); });
    row.incoming.forEach((i) => line(x(i), 0, x(row.col), mid, laneColor(i)));
    row.parents.forEach((pc) => line(x(row.col), mid, x(pc), ROW, laneColor(pc)));
    const box = el("span", "git-lanes");
    const dot = el("i", `git-node${tipped ? " tip" : ""}`);
    dot.style.left = `${x(row.col)}px`;
    if (!tipped) dot.style.background = color;
    dot.title = initial;
    box.append(svg, dot);
    return box;
  }

  // ------------------------------------------------------------ left: branches, tags, PRs, checks
  const branches = () => (S.graph?.refs || []).filter((r) => r.kind === "branch");
  const tags = () => (S.graph?.refs || []).filter((r) => r.kind === "tag");
  const pulls = () => S.prs?.pulls || [];
  const prFor = (branch) => pulls().find((p) => p.state === "open" && p.head === branch);
  function block(key, title, count, body) {
    const sec = el("section", "git-side-sec"), head = el("button", "git-side-head");
    head.type = "button"; head.setAttribute("aria-expanded", String(S.open[key]));
    head.append(el("span", "", title), el("output", "", String(count)));
    head.addEventListener("click", () => { S.open[key] = !S.open[key]; paint(); });
    sec.append(head);
    if (S.open[key]) sec.append(body());
    return sec;
  }
  function refItem(label, title, active, onClick, extra, mono = true) {
    const b = el("button", `git-side-item${active ? " on" : ""}${mono ? " mono" : ""}`); b.type = "button"; b.title = title;
    b.append(el("span", "git-ellipsis", label)); if (extra) b.append(extra);
    b.addEventListener("click", onClick); return b;
  }
  function branchTree() {
    const box = el("div", "git-side-list"), folders = new Map(), def = S.graph?.repo?.default_branch;
    for (const b of branches()) {
      const cut = b.name.indexOf("/"), folder = cut > 0 ? b.name.slice(0, cut) : "";
      if (!folders.has(folder)) folders.set(folder, []);
      folders.get(folder).push(b);
    }
    const item = (b, label) => {
      const pr = prFor(b.name);
      return refItem(label, b.name, S.sel?.type === "branch" && S.sel.name === b.name, () => select({ type: "branch", name: b.name, sha: b.sha }),
        pr ? el("span", "git-tag", `#${pr.number}`) : b.name === def ? el("span", "git-tag", "기본") : null);
    };
    (folders.get("") || []).forEach((b) => box.append(item(b, b.name)));
    [...folders.keys()].filter(Boolean).sort((a, b) => Number(a > b) - Number(a < b)).forEach((f) => {
      const d = el("details", "git-folder"); d.open = folders.get(f).some((b) => S.sel?.name === b.name);
      const sum = el("summary", "");
      sum.append(el("span", "", `${f}/`), el("output", "", String(folders.get(f).length)));
      d.append(sum);
      folders.get(f).forEach((b) => d.append(item(b, b.name.slice(f.length + 1))));
      box.append(d);
    });
    return box;
  }
  const PR_GROUPS = [["open", "열린 PR", (p) => p.state === "open"], ["mine", "내 PR", (p) => p.mine && p.state === "open"],
    ["assigned", "나에게 배정", (p) => p.assigned && p.state === "open"], ["review", "내 리뷰 대기", (p) => p.review_requested && p.state === "open"], ["all", "전체", () => true]];
  function prTabs(all) {
    const tabs = el("div", "git-pr-tabs");
    PR_GROUPS.filter(([k, , f]) => all || k === "open" || k === S.prGroup || pulls().some(f)).forEach(([k, label, f]) => {
      const t = el("button", `git-chip${S.prGroup === k ? " on" : ""}`, `${label} ${pulls().filter(f).length}`); t.type = "button";
      t.addEventListener("click", () => { S.prGroup = k; paint(); }); tabs.append(t);
    });
    return tabs;
  }
  function prList() {
    const box = el("div", "git-side-list");
    box.append(prTabs(false));
    if (!S.prs || S.prs.failed) { box.append(el("p", "git-empty", S.prs?.failed ? "PR을 불러오지 못했어요. 가져오기로 다시 시도하세요." : "PR을 불러오는 중…")); return box; }
    const f = PR_GROUPS.find((g) => g[0] === S.prGroup)[2];
    const rows = pulls().filter(f).slice(0, 40);
    if (!rows.length) box.append(el("p", "git-empty", "해당하는 PR이 없어요."));
    rows.forEach((p) => {
      const b = el("button", `git-side-pr${S.sel?.type === "pr" && S.sel.number === p.number ? " on" : ""}`); b.type = "button"; b.title = p.title;
      const t = el("span", "git-side-pr-text");
      t.append(el("small", "", `#${p.number}`), el("span", "", p.title));
      b.append(checkDot(p.state === "merged" ? "merged" : p.checks), t);
      b.addEventListener("click", () => select({ type: "pr", number: p.number }));
      box.append(b);
    });
    return box;
  }
  function checkDot(s) {
    const map = { success: ["ok", "통과"], failure: ["bad", "실패"], pending: ["run", "진행 중"], merged: ["merged", "병합됨"], none: ["", "검사 없음"] };
    const [cls, label] = map[s] || map.none, d = el("span", `git-dot ${cls}`); d.title = label; d.setAttribute("aria-label", label); return d;
  }
  function checksList() {
    const box = el("div", "git-side-list"), latest = new Map();
    if (!S.runs || S.runs.failed) { box.append(el("p", "git-empty", S.runs?.failed ? "검사 기록을 불러오지 못했어요." : "검사 기록을 불러오는 중…")); return box; }
    for (const r of S.runs?.runs || []) if (!latest.has(r.workflow)) latest.set(r.workflow, r);
    if (!latest.size) box.append(el("p", "git-empty", "최근 검사가 없어요."));
    latest.forEach((r) => {
      const history = el("span", "git-history");
      (S.runs.runs.filter((x) => x.workflow === r.workflow).slice(0, 8).reverse()).forEach((x) => history.append(checkDot(x.status !== "completed" ? "pending" : x.conclusion === "success" ? "success" : "failure")));
      const b = refItem(r.workflow, `${r.workflow} · ${r.branch}`, S.sel?.type === "run" && S.sel.id === r.id, () => select({ type: "run", id: r.id }), history, false);
      b.prepend(checkDot(r.status !== "completed" ? "pending" : r.conclusion === "success" ? "success" : "failure"));
      box.append(b);
    });
    return box;
  }

  // ------------------------------------------------------------ center: graph rows
  function center() {
    const box = el("section", "git-center"), commits = S.graph?.commits || [];
    const head = el("div", "git-row git-row-head");
    head.append(el("span", "git-graph", "그래프"), el("span", "git-msg", "브랜치 / 커밋 메시지"), el("span", "git-sha", ""), el("span", "git-time", "시간"));
    box.append(head);
    if (!commits.length) { box.append(el("p", "git-empty", S.query ? "검색 결과가 없어요." : "커밋이 없어요.")); return box; }
    const rows = lanes(commits), width = Math.max(Math.min(Math.max(...rows.map((r) => Math.max(r.before.length, r.after.length, r.col + 1))), 10) * LANE, 28);
    head.firstChild.style.width = `${width}px`;
    const bySha = new Map(); for (const r of S.graph.refs) { if (!bySha.has(r.sha)) bySha.set(r.sha, []); bySha.get(r.sha).push(r); }
    const list = el("div", "git-rows");
    commits.forEach((c, i) => {
      const refs = bySha.get(c.sha) || [];
      const on = S.sel?.type === "commit" && S.sel.sha === c.sha;
      const row = el("button", `git-row${on ? " on" : ""}${refs.length ? " tipped" : ""}`); row.type = "button";
      row.title = c.author;
      const g = el("span", "git-graph"); g.style.width = `${width}px`;
      g.append(graphCell(rows[i], width, c.author || "?", refs.length > 0));
      const msg = el("span", "git-msg");
      if (refs.length) {
        const r = el("span", "git-refs");
        refs.slice(0, 2).forEach((x) => r.append(el("span", `git-ref ${x.kind}`, x.name)));
        if (refs.length > 2) r.append(el("span", "git-ref more", `+${refs.length - 2}`));
        msg.append(r);
      }
      msg.append(el("span", "git-ellipsis git-subject", c.subject));
      row.append(g, msg, el("span", "git-sha", c.sha.slice(0, 7)), el("span", "git-time", ago(new Date(c.time * 1000).toISOString())));
      row.addEventListener("click", () => select({ type: "commit", sha: c.sha }));
      list.append(row);
    });
    box.append(list);
    if (S.graph.more) box.append(button("커밋 더 보기", "", "", more));
    return box;
  }

  // ------------------------------------------------------------ right: detail and actions
  async function select(sel) {
    S.sel = sel; S.detail = null; S.diff = null; paint();
    const ticket = epoch;
    try {
      let detail = null;
      if (sel.type === "commit") detail = await call("commit", { repo: S.repo, sha: sel.sha });
      if (sel.type === "pr") detail = await call("pr", { repo: S.repo, number: sel.number });
      if (sel.type === "branch") detail = await call("commit", { repo: S.repo, sha: sel.sha });
      if (sel.type === "run") detail = S.runs.runs.find((r) => r.id === sel.id);
      if (live(ticket) && S.sel === sel) S.detail = detail;
    } catch (e) { if (live(ticket) && S.sel === sel) S.detail = { error: e.message }; }
    paint();
  }
  function facts(pairs) {
    const dl = el("dl", "git-facts");
    pairs.filter(([, v]) => v !== undefined && v !== null && v !== "").forEach(([k, v, cls]) => { dl.append(el("dt", "", k)); const dd = el("dd", cls || ""); dd.append(v); dl.append(dd); });
    return dl;
  }
  function files(list, sha) {
    const box = el("div", "git-files");
    list.forEach((f) => {
      const b = el("button", `git-file${S.diff?.path === f.path ? " on" : ""}`); b.type = "button";
      const n = el("span", "git-num");
      if (f.added != null) n.append(el("span", "add", `+${f.added}`), el("span", "del", `−${f.deleted}`)); else n.append(el("span", "", "이진"));
      b.append(el("span", "git-ellipsis", f.old_path ? `${f.old_path} → ${f.path}` : f.path), n);
      if (sha) b.addEventListener("click", () => openDiff(sha, f.path)); else b.disabled = true;
      box.append(b);
    });
    return box;
  }
  async function openDiff(sha, path) {
    const loading = { path, loading: true };
    S.diff = loading; paint();
    try { const d = await call("diff", { repo: S.repo, sha, path }); if (S.diff === loading) S.diff = d; }
    catch (e) { if (S.diff === loading) S.diff = { path, error: e.message }; }
    paint();
  }
  function diffView() {
    const box = el("section", "git-diff");
    box.append(el("h4", "git-diff-path", S.diff.path));
    if (S.diff.loading) { box.append(el("p", "git-empty", "변경을 불러오는 중…")); return box; }
    if (S.diff.error) { box.append(el("p", "git-error", S.diff.error)); return box; }
    const pre = el("pre", "git-patch");
    S.diff.patch.split("\n").forEach((line) => {
      if (/^(diff --git|index |--- |\+\+\+ )/.test(line)) return;
      pre.append(el("span", line.startsWith("@@") ? "hunk" : line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : "", `${line}\n`));
    });
    box.append(pre);
    if (S.diff.truncated) box.append(el("p", "git-empty", "변경이 커서 앞부분만 보여요. GitHub에서 전체를 보세요."));
    return box;
  }
  async function act(label, action, payload, done) {
    if (S.busy) return;
    S.busy = label; paint();
    try {
      const out = await call(action, { repo: S.repo, ...payload });
      notice(done(out));
      S.busy = "";
      await load(true);
      return out;
    } catch (e) { S.busy = ""; notice(e.message); paint(); }
  }
  // A form window (Figma 05 · 브랜치·PR 만들기 198:4758, 커밋·병합 작업 198:4844, 확인 팝업 241:27867).
  function ask(title, fields, confirmLabel, onOk, lead = "") {
    const dlg = el("dialog", `git-dialog${fields.length ? "" : " confirm"}`), form = el("form"); form.method = "dialog";
    const head = el("div", "git-dialog-head");
    head.append(el("h3", "", title));
    if (lead) head.append(el("p", "", lead));
    form.append(head);
    const inputs = {};
    let pair = null;
    fields.forEach((f) => {
      const label = el("label", `git-field${f.type === "checkbox" ? " toggle" : ""}`);
      let input;
      if (f.options) { input = el("select"); f.options.forEach(([v, t]) => { const o = el("option", "", t); o.value = v; input.append(o); }); }
      else if (f.area) input = el("textarea");
      else { input = el("input"); input.type = f.type || "text"; }
      if (f.value != null) input.value = f.value;
      if (f.type === "checkbox") { input.checked = !!f.value; input.setAttribute("role", "switch"); }
      if (f.placeholder) input.placeholder = f.placeholder;
      inputs[f.name] = input;
      if (f.type === "checkbox") {
        const t = el("span", "git-field-text");
        t.append(el("span", "", f.label));
        if (f.hint) t.append(el("small", "", f.hint));
        label.append(t, input);
        form.append(label);
        return;
      }
      label.append(el("span", "git-field-label", f.label), input);
      if (f.hint) label.append(el("small", "git-hint", f.hint));
      // 올릴 브랜치 → 합칠 곳 sit on one line.
      if (f.pair === "start") { pair = el("div", "git-pair"); pair.append(label); form.append(pair); return; }
      if (f.pair === "end" && pair) { pair.append(el("span", "git-pair-arrow", "→"), label); pair = null; return; }
      form.append(label);
    });
    const row = el("div", "git-dialog-actions"), cancel = button("취소", ""), ok = button(confirmLabel, fields.length ? "primary" : "danger");
    cancel.type = "button"; cancel.addEventListener("click", () => dlg.close());
    ok.type = "button"; ok.addEventListener("click", () => {
      const values = Object.fromEntries(Object.entries(inputs).map(([k, i]) => [k, i.type === "checkbox" ? i.checked : i.value.trim()]));
      dlg.close(); onOk(values);
    });
    row.append(cancel, ok); form.append(row); dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg); dlg.showModal();
  }
  const branchOptions = () => branches().map((b) => [b.name, b.name]);
  function newBranch(sha, from) {
    ask(`새 브랜치 (${from || sha.slice(0, 7)}에서)`, [{ name: "name", label: "브랜치 이름", placeholder: "feat/ui-completion", hint: "GitHub에 바로 만들어져요." }], "새 브랜치 만들기",
      (v) => act("브랜치 만드는 중…", "branch_create", { name: v.name, sha }, (o) => `브랜치 ${o.created}을(를) 만들었어요.`));
  }
  function newPr(head) {
    const def = S.graph?.repo?.default_branch || "main";
    ask("PR 만들기", [{ name: "head", label: "올릴 브랜치", options: branchOptions().filter(([n]) => n !== def), value: head, pair: "start" },
      { name: "base", label: "합칠 곳", options: branchOptions(), value: def, pair: "end" }, { name: "title", label: "제목" }, { name: "body", label: "설명", area: true },
      { name: "draft", label: "초안으로 만들기", type: "checkbox", hint: "초안은 리뷰 요청과 병합이 막혀 있어요." }], "PR 만들기",
    (v) => act("PR 만드는 중…", "pr_create", v, (o) => `PR을 만들었어요: ${o.url}`), "올릴 브랜치를 합칠 곳으로 보내는 요청이에요.");
  }
  function applyCommit(kind, sha) {
    const def = S.graph?.repo?.default_branch || "main";
    const containing = (S.detail?.branches || [])[0] || def;
    const subject = (S.detail?.message || "").split("\n")[0];
    ask(kind === "revert" ? "커밋 되돌리기" : "다른 브랜치로 가져오기",
      [{ name: "branch", label: "적용할 브랜치", options: branchOptions(), value: kind === "revert" ? containing : "", hint: `기본 브랜치(${def})를 고르면 새 브랜치와 PR로 올려요. 다른 브랜치에는 바로 올려요.` }],
      kind === "revert" ? "되돌리기" : "가져오기",
      (v) => {
        const head = branches().find((b) => b.name === v.branch);
        if (!head) return notice("브랜치를 찾지 못했어요.");
        void act(kind === "revert" ? "되돌리는 중…" : "가져오는 중…", kind === "revert" ? "revert" : "cherry_pick", { sha, branch: v.branch, expected_sha: head.sha },
          (o) => (o.url ? `새 브랜치 ${o.branch}와 PR을 만들었어요.` : `${o.branch}에 올렸어요.`));
      }, kind === "revert" ? `${sha.slice(0, 7)}${subject ? ` · ${subject}` : ""}` : "같은 커밋을 다른 브랜치에도 적용해요.");
  }
  const reviewOf = (number) => (state.data?.reviews?.items || []).find((x) => x.number === number && String(x.repo || "").split("/").pop() === S.repo);
  function aiFindings(number) {
    const it = reviewOf(number);
    if (!it) return null;
    const open = (it.findings || []);
    const sec = card(`AI 리뷰 · 지적 ${open.length}건`);
    open.slice(0, 20).forEach((f) => {
      const c = el("div", "git-finding");
      const top = el("div", "git-finding-top");
      top.append(el("span", "git-badge warn", f.severity === "blocking" ? "심각" : "권고"), el("code", "", `${f.path}:${f.line}`));
      c.append(top, el("p", "", f.text));
      sec.append(c);
    });
    const more_ = button("코드 리뷰에서 자세히", "", null, () => openReview(`review:${it.repo}#${it.number}`));
    more_.title = "AI로 고치기·다시 검사";
    sec.append(more_);
    return sec;
  }
  function card(title, aside) {
    const s = el("section", "git-card");
    const h = el("div", "git-card-head");
    h.append(el("h4", "", title));
    if (aside) h.append(el("small", "", aside));
    s.append(h);
    return s;
  }
  function sec(title, aside) {
    const s = el("section", "git-sec");
    const h = el("div", "git-sec-head");
    h.append(el("h4", "", title));
    if (aside) h.append(el("small", "", aside));
    s.append(h);
    return s;
  }
  function prState(d) { return d.state === "merged" ? ["", "병합됨"] : d.state === "closed" ? ["warn", "닫힘"] : d.draft ? ["", "초안"] : ["", "열림"]; }
  const CHECK_TEXT = { success: "검사 통과", failure: "검사 실패", pending: "검사 진행 중", merged: "병합됨", none: "검사 없음" };
  function prActions(d) {
    const acts = el("div", "git-actions");
    if (d.state === "open") {
      const merge = button("병합하기", "primary", null, () => ask(`PR #${d.number} 병합`, [{ name: "method", label: "방식", options: [["merge", "병합 커밋"], ["squash", "하나로 합쳐 병합(squash)"]] }],
        "병합하기", (v) => act("병합하는 중…", "pr_merge", { number: d.number, expected_head_sha: d.head_sha, method: v.method }, () => `PR #${d.number}을(를) 병합했어요.`),
        "초안·충돌·검사 실패 상태에서는 병합할 수 없어요."));
      merge.disabled = d.mergeable !== "mergeable" || d.draft;
      if (merge.disabled) merge.title = "충돌·초안·검사 상태를 먼저 해결하세요.";
      acts.append(merge);
    }
    acts.append(button("GitHub에서 열기", "", null, () => window.open(d.url, "_blank")));
    if (d.state === "open") {
      const failed = (S.runs?.runs || []).find((r) => r.sha === d.head_sha && r.status === "completed" && r.conclusion && r.conclusion !== "success");
      if (failed) acts.append(button("실패한 검사 다시", "", null, () => act("다시 돌리는 중…", "rerun", { run_id: failed.id }, () => "실패한 검사를 다시 돌려요.")));
    }
    return acts;
  }
  // The PR's info, actions and description (right column). In PR 상세 the checks, reviews and files sit in the middle.
  function prDetail(box, d, withBody) {
    const [cls, label] = prState(d);
    box.append(el("p", "git-kicker mono", `PR #${d.number}`), el("h3", "", d.title));
    const badges = el("div", "git-badges");
    badges.append(el("span", `git-badge ${cls}`, label));
    const ck = el("span", "git-check-state"); ck.append(checkDot(d.check_summary), CHECK_TEXT[d.check_summary] || CHECK_TEXT.none);
    badges.append(ck);
    box.append(badges, el("p", "git-kicker mono", `${d.head} → ${d.base}`));
    const info = sec("정보");
    info.append(facts([["작성자", d.author], ["바뀐 양", `+${d.additions} −${d.deletions} · 파일 ${d.changed_files} · 커밋 ${d.commits}`],
      ["병합 가능", { mergeable: "가능", conflicting: "충돌", unknown: "확인 중" }[d.mergeable] || d.mergeable, d.mergeable === "conflicting" ? "warn" : ""],
      ["리뷰", { approved: "승인", changes_requested: "수정 요청", review_required: "리뷰 필요" }[d.review] || "없음", ["changes_requested", "review_required"].includes(d.review) ? "warn" : ""],
      ["업데이트", ago(d.updated_at)]]));
    box.append(info, prActions(d));
    if (d.body) { const s = sec("설명"); s.append(el("pre", "git-body", d.body)); box.append(s); }
    if (withBody) prParts(d).forEach((p) => box.append(p));
  }
  function prParts(d) {
    const out = [];
    if (d.checks.length) {
      const s = card("검사", `${d.checks.length}개`);
      const g = el("div", "git-check-grid");
      d.checks.forEach((c) => { const r = el("div", "git-check"); r.append(checkDot(c.state === "success" ? "success" : ["failure", "error", "cancelled", "timed_out"].includes(c.state) ? "failure" : "pending"), el("span", "git-ellipsis", c.name)); g.append(r); });
      s.append(g);
      out.push(s);
    }
    const ai = aiFindings(d.number); if (ai) out.push(ai);
    if (d.reviews.length) {
      const s = card("리뷰", `${d.reviews.length}개`);
      d.reviews.forEach((v) => { const r = el("div", "git-review"); r.append(el("code", "", v.author), el("span", "git-badge", v.state)); if (v.body) r.append(el("p", "", v.body)); s.append(r); });
      out.push(s);
    }
    const fs = card("바뀐 파일", `${d.files.length}개`);
    fs.append(files(d.files, null));
    out.push(fs);
    return out;
  }
  function right() {
    const box = el("div", "git-right"), d = S.detail, sel = S.sel;
    if (!sel) { box.append(el("p", "git-kicker", "저장소"), el("h3", "", S.repo || "저장소"), el("p", "git-note", "커밋·브랜치·PR을 고르면 자세한 내용과 할 수 있는 일이 여기에 나와요.")); return box; }
    if (!d) { box.append(el("p", "git-empty", "불러오는 중…")); return box; }
    if (d.error) { box.append(el("p", "git-error", d.error)); return box; }
    if (sel.type === "commit" || sel.type === "branch") {
      const [subject, ...rest] = d.message.split("\n");
      if (sel.type === "branch") {
        const pr = prFor(sel.name), def = S.graph.repo.default_branch;
        box.append(el("p", "git-kicker", "브랜치"), el("h3", "mono", sel.name));
        const acts = el("div", "git-actions");
        if (pr) acts.append(button(`PR #${pr.number} 보기`, "", null, () => { S.mode = "prs"; void select({ type: "pr", number: pr.number }); }));
        else if (sel.name !== def) acts.append(button("PR 만들기", "primary", null, () => newPr(sel.name)));
        acts.append(button("여기서 새 브랜치", "", null, () => newBranch(sel.sha, sel.name)));
        if (sel.name !== def) acts.append(button("브랜치 지우기", "", null, () => ask(`브랜치 ${sel.name} 지우기`, [], "브랜치 지우기",
          () => act("지우는 중…", "branch_delete", { name: sel.name, expected_sha: sel.sha }, () => `${sel.name}을(를) 지웠어요.`),
          "기본 브랜치나 열린 PR이 있는 브랜치는 지울 수 없어요.")));
        box.append(acts, el("p", "git-kicker", "최근 커밋"));
      } else box.append(el("p", "git-kicker", "커밋"));
      box.append(el("h3", "", subject));
      if (rest.join("\n").trim()) box.append(el("p", "git-note git-msgbody", rest.join("\n").trim()));
      const info = sec("정보");
      info.append(facts([["작성", `${d.author} · ${ago(new Date(d.time * 1000).toISOString())}`], ["커밋", d.sha.slice(0, 7), "mono"],
        ["부모", d.parents.map((p) => p.slice(0, 7)).join(", ") || "없음", "mono"], ["포함 브랜치", d.branches.slice(0, 6).join(", ") + (d.branches.length > 6 ? " …" : ""), "mono"]]));
      box.append(info);
      if (sel.type === "commit") {
        const acts = el("div", "git-actions");
        acts.append(button("여기서 새 브랜치", "", null, () => newBranch(d.sha)), button("되돌리기", "", null, () => applyCommit("revert", d.sha)),
          button("다른 브랜치로 가져오기", "", null, () => applyCommit("cherry-pick", d.sha)));
        box.append(acts);
      }
      const fs = sec("바뀐 파일", `${d.files.length}${d.more_files ? `+${d.more_files}` : ""}`);
      fs.append(files(d.files, d.sha));
      box.append(fs);
      if (S.diff) box.append(diffView());
      return box;
    }
    if (sel.type === "pr") { prDetail(box, d, S.mode !== "prs"); return box; }
    if (sel.type === "run") {
      box.append(el("p", "git-kicker", "검사"), el("h3", "", d.workflow));
      const info = sec("정보");
      info.append(facts([["제목", d.title], ["브랜치", d.branch, "mono"], ["상태", d.status === "completed" ? d.conclusion || "완료" : "진행 중"], ["시작", ago(d.created_at)]]));
      box.append(info);
      const acts = el("div", "git-actions");
      if (d.status === "completed" && d.conclusion && d.conclusion !== "success") acts.append(button("실패한 검사 다시", "primary", null, () => act("다시 돌리는 중…", "rerun", { run_id: d.id }, () => "실패한 검사를 다시 돌려요.")));
      acts.append(button("GitHub에서 열기", "", null, () => window.open(d.url, "_blank")));
      box.append(acts);
    }
    return box;
  }

  // ------------------------------------------------------------ PR 상세 (Figma 198:2694): PR list, then checks · AI 리뷰 · 리뷰 · 바뀐 파일
  function prPage() {
    const box = el("div", "git-prs");
    const bar = el("div", "git-prbar");
    const search = el("input", "git-search"); search.type = "search"; search.placeholder = "PR 제목·번호 찾기"; search.value = S.prQuery || "";
    search.addEventListener("input", () => { S.prQuery = search.value; const at = search.selectionStart; paint(); const s = document.querySelector(".git-prbar .git-search"); if (s) { s.focus(); s.setSelectionRange(at, at); } });
    bar.append(prTabs(false), search);
    box.append(bar);
    if (!S.prs || S.prs.failed) { box.append(el("p", "git-empty", S.prs?.failed ? "PR을 불러오지 못했어요. 가져오기로 다시 시도하세요." : "PR을 불러오는 중…")); return box; }
    const f = PR_GROUPS.find((g) => g[0] === S.prGroup)[2], q = (S.prQuery || "").trim().toLowerCase();
    const rows = pulls().filter(f).filter((p) => !q || `#${p.number} ${p.title}`.toLowerCase().includes(q)).slice(0, 40);
    const list = el("div", "git-prlist");
    rows.forEach((p) => {
      const on = S.sel?.type === "pr" && S.sel.number === p.number;
      const b = el("button", `git-pr${on ? " on" : ""}`); b.type = "button";
      const t = el("span", "git-pr-text");
      t.append(el("span", "git-ellipsis", p.title), el("small", "git-ellipsis", [`${p.head || ""} → ${p.base || ""}`, p.author, p.updated_at ? ago(p.updated_at) : ""].filter(Boolean).join(" · ")));
      const pending = p.state === "open" && p.checks === "pending";
      b.append(el("span", "git-pr-num", `#${p.number}`), t, pending ? el("span", "git-badge accent", "검사 진행 중") : el("span", "git-badge", prState(p)[1]));
      b.addEventListener("click", () => select({ type: "pr", number: p.number }));
      list.append(b);
    });
    if (!rows.length) list.append(el("p", "git-empty", "해당하는 PR이 없어요."));
    box.append(list);
    if (S.sel?.type === "pr" && S.detail && !S.detail.error) prParts(S.detail).forEach((p) => box.append(p));
    return box;
  }

  // ------------------------------------------------------------ page
  function headButtons() {
    const pick = el("select", "git-repo"); pick.setAttribute("aria-label", "저장소");
    (S.repos || []).forEach((r) => { const o = el("option", "", `${r.name}${r.archived ? " (보관)" : ""}`); o.value = r.name; pick.append(o); });
    pick.value = S.repo;
    pick.addEventListener("change", () => { S.repo = pick.value; keep(REPO_KEY, S.repo); S.sel = null; S.detail = null; S.graph = null; codegraph?.reset(); void load(); });
    const def = S.graph?.repo?.default_branch;
    const head = def && branches().find((b) => b.name === def);
    const out = S.mode === "prs" ? [] : [pick];
    out.push(button("가져오기", "", null, () => load(true)), button("새 브랜치", "", null, () => head && newBranch(head.sha, def)), button("PR 만들기", "primary", null, () => newPr("")));
    return out;
  }
  // 코드 그래프 · PR 영향 (codegraph.js) draw inside this screen; this is what they read from Git.
  const CODE_MODES = ["code", "impact"];
  function codeHost() {
    const def = S.graph?.repo?.default_branch, head = def && branches().find((b) => b.name === def);
    const commits = S.graph?.commits || [];
    return { paint, call, openReview, gitRepo: S.repo, repoUrl: (S.repos || []).find((r) => r.name === S.repo)?.url || "", headSha: head?.sha || "", prs: pulls,
      commitsSince: (sha) => { const i = commits.findIndex((c) => c.sha.startsWith(sha)); return i < 0 ? null : i; },
      setMode: (m) => { S.mode = m; paint(); } };
  }
  function toolbar() {
    const bar = el("div", "git-toolbar");
    const search = el("input", "git-search"); search.type = "search"; search.placeholder = "커밋 메시지·해시 찾기"; search.value = S.query;
    search.addEventListener("keydown", (e) => { if (e.key === "Enter") { S.query = search.value.trim(); void load(); } });
    bar.append(search, button("PR 상세", "", null, () => { S.mode = "prs"; const first = pulls().find((p) => p.state === "open"); if (first && S.sel?.type !== "pr") void select({ type: "pr", number: first.number }); else paint(); }),
      button("코드 리뷰", "", null, () => go("reviews")));
    if (S.busy) bar.append(el("span", "git-busy", S.busy));
    return bar;
  }
  const subText = () => (CODE_MODES.includes(S.mode) ? codegraph.sub(S.mode) : S.mode === "prs" ? "열린 PR의 검사·리뷰·바뀐 파일을 보고 병합해요." : "브랜치·PR·검사 상태와 커밋 기록을 한눈에 봐요.");
  function render() {
    const table = $("#table"), detail = $("#detail");
    $("#title").textContent = S.mode === "prs" ? "PR 상세" : "저장소"; // paint() redraws only this page, so the title follows here too
    table.className = "git-wrap";
    const plain = (text) => { headExtras(); table.replaceChildren(el("p", "git-empty", text)); detail.replaceChildren(); };
    if (!tauri) return plain("Git 화면은 Arkism 데스크톱 앱에서 써요. 서버에 SSH로 연결해 저장소를 읽어요.");
    if (state.mode === "local") return plain("Git 화면은 서버에 연결했을 때 써요. 서버의 저장소를 SSH로 읽어 커밋·PR·검사를 보여 줘요. 설정 › 고급에서 서버에 연결할 수 있어요.");
    if (state.caps && state.caps.role !== "human") return plain("관리 계정에서만 쓸 수 있어요.");
    if (S.repos === null && !S.busy && !S.error) { queueMicrotask(() => load()); return plain("저장소를 불러오는 중…"); }
    const code = codegraph && CODE_MODES.includes(S.mode) && !S.error;
    headExtras({ count: S.repo, back: S.mode === "prs" ? ["저장소", () => { S.mode = "graph"; paint(); }] : null, aside: code ? codegraph.headButtons() : headButtons() });
    const parts = [];
    if (S.error) {
      const box = el("div", "git-error-box"), hostLabel = el("label", "git-field"), host = el("input"); host.value = S.host || state.defaults.ssh_host;
      hostLabel.append(el("span", "git-field-label", "SSH 주소"), host);
      box.append(el("h4", "", "연결 오류"), el("p", "git-error", S.error), hostLabel, button("다시 연결", "", "", () => { S.host = host.value.trim() || state.defaults.ssh_host; keep(HOST_KEY, S.host); S.repos = null; void load(true); }),
        el("p", "git-note", "브랜치가 바뀜 · 충돌 · 같은 이름 · SSH 실패 때는 작업을 다시 확인해요."));
      parts.push(box);
    } else if (code) {
      table.className = "git-wrap cg-wrap";
      const out = codegraph.render(S.mode, codeHost());
      table.replaceChildren(...out.parts);
      detail.replaceChildren(out.detail);
      return;
    } else if (S.mode === "prs" && S.graph) {
      parts.push(prPage());
    } else if (S.graph) {
      if (codegraph) parts.push(codegraph.seg("graph", codeHost()));
      parts.push(toolbar());
      const grid = el("div", "git-grid"), side = el("aside", "git-side");
      side.append(block("branches", "브랜치", branches().length, branchTree), block("prs", "PR", pulls().filter((p) => p.state === "open").length, prList),
        block("checks", "검사", new Set((S.runs?.runs || []).map((r) => r.workflow)).size, checksList), block("tags", "태그", tags().length, () => {
          const box = el("div", "git-side-list"); tags().forEach((t) => box.append(refItem(t.name, t.name, false, () => select({ type: "commit", sha: t.sha })))); return box;
        }));
      grid.append(side, center());
      parts.push(grid);
    } else if (S.busy) parts.push(el("p", "git-empty", S.busy));
    table.replaceChildren(...parts);
    detail.replaceChildren(right());
  }
  return {
    reset: () => { epoch++; codegraph?.reset(); Object.assign(S, { repos: null, graph: null, prs: null, runs: null, sel: null, detail: null, diff: null, busy: "", error: "", mode: "graph" }); },
    views: { git: { title: () => (S.mode === "prs" ? "PR 상세" : "저장소"), keepDetail: true,
      sub: subText,
      render, head: () => [] } },
  };
};
