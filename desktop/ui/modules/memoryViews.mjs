// @ts-check
// 기억 › 타임라인 and 그래프: two more ways to see the memories the list already loaded (catalogQuery "memories"),
// so search, kind tabs and 자세히 filters apply to all three. Picking a memory opens the same right detail panel.
import { memoryLabels } from "./memoryLabels.mjs";

const DAY = 86400000;
/** px per day and the bounds of one step between neighbouring dots: long empty stretches are compressed, crowded ones spread. */
const ZOOM = { day: 36, week: 10, month: 3 };
const GAP_MIN = 84, GAP_MAX = 320, PAD_L = 132, PAD_R = 96, LABEL_CHARS = 18, DOT_MIN = 8, DOT_MAX = 20;

/** When a memory happened: the event time it states, else when it was observed, else when it was saved. */
export const memoryWhen = (m) => {
  const t = Date.parse(m?.occurred?.from || m?.memory_context?.observed_at || m?.created_at || "");
  return Number.isFinite(t) ? t : 0;
};

/** The local day, Monday-started week or month a time falls in, as its start time. */
function bucketStart(t, zoom) {
  const d = new Date(t);
  if (zoom === "month") return new Date(d.getFullYear(), d.getMonth(), 1).getTime();
  const day = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  if (zoom === "week") day.setDate(day.getDate() - ((day.getDay() + 6) % 7));
  return day.getTime();
}

const clip = (s, n = LABEL_CHARS) => { const c = [...String(s || "").trim()]; return c.length > n ? `${c.slice(0, n - 1).join("")}…` : c.join(""); };

/**
 * Places memories on one horizontal line, oldest left. Memories of the same day (week, month at that zoom) share one dot.
 * use(m): how often it showed in answers; hollow(m): unused or expired; review(m): waiting on a person.
 * @returns {{width: number, dots: {key: string, at: number, items: any[], x: number, size: number, side: "above"|"below", hollow: boolean, review: boolean}[]}}
 */
export function timelineLayout(items, { zoom = "day", use = () => 0, hollow = () => false, review = () => false } = {}) {
  const per = ZOOM[zoom] || ZOOM.day, by = new Map();
  for (const m of items) { const at = bucketStart(memoryWhen(m), zoom); const b = by.get(at); if (b) b.push(m); else by.set(at, [m]); }
  const buckets = [...by].sort((a, b) => a[0] - b[0]);
  const weight = buckets.map(([, ms]) => ms.reduce((s, m) => s + Math.max(0, Number(use(m)) || 0), 0)), top = Math.max(1, ...weight);
  let x = PAD_L;
  const dots = buckets.map(([at, ms], i) => {
    if (i) x += Math.min(GAP_MAX, Math.max(GAP_MIN, ((at - buckets[i - 1][0]) / DAY) * per));
    return { key: String(at), at, items: ms, x, size: Math.round(DOT_MIN + (DOT_MAX - DOT_MIN) * Math.sqrt(weight[i] / top)),
      // newest gets its label above, then they alternate leftwards, so the dot at the right end never changes side as older pages load
      side: /** @type {"above"|"below"} */ ((buckets.length - 1 - i) % 2 ? "below" : "above"), hollow: ms.every(hollow), review: ms.some(review) };
  });
  return { width: x + PAD_R, dots };
}

/**
 * The timeline strip. deps: el, raw, button, kindLabel, memStat, idle, usageDone, maintenance, selected, onPick(id),
 * more (null, or {label-less} () => Promise: loads the next older page). view: { zoom, fromRight } kept by the caller across redraws.
 */
export function timelineView(items, deps, view) {
  const { el, raw } = deps, now = Date.now();
  const use = (m) => deps.memStat(m.id)?.used || 0;
  const hollow = (m) => (deps.usageDone && deps.idle(m)) || (Date.parse(m.expires_at || "") < now) || m.status === "expired";
  const review = (m) => memoryLabels(m, { maintenance: deps.maintenance }).some((l) => l.text === "정정 검토 중" || l.text === "임시 · 승인 전");
  const lay = timelineLayout(items, { zoom: view.zoom, use, hollow, review });
  const loc = globalThis.ArkismI18n?.locale?.() || "ko-KR";
  const fmt = new Intl.DateTimeFormat(loc, view.zoom === "month" ? { year: "numeric", month: "numeric" } : { month: "numeric", day: "numeric" });
  const long = new Intl.DateTimeFormat(loc, { year: "numeric", month: "long", day: "numeric" });
  const strip = el("div", "mem-tl"), track = el("div", "mem-tl-track");
  strip.setAttribute("role", "group"); strip.setAttribute("aria-label", "기억 타임라인");
  track.style.width = `${lay.width}px`;
  const line = el("i", "mem-tl-line"); line.style.left = `${PAD_L - 28}px`; line.style.width = `${lay.width - PAD_L - PAD_R + 56}px`;
  track.append(line);
  if (deps.more) {
    const b = el("button", "mem-tl-more", "더 이전 불러오기"); b.type = "button";
    b.addEventListener("click", () => { b.disabled = true; void deps.more(); });
    track.append(b);
  }
  let pop = null;
  const closePop = (back) => { if (!pop) return; pop.remove(); pop = null; back?.focus(); };
  const buttons = lay.dots.map((d) => {
    const one = d.items.length === 1, m = d.items[0];
    const b = el("button", `mem-tl-item ${d.side}${d.hollow ? " hollow" : ""}${d.review ? " review" : ""}${one ? "" : " group"}`);
    b.type = "button"; b.style.left = `${d.x}px`; b.style.setProperty("--dot", `${d.size}px`);
    if (one) b.dataset.id = m.id;
    if (d.items.some((x) => x.id === deps.selected)) b.classList.add("selected");
    const day = fmt.format(d.at), text = one ? m.text : `기억 ${d.items.length}개`;
    b.setAttribute("aria-label", `${long.format(d.at)} · ${one ? clip(m.text, 60) : text}`);
    const label = el("span", "mem-tl-label"), title = el("span", "mem-tl-title", one ? clip(m.text) : text);
    if (one) raw(title);
    label.append(title, raw(el("time", "mem-tl-date", day)));
    label.lastChild.setAttribute("datetime", new Date(d.at).toISOString().slice(0, 10));
    b.append(el("i", "mem-tl-tick"), el("i", "mem-tl-dot"), label);
    b.addEventListener("click", () => {
      if (one) { closePop(); deps.onPick(m.id); return; }
      const again = pop?.dataset.key === d.key; closePop(); if (again) return;
      pop = el("div", `mem-tl-pop ${d.side === "above" ? "below" : "above"}`); pop.dataset.key = d.key; pop.style.left = `${Math.min(Math.max(d.x, 136), lay.width - 136)}px`; // 16rem wide, kept inside the track
      pop.setAttribute("role", "dialog"); pop.setAttribute("aria-label", long.format(d.at));
      pop.append(raw(el("p", "mem-tl-pop-day", long.format(d.at))));
      for (const x of d.items) {
        const r = el("button", `mem-tl-pop-row${x.id === deps.selected ? " selected" : ""}`); r.type = "button"; r.dataset.id = x.id;
        r.append(raw(el("span", "", clip(x.text, 40))), el("small", "", deps.kindLabel(x.kind)));
        r.addEventListener("click", () => { closePop(); deps.onPick(x.id); });
        pop.append(r);
      }
      pop.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closePop(b); } });
      track.append(pop); pop.querySelector("button")?.focus();
    });
    return b;
  });
  // one tab stop: the selected dot, else the newest; arrows walk the dots
  const picked = buttons.findIndex((b) => b.classList.contains("selected")), start = picked < 0 ? buttons.length - 1 : picked;
  buttons.forEach((b, i) => { b.tabIndex = i === start ? 0 : -1; });
  track.addEventListener("keydown", (e) => {
    const i = buttons.indexOf(/** @type {any} */ (e.target)); if (i < 0) return;
    const to = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: buttons.length - 1 }[e.key];
    if (to === undefined || !buttons[to]) return;
    e.preventDefault(); buttons[i].tabIndex = -1; buttons[to].tabIndex = 0; buttons[to].focus(); buttons[to].scrollIntoView?.({ block: "nearest", inline: "nearest" });
  });
  track.append(...buttons);
  strip.append(track);
  strip.addEventListener("pointerdown", (e) => { if (pop && !pop.contains(/** @type {any} */ (e.target))) closePop(); });
  strip.addEventListener("scroll", () => { view.fromRight = lay.width - strip.scrollLeft; });
  // newest at the right: kept as the distance from the right end, so loading older pages on the left never moves the view
  strip.place = () => { strip.scrollLeft = Math.max(0, lay.width - (view.fromRight || 0)); };
  return Object.assign(strip, { layout: lay });
}

/**
 * 그래프: the memory map renderer (memory-bubbles.js) on the page, one bubble per loaded memory, grouped by kind.
 * edges: [{from, to, type}] from GET /v1/memory-graph, or []. deps: kindLabel, selected, onPick(id).
 * Returns the host element; host.destroy() stops the map when the page redraws.
 */
export function graphView(items, edges, deps) {
  const Bubbles = window.ArkismMemoryBubbles, colors = Bubbles.KIND_COLOR || {};
  const host = deps.el("div", "mem-graph");
  const byId = new Map(items.map((m) => [m.id, m]));
  const map = Bubbles.create({
    tabs: [["kind", "종류별"], ["all", "전체"]],
    groupOf: (mode, p) => mode === "kind" ? { key: p.kind || "_", name: deps.kindLabel(p.kind), color: colors[p.kind] || "#8a8f98" } : { key: "all", name: "기억", color: "#8a8f98" },
    tip: (id) => { const m = byId.get(id); return m ? { text: clip(m.text, 40), meta: deps.kindLabel(m.kind) } : null; },
    card: (id) => { const m = byId.get(id); return m ? { title: clip(m.text, 28), place: deps.kindLabel(m.kind), meta: "" } : null; },
    onSelect: (id) => deps.onPick(id),
    onClear: () => {},
  });
  map.setItems(items.map((m) => ({ id: m.id, v: deps.memStat?.(m.id)?.used || 1, color: colors[m.kind] || "#8a8f98", reach: "", kind: m.kind || "" })));
  map.setEdges(edges.filter((e) => e.type !== "similar" && byId.has(e.from) && byId.has(e.to)).map((e) => ({ from: e.from, to: e.to, t: e.type === "human_link" ? "supports" : e.type })));
  map.setSelected(deps.selected);
  host.append(map.el);
  return Object.assign(host, { destroy: () => map.destroy(), select: (id) => map.setSelected(id) });
}
