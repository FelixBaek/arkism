// @ts-check
// 기억 › 타임라인: the memories the list already loaded (catalogQuery "memories", newest first) under a sticky month
// heading and a day strip (one bar per day, its height the day's count; a bar jumps to its day), then every loaded day as
// a group of rows. The strip covers only days the loaded pages fully cover, so a bar never reads empty for a day that is
// simply not loaded yet; ‹ (이전 달) and scrolling to the bottom load older pages. Search, kind tabs and 자세히 apply as in
// the list; a row opens the same right detail panel.
import { memoryLabels, labelLine, rowLabels } from "./memoryLabels.mjs";
import { briefRail } from "./memoryTimeline.mjs";

const DAY = 86400000;

/** When a memory happened: the event time it states, else when it was observed, else when it was saved. */
export const memoryWhen = (m) => {
  const t = Date.parse(m?.occurred?.from || m?.memory_context?.observed_at || m?.created_at || "");
  return Number.isFinite(t) ? t : 0;
};

/** The start of the local day a time falls in. */
export const dayStart = (t) => { const d = new Date(t); return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(); };
const monthStart = (t, delta = 0) => { const d = new Date(t); return new Date(d.getFullYear(), d.getMonth() + delta, 1).getTime(); };
const nextDay = (t) => dayStart(t + DAY * 1.5); // noon-safe across a daylight-saving change

/** Memories grouped by local day, newest day first and newest first inside each. */
export function timelineGroups(items) {
  const by = new Map();
  for (const m of [...items].sort((a, b) => memoryWhen(b) - memoryWhen(a))) {
    const at = dayStart(memoryWhen(m)), g = by.get(at);
    if (g) g.push(m); else by.set(at, [m]);
  }
  return [...by].sort((a, b) => b[0] - a[0]).map(([at, ms]) => ({ at, items: ms }));
}

const loc = () => globalThis.ArkismI18n?.locale?.() || "ko-KR";
const fmt = (o) => new Intl.DateTimeFormat(loc(), o);
const kindColor = (k) => globalThis.ArkismMemoryBubbles?.KIND_COLOR?.[k === "semantic" ? "fact" : k] || "";

/** One memory: its text, then a kind dot · kind · 답에 보임 N번, then at most one label (정정 검토 중 …). */
function memRow(m, ctx, deps) {
  const { el, raw } = deps;
  const b = el("button", `mem-brief-row${ctx.muted(m) ? " muted" : ""}${m.id === deps.selected ? " selected" : ""}`); b.type = "button"; b.dataset.id = m.id;
  const meta = el("span", "mem-brief-meta"), dot = el("i", "tl-kind");
  dot.setAttribute("aria-hidden", "true");
  const c = kindColor(m.kind); if (c) dot.style.setProperty("--k", c);
  meta.append(dot, el("span", "", deps.kindLabel(m.kind)), " · ", el("span", "", `답에 보임 ${deps.memStat(m.id)?.used || 0}번`));
  b.append(raw(el("span", "mem-brief-text", m.text)), meta);
  const label = labelLine(rowLabels(memoryLabels(m, { maintenance: deps.maintenance })), { el });
  if (label) b.append(label);
  b.addEventListener("click", () => deps.onPick(m.id));
  return b;
}

/** The day strip: one bar per day from `first` to today. Bars of days with memories are one tab stop, walked by ← → Home End. */
function dayStrip(first, today, byDay, anchors, deps) {
  const { el, raw } = deps, strip = el("div", "tlc-strip"), ticks = el("div", "tlc-ticks"), bars = new Map();
  strip.setAttribute("role", "group"); strip.setAttribute("aria-label", "날짜로 이동");
  const days = []; for (let t = first; t <= today; t = nextDay(t)) days.push(t);
  const max = Math.max(1, ...days.map((t) => byDay.get(t)?.length || 0));
  for (const [i, t] of days.entries()) {
    const n = byDay.get(t)?.length || 0, d = new Date(t);
    const bar = el("button", `tlc-bar${n ? "" : " empty"}${t === today ? " today" : ""}`); bar.type = "button";
    bar.style.setProperty("--h", String(n ? 0.25 + 0.75 * n / max : 0));
    bar.setAttribute("aria-label", `${fmt({ month: "long", day: "numeric", weekday: "short" }).format(d)} · ${n}`); bar.setAttribute("data-no-i18n", "");
    bar.title = bar.getAttribute("aria-label");
    bar.tabIndex = -1;
    if (n) bar.addEventListener("click", () => jump(t, anchors, true));
    else bar.disabled = true;
    bar.append(el("i"));
    strip.append(bar); bars.set(t, bar);
    // a month label at each 1st, and at the start when it is early enough in its month not to crowd the next label
    if (d.getDate() === 1 || (i === 0 && d.getDate() < 24)) { const tk = raw(el("span", "tlc-tick", fmt({ month: "short" }).format(d))); tk.style.setProperty("--x", String(i / days.length)); ticks.append(tk); }
  }
  const live = [...bars.values()].filter((b) => !b.disabled);
  if (live.length) live[live.length - 1].tabIndex = 0;
  strip.addEventListener("keydown", (e) => {
    const i = live.indexOf(/** @type {any} */ (e.target)), next = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: live.length - 1 }[e.key];
    if (i < 0 || next === undefined || !live[next]) return;
    e.preventDefault(); live[i].tabIndex = -1; live[next].tabIndex = 0; live[next].focus();
  });
  return { strip, ticks, bars };
}

/** Scroll a day into view; focusRow also moves focus to its first memory (a click or Enter on a bar). */
function jump(t, anchors, focusRow = false, behavior = globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth") {
  const a = anchors.get(t); if (!a) return;
  a.scrollIntoView?.({ block: "start", behavior: /** @type {ScrollBehavior} */ (behavior) });
  if (focusRow) a.querySelector(".mem-brief-row")?.focus({ preventScroll: true });
}

/**
 * The timeline. deps: el, raw, kindLabel, memStat, idle, usageDone, maintenance, selected, onPick(id),
 * covered (the oldest day the loaded pages fully cover, or null when every page is loaded),
 * more (null, or () => Promise loading the next older page), loadUntil (null, or (t) => Promise loading pages back to day t).
 * view: { io, onScroll, month, jump } kept by the caller across redraws.
 */
export function pageTimeline(items, deps, view) {
  const { el, raw } = deps, now = Date.now(), today = dayStart(now), groups = timelineGroups(items);
  const byDay = new Map(groups.map((g) => [g.at, g.items]));
  const ctx = { muted: (m) => (deps.usageDone && deps.idle(m)) || (Date.parse(m.expires_at || "") < now) || m.status === "expired" };
  const box = el("div", "mem-brief mem-page-tl"), rows = [], anchors = new Map();
  const list = briefRail(() => rows, deps, "기억 타임라인");
  for (const g of groups) {
    const d = new Date(g.at), li = el("li", "tlc-day"), h = el("h3", "tlc-dayhead");
    li.dataset.at = String(g.at);
    h.append(raw(el("span", "tlc-date", fmt(d.getFullYear() === new Date(now).getFullYear() ? { month: "long", day: "numeric" } : { year: "numeric", month: "long", day: "numeric" }).format(d))),
      g.at === today ? el("span", "tlc-wd", "오늘") : raw(el("span", "tlc-wd", fmt({ weekday: "short" }).format(d))), el("span", "tlc-n", `${g.items.length}개`));
    li.append(h);
    for (const m of g.items) { const b = memRow(m, ctx, deps); rows.push(b); li.append(b); }
    list.append(li); anchors.set(g.at, li);
  }
  // the strip starts where the loaded pages stop being complete; with every page loaded, at the oldest memory
  const first = Math.min(today, deps.covered ?? groups.at(-1)?.at ?? today);
  const { strip, ticks, bars } = dayStrip(first, today, byDay, anchors, deps);
  const head = el("div", "tlc-head"), title = el("div", "tlc-title"), month = raw(el("span", "tlc-month")), sub = el("span", "tlc-sub");
  const arrow = (label, delta) => {
    const b = el("button", `tlc-arrow ${delta < 0 ? "prev" : "next"}`); b.type = "button"; b.setAttribute("aria-label", label); b.title = label;
    b.append(el("i", "tlc-chev")); b.addEventListener("click", () => void toMonth(delta, b));
    return b;
  };
  const prev = arrow("이전 달", -1), next = arrow("다음 달", 1);
  title.append(prev, month, next, sub);
  head.append(title, strip, ticks);
  box.append(head, list);
  const setMonth = (t) => {
    view.month = monthStart(t);
    month.textContent = fmt({ year: "numeric", month: "long" }).format(view.month);
    sub.textContent = `기억 ${groups.filter((g) => monthStart(g.at) === view.month).reduce((a, g) => a + g.items.length, 0)}개`;
    next.disabled = view.month >= monthStart(today);
    prev.disabled = !deps.loadUntil && !groups.some((g) => g.at < view.month);
  };
  // ‹ ›: the newest day of the month before (after) the one in sight; an older month not loaded yet is fetched first
  async function toMonth(delta, b) {
    const from = monthStart(view.month ?? today, delta), to = monthStart(from, 1);
    if (delta < 0 && deps.loadUntil && (deps.covered ?? 0) > from) { b.disabled = true; view.jump = from; await deps.loadUntil(from); return; }
    const g = groups.find((x) => x.at < to && (delta < 0 || x.at >= from)) || groups.findLast((x) => x.at >= from);
    if (g) { setMonth(g.at); jump(g.at, anchors); }
  }
  setMonth(view.jump ?? view.month ?? groups[0]?.at ?? today);
  // a month asked for by ‹ is reached once the loading stops short of it no longer
  if (view.jump !== undefined && (deps.covered ?? 0) <= view.jump) {
    const to = monthStart(view.jump, 1), g = groups.find((x) => x.at < to);
    view.jump = undefined;
    if (g) queueMicrotask(() => { jump(g.at, anchors, false, "auto"); });
  }
  // the days in sight below the sticky heading (a day mostly hidden under it does not count) light their bars; the topmost names the month (read on scroll, at most once a frame)
  if (view.onScroll) globalThis.removeEventListener?.("scroll", view.onScroll, true);
  let queued = false;
  view.onScroll = () => {
    if (queued || !box.isConnected) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      const top = head.getBoundingClientRect().bottom, bottom = globalThis.innerHeight, seen = [];
      for (const [t, li] of anchors) { const r = li.getBoundingClientRect(); if (r.bottom > top + 24 && r.top < bottom) seen.push(t); }
      for (const [t, bar] of bars) bar.classList.toggle("seen", seen.includes(t));
      if (seen.length) setMonth(Math.max(...seen));
    });
  };
  if (globalThis.requestAnimationFrame) globalThis.addEventListener?.("scroll", view.onScroll, { capture: true, passive: true });
  view.io?.disconnect(); view.io = null;
  if (deps.more) {
    const b = el("button", "link-btn mem-brief-older", "더 이전 불러오기"); b.type = "button";
    const load = () => { if (b.disabled) return; b.disabled = true; view.io?.disconnect(); void deps.more(); };
    b.addEventListener("click", load);
    box.append(b);
    // scrolled to the bottom: the next older page loads by itself; the button stays as the fallback
    if (globalThis.IntersectionObserver) {
      view.io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) load(); }, { rootMargin: "0px 0px 200px 0px" });
      view.io.observe(b);
    }
  }
  return box;
}
