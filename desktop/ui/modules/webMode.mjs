// @ts-check
// Web mode: memoryd serves these screens under /app/ to a plain browser on the private network.
// The API is the same origin; the access token lives in sessionStorage only (gone when the tab closes)
// and is sent as a Bearer header, so there is no cookie and no CSRF surface.
export const WEB_PATH = "/app/";
export const WEB_TOKEN_KEY = "arkism.webToken";

/** @param {{ pathname?: string } | undefined} loc @param {unknown} tauri */
export const isWebMode = (loc, tauri) => !tauri && String(loc?.pathname || "").startsWith(WEB_PATH);

/** Trimmed token, or "" when it holds whitespace or control characters (a bad paste, never a header). */
export function cleanToken(value) {
  const token = String(value ?? "").trim();
  return token && token.length <= 4096 && !/[\s\x00-\x1f\x7f]/.test(token) ? token : "";
}

/** @param {Storage | undefined} storage */
export function readWebToken(storage) {
  try { return cleanToken(storage?.getItem(WEB_TOKEN_KEY)); } catch { return ""; }
}

/** Saves a token (or removes it on ""). Storage that refuses writes asks again next time. @param {Storage | undefined} storage */
export function saveWebToken(storage, token) {
  try { token ? storage?.setItem(WEB_TOKEN_KEY, token) : storage?.removeItem(WEB_TOKEN_KEY); } catch { /* private window: not kept */ }
}
