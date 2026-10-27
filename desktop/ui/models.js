// 설정 › 모델 설정. Providers, logged-in CLI accounts, quota, stored keys (last four
// only) and which models workflow AI nodes may use. Keys go to the server through Arkism's one-time handoff:
// the host runner (`arkism crew` providers watch) stores or tests them; nothing here keeps a key.
// "5h" → "5시간 한도", "week" → "주간 한도".
// "week-fable" → "주간 한도 · fable", "day-gemini-2.5-pro" → "일일 한도 · gemini-2.5-pro".
function windowName(w) {
  const [base, ...model] = String(w).split("-");
  const name = base === "week" ? "주간 한도" : base === "day" ? "일일 한도" : /^\d+h$/.test(base) ? `${Number.parseInt(base, 10)}시간 한도` : `${base} 한도`;
  return model.length ? `${name} · ${model.join("-")}` : name;
}
window.ArkismModels = (ctx) => {
  "use strict";
  const { state, el, icon, button, go, render, request, query, notice, agentLogo, ago } = ctx;

  const KNOWN = [
    ["anthropic", "Anthropic", "img:claude.svg", "https://console.anthropic.com/settings/keys"],
    ["openai", "OpenAI", "sym:a-codex", "https://platform.openai.com/api-keys"],
    ["google", "Google", "sym:a-gemini", "https://aistudio.google.com/apikey"],
    ["zai", "Z.ai", "sym:a-zcode", "https://z.ai/manage-apikey/apikey-list"]
  ];
  const known = (id) => KNOWN.find(([k]) => k === id);
  // Every suffix of a key's base URL host (api.deepseek.com → deepseek.com), for a brand mark of a hand-added provider.
  const hosts = (u) => { try { const h = new URL(u).hostname.split("."); return h.map((_, i) => h.slice(i).join(".")); } catch { return []; } };
  // The provider's LobeHub mark (brand.js) by id, name or key base URL; else the KNOWN logo; else the generic stack.
  const logo = (p, name = p.name) => {
    const m = [p.id, p.name, ...(p.keys || []).flatMap((k) => hosts(k.base_url))].map((n) => window.ArkismBrand?.mark(n)).find(Boolean);
    if (m) { const t = el("span", "agent-tile"); t.title = name; t.append(m); return t; }
    if (known(p.id)) return agentLogo(known(p.id)[2], name);
    const t = el("span", "agent-tile mono-logo"); t.append(icon(p.id === "xai" ? "x" : "stack")); return t;
  };
  let selected = "openai";
  const filters = {}; // 모델 찾기 text per provider
  let typing = null; // the provider whose filter had focus before a redraw

  const doc = () => state.data.providers || { providers: [], requests: [], disabled: [] };
  async function load() {
    try {
      state.data.providers = (await request(`/v1/providers?${query()}`)).providers || null;
    } catch (_) {
      state.data.providers = null; // older server: the tab says so
    }
    return state.data.providers;
  }
  // The list always shows the four known providers, so a key can be added before the first report.
  function providers() {
    const reported = doc().providers || [];
    const out = KNOWN.map(([id, name]) => reported.find((p) => p.id === id) || { id, name, kind: "api", accounts: [], keys: [], models: [] });
    return [...out, ...reported.filter((p) => !known(p.id))];
  }
  // Reported models plus the ones the owner added by hand (모델 추가).
  const extraOf = (pid) => (doc().extra || []).filter((k) => k.startsWith(`${pid}/`)).map((k) => k.slice(pid.length + 1));
  // 직접 추가 providers (one per key: custom-…) can offer hundreds of models: only the ones the owner turned on count.
  const isCustom = (pid) => pid.startsWith("custom-");
  const customPid = (keyId) => `custom-${keyId.replace(/^key_/, "").slice(0, 12).toLowerCase()}`;
  const enabled = (pid, model) => !(doc().disabled || []).includes(`${pid}/${model}`) && (!isCustom(pid) || extraOf(pid).includes(model));
  const modelsOf = (p) => [...new Set([...(p.models || []), ...extraOf(p.id)])];
  // Models workflow AI nodes may pick: every listed model that is on.
  const enabledModels = () => providers().flatMap((p) => modelsOf(p).filter((m) => enabled(p.id, m)));
  // The same, per provider ({name, models}), for pickers that show where a model comes from.
  const enabledByProvider = () => providers().map((p) => ({ id: p.id, name: p.name || p.id, keys: p.keys, models: modelsOf(p).filter((m) => enabled(p.id, m)) })).filter((p) => p.models.length);

  const ERR = {
    access_denied: "이 계정은 모델 설정을 바꿀 수 없어요.",
    invalid_request: "칸 형식을 확인해 주세요. 키에는 띄어쓰기가 없어야 해요.",
    secret_detected: "이름이나 주소에 키처럼 보이는 값이 있어요."
  };
  const errText = (e) => ERR[e.message] || `처리하지 못했어요 (${e.message}).`;

  async function switchModel(pid, model, on, extra = {}) {
    try {
      state.data.providers = (await request("/v1/providers/models", { method: "POST", body: JSON.stringify({ scope: state.scope, provider: pid, model, enabled: on, ...extra }) })).providers;
      render(false);
    } catch (e) { notice(errText(e)); }
  }
  // 연결 시험: 서버 sends one short prompt to the model through its CLI and reports how long it took.
  const lastTest = (pid, model) => (doc().requests || []).findLast((q) => q.kind === "model_test" && q.provider === pid && q.model === model);
  async function testModel(pid, model, btn) {
    if (btn) btn.disabled = true;
    try {
      const rec = await request("/v1/providers/test", { method: "POST", body: JSON.stringify({ scope: state.scope, provider: pid, model }) });
      state.data.providers = rec.providers;
      render(false);
      notice(`${model}에 시험 문장을 보냈어요. 길면 1분쯤 걸려요.`);
      const q = await waitFor(last(rec).id, 120000);
      if (!q) notice("서버 실행기가 아직 답하지 않았어요. 잠시 뒤 다시 확인해 주세요.");
      else notice(q.status === "done" ? `${model} 연결 정상 · ${q.note || "응답 받음"}` : `${model} 시험 실패: ${q.error || "알 수 없음"}`);
    } catch (e) { notice(errText(e)); } finally { if (btn) btn.disabled = false; }
  }
  function testTag(pid, model) {
    const q = lastTest(pid, model);
    if (!q) return null;
    if (q.status === "pending") return el("span", "tag", "시험 중");
    return el("span", `tag ${q.status === "done" ? "ok" : "warn"}`, q.status === "done" ? (q.note || "정상") : "시험 실패");
  }
  // 계정 추가: a CLI login happens on the server, so the window shows the command to run there.
  const LOGIN = { codex: "codex login", claude: "claude  →  /login", gemini: "gemini  →  로그인 선택",
    grok: "grok  →  /login", agy: "agy  →  로그인" };
  function openAccountHelp(p) {
    const clis = [...new Set([...(p.accounts || []).map((a) => a.cli), { openai: "codex", anthropic: "claude", google: "gemini" }[p.id]].filter(Boolean))];
    const dlg = el("dialog", "crew-dialog");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const t = el("div", "");
    t.append(el("h2", "", `${p.name} 계정 추가`), el("p", "", "구독 로그인은 서버에서 한 번 해 두면 돼요. 로그인하면 1분 안에 여기 보여요."));
    const close = el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    head.append(t, close);
    const body = el("div", "dialog-body");
    if (!clis.length) body.append(el("p", "", `${p.name}는 API 키로 연결해요. 아래 '키 넣기'에서 키를 넣으면 바로 써요.`));
    for (const c of clis) {
      const row = el("div", "login-row");
      row.append(el("strong", "", `${c} CLI`), el("code", "", LOGIN[c] || `${c} login`));
      body.append(row);
    }
    body.append(el("p", "pg-hint", "여러 계정을 번갈아 쓰려면 계정마다 따로 로그인한 홈 폴더를 쓰세요. API로 쓰려면 '키 넣기'를 눌러요."));
    const foot = el("div", "dialog-foot");
    const key = button("키 넣기", clis.length ? "" : "primary", null, () => { dlg.close(); openKeyDialog(p); });
    key.type = "button";
    const ok = button("닫기", clis.length ? "primary" : "");
    ok.type = "submit";
    foot.append(key, ok);
    form.append(head, body, foot);
    dlg.append(form);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  }
  async function removeKey(pid, key) {
    if (!await ask(`'${key.name}' 키(끝 ${key.last4})를 서버에서 지울까요?`)) return;
    try {
      const r = await request("/v1/providers/keys/remove", { method: "POST", body: JSON.stringify({ scope: state.scope, provider: pid, key_id: key.id }) });
      state.data.providers = r.providers;
      notice("지우기를 요청했어요. 서버에서 지우면 목록에서 빠져요.");
      render(false);
      const done = await waitFor(last(r).id);
      if (done?.status === "failed") notice(`지우지 못했어요: ${done.error || "알 수 없음"}`);
    } catch (e) { notice(errText(e)); }
  }
  const last = (rec) => rec.providers.requests[rec.providers.requests.length - 1];
  // The runner checks every few seconds; wait for it to settle a request.
  async function waitFor(id, ms = 30000) {
    const until = Date.now() + ms;
    while (Date.now() < until) {
      await new Promise((r) => setTimeout(r, 1500));
      const d = await load().catch(() => null);
      const q = d?.requests?.find((x) => x.id === id);
      if (q && q.status !== "pending") { render(false); return q; }
    }
    return null;
  }

  // ---------------------------------------------------------------- the tab
  function quotaCard(title, q) {
    const card = el("div", "quota");
    const used = q.left_pct == null ? null : 100 - q.left_pct;
    const top = el("div", "quota-top");
    top.append(el("span", "", title), el("strong", "", used == null ? "—" : `${used}%`));
    const bar = el("div", "quota-bar");
    const fill = el("i");
    fill.style.width = `${used ?? 0}%`;
    bar.append(fill);
    card.append(top, bar, el("small", "", q.resets_at ? `${new Date(q.resets_at).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" })} 초기화` : "초기화 시각 모름"));
    return card;
  }
  function statusTag(status) {
    const [cls, text] = { connected: ["ok", "연결됨"], expired: ["warn", "다시 인증"], missing: ["warn", "로그인 필요"] }[status] || ["", status];
    const t = el("span", `tag mp-status ${cls}`);
    t.append(el("i", `mp-dot ${cls}`), text);
    return t;
  }
  // 07b: one head per logged-in CLI (name, status, refresh, 계정 추가) over a grey card with its limits,
  // then keys (API providers, or any provider holding one), then the model list.
  const CLI_NAMES = { claude: "Claude Code", codex: "Codex", glm: "ZCode", gemini: "Gemini CLI", grok: "Grok", agy: "antigravity" };
  // 연필: the owner's own names for accounts and models; an empty answer goes back to the default name.
  const labelOf = (kind, pid, id) => doc().labels?.[`${kind}:${pid}/${id}`] || "";
  const modelName = (pid, m) => labelOf("model", pid, m) || m.replace(/^antigravity-/, "");
  async function rename(kind, pid, id, current) {
    const name = await askText(kind === "account" ? "이 계정을 뭐라고 부를까요? (비우면 기본 이름)" : "이 모델을 뭐라고 부를까요? (비우면 모델 이름 그대로)", current);
    if (name === null) return;
    try {
      const rec = await request("/v1/providers/labels", { method: "POST", body: JSON.stringify({ scope: state.scope, provider: pid, kind, id, label: name.trim() }) });
      state.data.providers = rec.providers;
      render(false);
    } catch (e) { notice(errText(e)); }
  }
  function pencil(kind, pid, id, current, what) {
    const b = button("", "icon-btn plain", "pencil-simple", () => rename(kind, pid, id, current));
    b.title = "이름 고치기";
    b.setAttribute("aria-label", `${what} 이름 고치기`);
    return b;
  }
  // 새로 고침: 도는 동안 아이콘을 돌리고 다시 누르지 못하게 한다.
  // 내장 Arkism이면 버튼이 한도를 그 자리에서 새로 확인하고, 서버면 서버가 1분마다 올린 최신 보고를 다시 읽는다.
  // 새로고침: every provider's model list and the limits, read again past their caches. On this PC the host does it
  // now; on a server the runner gets a models_refresh request and the list comes back with its next report.
  async function reload(b) {
    b.disabled = true;
    b.classList.add("busy");
    try {
      if (state.mode === "local") await window.__TAURI__?.core?.invoke("providers_refresh").catch(() => notice("한도와 모델 목록을 새로 받지 못했어요. 마지막 결과를 보여 줘요."));
      else {
        const rec = await request("/v1/providers/models_refresh", { method: "POST", body: JSON.stringify({ scope: state.scope }) }).catch((e) => e.message === "revision_conflict" ? null : Promise.reject(e));
        // A conflict usually means one already waits: follow it. It is also sent for a full request list or a write clash,
        // when nothing was queued, so only a still-pending refresh is followed; an older finished one would mislead.
        const id = rec?.providers ? last(rec).id : (await load())?.requests?.findLast((x) => x.kind === "models_refresh" && x.status === "pending")?.id;
        if (!id) notice("새로고침을 넣지 못했어요. 잠시 뒤 다시 눌러 주세요.");
        const q = id ? await waitFor(id, 60000) : null;
        if (q?.status === "failed" || q?.status === "expired") notice(`모델 목록을 새로 받지 못했어요: ${q.error || "알 수 없음"}`);
        else if (id && !q) notice("서버 실행기가 아직 답하지 않았어요. 잠시 뒤 다시 눌러 주세요.");
      }
      await load();
    } catch (e) { notice(errText(e)); } finally { render(false); }
  }
  // Figma 198:544 (모델 설정): provider chips on top, then a 계정 card and a 모델 card; 기본 모델 · API 키 · 한도 안내 on the right.
  async function renameTo(kind, pid, id, name) {
    try {
      const rec = await request("/v1/providers/labels", { method: "POST", body: JSON.stringify({ scope: state.scope, provider: pid, kind, id, label: name.trim() }) });
      state.data.providers = rec.providers;
      notice("이름을 저장했어요.");
      render(false);
    } catch (e) { notice(errText(e)); }
  }
  const actionsRow = (...buttons) => { const r = el("div", "v4-side-actions"); r.append(...buttons); return r; };
  const card = (title, hint) => {
    const c = el("section", "mp-card");
    c.append(el("h3", "", title));
    if (hint) c.append(el("p", "mp-card-hint", hint));
    return c;
  };
  // CLI 버전 (cli_info) and 업데이트: POST /v1/providers/cli_update queues it; the host runner updates Claude Code / Codex.
  const UPDATABLE = { anthropic: "claude", openai: "codex" };
  const VIA = { npm: "npm", brew: "Homebrew", self: "자체 업데이트" };
  const requestId = () => [...crypto.getRandomValues(new Uint8Array(16))].map((b) => b.toString(16).padStart(2, "0")).join("");
  async function updateCLI(p, a, btn) {
    btn.disabled = true;
    try {
      const r = await request("/v1/providers/cli_update", { method: "POST", body: JSON.stringify({ scope: state.scope, provider: p.id, cli: a.cli,
        request_id: requestId(), expected_version: a.cli_info.version, expected_via: a.cli_info.via }) });
      if (r?.providers) state.data.providers = r.providers;
      notice(`${CLI_NAMES[a.cli] || a.cli} 업데이트를 요청했어요. ${state.mode === "local" ? "이 PC" : "서버"}에서 설치하면 버전이 바뀌어요.`);
      render(false);
      const id = r?.providers ? last(r).id : "";
      const done = id ? await waitFor(id, 120000) : null;
      if (done?.status === "done") notice(`${CLI_NAMES[a.cli] || a.cli}를 업데이트했어요.`);
      else if (done?.status === "failed") notice(`업데이트하지 못했어요: ${done.error || "알 수 없음"}`);
    } catch (e) { notice(e.message === "revision_conflict" ? "버전이 바뀌었거나 이미 업데이트 중이에요. 새로고침해 주세요." : errText(e)); btn.disabled = false; }
  }
  function cliLine(p, a) {
    const info = a.cli_info;
    if (!info?.version) return null;
    const row = el("div", "mp-cli");
    const pending = (doc().requests || []).find((q) => q.kind === "cli_update" && q.cli === a.cli && q.status === "pending");
    const failed = [...(doc().requests || [])].reverse().find((q) => q.kind === "cli_update" && q.cli === a.cli);
    const text = el("span", "", `${CLI_NAMES[a.cli] || a.cli} ${info.version}${info.via ? ` · ${VIA[info.via] || info.via}` : ""}`);
    row.append(text);
    if (info.update && info.latest) row.append(el("span", "tag warn", `새 버전 ${info.latest}`));
    if (pending) row.append(el("span", "tag", "업데이트 중…"));
    else if (info.update && UPDATABLE[p.id] === a.cli && info.via) {
      const b = button("CLI 업데이트", "", null, () => updateCLI(p, a, b));
      row.append(b);
    }
    if (!pending && failed?.status === "failed" && info.update) row.append(el("small", "mp-cli-err", `지난 업데이트 실패: ${failed.error || "알 수 없음"}`));
    return row;
  }
  function accountBlock(p, a, first) {
    const box = el("div", "mp-account");
    const head = el("div", "mp-account-head");
    head.append(logo(p, a.label), el("strong", "", CLI_NAMES[a.cli] || a.label), statusTag(a.status));
    box.append(head);
    // 이름: the owner's own name for this account (empty = the default name).
    const nameRow = el("div", "mp-name-row");
    const nameIn = el("input");
    nameIn.maxLength = 60;
    nameIn.value = labelOf("account", p.id, a.id);
    nameIn.placeholder = CLI_NAMES[a.cli] || a.label || "계정 이름";
    nameIn.setAttribute("aria-label", "계정 이름");
    nameRow.append(nameIn, button("이름 저장", "", null, () => renameTo("account", p.id, a.id, nameIn.value)));
    box.append(nameRow);
    if (a.status !== "connected") {
      const where = state.mode === "local" ? "이 PC에서" : "서버에서";
      // Gemini CLI renews its own sign-in whenever it runs; Arkism never refreshes it.
      const hint = a.cli === "gemini" && a.status === "expired" ? `${where} Gemini CLI를 한 번 실행하면 로그인이 갱신돼요.` : `${where} ${a.cli || "CLI"} 로그인을 다시 해 주세요.`;
      box.append(el("div", "mp-warn", hint));
    }
    let windows = [];
    if (a.quotas?.length) windows = a.quotas;
    else if (a.quota) windows = [a.quota];
    if (windows.length) {
      const qs = el("div", "quota-row");
      for (const q of windows) qs.append(quotaCard(windowName(q.window), q));
      box.append(qs);
    } else if (a.status === "connected") box.append(el("small", "dim", "이 계정은 사용 한도를 알려 주지 않아요."));
    const cli = cliLine(p, a);
    if (cli) box.append(cli);
    const acts = el("div", "mp-actions");
    const re = button("새로고침", "ghost", null, () => reload(re));
    const at = doc().collected_at;
    re.title = at ? (state.mode === "local" ? `이 PC에서 ${ago(at)} 확인 · 눌러서 지금 확인` : `서버에서 ${ago(at)} 확인 · 눌러서 지금 확인`) : "지금 확인";
    acts.append(re);
    const firstOn = modelsOf(p).find((m) => enabled(p.id, m));
    if (firstOn && a.status === "connected") {
      const plug = button("연결 시험", "", null, () => testModel(p.id, firstOn, plug));
      plug.title = `${firstOn}로 연결 시험`;
      acts.append(plug);
    }
    if (first && !p.custom) acts.append(button("계정 추가", "", null, () => openAccountHelp(p)));
    box.append(acts);
    return box;
  }
  function accountsCard(p) {
    const accounts = p.accounts || [];
    const kindText = p.kind === "subscription" ? `${p.name} · ${[...new Set(accounts.map((a) => CLI_NAMES[a.cli] || a.label))].join("·") || "구독"} 구독 계정이에요.` : `${p.name} · API 키로 연결해요.`;
    const c = card("계정", kindText);
    accounts.forEach((a, i) => c.append(accountBlock(p, a, i === 0)));
    if (!accounts.length) {
      c.append(el("p", "mp-empty", p.custom ? "직접 추가한 제공자는 키로만 연결해요." : "로그인한 계정이 없어요. 구독으로 쓰려면 계정을 추가하고, API로 쓰려면 오른쪽에서 키를 넣어요."));
      if (!p.custom) { const acts = el("div", "mp-actions"); acts.append(button("계정 추가", "", null, () => openAccountHelp(p))); c.append(acts); }
    }
    return c;
  }
  function keysSection(p) {
    const s = kit.sideSec("API 키");
    const pendingKeys = (doc().requests || []).filter((x) => x.provider === p.id && x.kind === "key_add" && x.status !== "done").slice(-3);
    if (!(p.keys || []).length && !pendingKeys.length) s.append(el("p", "", p.kind === "subscription" ? "저장된 키가 없어요. 구독 로그인만 쓰면 키가 없어도 돼요." : "저장된 키가 없어요."));
    for (const k of p.keys || []) {
      const r = el("div", "mp-key");
      r.append(el("span", "mp-key-name", k.name), el("span", "mono", [`••••${k.last4}`, k.monthly_budget_usd != null ? `$${k.monthly_budget_usd}/월` : ""].filter(Boolean).join("  ·  ")));
      const del = button("", "icon-btn plain", "trash", () => removeKey(p.id, k));
      del.title = "키 제거";
      del.setAttribute("aria-label", `${k.name} 키 지우기`);
      r.append(del);
      const meta = [k.base_url ? k.base_url.replace(/^https?:\/\//, "") : "", k.created_at ? ago(k.created_at) : ""].filter(Boolean).join(" · ");
      s.append(r);
      if (meta) s.append(el("small", "mp-key-meta", meta));
    }
    for (const q of pendingKeys) {
      const msg = { pending: "서버에 저장하는 중", failed: `저장 실패: ${q.error || "알 수 없음"}`, expired: "실행기가 받지 못해 버렸어요. 다시 넣어 주세요." }[q.status];
      const r = el("div", "mp-key");
      r.append(el("span", "mp-key-name", `${q.name} ••••${q.last4}`), el("span", `tag ${q.status === "pending" ? "" : "warn"}`, q.status === "pending" ? "대기" : "실패"));
      s.append(r, el("small", "mp-key-meta", msg));
    }
    if (!p.custom) s.append(actionsRow(button("키 추가", "", null, () => openKeyDialog(p))));
    return s;
  }
  // compact (설정 › 에이전트 detail): no title, no 모델 추가 — the list comes from the account, not typed in.
  function modelsCard(p, compact) {
    const c = compact ? el("section", "mp-card ag-models") : card("모델", "켠 모델만 라우팅과 AI 도구에서 골라 쓸 수 있어요.");
    const addRow = el("div", "model-add");
    addRow.hidden = true;
    const mi = el("input");
    mi.className = "mono";
    mi.placeholder = "모델 이름 (예: glm-5.3)";
    const addIt = async () => {
      const m = mi.value.trim();
      if (!/^[A-Za-z0-9][A-Za-z0-9._:/ -]{0,79}$/.test(m)) { notice("모델 이름은 글자·숫자·.-_:/ 만 써요."); return; }
      await switchModel(p.id, m, true, { add: true });
      notice(`${m}를 넣었어요. 워크플로우 모델 목록에 보여요.`);
    };
    mi.addEventListener("keydown", (e) => { if (e.key === "Enter" && !e.isComposing) void addIt(); });
    addRow.append(mi, button("넣기", "primary small", null, addIt));
    // A 직접 추가 provider may list hundreds: the ones turned on come first.
    const all = isCustom(p.id) ? modelsOf(p).sort((a, b) => Number(enabled(p.id, b)) - Number(enabled(p.id, a))) : modelsOf(p);
    const extra = extraOf(p.id);
    // The filter's text and focus survive the screen redrawing.
    const q = () => (filters[p.id] || "").trim().toLowerCase();
    const tools = el("div", "mp-model-tools");
    const f = el("input", "mp-model-filter");
    f.type = "search";
    f.value = filters[p.id] || "";
    f.placeholder = all.length > 20 ? `모델 ${all.length}개에서 찾기` : "모델 검색";
    f.setAttribute("aria-label", "모델 찾기");
    f.addEventListener("focus", () => { typing = p.id; });
    f.addEventListener("blur", (ev) => { if (ev.relatedTarget) typing = null; });
    if (typing === p.id) requestAnimationFrame(() => { if (!document.activeElement || document.activeElement === document.body) { f.focus(); f.setSelectionRange(f.value.length, f.value.length); } });
    const re = button(compact ? "목록 새로고침" : "새로고침", compact ? "" : "ghost", null, () => reload(re));
    tools.append(f, re);
    if (!compact) tools.append(button("모델 추가", "", null, () => { addRow.hidden = !addRow.hidden; mi.focus(); }));
    c.append(tools, addRow);
    const ml = el("div", "mp-models");
    f.addEventListener("input", () => { filters[p.id] = f.value; ml.querySelectorAll(".mp-model-row").forEach((r) => { r.hidden = !!q() && !r.dataset.model.toLowerCase().includes(q()); }); });
    if (!all.length) ml.append(el("p", "mp-empty", compact ? "서버 실행기가 알려 준 모델이 없어요. '목록 새로고침'으로 다시 받아 보세요." : "서버 실행기가 알려 준 모델이 없어요. '모델 추가'로 넣을 수 있어요."));
    let onCount = 0;
    for (const m of all) {
      const on = enabled(p.id, m);
      if (on) onCount++;
      const r = el("div", `mp-model-row${on ? "" : " off"}`);
      r.dataset.model = m;
      r.hidden = !!q() && !m.toLowerCase().includes(q());
      const t = el("div", "mp-model");
      const shown = modelName(p.id, m);
      t.append(el("code", "", shown));
      if (shown !== m) t.append(el("small", "dim mono", m));
      if (extra.includes(m) && !(p.models || []).includes(m)) t.append(el("span", "tag", "직접 추가"));
      const acts = el("div", "mp-model-actions");
      const tag = testTag(p.id, m);
      if (tag) acts.append(tag);
      acts.append(pencil("model", p.id, m, shown, m));
      const plug = button("", "icon-btn plain", "plug", () => testModel(p.id, m, plug));
      plug.title = "연결 시험";
      plug.setAttribute("aria-label", `${m} 연결 시험`);
      acts.append(plug);
      if (extra.includes(m) && !(p.models || []).includes(m)) {
        const del = button("", "icon-btn plain", "trash", async () => { if (await ask(`${m}를 목록에서 뺄까요?`)) void switchModel(p.id, m, true, { remove: true }); });
        del.setAttribute("aria-label", `${m} 빼기`);
        acts.append(del);
      }
      const s = el("button", "switch");
      s.type = "button";
      s.setAttribute("role", "switch");
      s.setAttribute("aria-checked", String(on));
      s.setAttribute("aria-label", `${m} 켜기`);
      s.append(el("i"));
      // 직접 추가: on means listed in extra, so turning on adds and turning off removes it
      let change = {};
      if (isCustom(p.id)) change = on ? { remove: true } : { add: true };
      s.addEventListener("click", () => switchModel(p.id, m, !on, change));
      acts.append(s);
      r.append(t, acts);
      ml.append(r);
    }
    c.append(ml);
    if (all.length && !compact) c.append(el("small", "mp-count", `${all.length}개 중 ${onCount}개 켜짐`));
    return c;
  }
  // ---------------------------------------------------------------- 설정 › 에이전트 (Figma 1300:5254)
  // Which provider and CLI an agent runs on, so its row and detail read the same report as 모델 설정.
  const AGENT_SOURCE = { claude_code: ["anthropic", "claude"], codex: ["openai", "codex"], gemini: ["google", "gemini"], zcode: ["zai", "glm"], grok: ["xai", "grok"] };
  const PLAN_NAMES = { max: "MAX", pro: "PRO", plus: "PLUS", team: "TEAM", free: "FREE", enterprise: "ENTERPRISE", supergrok: "SUPER GROK" };
  const planName = (plan) => (plan ? PLAN_NAMES[String(plan).toLowerCase().replace(/[\s_-]/g, "")] || String(plan).toUpperCase() : "");
  // An agent's report: its provider, its CLI's accounts (the default first) and whether a key is stored.
  function agentSource(agentId) {
    const [pid, cli] = AGENT_SOURCE[agentId] || [];
    if (!pid || state.data.providers == null) return null;
    const p = providers().find((x) => x.id === pid);
    if (!p) return null;
    const accounts = (p.accounts || []).filter((a) => !a.cli || a.cli === cli).sort((a, b) => Number(!!b.default) - Number(!!a.default));
    const main = accounts.find((a) => a.status === "connected") || accounts[0] || null;
    return { p, cli, accounts, main, info: accounts.find((a) => a.cli_info?.version)?.cli_info || null, keys: p.keys || [] };
  }
  // The row's CLI version and, when a newer one exists, a button that installs it (the same request as 모델 설정).
  function cliBadge(src) {
    const a = src.accounts.find((x) => x.cli_info?.version);
    if (!a) return [];
    const info = a.cli_info;
    const out = [el("span", "ag-cli", `CLI ${info.version}`)];
    const pending = (doc().requests || []).some((q) => q.kind === "cli_update" && q.cli === a.cli && q.status === "pending");
    if (pending) out.push(el("span", "ag-update busy", "업데이트 중…"));
    else if (info.update && info.latest) {
      const can = UPDATABLE[src.p.id] === a.cli && info.via;
      const b = el("button", "ag-update", `↓ ${info.latest}`);
      b.type = "button";
      b.title = can ? `${CLI_NAMES[a.cli] || a.cli}를 ${info.latest}로 업데이트` : `새 버전 ${info.latest}: 이 설치 방식은 직접 업데이트해 주세요`;
      b.disabled = !can;
      b.addEventListener("click", (e) => { e.stopPropagation(); void updateCLI(src.p, a, b); });
      out.push(b);
    }
    return out;
  }
  const quotaMini = (q) => {
    const used = q.left_pct == null ? null : 100 - q.left_pct;
    const w = el("span", "ag-quota");
    const bar = el("i");
    const fill = el("b");
    fill.style.width = `${used ?? 0}%`;
    bar.append(fill);
    w.append(el("small", "", windowName(q.window).replace(" 한도", "").replace("주간", "7일")), bar, el("small", "", used == null ? "—" : `${used}%`));
    return w;
  };
  // 계정: every login of this agent's CLI. 사용 중 = the default one, 다음 차례 = another signed-in one, 안 씀 = signed out.
  function agentAccounts(src, title) {
    const c = el("section", "ag-card");
    const live = src.accounts.filter((a) => a.status === "connected").length;
    const head = el("div", "ag-card-head");
    head.append(el("strong", "", "계정"), el("small", "", src.accounts.length ? `${src.accounts.length}개 중 ${live}개 사용` : ""));
    c.append(head);
    const list = el("div", "ag-accounts");
    for (const a of src.accounts) {
      const on = a.status === "connected";
      const row = el("div", `ag-account${on ? "" : " off"}`);
      const mark = el("span", `ag-check${on ? " on" : ""}`);
      if (on) mark.append(icon("check"));
      const name = el("div", "ag-account-name");
      const top = el("div", "ag-account-top");
      top.append(el("strong", "", labelOf("account", src.p.id, a.id) || a.label || CLI_NAMES[a.cli] || a.id));
      if (a.plan) top.append(el("span", "ag-plan", planName(a.plan)));
      name.append(top);
      const windows = a.quotas?.length ? a.quotas : a.quota ? [a.quota] : [];
      if (on && windows.length) { const qs = el("div", "ag-quotas"); windows.slice(0, 2).forEach((q) => qs.append(quotaMini(q))); name.append(qs); }
      else if (!on) name.append(el("small", "ag-account-warn", a.status === "expired" ? "다시 로그인해 주세요" : "로그인 필요"));
      const inUse = on && a === src.main; // the default login, else the first signed-in one
      let pillText = "안 씀";
      if (on) pillText = inUse ? "사용 중" : "다음 차례";
      const re = button("", "icon-btn plain", "arrows-clockwise", () => reload(re));
      re.title = "한도 새로 확인";
      re.setAttribute("aria-label", `${a.label || a.id} 한도 새로 확인`);
      row.append(mark, name, el("span", `ag-pill${inUse ? " live" : ""}`, pillText), re);
      list.append(row);
    }
    if (!src.accounts.length) list.append(el("p", "ag-empty", "로그인한 계정이 없어요."));
    const add = el("button", "ag-add-line");
    add.type = "button";
    add.append(icon("plus"), el("span", "", `다른 ${title} 계정 추가`));
    add.addEventListener("click", () => openAccountHelp(src.p));
    list.append(add);
    c.append(list);
    return c;
  }
  // API 행의 상세: 저장된 키(끝 네 자리만)와 키 추가.
  function agentKeys(src) {
    const c = el("section", "ag-card");
    const head = el("div", "ag-card-head");
    head.append(el("strong", "", "API 키"), el("small", "", src.keys.length ? `${src.keys.length}개` : ""));
    c.append(head);
    const list = el("div", "ag-accounts");
    for (const k of src.keys) {
      const row = el("div", "ag-account");
      const mark = el("span", "ag-check on");
      mark.append(icon("check"));
      const name = el("div", "ag-account-name");
      const top = el("div", "ag-account-top");
      top.append(el("strong", "", k.name), el("span", "ag-plan", "API"));
      name.append(top, el("small", "mono", [`••••${k.last4}`, k.monthly_budget_usd != null ? `$${k.monthly_budget_usd}/월` : ""].filter(Boolean).join(" · ")));
      const del = button("", "icon-btn plain", "trash", () => removeKey(src.p.id, k));
      del.setAttribute("aria-label", `${k.name} 키 지우기`);
      row.append(mark, name, del);
      list.append(row);
    }
    const add = el("button", "ag-add-line");
    add.type = "button";
    add.append(icon("plus"), el("span", "", "키 추가"));
    add.addEventListener("click", () => openKeyDialog(src.p));
    list.append(add);
    c.append(list);
    return c;
  }
  // AI 도구 기본 모델 (설정 › 에이전트): which model fills prompt fields, polishes text and classifies. Empty = the runner's default.
  // Grouped by provider and saved with it: several providers offer the same model name. Value = "provider|model" (model names have no |).
  // combo.js draws the optgroups like the workflow model list: the provider's logo (the same one as its 모델 설정 chip) and name, then each model with its provider and input kind.
  function assistPicker() {
    const d = doc();
    const sel = el("select", "mp-assist");
    sel.setAttribute("aria-label", "AI 도구 기본 모델");
    const current = d.assist_model ? `${d.assist_provider || ""}|${d.assist_model}` : "";
    sel.append(Object.assign(el("option", "", "기본값 · 서버 설정"), { value: "" }));
    const groups = enabledByProvider();
    const tr = (x) => (window.ArkismI18n ? window.ArkismI18n.translate(x) : x);
    for (const p of groups) {
      const g = el("optgroup"); g.label = p.name; g.logo = logo(p);
      for (const m of p.models) {
        const o = Object.assign(el("option", "", m), { value: `${p.id}|${m}` });
        o.dataset.hint = p.name;
        o.dataset.tag = tr(window.ArkismBrand?.kindLabel(m) || "");
        g.append(o);
      }
      sel.append(g);
    }
    if (current && ![...sel.options].some((o) => o.value === current)) sel.append(Object.assign(el("option", "", d.assist_model), { value: current })); // picked earlier, now off or saved without a provider
    sel.value = current;
    sel.addEventListener("change", async () => {
      const [provider, model] = sel.value ? sel.value.split("|") : ["", ""];
      const from = groups.find((p) => p.id === provider)?.name;
      sel.disabled = true;
      try {
        state.data.providers = (await request("/v1/providers/assist_model", { method: "POST", body: JSON.stringify({ scope: state.scope, model, ...(provider ? { provider } : {}) }) })).providers;
        notice(!model ? "AI 도움은 서버 기본 모델로 답해요." : from ? `AI 도움은 이제 ${from}의 ${model}로 답해요.` : `AI 도움은 이제 ${model}로 답해요.`);
        render(false);
      } catch (e) { notice(errText(e)); sel.value = current; } finally { sel.disabled = false; }
    });
    return sel;
  }
  let kit = null;
  function tab(k) {
    kit = k;
    const add = button("공급자 추가", "primary", null, () => openKeyDialog({ id: "custom", name: "직접 추가한 제공자", custom: true }));
    if (state.data.providers === null) {
      const g = el("section", "mp-card");
      g.append(el("p", "pg-hint", "이 서버는 아직 모델 설정을 지원하지 않아요. 서버를 새 버전으로 바꾸면 보여요."));
      return [g];
    }
    const d = doc();
    const out = [];
    if (!d.collected_at) out.push(el("p", "callout-line", "서버 실행기가 아직 보고하지 않았어요. 실행기를 켜면 로그인 상태와 한도가 보여요."));
    const list = providers();
    if (!list.some((p) => p.id === selected)) selected = list[0].id;
    const chips = el("div", "mp-chips");
    chips.setAttribute("role", "tablist");
    for (const p of list) {
      const b = el("button", `mp-chip${p.id === selected ? " active" : ""}`);
      b.type = "button";
      b.setAttribute("role", "tab");
      b.setAttribute("aria-selected", String(p.id === selected));
      const warn = (p.accounts || []).some((a) => a.status !== "connected");
      const ready = (p.accounts || []).some((a) => a.status === "connected") || (p.keys || []).length;
      const kindText = warn ? "확인" : !ready ? "대기" : p.kind === "subscription" ? "구독" : "API";
      b.append(logo(p), el("span", "", p.name), el("small", warn ? "warn" : "", kindText));
      b.addEventListener("click", () => { selected = p.id; render(false); });
      chips.append(b);
    }
    // 사용량 보기 · 공급자 추가 sit at the right end of the chip row, next to what they add to
    const bar = el("div", "mp-chipbar"), acts = el("div", "mp-chip-actions");
    acts.append(button("사용량 보기", "ghost", null, () => go("usage")), add);
    bar.append(chips, acts);
    out.push(bar);
    const p = list.find((x) => x.id === selected);
    out.push(accountsCard(p), modelsCard(p));
    kit.side(keysSection(p),
      kit.sideSec("한도 안내", el("p", "", "한도를 알려 주지 않는 계정은 안내만 보여 줘요. 계정을 추가하면 로그인 방법을 안내해요.")));
    return out;
  }

  // ---------------------------------------------------------------- API key dialog
  function openKeyDialog(p) {
    const dialog = el("dialog", "crew-dialog key-dialog");
    const form = el("form");
    form.method = "dialog";
    const head = el("div", "dialog-head");
    const ht = el("div", "mp-head");
    ht.append(logo(p), el("h2", "", `${p.name} API 키 추가`));
    const close = el("button", "icon-btn plain");
    close.value = "cancel";
    close.setAttribute("aria-label", "닫기");
    close.append(icon("x"));
    head.append(ht, close);
    const body = el("div", "dialog-body");
    const field = (label, input, hint) => {
      const f = el("label", "field", label);
      f.append(input);
      if (hint) f.append(hint);
      return f;
    };
    const name = el("input");
    name.maxLength = 60;
    name.placeholder = `업무용 ${p.name}`;
    const key = el("input");
    key.type = "password";
    key.autocomplete = "off";
    key.spellcheck = false;
    key.required = true;
    key.placeholder = "키를 붙여 넣어요";
    const keyRow = el("div", "key-input");
    const peek = el("button", "icon-btn plain", "보기");
    peek.type = "button";
    peek.addEventListener("click", () => { key.type = key.type === "password" ? "text" : "password"; peek.textContent = key.type === "password" ? "보기" : "숨기기"; });
    keyRow.append(key, peek);
    const keyHint = el("div", "key-hint");
    if (known(p.id)) {
      const a = el("a", "", `키 받기 · ${p.name}`);
      a.href = known(p.id)[3];
      a.target = "_blank";
      a.rel = "noreferrer";
      keyHint.append(a);
    } else keyHint.append(el("span"));
    keyHint.append(el("small", "dim", "서버에만 저장 · 화면엔 끝 4자리만"));
    const budget = el("input");
    budget.type = "number";
    budget.min = "0";
    budget.step = "1";
    budget.placeholder = "$30";
    const base = el("input");
    base.placeholder = "https://…/v1";
    base.required = !!p.custom;
    const adv = el("details", "key-adv");
    if (p.custom) adv.open = true;
    adv.append(el("summary", "", p.custom ? "기본 주소 (필수)" : "고급 · 기본 주소 바꾸기 (프록시·게이트웨이를 쓸 때)"), base);
    const result = el("div", "key-result");
    result.hidden = true;
    const chips = el("div", "key-chips");
    const off = new Set();
    let found = [];
    function showChips() {
      chips.replaceChildren();
      if (!found.length) return;
      chips.append(el("small", "dim", "모델 선택 목록에 보이기"));
      const row = el("div", "chip-row");
      for (const m of found) {
        const c = el("button", `chip mono${off.has(m) ? "" : " active"}`, m);
        c.type = "button";
        c.addEventListener("click", () => { off.has(m) ? off.delete(m) : off.add(m); showChips(); });
        row.append(c);
      }
      chips.append(row);
    }
    const err = el("p", "form-error");
    err.setAttribute("role", "alert");
    body.append(field("이름 (선택)", name, el("small", "dim", "키가 여러 개일 때 구분하는 이름이에요")), field("API 키", keyRow, keyHint),
      field("월 예산 (선택)", budget, el("small", "dim", "정해 두면 쓴 만큼 빼서 보여 줘요")), adv, result, chips, err);
    const payload = (testOnly) => ({
      scope: state.scope, provider: p.id, name: name.value.trim() || (testOnly ? "" : `${p.name} 키`), key: key.value.trim(),
      base_url: base.value.trim(), ...(budget.value !== "" ? { monthly_budget_usd: Number(budget.value) } : {}), ...(testOnly ? { test_only: true } : {})
    });
    // Z.ai keys go to its https address only; say so here instead of after the dialog closes (#171)
    const baseError = () => p.id === "zai" && base.value.trim() && !base.value.trim().startsWith("https://") ? "Z.ai 주소는 https://로 시작해야 해요." : "";
    function say(ok, title, text) {
      result.hidden = false;
      result.className = `key-result ${ok ? "ok" : "bad"}`;
      result.replaceChildren(icon(ok ? "check-circle" : "warning-circle"));
      const t = el("div");
      t.append(el("strong", "", title), el("small", "", text));
      result.append(t);
    }
    const foot = el("div", "dialog-foot");
    const test = button("연결 테스트", "ghost", "plug");
    test.type = "button";
    const cancel = button("취소", "ghost");
    cancel.type = "submit";
    cancel.value = "cancel";
    const ok = button("저장", "primary");
    ok.type = "submit";
    ok.value = "save";
    const spacer = el("span", "grow");
    foot.append(test, spacer, cancel, ok);
    test.addEventListener("click", async () => {
      err.textContent = "";
      if (!key.value.trim()) { err.textContent = "키를 먼저 넣어 주세요."; return; }
      if (baseError()) { err.textContent = baseError(); return; }
      test.disabled = true;
      say(true, "확인하는 중…", "서버가 공급자에 물어보고 있어요.");
      try {
        const r = await request("/v1/providers/keys", { method: "POST", body: JSON.stringify(payload(true)) });
        state.data.providers = r.providers;
        const q = await waitFor(last(r).id);
        if (!q) say(false, "답이 없어요", "서버 실행기가 켜져 있는지 확인해 주세요.");
        else if (q.status === "done") {
          found = q.models || [];
          if (p.custom) { off.clear(); found.forEach((m) => off.add(m)); } // many models: the owner picks the ones to use
          say(true, `키가 맞아요 · 모델 ${found.length}개를 찾았어요`, p.custom ? "쓸 모델을 눌러 켜요. 켠 모델만 워크플로우 모델 목록에 보여요" : "켜 둔 모델만 워크플로우 모델 목록에 보여요");
          showChips();
        }
        else say(false, "연결하지 못했어요", q.error || "키나 주소를 확인해 주세요.");
      } catch (e) { result.hidden = true; err.textContent = errText(e); } finally { test.disabled = false; }
    });
    form.append(head, body, foot);
    form.addEventListener("submit", async (e) => {
      if (e.submitter?.value !== "save") return;
      e.preventDefault();
      err.textContent = "";
      if (!key.value.trim()) { err.textContent = "키를 넣어 주세요."; return; }
      if (p.custom && !base.value.trim()) { err.textContent = "직접 추가한 제공자는 주소가 필요해요."; return; }
      if (baseError()) { err.textContent = baseError(); return; }
      ok.disabled = true;
      try {
        const r = await request("/v1/providers/keys", { method: "POST", body: JSON.stringify(payload(false)) });
        key.value = "";
        state.data.providers = r.providers;
        if (p.custom) { // the new key becomes its own provider; the models picked here are the ones it starts with
          const pid = customPid(last(r).key_id || "");
          for (const m of found) if (!off.has(m)) await switchModel(pid, m, true, { add: true });
          selected = pid;
        } else for (const m of off) await switchModel(p.id, m, false);
        dialog.close();
        notice("서버에 저장하도록 보냈어요.");
        render(false);
        const q = await waitFor(last(r).id);
        notice(!q ? "아직 저장되지 않았어요. 서버 실행기를 확인해 주세요." : q.status === "done" ? "키를 저장했어요." : `저장하지 못했어요: ${q.error || "알 수 없음"}`);
      } catch (x) { err.textContent = errText(x); } finally { ok.disabled = false; }
    });
    dialog.append(form);
    dialog.addEventListener("close", () => { key.value = ""; dialog.remove(); });
    document.body.append(dialog);
    dialog.showModal();
    (p.custom ? base : key).focus();
  }

  return { load, tab, enabledModels, enabledByProvider, assistPicker, openKeyDialog, agentSource, cliBadge, planName, agentAccounts, agentKeys, modelsCard, logo };
};
