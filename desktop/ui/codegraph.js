// TOOLS › 저장소 › 코드 그래프 (Figma 377:27520) and PR 영향 (379:27807): a sub-view of the Git screen (git.js).
//   GET  /v1/code_graph/repos                   색인된 저장소: 마지막 색인 커밋·시각·심볼 수·stats
//   POST /v1/code_graph/search|callers|callees|impact   심볼 찾기, 부르는 곳 / 부르는 것 / 바꾸면 영향 받는 곳(깊이 1~8)
//   POST /v1/code_graph/changed                 PR이 바꾼 줄에 걸치는 함수·메서드와 각각의 영향(깊이 2)
// Indexing runs on the host that has the code (`arkism codegraph index --mirror <name>`); the app only shows the command.
// Everything the server returns is data about code (authority data_only_untrusted): drawn as plain text, never as markup.
window.ArkismCodeGraph = (ctx) => {
  "use strict";
  const { state, el, raw, button, request, query, notice, ago } = ctx;
  const KIND = { func: "함수", method: "메서드", type: "타입", var: "변수", const: "상수", package: "패키지", module: "모듈" };
  const CHIPS = ["func", "method", "type", "var", "const"];
  const REASON = { limit: "개수 한도에서 멈춤", token_cap: "길이 한도에서 멈춤", node_cap: "300개에서 멈춤", depth_cap: "깊이 8에서 멈춤" };
  const VIA = { interface: "인터페이스 경유" };
  const KIND_OBJ = { func: "이 함수를", method: "이 메서드를", type: "이 타입을", var: "이 변수를", const: "이 상수를" };
  const CODE_FILE = /\.(go|js|mjs|cjs|jsx|ts|tsx|mts|cts)$/;
  const S = { repos: null, loading: false, error: "", repo: "", q: "", kind: "all", folder: "", sort: "rank", results: null, searching: false,
    sel: null, info: null, cands: null, depth: 3, open: { callers: false, callees: false, impact: false }, showCmd: false,
    pr: { number: 0, detail: null, changed: null, busy: "", error: "", sel: "", chip: "changed", lines: {} } };
  let host = null, epoch = 0, searchEpoch = 0, searchTimer = 0;
  const paint = () => host && host.paint();
  const scopeBody = (extra) => JSON.stringify({ scope: state.scope, repo: S.repo, ...extra });
  const post = (op, extra) => request(`/v1/code_graph/${op}`, { method: "POST", body: scopeBody(extra) });
  const num = (n) => Number(n || 0).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");
  const short = (sha) => (sha || "").slice(0, 7);
  const data = (tag, cls, text) => raw(el(tag, cls, text));
  const lang = (loc) => (/\.go:/.test(loc || "") ? "Go" : /\.tsx?:|\.[mc]ts:/.test(loc || "") ? "TypeScript" : "JavaScript");
  const repoRow = () => (S.repos || []).find((r) => r.repo === S.repo) || null;
  const stats = () => repoRow()?.stats || {};
  const command = () => {
    const mirror = host?.gitRepo || S.repo;
    return `arkism codegraph index --mirror ${mirror}${S.repo && S.repo !== mirror ? ` --repo ${S.repo}` : ""}`;
  };
  const notFound = (e) => /not_found|HTTP 404/.test(String(e?.message || e));

  // ------------------------------------------------------------ data
  async function load(refresh = false) {
    if (S.loading || (S.repos && !refresh)) return;
    S.loading = true; S.error = ""; paint();
    try { S.repos = (await request(`/v1/code_graph/repos?${query()}`)).repos || []; }
    catch (e) { S.error = String(e.message || e); S.repos = S.repos || []; }
    S.loading = false;
    pickRepo(true);
    paint();
  }
  // The code graph repo that goes with the Git screen's repo: same name, or the same last path part.
  function pickRepo(force) {
    if (!S.repos) return;
    const want = host?.gitRepo || "";
    const match = S.repos.find((r) => r.repo === want) || S.repos.find((r) => r.repo.split("/").pop() === want);
    if (force || !S.repos.some((r) => r.repo === S.repo)) {
      const next = (match || (want ? null : S.repos[0]) || {}).repo || "";
      if (next !== S.repo) { S.repo = next; reset(); }
    }
  }
  function reset() {
    epoch++; searchEpoch++;
    Object.assign(S, { results: null, sel: null, info: null, cands: null, q: "", folder: "" });
    Object.assign(S.pr, { changed: null, sel: "", error: "", busy: "", lines: {} });
  }
  async function search(q) {
    S.q = q;
    if (!q.trim() || !S.repo) { S.results = null; paint(); return; }
    const ticket = ++searchEpoch;
    S.searching = true; paint();
    try {
      const out = await post("search", { query: q.trim(), limit: 50, max_tokens: 8000 });
      if (ticket === searchEpoch) S.results = out;
    } catch (e) { if (ticket === searchEpoch) S.results = { items: [], error: String(e.message || e) }; }
    if (ticket === searchEpoch) { S.searching = false; paint(); }
  }
  // Open one symbol (id or name). A name that fits several symbols answers with candidates; the user picks one.
  async function open(ref) {
    const ticket = ++epoch;
    S.sel = ref; S.info = null; S.cands = null; S.open = { callers: false, callees: false, impact: false }; paint();
    try {
      const [callers, callees, impact] = await Promise.all([post("callers", { symbol: ref, limit: 100 }), post("callees", { symbol: ref, limit: 100 }),
        post("impact", { symbol: ref, depth: S.depth, limit: 200, max_tokens: 8000 })]);
      if (ticket !== epoch) return;
      if (!callers.symbol) { S.cands = callers.candidates || []; S.sel = null; }
      else S.info = { symbol: callers.symbol, callers, callees, impact };
    } catch (e) {
      if (ticket !== epoch) return;
      if (notFound(e) && !/^[0-9a-f]{16,64}$/.test(ref)) { S.sel = null; paint(); return; }
      S.info = { error: notFound(e) ? "이 심볼이 색인에 없어요. 다시 색인한 뒤 찾아보세요." : String(e.message || e) };
    }
    paint();
  }
  async function redepth(d) {
    S.depth = d;
    const id = S.info?.symbol?.id;
    if (!id) { paint(); return; }
    const ticket = epoch;
    try {
      const impact = await post("impact", { symbol: id, depth: d, limit: 200, max_tokens: 8000 });
      if (ticket === epoch && S.info?.symbol?.id === id) S.info = { ...S.info, impact };
    } catch (e) { notice(String(e.message || e)); }
    paint();
  }
  function reindex() {
    S.showCmd = true;
    const cmd = command();
    navigator.clipboard?.writeText(cmd).then(() => notice("색인 명령을 복사했어요. 코드가 있는 서버에서 실행하세요."), () => {});
    paint();
  }

  // ------------------------------------------------------------ PR 영향: the PR's diff → changed line ranges → /changed
  // New-side line ranges of a unified diff; a deletion marks the line it sits before. +/− are counted per new line.
  function hunks(patch) {
    const marks = new Map();
    let line = 0;
    for (const t of (patch || "").split("\n")) {
      const h = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(t);
      if (h) { line = Number(h[1]); continue; }
      if (!line || /^(\+\+\+|---) /.test(t)) continue;
      if (t.startsWith("+")) { const m = marks.get(line) || { add: 0, del: 0 }; m.add++; marks.set(line, m); line++; }
      else if (t.startsWith("-")) { const at = Math.max(line, 1), m = marks.get(at) || { add: 0, del: 0 }; m.del++; marks.set(at, m); }
      else if (!t.startsWith("\\")) line++;
    }
    const lines = [...marks.keys()].sort((a, b) => a - b), ranges = [];
    for (const l of lines) {
      const last = ranges[ranges.length - 1];
      if (last && l <= last.end_line + 1) last.end_line = l; else ranges.push({ start_line: l, end_line: l });
    }
    return { ranges, marks };
  }
  async function loadPr(number) {
    const pr = S.pr, ticket = ++epoch;
    Object.assign(pr, { number, detail: null, changed: null, sel: "", error: "", busy: "PR을 불러오는 중…", lines: {} }); paint();
    try {
      const d = await host.call("pr", { repo: host.gitRepo, number });
      if (ticket !== epoch) return;
      pr.detail = d; pr.busy = "바뀐 줄을 읽는 중…"; paint();
      const files = (d.files || []).map((f) => f.path).filter((p) => CODE_FILE.test(p) && !p.endsWith("_test.go")).slice(0, 20);
      const ranges = [];
      const diffs = await Promise.all(files.map((path) => host.call("diff", { repo: host.gitRepo, sha: d.head_sha, base: d.base, path }).catch(() => null)));
      if (ticket !== epoch) return;
      diffs.forEach((df, i) => {
        if (!df) return;
        const { ranges: rs, marks } = hunks(df.patch);
        pr.lines[files[i]] = { marks, patch: df.patch };
        rs.forEach((r) => ranges.push({ path: files[i], ...r }));
      });
      if (!ranges.length) { pr.changed = { items: [], empty: files.length ? "바뀐 줄을 읽지 못했어요." : "이 PR은 색인하는 코드(Go·JS·TS)를 바꾸지 않았어요." }; }
      else {
        pr.busy = "영향을 계산하는 중…"; paint();
        pr.changed = await post("changed", { ranges: ranges.slice(0, 1000), depth: 2 });
      }
      if (ticket !== epoch) return;
      pr.sel = pr.changed.items?.[0]?.symbol?.id || "";
    } catch (e) {
      if (ticket !== epoch) return;
      pr.error = notFound(e) ? "이 저장소는 아직 색인하지 않았어요." : String(e.message || e);
    }
    if (ticket === epoch) { pr.busy = ""; paint(); }
  }
  function lineCount(path, start, end) {
    const m = S.pr.lines[path]?.marks;
    let add = 0, del = 0;
    if (m) for (const [l, c] of m) if (l >= start && l <= end) { add += c.add; del += c.del; }
    return { add, del };
  }

  // ------------------------------------------------------------ pieces
  function seg(mode, h) {
    if (h) host = h;
    const s = el("div", "cg-seg"); s.setAttribute("role", "tablist");
    [["graph", "커밋"], ["code", "코드 그래프"], ["impact", "PR 영향"]].forEach(([k, label]) => {
      const t = el("button", `cg-seg-tab${k === mode ? " on" : ""}`, label); t.type = "button";
      t.setAttribute("role", "tab"); t.setAttribute("aria-selected", String(k === mode));
      t.addEventListener("click", () => host.setMode(k));
      s.append(t);
    });
    return s;
  }
  function headButtons() {
    const pick = el("select", "git-repo cg-repo"); pick.setAttribute("aria-label", "색인된 저장소");
    (S.repos || []).forEach((r) => { const o = data("option", "", r.repo); o.value = r.repo; pick.append(o); });
    if (!repoRow()) { const o = el("option", "", "색인 안 됨"); o.value = ""; pick.prepend(o); }
    pick.value = S.repo;
    pick.disabled = !(S.repos || []).length;
    pick.addEventListener("change", () => { S.repo = pick.value; reset(); paint(); });
    return [pick, button("다시 색인", "", null, reindex)];
  }
  function cmdBox() {
    const box = el("div", "cg-cmd");
    const code = data("code", "", command());
    const copy = button("복사", "", null, () => navigator.clipboard?.writeText(command()).then(() => notice("복사했어요."), () => notice("복사하지 못했어요. 직접 선택해 복사하세요.")));
    box.append(code, copy);
    return box;
  }
  const accuracy = () => el("p", "cg-fine", "Go는 타입 정보로 정확하게 잇고, JS·TS는 이름으로 추정해요(같은 파일·상대 경로 import·this.m()만). 테스트 파일은 기본으로 빼요.");
  function emptyState() {
    const box = el("section", "cg-empty");
    box.append(el("h4", "", "아직 색인하지 않았어요"),
      el("p", "", "코드가 있는 서버에서 이 명령을 한 번 실행하면 함수·메서드가 서로 어떻게 불리는지 볼 수 있어요."), cmdBox(),
      el("p", "cg-fine", "코드는 서버로 가지 않고 심볼·시그니처·호출 관계만 올라갑니다."), accuracy());
    if ((S.repos || []).length) box.append(el("p", "cg-fine", "위 저장소 목록에서 이미 색인한 다른 저장소를 고를 수도 있어요."));
    return box;
  }
  function freshness() {
    const r = repoRow(), head = host.headSha || "";
    if (!r) return { stale: false, behind: null };
    const stale = !!(head && r.commit && !head.startsWith(r.commit) && !r.commit.startsWith(head));
    return { stale, behind: stale ? host.commitsSince(r.commit) : 0 };
  }
  function statusCard() {
    const r = repoRow(), st = stats(), { stale, behind } = freshness();
    const box = el("section", `cg-status${stale ? " stale" : ""}`);
    const text = el("div", "cg-status-text");
    const head = el("p", "cg-status-head");
    head.append(stale ? "색인이 오래됐어요" : "색인 최신", " · 마지막 색인 커밋 ", data("span", "mono", short(r.commit) || "—"), ` · ${ago(r.indexed_at || r.updated_at)}`);
    const langs = [st.go_files ? "Go" : "", st.js_files ? "JS·TS" : ""].filter(Boolean).join("·");
    const meta = [`심볼 ${num(r.symbols)}개`, `파일 ${num(r.files)}개`, `부름 관계 ${num(r.edges)}개`, langs,
      st.mode ? (st.mode === "incremental" ? "바뀐 파일만 색인" : "전체 색인") : "", st.duration_ms ? `${(st.duration_ms / 1000).toFixed(1)}초` : ""].filter(Boolean).join(" · ");
    text.append(head, el("p", "cg-status-meta", meta));
    box.append(el("span", "cg-dot"), text);
    if (st.type_errors) box.append(el("span", "cg-badge warn", `타입 오류 ${num(st.type_errors)}`));
    box.append(button("다시 색인", "", null, reindex));
    const wrap = el("div", "cg-status-wrap");
    wrap.append(box);
    if (S.showCmd) { const c = el("div", "cg-cmd-row"); c.append(el("p", "cg-fine", "앱에서는 색인을 돌리지 않아요. 코드가 있는 서버에서 실행하세요."), cmdBox()); wrap.append(c); }
    if (stale) {
      const card = el("section", "cg-check");
      const t = el("div", "cg-check-text"), h = el("p", "cg-check-head");
      h.append("확인해 주세요", el("span", "cg-check-n", "1"));
      t.append(h, el("p", "", behind ? `마지막 색인 뒤 새 커밋 ${behind}개 · 다시 색인하면 최신 코드로 볼 수 있어요` : "기본 브랜치가 색인한 커밋보다 앞서 있어요 · 다시 색인하면 최신 코드로 볼 수 있어요"));
      card.append(t, button("다시 색인", "primary", null, reindex));
      wrap.append(card);
    }
    return wrap;
  }
  function select(label, options, value, onChange) {
    const s = el("select", "cg-select"); s.setAttribute("aria-label", label);
    options.forEach(([v, t]) => { const o = el("option", "", t); o.value = v; s.append(o); });
    s.value = value;
    s.addEventListener("change", () => onChange(s.value));
    return s;
  }
  function filtered() {
    let items = (S.results?.items || []).filter((h) => (S.kind === "all" || h.kind === S.kind) && (!S.folder || h.loc.startsWith(S.folder + "/")));
    if (S.sort === "name") items = [...items].sort((a, b) => a.qname.localeCompare(b.qname));
    if (S.sort === "path") items = [...items].sort((a, b) => a.loc.localeCompare(b.loc, undefined, { numeric: true }));
    return items;
  }
  function folderOf(loc) { const p = loc.split(":")[0].split("/"); return p.length > 2 ? p.slice(0, 2).join("/") : p.length > 1 ? p[0] : ""; }
  function searchBar() {
    const bar = el("div", "cg-bar");
    const box = el("label", "cg-search");
    const input = el("input"); input.type = "search"; input.placeholder = "함수·타입 이름 찾기 (Enter: 이름으로 바로 열기)"; input.value = S.q;
    input.setAttribute("aria-label", "심볼 찾기");
    input.addEventListener("input", () => { clearTimeout(searchTimer); const v = input.value; searchTimer = setTimeout(() => search(v), 250); });
    input.addEventListener("keydown", (e) => { if (e.key === "Enter" && input.value.trim()) { clearTimeout(searchTimer); void search(input.value); void open(input.value.trim()); } });
    box.append(el("span", "cg-search-icon", "⌕"), input);
    const folders = [...new Set((S.results?.items || []).map((h) => folderOf(h.loc)).filter(Boolean))].sort((a, b) => a.localeCompare(b));
    bar.append(box,
      select("폴더", [["", "폴더: 전체"], ...folders.map((f) => [f, `폴더: ${f}`])], S.folder, (v) => { S.folder = v; paint(); }),
      select("정렬", [["rank", "가까운 순"], ["name", "이름 순"], ["path", "위치 순"]], S.sort, (v) => { S.sort = v; paint(); }));
    return bar;
  }
  function chips() {
    const by = stats().by_kind || {}, box = el("div", "cg-chips");
    const total = CHIPS.reduce((n, k) => n + (by[k] || 0), 0);
    [["all", "전체", total], ...CHIPS.map((k) => [k, KIND[k], by[k] || 0])].forEach(([k, label, n]) => {
      const c = el("button", `cg-chip${S.kind === k ? " on" : ""}`); c.type = "button";
      c.append(label, el("span", "cg-chip-n", num(n)));
      c.addEventListener("click", () => { S.kind = k; paint(); });
      box.append(c);
    });
    return box;
  }
  function symRow(h, on, onClick, right) {
    const b = el("button", `cg-row${on ? " on" : ""}`); b.type = "button";
    const t = el("span", "cg-row-text");
    t.append(data("span", "cg-name", h.qname), data("span", "cg-meta", `${KIND[h.kind] || h.kind} · ${h.loc}`));
    b.append(t);
    if (right) b.append(right);
    b.addEventListener("click", onClick);
    return b;
  }
  function results() {
    const box = el("div", "cg-list-wrap");
    if (!S.q.trim()) {
      const p = el("div", "cg-list cg-hint");
      p.append(el("p", "", "함수·메서드·타입 이름을 찾아보세요."), el("p", "cg-fine", "예: Service.CodeImpact · render · respond — 줄을 누르면 부르는 곳과 영향 받는 곳이 오른쪽에 나와요."));
      box.append(p);
      return box;
    }
    if (S.results?.error) { box.append(el("p", "git-error", S.results.error)); return box; }
    if (!S.results) { box.append(el("p", "git-empty", "찾는 중…")); return box; }
    const items = filtered(), list = el("div", "cg-list");
    items.forEach((h) => list.append(symRow(h, S.info?.symbol?.id === h.id || S.sel === h.id, () => open(h.id), h.exported ? el("span", "cg-quiet", "공개") : null)));
    if (!items.length) list.append(el("p", "git-empty", S.results.items.length ? "이 종류·폴더에 맞는 결과가 없어요." : "찾는 이름이 없어요."));
    box.append(list);
    const foot = [`${num(S.results.items.length)}개 중 ${num(items.length)}개 표시`];
    if (S.results.truncated) foot.push(REASON[S.results.reason] || "일부만 보여요");
    box.append(el("p", "cg-foot", foot.join(" · ")));
    return box;
  }

  // ------------------------------------------------------------ right column
  function hitList(items, openAll, onAll, total) {
    const list = el("div", "cg-mini");
    (openAll ? items : items.slice(0, 3)).forEach((h) => {
      const b = el("button", "cg-mini-row"); b.type = "button";
      const t = el("span", "cg-row-text");
      t.append(data("span", "cg-name", h.qname), data("span", "cg-meta", h.at ? `${h.at}에서` : h.loc));
      b.append(t);
      if (VIA[h.via]) b.append(el("span", "cg-badge", VIA[h.via]));
      b.addEventListener("click", () => open(h.id));
      list.append(b);
    });
    const out = [list];
    if (!openAll && (total || items.length) > 3) {
      const more = el("button", "cg-link", `${num(total || items.length)}곳 모두 보기`); more.type = "button"; more.addEventListener("click", onAll); out.push(more);
    }
    return out;
  }
  function section(title, aside) {
    const s = el("section", "cg-sec"), h = el("div", "cg-sec-head");
    h.append(el("h4", "", title));
    if (aside) h.append(typeof aside === "string" ? el("small", "", aside) : aside);
    s.append(h);
    return s;
  }
  // 깊이별 묶음: 깊이 n · 몇 곳 · 막대 · 이름 몇 개
  function depthBars(items, open_, onPick) {
    const groups = new Map();
    items.forEach((h) => { const d = h.depth || 1; if (!groups.has(d)) groups.set(d, []); groups.get(d).push(h); });
    const max = Math.max(1, ...[...groups.values()].map((g) => g.length)), box = el("div", "cg-depths"), last = Math.max(0, ...groups.keys());
    [...groups.keys()].sort((a, b) => a - b).forEach((d) => {
      const g = groups.get(d), row = el("div", "cg-depth");
      const top = el("div", "cg-depth-top");
      top.append(el("span", "", d === 1 ? "깊이 1 · 바로 부름" : `깊이 ${d}`), el("span", d === last && d > 1 ? "cg-depth-n far" : "cg-depth-n", `${num(g.length)}곳`));
      const bar = el("div", "cg-bar-track"), fill = el("span", d === last && d > 1 ? "far" : ""); fill.style.width = `${Math.max(4, (100 * g.length) / max)}%`; bar.append(fill);
      row.append(top, bar);
      if (open_) {
        const list = el("div", "cg-depth-list");
        g.forEach((h) => { const b = data("button", "cg-depth-name", h.qname); b.type = "button"; b.title = h.loc; b.addEventListener("click", () => onPick(h.id)); list.append(b); });
        row.append(list);
      } else {
        const names = el("p", "cg-depth-names");
        names.append(data("span", "", g.slice(0, 3).map((h) => h.name).join(" · ")));
        if (g.length > 3) names.append(el("span", "", ` 외 ${g.length - 3}`));
        row.append(names);
      }
      box.append(row);
    });
    return box;
  }
  function blobUrl(loc) {
    const base = host.repoUrl, commit = repoRow()?.commit;
    if (!base || !commit || !base.startsWith("https://github.com/")) return "";
    const [path, line] = loc.split(":");
    return `${base.replace(/\/$/, "")}/blob/${commit}/${path.split("/").map(encodeURIComponent).join("/")}#L${line}`;
  }
  function codeButton(loc) {
    const url = blobUrl(loc);
    if (!url) return null;
    return button("코드 열기", "", null, () => window.open(url, "_blank"));
  }
  function symbolDetail() {
    const box = el("div", "git-right cg-right");
    if (S.cands) {
      box.append(el("p", "git-kicker", "이름이 여러 개와 맞아요"), el("h3", "", "어느 심볼인가요?"),
        el("p", "git-note", "같은 이름의 심볼이 여러 개라 하나를 고르면 부르는 곳과 영향을 보여 줘요. 자동으로 고르지 않아요."));
      const list = el("div", "cg-mini");
      S.cands.forEach((h) => list.append(symRow(h, false, () => open(h.id))));
      box.append(list);
      return box;
    }
    if (!S.sel && !S.info) {
      const r = repoRow();
      box.append(el("p", "git-kicker", "코드 그래프"), data("h3", "", r?.repo || host?.gitRepo || "저장소"),
        el("p", "git-note", "함수·메서드를 고르면 부르는 곳, 부르는 것, 바꾸면 영향 받는 곳이 여기에 나와요."), accuracy());
      return box;
    }
    if (!S.info) { box.append(el("p", "git-empty", "불러오는 중…")); return box; }
    if (S.info.error) { box.append(el("p", "git-error", S.info.error)); return box; }
    const { symbol: h, callers, callees, impact } = S.info;
    box.append(el("p", "git-kicker", `${KIND[h.kind] || h.kind} · ${lang(h.loc)}`), data("h3", "mono", h.qname));
    const meta = data("p", "cg-loc", h.loc);
    meta.append(el("span", "", ` · 색인 ${short(repoRow()?.commit)}`));
    box.append(meta);
    if (h.signature) box.append(data("pre", "cg-sig", h.signature));
    const files = new Set(impact.items.map((x) => x.loc.split(":")[0])).size, n = impact.items.length;
    const act = el("section", n ? "cg-act" : "cg-act calm");
    act.append(el("p", "", n ? `${KIND_OBJ[h.kind] || "이 심볼을"} 바꾸면 ${num(n)}곳(파일 ${num(files)}개)이 영향을 받아요.${impact.truncated && impact.reason !== "depth_cap" ? " 일부만 셌어요." : ""}`
      : "색인에는 이 심볼을 부르는 곳이 없어요. 동적 호출·리플렉션·테스트는 빠질 수 있어요."));
    const acts = el("div", "cg-acts");
    if (n) acts.append(button("영향 받는 곳 보기", "primary", null, () => { S.open.impact = true; paint(); requestAnimationFrame(() => document.querySelector(".cg-impact")?.scrollIntoView({ block: "start", behavior: "smooth" })); }));
    const code = codeButton(h.loc); if (code) acts.append(code);
    if (acts.childNodes.length) act.append(acts);
    box.append(act);
    const c1 = section("부르는 곳", num(callers.items.length) + (callers.truncated ? "+" : ""));
    if (callers.items.length) c1.append(...hitList(callers.items, S.open.callers, () => { S.open.callers = true; paint(); }));
    else c1.append(el("p", "cg-fine", "부르는 곳이 없어요."));
    const c2 = section("부르는 것", num(callees.items.length) + (callees.truncated ? "+" : ""));
    if (callees.items.length) c2.append(...hitList(callees.items, S.open.callees, () => { S.open.callees = true; paint(); }));
    else c2.append(el("p", "cg-fine", "부르는 것이 없어요."));
    const depthPick = select("깊이", [1, 2, 3, 4, 5, 6, 7, 8].map((d) => [String(d), `깊이 ${d}까지`]), String(S.depth), (v) => redepth(Number(v)));
    depthPick.classList.add("cg-depth-pick");
    const c3 = section("바꾸면 영향 받는 곳", depthPick); c3.classList.add("cg-impact");
    if (n) c3.append(depthBars(impact.items, S.open.impact, open));
    else c3.append(el("p", "cg-fine", "영향 받는 곳이 없어요."));
    if (impact.truncated) c3.append(el("p", "cg-fine warn", REASON[impact.reason] || "일부만 셌어요"));
    if (n && !S.open.impact) { const all = el("button", "cg-link", "이름 모두 보기"); all.type = "button"; all.addEventListener("click", () => { S.open.impact = true; paint(); }); c3.append(all); }
    box.append(c1, c2, c3, el("p", "cg-fine", "색인 시점의 정적 분석이에요. 지금 파일과 다르면 파일이 맞아요."));
    return box;
  }

  // ------------------------------------------------------------ PR 영향
  function prPicker() {
    const prs = host.prs().filter((p) => p.state === "open"), d = S.pr.detail, box = el("section", "cg-prbar");
    const left = el("div", "cg-prbar-text");
    if (d) {
      const t = el("p", "cg-prbar-title"); t.append(el("span", "cg-prnum", `#${d.number}`), data("span", "", d.title));
      left.append(t, data("p", "cg-meta", `${d.head} → ${d.base} · 커밋 ${d.commits} · 파일 ${d.changed_files} · 색인 ${short(repoRow()?.commit)} 기준`));
    } else left.append(el("p", "cg-meta", prs.length ? "PR을 고르세요." : "열린 PR이 없어요."));
    const pick = el("select", "cg-select"); pick.setAttribute("aria-label", "PR 바꾸기");
    const first = el("option", "", "PR 바꾸기"); first.value = ""; pick.append(first);
    prs.forEach((p) => { const o = data("option", "", `#${p.number} ${p.title}`); o.value = String(p.number); pick.append(o); });
    pick.value = "";
    pick.addEventListener("change", () => { if (pick.value) void loadPr(Number(pick.value)); });
    box.append(left, pick);
    return box;
  }
  function prItems() { return S.pr.changed?.items || []; }
  function impactUnion() { const m = new Map(); prItems().forEach((c) => c.impact.forEach((h) => m.set(h.id, h))); return [...m.values()]; }
  function prMain() {
    const pr = S.pr, parts = [prPicker()];
    if (!pr.number) {
      const first = host.prs().find((p) => p.state === "open");
      if (first && !pr.busy) queueMicrotask(() => { if (!S.pr.number) void loadPr(first.number); });
      return parts;
    }
    if (pr.error) { parts.push(el("p", "git-error", pr.error)); return parts; }
    if (pr.busy && !pr.changed) { parts.push(el("p", "git-empty", pr.busy)); return parts; }
    const ch = pr.changed;
    if (ch?.empty) { parts.push(el("p", "git-empty", ch.empty)); return parts; }
    const items = prItems(), union = impactUnion(), big = items.filter((c) => c.impact_total >= 10);
    const stale = pr.detail?.head_sha && ch.commit && !pr.detail.head_sha.startsWith(ch.commit);
    if (stale || big.length) {
      const card = el("section", "cg-check"), t = el("div", "cg-check-text"), h = el("p", "cg-check-head");
      h.append("확인해 주세요", el("span", "cg-check-n", String((stale ? 1 : 0) + (big.length ? 1 : 0))));
      const lines = [];
      if (big.length) lines.push(`이 PR이 바꾼 함수 중 ${big.length}개가 10곳 넘게 영향을 줘요`);
      if (stale) lines.push("색인이 PR보다 오래돼 줄 번호가 조금 다를 수 있어요");
      t.append(h, el("p", "", lines.join(" · ")));
      card.append(t, stale ? button("다시 색인", "primary", null, reindex) : button("하나씩 확인하기", "primary", null, () => { pr.sel = big[0].symbol.id; paint(); }));
      parts.push(card);
      if (S.showCmd) parts.push(cmdBox());
    }
    const chipBox = el("div", "cg-chips");
    [["changed", "바뀐 함수", items.length], ["impact", "영향 받는 곳", union.length]].forEach(([k, label, n]) => {
      const c = el("button", `cg-chip${pr.chip === k ? " on" : ""}`); c.type = "button"; c.append(label, el("span", "cg-chip-n", num(n)));
      c.addEventListener("click", () => { pr.chip = k; paint(); }); chipBox.append(c);
    });
    parts.push(chipBox);
    const list = el("div", "cg-list");
    if (pr.chip === "changed") {
      items.forEach((c) => {
        const h = c.symbol, path = h.loc.split(":")[0], start = Number(h.loc.split(":")[1]), { add, del } = lineCount(path, start, c.end_line || start);
        const b = el("button", `cg-row${pr.sel === h.id ? " on" : ""}`); b.type = "button";
        const t = el("span", "cg-row-text");
        t.append(data("span", "cg-name", h.qname), data("span", "cg-meta", [h.loc, add || del ? `+${add} −${del}` : "", `영향 ${num(c.impact_total)}곳`].filter(Boolean).join(" · ")));
        b.append(t, c.impact_total >= 10 ? el("span", "cg-badge warn", "영향 큼") : el("span", "cg-quiet", `영향 ${num(c.impact_total)}곳`));
        b.addEventListener("click", () => { pr.sel = h.id; paint(); });
        list.append(b);
      });
      if (!items.length) list.append(el("p", "git-empty", "바뀐 줄에 걸치는 함수·메서드가 색인에 없어요."));
    } else {
      union.sort((a, b) => (a.depth - b.depth) || a.loc.localeCompare(b.loc));
      union.forEach((h) => list.append(symRow(h, false, () => { host.setMode("code"); void open(h.id); }, el("span", "cg-quiet", `깊이 ${h.depth}`))));
      if (!union.length) list.append(el("p", "git-empty", "영향 받는 곳이 없어요."));
    }
    parts.push(list);
    const foot = [`바뀐 함수 ${items.length}개`, `영향 받는 곳은 깊이 ${ch.depth || 2}까지 셌어요`];
    if (ch.truncated) foot.push(REASON[ch.reason] || "일부만 보여요");
    parts.push(el("p", "cg-foot", foot.join(" · ")));
    return parts;
  }
  // The changed lines of one function from the PR patch: the hunks that touch start..end (new side).
  function patchFor(path, start, end) {
    const patch = S.pr.lines[path]?.patch || "", out = [];
    let line = 0, from = 0, to = 0;
    for (const t of patch.split("\n")) {
      const h = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(t);
      if (h) { line = Number(h[1]); continue; }
      if (!line || /^(\+\+\+|---) /.test(t)) continue;
      const inside = line >= start && line <= end;
      if ((t.startsWith("+") || t.startsWith("-")) && inside) { out.push(t); from = from || line; to = line; }
      if (!t.startsWith("-") && !t.startsWith("\\")) line++;
      if (out.length >= 14) break;
    }
    return { text: out.join("\n"), from, to };
  }
  function prDetail() {
    const box = el("div", "git-right cg-right"), pr = S.pr, c = prItems().find((x) => x.symbol.id === pr.sel);
    if (!c) {
      box.append(el("p", "git-kicker", "PR 영향"), el("h3", "", "이 PR이 바꾼 함수의 영향"),
        el("p", "git-note", "PR이 바꾼 줄에 걸치는 함수·메서드를 찾아, 바꾸면 영향 받는 곳을 깊이별로 보여 줘요."), accuracy());
      return box;
    }
    const h = c.symbol, d = pr.detail;
    box.append(el("p", "git-kicker", `PR #${d.number} · 바뀐 ${KIND[h.kind] || "함수"}`), el("h3", "", "이 PR이 바꾼 함수의 영향"), data("p", "cg-loc", `${h.qname} · ${h.loc}`));
    const act = el("section", c.impact_total ? "cg-act" : "cg-act calm");
    act.append(el("p", "", c.impact_total ? `영향 받는 곳이 ${num(c.impact_total)}곳이에요. 리뷰할 때 함께 확인하세요.` : "색인에는 이 함수를 부르는 곳이 없어요."));
    const acts = el("div", "cg-acts");
    acts.append(button("코드 리뷰에서 보기", "primary", null, () => host.openReview(`review:${host.gitRepo}#${d.number}`)));
    const code = codeButton(h.loc); if (code) acts.append(code);
    acts.append(button("코드 그래프에서 열기", "", null, () => { host.setMode("code"); void open(h.id); }));
    act.append(acts);
    box.append(act);
    const s1 = section("깊이별 영향", `${num(c.impact_total)}곳`);
    if (c.impact.length) s1.append(depthBars(c.impact, false, (id) => { host.setMode("code"); void open(id); }));
    else s1.append(el("p", "cg-fine", "영향 받는 곳이 없어요."));
    if (c.truncated) s1.append(el("p", "cg-fine warn", REASON[c.reason] || "일부만 셌어요"));
    box.append(s1);
    const path = h.loc.split(":")[0], { text, from, to } = patchFor(path, Number(h.loc.split(":")[1]), c.end_line || 0);
    if (text) {
      const link = el("button", "cg-link", "코드 리뷰에서 보기"); link.type = "button"; link.addEventListener("click", () => host.openReview(`review:${host.gitRepo}#${d.number}`));
      const s2 = section("바뀐 줄", link), pre = el("div", "cg-patch"), p = raw(el("pre", ""));
      text.split("\n").forEach((l) => p.append(el("span", l.startsWith("+") ? "add" : "del", `${l}\n`)));
      pre.append(p, data("small", "", `${path.split("/").pop()}:${from}${to > from ? `–${to}` : ""}`));
      s2.append(pre);
      box.append(s2);
    }
    return box;
  }

  // ------------------------------------------------------------ page (called by git.js render)
  function render(mode, h) {
    host = h;
    if (S.repos === null && !S.loading) { queueMicrotask(() => load()); }
    else pickRepo(false);
    const parts = [seg(mode)];
    if (S.repos === null || S.loading) parts.push(el("p", "git-empty", "코드 그래프를 불러오는 중…"));
    else if (S.error && !S.repos.length) parts.push(el("p", "git-error", S.error === "HTTP 404" ? "이 서버는 코드 그래프를 지원하지 않아요. 서버를 업데이트하세요." : S.error));
    else if (!repoRow()) parts.push(emptyState());
    else if (mode === "impact") parts.push(...prMain());
    else parts.push(statusCard(), searchBar(), chips(), results());
    return { parts, detail: !repoRow() ? symbolDetail() : mode === "impact" ? prDetail() : symbolDetail() };
  }
  return { render, seg, headButtons, reset: () => { S.repos = null; reset(); }, sub: (mode) => (mode === "impact"
    ? "PR이 바꾼 함수가 어디까지 영향을 주는지 봐요. 리뷰 전에 영향 큰 곳부터 확인해요."
    : "함수와 메서드가 서로 어떻게 불리는지 봐요. 바꾸기 전에 영향 받는 곳을 먼저 확인해요.") };
};
