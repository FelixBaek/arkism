// @ts-check
// 개선하기 › 비교 평가 (Figma 198:4242): the left card picks the eval server and profile and starts a run,
// a second card lists the runs, and the page's right panel shows the picked run's state and its actions.
window.ArkismSkillExecution = ({ state, el, button, tauri }) => {
 const pending = new Map();
 const trimSlashes = (text) => { let end = text.length; while (end > 0 && text[end - 1] === "/") end--; return text.slice(0, end); };
 const STATUS = { running: ["실행 중", ""], awaiting_publication: ["저장 대기", "warn"], published: ["저장됨", ""], interrupted_or_failed: ["중단", "quiet"] };
 const TITLE = { running: "비교 실행 중", awaiting_publication: "결과 저장 대기", published: "결과 저장됨", interrupted_or_failed: "실행 중단" };
 const LEAD = { running: "비교 실행 중이에요.", awaiting_publication: "모델 실행은 끝났고, 서버에 저장하는 것만 남았어요.", published: "비교 결과를 저장했어요. 승인·적용은 별도로 진행해 주세요.", interrupted_or_failed: "저장된 결과 없이 실행이 끝났거나 중단됐어요. 재실행 전에 서버에서 실행 상태를 확인해 주세요." };
 // side: { box, extra, go, section, table } of the 개선하기 page; target: the 비교 대상 field from skill-review.js.
 return (version, refresh, side, target) => {
  const box = el("details", "section"), summary = el("summary", "", "고정 과제로 비교 실행"), body = el("div"); box.append(summary, body);
  const owner = JSON.stringify([state.server, state.token, state.scope, state.selected]);
  const draws = globalThis.ArkismKeep.latest();
  let host = state.defaults.ssh_host, busy = false, current = null, jobs = [];
  const live = ticket => box.isConnected && box.open && ticket.live() && state.view === "skills" && owner === JSON.stringify([state.server, state.token, state.scope, state.selected]);
  const base = { scope: { ...state.scope }, server: trimSlashes(state.server) }, candidate = { id: version.id, revision: version.revision };
  const pendingKey = () => JSON.stringify([owner, version.id, version.revision, host]);
  const known = new Set(Object.keys(STATUS));
  const when = job => { const d = new Date(job.created_at || ""); return Number.isFinite(d.getTime()) ? d.toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }) : job.created_at || "새 비교"; };
  const result = job => job.status === "published" && job.report && typeof job.passed === "boolean" ? `${job.passed ? "통과" : "미달"} · ${job.report.candidate_score}` : "—";
  async function call(action, extra = {}) {
    const reply = await tauri.invoke("skill_execution", { host, request: { ...base, action, ...extra } });
    if (!reply || reply.error || reply.authority !== "data_only_untrusted") throw new Error("execution_unavailable");
    return reply;
  }
  function validJob(value) { return known.has(value?.status) && /^[a-f0-9]{64}$/.test(value?.job_id); }
  async function draw() {
   const ticket = draws.begin();
   const top = target ? [target] : [];
   if (!tauri || state.caps?.role !== "human") { body.replaceChildren(...top, el("p", "meta", "관리 계정의 데스크톱 앱에서 비교를 실행할 수 있어요.")); return; }
   if (version.status !== "candidate" || !version.comparison_plan) { body.replaceChildren(...top, el("p", "meta", "고정 비교 계획이 있는 검토 대기 버전에서 실행할 수 있어요.")); side.extra.replaceChildren(); return; }
   const label = el("label", "field", "평가 서버"), input = el("input"), note = el("p", "meta"), profile = el("div"), pair = el("div", "v4s-pair"), actions = el("div", "v4s-actions");
   input.value = host; input.type = "text"; label.append(input); note.setAttribute("role", "status"); pair.append(...top, label);
   const start = button("비교 실행", "primary small", null, () => {
    const select = profile.querySelector("select");
    if (busy || !live(ticket) || !select) return;
    current = pending.get(pendingKey()) || { event_id: crypto.randomUUID(), profile_id: select.value };
    pending.set(pendingKey(), current);
    select.disabled = true; void act("start", { candidate, event_id: current.event_id, profile_id: current.profile_id });
   }), find = button("설정·기록 보기", "ghost small", null, lookup);
   start.disabled = true; actions.append(start, find);
   body.replaceChildren(pair, profile, actions, note);
   input.addEventListener("input", () => { host = input.value.trim(); current = null; jobs = []; profile.replaceChildren(); start.disabled = true; paint(); note.textContent = "서버를 다시 조회해 주세요."; });
   async function lookup() {
    if (busy || !live(ticket)) return;
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$/.test(host)) { note.textContent = "SSH 서버 이름을 확인해 주세요."; return; }
    busy = true; input.disabled = find.disabled = start.disabled = true; profile.replaceChildren(); note.textContent = "설정과 실행 기록을 확인하는 중…";
    try {
     const history = await call("history", { candidate });
     if (!live(ticket)) return;
     if (!Array.isArray(history.items) || history.items.some(row => !validJob(row))) throw new Error("invalid_history");
     jobs = history.items; current = jobs[0] || null; paint();
     const profiles = await call("profiles"); if (!live(ticket)) return;
     if (!Array.isArray(profiles.items)) throw new Error("invalid_profiles");
     const matched = profiles.items.filter(row => /^[A-Za-z0-9_.-]{1,128}$/.test(row.id) && typeof row.name === "string" && row.dataset_sha256 === version.comparison_plan.dataset_sha256 && row.environment_sha256 === version.comparison_plan.environment_sha256);
     if (!matched.length) { note.textContent = "이 버전의 고정 과제·실행 조건과 일치하는 평가 설정이 없어요."; return; }
     const field = el("label", "field", "평가 설정"), select = el("select"); matched.forEach(row => { const option = el("option", "", `${row.name} · ${row.cases}개 과제`); option.value = row.id; select.append(option); }); field.append(select);
     const held = pending.get(pendingKey());
     select.dataset.executionProfile = "true";
     if (held?.event_id) { select.value = held.profile_id; select.disabled = true; }
     profile.append(field); start.disabled = false;
     note.textContent = jobs.length ? "실행 기록을 불러왔어요. 같은 과제로 다시 비교할 수 있어요." : "동일 과제로 기존 버전과 후보를 비교해요. 실행만으로 승인·적용되지는 않아요.";
    } catch (_) { if (live(ticket)) note.textContent = "불러오지 못했어요. SSH 연결과 평가 서버의 범위·API 설정을 확인해 주세요."; }
    finally { busy = false; if (live(ticket)) input.disabled = find.disabled = false; }
   }
   // The runs card (side.extra) and the right panel follow `jobs` and `current`.
   function paint() {
    if (!jobs.length) side.extra.replaceChildren();
    else {
     const card = el("section", "v4s-card");
     card.append(el("h3", "", "실행 기록"), side.table(["기록", "상태", "결과"], jobs.map(job => [[[when(job), "mono"], STATUS[job.status], result(job)], () => { current = job; paint(); }, job === current])));
     side.extra.replaceChildren(card);
    }
    const parts = [];
    if (!current) parts.push(el("h2", "", "아직 비교 기록이 없어요"), el("p", "v4s-lead", "앱의 API 연결과 같은 평가 서버에서, 등록된 고정 과제와 실행 조건만 써요. 실행만으로 승인·적용되지는 않아요."));
    else {
     const job = current, row = el("div", "v4s-actions");
     parts.push(el("h2", "", `${when(job)} ${TITLE[job.status]}`), el("p", "v4s-lead", LEAD[job.status]));
     if (job.status === "awaiting_publication") row.append(button("다시 저장 시도", "small", null, () => act("publish", { job_id: job.job_id })));
     row.append(button("상태 확인", job.status === "awaiting_publication" ? "ghost small" : "small", null, () => act("status", { job_id: job.job_id })));
     if (job.status !== "running" && job.status !== "interrupted_or_failed") row.append(button("평가 다시 읽기", "ghost small", null, refresh));
     parts.push(row);
    }
    const saved = jobs.find(job => job.status === "published" && job.passed === true);
    if (saved) {
     const next = side.section("다음 단계");
     next.append(el("p", "meta", `저장된 평가(${when(saved)} · ${saved.report?.candidate_score})로 후보를 검토할 수 있어요.`), button("저장된 평가로 검토", "primary small", null, () => side.go(2)));
     parts.push(next);
    }
    side.box.replaceChildren(...parts);
   }
   async function act(action, extra) {
    if (busy || !live(ticket)) return;
    const controls = () => [...body.querySelectorAll("button,select"), ...side.box.querySelectorAll("button")];
    busy = true; input.disabled = true; controls().forEach(node => node.disabled = true); note.textContent = "실행 상태를 확인하는 중…";
    try {
     const reply = await call(action, extra); if (!live(ticket)) return; if (!validJob(reply)) throw new Error("invalid_job");
     // Only a start is remembered: pressing 비교 실행 again replays the same event, so the paid run never repeats.
     const old = jobs.find(job => job.job_id === reply.job_id);
     current = { ...old, ...(action === "start" ? pending.get(pendingKey()) : {}), ...reply };
     if (action === "start") pending.set(pendingKey(), current);
     jobs = old ? jobs.map(job => job === old ? current : job) : [current, ...jobs];
     paint(); note.textContent = "";
    } catch (_) { if (live(ticket)) note.textContent = "결과를 확인하지 못했어요. 같은 버튼으로 다시 확인하세요. 같은 실행 요청은 중복 실행하지 않아요."; }
    finally { busy = false; if (live(ticket)) { input.disabled = false; controls().forEach(node => node.disabled = node.dataset.executionProfile === "true" && Boolean(pending.get(pendingKey())?.event_id)); start.disabled = !profile.querySelector("select"); } }
   }
   paint(); void lookup();
  }
  box.addEventListener("toggle", () => { if (box.open) void draw(); else { draws.cancel(); body.replaceChildren(); } });
  return box;
 };
};
