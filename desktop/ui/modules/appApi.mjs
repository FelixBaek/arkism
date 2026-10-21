// Owns transport and API errors. Dependencies are explicit live ports, never a global scope.
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
