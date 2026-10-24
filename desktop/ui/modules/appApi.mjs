// Owns transport and API errors. Dependencies are explicit live ports, never a global scope.

// A failed load in words, never the raw code: the server was not reached, or it answered
// with an error or a shape this app cannot read (HTTP errors carry status; *_invalid is a bad shape).
export function loadFailure(error) {
  const code = String(error?.code || error?.message || ""), status = Number(error?.status) || 0;
  if (code.endsWith("_invalid")) return { code, reached: true, title: "서버 답을 읽지 못했어요", text: "서버가 이 앱이 읽을 수 없는 형식으로 답했어요. 앱과 서버의 Arkism 버전을 맞춰 주세요." };
  if (status === 401 || status === 403 || code === "access_denied") return { code, reached: true, title: "서버가 접속을 거절했어요", text: "접속 토큰이나 이 계정의 권한을 확인해 주세요." };
  if (status >= 500) return { code, reached: true, title: "서버에 문제가 생겼어요", text: "서버가 요청을 처리하지 못했어요. 잠시 뒤 다시 시도해 주세요." };
  if (status) return { code, reached: true, title: "서버가 요청을 받지 않았어요", text: "앱과 서버의 Arkism 버전이 맞지 않을 수 있어요. 둘 다 업데이트해 주세요." };
  return { code, reached: false, title: "서버 연결이 끊겼어요", text: "" };
}

export function createAppApi(deps) {

  const query = (extra = {}) => new URLSearchParams(Object.fromEntries(Object.entries({ ...deps.state.scope, ...extra }).filter(([, v]) => v !== ""))).toString();

  // In the desktop app the Rust side makes the call (no CORS, token stays local);
  // a plain browser (development) talks to the server directly.
  /** @returns {Promise<object>} Failed requests retain HTTP status and machine code. */
  const identity = () => JSON.stringify([deps.state.server, deps.state.token, deps.state.scope, deps.state.connection?.generation]);
  async function request(path, options = {}) {
    const session = identity();
    let status, text;
    if (deps.tauri) {
      const r = await deps.tauri.invoke("api", { base: deps.trimSlash(deps.state.server), token: deps.state.token, method: options.method || "GET", path, body: options.body ?? null })
        .catch((e) => { throw new Error(String(e)); });
      status = r.status; text = r.body;
    } else {
      const res = await fetch(deps.trimSlash(deps.state.server) + path, { ...options, headers: { Authorization: `Bearer ${deps.state.token}`, "Content-Type": "application/json" } });
      status = res.status; text = await res.text();
    }
    if (session !== identity()) {
      const error = new Error("api_context_changed");
      error.status = 0; error.code = "api_context_changed";
      throw error;
    }
    const body = deps.readJSON(text) || {};
    if (status < 200 || status >= 300) {
      const error = new Error(body.code || `HTTP ${status}`);
      error.status = status; error.code = body.code || "http_error";
      throw error;
    }
    return body;
  }

  return {
    get query() { return query; },
    get request() { return request; }
  };
}
