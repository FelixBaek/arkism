// 서버 버전 맞추기: 연결하면 서버의 /healthz(버전·빌드·api_level·features)를 읽어, 앱보다 오래된 서버면
// 짧은 배너로 설정 › 정보를 가리키고 그 서버에 없는 기능만 끈다. 업데이트는 설정 › 정보의 서버 줄에서 한다.

// The API level this app needs. Raise it together with buildinfo.APILevel when a new API becomes required.
export const REQUIRED_API_LEVEL = 1;
// API additions the app turns off on a server that does not list them (buildinfo.Features on the server).
export const APP_FEATURES = ["role_model_picks", "forget_both"];
export const NEEDS_UPDATE = "서버 업데이트가 필요해요";

const numeric = (v) => /^\d+\.\d+\.\d+$/.test(v || "") ? v.split(".").map(Number) : null;

// A server from before this check answers {"status":"ok"} only: level 0, version unknown, none of the features.
export function readServerInfo(body) {
  if (!body || typeof body !== "object") return null;
  return {
    version: typeof body.version === "string" ? body.version : "",
    commit: typeof body.commit === "string" ? body.commit : "",
    built: typeof body.built === "string" ? body.built : "",
    apiLevel: Number.isInteger(body.api_level) ? body.api_level : 0,
    features: Array.isArray(body.features) ? body.features.filter((f) => typeof f === "string") : []
  };
}

// The features this app uses that the server lacks. Unknown (health not read) disables nothing.
export const missingFeatures = (info) => new Set(info ? APP_FEATURES.filter((f) => !info.features.includes(f)) : []);

// Older: below the level this app needs, or a lower release number than the app (dev builds compare by level only).
export function serverOlder(info, appVersion) {
  if (!info) return false;
  if (info.apiLevel < REQUIRED_API_LEVEL) return true;
  const s = numeric(info.version), a = numeric(appVersion);
  if (!s || !a) return false;
  for (let i = 0; i < 3; i++) if (s[i] !== a[i]) return s[i] < a[i];
  return false;
}

// 최신 / 업데이트 있음(앱보다 낮은 릴리스) / 앱보다 오래됨(이 앱이 쓰는 API가 없음).
export function serverStatus(info, appVersion) {
  if (!info) return "unknown";
  if (info.apiLevel < REQUIRED_API_LEVEL) return "older";
  return serverOlder(info, appVersion) ? "update" : "current";
}
export const STATUS_TEXT = { unknown: "확인 못 함", older: "앱보다 오래됨", update: "업데이트 있음", current: "최신" };

// "10월 26일 판 · abc1234" from the build's commit time (its date as written, no time zone shift) and revision.
export function buildText(info) {
  const d = /^\d{4}-(\d\d)-(\d\d)/.exec(info?.built || "");
  return [d ? `${Number(d[1])}월 ${Number(d[2])}일 판` : "", (info?.commit || "").slice(0, 7)].filter(Boolean).join(" · ");
}

export function bannerText(info, appVersion) {
  const server = info?.version && info.version !== "dev" ? `서버 v${info.version}` : "서버 (이전 버전)";
  return `서버가 앱보다 오래됐어요 · ${server} / ${appVersion ? `앱 v${appVersion}` : "이 앱"}`;
}

// What the remote update script and the native side report, in words.
const UPDATE_ERRORS = {
  server_update_disabled: "이 앱은 업데이트 주소 없이 빌드돼 서버 묶음을 받을 수 없어요.",
  server_release_missing: "이 앱 버전에 맞는 서버 묶음이 릴리스에 없어요.",
  server_arch_unsupported: "서버의 CPU 종류(아키텍처)용 묶음이 없어요. Linux x86_64·arm64만 지원해요.",
  update_signature_invalid: "내려받은 서버 묶음의 서명이 맞지 않아 설치하지 않았어요.",
  update_feed_unreachable: "릴리스에서 서버 묶음을 받지 못했어요. 인터넷 연결을 확인하세요.",
  arkism_not_found: "서버에서 Arkism 설치 폴더를 찾지 못했어요.",
  needs_write: "설치 폴더에 쓸 권한이 없어요. 서버에서 이 SSH 사용자에게 폴더 쓰기 권한을 주거나, 비밀번호 없는 sudo(sudo -n)를 허용해 주세요.",
  needs_sudo_restart: "서비스를 다시 시작하려면 sudo가 필요해요. 이 SSH 사용자에게 비밀번호 없는 sudo(sudo -n)를 허용하거나, 서버에서 직접 업데이트해 주세요.",
  no_service: "Arkism 서버를 돌리는 systemd 서비스를 찾지 못했어요. 서버를 서비스로 띄운 뒤 다시 시도하거나, 직접 업데이트해 주세요.",
  bad_archive: "서버 묶음 안의 파일이 예상과 달라 설치하지 않았어요.",
  health_failed_rolled_back: "새 서버가 정상으로 뜨지 않아 예전 버전으로 되돌렸어요. DB 구조 변경이 필요하면 서버에서 memoryd --init-db를 먼저 실행하세요.",
  rollback_failed: "새 서버가 뜨지 않았고 되돌리기도 실패했어요. 서버의 설치 폴더 .backup-* 에서 직접 되돌려 주세요."
};

export function updateErrorText(code) {
  const key = String(code || "").split(/[ :]/)[0];
  return UPDATE_ERRORS[key] || `서버를 업데이트하지 못했어요 (${code}).`;
}

const STEP_TEXT = { download: "서버 묶음 내려받는 중", verify: "서명 확인 중", install: "서버에 설치하는 중 (백업 → 교체 → 재시작 → 확인)" };

// Why this connection cannot update its server from the app, or "" when the button can try.
// Write and sudo problems are only known once the SSH script checks them; it stops before any change.
function updateBlocked(state) {
  if (state.mode === "local") return "이 PC 내장 서버는 앱과 함께 업데이트돼요. 앱을 업데이트한 뒤 다시 연결하세요.";
  if (state.mode !== "remote" || !state.ssh) return "서버 주소로 연결하면 앱에서 서버를 업데이트할 수 없어요. 서버에서 직접 업데이트해 주세요.";
  return "";
}

// The banner under the area tabs and the server line in 설정 › 정보.
// deps: $, el, state, request, tauri, listen, notice, reconnect, ask, sshForm(), appVersion(), openInfo(), changed().
export function createServerBanner(deps) {
  let busy = false, progress = "", failure = "";

  async function check() {
    deps.state.serverInfo = readServerInfo(await deps.request("/healthz").catch(() => null));
    deps.state.serverLacks = missingFeatures(deps.state.serverInfo);
    draw();
  }

  // The banner stays one short line; the update itself lives in 설정 › 정보.
  function draw() {
    deps.changed?.();
    const box = deps.$("#server-banner");
    if (!box) return;
    const info = deps.state.serverInfo, app = deps.appVersion();
    box.hidden = !busy && !serverOlder(info, app);
    if (box.hidden) return;
    const b = deps.el("button", "btn", "업데이트 보기");
    b.type = "button";
    b.addEventListener("click", () => deps.openInfo?.());
    box.replaceChildren(deps.el("span", "server-banner-text", busy ? progress : bannerText(info, app)), b);
  }

  // 설정 › 정보: "서버 v0.1.5 (10월 26일 판 · abc1234) · 업데이트 있음" with its button or the reason it has none.
  function row() {
    const { el, state } = deps, info = state.serverInfo, app = deps.appVersion();
    const status = serverStatus(info, app);
    const where = state.mode === "local" ? "이 PC" : state.mode === "remote" && state.ssh ? state.ssh.host : "서버 주소";
    const r = el("div", "v4-version server-version");
    const left = el("div", "v4-version-text");
    const top = el("div", "v4-version-top");
    top.append(el("span", "", `서버 · ${where}`), el("span", `tag${status === "current" ? "" : " warn"}`, STATUS_TEXT[status]));
    const ver = el("div", "v4-version-num");
    ver.append(el("strong", "mono", info?.version ? `v${info.version}` : info ? "이전 버전" : "—"));
    const build = buildText(info);
    if (build) ver.append(el("small", "", build));
    left.append(top, ver);
    const blocked = updateBlocked(state);
    const note = busy ? progress : failure || (status !== "current" && status !== "unknown" ? blocked : "");
    if (note) left.append(el("small", "server-version-note", note));
    const acts = el("div", "v4-version-actions");
    if (deps.tauri && status !== "current" && status !== "unknown") {
      // A built-in server comes inside the app installer: restarting it brings the matching one.
      const b = el("button", "btn primary", blocked ? (state.mode === "local" ? "다시 연결" : "업데이트할 수 없음") : "서버 업데이트");
      b.type = "button";
      b.disabled = busy || (!!blocked && state.mode !== "local");
      b.addEventListener("click", () => void (blocked ? deps.reconnect(b) : update()));
      acts.append(b);
    }
    r.append(left, acts);
    return r;
  }

  async function update() {
    const app = deps.appVersion();
    if (busy || !app) return;
    if (!await deps.ask(`서버를 v${app}로 업데이트할까요?\n\n지금 서버 프로그램을 백업하고 바꾼 뒤 다시 시작해요. 새 서버가 정상으로 뜨지 않으면 자동으로 되돌려요.`, "업데이트")) return;
    busy = true; failure = ""; progress = STEP_TEXT.download;
    draw();
    const stop = await deps.listen?.("server-update-step", (e) => { progress = STEP_TEXT[e.payload] || String(e.payload); draw(); }).catch(() => null);
    try {
      const r = await deps.tauri.invoke("server_update", { form: deps.sshForm(), version: app });
      busy = false;
      deps.notice(`서버를 v${r.version}로 올렸어요.`);
      await deps.reconnect(); // reconnecting reads /healthz again (open → check)
    } catch (e) {
      busy = false;
      failure = updateErrorText(String(e?.message || e)); // nothing changed, or the backup is back
      draw();
      deps.notice(failure);
    } finally {
      stop?.();
    }
  }

  return { check, draw, row, update, lacks: (feature) => !!deps.state.serverLacks?.has(feature) };
}

// A control for a feature this server lacks: kept visible, turned off, with the reason on hover and for screen readers.
export function serverNeeded(control) {
  if (!control) return;
  control.disabled = true;
  control.title = "서버 업데이트가 필요해요 · 이 서버는 이 기능을 아직 몰라요.";
}
