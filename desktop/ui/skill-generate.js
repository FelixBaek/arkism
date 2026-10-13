"use strict";
window.ArkismSkillGenerate = ({ state, el, button, request, query, reload }) => {
  // Persist the request before sending; tokens and lesson/file contents never enter the journal.
  const pending = new Map(), running = new Map();
  return sk => {
    const panel = el("details", "section skill-generate"), summary = el("summary", "", "교훈으로 개선안 만들기"), body = el("div");
    panel.append(summary, body);
    const identity = () => JSON.stringify([state.server, state.token, state.scope, sk.id]);
    const owner = identity(), scope = { ...state.scope };
    let epoch = 0, busy = false, storageKey;
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
    const live = ticket => panel.isConnected && panel.open && ticket === epoch && identity() === owner && state.view === "skills" && state.selected === `skill:${sk.id}`;
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
            if (!body.querySelector(".generation-reset")) body.append(button("입력 다시 선택", "small generation-reset", null, () => { if (busy || !live(ticket)) return; try { forget(); draw(); } catch (_) { note.textContent = "보류 요청을 지우지 못했어요. 앱 저장 공간을 확인해 주세요."; } }));
          }
        }
      } finally { busy = false; running.delete(owner); }
    }
    function pendingView(ticket) {
      const note = el("p", "meta", "처리 결과를 확인할 생성 요청이 있어요."), retry = button("생성 결과 다시 확인", "primary small", null, () => submit(pending.get(owner), ticket, note, retry));
      note.setAttribute("role", "status"); body.replaceChildren(note, retry); return { note, retry };
    }
    async function draw() {
      const ticket = ++epoch;
      if (state.caps?.role !== "human") { body.replaceChildren(el("p", "meta", "개선안 생성은 관리 계정에서 시작할 수 있어요.")); return; }
      try { await key; if (!live(ticket)) return; recover(); }
      catch (_) { if (live(ticket)) body.replaceChildren(el("p", "meta", "보류 요청을 읽지 못했어요. 중복 생성을 막기 위해 새 요청을 시작하지 않아요. 앱 저장 공간을 확인해 주세요.")); return; }
      if (pending.has(owner)) { pendingView(ticket); return; }
      body.replaceChildren(el("p", "meta", "현재 기준 버전을 확인하는 중…"));
      try {
        const skill = await request(`/v1/skills/${encodeURIComponent(sk.id)}?${query()}`);
        if (!live(ticket)) return;
        if (skill.id !== sk.id || !sameScope(skill.scope) || skill.content_erased || skill.privacy_quarantined || !Number.isInteger(skill.revision) || skill.revision < 1 || typeof skill.active_version !== "string" || !skill.active_version || !sk.source_path?.endsWith("SKILL.md")) throw new Error("invalid_baseline");
        const search = el("form"), topic = field(search, "작업 주제", "text", `skill:${sk.id}`), find = button("교훈 찾기", "small"), choices = el("div"), form = el("form"), note = el("p", "meta");
        topic.maxLength = 128; find.type = "submit"; search.append(find); note.setAttribute("role", "status");
        const files = el("div"), dataset = field(files, "고정 평가 과제 파일", "file"), environment = field(files, "실행 조건 파일", "file");
        dataset.accept = environment.accept = ".json,application/json";
        const gain = field(form, "필요한 점수 향상 (0 초과, 1 이하)", "number"), cost = field(form, "후보 전체 비용 상한 (평가기 단위)", "number"), duration = field(form, "후보 전체 실행 시간 상한 (밀리초)", "number");
        gain.min = "0.000001"; gain.max = "1"; gain.step = cost.step = "any"; cost.min = "0"; duration.min = "1"; duration.step = "1";
        const generate = button("개선안 생성", "primary small"); generate.type = "submit";
        form.prepend(files); form.append(el("p", "meta", "선택한 교훈과 기존 스킬이 모델에 전달되며 생성 비용이 발생해요. 비교 파일은 실행하거나 전송하지 않아요. 같은 파일을 보관해 주세요."), generate);
        let refs = [], searchEpoch = 0, selected = [];
        body.replaceChildren(el("p", "meta", "같은 교훈의 관측 3~16개로 개선안을 만들어요. 생성 후 별도의 평가·승인·적용이 필요해요."), search, choices, form, note);
        const clearChoices = () => { searchEpoch++; refs = []; selected = []; choices.replaceChildren(); };
        topic.addEventListener("input", clearChoices);
        search.addEventListener("submit", async event => {
          event.preventDefault(); if (busy) return; clearChoices(); const lookup = searchEpoch, subject = topic.value.trim();
          if (!subject || new TextEncoder().encode(subject).length > 128) { note.textContent = "작업 주제를 128바이트 이내로 입력해 주세요."; return; }
          find.disabled = true; note.textContent = "교훈을 찾는 중…";
          try {
            const page = await request(`/v1/reflections?${query({ subject })}`);
            if (!live(ticket) || lookup !== searchEpoch) return;
            if (page.authority !== "data_only_untrusted" || !Array.isArray(page.items) || page.items.some(item => !sameScope(item.scope) || item.subject !== subject || typeof item.interpretation !== "string" || !Array.isArray(item.candidate_refs) || item.candidate_refs.some(ref => !validRef(ref)) || new Set(item.candidate_refs.map(ref => ref.id)).size !== item.candidate_refs.length)) throw new Error("invalid_lessons");
            const groups = page.items.filter(item => item.status === "candidate" && item.candidate_refs.length >= 3);
            note.textContent = groups.length ? "교훈을 선택한 뒤 사용할 관측을 골라 주세요." : "관측이 3개 이상인 검토 대기 교훈이 없어요.";
            const label = el("label", "field", "개선에 사용할 교훈"), select = el("select"), evidence = el("fieldset");
            const empty = el("option", "", "교훈 선택"); empty.value = ""; select.append(empty);
            groups.forEach((item, i) => { const option = el("option", "", item.interpretation); option.value = String(i); select.append(option); }); label.append(select);
            choices.replaceChildren(label, evidence);
            select.addEventListener("change", () => {
              refs = select.value === "" ? [] : groups[Number(select.value)].candidate_refs; selected = [];
              evidence.replaceChildren(el("legend", "", "사용할 관측 (3~16개)"));
              refs.forEach((ref, i) => { const label = el("label", "generation-observation"), check = el("input"); check.type = "checkbox";
                label.append(check, document.createTextNode(`관측 ${i + 1} · 판 ${ref.revision}`)); label.title = ref.id;
                check.addEventListener("change", () => { selected = [...evidence.querySelectorAll("input")].flatMap((input, index) => input.checked ? [refs[index]] : []); }); evidence.append(label);
              });
            });
          } catch (_) { if (live(ticket) && lookup === searchEpoch) note.textContent = "교훈을 읽지 못했어요. 권한과 서버 버전을 확인하고 다시 찾아 주세요."; }
          finally { if (live(ticket)) find.disabled = false; }
        });
        form.addEventListener("submit", async event => {
          event.preventDefault(); if (busy || !live(ticket)) return;
          if (selected.length < 3 || selected.length > 16) { note.textContent = "같은 교훈의 관측을 3~16개 선택해 주세요."; return; }
          const min = Number(gain.value), max = Number(cost.value), ms = Number(duration.value), lessons = selected.map(ref => ({ ...ref }));
          if (!(min > 0 && min <= 1 && Number.isFinite(max) && max >= 0 && Number.isSafeInteger(ms) && ms > 0)) return;
          busy = true; body.querySelectorAll("input, select, button").forEach(node => node.disabled = true);
          try {
            const hash = async input => { const file = input.files[0]; if (!file || file.size > 8 * 1024 * 1024) throw new Error("invalid_file"); const bytes = await file.arrayBuffer(); JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), byte => byte.toString(16).padStart(2, "0")).join(""); };
            const datasetHash = await hash(dataset), environmentHash = await hash(environment);
            if (!live(ticket)) return;
            const current = await request(`/v1/skills/${encodeURIComponent(sk.id)}?${query()}`);
            if (!live(ticket)) return;
            if (!sameScope(current.scope) || current.id !== skill.id || current.revision !== skill.revision || current.active_version !== skill.active_version || current.content_erased || current.privacy_quarantined) throw new Error("changed_baseline");
            const input = { scope, event_id: crypto.randomUUID(), skill_id: sk.id, path: sk.source_path, lessons, comparison_plan: { baseline_version: skill.active_version, dataset_sha256: datasetHash, environment_sha256: environmentHash, min_score_gain: min, max_cost: max, max_duration_ms: ms } };
            localStorage.setItem(storageKey, JSON.stringify(input)); pending.set(owner, input); const { note, retry } = pendingView(ticket); busy = false; await submit(input, ticket, note, retry);
          } catch (_) { if (live(ticket)) { note.textContent = "생성을 시작하지 못했어요. 앱 저장 공간, 8MB 이내의 JSON 파일과 현재 기준 버전을 확인해 주세요."; body.querySelectorAll("input, select, button").forEach(node => node.disabled = false); } }
          finally { busy = false; }
        });
      } catch (_) { if (live(ticket)) body.replaceChildren(el("p", "meta", "현재 기준 버전을 읽지 못했어요. 접었다 다시 펼쳐 주세요.")); }
    }
    panel.addEventListener("toggle", () => { if (panel.open) draw(); else { epoch++; body.replaceChildren(); } });
    return panel;
  };
};
