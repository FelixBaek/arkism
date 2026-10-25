// @ts-check
window.ArkismSkillReview = ({ state, el, button, request, query, reload, execution, trial }) => (sk, mode) => {
  "use strict";
  // mode "" shows everything; the 개선하기 steps show one part each: "compare" (비교 실행), "review" (평가·승인), "trial" (시험 적용).
  const panel = el("details", mode ? "section improve-part" : "section"), summary = el("summary", "", { compare: "비교 평가", review: "검토·승인", trial: "시험 적용" }[mode] || "개선안과 버전 검토"), body = el("div");
  const opened = (part) => { part.open = true; return part; };
  panel.append(summary, body);
  const owner = JSON.stringify([state.server, state.token, state.scope, state.view, state.selected]);
  let epoch = 0;
  const live = ticket => panel.isConnected && panel.open && ticket === epoch && owner === JSON.stringify([state.server, state.token, state.scope, state.view, state.selected]);
  const status = { candidate: "검토 대기", approved: "승인됨", active: "사용 중", superseded: "과거 버전", rejected: "제외됨", deprecated: "중단됨" };
  async function read(collection, id, revision) {
    if (typeof id !== "string" || !id || id === "." || id === ".." || id.length > 256) throw new Error("invalid_reference");
    const record = await request(`/v1/${collection}/${encodeURIComponent(id)}?${query()}`);
    if (record.id !== id || !Number.isInteger(record.revision) || record.revision < 1 || (revision != null && record.revision !== revision) || record.content_erased || record.privacy_quarantined ||
        !["workspace_id", "project_id", "user_id", "agent_id"].every(k => (record.scope?.[k] || "") === (state.scope[k] || ""))) throw new Error("changed_record");
    return record;
  }
  const passage = (label, text) => {
    const d = el("details"), p = el("p", "original-text", text || "저장된 내용이 없어요.");
    d.append(el("summary", "", label), p); return d;
  };
  async function load(preferred) {
    const ticket = ++epoch;
    body.replaceChildren(el("p", "meta", "버전과 평가를 불러오는 중…"));
    try {
      const skill = await read("skills", sk.id);
      if (!live(ticket)) return;
      if (!Array.isArray(skill.version_history) || !skill.version_history.length || skill.version_history.some(id => typeof id !== "string") || new Set(skill.version_history).size !== skill.version_history.length) throw new Error("invalid_history");
      const label = el("label", "field", "검토할 버전"), select = el("select"), detail = el("div"), trials = el("div");
      for (const [index, id] of skill.version_history.entries()) {
        const option = el("option", "", `버전 ${index + 1}${id === skill.active_version ? " · 사용 중" : ""}`); option.value = id; select.append(option);
      }
      select.value = skill.version_history.includes(preferred) ? preferred : skill.version_history.at(-1);
      label.append(select);
      body.replaceChildren(el("p", "meta", "스킬 원문과 평가는 검토 자료예요. 조회만으로 승인되거나 실행되지 않아요."), label, trials, detail);
      const show = async () => {
        const versionTicket = ++epoch, id = select.value;
        if (mode === "trial") { trials.replaceChildren(opened(trial(skill.id, id))); detail.replaceChildren(); return; }
        trials.replaceChildren(mode ? "" : trial(skill.id, id));
        detail.replaceChildren(el("p", "meta", "평가를 읽는 중…"));
        try {
          const version = await read("skill_versions", id);
          if (!live(versionTicket)) return;
          if (version.skill_id !== skill.id || typeof version.text !== "string" || !Object.hasOwn(status, version.status)) throw new Error("invalid_version");
          if (mode === "compare") { detail.replaceChildren(opened(execution(version, () => load(id)))); return; }
          const baseline = version.base_version ? await read("skill_versions", version.base_version) : null;
          if (!live(versionTicket)) return;
          if (baseline && (baseline.skill_id !== skill.id || typeof baseline.text !== "string")) throw new Error("invalid_baseline");
          const target = { collection: "skill_versions", id, revision: version.revision };
          const evaluations = await request("/v1/evaluations/summary", { method: "POST", body: JSON.stringify({ scope: state.scope, target }) });
          if (!live(versionTicket)) return;
          if (evaluations.authority !== "data_only_untrusted" || evaluations.target?.id !== id || evaluations.target?.collection !== target.collection || evaluations.target?.revision !== version.revision || !Array.isArray(evaluations.evaluation_refs) || evaluations.evaluation_refs.some(ref => ref.collection !== "evaluations" || !Number.isInteger(ref.revision) || ref.revision < 1)) throw new Error("invalid_summary");
          const refs = [...evaluations.evaluation_refs];
          if (version.validation_id && !refs.some(ref => ref.id === version.validation_id)) refs.push({ collection: "evaluations", id: version.validation_id });
          const records = [];
          for (const ref of refs) {
            if (ref.collection !== "evaluations") throw new Error("invalid_evaluation");
            const ev = await read("evaluations", ref.id, ref.revision);
            if (!live(versionTicket)) return;
            if (ev.target?.collection !== "skill_versions" || ev.target?.id !== id || ev.status !== "active" || (ev.expires_at && !(Date.parse(ev.expires_at) > Date.now()))) throw new Error("invalid_evaluation");
            records.push(ev);
          }
          if (baseline) await read("skill_versions", baseline.id, baseline.revision);
          await read("skills", skill.id, skill.revision);
          await read("skill_versions", id, version.revision);
          if (!live(versionTicket)) return;
          detail.replaceChildren(el("h3", "", status[version.status]), passage("버전 원문", version.text));
          if (baseline) detail.append(passage("기존 버전 원문", baseline.text));
          if (version.comparison_plan) {
            const plan = version.comparison_plan;
            detail.append(el("p", "meta", `고정 비교 조건 · 점수 향상 ${plan.min_score_gain} 이상 · 비용 ${plan.max_cost} 이하 · 시간 ${plan.max_duration_ms}ms 이하`));
          }
          const note = el("p", "meta", records.length ? "평가를 선택해 검토하세요. 승인과 적용은 서버가 근거와 현재 상태를 다시 검사해요." : "이 버전에 연결된 평가가 없어요."); note.setAttribute("role", "status"); detail.append(note);
          for (const ev of records) {
            const group = el("section", "section"), data = ev.evaluation, self = data?.eval_type === "model_self";
            group.append(el("h3", "", self ? "모델 자기평가" : "평가 기록"), el("p", "meta", `결과 · ${data?.passed === true ? "통과" : data?.passed === false ? "실패" : "판정 없음"}`), passage("평가 내용", ev.text));
            if (data?.comparison_report) {
              const r = data.comparison_report;
              group.append(el("p", "", `기존 점수 ${r.baseline_score} → 후보 점수 ${r.candidate_score}`), el("p", "meta", `비용 ${r.candidate_cost} · 시간 ${r.candidate_duration_ms}ms · 회귀 ${r.regression_passed ? "통과" : "실패"} · 안전 ${r.safety_passed ? "통과" : "실패"}`));
            }
            if (state.caps?.role === "human" && version.status === "candidate" && !self && data?.passed === true && ev.created_by !== version.created_by) {
              group.append(button("이 평가로 후보 승인", "small", null, () => act(`/v1/skill_versions/${encodeURIComponent(id)}/transition`, { expected_revision: version.revision, status: "approved", validation_id: ev.id })));
            }
            if (state.caps?.role === "human" && version.status === "superseded" && !version.validation_id && !self && data?.passed === true) group.append(button("이 평가로 과거판 복귀", "small", null, () => act(`/v1/skills/${encodeURIComponent(skill.id)}/rollback`, { expected_revision: skill.revision, version_id: id, validation_id: ev.id })));
            detail.append(group);
          }
          if (state.caps?.role === "human") {
            if (version.status === "candidate") detail.append(button("후보 제외", "small", null, () => act(`/v1/skill_versions/${encodeURIComponent(id)}/transition`, { expected_revision: version.revision, status: "rejected" })));
            if (version.status === "superseded" && version.validation_id) detail.append(button("이 버전으로 복귀", "small", null, () => act(`/v1/skills/${encodeURIComponent(skill.id)}/rollback`, { expected_revision: skill.revision, version_id: id })));
            if (version.status === "approved") detail.append(button("이 버전 적용", "primary small", null, () => act(`/v1/skills/${encodeURIComponent(skill.id)}/apply`, { expected_revision: skill.revision, version_id: id })));
          }
          if (!mode) detail.append(execution(version, () => load(id)));
          async function act(path, input) {
            if (!live(versionTicket)) return;
            detail.querySelectorAll("button").forEach(b => b.disabled = true); select.disabled = true;
            try {
              await request(path, { method: "POST", body: JSON.stringify({ scope: state.scope, ...input }) });
              if (live(versionTicket)) {
                if (path.endsWith("/apply") || path.endsWith("/rollback")) await reload();
                else await load(id);
              }
            } catch (_) {
              if (live(versionTicket)) { note.textContent = "반영 결과를 확인하지 못했어요. 권한·평가 기준·기록 변경 여부를 확인하려면 다시 조회하세요."; detail.append(button("다시 조회", "small", null, () => load(id))); }
            } finally { if (live(versionTicket)) select.disabled = false; }
          }
        } catch (_) { if (live(versionTicket)) detail.replaceChildren(el("p", "meta", "버전이나 평가를 읽지 못했어요. 권한·삭제·기록 변경 여부를 확인하고 다시 조회하세요."), button("다시 조회", "small", null, () => load(id))); }
      };
      select.addEventListener("change", show); await show();
    } catch (_) { if (live(ticket)) body.replaceChildren(el("p", "meta", "버전 목록을 읽지 못했어요. 접었다 다시 펼쳐 주세요.")); }
  }
  panel.addEventListener("toggle", () => { if (panel.open) void load(); else { epoch++; body.replaceChildren(); } });
  if (mode) panel.open = true;
  return panel;
};
