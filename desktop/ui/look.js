// @ts-check
// 설정 › 모양: 화면 언어·테마·글자 크기·화면 배율. 이 PC에만 저장하고, 앱이 뜨자마자 적용한다.
// 테마 색은 tokens.css의 :root[data-theme] 블록이 정한다. "시스템"은 속성을 지워 Windows 설정을 따른다.
window.ArkismLook = (() => {
  "use strict";
  const KEY = "arkism.look";
  const THEMES = ["system", "dark", "light"];
  const LANGS = ["ko", "en"];
  // 저장한 언어가 없으면 시스템 언어를 따른다: ko로 시작하면 한국어, 아니면 영어.
  const systemLang = () => (String(navigator.language || "ko").toLowerCase().startsWith("ko") ? "ko" : "en");
  const FONTS = { sm: 1, md: 1.1, lg: 1.2, xl: 1.32 }; // md was 1: 13px body read too small (10-21)
  const ZOOM_MIN = 0.7, ZOOM_MAX = 1.6, ZOOM_STEP = 0.1;
  const clampZoom = (z) => Math.round(Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z)) * 100) / 100;
  function read() {
    let saved = null;
    try { saved = JSON.parse(localStorage.getItem(KEY) || "null"); } catch (_) { /* storage off: defaults */ }
    const zoom = Number(saved?.zoom);
    return {
      theme: THEMES.includes(saved?.theme) ? saved.theme : "system",
      font: Object.hasOwn(FONTS, saved?.font ?? "") ? saved.font : "md",
      zoom: Number.isFinite(zoom) && zoom > 0 ? clampZoom(zoom) : 1,
      lang: LANGS.includes(saved?.lang) ? saved.lang : undefined
    };
  }
  let look = read();
  // This page and every popout window (popup-drag.js) open now. CSSOM only: the CSP blocks a style attribute or element.
  function apply() {
    for (const d of [document, ...window.ArkismPopout?.docs() || []]) {
      const root = d.documentElement;
      if (look.theme === "system") root.removeAttribute("data-theme");
      else root.setAttribute("data-theme", look.theme);
      root.style.setProperty("--fs", String(FONTS[look.font]));
      root.style.zoom = String(look.zoom);
    }
  }
  function set(patch) {
    look = { ...look, ...patch };
    look.zoom = clampZoom(look.zoom);
    try { localStorage.setItem(KEY, JSON.stringify(look)); } catch (_) { /* storage off: this session only */ }
    apply();
    window.dispatchEvent(new CustomEvent("arkism-look"));
  }
  // Ctrl + / Ctrl − / Ctrl 0: 어느 화면에서든 배율을 바꾼다.
  const zoomKeys = (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
    if (e.key === "=" || e.key === "+") set({ zoom: look.zoom + ZOOM_STEP });
    else if (e.key === "-") set({ zoom: look.zoom - ZOOM_STEP });
    else if (e.key === "0") set({ zoom: 1 });
    else return;
    e.preventDefault();
  };
  document.addEventListener("keydown", zoomKeys);
  window.addEventListener("arkism-popout", (e) => /** @type {CustomEvent<Window>} */ (e).detail.addEventListener("keydown", zoomKeys)); // a popout window dresses as this one, so only the keys
  apply();
  return { get: () => ({ ...look, lang: look.lang || systemLang() }), set, ZOOM_STEP };
})();
