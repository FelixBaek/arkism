// Owns lock and server connections. Dependencies are explicit live ports, never a global scope.
export function createAppConnection(deps) {


  // ---------------------------------------------------------------- auth, settings, keys
  function unlocked(on) {
    deps.$("#unlock").hidden = on;
    deps.$("#app").inert = !on;
    on ? deps.$("#app").removeAttribute("aria-hidden") : deps.$("#app").setAttribute("aria-hidden", "true");
  }

  function lock() {
    deps.catalogQuery.reset(); deps.collectionPaging.reset(); deps.virtualList.clear(); deps.similarity.reset();
    deps.memoryPages.reset(); deps.memoryResults.reset(); deps.maintenanceRecords.clear();
    deps.crew.reset();
    deps.routing.reset();
    deps.usage.reset();
    deps.knowledge.reset(); deps.overview.reset();
    deps.generation++;
    deps.state.token = "";
    deps.state.caps = null;
    deps.store("arkism.token", null);
    deps.store("arkism.email", null);
    unlocked(false);
    deps.setStatus("", "연결 끊김");
    unlockMode();
    unlockSSH().focus();
  }

  async function open() {
    deps.state.caps = await deps.request(`/v1/capabilities?${deps.query()}`);
    unlocked(true);
    await deps.loadAll();
    deps.pages.checkUpdate(true);
  }

  // 서버 연결은 SSH 주소 하나로: 앱이 그 로그인으로 서버의 접속 정보(주소·토큰·범위)를 받아 오고
  // API를 이 PC로 끌어온다. 토큰·Google 로그인·워크스페이스를 사용자가 넣을 일은 없다.
  const SSH_ERRORS = {
    ssh_address_invalid: "주소를 확인하세요. user@서버주소 또는 user@서버주소:포트 처럼 적어요.",
    ssh_missing: "이 PC에서 ssh 프로그램을 찾지 못했어요. OpenSSH(또는 Git for Windows)를 설치해 주세요.",
    ssh_auth_failed: "SSH 로그인이 거부됐어요. 이 PC에서 ‘ssh 주소’가 비밀번호 없이(SSH 키로) 되는지 확인하세요. 키가 없으면 인증 방식을 비밀번호로 바꾸세요.",
    ssh_password_rejected: "비밀번호 로그인이 거부됐어요. 비밀번호를 확인하세요. 서버가 비밀번호 로그인을 막아 두었으면 SSH 키가 필요해요.",
    ssh_password_missing: "비밀번호를 넣어 주세요.",
    ssh_host_key_changed: "서버의 SSH 키가 예전과 달라 접속을 막았어요. 관리자에게 새 지문을 확인한 뒤 다시 연결하세요.",
    ssh_host_key_cancelled: "서버 지문 확인을 취소했어요. 접속하지 않았습니다.",
    ssh_host_key_confirmation_required: "서버 지문 확인이 필요해요. 다시 연결해 주세요.",
    ssh_host_key_probe_failed: "서버 지문을 가져오지 못했어요. 주소·포트와 SSH 연결을 확인하세요.",
    ssh_host_key_review_expired: "지문 확인 시간이 지났어요. 다시 연결해 주세요.",
    ssh_host_key_review_stale: "그사이 승인한 키가 바뀌었어요. 다시 연결해 주세요.",
    ssh_trust_store_failed: "저장한 서버 키를 확인할 수 없어 접속을 막았어요.",
    ssh_unreachable: "서버에 닿지 못했어요. 주소·포트와 네트워크 연결을 확인하세요.",
    arkism_not_found: "서버에 로그인은 됐지만 Arkism이 설치돼 있지 않아요.",
    arkism_too_old: "서버의 Arkism이 오래돼서 이 연결 방식을 몰라요. 서버의 Arkism을 업데이트해 주세요."
  };

  const sshErrorText = (code) => SSH_ERRORS[code] || (code.startsWith("arkism_connect_failed") ? `서버의 Arkism이 접속 정보를 주지 못했어요 (${code.split(": ")[1] || "알 수 없음"}).` : `연결하지 못했어요 (${code}).`);

  // 저장된 SSH 연결: {host, user, port, key, bin, auth}. 예전 판은 주소 문자열 하나만 저장했다.
  // 비밀번호는 arkism.ssh에 넣지 않는다. 연결되면 앱(Rust)이 OS 키체인에 저장하고, 빈 칸이면 거기서 꺼내 쓴다.
  // 여기 sshPassword는 키체인이 없는 PC에서 이 창이 열려 있는 동안 다시 연결할 때 쓴다.
  let sshPassword = "";

  function readSSH(v) {
    if (!v) return null;
    try { const o = JSON.parse(v); if (o && typeof o.host === "string") return o; } catch (_) { /* a plain address */ }
    return { host: v };
  }

  const sshLabel = (f) => f?.host ? (f.user && !f.host.includes("@") ? `${f.user}@${f.host}` : f.host) + (f.port ? `:${f.port}` : "") : "";

  let sshFormSeq = 0;

  // SSH 입력 칸: 연결 화면과 설정 › 고급 › 연결 방식이 같이 쓴다. 빈 칸은 ssh가 정한다(~/.ssh/config, ssh-agent).
  function sshFields(saved) {
    const box = deps.el("div", "ssh-rows");
    const f = saved || {};
    const row = (label, hint, ph, value) => {
      const r = deps.el("label", "ssh-row");
      const text = deps.el("span", "ssh-row-text");
      text.append(deps.el("strong", "", label), deps.el("small", "", hint));
      const i = deps.el("input");
      i.placeholder = ph; i.value = value || ""; i.autocomplete = "off"; i.spellcheck = false; i.setAttribute("autocapitalize", "off");
      r.append(text, i);
      box.append(r);
      return i;
    };
    const host = row("호스트", "user@host 또는 ~/.ssh/config의 Host 별칭", "server.example.com", f.host);
    const user = row("사용자", "비우면 ~/.ssh/config 또는 지금 사용자", "root", f.user);
    const port = row("포트", "비우면 22 또는 ~/.ssh/config의 포트", "22", f.port ? String(f.port) : "");
    port.inputMode = "numeric";
    let auth = f.auth === "password" ? "password" : "key";
    const authRow = deps.el("div", "ssh-row ssh-auth");
    authRow.setAttribute("role", "group");
    authRow.setAttribute("aria-label", "인증 방식");
    box.append(authRow);
    const key = row("키 파일", "개인 키 경로. 비우면 ssh-agent 또는 ~/.ssh/config", "~/.ssh/id_ed25519", f.key);
    const pass = row("비밀번호", "연결되면 이 PC의 키체인(자격 증명 관리자)에 저장해 다시 묻지 않아요. 비워 두면 저장된 비밀번호를 써요", "", "");
    pass.type = "password";
    const drawAuth = () => {
      const seg = deps.segment([["key", "SSH 키"], ["password", "비밀번호"]], auth, (k) => { auth = k; drawAuth(); (k === "password" ? pass : key).focus(); });
      const text = deps.el("span", "ssh-row-text");
      text.append(deps.el("strong", "", "인증 방식"), deps.el("small", "", auth === "password" ? "서버의 Git·스킬 화면은 이 연결로 돼요. 에이전트의 기억 수집은 SSH 키가 있어야 서버로 가요." : "SSH 키 또는 ssh-agent로 로그인해요"));
      authRow.replaceChildren(text, seg);
      key.closest(".ssh-row").hidden = auth === "password";
      pass.closest(".ssh-row").hidden = auth !== "password";
    };
    drawAuth();
    const bin = row("Arkism 경로 (선택)", "서버의 arkism 프로그램 전체 경로. 비우면 자동으로 찾아요", "자동", f.bin);
    const list = deps.el("datalist");
    list.id = `ssh-keys-${++sshFormSeq}`;
    key.setAttribute("list", list.id);
    box.append(list);
    const hosts = deps.el("datalist");
    hosts.id = `ssh-hosts-${sshFormSeq}`;
    host.setAttribute("list", hosts.id);
    box.append(hosts);
    const fill = (cmd, into) => deps.tauri?.invoke(cmd).then((items) => { for (const k of items || []) { const o = deps.el("option"); o.value = k; into.append(o); } }).catch(() => {});
    fill("ssh_keys", list);
    fill("ssh_hosts", hosts); // ~/.ssh/config의 Host 별칭
    const read = () => {
      const p = port.value.trim();
      return { host: host.value.trim(), user: user.value.trim() || null, port: p ? Number(p) : null, key: key.value.trim() || null, bin: bin.value.trim() || null,
        auth, password: auth === "password" ? pass.value || null : null };
    };
    // 비밀번호 방식으로 저장된 연결이면 비밀번호 칸부터.
    const focus = () => (auth === "password" && f.host ? pass : host).focus();
    return { node: box, read, focus };
  }

  function sshFormError(form) {
    if (!form.host) return "호스트를 넣어 주세요.";
    if (form.port !== null && !(Number.isInteger(form.port) && form.port > 0 && form.port < 65536)) return "포트는 1~65535 사이 숫자예요.";
    return "";
  }

  // SSH 테스트: 로그인하고 서버의 Arkism이 접속 정보를 주는지만 본다. 연결은 바꾸지 않는다.
  async function sshTest(form) {
    const bad = sshFormError(form);
    if (bad) return { ok: false, text: bad };
    if (!deps.tauri) return { ok: false, text: "서버 연결은 Arkism 앱에서만 돼요." };
    try {
      const r = await deps.tauri.invoke("ssh_test", { form });
      return { ok: true, text: `SSH 로그인과 서버의 Arkism 응답을 확인했어요 (${r.host}).` };
    } catch (err) {
      return { ok: false, text: sshErrorText(String(err?.message || err)) };
    }
  }

  // 연결에 실패하면 지금 쓰던 연결을 그대로 둔다.
  async function sshConnect(form) {
    const bad = sshFormError(form);
    if (bad) throw new Error(bad);
    if (!deps.tauri) throw new Error("서버 연결은 Arkism 앱에서만 돼요.");
    const before = { mode: deps.state.mode, server: deps.state.server, token: deps.state.token, scope: deps.state.scope, sshHost: deps.state.defaults.ssh_host };
    let r;
    try {
      r = await deps.tauri.invoke("ssh_connect", { form });
    } catch (err) {
      const code = String(err?.message || err);
      throw Object.assign(new Error(/^(ssh|arkism)_/.test(code) ? sshErrorText(code) : `연결하지 못했어요 (${code}).`), { code });
    }
    try {
      deps.state.mode = "remote";
      deps.state.server = r.server;
      deps.state.token = r.token;
      deps.state.email = "";
      deps.state.scope = { ...deps.defaults, workspace_id: r.workspace_id, project_id: r.project_id };
      // 서버의 Git·스킬 실행도 활성 연결의 포트·개인 키와 승인한 서버 키를 함께 쓴다.
      deps.state.defaults.ssh_host = r.ssh_host;
      await open();
    } catch (_) {
      Object.assign(deps.state, { mode: before.mode, server: before.server, token: before.token, scope: before.scope });
      deps.state.defaults.ssh_host = before.sshHost;
      throw new Error("연결은 됐지만 서버의 Arkism을 열지 못했어요. 서버에서 Arkism이 켜져 있는지 확인하세요.");
    }
    sshPassword = form.password || "";
    deps.state.ssh = { ...form, password: null };
    deps.store("arkism.ssh", JSON.stringify(deps.state.ssh));
    deps.store("arkism.mode", "remote");
    deps.store("arkism.server", null);
    deps.store("arkism.scope", null);
  }

  let unlockForm = null;

  function unlockSSH() {
    if (!unlockForm) {
      unlockForm = sshFields(deps.state.ssh);
      deps.$("#ssh-form").replaceChildren(unlockForm.node);
    }
    return unlockForm;
  }

  // manual: the person pressed 연결하기, so 시작 2·3단계 (에이전트 선택 · 연결 확인) follow on 홈.
  async function unlockConnect(manual) {
    const b = deps.$("#ssh-button");
    deps.$("#unlock-error").textContent = "";
    deps.$("#ssh-test-result").textContent = "";
    deps.home.sshError(null);
    b.disabled = true;
    b.lastChild.textContent = "연결하는 중…";
    const form = unlockSSH().read();
    try {
      await sshConnect(form);
      if (manual === true) { deps.state.start = 2; deps.go("todo"); }
    } catch (err) {
      deps.state.token = "";
      // 키체인에 저장된 비밀번호가 없음: 오류가 아니라 비밀번호 칸으로 안내한다.
      if (err.code === "ssh_password_missing") {
        unlocked(false);
        unlockMode();
        deps.$("#ssh-test-result").textContent = "저장된 비밀번호가 없어요. 비밀번호를 넣고 연결하세요.";
        unlockSSH().focus();
        return;
      }
      deps.$("#unlock-error").textContent = err.message;
      deps.home.sshError(err.code || "error", err.message, { ...form, password: null });
      unlocked(false);
      unlockMode();
    } finally {
      b.disabled = false;
      b.lastChild.textContent = "연결하기";
    }
  }

  deps.$("#ssh-test").addEventListener("click", async (e) => {
    const b = e.currentTarget, out = deps.$("#ssh-test-result");
    deps.$("#unlock-error").textContent = "";
    b.disabled = true;
    out.textContent = "확인하는 중…";
    const r = await sshTest(unlockSSH().read());
    out.textContent = r.text;
    out.classList.toggle("warn", !r.ok);
    b.disabled = false;
  });

  function unlockMode() {
    // 처음 실행(choose): 이 PC / SSH 두 카드. 이 PC를 고르면 카드 아래에 저장 위치가 펼쳐진다.
    // 내장 Arkism을 켜는 중이거나 켜지 못했을 때는 연결 칸 대신 진행 상태만 보여 준다.
    const choose = deps.state.mode === "choose", builtIn = deps.state.mode === "local";
    deps.$("#start-choice").hidden = !choose;
    deps.$("#unlock-box").hidden = choose;
    deps.$("#unlock-title").textContent = choose ? "Arkism을 어디서 쓸까요?" : "서버의 Arkism에 연결";
    for (const id of ["#ssh-form", "#ssh-actions", "#ssh-hint", "#ssh-test-result"]) deps.$(id).hidden = builtIn || choose;
    deps.$("#local-retry").hidden = !builtIn || !deps.state.localError;
    deps.$("#remote-link").hidden = !builtIn || !deps.state.localError;
    deps.$("#local-link").hidden = builtIn || choose || !deps.state.defaults.local;
    deps.$("#unlock-intro").textContent = choose ? "처음 한 번만 고르면 돼요. 나중에 설정 › 연결에서 바꿀 수 있어요." : builtIn
      ? (deps.state.localError ? `이 PC의 Arkism을 켜지 못했어요 (${deps.state.localError}). 다시 시도하거나 서버에 연결할 수 있어요.` : "이 PC의 Arkism을 준비하고 있어요…")
      : "SSH로 접속하듯 서버 주소만 넣으면 돼요. 토큰이나 로그인은 필요 없어요.";
  }

  // 기본은 이 PC의 내장 Arkism(로그인 없음). 서버 연결은 사용자가 고를 때만 쓴다.
  async function startLocal() {
    deps.tauri?.invoke("ssh_disconnect").catch(() => {});
    deps.state.mode = "local";
    // The built-in Arkism registers its tokens for the default scope only.
    deps.state.scope = { ...deps.defaults };
    deps.state.localError = "";
    unlocked(false);
    unlockMode();
    try {
      const r = await deps.tauri.invoke("local_start");
      deps.state.server = r.server;
      deps.state.token = r.token;
      await open();
    } catch (err) {
      deps.state.token = "";
      deps.state.localError = String(err?.message || err);
      unlocked(false);
      unlockMode();
    }
  }

  function useServer() {
    deps.state.mode = "remote";
    lock();
  }

  function useLocal() {
    deps.tauri?.invoke("ssh_disconnect").catch(() => {});
    deps.store("arkism.mode", "local");
    deps.store("arkism.token", null);
    location.reload();
  }

  // 연결 끊기: 서버 연결을 닫고 주소 입력으로 돌아간다. 다음 시작 때 자동으로 다시 붙지 않는다.
  function disconnect() {
    deps.tauri?.invoke("ssh_disconnect").catch(() => {});
    // "choose", not cleared: no choice at all reads as an older install that used this PC.
    deps.store("arkism.mode", deps.state.defaults.local ? "choose" : null);
    if (deps.state.defaults.local) { location.reload(); return; }
    lock();
  }

  // 서버 연결 끊김 카드의 지금 다시 연결: 쓰던 연결 방식 그대로 한 번 더.
  async function reconnect(b) {
    if (b) b.disabled = true;
    try {
      if (deps.state.mode === "remote" && deps.tauri && deps.state.ssh) await sshConnect({ ...deps.state.ssh, password: sshPassword || null });
      else if (deps.state.mode === "local" && deps.tauri) await startLocal();
      else await deps.loadAll();
    } catch (e) { deps.notice(e.message); } finally { if (b?.isConnected) b.disabled = false; }
  }

  deps.$("#local-retry").addEventListener("click", startLocal);

  deps.$("#remote-link").addEventListener("click", useServer);

  // 처음 실행에서 SSH를 골랐다가 돌아오면 선택 화면의 '이 PC'로: 저장 위치를 건너뛰지 않는다.
  let fromChoice = false;
  deps.$("#local-link").addEventListener("click", () => {
    if (!fromChoice) { useLocal(); return; }
    deps.state.mode = "choose";
    unlockMode();
    chooseLocal();
  });

  // 저장 위치 줄 (처음 실행과 설정 › 연결의 '이 PC' 카드 아래): 왼쪽 제목·설명, 오른쪽 기본값/사용자 지정과 경로.
  // 고른 폴더는 앱이 검사해 runtime-location.json에 남기고, 내장 Arkism이 아직 안 돌면 바로 쓴다.
  const STORAGE_ERRORS = {
    local_location_absolute: "C:\\ 나 /로 시작하는 전체 경로를 넣어 주세요.",
    local_location_not_folder: "파일이 아니라 폴더 경로를 넣어 주세요.",
    local_location_not_empty: "빈 폴더나 Arkism 데이터 폴더만 쓸 수 있어요.",
    local_location_unreadable: "이 폴더를 읽을 수 없어요.",
    local_location_unwritable: "이 폴더에 쓸 수 없어요. 권한을 확인해 주세요.",
  };

  function storageRow() {
    const row = deps.el("div", "storage-row");
    const text = deps.el("div");
    text.append(deps.el("strong", "", "저장 위치"), deps.el("small", "", "기억·설정·토큰이 저장되는 폴더예요. 바꿔도 지금 데이터는 옮기지 않아요."));
    const right = deps.el("div", "storage-pick");
    const path = deps.raw(deps.el("p", "storage-path mono"));
    const input = deps.el("input", "mono");
    input.setAttribute("aria-label", "폴더 경로");
    input.placeholder = "빈 폴더의 전체 경로";
    input.spellcheck = false;
    const note = deps.el("p", "storage-note", "기본 위치에 있던 데이터는 그대로 남아요.");
    const err = deps.el("p", "form-error");
    err.setAttribute("role", "alert");
    // touched: the person already picked or typed, so the late answer must not undo it.
    let loc = null, custom = false, touched = false;
    input.addEventListener("input", () => { touched = true; });
    const draw = () => {
      path.textContent = loc?.default || "…";
      const seg = deps.segment([["default", "기본값"], ["custom", "사용자 지정"]], custom ? "custom" : "default", (k) => {
        touched = true;
        custom = k === "custom";
        err.textContent = "";
        draw();
        if (custom) input.focus();
      });
      right.replaceChildren(seg, custom ? input : path, ...(custom && loc?.default_has_data ? [note] : []), err);
    };
    draw();
    row.append(text, right);
    const ready = deps.tauri.invoke("local_location").then((l) => {
      loc = l;
      if (!touched) { custom = l.next !== l.default; if (custom) input.value = l.next; }
      const typing = document.activeElement === input;
      draw();
      if (typing) input.focus();
      return l;
    }, () => { err.textContent = "저장 위치를 읽지 못했어요."; return null; });
    const wanted = () => (custom ? input.value.trim() : null);
    return {
      node: row,
      ready,
      get loc() { return loc; },
      // 지금 기억된 위치와 다른 것을 골랐는지.
      changed: () => !!loc && (wanted() ?? loc.default) !== loc.next,
      // 고른 것을 저장한다. 실패하면 줄 안에 이유를 보여 주고 null.
      async save() {
        // Lookup failed: saving now would reset a remembered folder to the default.
        if (!await ready) return null;
        err.textContent = "";
        if (custom && !wanted()) { err.textContent = "폴더 경로를 넣어 주세요."; input.focus(); return null; }
        try {
          loc = await deps.tauri.invoke("local_location_set", { path: wanted() });
          return loc;
        } catch (e) {
          const m = String(e?.message || e);
          err.textContent = STORAGE_ERRORS[m.split(":")[0]] || m;
          return null;
        }
      },
    };
  }

  // 처음 실행 › 이 PC: 카드를 고르면 아래에 저장 위치와 시작 버튼이 펼쳐진다.
  let startStorage = null;

  function chooseLocal() {
    deps.$("#choose-local").setAttribute("aria-pressed", "true");
    const body = deps.$("#start-mode-body");
    if (!startStorage) {
      startStorage = storageRow();
      const startBtn = deps.button("시작", "primary", null, startChosen);
      const acts = deps.el("div", "unlock-actions");
      acts.append(startBtn);
      body.replaceChildren(startStorage.node, acts);
    }
    body.hidden = false;
  }

  async function startChosen(e) {
    const b = e?.currentTarget;
    if (b) b.disabled = true;
    const saved = await startStorage.save();
    if (b?.isConnected) b.disabled = false;
    if (!saved) return;
    deps.store("arkism.mode", "local");
    void startLocal();
  }

  deps.$("#choose-local").addEventListener("click", chooseLocal);

  deps.$("#choose-ssh").addEventListener("click", () => { fromChoice = true; deps.state.mode = "remote"; unlockMode(); unlockSSH().focus(); });

  deps.$("#unlock-form").addEventListener("submit", (e) => {
    e.preventDefault();
    if (deps.state.mode === "choose") { if (startStorage) void startChosen(); return; }
    void unlockConnect(true);
  });

  document.querySelectorAll("[data-view]").forEach((b) => b.addEventListener("click", () => deps.go(b.dataset.view, b.dataset.tab)));

  deps.$("#search").addEventListener("input", (e) => {
    if (deps.home.onInput()) return; // 홈·설정 같은 화면: 전체 검색 (home.js)
    deps.state.query = e.target.value.trim().toLowerCase();
    if (deps.state.view === "todo" || deps.pages.views[deps.state.view] || deps.crew.pages[deps.state.view]) deps.state.view = "memories";
    deps.render(false);
  });

  return {
    get disconnect() { return disconnect; },
    get lock() { return lock; },
    get open() { return open; },
    set open(value) { open = value; },
    get readSSH() { return readSSH; },
    get reconnect() { return reconnect; },
    get sshConnect() { return sshConnect; },
    get sshErrorText() { return sshErrorText; },
    get sshFields() { return sshFields; },
    get sshLabel() { return sshLabel; },
    get sshTest() { return sshTest; },
    get startLocal() { return startLocal; },
    get storageRow() { return storageRow; },
    get unlockConnect() { return unlockConnect; },
    get unlockSSH() { return unlockSSH; },
    get useLocal() { return useLocal; },
    get useServer() { return useServer; }
  };
}
