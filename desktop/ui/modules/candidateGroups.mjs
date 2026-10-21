// Group presentation is separate from review writes. Collapsed groups create no
// child rows; expanded groups show at most 50 candidates, all individually reachable.
export function candidateGroup(group, { el, button, row, request, query }) {
  if (group.items.length === 1) return row(group.items[0]);
  const box = el("details", "candidate-group"), head = el("summary", "");
  const title = el("strong", "", group.items[0].text); title.setAttribute("data-no-i18n", "");
  head.append(title, el("span", "meta", `비슷한 후보 ${group.items.length}개`));
  const evidence = el("p", "meta", "펼치면 서로 다른 원문 발화 수를 확인해요. 횟수는 승인이나 신뢰 점수가 아니에요.");
  evidence.setAttribute("aria-live", "polite");
  const children = el("div", "candidate-children"), controls = el("p", "meta");
  box.append(head, evidence, el("p", "meta", "각 문장의 조건과 예외를 확인하고 항목별로 결정해 주세요."), children, controls);
  let page = 0, loading = false;
  function paint() {
    const begin = page * 50, end = Math.min(begin + 50, group.items.length);
    children.replaceChildren(...group.items.slice(begin, end).map(row));
    controls.replaceChildren(el("span", "", `${group.items.length}개 중 ${begin + 1}–${end}개 표시`));
    const previous = button("이전 후보", "link-btn", null, () => { page--; paint(); controls.querySelector("button:not(:disabled)")?.focus(); });
    const next = button("다음 후보", "link-btn", null, () => { page++; paint(); controls.querySelector("button:not(:disabled)")?.focus(); });
    previous.disabled = page === 0; next.disabled = end === group.items.length;
    if (group.items.length > 50) controls.append(" · ", previous, " · ", next);
  }
  box.addEventListener("toggle", async () => {
    if (!box.open) { children.replaceChildren(); controls.replaceChildren(); return; }
    paint();
    if (loading) return;
    loading = true; evidence.textContent = "원문 근거를 확인하는 중…";
    const summaries = await readEvidence(group.items, { box, request, query });
    if (box.isConnected) evidence.textContent = window.ArkismMemoryManagement.evidence(summaries).label;
    loading = false;
  });
  return box;
}

async function readEvidence(items, { box, request, query }) {
  const selected = items.slice(0, 32), summaries = [];
  let next = 0;
  async function worker() {
    while (next < selected.length && box.open && box.isConnected) {
      const candidate = selected[next++];
      try { summaries.push(await request(`/v1/memory_candidates/${encodeURIComponent(candidate.id)}/evidence-summary?${query()}`)); }
      catch { summaries.push(null); }
    }
  }
  await Promise.all([worker(), worker()]);
  if (summaries.length < items.length) summaries.push(null);
  return summaries;
}
