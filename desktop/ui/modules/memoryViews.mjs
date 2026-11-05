// @ts-check
// 기억 › 타임라인: the memories the list already loaded (catalogQuery "memories") as a vertical date briefing, newest on
// top, on the same rail as 변경 이력 (memoryTimeline.mjs). Search, kind tabs and 자세히 apply as in the list; a row opens
// the same right detail panel.
import { memoryLabels, labelLine, rowLabels } from "./memoryLabels.mjs";
import { briefRail, briefStop, briefDate } from "./memoryTimeline.mjs";

const DAY = 86400000;

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

/** Memories grouped by local day (week, month), newest group first and newest first inside each. */
export function timelineGroups(items, zoom = "day") {
  const by = new Map();
  for (const m of [...items].sort((a, b) => memoryWhen(b) - memoryWhen(a))) {
    const at = bucketStart(memoryWhen(m), zoom), g = by.get(at);
    if (g) g.push(m); else by.set(at, [m]);
  }
  return [...by].sort((a, b) => b[0] - a[0]).map(([at, ms]) => ({ at, items: ms }));
}

/** A group's heading: the date with the year, and 오늘 · 어제 or the weekday for a day; a range for a week; the month. */
function heading(at, zoom, now) {
  const loc = globalThis.ArkismI18n?.locale?.() || "ko-KR", ymd = { year: "numeric", month: "numeric", day: "numeric" };
  if (zoom === "month") return { date: new Intl.DateTimeFormat(loc, { year: "numeric", month: "numeric" }).format(at), tag: "" };
  const f = new Intl.DateTimeFormat(loc, /** @type {any} */ (ymd));
  // the week's last day at noon, so a daylight-saving change never pulls it back a day
  if (zoom === "week") return { date: f.formatRange(at, at + 6 * DAY + DAY / 2), tag: "" };
  const today = bucketStart(now, "day"), tag = at === today ? "오늘" : at === bucketStart(today - DAY / 2, "day") ? "어제" : null;
  return { date: f.format(at), tag: tag ?? new Intl.DateTimeFormat(loc, { weekday: "short" }).format(at) };
}

/**
 * The timeline. deps: el, raw, kindLabel, memStat, idle, usageDone, maintenance, selected, onPick(id),
 * more (null, or () => Promise loading the next older page). view: { zoom, io } kept by the caller across redraws.
 */
export function pageTimeline(items, deps, view) {
  const { el, raw } = deps, now = Date.now(), zoom = view.zoom || "day";
  const muted = (m) => (deps.usageDone && deps.idle(m)) || (Date.parse(m.expires_at || "") < now) || m.status === "expired";
  const box = el("div", "mem-brief mem-page-tl"), rows = [];
  const list = briefRail(() => rows, deps, "기억 타임라인");
  for (const g of timelineGroups(items, zoom)) {
    const labels = g.items.map((m) => memoryLabels(m, { maintenance: deps.maintenance }));
    const review = labels.some((ls) => ls.some((l) => l.text === "정정 검토 중" || l.text === "임시 · 승인 전"));
    const stop = briefStop(el, review && "review", g.items.every(muted) && "muted"), { date, tag } = heading(g.at, zoom, now);
    const head = briefDate(date, tag && (tag === "오늘" || tag === "어제" ? tag : raw(el("span", "", tag))), deps, "h3");
    stop.append(head);
    g.items.forEach((m, i) => {
      const b = el("button", `mem-brief-row${muted(m) ? " muted" : ""}${m.id === deps.selected ? " selected" : ""}`); b.type = "button"; b.dataset.id = m.id;
      const meta = el("span", "mem-brief-meta");
      meta.append(el("span", "", deps.kindLabel(m.kind)), " · ", el("span", "", `답에 보임 ${deps.memStat(m.id)?.used || 0}번`));
      b.append(raw(el("span", "mem-brief-text", m.text)), meta);
      const label = labelLine(rowLabels(labels[i]), { el });
      if (label) b.append(label);
      b.addEventListener("click", () => deps.onPick(m.id));
      rows.push(b); stop.append(b);
    });
    list.append(stop);
  }
  box.append(list);
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
