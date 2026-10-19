// Lucian UX/UI Prompt #204 · MIT License (c) 2026 Seokwoooo · https://lucianlabs.dev/uxui-prompt/#204
// 기억 지도 canvas: one physics bubble per memory, regrouped by 전체 / 칸별 / 종류별, with relation lines drawn under the bubbles.
// Ported from the reference (same step() maths, rAF loop, tab pill, hover/tooltip, reduced-motion settle); the data mapping is Arkism's.
window.ArkismMemoryBubbles = (() => {
  "use strict";
  // Line styles = the legend bar (v4-overview.css .ov-map-e-*): 지지 solid, 관련 dotted, 충돌 red dashed, 대체 purple dotted.
  const EDGE = { supports: { c: "#6fbf8f", dash: [] }, same_session: { c: "#9a9a94", dash: [1, 5] }, conflicts: { c: "#ff5160", dash: [5, 4] }, supersedes: { c: "#8b7cf6", dash: [1, 4] } };
  const PAD = { l: 18, r: 18, t: 84, b: 76 }; // top clears the tabs + group labels, bottom clears the legend bar
  const SPRING = { len: 60, k: 0.5 };          // weak pull along a relation (same group only, attract only)

  function create(opts) {
    const { tabs, groupOf, tip, onSelect, onClear } = opts;
    const doc = document, mk = (tag, cls, txt) => { const n = doc.createElement(tag); if (cls) n.className = cls; if (txt != null) n.textContent = txt; return n; };
    const root = mk("div", "mm-bubbles"), canvas = mk("canvas", "mm-canvas"), ctx = canvas.getContext("2d");
    const tabBar = mk("div", "mm-tabs"), pill = mk("span", "mm-pill"), live = mk("div", "mm-live"), tipEl = mk("div", "mm-tip"), labels = mk("div", "mm-labels");
    canvas.tabIndex = 0; canvas.setAttribute("role", "application"); canvas.setAttribute("aria-label", "기억 버블 지도");
    live.setAttribute("aria-live", "polite"); tipEl.setAttribute("aria-hidden", "true");
    const tipT = mk("span", "mm-tip-t"), tipM = mk("span", "mm-tip-m"); tipT.setAttribute("data-no-i18n", ""); tipEl.append(tipT, tipM);
    live.setAttribute("data-no-i18n", "");
    tabBar.append(pill); root.append(canvas, labels, tabBar, tipEl, live);

    const reducedMq = window.matchMedia ? matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };
    let W = 0, H = 0, dpr = 1, items = [], byId = new Map(), links = [], edgesAll = [], groups = [], mode = tabs[0][0], idsKey = "";
    let sel = null, kb = null, hover = null, showAll = true, zoom = 1, scale = 1, raf = 0, last = 0, calm = 0, visible = true, energy = 99;
    const btns = new Map();
    for (const [k, l] of tabs) { const b = mk("button", "mm-tab", l); b.type = "button"; b.addEventListener("click", () => setGroup(k)); btns.set(k, b); tabBar.append(b); }

    const tok = (n, d) => getComputedStyle(root).getPropertyValue(n).trim() || d;
    function placePill() { const b = btns.get(mode); if (!b || !b.offsetWidth) return; pill.style.left = `${b.offsetLeft}px`; pill.style.width = `${b.offsetWidth}px`; for (const [k, x] of btns) x.classList.toggle("on", k === mode); }
    const seed = (s) => { let h = 2166136261; for (const ch of s) h = Math.imul(h ^ ch.charCodeAt(0), 16777619); return ((h >>> 0) % 1000) / 1000; };

    function regroup() {
      const m = new Map();
      for (const p of items) { const g = groupOf(mode, p); let o = m.get(g.key); if (!o) m.set(g.key, o = { key: g.key, name: g.name, ord: g.ord ?? 0, count: 0, el: null, cx: 0, cy: 0, top: null }); o.count++; p.g = o; }
      groups = [...m.values()].sort((a, b) => a.ord - b.ord || b.count - a.count);
      labels.replaceChildren(...groups.map((g) => { g.el = mk("div", "mm-label"); g.el.append(mk("b", "", g.name), doc.createTextNode(` · ${g.count}개`)); return g.el; }));
      layout();
    }
    function layout() {
      if (!W) return;
      const n = groups.length || 1, aw = W - PAD.l - PAD.r, ah = H - PAD.t - PAD.b;
      const cols = n <= 3 ? n : Math.ceil(Math.sqrt(n * aw / ah)), rows = Math.ceil(n / cols);
      groups.forEach((g, i) => { const r = Math.floor(i / cols), inRow = r === rows - 1 ? n - r * cols : cols, c = i % cols + (cols - inRow) / 2; g.cx = PAD.l + (c + 0.5) / cols * aw; g.cy = PAD.t + (r + 0.5) / rows * ah; });
      // bubble size follows the canvas; shrink when many memories would not fit the free area (area share ~ 40%)
      const base = Math.max(0.8, Math.min(1.6, Math.min(W, H) / 520));
      const need = items.reduce((a, p) => a + Math.PI * (4.5 + Math.sqrt(p.v) * 5) ** 2, 0) * base * base;
      scale = base * Math.min(1, Math.sqrt(aw * ah * 0.4 / Math.max(1, need)));
      for (const p of items) p.r = (4.5 + Math.sqrt(p.v) * 5) * scale;
      items.sort((a, b) => b.r - a.r);
    }
    function place(p) { p.placed = true; p.x = (p.g?.cx ?? W / 2) + (seed(p.id) - 0.5) * 40; p.y = (p.g?.cy ?? H / 2) + (seed(p.id + "y") - 0.5) * 40; }

    function setItems(list) {
      const key = list.map((x) => `${x.id}:${x.v}:${x.reach}:${x.kind}`).join("|");
      const same = key === idsKey; idsKey = key;
      if (same) { draw(); return; }
      items = list.map((x) => { const o = byId.get(x.id); return o ? Object.assign(o, x) : { ...x, x: 0, y: 0, vx: 0, vy: 0, r: 8, g: null, placed: false }; });
      byId = new Map(items.map((p) => [p.id, p]));
      regroup(); if (W) for (const p of items) if (!p.placed) place(p);
      linkUp(); run();
    }
    function setEdges(list) { edgesAll = list; linkUp(); draw(); }
    function linkUp() { links = edgesAll.filter((e) => byId.has(e.from) && byId.has(e.to)).map((e) => ({ a: byId.get(e.from), b: byId.get(e.to), t: e.t })); }
    function setGroup(k) { if (k === mode) return; mode = k; placePill(); regroup(); kick(30); run(); }
    function kick(a) { for (const p of items) { p.vx += (Math.random() - 0.5) * 2 * a; p.vy += (Math.random() - 0.5) * 2 * a; } }
    function setSelected(id) { const n = id && byId.has(id) ? id : null; if (n && n !== sel) announce(n); const moved = n !== sel; sel = n; if (moved) run(); draw(); }
    function setEdgeAll(v) { showAll = !!v; draw(); }
    function setZoom(z) { zoom = z; draw(); }
    function announce(id) { const t = tip(id); if (t) live.textContent = `${t.text} ${t.meta}`; }

    function step(dt) {
      const damp = Math.exp(-5 * dt);
      for (const p of items) { p.vx += (p.g.cx - p.x) * 5 * dt; p.vy += (p.g.cy - p.y) * 5 * dt; }
      for (const l of links) {
        if (l.a.g !== l.b.g) continue; const dx = l.b.x - l.a.x, dy = l.b.y - l.a.y, d = Math.hypot(dx, dy) || 1;
        // the selected memory holds its neighbours a little apart so the relation lines between them have room to show
        const open = sel && (l.a.id === sel || l.b.id === sel), len = open ? (l.a.r + l.b.r) * 2.2 : SPRING.len;
        if (!open && d <= len) continue; const f = (d - len) * (open ? 3 : SPRING.k) * dt, fx = dx / d * f, fy = dy / d * f; l.a.vx += fx; l.a.vy += fy; l.b.vx -= fx; l.b.vy -= fy;
      }
      let e = 0;
      for (const p of items) { p.vx *= damp; p.vy *= damp; p.x += p.vx * dt * 1.6; p.y += p.vy * dt * 1.6; e += p.vx * p.vx + p.vy * p.vy; }
      energy = items.length ? e / items.length : 0;
      for (let it = 0; it < 3; it++) for (let i = 0; i < items.length; i++) for (let j = i + 1; j < items.length; j++) {
        const a = items[i], b = items[j]; let dx = b.x - a.x, dy = b.y - a.y, d = Math.hypot(dx, dy); const min = a.r + b.r + 1.5; if (d >= min) continue;
        if (!d) { dx = 1; dy = 0; d = 1; } const push = (min - d) / 2, nx = dx / d, ny = dy / d; a.x -= nx * push; a.y -= ny * push; b.x += nx * push; b.y += ny * push;
      }
      for (const p of items) { p.x = Math.max(PAD.l + p.r, Math.min(W - PAD.r - p.r, p.x)); p.y = Math.max(PAD.t + p.r, Math.min(H - PAD.b - p.r, p.y)); }
    }
    function frame(t) {
      raf = 0; if (!visible || !root.isConnected) return;
      const dt = Math.min(0.033, (t - last) / 1000 || 0.016); last = t; step(dt); draw();
      if (energy < 8) { if (++calm >= 30) return; } else calm = 0;
      raf = requestAnimationFrame(frame);
    }
    function run() {
      calm = 0;
      if (reducedMq.matches) { for (let i = 0; i < 400; i++) step(1 / 60); draw(); return; }
      if (!raf && visible && W) { last = performance.now(); raf = requestAnimationFrame(frame); }
    }

    // blend two colours (hex / rgb() / CSS names via a 1px canvas); cached per pair
    const mixCache = new Map(), probe = doc.createElement("canvas").getContext("2d");
    const rgb = (c) => { probe.fillStyle = "#000"; probe.fillStyle = c; const h = probe.fillStyle; if (h[0] === "#") return [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16)); return h.match(/[\d.]+/g).slice(0, 3).map(Number); };
    function mix(a, b, t) { const k = `${a}|${b}|${t}`; let v = mixCache.get(k); if (!v) { const x = rgb(a), y = rgb(b); v = `rgb(${x.map((n, i) => Math.round(n + (y[i] - n) * t)).join(",")})`; mixCache.set(k, v); } return v; }
    const scr = (x, y) => [W / 2 + (x - W / 2) * zoom, H / 2 + (y - H / 2) * zoom];
    function draw() {
      if (!W) return;
      const text = tok("--nc-text", "#eee");
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, W, H);
      ctx.translate(W / 2, H / 2); ctx.scale(zoom, zoom); ctx.translate(-W / 2, -H / 2);
      const foci = new Set([hover, kb, sel].filter(Boolean)), near = new Set(foci);
      for (const l of links) if (foci.has(l.a.id) || foci.has(l.b.id)) { near.add(l.a.id); near.add(l.b.id); }
      ctx.lineCap = "round";
      const hot = [];
      for (const l of links) { // quiet lines sit under the bubbles; lines of the focused memory are drawn on top (below)
        const on = foci.has(l.a.id) || foci.has(l.b.id); if (on) { hot.push(l); continue; } if (!showAll) continue;
        const s = EDGE[l.t] || EDGE.supports; ctx.globalAlpha = foci.size ? 0.05 : 0.3; ctx.strokeStyle = s.c; ctx.lineWidth = 1.2; ctx.setLineDash(s.dash);
        ctx.beginPath(); ctx.moveTo(l.a.x, l.a.y); ctx.lineTo(l.b.x, l.b.y); ctx.stroke();
      }
      ctx.setLineDash([]);
      for (const g of groups) g.top = null;
      for (const p of items) {
        const dim = foci.size && !near.has(p.id);
        ctx.globalAlpha = dim ? 0.22 : 1;
        // glossy sphere: lit top-left, darker rim, soft drop shadow
        const gr = ctx.createRadialGradient(p.x - p.r * 0.35, p.y - p.r * 0.4, p.r * 0.1, p.x, p.y, p.r);
        gr.addColorStop(0, mix(p.color, "#ffffff", 0.45)); gr.addColorStop(0.55, p.color); gr.addColorStop(1, mix(p.color, "#000000", 0.35));
        ctx.shadowColor = "rgba(0,0,0,.35)"; ctx.shadowBlur = dim ? 0 : p.r * 0.6; ctx.shadowOffsetY = p.r * 0.15;
        ctx.fillStyle = gr; ctx.beginPath(); ctx.arc(p.x, p.y, p.r, 0, 7); ctx.fill();
        ctx.shadowColor = "transparent"; ctx.shadowBlur = 0; ctx.shadowOffsetY = 0;
        ctx.strokeStyle = mix(p.color, "#ffffff", 0.3); ctx.lineWidth = 1; ctx.globalAlpha *= 0.6; ctx.beginPath(); ctx.arc(p.x, p.y, p.r - 0.5, 0, 7); ctx.stroke();
        ctx.globalAlpha = dim ? 0.22 : 0.55; ctx.fillStyle = "#fff"; ctx.beginPath(); ctx.ellipse(p.x - p.r * 0.32, p.y - p.r * 0.42, p.r * 0.28, p.r * 0.16, -0.6, 0, 7); ctx.fill();
        if (!p.g.top || p.y - p.r < p.g.top.y - p.g.top.r) p.g.top = p;
      }
      const bg = tok("--nc-canvas", "#0b0b0b");
      for (const l of hot) { // focused relations: on top, centre to centre as an arc bowed away from the cluster, so they read even when the bubbles touch
        const s = EDGE[l.t] || EDGE.supports, dx = l.b.x - l.a.x, dy = l.b.y - l.a.y, d = Math.hypot(dx, dy) || 1;
        const mx = (l.a.x + l.b.x) / 2, my = (l.a.y + l.b.y) / 2, gx = (l.a.g.cx + l.b.g.cx) / 2, gy = (l.a.g.cy + l.b.g.cy) / 2;
        let nx = -dy / d, ny = dx / d; if ((mx - gx) * nx + (my - gy) * ny < 0) { nx = -nx; ny = -ny; }
        const bow = Math.max(28, d * 0.35), cx = mx + nx * bow, cy = my + ny * bow;
        ctx.globalAlpha = 1; ctx.setLineDash([]); ctx.strokeStyle = bg; ctx.lineWidth = 6; // halo keeps the line apart from the bubbles under it
        ctx.beginPath(); ctx.moveTo(l.a.x, l.a.y); ctx.quadraticCurveTo(cx, cy, l.b.x, l.b.y); ctx.stroke();
        ctx.strokeStyle = s.c; ctx.lineWidth = 2.5; ctx.setLineDash(s.dash.map((v) => v * 2)); ctx.shadowColor = s.c; ctx.shadowBlur = 6;
        ctx.beginPath(); ctx.moveTo(l.a.x, l.a.y); ctx.quadraticCurveTo(cx, cy, l.b.x, l.b.y); ctx.stroke();
        ctx.setLineDash([]); ctx.shadowBlur = 0; ctx.shadowColor = "transparent";
        for (const q of [l.a, l.b]) { ctx.fillStyle = bg; ctx.beginPath(); ctx.arc(q.x, q.y, 5, 0, 7); ctx.fill(); ctx.fillStyle = s.c; ctx.beginPath(); ctx.arc(q.x, q.y, 3.5, 0, 7); ctx.fill(); }
        const o = foci.has(l.a.id) ? l.b : l.a; // ring the memory on the other end in the relation colour
        ctx.strokeStyle = s.c; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(o.x, o.y, o.r + 2, 0, 7); ctx.stroke();
      }
      for (const p of items) if (p.id === hover || p.id === sel || p.id === kb) { ctx.globalAlpha = 1; ctx.strokeStyle = text; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(p.x, p.y, p.r + (p.id === hover ? 1 : 4), 0, 7); ctx.stroke(); }
      ctx.globalAlpha = 1;
      for (const g of groups) { if (!g.top) continue; const [x, y] = scr(g.top.x, g.top.y - g.top.r); g.el.style.transform = `translate(${x}px,${y - 8 * zoom}px) translate(-50%,-100%)`; }
      const id = hover || kb, p = id && byId.get(id), t = p && tip(id);
      if (p && t) { const [sx, y] = scr(p.x, p.y - p.r); tipT.textContent = t.text; tipM.textContent = t.meta; const hw = tipEl.offsetWidth / 2 + 6, x = Math.max(hw, Math.min(W - hw, sx)); tipEl.style.transform = `translate(${x}px,${y - 8}px) translate(-50%,-100%)`; tipEl.classList.add("on"); } else tipEl.classList.remove("on");
    }

    function hit(ev) {
      const r = canvas.getBoundingClientRect(), x = (ev.clientX - r.left - W / 2) / zoom + W / 2, y = (ev.clientY - r.top - H / 2) / zoom + H / 2;
      let best = null, bd = 1e9;
      for (const p of items) { const d = Math.hypot(p.x - x, p.y - y) - p.r; if (d <= 2 && d < bd) { bd = d; best = p.id; } }
      return best;
    }
    canvas.addEventListener("pointermove", (ev) => { const id = hit(ev); if (id !== hover) { hover = id; if (id) announce(id); canvas.style.cursor = id ? "pointer" : ""; draw(); } });
    canvas.addEventListener("pointerleave", () => { if (hover) { hover = null; draw(); } });
    canvas.addEventListener("click", (ev) => { const id = hit(ev); if (id) { kb = null; onSelect(id); } });
    canvas.addEventListener("blur", () => { if (kb) { kb = null; draw(); } });
    canvas.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") { kb = null; draw(); onClear(); return; }
      if (ev.key === "Enter") { const id = kb || sel; if (id) { ev.preventDefault(); onSelect(id); } return; }
      const dir = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowDown: [0, 1], ArrowUp: [0, -1] }[ev.key]; if (!dir || !items.length) return;
      ev.preventDefault(); const cur = byId.get(kb || sel); let pick = null;
      if (!cur) pick = items[0]; else { let bs = 1e9; for (const p of items) { if (p === cur) continue; const dx = p.x - cur.x, dy = p.y - cur.y, along = dx * dir[0] + dy * dir[1], perp = Math.abs(dx * dir[1] - dy * dir[0]); if (along <= 0) continue; const s = along + perp * 2; if (s < bs) { bs = s; pick = p; } } }
      if (pick) { kb = pick.id; announce(pick.id); draw(); }
    });

    function resize() {
      const r = root.getBoundingClientRect(); if (!r.width || !r.height) return;
      W = r.width; H = r.height; dpr = Math.min(2, window.devicePixelRatio || 1);
      canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr); canvas.style.width = `${W}px`; canvas.style.height = `${H}px`;
      layout(); for (const p of items) if (!p.placed) place(p);
      placePill(); run(); draw();
    }
    if (window.ResizeObserver) new ResizeObserver(resize).observe(root);
    if (window.IntersectionObserver) new IntersectionObserver((es) => { visible = es[es.length - 1].isIntersecting; if (visible) run(); }).observe(root);
    if (reducedMq.addEventListener) reducedMq.addEventListener("change", () => run());

    return { el: root, setItems, setEdges, setSelected, setEdgeAll, setZoom, setGroup };
  }
  return { create };
})();
