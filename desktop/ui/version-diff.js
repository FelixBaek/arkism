// @ts-check
// 버전 기록 › 바뀐 점 보기: two saved crew definitions as a diff — "- old" in red, "+ new" in green, one block per changed field.
// Long text (instructions, goals) is diffed line by line so only the lines that changed show up.
(() => {
  const LABELS = {
    name: "이름", handle: "@이름", role: "소개", category: "카테고리", instructions: "지시", model: "기본 모델", fallback_model: "예비 모델",
    image_model: "이미지 모델", effort: "생각 깊이", workflow: "일하는 순서", skills: "스킬", tools: "도구", agents: "부르는 곳", memory: "기억 종류",
    pinned: "고정한 것", budget: "예산", avatar: "아바타", cover: "카드 사진", mode: "회의 방식", ending: "끝나는 조건", members: "전문가",
    context: "팀 자료", templates: "회의 템플릿", discord_mirror: "디스코드에도 올리기", goal: "목표", trigger: "언제 실행", enabled: "켜기",
    limits: "멈추는 조건", notify: "진행 알림", graph: "단계", temperature: "창의성", max_tokens: "답변 길이", output: "출력 형식", file_edits: "파일 고치기",
  };
  const SKIP = new Set(["revision", "updated_at", "created_at", "id", "type", "copy_of"]);
  const text = (v) => (v == null || v === "" ? "" : typeof v === "string" ? v : Array.isArray(v) && v.every((x) => typeof x !== "object") ? v.join(", ") : JSON.stringify(v, null, 1).replace(/\n\s*/g, " "));
  // Plain LCS over lines; definitions are small, so O(n·m) is fine.
  function lineDiff(x, y) {
    const a = x.split("\n"), b = y.split("\n"), n = a.length, m = b.length;
    const t = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) t[i][j] = a[i] === b[j] ? t[i + 1][j + 1] + 1 : Math.max(t[i + 1][j], t[i][j + 1]);
    const out = [];
    let i = 0, j = 0;
    while (i < n && j < m) {
      if (a[i] === b[j]) { i++; j++; } else if (t[i + 1][j] >= t[i][j + 1]) out.push(["-", a[i++]]); else out.push(["+", b[j++]]);
    }
    while (i < n) out.push(["-", a[i++]]);
    while (j < m) out.push(["+", b[j++]]);
    return out.filter(([, s]) => s.trim());
  }
  // a = what is there now, b = the version being looked at; returns [{ label, rows: [["-"|"+", text]] }].
  function blocks(a, b, labels = {}) {
    const out = [];
    for (const k of new Set([...Object.keys(a || {}), ...Object.keys(b || {})])) {
      if (SKIP.has(k)) continue;
      const x = text(a?.[k]), y = text(b?.[k]);
      if (x === y) continue;
      const long = typeof (a?.[k] ?? b?.[k]) === "string" && (x.includes("\n") || y.includes("\n"));
      const rows = long ? lineDiff(x, y) : [...(x ? [["-", x]] : []), ...(y ? [["+", y]] : [])];
      if (rows.length) out.push({ label: labels[k] || LABELS[k] || k, rows });
    }
    return out;
  }
  function render(el, a, b, title, labels) {
    const box = el("div", "vdiff");
    if (title) box.append(el("b", "vdiff-title", title));
    const list = blocks(a, b, labels);
    if (!list.length) box.append(el("span", "vdiff-none", "바뀐 점을 찾지 못했어요."));
    for (const blk of list) {
      box.append(el("span", "vdiff-label", blk.label));
      for (const [s, t] of blk.rows) { const r = el("code", `vdiff-row ${s === "-" ? "del" : "add"}`, `${s} ${t}`); r.setAttribute("aria-label", `${s === "-" ? "지금" : "바꾸면"}: ${t}`); box.append(r); }
    }
    return box;
  }
  window.ArkismVersionDiff = { blocks, render, lineDiff };
})();
