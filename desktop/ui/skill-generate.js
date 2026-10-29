// @ts-check
"use strict";
window.ArkismSkillGenerate = ({ state, el, button, request, query, reload, tauri }) => {
  // Persist the request before sending; tokens and lesson contents never enter the journal.
  // Step 1 fills itself: the skill's lessons from /v1/reflections, the eval set and pass rule from the eval server's registered profiles.
  const NO_COST_CAP = 1e6; // the eval server's defaultMaxCost (internal/host/crew/skillexec.go)
  const hex64 = /^[a-f0-9]{64}$/;
  const pending = new Map(), running = new Map();
  return sk => {
    const panel = el("details", "section skill-generate"), summary = el("summary", "", "교훈으로 개선안 만들기"), body = el("div");
    panel.append(summary, body);
    const identity = () => JSON.stringify([state.server, state.token, state.scope, sk.id]);
    const owner = identity(), scope = { ...state.scope };
    let busy = false, storageKey;
    const draws = globalThis.ArkismKeep.latest();
    const digest = async value => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value))), byte => byte.toString(16).padStart(2, "0")).join("");
    const key = digest(JSON.stringify([state.server, state.token, ...["workspace_id", "project_id", "user_id", "agent_id"].map(k => scope[k] || ""), sk.id])).then(hash => storageKey = `arkism.skill-generation.v1.${hash}`);
    function forget() { localStorage.removeItem(storageKey); pending.delete(owner); }
    function recover() {
      const raw = localStorage.getItem(storageKey);
      if (raw === null) return;
      if (raw.length > 32768) throw new Error("invalid_pending");
      const input = JSON.parse(raw), plan = input?.comparison_plan;
      const fields = (value, names) => value && typeof value === "object" && !Array.isArray(value) && Object.keys(value).every(k => names.includes(k));
      if (!fields(input, ["scope", "event_id", "skill_id", "path", "lessons", "comparison_plan"]) ||
          !fields(input.scope, ["workspace_id", "project_id", "user_id", "agent_id"]) || !sameScope(input.scope) ||
          input.skill_id !== sk.id || typeof input.path !== "string" || !input.path.endsWith("SKILL.md") || input.path !== sk.source_path || typeof input.event_id !== "string" || !/^[a-zA-Z0-9._-]{1,128}$/.test(input.event_id || "") ||
          !Array.isArray(input.lessons) || input.lessons.length < 3 || input.lessons.length > 16 ||
          input.lessons.some(ref => !validRef(ref) || !fields(ref, ["collection", "id", "revision"])) ||
          new Set(input.lessons.map(ref => ref.id)).size !== input.lessons.length ||
          !fields(plan, ["baseline_version", "dataset_sha256", "environment_sha256", "min_score_gain", "max_cost", "max_duration_ms"]) ||
          typeof plan.baseline_version !== "string" || !plan.baseline_version || plan.baseline_version.length > 256 ||
          !/^[a-f0-9]{64}$/.test(plan.dataset_sha256 || "") || !/^[a-f0-9]{64}$/.test(plan.environment_sha256 || "") ||
          !(plan.min_score_gain > 0 && plan.min_score_gain <= 1 && Number.isFinite(plan.min_score_gain) && Number.isFinite(plan.max_cost) && plan.max_cost >= 0 && Number.isSafeInteger(plan.max_duration_ms) && plan.max_duration_ms > 0)) throw new Error("invalid_pending");
      pending.set(owner, input);
    }
    const live = ticket => panel.isConnected && panel.open && ticket.live() && identity() === owner && state.view === "skills" && state.selected === `skill:${sk.id}`;
    const sameScope = value => ["workspace_id", "project_id", "user_id", "agent_id"].every(k => (value?.[k] || "") === (scope[k] || ""));
    const validRef = ref => ref?.collection === "memory_candidates" && typeof ref.id === "string" && ref.id.length > 0 && ref.id.length <= 256 && ![".", ".."].includes(ref.id) && Number.isInteger(ref.revision) && ref.revision > 0;
    function field(form, label, type, value = "") {
      const wrap = el("label", "field", label), input = el("input"); input.type = type; input.value = value; input.required = true; wrap.append(input); form.append(wrap); return input;
    }
    async function submit(input, ticket, note, retry) {
      if (busy || !live(ticket) || state.caps?.role !== "human") return;
      if (!input) { await reload(); return; }
      busy = true; retry.disabled = true; note.textContent = "개선안을 생성하는 중…";
      try {
        if (!running.has(owner)) running.set(owner, request("/v1/skills/generate", { method: "POST", body: JSON.stringify(input) }));
        const result = await running.get(owner);
        if (!result?.record || result.record.skill_id !== sk.id || !sameScope(result.record.scope) || !["candidate", "approved", "active"].includes(result.record.status) || !Number.isInteger(result.record.revision) || typeof result.record.id !== "string") throw new Error("invalid_generation_result");
        forget();
        if (live(ticket)) { note.textContent = "개선안을 저장했어요. ‘개선안과 버전 검토’에서 확인하세요."; await reload(); }
      } catch (error) {
        if (live(ticket)) {
          note.textContent = "생성 결과를 확인하지 못했어요. 같은 요청으로 다시 확인하세요. 완료된 요청은 새로 생성하지 않아요."; retry.disabled = false;
          if (["invalid_request", "access_denied", "not_found", "revision_conflict"].includes(error.message)) {
            note.textContent = "서버가 요청을 거절했어요. 현재 근거와 권한을 확인한 뒤 입력을 다시 선택할 수 있어요.";
            if (!body.querySelector(".generation-reset")) body.append(button("입력 다시 선택", "small generation-reset", null, () => { if (busy || !live(ticket)) return; try { forget(); void draw(); } catch (_) { note.textContent = "보류 요청을 지우지 못했어요. 앱 저장 공간을 확인해 주세요."; } }));
          }
        }
      } finally { busy = false; running.delete(owner); }
    }
    function pendingView(ticket) {
      const note = el("p", "meta", "처리 결과를 확인할 생성 요청이 있어요."), retry = button("생성 결과 다시 확인", "primary small", null, () => submit(pending.get(owner), ticket, note, retry));
      note.setAttribute("role", "status"); body.replaceChildren(note, retry); return { note, retry };
    }
    // { groups } sorted by observation count, or { missing } saying what the owner can do.
    async function readLessons() {
      const subject = `skill:${sk.id}`;
      try {
        const page = await request(`/v1/reflections?${query({ subject })}`);
        if (page.authority !== "data_only_untrusted" || !Array.isArray(page.items) || page.items.some(item => !sameScope(item.scope) || item.subject !== subject || typeof item.interpretation !== "string" || !Array.isArray(item.candidate_refs) || item.candidate_refs.some(ref => !validRef(ref)) || new Set(item.candidate_refs.map(ref => ref.id)).size !== item.candidate_refs.length)) throw new Error("invalid_lessons");
        const open = page.items.filter(item => item.status === "candidate").sort((a, b) => b.candidate_refs.length - a.candidate_refs.length);
        const groups = open.filter(item => item.candidate_refs.length >= 3);
        if (groups.length) return { groups };
        return { missing: `이 스킬에 대한 관측이 아직 ${open[0]?.candidate_refs.length || 0}개예요. 같은 교훈의 관측이 3개 이상 모이면 만들 수 있어요. 이 스킬을 실제 작업에 더 써 보세요.` };
      } catch (_) { return { missing: "교훈을 읽지 못했어요. 권한과 서버 버전을 확인해 주세요." }; }
    }
    async function readProfiles() {
      if (!tauri) return { missing: "평가 과제는 데스크톱 앱이 평가 서버에서 불러와요. 데스크톱 앱에서 열어 주세요." };
      let reply;
      try { reply = await tauri.invoke("skill_execution", { host: state.defaults.ssh_host, request: { scope: { ...scope }, server: String(state.server).replace(/\/+$/, ""), action: "profiles" } }); }
      catch (_) { return { missing: `평가 서버(${state.defaults.ssh_host})에 연결하지 못했어요. 설정의 SSH 연결을 확인해 주세요.` }; }
      if (!reply || reply.error || reply.authority !== "data_only_untrusted" || !Array.isArray(reply.items)) return { missing: "평가 서버가 평가 설정을 보내지 않았어요. 평가 서버의 Arkism 설정을 확인해 주세요." };
      const items = reply.items.filter(row => /^[A-Za-z0-9_.-]{1,128}$/.test(row?.id) && typeof row.name === "string" && Number.isInteger(row.cases) && hex64.test(row.dataset_sha256) && hex64.test(row.environment_sha256));
      if (!items.length) return { missing: "평가 서버에 등록된 평가 과제가 없어요. 평가 서버 설정에 과제를 하나 등록하면 자동으로 채워져요." };
      const plan = row => row.default_plan, ready = items.filter(row => plan(row) && plan(row).min_score_gain > 0 && plan(row).min_score_gain <= 1 && Number.isFinite(plan(row).max_cost) && plan(row).max_cost >= 0 && Number.isSafeInteger(plan(row).max_duration_ms) && plan(row).max_duration_ms > 0);
      return ready.length ? { items: ready } : { missing: "평가 서버의 Arkism이 오래돼 통과 기준을 주지 않아요. 평가 서버를 업데이트하면 자동으로 채워져요." };
    }
    async function draw() {
      const ticket = draws.begin();
      if (state.caps?.role !== "human") { body.replaceChildren(el("p", "meta", "개선안 생성은 관리 계정에서 시작할 수 있어요.")); return; }
      try { await key; if (!live(ticket)) return; recover(); }
      catch (_) { if (live(ticket)) body.replaceChildren(el("p", "meta", "보류 요청을 읽지 못했어요. 중복 생성을 막기 위해 새 요청을 시작하지 않아요. 앱 저장 공간을 확인해 주세요.")); return; }
      if (pending.has(owner)) { pendingView(ticket); return; }
      body.replaceChildren(el("p", "meta", "현재 기준 버전을 확인하는 중…"));
      try {
        const skill = await request(`/v1/skills/${encodeURIComponent(sk.id)}?${query()}`);
        if (!live(ticket)) return;
        if (skill.id !== sk.id || !sameScope(skill.scope) || skill.content_erased || skill.privacy_quarantined || !Number.isInteger(skill.revision) || skill.revision < 1 || typeof skill.active_version !== "string" || !skill.active_version || !sk.source_path?.endsWith("SKILL.md")) throw new Error("invalid_baseline");
        const [lessons, profiles] = await Promise.all([readLessons(), readProfiles()]);
        if (!live(ticket)) return;
        const missing = [lessons.missing, profiles.missing].filter(Boolean);
        if (missing.length) {
          const list = el("ul", "generation-missing"); missing.forEach(text => list.append(el("li", "", text)));
          body.replaceChildren(el("p", "", "아직 개선안을 만들 수 없어요."), list, button("다시 확인", "small", null, () => void draw())); return;
        }
        const form = el("form"), note = el("p", "meta"), evidence = el("fieldset"), rule = el("p", "generation-rule");
        note.setAttribute("role", "status");
        let group = lessons.groups[0], profile = profiles.items[0], selected = [];
        const paintEvidence = () => {
          evidence.replaceChildren(el("legend", "", "쓸 관측 (빼려면 체크 해제)"));
          const boxes = group.candidate_refs.map((ref, i) => { const label = el("label", "generation-observation"), check = el("input"); check.type = "checkbox"; check.checked = i < 16;
            label.append(check, document.createTextNode(`관측 ${i + 1} · 판 ${ref.revision}`)); label.title = ref.id; evidence.append(label); return check; });
          const sync = () => { selected = group.candidate_refs.filter((_, i) => boxes[i].checked); };
          boxes.forEach(check => check.addEventListener("change", sync)); sync();
        };
        const pick = (label, items, text, onPick) => {
          if (items.length === 1) return el("p", "meta", `${label} · ${text(items[0])}`);
          const wrap = el("label", "field", label), select = el("select"); items.forEach((item, i) => { const option = el("option", "", text(item)); option.value = String(i); select.append(option); });
          select.addEventListener("change", () => onPick(items[Number(select.value)])); wrap.append(select); return wrap;
        };
        const lessonPick = pick("교훈", lessons.groups, item => `${item.interpretation} · 관측 ${item.candidate_refs.length}개`, item => { group = item; paintEvidence(); });
        const advanced = el("details", "generation-advanced"), gain = field(advanced, "필요한 점수 향상 (100점 만점)", "number"), minutes = field(advanced, "후보 실행 시간 상한 (분)", "number"), cost = field(advanced, "비용 상한 (평가기가 보고하는 값, 비우면 제한 없음)", "number");
        advanced.prepend(el("summary", "", "고급 설정")); cost.required = false;
        gain.min = "0.01"; gain.max = "100"; gain.step = minutes.step = cost.step = "any"; minutes.min = "0.01"; cost.min = "0";
        const plan = () => ({ min_score_gain: Number(gain.value) / 100, max_cost: cost.value === "" ? NO_COST_CAP : Number(cost.value), max_duration_ms: Math.round(Number(minutes.value) * 60000) });
        const paintRule = () => {
          const p = plan(), ok = p.min_score_gain > 0 && p.min_score_gain <= 1 && Number.isFinite(p.max_cost) && p.max_cost >= 0 && Number.isSafeInteger(p.max_duration_ms) && p.max_duration_ms > 0;
          rule.textContent = ok ? `통과 기준 · 점수가 ${+(p.min_score_gain * 100).toFixed(2)}점 이상 오르고 ${+(p.max_duration_ms / 60000).toFixed(1)}분 안에 끝나면 통과 · ${p.max_cost >= NO_COST_CAP ? "비용 제한 없음" : `비용 ${p.max_cost} 이하`}` : "고급 설정의 값을 확인해 주세요.";
          return ok;
        };
        const usePlan = item => { const d = item.default_plan; profile = item; gain.value = String(+(d.min_score_gain * 100).toFixed(4)); minutes.value = String(+(d.max_duration_ms / 60000).toFixed(4)); cost.value = d.max_cost >= NO_COST_CAP ? "" : String(d.max_cost); paintRule(); };
        const profilePick = pick("평가 과제", profiles.items, item => `${item.name} · 과제 ${item.cases}개`, usePlan);
        [gain, minutes, cost].forEach(input => input.addEventListener("input", paintRule));
        const generate = button("개선안 만들기", "primary small"); generate.type = "submit";
        form.append(lessonPick, evidence, profilePick, rule, advanced, el("p", "meta", "고른 관측과 지금 스킬이 모델에 전달되며 생성 비용이 들어요. 만든 뒤 비교 평가·승인을 거쳐야 적용돼요."), generate, note);
        body.replaceChildren(form); paintEvidence(); usePlan(profile);
        form.addEventListener("submit", async event => {
          event.preventDefault(); if (busy || !live(ticket)) return;
          if (selected.length < 3 || selected.length > 16) { note.textContent = "관측을 3~16개 골라 주세요."; return; }
          if (!paintRule()) { advanced.open = true; return; }
          const lessons = selected.map(ref => ({ ...ref })), limits = plan(), chosen = profile;
          busy = true; body.querySelectorAll("input, select, button").forEach(node => node.disabled = true);
          try {
            const current = await request(`/v1/skills/${encodeURIComponent(sk.id)}?${query()}`);
            if (!live(ticket)) return;
            if (!sameScope(current.scope) || current.id !== skill.id || current.revision !== skill.revision || current.active_version !== skill.active_version || current.content_erased || current.privacy_quarantined) throw new Error("changed_baseline");
            const input = { scope, event_id: crypto.randomUUID(), skill_id: sk.id, path: sk.source_path, lessons, comparison_plan: { baseline_version: skill.active_version, dataset_sha256: chosen.dataset_sha256, environment_sha256: chosen.environment_sha256, ...limits } };
            localStorage.setItem(storageKey, JSON.stringify(input)); pending.set(owner, input); const { note, retry } = pendingView(ticket); busy = false; await submit(input, ticket, note, retry);
          } catch (_) { if (live(ticket)) { note.textContent = "만들기를 시작하지 못했어요. 스킬이 그사이 바뀌었거나 앱 저장 공간에 문제가 있어요. 다시 열어 주세요."; body.querySelectorAll("input, select, button").forEach(node => node.disabled = false); } }
          finally { busy = false; }
        });
      } catch (_) { if (live(ticket)) body.replaceChildren(el("p", "meta", "현재 기준 버전을 읽지 못했어요. 접었다 다시 펼쳐 주세요.")); }
    }
    panel.addEventListener("toggle", () => { if (panel.open) void draw(); else { draws.cancel(); body.replaceChildren(); } });
    return panel;
  };
};
