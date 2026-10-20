// v4 · NOCTURNE shell: six menus on the left rail, each with its own row of area tabs on top (Figma 아키즘 v4, 198:2).
// A tab only points at an existing screen (view + sub-tab); the screens themselves keep their own data and actions.
// To add a tab, add a line here — app.js reads nothing else about the menu layout.
window.ArkismShell = (ctx) => {
  "use strict";
  const { state, $, el, go } = ctx;
  const count = (id) => () => $(`#count-${id}`)?.textContent || "";

  // [key, label, open(), isActive(), count?]
  const MENUS = {
    home: { label: "HOME", tabs: [
      ["home", "홈", () => go("todo"), () => state.view === "todo", count("todo")]
    ] },
    memory: { label: "MEMORY", tabs: [
      ["overview", "개요", () => go("overview"), () => state.view === "overview"],
      ["memories", "기억", () => go("memories", "active"), () => state.view === "memories", count("memories")],
      ["sessions", "세션", () => go("sessions"), () => state.view === "sessions", count("sessions")],
      ["usage", "사용량", () => go("usage"), () => state.view === "usage"]
    ] },
    agent: { label: "CREW", tabs: [
      ["workers", "전문가", () => { state.workerNew = false; go("workers"); }, () => state.view === "workers"],
      ["teams", "팀", () => go("teams"), () => ["teams", "teamNew", "meetings"].includes(state.view)],
      ["crew", "자동화", () => go("crew"), () => ["crew", "graph"].includes(state.view), count("crew")],
      // 디스코드 tab hidden: bots live in 설정 › 메신저 (Figma 1012:60790); the view code stays in team.js.
      ["crew-usage", "사용량", () => go("crewUsage"), () => state.view === "crewUsage"]
    ] },
    router: { label: "ROUTER", tabs: [
      ["routing", "라우팅 현황", () => go("routing"), () => state.view === "routing", count("routing")],
      ["routing-set", "라우팅 설정", () => settings("routing"), () => state.view === "settings" && state.settingsTab === "routing"],
      ["models", "모델 설정", () => settings("models"), () => state.view === "settings" && state.settingsTab === "models"],
      ["usage", "사용량", () => go("usage"), () => state.view === "usage", count("usage")]
    ] },
    connect: { label: "TOOLS", tabs: [
      ["skills", "스킬", () => go("skills"), () => state.view === "skills", count("skills")],
      ["plugin", "플러그인", () => { state.toolTab = "plugin"; go("tools"); }, () => state.view === "tools" && state.toolTab === "plugin"],
      ["mcp", "MCP", () => { state.toolTab = "mcp"; go("tools"); }, () => state.view === "tools" && state.toolTab !== "plugin"],
      ["knowledge", "지식", () => go("knowledge"), () => state.view === "knowledge"], // adopt-knowledge (knowledge.js)
      ["git", "저장소", () => go("git"), () => ["git", "reviews"].includes(state.view)],
      ["usage", "사용량", () => go("usage"), () => state.view === "usage"]
    ] },
    gallery: { label: "GALLERY", tabs: [
      ["design", "디자인 시스템", () => go("design"), () => ["design", "tokens"].includes(state.view), count("design")],
      ["image", "이미지", () => go("images", "image"), () => state.view === "images" && state.imageTab !== "slide", count("image")],
      ["slide", "슬라이드", () => go("images", "slide"), () => state.view === "images" && state.imageTab === "slide", count("slide")]
    ] },
    // 설정 tabs (Figma 07 · 설정): one area tab per settings page. 라우팅·모델 설정 live under ROUTER.
    settings: { label: "설정", tabs: [
      ["general", "일반"], ["connect", "연결"], ["messenger", "메신저"], ["roles", "역할별 모델"], ["appearance", "모양"], ["agents", "에이전트"],
      ["workflow", "페르소나"], ["categories", "카테고리"], ["advanced", "고급"]
    ].map(([k, label]) => [k, label, () => settings(k), () => state.view === "settings" && state.settingsTab === k]) }
  };
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
    // MEMORY draws no tabs: the overview's own links open 기억·세션·사용량, and the rail goes back to 개요.
    const tabs = key === "memory" ? [] : MENUS[key].tabs;
    box.replaceChildren(...tabs.map(([id, label, open, active, n]) => {
      const t = el("button", `area-tab${active() ? " active" : ""}`);
      t.type = "button";
      t.dataset.tab = id;
      t.setAttribute("role", "tab");
      t.setAttribute("aria-selected", String(active()));
      t.append(el("span", "", label));
      const num = n?.();
      if (num) t.append(el("output", "", num));
      t.addEventListener("click", () => { state.menu = key; open(); });
      return t;
    }));
  }

  // Left rail: a menu opens its first tab.
  document.querySelectorAll(".nav-item[data-menu]").forEach((b) => b.addEventListener("click", () => {
    const key = b.dataset.menu;
    state.menu = key;
    MENUS[key].tabs[0][2]();
  }));
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
