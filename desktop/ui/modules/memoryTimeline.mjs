// @ts-check
// 변경 이력 as a dated horizontal timeline: each revision from when it held (valid_from) to when it stopped
// (valid_to), who approved it and how many sources back it. A correction waiting for a person sits at the right end.

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
  if (pending) rows.unshift({ ...row("pending", "검토 중", pending), to: "" });
  return rows;
}

/**
 * Where each dot sits on the horizontal line: oldest left, newest right, a waiting correction last.
 * More than `limit` dots: only the newest `limit`, and `hidden` says how many are left out. A conflict is another
 * memory, not a version, so it has no dot. Grid lines are 1-based on 2 half-columns per dot (+1 leading column for
 * 이전 N개): dot i is centred on line 2i+2; its label spans one dot either side (lines 2i…2i+4), so labels on the same
 * side never share a column and alternate up/down.
 * @returns {{hidden: number, dots: {row: any, up: boolean, size: number, from: number, to: number}[]}}
 */
export function timelineLayout(rows, { limit = 5, all = false } = {}) {
  const dots = [...rows.filter((r) => r.kind !== "pending" && r.kind !== "conflict").reverse(), ...rows.filter((r) => r.kind === "pending")];
  const hidden = all ? 0 : Math.max(0, dots.length - limit), shown = dots.slice(hidden), n = shown.length;
  // dot size follows the evidence count (8–16px); the current version is always the largest
  const size = (r) => r.kind === "current" ? 18 : r.kind === "pending" ? 12 : Math.min(16, 8 + 2 * r.evidence);
  return { hidden, dots: shown.map((row, i) => ({ row, up: i % 2 === 0, size: size(row), from: Math.max(1, 2 * i), to: Math.min(2 * n + 1, 2 * i + 4) })) };
}

const LABEL = { current: "지금 내용", previous: "이전 내용", next: "새 내용으로 바뀜", pending: "검토 중" };

/** One version under the line: when it held, its text, who approved it and how many sources back it. deps: el, raw, shortDate. */
function versionBlock(r, { el, raw, shortDate }) {
  const box = el("div", `mem-tlh-version ${r.kind}`);
  box.append(el("p", "mem-tlh-when", r.kind === "pending" ? "승인 전" : `${r.from ? shortDate(r.from) : "날짜 모름"} → ${r.to ? shortDate(r.to) : "지금"}`));
  if (r.text) box.append(raw(el("p", "mem-tlh-text", `“${r.text}”`)));
  // one span per piece, so each is translated on its own and the approver id stays as written
  const meta = el("p", "mem-tlh-meta"), first = r.kind === "pending" || !!r.approver;
  if (r.kind === "pending") meta.append(el("span", "", "사람이 고르기 전까지 지금 내용을 계속 써요"));
  else if (r.approver) meta.append(el("span", "", "승인"), " ", raw(el("span", "", r.approver)));
  if (r.evidence) meta.append(...(first ? [" · "] : []), el("span", "", `근거 ${r.evidence}개`));
  if (first || r.evidence) box.append(meta);
  return box;
}

/**
 * 변경 이력 as one thin horizontal line: a dot per version (a button), labels alternating above and below, and the
 * picked version's text under it (default: the current one). revert(row, button): 이전으로 되돌리기 for a picked
 * older version, or null. deps: el, raw, shortDate.
 */
export function timelineView(rows, deps, { revert = null, limit = 5 } = {}) {
  const { el } = deps, box = el("div", "mem-tlh");
  let all = false, picked = rows.find((r) => r.kind === "current") || rows[0];
  const paint = (focus) => {
    const { hidden, dots } = timelineLayout(rows, { limit, all }), o = hidden ? 1 : 0, n = dots.length;
    if (!dots.some((d) => d.row === picked)) picked = dots.find((d) => d.row.kind === "current")?.row || dots.at(-1)?.row;
    const grid = el("div", "mem-tlh-grid");
    grid.style.gridTemplateColumns = `${o ? "auto " : ""}repeat(${2 * n}, minmax(0, 1fr))`;
    const at = (node, from, to, row) => { node.style.gridColumn = `${from + o} / ${to + o}`; node.style.gridRow = String(row); grid.append(node); return node; };
    if (hidden) {
      const more = el("button", "link-btn mem-tlh-more", `이전 ${hidden}개`); more.type = "button";
      more.addEventListener("click", () => { all = true; paint(true); });
      more.style.gridColumn = "1"; more.style.gridRow = "3"; grid.append(more);
    }
    if (n > 1) at(el("span", "mem-tlh-line"), 2, 2 * n, 3);
    const buttons = dots.map((d, i) => {
      const label = LABEL[d.row.kind] || d.row.title, date = d.row.from ? deps.shortDate(d.row.from) : "날짜 모름", on = d.row === picked;
      const dot = el("button", `mem-tlh-dot ${d.row.kind}${on ? " on" : ""}`); dot.type = "button";
      dot.style.setProperty?.("--d", `${d.size / 16}rem`);
      dot.setAttribute("aria-label", `${label} · ${date}`); dot.setAttribute("aria-pressed", String(on)); dot.tabIndex = on ? 0 : -1;
      dot.addEventListener("click", () => { picked = d.row; paint(true); });
      at(dot, 2 * i + 1, 2 * i + 3, 3);
      at(el("span", `mem-tlh-tick ${d.up ? "up" : "down"}`), 2 * i + 1, 2 * i + 3, d.up ? 2 : 4);
      const edge = i === 0 && n > 1 ? " start" : i === n - 1 && n > 1 ? " end" : "";
      const tag = el("p", `mem-tlh-label ${d.up ? "up" : "down"}${edge}${on ? " on" : ""}`);
      tag.append(el("span", "", label), el("small", "", date));
      tag.addEventListener("click", () => { picked = d.row; paint(true); });
      at(tag, d.from, d.to, d.up ? 1 : 5);
      return dot;
    });
    // ← → Home End move the pick along the line, like a radio group (one tab stop)
    grid.addEventListener("keydown", (e) => {
      const i = dots.findIndex((d) => d.row === picked), next = { ArrowLeft: i - 1, ArrowRight: i + 1, Home: 0, End: n - 1 }[e.key];
      if (next === undefined || !dots[next] || !buttons.includes(e.target)) return;
      e.preventDefault(); picked = dots[next].row; paint(true);
    });
    const version = versionBlock(picked, deps);
    if (revert && picked.kind === "previous" && picked.text) {
      const back = el("button", "link-btn d-revert", "이전으로 되돌리기"); back.type = "button";
      back.addEventListener("click", () => revert(picked, back));
      version.append(back);
    }
    box.replaceChildren(grid, version);
    if (focus) buttons[dots.findIndex((d) => d.row === picked)]?.focus?.();
  };
  paint(false);
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
