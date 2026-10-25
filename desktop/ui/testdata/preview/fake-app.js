// Browser stand-in for the desktop shell, injected by serve.py: start on the built-in (local) Arkism = this preview server.
// shot.mjs installs its own richer stand-in first; then this does nothing.
if (!window.__TAURI__) {
  const origin = location.origin;
  try { if (!localStorage.getItem("arkism.look")) localStorage.setItem("arkism.look", JSON.stringify({ theme: "dark", lang: "ko" })); } catch { /* storage blocked: keep the default look */ }
  const home = "C:\\Users\\you\\AppData\\Local\\dev.arkism.desktop\\runtime";
  window.__arkismLocation = { path: home, next: home, default: home, default_has_data: false };
  window.__TAURI__ = { core: { invoke: async (cmd, a = {}) => {
    if (cmd === "app_defaults") return { local: true };
    if (cmd === "local_start") { window.__arkismStarted = true; return { server: origin, token: "preview" }; }
    if (cmd === "ssh_disconnect") return null;
    // 저장 위치: the preview keeps the choice in memory; a relative path fails like the app's check.
    if (cmd === "local_location") return { ...window.__arkismLocation };
    if (cmd === "local_location_set") {
      const l = window.__arkismLocation;
      if (a.path != null && !/^([A-Za-z]:\\|\/)/.test(a.path)) throw "local_location_absolute";
      l.next = a.path || l.default;
      if (!window.__arkismStarted) l.path = l.next;
      return { ...l };
    }
    if (cmd === "api") { const r = await fetch(a.base + a.path, { method: a.method, body: a.body == null ? undefined : (typeof a.body === "string" ? a.body : JSON.stringify(a.body)) }); return { status: r.status, body: await r.text() }; }
    throw new Error("preview: " + cmd);
  } } };
}
