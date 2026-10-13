window.ArkismSkillExecution = ({ state, el, button, tauri }) => {
 const pending = new Map();
 const trimSlashes = (text) => { let end = text.length; while (end > 0 && text[end - 1] === "/") end--; return text.slice(0, end); };
 return (version, refresh) => {
  const box = el("details", "section"), summary = el("summary", "", "고정 과제로 비교 실행"), body = el("div"); box.append(summary, body);
  const owner = JSON.stringify([state.server, state.token, state.scope, state.selected]);
  let epoch = 0, host = state.defaults.ssh_host, busy = false, current = null;
  const live = ticket => box.isConnected && box.open && epoch === ticket && state.view === "skills" && owner === JSON.stringify([state.server, state.token, state.scope, state.selected]);
  const base = { scope: { ...state.scope }, server: trimSlashes(state.server) }, target = { id: version.id, revision: version.revision };
  const pendingKey = () => JSON.stringify([owner, version.id, version.revision, host]);
  const known = new Set(["running", "published", "awaiting_publication", "interrupted_or_failed"]);
  async function call(action, extra = {}) {
    const reply = await tauri.invoke("skill_execution", { host, request: { ...base, action, ...extra } });
    if (!reply || reply.error || reply.authority !== "data_only_untrusted") throw new Error("execution_unavailable");
    return reply;
  }
  function validJob(value) { return known.has(value?.status) && /^[a-f0-9]{64}$/.test(value?.job_id); }
  async function draw() {
    const ticket = ++epoch;
    if (!tauri || state.caps?.role !== "human") { body.replaceChildren(el("p", "meta", "관리 계정의 데스크톱 앱에서 비교를 실행할 수 있어요.")); return; }
    if (version.status !== "candidate" || !version.comparison_plan) { body.replaceChildren(el("p", "meta", "고정 비교 계획이 있는 검토 대기 버전에서 실행할 수 있어요.")); return; }
    const label = el("label", "field", "평가 서버 (SSH)"), input = el("input"), note = el("p", "meta"), content = el("div"); input.value = host; input.type = "text"; label.append(input); note.setAttribute("role", "status");
    const find = button("평가 설정과 실행 기록 조회", "small", null, lookup);
    body.replaceChildren(el("p", "meta", "앱의 API 연결과 일치하는 평가 서버에서, 등록된 고정 과제와 실행 조건만 사용해요. 실행 비용이 발생할 수 있어요."), label, find, content, note);
    input.addEventListener("input", () => { host = input.value.trim(); current = null; content.replaceChildren(); note.textContent = "서버를 다시 조회해 주세요."; });
    async function lookup() {
      if (busy || !live(ticket)) return;
      if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$/.test(host)) { note.textContent = "SSH 서버 이름을 확인해 주세요."; return; }
      busy = true; input.disabled = find.disabled = true; content.replaceChildren(); note.textContent = "설정과 실행 기록을 확인하는 중…";
      try {
        const history = await call("history", { candidate: target });
        if (!live(ticket)) return;
        if (!Array.isArray(history.items) || history.items.some(row => !validJob(row))) throw new Error("invalid_history");
        if (history.items.length) {
          const selectLabel = el("label", "field", "비교 실행 기록"), select = el("select"), result = el("div");
          history.items.forEach((row, i) => { const option = el("option", "", `${row.created_at} · ${row.profile_id}`); option.value = String(i); select.append(option); }); selectLabel.append(select);content.append(selectLabel,result);
          const show = () => { current = history.items[Number(select.value)]; paintJob(result, current); };
          select.addEventListener("change", show);show();note.textContent = "기존 실행 기록을 불러왔어요.";return;
        }
        const profiles = await call("profiles"); if (!live(ticket)) return;
        if (!Array.isArray(profiles.items)) throw new Error("invalid_profiles");
        const matched = profiles.items.filter(row => /^[A-Za-z0-9_.-]{1,128}$/.test(row.id) && typeof row.name === "string" && row.dataset_sha256 === version.comparison_plan.dataset_sha256 && row.environment_sha256 === version.comparison_plan.environment_sha256);
        if (!matched.length) { note.textContent = "이 버전의 고정 과제·실행 조건과 일치하는 평가 설정이 없어요.";return; }
        const label = el("label", "field", "평가 설정"), select = el("select");matched.forEach(row => { const option=el("option","",`${row.name} · ${row.cases}개 과제`);option.value=row.id;select.append(option); });label.append(select);
        current = pending.get(pendingKey()) || null;
        select.dataset.executionProfile = "true";
        if (current?.event_id) { select.value = current.profile_id; select.disabled = true; }
        const start = button("비교 실행", "primary small", null, () => {
          if (busy || !live(ticket)) return;
          current ||= pending.get(pendingKey()) || { event_id: crypto.randomUUID(), profile_id: select.value };
          pending.set(pendingKey(), current);
          select.disabled = true; act("start", { candidate: target, event_id: current.event_id, profile_id: current.profile_id }, content);
        });content.append(label,start);note.textContent = "동일 과제로 기존 버전과 후보를 비교해요. 실행만으로 승인·적용되지는 않아요.";
      } catch (_) { if (live(ticket)) note.textContent = "불러오지 못했어요. SSH 연결과 평가 서버의 범위·API 설정을 확인해 주세요."; }
      finally { busy = false;if(live(ticket)) input.disabled = find.disabled = false; }
    }
    function paintJob(targetNode, job) {
      targetNode.replaceChildren();
      const text = { running: "비교 실행 중이에요.", awaiting_publication: "측정은 끝났지만 평가 저장이 완료되지 않았어요.", published: "비교 결과를 저장했어요. 승인·적용은 별도로 진행해 주세요.", interrupted_or_failed: "저장된 결과 없이 실행이 끝났거나 중단됐어요. 재실행 전에 서버에서 실행 상태를 확인해 주세요." };
      targetNode.append(el("p", "meta", text[job.status]));
      if (job.status === "published") {
        if (job.report && typeof job.passed === "boolean") targetNode.append(el("p", "", `비교 기준 ${job.passed ? "통과" : "미달"} · 기존 점수 ${job.report.baseline_score} → 후보 점수 ${job.report.candidate_score}`));
        targetNode.append(button("평가 다시 읽기", "small", null, refresh));
      } else if (job.status === "awaiting_publication") targetNode.append(button("모델 재실행 없이 저장 재시도", "small", null, () => act("publish", { job_id: job.job_id }, targetNode)));
      targetNode.append(button("상태 확인", "small", null, () => act("status", { job_id: job.job_id }, targetNode)));
    }
    async function act(action, extra, targetNode) {
      if (busy || !live(ticket)) return;
      busy=true; input.disabled=find.disabled=true;body.querySelectorAll("button,select").forEach(node=>node.disabled=true);note.textContent="실행 상태를 확인하는 중…";
      try { const result=await call(action,extra);if(!live(ticket))return;if(!validJob(result))throw new Error("invalid_job");current={...current,...result};pending.set(pendingKey(),current);paintJob(targetNode,result);note.textContent=""; }
      catch (_) { if(live(ticket))note.textContent="결과를 확인하지 못했어요. 같은 버튼으로 다시 확인하세요. 같은 실행 요청은 중복 실행하지 않아요."; }
      finally { busy=false;if(live(ticket)){ input.disabled=find.disabled=false;body.querySelectorAll("button,select").forEach(node=>node.disabled=node.dataset.executionProfile === "true" && Boolean(current?.event_id)); } }
    }
  }
  box.addEventListener("toggle", () => { if(box.open)draw();else{epoch++;body.replaceChildren();} });return box;
};
};
