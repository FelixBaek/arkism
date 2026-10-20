// What an AI proposal changes: [["=", same], ["-", removed], ["+", added]].
// Lines first; a changed line that keeps at least half of its shorter side's words shows word by word, otherwise whole old line → new line.
window.ArkismTextDiff = (() => {
  "use strict";
  // LCS over two token lists. shortcut: plain table; texts are capped at 4000 chars, so at worst a few million cells.
  function lcs(A, B) {
    const L = Array.from({ length: A.length + 1 }, () => new Uint16Array(B.length + 1));
    for (let i = A.length - 1; i >= 0; i--) for (let j = B.length - 1; j >= 0; j--) L[i][j] = A[i] === B[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
    const out = [];
    let i = 0, j = 0;
    while (i < A.length && j < B.length) {
      if (A[i] === B[j]) { out.push(["=", A[i]]); i++; j++; } else if (L[i + 1][j] >= L[i][j + 1]) out.push(["-", A[i++]]); else out.push(["+", B[j++]]);
    }
    while (i < A.length) out.push(["-", A[i++]]);
    while (j < B.length) out.push(["+", B[j++]]);
    return out;
  }
  const words = (s) => s.split(/(\s+)/).filter(Boolean);
  return (a, b) => {
    const out = [];
    const push = (op, t) => { const last = out[out.length - 1]; if (last && last[0] === op) last[1] += t; else out.push([op, t]); };
    const lines = lcs(String(a || "").split(/(?<=\n)/).filter(Boolean), String(b || "").split(/(?<=\n)/).filter(Boolean));
    for (let k = 0; k < lines.length;) {
      if (lines[k][0] === "=") { push("=", lines[k++][1]); continue; }
      let gone = "", come = "";
      for (; k < lines.length && lines[k][0] !== "="; k++) if (lines[k][0] === "-") gone += lines[k][1]; else come += lines[k][1];
      const w = lcs(words(gone), words(come));
      const same = w.filter(([op, t]) => op === "=" && t.trim()).length;
      const total = Math.min(words(gone).filter((t) => t.trim()).length, words(come).filter((t) => t.trim()).length);
      if (gone && come && same * 2 >= total) for (const [op, t] of w) push(op, t);
      else { if (gone) push("-", gone); if (come) push("+", come); }
    }
    return out;
  };
})();
