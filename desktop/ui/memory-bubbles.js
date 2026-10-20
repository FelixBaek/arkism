// Lucian UX/UI Prompt #204 · MIT License (c) 2026 Seokwoooo · https://lucianlabs.dev/uxui-prompt/#204
// 기억 지도 canvas: one physics bubble per memory, regrouped by 전체 / 칸별 / 종류별, with relation lines drawn under the bubbles.
// Ported from the reference (same step() maths, rAF loop, tab pill, hover/tooltip, reduced-motion settle); the data mapping is Arkism's.
window.ArkismMemoryBubbles = (() => {
  "use strict";
  // Line styles = the legend bar (v4-overview.css .ov-map-e-*): 지지 solid, 관련 dotted, 충돌 red dashed, 대체 purple dotted.
  const EDGE = { supports: { c: "#7fc7a0", dash: [], n: "지지" }, same_session: { c: "#c8c8c2", dash: [1, 3.5], n: "관련" }, conflicts: { c: "#ef5a64", dash: [6, 5], n: "충돌" }, supersedes: { c: "#a596f7", dash: [1, 3.5], n: "대체" } };
  const RIM = 16, MANY = 150; // area rim past its outer dots · above MANY dots the overview shows topic-to-topic lines, not every line
  const PAD = { l: 18, r: 18, t: 84, b: 76 }; // top clears the tabs + group labels, bottom clears the legend bar
  const SPRING = { len: 60, k: 0.5 };          // weak pull along a relation (same group only, attract only)

  function create(opts) {
    const { tabs, groupOf, tip, card, onSelect, onClear } = opts;
    const doc = document, mk = (tag, cls, txt) => { const n = doc.createElement(tag); if (cls) n.className = cls; if (txt != null) n.textContent = txt; return n; };
    const root = mk("div", "mm-bubbles"), canvas = mk("canvas", "mm-canvas"), ctx = canvas.getContext("2d");
    const cardsEl = mk("div", "mm-cards"), tabBar = mk("div", "mm-tabs"), pill = mk("span", "mm-pill"), live = mk("div", "mm-live"), tipEl = mk("div", "mm-tip"), labels = mk("div", "mm-labels");
    canvas.tabIndex = 0; canvas.setAttribute("role", "application"); canvas.setAttribute("aria-label", "기억 버블 지도");
    live.setAttribute("aria-live", "polite"); tipEl.setAttribute("aria-hidden", "true");
    const tipT = mk("span", "mm-tip-t"), tipM = mk("span", "mm-tip-m"); tipT.setAttribute("data-no-i18n", ""); tipEl.append(tipT, tipM);
    live.setAttribute("data-no-i18n", "");
    tabBar.append(pill); cardsEl.setAttribute("aria-hidden", "true"); root.append(canvas, cardsEl, labels, tabBar, tipEl, live);

    const reducedMq = window.matchMedia ? matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };
    let cards = [], cardKey = "", W = 0, H = 0, dpr = 1, items = [], byId = new Map(), links = [], edgesAll = [], groups = [], mode = tabs[0][0], idsKey = "";
    let sel = null, kb = null, hover = null, showAll = true, zoom = 1, scale = 1, raf = 0, last = 0, calm = 0, visible = true, energy = 99, dead = false;
    const btns = new Map();
    for (const [k, l] of tabs) { const b = mk("button", "mm-tab", l); b.type = "button"; b.addEventListener("click", () => setGroup(k)); btns.set(k, b); tabBar.append(b); }

    const tok = (n, d) => getComputedStyle(root).getPropertyValue(n).trim() || d;
    function placePill() { const b = btns.get(mode); if (!b || !b.offsetWidth) return; pill.style.left = `${b.offsetLeft}px`; pill.style.width = `${b.offsetWidth}px`; for (const [k, x] of btns) x.classList.toggle("on", k === mode); }
    const seed = (s) => { let h = 2166136261; for (const ch of s) h = Math.imul(h ^ ch.charCodeAt(0), 16777619); return ((h >>> 0) % 1000) / 1000; };

    function regroup() {
      const m = new Map();
      for (const p of items) { const g = groupOf(mode, p); let o = m.get(g.key); if (!o) m.set(g.key, o = { key: g.key, name: g.name, ord: g.ord ?? 0, color: g.color || p.color, count: 0, el: null, cx: 0, cy: 0, top: null, rx: 0, ry: 0, rr: 0 }); o.count++; p.g = o; }
      groups = [...m.values()].sort((a, b) => a.ord - b.ord || b.count - a.count);
      labels.replaceChildren(...groups.map((g) => { g.el = mk("div", "mm-label"); const dot = mk("i", ""); dot.style.background = g.color; g.el.append(dot, mk("b", "", g.name), mk("em", "", String(g.count))); return g.el; }));
      layout();
    }
    function layout() {
      if (!W) return;
      const n = groups.length || 1, aw = W - PAD.l - PAD.r, ah = H - PAD.t - PAD.b;
      const cols = n <= 3 ? n : Math.ceil(Math.sqrt(n * aw / ah)), rows = Math.ceil(n / cols);
      groups.forEach((g, i) => { const r = Math.floor(i / cols), inRow = r === rows - 1 ? n - r * cols : cols, c = i % cols + (cols - inRow) / 2; g.cx = PAD.l + (c + 0.5) / cols * aw; g.cy = PAD.t + (r + 0.5) / rows * ah; });
      // bubble size follows the canvas; shrink when many memories would not fit the free area (area share ~ 40%)
      const base = Math.max(0.8, Math.min(1.6, Math.min(W, H) / 520));
      const dot = (p) => Math.min(6, 2 + Math.sqrt(p.v) * 0.9), gap = 7; // a dot plus its breathing room
      const need = items.reduce((a, p) => a + Math.PI * (dot(p) + gap) ** 2, 0) * base * base;
      // and the biggest group must fit its grid cell as a circle, so neighbouring areas never overlap
      const per = new Map(); for (const p of items) per.set(p.g, (per.get(p.g) || 0) + Math.PI * (dot(p) + gap) ** 2 * base * base);
      const cellR = Math.max(20, Math.min(aw / cols, ah / rows) / 2 - RIM - 14), fit = Math.PI * cellR * cellR * 0.7;
      scale = base * Math.min(1, Math.sqrt(aw * ah * 0.32 / Math.max(1, need)), Math.sqrt(fit / Math.max(1, ...per.values())));
      for (const p of items) { p.r = dot(p) * scale; p.cr = (dot(p) + gap) * scale; }
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
      linkUp(); aim(); run();
    }
    function setEdges(list) { edgesAll = list; linkUp(); aim(); cardKey = "x"; run(); draw(); }
    function linkUp() { links = edgesAll.filter((e) => byId.has(e.from) && byId.has(e.to)).map((e) => ({ a: byId.get(e.from), b: byId.get(e.to), t: e.t })); }
    function setGroup(k) { if (k === mode) return; mode = k; placePill(); regroup(); aim(); kick(30); run(); }
    let kicks = 0; // a visual nudge, so the per-memory hash is jitter enough (no randomness needed)
    function kick(a) { kicks++; for (const p of items) { p.vx += (seed(p.id + kicks) - 0.5) * 2 * a; p.vy += (seed(p.id + "v" + kicks) - 0.5) * 2 * a; } }
    function setSelected(id) { const n = id && byId.has(id) ? id : null; if (n && n !== sel) announce(n); const moved = n !== sel; sel = n; if (moved) { aim(); kick(20); run(); } draw(); }
    // Selected memory (Figma 기억 지도): it moves to the middle and its related memories fan out around it; the rest stay in their groups, dimmed.
    function aim() {
      for (const p of items) p.tx = p.ty = undefined;
      const sp = sel && byId.get(sel); if (!sp || !W) return;
      const nb = []; for (const l of links) { const o = l.a === sp ? l.b : l.b === sp ? l.a : null; if (o && !nb.includes(o)) nb.push(o); }
      const cx = PAD.l + (W - PAD.l - PAD.r) / 2, cy = PAD.t + (H - PAD.t - PAD.b) / 2, rx = (W - PAD.l - PAD.r) * 0.36, ry = (H - PAD.t - PAD.b) * 0.4;
      sp.tx = cx; sp.ty = cy;
      nb.sort((a, b) => (a.g.ord - b.g.ord) || a.g.key.localeCompare(b.g.key)); // neighbours of one group sit side by side
      nb.forEach((p, i) => { const t = -Math.PI / 2 + (i + 0.5) / nb.length * Math.PI * 2; p.tx = cx + Math.cos(t) * rx; p.ty = cy + Math.sin(t) * ry; });
    }
    function setEdgeAll(v) { showAll = !!v; draw(); }
    function setZoom(z) { zoom = z; draw(); }
    function announce(id) { const t = tip(id); if (t) live.textContent = `${t.text} ${t.meta}`; }

    function step(dt) {
      const damp = Math.exp(-5 * dt);
      for (const p of items) { const tx = p.tx ?? p.g.cx, ty = p.ty ?? p.g.cy; p.vx += (tx - p.x) * 5 * dt; p.vy += (ty - p.y) * 5 * dt; }
      if (!sel) for (const l of links) { if (l.a.g !== l.b.g) continue; const dx = l.b.x - l.a.x, dy = l.b.y - l.a.y, d = Math.hypot(dx, dy) || 1; if (d <= SPRING.len) continue; const f = (d - SPRING.len) * SPRING.k * dt, fx = dx / d * f, fy = dy / d * f; l.a.vx += fx; l.a.vy += fy; l.b.vx -= fx; l.b.vy -= fy; }
      const sp = sel && byId.get(sel);
      if (sp) for (const p of items) { // memories not on the fan move out of the middle so the fan reads clearly
        if (p === sp || p.tx != null) continue; const dx = p.x - sp.x, dy = p.y - sp.y, d = Math.hypot(dx, dy) || 1, room = Math.min(W, H) * 0.22;
        if (d < room) { p.vx += dx / d * (room - d) * 8 * dt; p.vy += dy / d * (room - d) * 8 * dt; }
      }
      let e = 0;
      for (const p of items) { p.vx *= damp; p.vy *= damp; p.x += p.vx * dt * 1.6; p.y += p.vy * dt * 1.6; e += p.vx * p.vx + p.vy * p.vy; }
      energy = items.length ? e / items.length : 0;
      // overlap pass on a grid (cell = widest pair), so each bubble only meets its neighbours: O(n), not all pairs
      let maxR = 1; for (const p of items) maxR = Math.max(maxR, p.cr || p.r);
      const cell = 2 * maxR + 2;
      for (let it = 0; it < 3; it++) {
        const grid = new Map(), key = (gx, gy) => gx * 65536 + gy;
        items.forEach((p, i) => { const k = key(Math.floor(p.x / cell), Math.floor(p.y / cell)); (grid.get(k) || grid.set(k, []).get(k)).push(i); });
        items.forEach((a, i) => {
          const gx = Math.floor(a.x / cell), gy = Math.floor(a.y / cell);
          for (let ox = -1; ox <= 1; ox++) for (let oy = -1; oy <= 1; oy++) for (const j of grid.get(key(gx + ox, gy + oy)) || []) {
            if (j <= i) continue;
            const b = items[j]; let dx = b.x - a.x, dy = b.y - a.y, d = Math.hypot(dx, dy); const min = (a.cr || a.r) + (b.cr || b.r); if (d >= min) continue;
            if (!d) { dx = 1; dy = 0; d = 1; } const push = (min - d) / 2, nx = dx / d, ny = dy / d; a.x -= nx * push; a.y -= ny * push; b.x += nx * push; b.y += ny * push;
          }
        });
      }
      for (const p of items) { const m = p.r + RIM; p.x = Math.max(PAD.l + m, Math.min(W - PAD.r - m, p.x)); p.y = Math.max(PAD.t + m, Math.min(H - PAD.b - m, p.y)); }
    }
    function frame(t) {
      raf = 0; if (dead || !visible || !root.isConnected) return;
      const dt = Math.min(0.033, (t - last) / 1000 || 0.016); last = t; step(dt); draw();
      if (energy < 8) { if (++calm >= 30) return; } else calm = 0;
      raf = requestAnimationFrame(frame);
    }
    function run() {
      if (dead) return;
      calm = 0;
      if (reducedMq.matches) { for (let i = 0; i < 400; i++) step(1 / 60); draw(); return; }
      if (!raf && visible && W) { last = performance.now(); raf = requestAnimationFrame(frame); }
    }

    // blend two colours (hex / rgb() / CSS names via a 1px canvas); cached per pair
    const mixCache = new Map(), probe = doc.createElement("canvas").getContext("2d");
    const rgb = (c) => { probe.fillStyle = "#000"; probe.fillStyle = c; const h = probe.fillStyle; if (h[0] === "#") return [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16)); return h.match(/[\d.]+/g).slice(0, 3).map(Number); };
    const segDist = (px, py, ax, ay, bx, by) => { const dx = bx - ax, dy = by - ay, t = Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy || 1))); return Math.hypot(px - ax - t * dx, py - ay - t * dy); };
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
        const on = foci.has(l.a.id) || foci.has(l.b.id); if (on) { hot.push(l); continue; } if (!showAll || items.length > MANY) continue;
        const s = EDGE[l.t] || EDGE.supports; ctx.globalAlpha = foci.size ? 0.05 : 0.3; ctx.strokeStyle = s.c; ctx.lineWidth = 1.2; ctx.setLineDash(s.dash);
        ctx.beginPath(); ctx.moveTo(l.a.x, l.a.y); ctx.lineTo(l.b.x, l.b.y); ctx.stroke();
      }
      ctx.setLineDash([]);
      // shortcut: a topic pair whose line would cross a third area is left out; draw curves if that hides too much
      if (showAll && items.length > MANY && groups.length > 1) {
        const pair = new Map(); for (const l of links) if (l.a.g !== l.b.g) { const k = l.a.g.key < l.b.g.key ? `${l.a.g.key}|${l.b.g.key}` : `${l.b.g.key}|${l.a.g.key}`; const o = pair.get(k) || pair.set(k, { a: l.a.g, b: l.b.g, n: 0 }).get(k); o.n++; }
        ctx.strokeStyle = tok("--nc-muted", "#888");
        for (const { a, b, n } of pair.values()) { if (!a.rr || !b.rr) continue; ctx.globalAlpha = foci.size ? 0.05 : 0.35; ctx.lineWidth = Math.min(6, 1 + Math.log2(n)); const dx = b.rx - a.rx, dy = b.ry - a.ry, d = Math.hypot(dx, dy) || 1; if (d <= a.rr + b.rr || groups.some((g) => g !== a && g !== b && g.rr && segDist(g.rx, g.ry, a.rx, a.ry, b.rx, b.ry) < g.rr)) continue; ctx.beginPath(); ctx.moveTo(a.rx + dx / d * a.rr, a.ry + dy / d * a.rr); ctx.lineTo(b.rx - dx / d * b.rr, b.ry - dy / d * b.rr); ctx.stroke(); } // rim to rim, never across the dots
      }
      for (const g of groups) g.top = null;
      // topic areas (Figma 611:54134): a soft tinted circle with a dashed rim around each group's dots
      for (const g of groups) {
        let sx = 0, sy = 0, n = 0; for (const p of items) if (p.g === g) { sx += p.x; sy += p.y; n++; } if (!n) continue;
        g.rx = sx / n; g.ry = sy / n; let rr = 0; for (const p of items) if (p.g === g) rr = Math.max(rr, Math.hypot(p.x - g.rx, p.y - g.ry) + p.r);
        g.rr = rr + RIM;
        ctx.globalAlpha = foci.size ? 0.03 : 0.06; ctx.fillStyle = g.color; ctx.beginPath(); ctx.arc(g.rx, g.ry, g.rr, 0, 7); ctx.fill();
        ctx.globalAlpha = foci.size ? 0.15 : 0.35; ctx.strokeStyle = g.color; ctx.lineWidth = 1; ctx.setLineDash([3, 4]); ctx.stroke(); ctx.setLineDash([]);
      }
      for (const p of items) {
        const dim = foci.size && !near.has(p.id);
        ctx.globalAlpha = dim ? 0.22 : 1;
        // flat dot in the group's colour (Figma 611:54134), no gloss or shadow
        ctx.fillStyle = p.g.color || p.color; ctx.globalAlpha = dim ? 0.22 : 0.9; ctx.beginPath(); ctx.arc(p.x, p.y, p.r, 0, 7); ctx.fill();
        if (!p.g.top || p.y - p.r < p.g.top.y - p.g.top.r) p.g.top = p;
      }
      const bg = tok("--nc-canvas", "#0b0b0b"), tr = (x) => window.ArkismI18n?.translate(x) || x, tags = [];
      for (const l of hot) { // focused relations (Figma 기억 지도): thin soft curves on top of the bubbles, the relation name in a pill at the middle
        const s = EDGE[l.t] || EDGE.supports, dx = l.b.x - l.a.x, dy = l.b.y - l.a.y, d = Math.hypot(dx, dy) || 1;
        const mx = (l.a.x + l.b.x) / 2, my = (l.a.y + l.b.y) / 2, gx = (l.a.g.cx + l.b.g.cx) / 2, gy = (l.a.g.cy + l.b.g.cy) / 2;
        let nx = -dy / d, ny = dx / d; if ((mx - gx) * nx + (my - gy) * ny < 0) { nx = -nx; ny = -ny; }
        const bow = Math.max(10, d * 0.16), cx = mx + nx * bow, cy = my + ny * bow;
        const edge = (q) => { const ex = cx - q.x, ey = cy - q.y, el = Math.hypot(ex, ey) || 1; return [q.x + ex / el * (q.r + 2), q.y + ey / el * (q.r + 2)]; }; // start at the rim, not inside the bubble
        const [ax, ay] = edge(l.a), [bx, by] = edge(l.b), col = l.t === "same_session" ? tok("--nc-muted", s.c) : s.c;
        const path = () => { ctx.beginPath(); ctx.moveTo(ax, ay); ctx.quadraticCurveTo(cx, cy, bx, by); ctx.stroke(); };
        ctx.globalAlpha = 0.55; ctx.strokeStyle = bg; ctx.lineWidth = 4; ctx.setLineDash([]); path(); // faint backing so the line reads over bubbles
        ctx.globalAlpha = 1; ctx.strokeStyle = col; ctx.lineWidth = 1.6; ctx.setLineDash(s.dash); path();
        if (d > l.a.r + l.b.r + 40) { // pill a little past the middle, toward the related memory, so the focus card does not cover it
          const t = foci.has(l.a.id) ? 0.6 : 0.4, u = 1 - t;
          tags.push([s, col, u * u * l.a.x + 2 * u * t * cx + t * t * l.b.x, u * u * l.a.y + 2 * u * t * cy + t * t * l.b.y]);
        }
      }
      ctx.setLineDash([]); ctx.font = `500 11px ${tok("--sans", "sans-serif")}`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      for (const [s, c, x, y] of tags) {
        const t = tr(s.n), w = ctx.measureText(t).width + 16;
        ctx.globalAlpha = 1; ctx.fillStyle = bg; ctx.strokeStyle = c; ctx.lineWidth = 1;
        ctx.beginPath(); ctx.roundRect(x - w / 2, y - 10, w, 20, 10); ctx.fill(); ctx.globalAlpha = 0.7; ctx.stroke();
        ctx.globalAlpha = 1; ctx.fillStyle = c; ctx.fillText(t, x, y + 0.5);
      }
      for (const id of [sel, kb].filter(Boolean)) { const p = byId.get(id); if (!p) continue; // soft halo + dashed orbit like the Figma focus node
        ctx.globalAlpha = 0.07; ctx.fillStyle = p.color; ctx.beginPath(); ctx.arc(p.x, p.y, p.r + 14, 0, 7); ctx.fill();
        ctx.globalAlpha = 0.45; ctx.strokeStyle = p.color; ctx.lineWidth = 1; ctx.setLineDash([2, 4]); ctx.beginPath(); ctx.arc(p.x, p.y, p.r + 9, 0, 7); ctx.stroke(); ctx.setLineDash([]);
      }
      for (const p of items) if (p.id === hover || p.id === sel || p.id === kb) { ctx.globalAlpha = 1; ctx.strokeStyle = text; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(p.x, p.y, p.r + (p.id === hover ? 1 : 2.5), 0, 7); ctx.stroke(); }
      ctx.globalAlpha = 1;
      labels.hidden = !!sel; // group names would float over the fan
      for (const g of groups) { if (!g.rr) continue; const [x, y] = scr(g.rx, g.ry - g.rr); g.el.style.transform = `translate(${x}px,${y}px) translate(-50%,-50%)`; }
      placeCards();
      const id = hover || kb, p = id && byId.get(id), t = p && tip(id);
      if (p && t) { const [sx, y] = scr(p.x, p.y - p.r); tipT.textContent = t.text; tipM.textContent = t.meta; const hw = tipEl.offsetWidth / 2 + 6, x = Math.max(hw, Math.min(W - hw, sx)); tipEl.style.transform = `translate(${x}px,${y - 8}px) translate(-50%,-100%)`; tipEl.classList.add("on"); } else tipEl.classList.remove("on");
    }

    // Name cards (Figma 기억 지도): the selected memory gets a card under it, each related memory a small card beside it
    function buildCards() {
      const sp = sel && byId.get(sel), key = sp ? `${sel}|${links.length}|${items.length}` : "";
      if (key === cardKey) return; cardKey = key; cards = [];
      if (sp && card) {
        const c = card(sel); if (c) cards.push({ p: sp, main: true, title: c.title, sub: [c.place, c.meta].filter(Boolean).join(" · ") });
        const tr = (x) => window.ArkismI18n?.translate(x) || x, seen = new Set([sp]);
        for (const l of links) { const o = l.a === sp ? l.b : l.b === sp ? l.a : null; if (!o || seen.has(o)) continue; seen.add(o); const c2 = card(o.id); if (c2) cards.push({ p: o, title: c2.title, sub: [c2.place, tr((EDGE[l.t] || EDGE.supports).n)].filter(Boolean).join(" · ") }); }
      }
      cardsEl.replaceChildren(...cards.map((c) => { c.el = mk("div", c.main ? "mm-card main" : "mm-card"); const t = mk("b", "", c.title); t.setAttribute("data-no-i18n", ""); c.el.append(t, mk("small", "", c.sub)); return c.el; }));
      for (const c of cards) { c.w = c.el.offsetWidth; c.h = c.el.offsetHeight; }
    }
    function placeCards() {
      buildCards(); if (!cards.length) return;
      const box = [];
      for (const c of cards) {
        const [x, y] = scr(c.p.x, c.p.y), r = c.p.r * zoom;
        let L, T;
        if (c.main) { L = x - c.w / 2; T = y + r + 14; }
        else { // beside the bubble on the outer side when it fits, otherwise above (upper half) or below (lower half)
          const out = x >= W / 2 ? x + r + 10 : x - r - 10 - c.w;
          if (out >= 8 && out + c.w <= W - 8) { L = out; T = y - c.h / 2; } else { L = x - c.w / 2; T = y < H / 2 ? y - r - 8 - c.h : y + r + 8; }
        }
        L = Math.max(8, Math.min(W - c.w - 8, L)); T = Math.max(PAD.t - 30, Math.min(H - PAD.b - c.h, T)); // stay clear of the tabs and the legend bar
        const up = !c.main && y < H / 2;
        for (let i = 0; i < 6; i++) { const hitB = box.find((b) => L < b[0] + b[2] && L + c.w > b[0] && T < b[1] + b[3] && T + c.h > b[1]); if (!hitB) break; T = up ? hitB[1] - c.h - 4 : hitB[1] + hitB[3] + 4; } // nudge away from the middle past an earlier card
        const clash = T < PAD.t - 30 || T + c.h > H - PAD.b || box.some((b) => L < b[0] + b[2] && L + c.w > b[0] && T < b[1] + b[3] && T + c.h > b[1]);
        c.el.hidden = clash && !c.main; if (c.el.hidden) continue; // a small window drops the cards that do not fit; the side panel still lists them
        box.push([L, T, c.w, c.h]); c.el.style.transform = `translate(${L}px,${T}px)`;
      }
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
      layout(); for (const p of items) if (!p.placed) place(p); aim();
      placePill(); run(); draw();
    }
    const ro = window.ResizeObserver ? new ResizeObserver(resize) : null, io = window.IntersectionObserver ? new IntersectionObserver((es) => { visible = es[es.length - 1].isIntersecting; if (visible) run(); }) : null;
    ro?.observe(root); io?.observe(root);
    if (reducedMq.addEventListener) reducedMq.addEventListener("change", run);
    // the map's window calls this on close, so no observer or listener keeps the old map alive
    function destroy() { dead = true; if (raf) cancelAnimationFrame(raf); raf = 0; ro?.disconnect(); io?.disconnect(); reducedMq.removeEventListener?.("change", run); }

    return { el: root, setItems, setEdges, setSelected, setEdgeAll, setZoom, setGroup, destroy };
  }
  return { create };
})();
