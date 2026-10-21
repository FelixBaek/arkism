// Lucian UX/UI Prompt #204 · MIT License (c) 2026 Seokwoooo · https://lucianlabs.dev/uxui-prompt/#204
// 기억 지도 canvas: one physics bubble per memory, regrouped by 전체 / 칸별 / 종류별, with relation lines drawn under the bubbles.
// Ported from the reference (tab pill, hover/tooltip, name cards); the data mapping is Arkism's.
// The layout is static: each group is a sunflower spiral (biggest dots in the middle) around its area centre, so it never drifts.
// The reference's force simulation never came to rest with a few thousand dots (they kept jittering toward the centre).
// A change (group tab, selection, resize) computes the new places and the dots glide there once (no glide with reduced motion).
// Zoom (wheel · pinch · +/− keys · buttons) and drag-to-pan only move the view; past LABEL_ZOOM the visible dots get short names.
window.ArkismMemoryBubbles = (() => {
  "use strict";
  // Line styles = the legend bar (v4-overview.css .ov-map-e-*): 지지 solid, 관련 dotted, 충돌 red dashed, 대체 purple dotted.
  const EDGE = { supports: { c: "#7fc7a0", dash: [], n: "지지" }, same_session: { c: "#c8c8c2", dash: [1, 3.5], n: "관련" }, conflicts: { c: "#ef5a64", dash: [6, 5], n: "충돌" }, supersedes: { c: "#a596f7", dash: [1, 3.5], n: "대체" } };
  const RIM = 16, MANY = 150; // area rim past its outer dots · above MANY dots the overview shows topic-to-topic lines, not every line
  const PAD = { l: 18, r: 18, t: 84, b: 76 }; // top clears the tabs + group labels, bottom clears the legend bar
  const GOLDEN = Math.PI * (3 - Math.sqrt(5)); // spiral step
  const GLIDE = 420, ZMIN = 0.5, ZMAX = 4, LABEL_ZOOM = 1.6, LABEL_MAX = 60, LABEL_CHARS = 10;

  function create(opts) {
    const { tabs, groupOf, tip, card, onSelect, onClear, onZoom } = opts;
    const doc = document, mk = (tag, cls, txt) => { const n = doc.createElement(tag); if (cls) n.className = cls; if (txt != null) n.textContent = txt; return n; };
    const root = mk("div", "mm-bubbles"), canvas = mk("canvas", "mm-canvas"), ctx = canvas.getContext("2d");
    const cardsEl = mk("div", "mm-cards"), tabBar = mk("div", "mm-tabs"), pill = mk("span", "mm-pill"), live = mk("div", "mm-live"), tipEl = mk("div", "mm-tip"), labels = mk("div", "mm-labels");
    canvas.tabIndex = 0; canvas.setAttribute("role", "application"); canvas.setAttribute("aria-label", "기억 버블 지도 · 화살표로 고르고 +/−로 확대·축소, 끌어서 옮기기");
    live.setAttribute("aria-live", "polite"); tipEl.setAttribute("aria-hidden", "true");
    const tipT = mk("span", "mm-tip-t"), tipM = mk("span", "mm-tip-m"); tipT.setAttribute("data-no-i18n", ""); tipEl.append(tipT, tipM);
    live.setAttribute("data-no-i18n", "");
    tabBar.append(pill); cardsEl.setAttribute("aria-hidden", "true"); root.append(canvas, cardsEl, labels, tabBar, tipEl, live);

    const reducedMq = window.matchMedia ? matchMedia("(prefers-reduced-motion: reduce)") : { matches: false };
    let cards = [], cardKey = "", W = 0, H = 0, dpr = 1, items = [], byId = new Map(), links = [], edgesAll = [], groups = [], mode = tabs[0][0], idsKey = "";
    let sel = null, kb = null, hover = null, showAll = true, zoom = 1, panX = 0, panY = 0, scale = 1, raf = 0, glide = null, dead = false, edgeKey = "", nameBoxes = [];
    const btns = new Map();
    for (const [k, l] of tabs) { const b = mk("button", "mm-tab", l); b.type = "button"; b.addEventListener("click", () => setGroup(k)); btns.set(k, b); tabBar.append(b); }

    const tok = (n, d) => getComputedStyle(root).getPropertyValue(n).trim() || d;
    function placePill() { const b = btns.get(mode); if (!b || !b.offsetWidth) return; pill.style.left = `${b.offsetLeft}px`; pill.style.width = `${b.offsetWidth}px`; for (const [k, x] of btns) x.classList.toggle("on", k === mode); }

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
      const dot = (p) => Math.min(9, 2 + Math.sqrt(p.deg || 0) * 1.6), gap = 7; // a dot (bigger with more relations) plus its breathing room
      const need = items.reduce((a, p) => a + Math.PI * (dot(p) + gap) ** 2, 0) * base * base;
      // and the biggest group must fit its grid cell as a circle, so neighbouring areas never overlap
      const per = new Map(); for (const p of items) per.set(p.g, (per.get(p.g) || 0) + Math.PI * (dot(p) + gap) ** 2 * base * base);
      const cellR = Math.max(20, Math.min(aw / cols, ah / rows) / 2 - RIM - 14), fit = Math.PI * cellR * cellR * 0.7;
      scale = base * Math.min(1, Math.sqrt(aw * ah * 0.32 / Math.max(1, need)), Math.sqrt(fit / Math.max(1, ...per.values())));
      for (const p of items) { p.r = dot(p) * scale; p.cr = (dot(p) + gap) * scale; }
      items.sort((a, b) => b.r - a.r);
    }
    // Final places: a spiral per group (Vogel: radius c·√k, golden-angle turns; c from the dots' own room, so neighbours never touch).
    // With a selection, it and its related memories take the fan places from aim(); the other dots stay put, dimmed under them.
    function arrange() {
      const by = new Map(); for (const p of items) { const l = by.get(p.g); if (l) l.push(p); else by.set(p.g, [p]); }
      for (const [g, list] of by) {
        list.sort((a, b) => b.r - a.r || (a.id < b.id ? -1 : 1));
        const c = list.reduce((a, p) => a + p.cr, 0) / list.length * 0.92;
        list.forEach((p, k) => { const rad = c * Math.sqrt(k + 0.5), t = k * GOLDEN; p.x = g.cx + Math.cos(t) * rad; p.y = g.cy + Math.sin(t) * rad; });
      }
      for (const p of items) {
        if (p.tx != null) { p.x = p.tx; p.y = p.ty; continue; }
        const m = p.r + RIM; p.x = Math.max(PAD.l + m, Math.min(W - PAD.r - m, p.x)); p.y = Math.max(PAD.t + m, Math.min(H - PAD.b - m, p.y));
      }
    }

    function setItems(list) {
      const key = list.map((x) => `${x.id}:${x.v}:${x.reach}:${x.kind}`).join("|");
      const same = key === idsKey; idsKey = key;
      if (same) { draw(); return; }
      items = list.map((x) => { const o = byId.get(x.id); return o ? Object.assign(o, x) : { ...x, x: 0, y: 0, r: 8, g: null }; });
      byId = new Map(items.map((p) => [p.id, p]));
      linkUp(); regroup(); aim(); run();
    }
    function setEdges(list) {
      const key = list.map((e) => `${e.from}>${e.to}:${e.t}`).join("|"); if (key === edgeKey) { draw(); return; } // the map repaints often; same lines → no re-settle
      edgeKey = key; edgesAll = list; linkUp(); layout(); aim(); cardKey = "x"; run();
    }
    function linkUp() {
      links = edgesAll.filter((e) => byId.has(e.from) && byId.has(e.to)).map((e) => ({ a: byId.get(e.from), b: byId.get(e.to), t: e.t }));
      for (const p of items) p.deg = 0; for (const l of links) { l.a.deg++; l.b.deg++; }
    }
    function setGroup(k) { if (k === mode) return; mode = k; placePill(); regroup(); aim(); run(); }
    function setSelected(id) { const n = id && byId.has(id) ? id : null; if (n && n !== sel) { announce(n); panX = panY = 0; } const moved = n !== sel; sel = n; if (moved) { aim(); run(); } else draw(); } // a new selection sits in the middle of the view
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
    // view only: screen = centre + (world − centre) · zoom + pan; (cx, cy) stays under the cursor while zooming
    function setZoom(z, cx = W / 2, cy = H / 2) {
      const nz = Math.min(ZMAX, Math.max(ZMIN, z)); if (!W) { zoom = nz; return; }
      const wx = (cx - W / 2 - panX) / zoom, wy = (cy - H / 2 - panY) / zoom;
      zoom = nz; panX = cx - W / 2 - wx * zoom; panY = cy - H / 2 - wy * zoom; onZoom?.(zoom); draw();
    }
    const zoomBy = (f, cx, cy) => setZoom(zoom * f, cx, cy);
    function resetView() { zoom = 1; panX = panY = 0; onZoom?.(zoom); draw(); }
    function announce(id) { const t = tip(id); if (t) live.textContent = `${t.text} ${t.meta}`; }

    // Compute the final places, then glide once from where the dots are drawn now.
    function run() {
      if (dead || !W || !items.length) { draw(); return; }
      const shown = new Map(); for (const p of items) if (p.seen) shown.set(p, [p.x, p.y]);
      glide = null;
      arrange();
      for (const p of items) p.seen = true;
      if (reducedMq.matches || !shown.size) { draw(); return; }
      glide = { from: shown, to: new Map(items.map((p) => [p, [p.x, p.y]])), t0: performance.now() };
      if (!raf) raf = requestAnimationFrame(frame);
    }
    function frame(t) {
      raf = 0; if (dead || !glide) return;
      const k = Math.min(1, (t - glide.t0) / GLIDE), e = 1 - (1 - k) ** 3;
      for (const [p, [x, y]] of glide.to) { const f = glide.from.get(p); if (f) { p.x = f[0] + (x - f[0]) * e; p.y = f[1] + (y - f[1]) * e; } }
      if (k >= 1) glide = null; else raf = requestAnimationFrame(frame);
      draw();
    }

    const segDist = (px, py, ax, ay, bx, by) => { const dx = bx - ax, dy = by - ay, t = Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy || 1))); return Math.hypot(px - ax - t * dx, py - ay - t * dy); };
    const px = (dash) => dash.map((v) => v / zoom); // dash lengths in screen pixels at any zoom
    const scr = (x, y) => [W / 2 + (x - W / 2) * zoom + panX, H / 2 + (y - H / 2) * zoom + panY];
    function draw() {
      if (!W) return;
      const text = tok("--nc-text", "#eee");
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, W, H);
      ctx.translate(W / 2 + panX, H / 2 + panY); ctx.scale(zoom, zoom); ctx.translate(-W / 2, -H / 2);
      const foci = new Set([hover, kb, sel].filter(Boolean)), near = new Set(foci);
      for (const l of links) if (foci.has(l.a.id) || foci.has(l.b.id)) { near.add(l.a.id); near.add(l.b.id); }
      ctx.lineCap = "round";
      const hot = [];
      for (const l of links) { // quiet lines sit under the bubbles; lines of the focused memory are drawn on top (below)
        const on = foci.has(l.a.id) || foci.has(l.b.id); if (on) { hot.push(l); continue; } if (!showAll || items.length > MANY) continue;
        const s = EDGE[l.t] || EDGE.supports; ctx.globalAlpha = foci.size ? 0.05 : 0.3; ctx.strokeStyle = s.c; ctx.lineWidth = 1.2 / zoom; ctx.setLineDash(px(s.dash));
        ctx.beginPath(); ctx.moveTo(l.a.x, l.a.y); ctx.lineTo(l.b.x, l.b.y); ctx.stroke();
      }
      ctx.setLineDash([]);
      // shortcut: a topic pair whose line would cross a third area is left out; draw curves if that hides too much
      if (showAll && items.length > MANY && groups.length > 1) {
        const pair = new Map(); for (const l of links) if (l.a.g !== l.b.g) { const k = l.a.g.key < l.b.g.key ? `${l.a.g.key}|${l.b.g.key}` : `${l.b.g.key}|${l.a.g.key}`; const o = pair.get(k) || pair.set(k, { a: l.a.g, b: l.b.g, n: 0 }).get(k); o.n++; }
        ctx.strokeStyle = tok("--nc-muted", "#888");
        for (const { a, b, n } of pair.values()) { if (!a.rr || !b.rr) continue; ctx.globalAlpha = foci.size ? 0.05 : 0.35; ctx.lineWidth = Math.min(6, 1 + Math.log2(n)) / zoom; const dx = b.rx - a.rx, dy = b.ry - a.ry, d = Math.hypot(dx, dy) || 1; if (d <= a.rr + b.rr || groups.some((g) => g !== a && g !== b && g.rr && segDist(g.rx, g.ry, a.rx, a.ry, b.rx, b.ry) < g.rr)) continue; ctx.beginPath(); ctx.moveTo(a.rx + dx / d * a.rr, a.ry + dy / d * a.rr); ctx.lineTo(b.rx - dx / d * b.rr, b.ry - dy / d * b.rr); ctx.stroke(); } // rim to rim, never across the dots
      }
      for (const g of groups) g.top = null;
      // topic areas (Figma 611:54134): a soft tinted circle with a dashed rim around each group's dots
      for (const g of groups) {
        let sx = 0, sy = 0, n = 0; const home = (p) => p.g === g && p.tx == null; for (const p of items) if (home(p)) { sx += p.x; sy += p.y; n++; } if (!n) continue;
        g.rx = sx / n; g.ry = sy / n; let rr = 0; for (const p of items) if (home(p)) rr = Math.max(rr, Math.hypot(p.x - g.rx, p.y - g.ry) + p.r);
        g.rr = rr + RIM;
        ctx.globalAlpha = foci.size ? 0.03 : 0.06; ctx.fillStyle = g.color; ctx.beginPath(); ctx.arc(g.rx, g.ry, g.rr, 0, 7); ctx.fill();
        ctx.globalAlpha = foci.size ? 0.15 : 0.35; ctx.strokeStyle = g.color; ctx.lineWidth = 1 / zoom; ctx.setLineDash(px([3, 4])); ctx.stroke(); ctx.setLineDash([]);
      }
      // dimmed dots first, then the focused ones on top of them
      for (const p of [...items.filter((q) => foci.size && !near.has(q.id)), ...items.filter((q) => !foci.size || near.has(q.id))]) {
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
        ctx.globalAlpha = 0.55; ctx.strokeStyle = bg; ctx.lineWidth = 4 / zoom; ctx.setLineDash([]); path(); // faint backing so the line reads over bubbles
        ctx.globalAlpha = 1; ctx.strokeStyle = col; ctx.lineWidth = 1.6 / zoom; ctx.setLineDash(px(s.dash)); path();
        if ((d - l.a.r - l.b.r) * zoom > 40) { // pill a little past the middle, toward the related memory, so the focus card does not cover it
          const t = foci.has(l.a.id) ? 0.6 : 0.4, u = 1 - t;
          tags.push([s, col, ...scr(u * u * l.a.x + 2 * u * t * cx + t * t * l.b.x, u * u * l.a.y + 2 * u * t * cy + t * t * l.b.y)]);
        }
      }
      ctx.save(); ctx.setTransform(dpr, 0, 0, dpr, 0, 0); // pills in screen pixels, like the dot names
      ctx.setLineDash([]); ctx.font = `500 11px ${tok("--sans", "sans-serif")}`; ctx.textAlign = "center"; ctx.textBaseline = "middle";
      for (const [s, c, x, y] of tags) {
        const t = tr(s.n), w = ctx.measureText(t).width + 16;
        ctx.globalAlpha = 1; ctx.fillStyle = bg; ctx.strokeStyle = c; ctx.lineWidth = 1;
        ctx.beginPath(); ctx.roundRect(x - w / 2, y - 10, w, 20, 10); ctx.fill(); ctx.globalAlpha = 0.7; ctx.stroke();
        ctx.globalAlpha = 1; ctx.fillStyle = c; ctx.fillText(t, x, y + 0.5);
      }
      ctx.restore();
      for (const id of [sel, kb].filter(Boolean)) { const p = byId.get(id); if (!p) continue; // soft halo + dashed orbit like the Figma focus node
        const hc = p.g.color || p.color; // the dot's own colour
        ctx.globalAlpha = 0.07; ctx.fillStyle = hc; ctx.beginPath(); ctx.arc(p.x, p.y, p.r + 14 / zoom, 0, 7); ctx.fill();
        ctx.globalAlpha = 0.45; ctx.strokeStyle = hc; ctx.lineWidth = 1 / zoom; ctx.setLineDash(px([2, 4])); ctx.beginPath(); ctx.arc(p.x, p.y, p.r + 9 / zoom, 0, 7); ctx.stroke(); ctx.setLineDash([]);
      }
      for (const p of items) if (p.id === hover || p.id === sel || p.id === kb) { ctx.globalAlpha = 1; ctx.strokeStyle = text; ctx.lineWidth = 2 / zoom; ctx.beginPath(); ctx.arc(p.x, p.y, p.r + (p.id === hover ? 1 : 2.5) / zoom, 0, 7); ctx.stroke(); }
      ctx.globalAlpha = 1;
      drawNames(foci, near, bg, text);
      labels.hidden = !!sel; // group names would float over the fan
      for (const g of groups) { if (!g.rr) continue; const [x, y] = scr(g.rx, g.ry - g.rr); g.el.style.transform = `translate(${x}px,${y}px) translate(-50%,-50%)`; }
      placeCards();
      const id = [hover, kb].find((x) => x && x !== sel), p = id && byId.get(id), t = p && tip(id); // the selection has its card
      if (p && t) { const [sx, y] = scr(p.x, p.y - p.r); tipT.textContent = t.text; tipM.textContent = t.meta; const hw = tipEl.offsetWidth / 2 + 6, x = Math.max(hw, Math.min(W - hw, sx)); tipEl.style.transform = `translate(${x}px,${y - 8}px) translate(-50%,-100%)`; tipEl.classList.add("on"); } else tipEl.classList.remove("on");
    }

    // Zoomed in: short names beside the dots on screen, biggest first, capped and never overlapping; they are clickable (hit()).
    function drawNames(foci, near, bg, text) {
      nameBoxes = []; if (zoom < LABEL_ZOOM) return;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.font = `500 11px ${tok("--sans", "sans-serif")}`; ctx.textAlign = "left"; ctx.textBaseline = "middle";
      const muted = tok("--nc-muted", "#999"), taken = [];
      const on = items.filter((p) => { if (foci.size && !near.has(p.id)) return false; const [x, y] = scr(p.x, p.y); return x > 0 && x < W - 40 && y > PAD.t - 40 && y < H - PAD.b; }).sort((a, b) => b.v - a.v);
      for (const p of on) {
        if (nameBoxes.length >= LABEL_MAX) break;
        const full = tip(p.id)?.text || ""; if (!full) continue;
        const t = [...full].length > LABEL_CHARS ? `${[...full].slice(0, LABEL_CHARS).join("")}…` : full;
        const [x, y] = scr(p.x, p.y), w = ctx.measureText(t).width + 10, L = x + p.r * zoom + 3, T = y - 8, R = [L, T, w, 16];
        if (L + w > W - 4 || taken.some((b) => L < b[0] + b[2] && L + w > b[0] && T < b[1] + b[3] && T + 16 > b[1])) continue;
        let dot = false; for (const q of on) { if (q === p) continue; const [qx, qy] = scr(q.x, q.y), rr = q.r * zoom; if (qx + rr > L && qx - rr < L + w && qy + rr > T && qy - rr < T + 16) { dot = true; break; } } // a name never covers another dot
        if (dot) continue;
        taken.push(R); nameBoxes.push([...R, p.id]);
        ctx.globalAlpha = 0.85; ctx.fillStyle = bg; ctx.beginPath(); ctx.roundRect(L, T, w, 16, 5); ctx.fill();
        ctx.globalAlpha = 1; ctx.fillStyle = p.id === sel || p.id === hover ? text : muted; ctx.fillText(t, L + 5, y + 0.5);
      }
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
      const r = canvas.getBoundingClientRect(), sx = ev.clientX - r.left, sy = ev.clientY - r.top;
      for (const [L, T, w, h, id] of nameBoxes) if (sx >= L && sx <= L + w && sy >= T && sy <= T + h) return id;
      const x = (sx - W / 2 - panX) / zoom + W / 2, y = (sy - H / 2 - panY) / zoom + H / 2;
      let best = null, bd = 1e9;
      for (const p of items) { const d = Math.hypot(p.x - x, p.y - y) - p.r; if (d <= 3 / zoom && d < bd) { bd = d; best = p.id; } }
      return best;
    }
    // drag pans the view; two fingers pinch-zoom; a drag never counts as a click
    const ptrs = new Map(); let drag = null, dragged = false, pinch = 0;
    const local = (ev) => { const r = canvas.getBoundingClientRect(); return [ev.clientX - r.left, ev.clientY - r.top]; };
    canvas.addEventListener("pointerdown", (ev) => {
      if (ev.button !== 0) return; ptrs.set(ev.pointerId, local(ev)); canvas.setPointerCapture?.(ev.pointerId); dragged = false;
      if (ptrs.size === 2) { const [a, b] = [...ptrs.values()]; pinch = Math.hypot(a[0] - b[0], a[1] - b[1]); drag = null; } else drag = { x: ev.clientX, y: ev.clientY, px: panX, py: panY };
    });
    canvas.addEventListener("pointermove", (ev) => {
      if (ptrs.has(ev.pointerId)) ptrs.set(ev.pointerId, local(ev));
      if (ptrs.size === 2 && pinch) { const [a, b] = [...ptrs.values()], d = Math.hypot(a[0] - b[0], a[1] - b[1]); if (d) { zoomBy(d / pinch, (a[0] + b[0]) / 2, (a[1] + b[1]) / 2); pinch = d; dragged = true; } return; }
      if (drag) { const dx = ev.clientX - drag.x, dy = ev.clientY - drag.y; if (dragged || Math.hypot(dx, dy) > 4) { dragged = true; panX = drag.px + dx; panY = drag.py + dy; canvas.style.cursor = "grabbing"; draw(); return; } }
      const id = hit(ev); if (id !== hover) { hover = id; if (id) announce(id); canvas.style.cursor = id ? "pointer" : "grab"; draw(); }
    });
    const up = (ev) => { ptrs.delete(ev.pointerId); if (ptrs.size < 2) pinch = 0; drag = null; canvas.style.cursor = hover ? "pointer" : "grab"; };
    canvas.addEventListener("pointerup", up); canvas.addEventListener("pointercancel", up);
    canvas.addEventListener("pointerleave", () => { if (hover) { hover = null; draw(); } });
    canvas.addEventListener("wheel", (ev) => { ev.preventDefault(); const [x, y] = local(ev); zoomBy(Math.exp(-ev.deltaY * (ev.ctrlKey ? 0.01 : 0.0015)), x, y); }, { passive: false }); // ctrlKey: a trackpad pinch
    canvas.addEventListener("click", (ev) => { if (dragged) { dragged = false; return; } const id = hit(ev); if (id) { kb = null; onSelect(id); } });
    canvas.addEventListener("blur", () => { if (kb) { kb = null; draw(); } });
    canvas.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") { if (!sel && !kb) return; ev.preventDefault(); kb = null; draw(); onClear(); return; } // the first Esc only clears; the window closes on the next
      if (ev.key === "Enter") { const id = kb || sel; if (id) { ev.preventDefault(); onSelect(id); } return; }
      if (ev.key === "+" || ev.key === "=") { ev.preventDefault(); zoomBy(1.25); return; }
      if (ev.key === "-" || ev.key === "_") { ev.preventDefault(); zoomBy(0.8); return; }
      if (ev.key === "0") { ev.preventDefault(); resetView(); return; }
      const dir = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowDown: [0, 1], ArrowUp: [0, -1] }[ev.key]; if (!dir || !items.length) return;
      ev.preventDefault(); const cur = byId.get(kb || sel); let pick = null;
      if (!cur) pick = items[0]; else { let bs = 1e9; for (const p of items) { if (p === cur) continue; const dx = p.x - cur.x, dy = p.y - cur.y, along = dx * dir[0] + dy * dir[1], perp = Math.abs(dx * dir[1] - dy * dir[0]); if (along <= 0) continue; const s = along + perp * 2; if (s < bs) { bs = s; pick = p; } } }
      if (pick) { kb = pick.id; announce(pick.id); draw(); }
    });

    function resize() {
      const r = root.getBoundingClientRect(); if (!r.width || !r.height) return;
      if (r.width === W && r.height === H) return;
      W = r.width; H = r.height; dpr = Math.min(2, window.devicePixelRatio || 1);
      canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr); canvas.style.width = `${W}px`; canvas.style.height = `${H}px`;
      layout(); aim();
      placePill(); run();
    }
    const ro = window.ResizeObserver ? new ResizeObserver(resize) : null;
    ro?.observe(root);
    // the map's window calls this on close, so no observer keeps the old map alive
    function destroy() { dead = true; if (raf) cancelAnimationFrame(raf); raf = 0; ro?.disconnect(); }

    return { el: root, setItems, setEdges, setSelected, setEdgeAll, setZoom, zoomBy, resetView, setGroup, destroy };
  }
  return { create };
})();
