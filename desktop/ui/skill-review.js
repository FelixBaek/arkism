// @ts-check
window.ArkismSkillReview = ({ state, el, button, request, query, reload, execution, trial }) => (sk, mode, side) => {
  "use strict";
  // mode "" shows everything; the 개선하기 steps show one part each: "compare" (비교 실행), "review" (평가·승인), "trial" (시험 적용).
  // side: the page's right panel ({ box, extra, go, section, facts, table }); 검토·승인 puts its decision there (Figma 198:4070).
  const panel = el("details", mode ? "section improve-part" : "section"), summary = el("summary", "", { compare: "비교 평가", review: "검토·승인", trial: "시험 적용" }[mode] || "개선안과 버전 검토"), body = el("div");
  const opened = (part) => { part.open = true; return part; };
  panel.append(summary, body);
  const owner = JSON.stringify([state.server, state.token, state.scope, state.view, state.selected]);
  const loads = globalThis.ArkismKeep.latest(); // the skill read and each version pick share one turn
  const live = ticket => panel.isConnected && panel.open && ticket.live() && owner === JSON.stringify([state.server, state.token, state.scope, state.view, state.selected]);
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
  // What changed, line by line: removed lines on the left, added lines on the right (order kept, not a full diff).
  const changed = (before, after) => {
    const lines = text => text.split("\n").map(line => line.trim()).filter(Boolean), a = lines(before), b = lines(after), inA = new Set(a), inB = new Set(b);
    const gone = a.filter(line => !inB.has(line)), added = b.filter(line => !inA.has(line));
    return Array.from({ length: Math.max(gone.length, added.length) }, (_, i) => [[gone[i] || "", "quiet"], added[i] || ""]);
  };
  const yes = value => value === true ? "통과" : value === false ? "실패" : "판정 없음";
  async function load(preferred) {
    const ticket = loads.begin();
    body.replaceChildren(el("p", "meta", "버전과 평가를 불러오는 중…"));
    try {
      const skill = await read("skills", sk.id);
      if (!live(ticket)) return;
      if (!Array.isArray(skill.version_history) || !skill.version_history.length || skill.version_history.some(id => typeof id !== "string") || new Set(skill.version_history).size !== skill.version_history.length) throw new Error("invalid_history");
      const label = el("label", "field", { compare: "비교 대상", trial: "시험할 버전" }[mode] || "검토할 버전"), select = el("select"), detail = el("div"), trials = el("div");
      const number = id => skill.version_history.indexOf(id) + 1;
      for (const id of skill.version_history) {
        const option = el("option", "", `버전 ${number(id)}${id === skill.active_version ? " · 사용 중" : ""}`); option.value = id; select.append(option);
      }
      select.value = skill.version_history.includes(preferred) ? preferred : skill.version_history.at(-1);
      label.append(select);
      body.replaceChildren(...(mode ? [] : [el("p", "meta", "스킬 원문과 평가는 검토 자료예요. 조회만으로 승인되거나 실행되지 않아요.")]), label, trials, detail);
      const show = async () => {
        const versionTicket = loads.begin(), id = select.value;
        if (mode) body.replaceChildren(label, trials, detail); // 비교 평가 moves the field into its own row; bring it back while reading
        if (mode === "trial") { trials.replaceChildren(opened(trial(skill.id, id, side))); detail.replaceChildren(); return; }
        trials.replaceChildren(mode ? "" : trial(skill.id, id, side));
        detail.replaceChildren(el("p", "meta", "평가를 읽는 중…"));
        try {
          const version = await read("skill_versions", id);
          if (!live(versionTicket)) return;
          if (version.skill_id !== skill.id || typeof version.text !== "string" || !Object.hasOwn(status, version.status)) throw new Error("invalid_version");
          if (mode === "compare") {
            const option = [...select.querySelectorAll("option")].find(o => o.value === id);
            if (option && skill.version_history.includes(version.base_version)) option.textContent = `기존 버전 ${number(version.base_version)} ↔ 후보 버전 ${number(id)}`;
            detail.replaceChildren(opened(execution(version, () => load(id), side, label))); return;
          }
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
          const human = state.caps?.role === "human", n = number(id);
          const self = ev => ev.evaluation?.eval_type === "model_self";
          const approvable = ev => human && version.status === "candidate" && !self(ev) && ev.evaluation?.passed === true && ev.created_by !== version.created_by;
          const restorable = ev => human && version.status === "superseded" && !version.validation_id && !self(ev) && ev.evaluation?.passed === true;
          detail.replaceChildren(el("h4", "", `버전 ${n} · ${status[version.status]}`));
          if (baseline) {
            const rows = changed(baseline.text, version.text);
            detail.append(rows.length ? side.table([`기존 버전 ${number(baseline.id) || "?"}`, `후보 버전 ${n}`], rows.map(cells => [cells])) : el("p", "meta", "기존 버전과 바뀐 줄이 없어요."));
          }
          detail.append(passage("버전 원문", version.text));
          if (baseline) detail.append(passage("기존 버전 원문", baseline.text));
          // 평가 결과: one evaluation at a time; the approvable one first.
          let picked = records.find(approvable) || records.find(restorable) || records[0] || null;
          const note = el("p", "meta", records.length ? "" : "이 버전에 연결된 평가가 없어요."); note.setAttribute("role", "status");
          const paintEvaluation = () => {
            if (!picked) { side.extra.replaceChildren(); return; }
            const card = el("section", "v4s-card"), data = picked.evaluation, r = data?.comparison_report;
            card.append(el("h3", "", "평가 결과"));
            if (records.length > 1) {
              const field = el("label", "field", "평가"), choose = el("select");
              records.forEach((ev, i) => { const option = el("option", "", `${self(ev) ? "모델 자기평가" : "평가 기록"} · ${yes(ev.evaluation?.passed)}`); option.value = String(i); choose.append(option); });
              choose.value = String(records.indexOf(picked)); field.append(choose); card.append(field);
              choose.addEventListener("change", () => { picked = records[Number(choose.value)]; paintEvaluation(); paintDecision(); });
            }
            card.append(el("p", "meta", `${self(picked) ? "모델 자기평가" : "평가 기록"} · ${yes(data?.passed)}`));
            // cost and time read "$0.24 / 32초"; reports from before the baseline totals were measured show "—"
            const spent = (cost, ms) => [`$${(+cost || 0).toFixed(2)} / ${+(ms / 1000).toFixed(1)}초`, "mono"];
            if (r) card.append(side.table(["항목", "기존", "후보"], [
              [["점수", [String(r.baseline_score), "mono"], [String(r.candidate_score), r.candidate_score > r.baseline_score ? "mono up" : "mono"]]],
              [["비용 / 시간", r.baseline_duration_ms ? spent(r.baseline_cost, r.baseline_duration_ms) : "—", spent(r.candidate_cost, r.candidate_duration_ms)]],
              [["회귀 / 안전", "—", `${yes(r.regression_passed)} / ${yes(r.safety_passed)}`]]]));
            const more = el("details"); more.append(el("summary", "", "평가 자세히"), el("p", "original-text", picked.text || "저장된 내용이 없어요."));
            const plan = version.comparison_plan;
            if (plan) more.append(el("p", "meta", `통과 기준 · 점수 ${+(plan.min_score_gain * 100).toFixed(2)}점 이상 향상 · ${plan.max_cost >= 1e6 ? "비용 제한 없음" : `비용 $${(+plan.max_cost).toFixed(2)} 이하`} · ${+(plan.max_duration_ms / 60000).toFixed(1)}분 이하`));
            card.append(more); side.extra.replaceChildren(card);
          };
          const paintDecision = () => {
            const title = version.status === "candidate" ? `버전 ${n} 후보를 승인할까요?` : version.status === "approved" ? `승인된 버전 ${n}, 지금 적용할까요?` : `버전 ${n} · ${status[version.status]}`;
            const decide = el("div", "v4s-actions"), apply = el("div", "v4s-actions"), turn = side.section("적용과 되돌리기");
            const action = (labelText, cls, enabled, path, input) => { const b = button(labelText, cls, null, () => act(path, input)); b.disabled = !enabled; return b; };
            decide.append(
              action("이 평가로 승인", "primary small", picked && approvable(picked), `/v1/skill_versions/${encodeURIComponent(id)}/transition`, { expected_revision: version.revision, status: "approved", validation_id: picked?.id }),
              action("후보 제외", "small", human && version.status === "candidate", `/v1/skill_versions/${encodeURIComponent(id)}/transition`, { expected_revision: version.revision, status: "rejected" }));
            apply.append(
              action("이 버전 적용", "small", human && version.status === "approved", `/v1/skills/${encodeURIComponent(skill.id)}/apply`, { expected_revision: skill.revision, version_id: id }),
              action("이 버전으로 복귀", "ghost small", human && version.status === "superseded" && Boolean(version.validation_id), `/v1/skills/${encodeURIComponent(skill.id)}/rollback`, { expected_revision: skill.revision, version_id: id }),
              action("과거 판으로 복귀", "ghost small", picked && restorable(picked), `/v1/skills/${encodeURIComponent(skill.id)}/rollback`, { expected_revision: skill.revision, version_id: id, validation_id: picked?.id }));
            turn.append(apply);
            side.box.replaceChildren(el("h2", "", title), el("p", "v4s-lead", "다른 사람이 만든 통과 평가가 있어야 승인할 수 있어요."), decide, turn, note, el("p", "meta", "관리 계정에서만 쓸 수 있어요."));
          };
          paintEvaluation(); paintDecision();
          if (!mode) detail.append(execution(version, () => load(id), side));
          async function act(path, input) {
            if (!live(versionTicket)) return;
            side.box.querySelectorAll("button").forEach(b => b.disabled = true); select.disabled = true;
            try {
              await request(path, { method: "POST", body: JSON.stringify({ scope: state.scope, ...input }) });
              if (live(versionTicket)) {
                if (path.endsWith("/apply") || path.endsWith("/rollback")) await reload();
                else await load(id);
              }
            } catch (_) {
              if (live(versionTicket)) { note.textContent = "반영 결과를 확인하지 못했어요. 권한·평가 기준·기록 변경 여부를 확인하려면 다시 조회하세요."; side.box.append(button("다시 조회", "small", null, () => load(id))); }
            } finally { if (live(versionTicket)) select.disabled = false; }
          }
        } catch (_) { if (live(versionTicket)) { side.extra.replaceChildren(); detail.replaceChildren(el("p", "meta", "버전이나 평가를 읽지 못했어요. 권한·삭제·기록 변경 여부를 확인하고 다시 조회하세요."), button("다시 조회", "small", null, () => load(id))); } }
      };
      select.addEventListener("change", show); await show();
    } catch (_) { if (live(ticket)) body.replaceChildren(el("p", "meta", "버전 목록을 읽지 못했어요. 접었다 다시 펼쳐 주세요.")); }
  }
  panel.addEventListener("toggle", () => { if (panel.open) void load(); else { loads.cancel(); body.replaceChildren(); } });
  if (mode) panel.open = true;
  return panel;
};
