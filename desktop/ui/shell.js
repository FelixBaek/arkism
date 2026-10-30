// v4 · NOCTURNE shell: six menus on the left rail, each with its own row of area tabs on top (Figma 아키즘 v4, 198:2).
// A tab only points at an existing screen (view + sub-tab); the screens themselves keep their own data and actions.
// To add a tab, add a line here — app.js reads nothing else about the menu layout.
window.ArkismShell = (ctx) => {
  "use strict";
  const { state, $, el, go } = ctx;
  // A zero count is left out (an empty 홈 is not 「홈 0」) unless the screen marks its count with data-show-zero.
  const count = (id) => () => { const c = $(`#count-${id}`), n = c?.textContent || ""; return n.trim() === "0" && !c.hasAttribute("data-show-zero") ? "" : n; };
  const check = () => ctx.memoryCheck;
  const noZero = (n) => () => (n() === "0" ? "" : n()); // 홈 · 기억: an empty count shows no number

  // [key, label, open(), isActive(), count?]
  const MENUS = {
    home: { label: "HOME", tabs: [
      ["home", "홈", () => go("todo"), () => state.view === "todo", noZero(count("todo"))]
    ] },
    // The list first: a new user lands on their memories, 확인해 주세요 holds every decision, 한눈에 is the brain map.
    memory: { label: "MEMORY", tabs: [
      ["memories", "기억", () => go("memories", "active"), () => state.view === "memories" && !check().isCheck(state.memTab), noZero(count("memories"))],
      ["check", "확인해 주세요", () => go("memories", check().firstCheck()), () => state.view === "memories" && check().isCheck(state.memTab), () => { const t = check().checkTotal(); return t.sum ? t.text : ""; }], // nothing known to wait: no 0 / 0+
      ["overview", "한눈에", () => go("overview"), () => state.view === "overview"]
    ] },
    agent: { label: "FLOW", tabs: [
      // One page, so no tab is drawn; usage sits in each workflow's 기록·사용량 tab. 디스코드 bots live in 설정 › 메신저.
      ["crew", "워크플로우", () => go("crew"), () => ["crew", "graph", "workers", "teams", "teamNew", "meetings"].includes(state.view), count("crew")]
    ] },
    router: { label: "ROUTER", tabs: [
      // 라우팅 설정 has no tab of its own (Figma 1303:79879): the policy card's 「라우팅 설정」 button opens it, and 라우팅 현황 stays lit.
      ["routing", "라우팅 현황", () => go("routing"), () => state.view === "routing" || (state.view === "settings" && state.settingsTab === "routing"), count("routing")],
      ["usage", "사용량", () => go("usage"), () => state.view === "usage", count("usage")]
    ] },
    connect: { label: "TOOLS", tabs: [
      ["skills", "스킬", () => go("skills"), () => state.view === "skills", count("skills")],
      ["plugin", "플러그인", () => { state.toolTab = "plugin"; go("tools"); }, () => state.view === "tools" && state.toolTab === "plugin", () => String(state.data.tools?.plugins.length ?? "")],
      ["mcp", "도구 서버(MCP)", () => { state.toolTab = "mcp"; go("tools"); }, () => state.view === "tools" && state.toolTab !== "plugin", () => String(state.data.tools?.servers.length ?? "")],
      ["knowledge", "지식", () => go("knowledge"), () => state.view === "knowledge"], // adopt-knowledge (knowledge.js)
      ["usage", "사용량", () => go("usage"), () => state.view === "usage"]
    ] },
    gallery: { label: "GALLERY", tabs: [
      ["design", "디자인 시스템", () => go("design"), () => ["design", "tokens"].includes(state.view), count("design")],
      ["image", "이미지", () => go("images", "image"), () => state.view === "images" && state.imageTab !== "slide", count("image")],
      ["slide", "슬라이드", () => go("images", "slide"), () => state.view === "images" && state.imageTab === "slide", count("slide")]
    ] },
    // 설정 tabs (Figma 07 · 설정): one area tab per settings page. 라우팅 설정 opens from ROUTER › 라우팅 현황.
    settings: { label: "설정", tabs: [
      ["general", "일반"], ["connect", "연결"], ["messenger", "메신저"], ["appearance", "모양"], ["agents", "에이전트"], ["roles", "역할별 모델"],
      ["workflow", "페르소나"], ["categories", "카테고리"], ["advanced", "고급"], ["info", "정보"]
    ].map(([k, label]) => [k, label, () => settings(k), () => state.view === "settings" && state.settingsTab === k]) }
  };
  for (const [key, menu] of Object.entries(MENUS)) {
    for (const [id, label] of menu.tabs) window.ArkismI18n.registerMessages({["navigation." + key + "." + id]: label});
  }
  function settings(tab) { state.settingsTab = tab; go("settings"); }

  // The menu a screen belongs to. 사용량 shows up under several menus, so the last menu picked wins when it has the screen.
  function menuOf() {
    const owners = Object.entries(MENUS).filter(([, m]) => m.tabs.some((t) => t[3]())).map(([k]) => k);
    if (owners.includes(state.menu)) return state.menu;
    return owners[0] || state.menu || "home";
  }

  function sync() {
    const key = menuOf();
    state.menu = key;
    document.querySelectorAll(".nav-item[data-menu]").forEach((b) => {
      const on = b.dataset.menu === key;
      b.classList.toggle("active", on);
      if (on) b.setAttribute("aria-current", "page"); else b.removeAttribute("aria-current");
    });
    const box = $("#area-tabs");
    // FLOW has one page, so no tab line
    const tabs = key === "agent" ? [] : MENUS[key].tabs;
    box.parentElement.hidden = key === "agent"; // FLOW (1424:2375) starts with its filters, no empty tab line
    box.dataset.active = tabs.find((t) => t[3]())?.[1] || ""; // app.js hides a page title that only repeats this tab
    // 현재 위치 (shown while the rail is folded): the menu, then the open tab when the menu has more than one
    const where = [el("span", "", $(`.nav-item[data-menu="${key}"] span`)?.textContent || MENUS[key].label)];
    const tab = MENUS[key].tabs.length > 1 && MENUS[key].tabs.find((t) => t[3]());
    if (tab) where.push(el("span", "sep", "›"), window.ArkismI18n.bindMessage(el("span"), "navigation." + key + "." + tab[0]));
    $("#location-bar").replaceChildren(...where);
    box.replaceChildren(...tabs.map(([id, , open, active, n]) => {
      const t = el("button", `area-tab${active() ? " active" : ""}`);
      t.type = "button";
      t.dataset.tab = id;
      t.setAttribute("role", "tab");
      t.setAttribute("aria-selected", String(active()));
      t.append(window.ArkismI18n.bindMessage(el("span"), "navigation." + key + "." + id));
      const num = active() && n?.(); // the count sits only on the open tab, never as a line under the tabs
      if (num) t.append(el("output", "", num));
      t.addEventListener("click", () => { state.menu = key; open(); });
      return t;
    }));
  }

  // Left rail: a menu opens its first tab.
  document.querySelectorAll(".nav-item[data-menu]").forEach((b) => b.addEventListener("click", () => {
    const key = b.dataset.menu;
    state.menu = key;
    if (key === "settings" && b.classList.contains("has-update")) { settings("info"); return; } // the dot means a new version: open where it installs
    MENUS[key].tabs[0][2]();
  }));
  // 메뉴 접기: the rail folds to icons (remembered on this PC). Narrow windows always get the folded rail, without the button.
  const FOLD = "arkism.rail.folded";
  const narrow = window.matchMedia?.("(max-width: 1000px)");
  const toggle = $("#rail-toggle");
  let folded = false;
  try { folded = localStorage.getItem(FOLD) === "1"; } catch (_) { /* storage off: start open */ }
  const fold = () => {
    const small = !!narrow?.matches;
    document.documentElement.classList.toggle("rail-collapsed", folded || small);
    toggle.hidden = small;
    const label = folded ? "메뉴 펼치기" : "메뉴 접기";
    toggle.setAttribute("aria-expanded", String(!folded));
    toggle.setAttribute("aria-label", label);
    toggle.title = label;
  };
  toggle.addEventListener("click", () => {
    folded = !folded;
    try { localStorage.setItem(FOLD, folded ? "1" : "0"); } catch (_) { /* this window only */ }
    fold();
  });
  narrow?.addEventListener?.("change", fold);
  fold();
  // The dot next to the logo is the server connection; clicking it shows the details.
  const card = $("#status-card");
  $("#status-toggle").addEventListener("click", (e) => {
    card.hidden = !card.hidden;
    e.currentTarget.setAttribute("aria-expanded", String(!card.hidden));
  });
  document.addEventListener("click", (e) => {
    if (!card.hidden && !e.target.closest("#status-card, #status-toggle")) { card.hidden = true; $("#status-toggle").setAttribute("aria-expanded", "false"); }
  });

  return { MENUS, menuOf, sync };
};
