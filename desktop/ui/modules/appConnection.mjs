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
    deps.git.reset();
    deps.routing.reset();
    deps.usage.reset();
    deps.sessions.reset(); deps.knowledge.reset(); deps.overview.reset();
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
    ssh_auth_failed: "SSH 로그인이 거부됐어요. 이 PC에서 ‘ssh 주소’가 비밀번호 없이(SSH 키로) 되는지 확인하세요.",
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

  // 저장된 SSH 연결: {host, user, port, key, bin}. 예전 판은 주소 문자열 하나만 저장했다.
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
    const host = row("Host", "user@host 또는 ~/.ssh/config의 Host 별칭", "server.example.com", f.host);
    const user = row("User", "비우면 ~/.ssh/config 또는 지금 사용자", "root", f.user);
    const port = row("Port", "비우면 22 또는 ~/.ssh/config의 포트", "22", f.port ? String(f.port) : "");
    port.inputMode = "numeric";
    const key = row("Identity file", "개인 키 경로. 비우면 ssh-agent 또는 ~/.ssh/config", "~/.ssh/id_ed25519", f.key);
    const bin = row("Arkism 경로 (선택)", "서버의 arkism 프로그램 전체 경로. 비우면 자동으로 찾아요", "자동", f.bin);
    const list = deps.el("datalist");
    list.id = `ssh-keys-${++sshFormSeq}`;
    key.setAttribute("list", list.id);
    box.append(list);
    deps.tauri?.invoke("ssh_keys").then((keys) => { for (const k of keys || []) { const o = deps.el("option"); o.value = k; list.append(o); } }).catch(() => {});
    const read = () => {
      const p = port.value.trim();
      return { host: host.value.trim(), user: user.value.trim() || null, port: p ? Number(p) : null, key: key.value.trim() || null, bin: bin.value.trim() || null };
    };
    return { node: box, read, focus: () => host.focus() };
  }

  function sshFormError(form) {
    if (!form.host) return "Host를 넣어 주세요.";
    if (form.port !== null && !(Number.isInteger(form.port) && form.port > 0 && form.port < 65536)) return "Port는 1~65535 사이 숫자예요.";
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
    deps.state.ssh = form;
    deps.store("arkism.ssh", JSON.stringify(form));
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
      deps.$("#unlock-error").textContent = err.message;
      deps.home.sshError(err.code || "error", err.message, form);
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
    // 처음 실행(choose): 이 PC / SSH 두 카드만. 이 PC를 고르면(storage) 저장 위치부터 정한다.
    // 내장 Arkism을 켜는 중이거나 켜지 못했을 때는 연결 칸 대신 진행 상태만 보여 준다.
    const storage = deps.state.mode === "storage", choose = deps.state.mode === "choose" || storage, builtIn = deps.state.mode === "local";
    deps.$("#start-choice").hidden = deps.state.mode !== "choose";
    deps.$("#start-storage").hidden = !storage;
    deps.$("#unlock-box").hidden = choose;
    deps.$("#unlock-title").textContent = storage ? "데이터를 어디에 저장할까요?" : choose ? "Arkism을 어디서 쓸까요?" : "서버의 Arkism에 연결";
    for (const id of ["#ssh-form", "#ssh-actions", "#ssh-hint", "#ssh-test-result"]) deps.$(id).hidden = builtIn || choose;
    deps.$("#local-retry").hidden = !builtIn || !deps.state.localError;
    deps.$("#remote-link").hidden = !builtIn || !deps.state.localError;
    deps.$("#local-link").hidden = builtIn || choose || !deps.state.defaults.local;
    deps.$("#unlock-intro").textContent = storage ? "기억·설정·토큰이 모두 이 폴더에 저장돼요. 나중에 설정 › 연결에서 바꿀 수 있어요." : choose ? "처음 한 번만 고르면 돼요. 나중에 설정 › 연결에서 바꿀 수 있어요." : builtIn
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
    deps.store("arkism.mode", null);
    if (deps.state.defaults.local) { location.reload(); return; }
    lock();
  }

  // 서버 연결 끊김 카드의 지금 다시 연결: 쓰던 연결 방식 그대로 한 번 더.
  async function reconnect(b) {
    if (b) b.disabled = true;
    try {
      if (deps.state.mode === "remote" && deps.tauri && deps.state.ssh) await sshConnect(deps.state.ssh);
      else if (deps.state.mode === "local" && deps.tauri) await startLocal();
      else await deps.loadAll();
    } catch (e) { deps.notice(e.message); } finally { if (b?.isConnected) b.disabled = false; }
  }

  deps.$("#local-retry").addEventListener("click", startLocal);

  deps.$("#remote-link").addEventListener("click", useServer);

  deps.$("#local-link").addEventListener("click", useLocal);

  // 저장 위치: 기본(앱 데이터 폴더) 또는 직접 고른 폴더. 고른 폴더는 앱이 검사한 뒤 local_start 전에 쓴다.
  const STORAGE_ERRORS = {
    local_location_absolute: "C:\\ 나 /로 시작하는 전체 경로를 넣어 주세요.",
    local_location_not_folder: "파일이 아니라 폴더 경로를 넣어 주세요.",
    local_location_not_empty: "빈 폴더나 Arkism 데이터 폴더만 쓸 수 있어요.",
    local_location_unreadable: "이 폴더를 읽을 수 없어요.",
    local_location_unwritable: "이 폴더에 쓸 수 없어요. 권한을 확인해 주세요.",
  };

  const storageError = (err) => { const m = String(err?.message || err); return STORAGE_ERRORS[m.split(":")[0]] || m; };

  function pickStorage(custom) {
    deps.$("#storage-default").setAttribute("aria-checked", String(!custom));
    deps.$("#storage-custom").setAttribute("aria-checked", String(custom));
    deps.$("#storage-path-field").hidden = !custom;
    deps.$("#storage-note").hidden = !(custom && deps.state.location?.default_has_data);
    deps.$("#storage-error").textContent = "";
    if (custom) deps.$("#storage-path").focus();
  }

  async function chooseStorage() {
    deps.state.mode = "storage";
    unlockMode();
    const loc = await deps.tauri.invoke("local_location").catch(() => null);
    deps.state.location = loc;
    deps.$("#storage-default-path").textContent = loc?.default || "";
    const custom = !!loc && loc.next !== loc.default;
    if (custom) deps.$("#storage-path").value = loc.next;
    pickStorage(custom);
  }

  async function startStorage() {
    const custom = deps.$("#storage-custom").getAttribute("aria-checked") === "true";
    const path = custom ? deps.$("#storage-path").value.trim() : null;
    if (custom && !path) { deps.$("#storage-error").textContent = "폴더 경로를 넣어 주세요."; deps.$("#storage-path").focus(); return; }
    const b = deps.$("#storage-start");
    b.disabled = true;
    try {
      await deps.tauri.invoke("local_location_set", { path });
    } catch (err) {
      deps.$("#storage-error").textContent = storageError(err);
      return;
    } finally { b.disabled = false; }
    deps.store("arkism.mode", "local");
    void startLocal();
  }

  deps.$("#storage-default").addEventListener("click", () => pickStorage(false));

  deps.$("#storage-custom").addEventListener("click", () => pickStorage(true));

  deps.$("#storage-start").addEventListener("click", startStorage);

  deps.$("#storage-back").addEventListener("click", () => { deps.state.mode = "choose"; unlockMode(); });

  deps.$("#choose-local").addEventListener("click", () => void chooseStorage());

  deps.$("#choose-ssh").addEventListener("click", () => { deps.state.mode = "remote"; unlockMode(); unlockSSH().focus(); });

  deps.$("#unlock-form").addEventListener("submit", (e) => {
    e.preventDefault();
    if (deps.state.mode === "storage") return void startStorage();
    if (deps.state.mode === "choose") return;
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
    get unlockConnect() { return unlockConnect; },
    get useLocal() { return useLocal; },
    get useServer() { return useServer; }
  };
}
