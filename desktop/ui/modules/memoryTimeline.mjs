// @ts-check
// 변경 이력 as a dated vertical timeline: each revision from when it held (valid_from) to when it stopped
// (valid_to), who approved it and how many sources back it. A correction waiting for a person sits on top.

/** When a record started to hold: the stated valid_from, else when it took effect, else when it was saved. */
const from = (r) => r?.memory_context?.valid_from || r?.effective_at || r?.created_at || r?.updated_at || "";
const time = (v) => { const t = Date.parse(v || ""); return Number.isFinite(t) ? t : 0; };

/**
 * The timeline rows, newest first. pending: the correction candidate waiting on this memory, if any.
 * @returns {{kind: string, title: string, text: string, from: string, to: string, approver: string, evidence: number}[]}
 */
export function timelineEntries(m, lineage, pending = null) {
  const links = Array.isArray(lineage?.links) ? lineage.links : [];
  const row = (kind, title, r) => ({ kind, title, text: r?.text || "", from: from(r), to: r?.expires_at || "",
    approver: r?.approved_by || r?.review?.by || "", evidence: Array.isArray(r?.sources) ? r.sources.length : 0 });
  const rows = [row("current", "지금 내용", m)];
  for (const l of links) {
    if (!l?.record) continue;
    if (l.relation === "supersedes") rows.push(row("previous", "이전 내용", l.record));
    else if (l.relation === "superseded_by") rows.push(row("next", "새 내용으로 바뀜", l.record));
    else if (l.relation === "conflict") rows.push(row("conflict", "다른 기억과 부딪힘", l.record));
  }
  rows.sort((a, b) => time(b.from) - time(a.from));
  // A revision without its own end held until the next newer revision began; the newest holds until now.
  const chain = rows.filter((r) => r.kind !== "conflict");
  chain.forEach((r, i) => { if (!r.to && i > 0) r.to = chain[i - 1].from; });
  if (!m?.expires_at && m?.valid_to_proposal?.at) rows.find((r) => r.kind === "current").to = m.valid_to_proposal.at;
  if (pending) rows.unshift({ ...row("pending", "정정 검토 중", pending), to: "" });
  return rows;
}

/** Draws the rows as <ol class="mem-timeline">; deps: el, raw, shortDate. */
export function timelineList(rows, { el, raw, shortDate }) {
  const ol = el("ol", "mem-timeline");
  for (const r of rows) {
    const li = el("li", `mem-tl ${r.kind}`);
    const when = el("p", "mem-tl-when", r.kind === "pending" ? "승인 전" : `${r.from ? shortDate(r.from) : "날짜 모름"} → ${r.to ? shortDate(r.to) : "지금"}`);
    const head = el("p", "mem-tl-title", r.title);
    li.append(when, head);
    if (r.text) li.append(raw(el("p", "mem-tl-text", `“${r.text}”`)));
    // one span per piece, so each is translated on its own and the approver id stays as written
    const meta = el("p", "mem-tl-meta"), first = r.kind === "pending" || !!r.approver;
    if (r.kind === "pending") meta.append(el("span", "", "사람이 고르기 전까지 지금 내용을 계속 써요"));
    else if (r.approver) meta.append(el("span", "", "승인"), " ", raw(el("span", "", r.approver)));
    if (r.evidence) meta.append(...(first ? [" · "] : []), el("span", "", `근거 ${r.evidence}개`));
    if (first || r.evidence) li.append(meta);
    ol.append(li);
  }
  return ol;
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
