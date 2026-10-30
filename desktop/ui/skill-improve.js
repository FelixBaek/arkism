// @ts-check
"use strict";
// 개선하기 in one click (Figma 「제안 · 스킬 개선 쉬운 흐름 · 10-30」): one 시작 runs gather lessons → make the new version
// → compare it with the current one, and ends on one result card with [적용] [버리기]. The server picks every
// technical input (lessons, eval task, pass rule); the four expert steps stay under 자세히.
// Paid work starts only from a click: generation and a comparison each run under a fixed event id,
// so pressing 시작 again replays the same request instead of paying twice.
window.ArkismSkillImprove = ({ state, el, button, request, query, tauri, reload, generate }) => {
  const SCOPE_KEYS = ["workspace_id", "project_id", "user_id", "agent_id"];
  const hex64 = /^[a-f0-9]{64}$/;
  const sameScope = (value) => SCOPE_KEYS.every((k) => (value?.[k] || "") === (state.scope[k] || ""));
  const sha = async (text) => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text))), (b) => b.toString(16).padStart(2, "0")).join("");
  async function read(collection, id) {
    if (typeof id !== "string" || !id || id === "." || id === ".." || id.length > 256) throw new Error("invalid_reference");
    const r = await request(`/v1/${collection}/${encodeURIComponent(id)}?${query()}`);
    if (r.id !== id || !Number.isInteger(r.revision) || r.revision < 1 || r.content_erased || r.privacy_quarantined || !sameScope(r.scope)) throw new Error("changed_record");
    return r;
  }
  const post = (path, body) => request(path, { method: "POST", body: JSON.stringify({ scope: state.scope, ...body }) });
  async function exec(action, extra = {}) {
    const reply = await tauri.invoke("skill_execution", { host: state.defaults.ssh_host, request: { scope: { ...state.scope }, server: String(state.server).replace(/\/+$/, ""), action, ...extra } });
    if (!reply || reply.error || reply.authority !== "data_only_untrusted") throw new Error("execution_unavailable");
    return reply;
  }
  const validJob = (j) => ["running", "awaiting_publication", "published", "interrupted_or_failed"].includes(j?.status) && hex64.test(j?.job_id);
  // Removed lines next to added ones, in order (the same reading as 검토·승인's table).
  function changed(before, after) {
    const lines = (t) => t.split("\n").map((l) => l.trim()).filter(Boolean), a = lines(before), b = lines(after), inA = new Set(a), inB = new Set(b);
    const gone = a.filter((l) => !inB.has(l)), added = b.filter((l) => !inA.has(l));
    return Array.from({ length: Math.max(gone.length, added.length) }, (_, i) => [[gone[i] || "", "quiet"], added[i] || ""]);
  }
  // better: the server's pass rule held; worse: lower score or a failed regression/safety check; similar: neither.
  function verdict(job) {
    const r = job.report;
    if (job.passed === true) return "better";
    if (r && (r.regression_passed === false || r.safety_passed === false || r.candidate_score < r.baseline_score)) return "worse";
    return "similar";
  }
  const VERDICT = {
    better: ["새 버전이 더 나아요", "같은 문제에서 점수가 올랐고, 안전 검사도 모두 통과했어요."],
    similar: ["두 버전이 비슷해요", "점수 차이가 작아서 바꿀 만큼 좋아지지 않았어요."],
    worse: ["새 버전이 더 나빠요", "점수가 내려갔거나 안전 검사를 통과하지 못했어요."] };
  const pts = (x) => `${Math.round((+x || 0) * 100)}점`;
  const secs = (ms) => (ms >= 60000 ? `${+(ms / 60000).toFixed(1)}분` : `${Math.round(ms / 1000)}초`);
  const money = (c) => `$${(+c || 0).toFixed(2)}`;
  const day = (iso) => { const d = new Date(iso); return Number.isFinite(d.getTime()) ? d.toLocaleDateString(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric" }) : ""; };

  // The 개선하기 page: card shows one state at a time; side: the page's right panel ({ box, table, facts, section }).
  function page(sk, side, openStep) {
    const card = el("section", "v4s-card v4s-better"), more = el("details", "v4s-better-more"), wrap = el("div", "v4s-improve");
    const facts = el("div");
    more.append(el("summary", "", "자세히"), facts);
    const steps = el("div", "v4s-actions");
    ["개선안", "비교 평가", "검토·승인", "시험 적용"].forEach((label, i) => steps.append(button(label, "ghost small", null, () => openStep(i))));
    more.append(steps);
    wrap.append(card, more);
    const draws = globalThis.ArkismKeep.latest();
    const owner = JSON.stringify([state.server, state.token, state.scope, sk.id]);
    const live = (t) => card.isConnected && t.live() && owner === JSON.stringify([state.server, state.token, state.scope, sk.id]) && state.view === "skills";
    let ctx = {}, timer = 0;
    const paint = (title, lead, ...parts) => card.replaceChildren(el("h3", "", title), el("p", "v4s-card-lead", lead), ...parts);
    const row = (...buttons) => { const r = el("div", "v4s-actions"); r.append(...buttons); return r; };
    function paintSide() {
      const s = ctx.skill, n = s ? s.version_history.indexOf(s.active_version) + 1 : 0;
      side.box.replaceChildren(el("h2", "", "스킬 더 좋게 만들기"), el("p", "v4s-lead", "버튼 하나로 새 버전을 만들고 지금 버전과 비교해요. 적용은 직접 골라요."),
        ...(n ? [side.facts([["지금 쓰는 버전", `버전 ${n}`]], true)] : []));
    }
    function paintFacts() {
      const p = ctx.profile, plan = p?.default_plan;
      facts.replaceChildren(side.facts([["평가 서버", state.defaults?.ssh_host || "", "mono"], ["평가 과제", p ? `${p.name} · ${p.cases}개` : "—"],
        ["통과 기준", plan ? `${+(plan.min_score_gain * 100).toFixed(2)}점 이상 오르기` : "—"], ["쓰는 교훈", ctx.group ? `${ctx.group.interpretation} · 관측 ${Math.min(ctx.group.candidate_refs.length, 16)}개` : "—"]], true));
    }
    // Where an earlier 시작 left off, read back from the server: a waiting new version and its comparison runs.
    async function load() {
      const t = draws.begin();
      clearTimeout(timer);
      paint("스킬 더 좋게 만들기", "지금 상태를 확인하는 중…");
      if (state.caps?.role !== "human" || !tauri) { paint("스킬 더 좋게 만들기", "관리 계정의 데스크톱 앱에서 시작할 수 있어요."); return; }
      try {
        const skill = await read("skills", sk.id);
        if (!live(t)) return;
        if (!Array.isArray(skill.version_history) || !skill.active_version || !sk.source_path?.endsWith("SKILL.md")) { ctx = { skill: null }; paintSide(); paint("아직 개선할 수 없어요", "먼저 이 스킬의 버전이 하나 있어야 해요."); return; }
        ctx = { skill, baseline: await read("skill_versions", skill.active_version) };
        const last = skill.version_history.at(-1);
        if (last !== skill.active_version) {
          const v = await read("skill_versions", last);
          if (v.skill_id === sk.id && v.status === "candidate" && v.base_version === skill.active_version && v.comparison_plan) ctx.candidate = v;
        }
        const [lessons, profiles] = await Promise.all([ctx.candidate ? {} : generate.readLessons(sk, state.scope), generate.readProfiles(state.scope)]);
        if (!live(t)) return;
        const plan = ctx.candidate?.comparison_plan;
        ctx.profile = profiles.items?.find((p) => !plan || (p.dataset_sha256 === plan.dataset_sha256 && p.environment_sha256 === plan.environment_sha256));
        ctx.group = lessons.groups?.[0];
        ctx.missing = [lessons.missing, profiles.missing, profiles.items && !ctx.profile ? "이 새 버전을 비교할 평가 과제가 평가 서버에 없어요." : ""].filter(Boolean);
        if (ctx.candidate && !ctx.missing.length) {
          const history = await exec("history", { candidate: { id: ctx.candidate.id, revision: ctx.candidate.revision } });
          if (!Array.isArray(history.items) || history.items.some((j) => !validJob(j))) throw new Error("invalid_history");
          ctx.jobs = history.items;
          ctx.job = history.items[0];
        }
        if (!live(t)) return;
        paintSide(); paintFacts();
        if (ctx.job && ctx.job.status !== "interrupted_or_failed") return void follow(t);
        intro(t);
      } catch (_) { if (live(t)) paint("상태를 읽지 못했어요", "서버나 평가 서버 연결을 확인하고 다시 시도해 주세요.", row(button("다시 시도", "small", null, load))); }
    }
    function intro(t) {
      if (ctx.missing.length) {
        const list = el("ul", "generation-missing"); ctx.missing.forEach((m) => list.append(el("li", "", m)));
        paint("아직 시작할 수 없어요", "아래 내용이 채워지면 시작할 수 있어요.", list, row(button("다시 확인", "small", null, load))); return;
      }
      const plan = ctx.profile.default_plan, minutes = Math.max(1, Math.ceil((2 * plan.max_duration_ms + 60000) / 60000));
      const how = el("ol", "v4s-better-how");
      if (!ctx.candidate) how.append(el("li", "", `모아 둔 교훈 ${Math.min(ctx.group.candidate_refs.length, 16)}개로 새 버전을 만들어요`));
      how.append(el("li", "", `같은 문제 ${ctx.profile.cases}개로 지금 버전과 비교해요`), el("li", "", "결과를 보고 적용할지 직접 골라요"));
      const startBtn = button("시작", "primary", null, () => void run(t));
      paint(ctx.candidate ? "새 버전을 만들어 뒀어요" : "이 스킬을 더 좋게 만들어 볼까요?", ctx.candidate ? "지금 버전과 비교만 하면 돼요." : "이 스킬을 쓰며 모은 교훈으로 새 버전을 만들고, 지금 버전과 겨뤄 봐요.",
        how, side.facts([["걸리는 시간", `길어야 약 ${minutes}분`], ["비용", ctx.candidate ? "비교할 때 AI 사용료가 들어요" : "새 버전을 만들고 비교할 때 AI 사용료가 들어요"]], true),
        el("p", "meta", "적용하면 7일 동안 지켜보다가, 나빠지면 예전 버전으로 되돌려요."), row(startBtn));
    }
    function progress(stage) {
      const list = el("ol", "v4s-better-steps");
      [["gather", "교훈 모으기"], ["make", "새 버전 만들기"], ["compare", "지금 버전과 비교하기"], ["done", "결과 정리하기"]].forEach(([k, label], i, all) => {
        const at = all.findIndex(([x]) => x === stage), cls = i < at ? "done" : i === at ? "current" : "";
        list.append(el("li", cls, `${i < at ? "✓" : i === at ? "●" : "○"} ${label}`));
      });
      paint(stage === "make" ? "새 버전을 만들고 있어요" : "지금 버전과 비교하고 있어요", stage === "make" ? "보통 1분쯤 걸려요." : "보통 몇 분 걸려요. 다른 화면으로 가도 계속돼요.", list);
    }
    async function run(t) {
      if (!live(t)) return;
      try {
        if (!ctx.candidate) {
          progress("make");
          const g = ctx.group, lessons = g.candidate_refs.slice(0, 16).map(({ collection, id, revision }) => ({ collection, id, revision }));
          const p = ctx.profile, plan = { baseline_version: ctx.skill.active_version, dataset_sha256: p.dataset_sha256, environment_sha256: p.environment_sha256, ...p.default_plan };
          // One event per exact input: a second 시작 gets the server's saved result, never a second paid call.
          const event = `imp-${(await sha(JSON.stringify([sk.id, ctx.skill.revision, lessons, plan]))).slice(0, 48)}`;
          const out = await post("/v1/skills/generate", { event_id: event, skill_id: sk.id, path: sk.source_path, lessons, comparison_plan: plan });
          const v = out?.record;
          if (!v || v.skill_id !== sk.id || !sameScope(v.scope) || v.status !== "candidate" || !Number.isInteger(v.revision) || typeof v.id !== "string") throw new Error("invalid_generation_result");
          ctx.candidate = v; ctx.jobs = [];
        }
        if (!live(t)) return;
        progress("compare");
        const c = { id: ctx.candidate.id, revision: ctx.candidate.revision };
        // A stopped run is retried only by this click; the attempt number gives it a new event id.
        const event = `imp-${(await sha(JSON.stringify([c.id, c.revision]))).slice(0, 48)}-${(ctx.jobs || []).length}`;
        const job = await exec("start", { candidate: c, event_id: event, profile_id: ctx.profile.id });
        if (!validJob(job)) throw new Error("invalid_job");
        ctx.job = job; ctx.jobs = [job, ...(ctx.jobs || [])];
        if (live(t)) void follow(t);
      } catch (_) { if (live(t)) paint("멈췄어요", "새 버전을 만들거나 비교하지 못했어요. 다시 시도하면 같은 요청을 이어서 확인해요.", row(button("다시 시도", "small", null, load))); }
    }
    // Poll the comparison until it is saved; saving a finished run again is free, a new run never starts here.
    async function follow(t) {
      if (!live(t)) return;
      let job = ctx.job;
      try {
        if (job.status === "running" || job.status === "awaiting_publication") {
          progress("compare");
          const next = await exec(job.status === "awaiting_publication" ? "publish" : "status", { job_id: job.job_id });
          if (!validJob(next)) throw new Error("invalid_job");
          job = ctx.job = { ...job, ...next };
        }
        if (!live(t)) return;
        if (job.status === "published") return result(t);
        if (job.status === "interrupted_or_failed") { ctx.missing = []; paint("비교가 중간에 멈췄어요", "다시 비교하면 AI 사용료가 한 번 더 들어요.", row(button("다시 비교", "primary small", null, () => void run(t)))); return; }
        timer = setTimeout(() => void follow(t), 4000);
      } catch (_) { if (live(t)) paint("비교 상태를 확인하지 못했어요", "평가 서버 연결을 확인해 주세요. 진행 중인 비교는 그대로 이어져요.", row(button("다시 확인", "small", null, load))); }
    }
    function result(t) {
      const job = ctx.job, r = job.report || {}, kind = verdict(job), [title, lead] = VERDICT[kind];
      const sum = side.table(["", "지금", "새 버전"], [
        [["점수", pts(r.baseline_score), [pts(r.candidate_score), r.candidate_score > r.baseline_score ? "up" : ""]]],
        [["걸린 시간", r.baseline_duration_ms ? secs(r.baseline_duration_ms) : "—", secs(r.candidate_duration_ms || 0)]],
        [["비용", r.baseline_duration_ms ? money(r.baseline_cost) : "—", money(r.candidate_cost)]]]);
      const lines = changed(ctx.baseline.text || "", ctx.candidate.text || "").slice(0, 3);
      const diff = lines.length ? [el("h4", "", "바뀐 줄"), side.table(["지금", "새 버전"], lines.map((cells) => [cells]))] : [];
      // 적용 only for a better version; otherwise 버리기 is the one clear choice.
      const drop = button("버리기", kind === "better" ? "ghost" : "primary", null, () => void discard(t));
      card.className = `v4s-card v4s-better ${kind}`;
      paint(title, lead, sum, ...diff, kind === "better" ? row(button("적용", "primary", null, () => void apply(t)), drop) : row(drop));
    }
    // 적용 is the owner's decision: approve with the independent comparison, apply, then start the watch.
    async function apply(t) {
      card.querySelectorAll("button").forEach((b) => (b.disabled = true));
      try {
        let v = await read("skill_versions", ctx.candidate.id);
        if (v.status === "candidate") {
          const ev = job_evaluation(ctx.job) || (await passingEvaluation(v));
          if (!ev) throw new Error("no_evaluation");
          await post(`/v1/skill_versions/${encodeURIComponent(v.id)}/transition`, { expected_revision: v.revision, status: "approved", validation_id: ev });
          v = await read("skill_versions", v.id);
        }
        const skill = await read("skills", sk.id);
        if (skill.active_version !== v.id) await post(`/v1/skills/${encodeURIComponent(sk.id)}/apply`, { expected_revision: skill.revision, version_id: v.id });
        await post(`/v1/skills/${encodeURIComponent(sk.id)}/watch`, { version_id: v.id });
        if (!live(t)) return;
        card.className = "v4s-card v4s-better";
        paint("새 버전을 적용했어요", "7일 동안 실제 사용을 지켜보고, 나빠지면 예전 버전으로 되돌려요.");
        await reload();
      } catch (_) { if (live(t)) paint("적용하지 못했어요", "서버가 승인이나 적용을 거절했어요. 다시 시도하거나 자세히에서 확인해 주세요.", row(button("다시 시도", "small", null, () => void apply(t)))); }
    }
    const job_evaluation = (job) => (typeof job?.evaluation?.id === "string" ? job.evaluation.id : "");
    async function passingEvaluation(v) {
      const target = { collection: "skill_versions", id: v.id, revision: v.revision };
      const page = await post("/v1/evaluations/summary", { target });
      for (const ref of page?.evaluation_refs || []) {
        const ev = await read("evaluations", ref.id);
        if (ev.status === "active" && ev.target?.id === v.id && ev.evaluation?.passed === true && ev.evaluation?.eval_type !== "model_self" && ev.created_by !== v.created_by) return ev.id;
      }
      return "";
    }
    async function discard(t) {
      card.querySelectorAll("button").forEach((b) => (b.disabled = true));
      try {
        const v = await read("skill_versions", ctx.candidate.id);
        await post(`/v1/skill_versions/${encodeURIComponent(v.id)}/transition`, { expected_revision: v.revision, status: "rejected" });
        if (!live(t)) return;
        card.className = "v4s-card v4s-better";
        paint("새 버전을 버렸어요", "지금 버전을 그대로 써요.", row(button("처음으로", "small", null, load)));
      } catch (_) { if (live(t)) paint("버리지 못했어요", "다시 시도해 주세요.", row(button("다시 시도", "small", null, () => void discard(t)))); }
    }
    paintSide(); void load();
    return wrap;
  }

  // The skill popup's line under the name: the watch after 적용, or a hint when enough lessons are waiting.
  function status(sk, open) {
    const box = el("div", "v4s-better-status");
    box.hidden = true;
    if (state.caps?.role !== "human" || sk.status === "deprecated") return box;
    const show = (...parts) => { box.replaceChildren(...parts); box.hidden = false; };
    (async () => {
      let w = null;
      try { w = await post(`/v1/skills/${encodeURIComponent(sk.id)}/watch/check`, {}); } catch (_) { w = null; }
      if (w?.authority === "data_only_untrusted" && w.state === "watching") {
        const back = button("되돌리기", "small", null, async () => {
          back.disabled = true;
          try { const skill = await read("skills", sk.id); await post(`/v1/skills/${encodeURIComponent(sk.id)}/rollback`, { expected_revision: skill.revision, version_id: w.previous_version }); await reload(); }
          catch (_) { back.disabled = false; box.append(el("p", "meta", "되돌리지 못했어요. 다시 시도해 주세요.")); }
        });
        // The server lets the owner go back to the version just before during the watch, evaluated or not.
        return show(el("span", "", `새 버전을 지켜보는 중이에요 · ${day(w.until)}까지`), ...(w.can_rollback ? [back] : []));
      }
      if (w?.state === "rolled_back") return show(el("span", "", "새 버전이 나빠져서 예전 버전으로 되돌렸어요."));
      if (w?.state === "rollback_blocked") return show(el("span", "warn", "새 버전이 나빠졌지만 저절로 되돌리지 못했어요. 그사이 버전이 바뀌었거나 예전 버전을 쓸 수 없어요."));
      const lessons = await generate.readLessons(sk, state.scope);
      if (lessons.groups?.length) { const b = el("button", "v4s-link", "이 스킬을 더 좋게 만들 수 있어요 ›"); b.type = "button"; b.addEventListener("click", open); show(b); }
    })();
    return box;
  }
  return { page, status };
};
