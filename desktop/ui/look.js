// @ts-check
// 설정 › 모양: 화면 언어·테마·글자 크기·화면 배율. 이 PC에만 저장하고, 앱이 뜨자마자 적용한다.
// 테마 색은 tokens.css의 :root[data-theme] 블록이 정한다. "시스템"은 속성을 지워 Windows 설정을 따른다.
window.ArkismLook = (() => {
  "use strict";
  const KEY = "arkism.look";
  const THEMES = ["system", "dark", "light"];
  // 화면 언어 [code, 그 언어로 쓴 이름]: ko is the source; every other code has its dictionary in i18n-<code>.js (i18n.js loads it).
  const LANGUAGES = [["ko", "한국어"], ["en", "English"], ["ja", "日本語"], ["zh-CN", "简体中文"], ["zh-TW", "繁體中文"], ["es", "Español"],
    ["de", "Deutsch"], ["fr", "Français"], ["pt", "Português"], ["vi", "Tiếng Việt"], ["id", "Bahasa Indonesia"]];
  const LANGS = LANGUAGES.map(([code]) => code);
  // 저장한 언어가 없으면 시스템 언어(선호 순서대로)에서 지원하는 첫 언어를 따르고, 없으면 한국어.
  function systemLang() {
    for (const raw of [...(navigator.languages || []), navigator.language]) {
      const tag = String(raw || "").toLowerCase(), base = tag.split("-")[0];
      if (base === "zh") return /^zh-(tw|hk|mo|hant)/.test(tag) ? "zh-TW" : "zh-CN";
      if (LANGS.includes(base)) return base;
    }
    return "ko";
  }
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
  (window.ArkismPopoutSetup ||= {}).zoomKeys = (d) => d.addEventListener("keydown", zoomKeys); // a popout window (popup-drag.js) dresses as this one, so only the keys
  apply();
  return { get: () => ({ ...look, lang: look.lang || systemLang() }), set, ZOOM_STEP, languages: LANGUAGES };
})();
