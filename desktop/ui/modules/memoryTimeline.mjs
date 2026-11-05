// @ts-check
// 변경 이력 as a vertical date briefing: newest on top, one date heading and a two-line brief per revision, a thin line
// with a dot each down the left. Each revision held from valid_from to valid_to; a correction waiting for a person sits on top.

/** When a record started to hold: the stated valid_from, else when it took effect, else when it was saved. */
const from = (r) => r?.memory_context?.valid_from || r?.effective_at || r?.created_at || r?.updated_at || "";
const time = (v) => { const t = Date.parse(v || ""); return Number.isFinite(t) ? t : 0; };
/** A date heading with the year ("2026. 10. 13." in Korean), in the UI language. */
const day = (v) => time(v) ? new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale?.() || "ko-KR", { year: "numeric", month: "numeric", day: "numeric" }).format(time(v)) : "";

/**
 * The timeline rows, newest first. pending: the correction candidate waiting on this memory, if any.
 * @returns {{kind: string, text: string, from: string, to: string, approver: string, evidence: number}[]}
 */
export function timelineEntries(m, lineage, pending = null) {
  const links = Array.isArray(lineage?.links) ? lineage.links : [];
  const KIND = { supersedes: "previous", superseded_by: "next", conflict: "conflict" };
  const row = (kind, r) => ({ kind, text: r?.text || "", from: from(r), to: r?.expires_at || "",
    approver: r?.approved_by || r?.review?.by || "", evidence: Array.isArray(r?.sources) ? r.sources.length : 0 });
  const rows = [row("current", m), ...links.filter((l) => l?.record && KIND[l.relation]).map((l) => row(KIND[l.relation], l.record))];
  rows.sort((a, b) => time(b.from) - time(a.from));
  // A revision without its own end held until the next newer revision began; the newest holds until now.
  const chain = rows.filter((r) => r.kind !== "conflict");
  chain.forEach((r, i) => { if (!r.to && i > 0) r.to = chain[i - 1].from; });
  if (!m?.expires_at && m?.valid_to_proposal?.at) rows.find((r) => r.kind === "current").to = m.valid_to_proposal.at;
  if (pending) rows.unshift({ ...row("pending", pending), to: "" });
  return rows;
}

let ids = 0;

/** What an opened entry adds under its brief: 기간, who approved it, how many sources back it. deps: el, raw. */
function entryMore(r, { el, raw }) {
  const meta = el("p", "mem-brief-meta"), parts = [];
  if (r.kind === "pending") parts.push(el("span", "", "사람이 고르기 전까지 지금 내용을 계속 써요"));
  else {
    // one span per piece, so each is translated on its own and dates and the approver id stay as written
    const span = el("span");
    span.append(el("span", "", "기간"), " ", raw(el("span", "", day(r.from) || "—")), " → ", r.to ? raw(el("span", "", day(r.to))) : el("span", "", "지금"));
    parts.push(span);
    if (r.approver) { const a = el("span"); a.append(el("span", "", "승인"), " ", raw(el("span", "", r.approver))); parts.push(a); }
  }
  if (r.evidence) parts.push(el("span", "", `근거 ${r.evidence}개`));
  parts.forEach((p, i) => meta.append(...(i ? [" · ", p] : [p])));
  return meta;
}

/**
 * 변경 이력 as a briefing list: each entry is a button (date, then the text clamped to two lines) that opens in place,
 * one at a time, to show 기간, approver, evidence and — for an older version — 이전으로 되돌리기. All closed at first.
 * More than `limit` versions: the newest `limit` and 이전 N개 더 보기. revert(row, button) or null. deps: el, raw.
 */
export function timelineView(rows, deps, { revert = null, limit = 5 } = {}) {
  const { el, raw } = deps, box = el("div", "mem-brief"), list = el("ol", "mem-brief-list");
  const shown = rows.filter((r) => r.kind !== "conflict");
  let versions = 0;
  const items = shown.map((r) => {
    const id = `mem-brief-${++ids}`, li = el("li", `mem-brief-item ${r.kind}`);
    if (r.kind !== "pending" && ++versions > limit) li.hidden = true;
    const head = el("button", "mem-brief-head"); head.type = "button";
    head.setAttribute("aria-expanded", "false"); head.setAttribute("aria-controls", id);
    const date = el("span", "mem-brief-date");
    date.append(raw(el("span", "", day(r.from) || "—")));
    const tag = r.kind === "current" ? "지금" : r.kind === "pending" ? "검토 중" : "";
    if (tag) { const t = el("span", "mem-brief-tag"); t.append(" · ", el("span", "", tag)); date.append(t); }
    head.append(date, raw(el("span", "mem-brief-text", r.text)));
    const more = el("div", "mem-brief-more"); more.id = id; more.hidden = true;
    more.append(entryMore(r, deps));
    if (revert && r.kind === "previous" && r.text) {
      const back = el("button", "link-btn d-revert", "이전으로 되돌리기"); back.type = "button";
      back.addEventListener("click", () => revert(r, back));
      more.append(back);
    }
    head.addEventListener("click", () => {
      const open = more.hidden;
      for (const it of items) { it.head.setAttribute("aria-expanded", "false"); it.more.hidden = true; }
      if (open) { head.setAttribute("aria-expanded", "true"); more.hidden = false; }
    });
    li.append(head, more); list.append(li);
    return { li, head, more };
  });
  // ↑ ↓ Home End move between the visible entries
  list.addEventListener("keydown", (e) => {
    const heads = items.filter((it) => !it.li.hidden).map((it) => it.head), i = heads.indexOf(e.target);
    const next = { ArrowUp: i - 1, ArrowDown: i + 1, Home: 0, End: heads.length - 1 }[e.key];
    if (i < 0 || next === undefined || !heads[next]) return;
    e.preventDefault(); heads[next].focus?.();
  });
  box.append(list);
  const hidden = items.filter((it) => it.li.hidden);
  if (hidden.length) {
    const older = el("button", "link-btn mem-brief-older", `이전 ${hidden.length}개 더 보기`); older.type = "button";
    older.addEventListener("click", () => { for (const it of hidden) it.li.hidden = false; older.hidden = true; hidden[0].head.focus?.(); });
    box.append(older);
  }
  return box;
}

/** The last moment of a local YYYY-MM-DD day, as an ISO time. */
export const endOfDay = (day) => new Date(`${day}T23:59:59.999`).toISOString();
/** A /v1/memories/at page as a list page (total unknown). revision 0: a row opened from it is re-read, so actions use the current revision. */
export function validPage(result) {
  if (!Array.isArray(result?.items)) throw Error("catalog_page_invalid");
  return { ...result, items: result.items.map((h) => ({ ...h, status: "active", revision: 0, created_at: h.effective_at || h.observed_at })), total: null, metadata: { total: null, facets: {} } };
}

/**
 * 시점 next to a memory search: a date (max today) and 지금 to clear it. onPick(day, asOf): day "" = now;
 * asOf is when it was picked, fixed so the server cursor stays bound to it. deps: el, button.
 */
export function whenPick(day, onPick, { el, button }) {
  const box = el("label", `mem-when-pick${day ? " on" : ""}`);
  const d = el("input"); d.type = "date"; d.setAttribute("aria-label", "시점"); d.title = "이 날 맞던 기억만 보기. 비우면 지금";
  d.max = new Date(Date.now() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 10); d.value = day || "";
  // shortcut: as_of 5s behind for clock skew (the server refuses a future as_of); a memory saved in those 5s is missed until the day is picked again
  d.addEventListener("change", () => onPick(d.value, d.value ? new Date(Date.now() - 5000).toISOString() : ""));
  box.append(el("span", "", "시점"), d);
  if (day) box.append(button("지금", "ghost small", null, () => onPick("", "")));
  return box;
}
