// 세션 기록 helpers: the commands a person copies to resume a session or to continue it in another agent,
// and the numbers a row shows. Nothing here runs a command; the window only copies the text.
window.ArkismSessionList = (() => {
  "use strict";
  // shortcut: CLI flags as the agents documented them in 2026-10; an agent without a known command shows none. Update when an agent changes its flags.
  const AGENTS = {
    claude_code: { label: "Claude Code", resume: (id) => ["claude", "--resume", id], start: (p) => ["claude", p] },
    codex: { label: "Codex", resume: (id) => ["codex", "resume", id], start: (p) => ["codex", p] },
    gemini: { label: "Gemini CLI", resume: (id) => ["gemini", "--resume", id], start: (p) => ["gemini", "-i", p] },
    opencode: { label: "OpenCode", resume: (id) => ["opencode", "--session", id], start: (p) => ["opencode", "--prompt", p] },
    zcode: { label: "ZCode" }, grok: { label: "Grok" }, cursor: { label: "Cursor" },
  };
  const NONE = "기록 없음";
  const label = (a) => AGENTS[a]?.label || a || "에이전트";
  // a Windows folder (C:\…, \\server\…) gets PowerShell quoting; anything else POSIX shell quoting
  const windows = (folder) => /^([A-Za-z]:[\\/]|\\\\)/.test(folder || "");
  function quote(s, ps) {
    s = String(s);
    if (/^[\w@%+=:,./-]+$/.test(s)) return s;
    return ps ? `'${s.replace(/'/g, "''")}'` : `'${s.replace(/'/g, "'\\''")}'`;
  }
  function line(argv, folder) {
    const ps = windows(folder), cmd = argv.map((a) => quote(a, ps)).join(" ");
    return folder ? `cd ${quote(folder, ps)}; ${cmd}` : cmd;
  }
  const resumeCommand = (s) => (AGENTS[s?.agent]?.resume && s.session_id ? line(AGENTS[s.agent].resume(s.session_id), s.folder) : "");
  // what the other agent is told: the saved work state first, else the archived dialogue (both read through Arkism's own tools)
  function handoffPrompt(h) {
    const from = `원래 ${label(h?.agent)} 세션 ${h?.session_id || ""}에서 하던 일이야.`;
    if (h?.work?.id) return `Arkism resume_work 도구로 작업 ${h.work.id}를 불러와 이어서 해 줘. ${from}`;
    if (h?.archive_id) return `Arkism search_session_archives 도구에 archive_id ${h.archive_id}를 넣어 대화를 읽고 이어서 해 줘. ${from}`;
    return "";
  }
  function continueCommand(target, h) {
    const p = handoffPrompt(h);
    return AGENTS[target]?.start && p ? line(AGENTS[target].start(p), h.folder) : "";
  }
  // other agents someone can continue in: the ones seen on this account that have a start command
  const continueTargets = (agent, seen) => [...new Set(seen)].filter((a) => a !== agent && AGENTS[a]?.start);

  const compact = (n) => Number(n).toLocaleString("ko-KR", { notation: "compact", maximumFractionDigits: 1 });
  // a session with no counted tokens did not report them; 0 would read as "used nothing"
  const tokens = (s) => (s?.tokens > 0 ? compact(s.tokens) : NONE);
  const split = (s) => (s?.tokens > 0 ? `입력 ${compact(s.input || 0)} · 출력 ${compact(s.output || 0)} · 캐시 ${compact((s.cache_read || 0) + (s.cache_write || 0))}` : "");
  const cost = (s) => (!(s?.tokens > 0) ? NONE : !s.priced ? "가격 모름" : `약 $${Number(s.cost_usd || 0).toFixed(2)}`);
  const models = (s) => (Array.isArray(s?.models) ? s.models.filter((m) => m?.name) : []);
  const mainModel = (s) => { const m = models(s); return m.length ? m[0].name + (m.length > 1 ? ` +${m.length - 1}` : "") : ""; };
  const modelLine = (m) => `${m.name} · ${m.tokens > 0 ? compact(m.tokens) : NONE} · ${!(m.tokens > 0) ? NONE : m.priced ? `약 $${Number(m.cost_usd || 0).toFixed(2)}` : "가격 모름"}`;

  // a search hit's snippet split around the words; the server gives the place in UTF-8 bytes
  function hitParts(snippet, match, q) {
    let at = -1;
    try { at = new TextDecoder().decode(new TextEncoder().encode(snippet).slice(0, match)).length; } catch { /* fall back below */ }
    if (snippet.slice(at, at + q.length) !== q) at = snippet.indexOf(q);
    return at < 0 ? [snippet, "", ""] : [snippet.slice(0, at), q, snippet.slice(at + q.length)];
  }

  // a message's time: HH:mm, with the day in front when it is not the previous message's day; no time recorded shows nothing
  function msgTime(at, prev, locale = "ko-KR") {
    const t = new Date(at || NaN), p = new Date(prev || NaN);
    if (!at || Number.isNaN(t.getTime())) return "";
    const hm = `${String(t.getHours()).padStart(2, "0")}:${String(t.getMinutes()).padStart(2, "0")}`;
    return !Number.isNaN(p.getTime()) && p.toDateString() === t.toDateString() ? hm : `${t.toLocaleDateString(locale, { month: "long", day: "numeric" })} ${hm}`;
  }

  return { AGENTS, hitParts, msgTime, NONE, label, quote, resumeCommand, handoffPrompt, continueCommand, continueTargets, tokens, split, cost, mainModel, modelLine, models };
})();
