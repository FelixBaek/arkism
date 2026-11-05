// @ts-check
// The labels a memory or candidate carries, drawn as accent-coloured text on list rows, the 새 기억 확인 cards
// and recall results: the extraction labels (capture_labels.go tags and entity_relations), a correction waiting
// on it, and an open path_missing proposal (maintenance.go). Labels only describe; nothing here decides.

const DECISION = { proposed: "결정: 제안", confirmed: "결정: 확정", dropped: "결정: 취소" };

/**
 * @param {any} item a memory, candidate or recall hit
 * @param {{maintenance?: any[], provisional?: boolean}} [opts] open maintenance proposals; provisional: a pending candidate shown in recall
 * @returns {{text: string, title?: string}[]}
 */
export function memoryLabels(item, { maintenance = [], provisional = false } = {}) {
  const tags = Array.isArray(item?.tags) ? item.tags : [], out = [];
  if (provisional) out.push({ text: "임시 · 승인 전" });
  if (tags.includes("review:pending_correction") || item?.pending_correction || item?.status === "active" && item?.conflict_id) out.push({ text: "정정 검토 중" });
  if (tags.includes("temporary")) out.push({ text: "이번만 · 7일 뒤 만료" });
  if (tags.includes("rule:always")) out.push({ text: "규칙: 항상" });
  else if (tags.includes("rule:never")) out.push({ text: "규칙: 하지 않기" });
  const status = tags.find((t) => typeof t === "string" && t.startsWith("decision_status:"))?.slice("decision_status:".length);
  if (status && Object.hasOwn(DECISION, status)) out.push({ text: DECISION[status] });
  const rel = Array.isArray(item?.entity_relations) ? item.entity_relations.filter((r) => r?.source && r?.target) : [];
  if (rel.length) out.push({ text: `관계 ${rel.length}개`, title: rel.map((r) => `${r.source} → ${r.relation} → ${r.target}`).join("\n") });
  if (item?.id && Array.isArray(maintenance) && maintenance.some((p) => p?.maintenance?.type === "invalidation" && p.maintenance.reason === "path_missing" && p.maintenance.memory?.id === item.id)) out.push({ text: "파일 경로 없음" });
  return out;
}

// A list row carries at most one label, the one that most changes what the row means; the rest wait in the detail.
const ROW = ["정정 검토 중", "임시 · 승인 전", "이번만 · 7일 뒤 만료", "파일 경로 없음"];
/** The single label a list row shows (or none): 정정 검토 중 > 임시 · 승인 전 > 이번만 > 파일 경로 없음. */
export const rowLabels = (labels) => { const top = ROW.map((t) => labels.find((l) => l.text === t)).find(Boolean); return top ? [top] : []; };

/** One line of labels (<span class="mem-labels">), or null when there are none. deps: el. */
export function labelLine(labels, { el }) {
  if (!labels.length) return null;
  const line = el("span", "mem-labels");
  for (const l of labels) {
    const s = el("span", "mem-label", l.text);
    if (l.title) s.title = l.title; // names from the memory: no dictionary entry matches them, so they stay as written
    line.append(s);
  }
  return line;
}

/**
 * The pending candidates a recall answer carries apart from its items (ContextBundle.provisional), each with
 * the 임시 · 승인 전 label and the server's notice; null when there are none. Shown, never mixed into the items.
 */
export function provisionalBlock(answer, { el, raw }) {
  const rows = Array.isArray(answer?.provisional) ? answer.provisional.filter((h) => h?.text || h?.summary) : [];
  if (!rows.length) return null;
  const box = el("div", "mem-provisional");
  box.append(el("p", "meta", "승인 전 후보 · 확정된 기억이 아니에요"));
  const ul = el("ul", "inject-list");
  for (const h of rows) {
    const li = el("li");
    li.append(labelLine(memoryLabels(h, { provisional: true }), { el }), raw(el("span", "", h.text || h.summary)));
    ul.append(li);
  }
  box.append(ul);
  return box;
}
