// Browser stand-in for the desktop shell, injected by serve.py: start on the built-in (local) Arkism = this preview server.
// shot.mjs installs its own richer stand-in first; then this does nothing.
if (!window.__TAURI__) {
  const origin = location.origin;
  try { if (!localStorage.getItem("arkism.look")) localStorage.setItem("arkism.look", JSON.stringify({ theme: "dark", lang: "ko" })); } catch {}
  window.__TAURI__ = { core: { invoke: async (cmd, a = {}) => {
    if (cmd === "app_defaults") return { local: true };
    if (cmd === "local_start") return { server: origin, token: "preview" };
    if (cmd === "ssh_disconnect") return null;
    if (cmd === "api") { const r = await fetch(a.base + a.path, { method: a.method, body: a.body == null ? undefined : (typeof a.body === "string" ? a.body : JSON.stringify(a.body)) }); return { status: r.status, body: await r.text() }; }
    throw new Error("preview: " + cmd);
  } } };
}
