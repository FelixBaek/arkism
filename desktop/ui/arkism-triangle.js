// @ts-check
// Rail art: the arkism triangle spiralling inward with glints running along its edge (ArkismTriangle.jsx, framework-free).
// Colors come from CSS (.ark-line / .ark-accent in shell.css) so the light/dark theme switch needs no remount;
// the page CSP forbids style attributes, so the svg is styled through .style and classes only.
let arkTriangleSeq = 0;
window.mountArkismTriangle = (el, opts = {}) => {
  const o = { width: 178, duration: 12, lineWeight: 1.5, layers: 28, twist: 62, glints: 3, fps: 12, ...opts };
  const OUTER = [[91.105, 11.71], [143.3, 105.76], [38.91, 105.76]];
  const OC = [(OUTER[0][0] + OUTER[1][0] + OUTER[2][0]) / 3, (OUTER[0][1] + OUTER[1][1] + OUTER[2][1]) / 3];
  const STEPS = 40, LEN = 0.14, NS = "http://www.w3.org/2000/svg";
  const smooth = (a, b, x) => { const t = Math.min(1, Math.max(0, (x - a) / (b - a))); return t * t * (3 - 2 * t); };
  const ease = (t) => -(Math.cos(Math.PI * t) - 1) / 2;
  const layerPath = (t, tw) => {
    const step = (tw * Math.PI) / 180 / STEPS;
    const s = Math.pow(1 / (Math.sqrt(3) * Math.sin(step) + Math.cos(step)), t * STEPS);
    const a = (tw * t * Math.PI) / 180, ca = Math.cos(a), sa = Math.sin(a);
    const p = OUTER.map(([x, y]) => { const dx = (x - OC[0]) * s, dy = (y - OC[1]) * s; return [OC[0] + dx * ca - dy * sa, OC[1] + dx * sa + dy * ca]; });
    return `M${p[0][0].toFixed(3)} ${p[0][1].toFixed(3)}L${p[1][0].toFixed(3)} ${p[1][1].toFixed(3)}L${p[2][0].toFixed(3)} ${p[2][1].toFixed(3)}Z`;
  };
  const mk = (tag, attrs, parent) => { const n = document.createElementNS(NS, tag); for (const k in attrs) n.setAttribute(k, attrs[k]); parent && parent.appendChild(n); return n; };
  const lw = o.lineWeight, gid = "ark-glow-" + ++arkTriangleSeq;
  const svg = mk("svg", { viewBox: "0 0 178 118", width: o.width, height: (o.width * 118) / 178, "aria-hidden": "true", class: "ark-triangle" });
  mk("feGaussianBlur", { stdDeviation: 1.6 }, mk("filter", { id: gid, x: "-50%", y: "-50%", width: "200%", height: "200%" }, mk("defs", {}, svg)));
  const base = { fill: "none", "stroke-linejoin": "round" };
  const layers = Array.from({ length: o.layers }, () => mk("path", { ...base, class: "ark-line", "stroke-width": 0.2 * lw }, svg));
  const hole = mk("path", { ...base, class: "ark-line", "stroke-width": 0.2 * lw, opacity: 0.7 }, svg);
  const outerD = layerPath(0, 0);
  mk("path", { ...base, d: outerD, class: "ark-accent", "stroke-width": 0.29 * lw, opacity: 0.8 }, svg);
  const glints = Array.from({ length: o.glints }, (_, i) => {
    const g = mk("g", { opacity: i === 0 ? 1 : 0.7 }, svg);
    const common = { d: outerD, fill: "none", class: "ark-accent", pathLength: 1, "stroke-dasharray": `${LEN} ${1 - LEN}`, "stroke-linecap": "round" };
    return [mk("path", { ...common, "stroke-width": 0.29 * lw * 5, opacity: 0.35, filter: `url(#${gid})` }, g), mk("path", { ...common, "stroke-width": 0.29 * lw * 1.6, opacity: 0.9 }, g)];
  });
  el.appendChild(svg);

  const render = (p) => {
    const tw = o.twist + 10 * (0.5 - 0.5 * Math.cos(2 * Math.PI * p) - 0.5), shift = p * 6, n = o.layers;
    layers.forEach((path, k) => {
      const t = ((((k + shift) % n) + n) % n) / n;
      path.setAttribute("d", layerPath(t, tw));
      path.setAttribute("opacity", 0.6 * smooth(0, 0.1, t) * (1 - smooth(0.86, 1, t)) * (0.65 + 0.35 * Math.cos(2 * Math.PI * (t * 2 - p * 2))));
    });
    hole.setAttribute("d", layerPath(1, tw));
    const head = ease(p);
    glints.forEach((pair, i) => pair.forEach((g) => g.setAttribute("stroke-dashoffset", -(head + i / o.glints - LEN))));
  };

  // A 12 s drift needs no 60 fps: about 12 renders a second (#177: every frame was ~4,000 svg writes a second).
  let raf = 0, last = -Infinity;
  const t0 = performance.now(), frame = 1000 / o.fps;
  const tick = (now) => {
    if (!document.hidden && now - last >= frame) { last = now; render((((now - t0) / 1000) % o.duration) / o.duration); }
    raf = requestAnimationFrame(tick);
  };
  // follow the reduced-motion setting live, not just at mount
  const still = window.matchMedia?.("(prefers-reduced-motion: reduce)");
  const sync = () => { cancelAnimationFrame(raf); raf = 0; if (still?.matches) render(0); else raf = requestAnimationFrame(tick); };
  sync();
  still?.addEventListener?.("change", sync);
  return () => { cancelAnimationFrame(raf); still?.removeEventListener?.("change", sync); svg.remove(); };
};
document.addEventListener("DOMContentLoaded", () => { const art = document.getElementById("rail-art"); if (art) window.mountArkismTriangle(art); });
