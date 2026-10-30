// @ts-check
// 홈의 시작 안내: 새로 설치한 뒤 빈 홈에 보이는 네 줄(에이전트 · 기억 뽑을 모델 · 대화 · 지침 가져오기)과,
// 기억을 뽑을 모델이 안 될 때 홈 맨 위와 상태 카드에 뜨는 쉬운 말 한 장.
(function (root) {
  "use strict";
  const CLI_NAME = { claude: "Claude Code", codex: "Codex", gemini: "Gemini CLI" };
  const cliName = (cli) => CLI_NAME[cli] || cli || "에이전트";

  // The extractor failures extractorIssue explains, as analysis_run.error/reason or extractor.state carries them;
  // action is the one button: login (로그인하기), model (다른 모델 고르기) or retry (다시 시도).
  const EXTRACTOR_CODES = ["extractor_missing", "login_needed", "extractor_exit_", "extractor_no_json", "TimeoutExpired"];

  // tools: agent_tools from the server. extractor is the host's pick (cli, state); analysis_run.reason/error carries
  // the last failure code. null: ready, or not reported yet (an older host sends no extractor).
  function extractorIssue(tools) {
    const x = tools?.extractor, run = tools?.analysis_run;
    const code = String(run?.error || run?.reason || "");
    const name = cliName(x?.cli);
    const fix = (title, text, short, action) => ({ title, text, short, action, code });
    if (x?.state === "missing" || code.startsWith("extractor_missing"))
      return fix("기억을 뽑을 프로그램이 없어요", `${name}를 찾지 못해 대화에서 기억 후보를 뽑지 못해요. Claude Code나 Codex를 설치하고 로그인하거나, 다른 모델을 고르세요.`, "프로그램 없음", "login");
    if (x?.state === "login_needed")
      return fix("기억을 뽑으려면 로그인이 필요해요", `${name}에 로그인하면 대화에서 기억 후보를 뽑기 시작해요.`, "로그인 필요", "login");
    if (code.startsWith("extractor_exit"))
      return fix("기억을 뽑는 모델이 답하지 않았어요", `${name} 로그인이 끝났거나 사용 한도에 걸렸을 수 있어요. 다시 로그인하거나 다른 모델을 고르세요.`, "응답 없음", "login");
    if (code.startsWith("extractor_no_json"))
      return fix("기억을 뽑는 모델의 답을 읽지 못했어요", "모델이 정해진 모양으로 답하지 않았어요. 다른 모델을 고르면 나아질 수 있어요.", "답을 못 읽음", "model");
    if (code === "TimeoutExpired")
      return fix("기억을 뽑는 데 너무 오래 걸렸어요", "다음 분석 때 저절로 다시 해요. 계속되면 더 빠른 모델을 고르세요.", "너무 오래 걸림", "retry");
    return null;
  }

  // install-arkism's record (agents-setup.json): which agents' files it changed, where the backup is.
  const AGENT_DIRS = [[/[\\/]\.claude([\\/]|\.json$)|[\\/]claude[\\/]/i, "Claude Code"], [/[\\/]\.codex[\\/]/i, "Codex"], [/[\\/]\.gemini[\\/]/i, "Gemini CLI"],
    [/[\\/]\.grok[\\/]/i, "Grok"], [/zcode/i, "ZCode"], [/opencode/i, "OpenCode"], [/cursor/i, "Cursor"]];
  function setupSummary(rec) {
    if (!rec || typeof rec !== "object") return null;
    if (rec.ok === false) return { ok: false, agents: [], backup: "", error: String(rec.error || "") };
    const files = Array.isArray(rec.summary?.files) ? rec.summary.files.map(String) : [];
    const agents = AGENT_DIRS.filter(([re]) => files.some((f) => re.test(f))).map(([, name]) => name);
    return { ok: true, agents, backup: String(rec.summary?.backup || ""), files: files.length, at: rec.at || "" };
  }

  function create(ctx) {
    const { state, el, button, render, notice, tauri, openSettings } = ctx;
    // One read per connection: the setup record, the import dry run and the autostart switch.
    let G = { g: -1, setup: undefined, found: undefined, autostart: undefined };
    function load() {
      const g = state.connection.generation;
      if (G.g === g) return;
      G = { g, setup: undefined, found: undefined, autostart: undefined };
      if (!tauri || state.mode !== "local") return;
      const got = (k) => (v) => { if (G.g !== g) return; G[k] = v; render(false); };
      tauri.invoke("agents_setup").then(got("setup"), () => got("setup")(null));
      tauri.invoke("import_agent_memories", { dryRun: true }).then(got("found"), () => got("found")(null));
      tauri.invoke("autostart_get").then(got("autostart"), () => got("autostart")(null));
    }
    const toAgents = () => openSettings("agents");
    const toRoles = () => openSettings("roles");

    // The plain card for a broken extractor: 홈 맨 위 (todo item) and the checklist row use the same words.
    function issueActions(issue) {
      const login = button("로그인하기", issue.action === "login" ? "small primary" : "small", null, toAgents);
      const model = button("다른 모델 고르기", issue.action !== "login" ? "small primary" : "small", null, toRoles);
      return issue.action !== "login" ? [model, login] : [login, model];
    }
    function issueItem() {
      const issue = extractorIssue(state.data.tools);
      if (!issue) return null;
      if (issue.action === "retry") issue.action = "model"; // 홈 has no re-read of its own: a slow model is the fix there
      return { id: "extractor", n: 1, icon: "warning-circle", label: "기억 뽑을 모델", title: issue.title, sub: issue.text, warn: true,
        action: issue.action === "model" ? "다른 모델 고르기" : "로그인하기", run: issue.action === "model" ? toRoles : toAgents, preview: [issue.text] };
    }

    // A fresh install: nothing saved, nothing waiting.
    const fresh = () => !state.data.memories.length && !state.data.candidates.length && !state.data.conflicted?.length;

    function row(list, title, sub, badge, actions = []) {
      const r = el("div", "start-row");
      const t = el("span", "start-agent-text");
      const s = el("small", "", "");
      if (Array.isArray(sub)) s.append(...sub); else s.textContent = sub;
      t.append(el("strong", "", title), s);
      const side = el("div", "start-row-side");
      side.append(...actions, el("output", `start-badge ${badge[0]}`.trim(), badge[1]));
      r.append(t, side);
      list.append(r);
      return r;
    }
    function agentsRow(list) {
      const t = state.data.tools;
      const linked = (t?.agents || []).filter((a) => a.found !== false && a.enabled);
      const setup = setupSummary(G.setup);
      if (setup && !setup.ok) {
        const retry = button("다시 연결", "small primary", null, async (e) => {
          const b = e.currentTarget;
          b.disabled = true;
          try { G.setup = await tauri.invoke("agents_setup_retry"); notice("에이전트를 다시 연결했어요. 열려 있는 에이전트는 다시 시작하세요."); }
          catch (_) { notice("다시 연결하지 못했어요. 설정 › 고급 › 진단에서 점검해 보세요."); }
          render(false);
        });
        row(list, "에이전트 연결", "에이전트 설정 파일을 고치지 못해 대화를 모으지 못해요.", ["warn", "연결 안 됨"], [retry]);
        return;
      }
      const names = linked.map((a) => a.name);
      const words = setup?.agents.length ? setup.agents : names;
      const parts = [words.length ? `${words.join(" · ")}에 Arkism을 이었어요. 열려 있는 에이전트는 다시 시작하세요.` : "Claude Code나 Codex를 설치하면 1분 안에 이어져요."];
      if (setup?.backup) {
        const where = el("span", "mono", setup.backup);
        where.setAttribute("data-no-i18n", "");
        parts.push(el("br"), "바꾸기 전 파일은 여기 있어요: ", where);
      }
      const badge = !t?.collected_at ? ["", "확인 중"] : linked.length ? ["ok", "연결됨"] : ["warn", "없음"];
      row(list, "에이전트 연결", parts, badge, linked.length ? [] : [button("에이전트 관리", "small", null, toAgents)]);
    }
    function extractorRow(list) {
      const issue = extractorIssue(state.data.tools), x = state.data.tools?.extractor;
      if (issue) { row(list, "기억 뽑을 모델", `${issue.title}. ${issue.text}`, ["warn", issue.short], issueActions(issue)); return; }
      if (x?.state === "ready") { row(list, "기억 뽑을 모델", `${cliName(x.cli)}로 대화에서 기억 후보를 뽑아요.`, ["ok", "준비됨"], [button("바꾸기", "small ghost", null, toRoles)]); return; }
      row(list, "기억 뽑을 모델", "에이전트가 처음 보고하면 여기에 보여요.", ["", "확인 중"]);
    }
    function talkRow(list) {
      row(list, "평소처럼 대화하기", "에이전트와 평소처럼 대화하면, 대화가 끝나고 몇 분 뒤 기억 후보가 여기 와요. 내가 승인한 것만 저장돼요.", ["", "기다리는 중"]);
    }
    function importRow(list) {
      if (!tauri || state.mode !== "local") return;
      const n = G.found?.statements || 0;
      if (G.found === undefined) { row(list, "기존 지침 파일 가져오기", "에이전트 지침 파일을 살펴보는 중이에요…", ["", "확인 중"]); return; }
      if (!n) { row(list, "기존 지침 파일 가져오기", "가져올 지침 파일이 없어요.", ["", "없음"]); return; }
      const run = button("가져오기", "small", null, () => window.ArkismAgentMemoryImport.run({ invoke: (cmd, args) => tauri.invoke(cmd, args), ask: ctx.ask, notice }).then(() => { G.found = { statements: 0 }; render(false); }));
      row(list, "기존 지침 파일 가져오기", `에이전트 지침 파일에서 기억 ${n}개를 찾았어요. 가져오면 검토 대기로 가고, 승인한 것만 저장돼요.`, ["", `${n}개`], [run]);
    }
    function autostartLine() {
      if (typeof G.autostart !== "boolean") return null;
      const p = el("p", "home-note start-autostart");
      const flip = el("button", "link-btn", G.autostart ? "끄기" : "켜기");
      flip.type = "button";
      flip.addEventListener("click", () => tauri.invoke("autostart_set", { on: !G.autostart }).then(() => { G.autostart = !G.autostart; render(false); }, () => notice("바꾸지 못했어요.")));
      p.append(G.autostart ? "PC에 로그인하면 Arkism이 트레이에서 자동으로 켜져요. 그래야 대화를 계속 모아요. " : "PC에 로그인해도 Arkism이 켜지지 않아요. 앱이 꺼져 있으면 대화를 모으지 않아요. ", flip);
      return p;
    }
    // The empty home on a fresh install; null when the usual empty panel fits better.
    function checklist() {
      if (!fresh()) return null;
      load();
      const card = el("div", "start-card home-start-list");
      const head = el("div", "start-card-head");
      head.append(el("strong", "", "시작하기"), el("small", "", "아래가 모두 준비되면 대화에서 기억 후보가 모여요."));
      card.append(head);
      agentsRow(card);
      extractorRow(card);
      talkRow(card);
      importRow(card);
      const wrap = el("div", "start-wrap");
      wrap.append(card);
      const auto = autostartLine();
      if (auto) wrap.append(auto);
      return wrap;
    }
    return { checklist, issueItem, fresh, extractorIssue: () => extractorIssue(state.data.tools) };
  }

  // The CLI that runs 기억 추출, as every screen names it; "" before the host reports one.
  const extractorName = (tools) => (tools?.extractor?.cli ? cliName(tools.extractor.cli) : "");
  const api = { create, extractorIssue, extractorName, setupSummary, EXTRACTOR_CODES };
  if (typeof window === "undefined" && typeof module === "object" && module.exports) module.exports = api;
  else root.ArkismHomeStart = api;
})(typeof window === "object" ? window : globalThis);
