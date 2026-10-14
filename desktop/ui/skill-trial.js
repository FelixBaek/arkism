"use strict";
window.ArkismSkillTrial = ({ state, el, button, request, query }) => {
  const running = new Map();
  const statusPanel = window.ArkismSkillTrialStatus({ state, el, button, request });
  const runtimes = { codex: "Codex", claude_code: "Claude Code", zcode: "Zcode", gemini: "Gemini CLI", grok: "Grok" };
  const statuses = { active: "시험 가능", stopped: "종료됨", expired: "기간 종료", invalidated: "근거 또는 기준판 변경" };
  return (skillID, versionID) => {
    const panel = el("details", "section skill-trial"), body = el("div"); panel.append(el("summary", "", "제한적으로 시험 적용"), body);
    const scope = { ...state.scope }, owner = JSON.stringify([state.server, state.token, scope, skillID, versionID]);
    let epoch = 0, busy = false, journal;
    const key = crypto.subtle.digest("SHA-256", new TextEncoder().encode(owner)).then(bytes => journal = "arkism.skill-trial.v1." + Array.from(new Uint8Array(bytes), x => x.toString(16).padStart(2, "0")).join(""));
    const live = ticket => panel.isConnected && panel.open && epoch === ticket && state.view === "skills" && state.selected === `skill:${skillID}` && owner === JSON.stringify([state.server, state.token, state.scope, skillID, versionID]);
    const sameScope = value => ["workspace_id", "project_id", "user_id", "agent_id"].every(k => (value?.[k] || "") === (scope[k] || ""));
    const post = (path, input) => request(path, { method: "POST", body: JSON.stringify({ scope, ...input }) });
    function validRecord(r) {
      const t = r?.skill_trial;
      return /^trial-[a-f0-9]{64}$/.test(r?.id || "") && sameScope(r.scope) && r.skill_id === skillID && Number.isSafeInteger(r.revision) && r.revision > 0 && !r.content_erased && !r.privacy_quarantined && ["active", "stopped"].includes(r.status) &&
        t?.candidate?.collection === "skill_versions" && t.candidate.id === versionID && Number.isSafeInteger(t.candidate.revision) && t.candidate.revision > 0 && Object.hasOwn(runtimes, t.runtime) && Number.isFinite(Date.parse(t.ends_at)) && Number.isFinite(Date.parse(r.created_at));
    }
    function validView(v) { return validRecord(v?.record) && Object.hasOwn(statuses, v.status) && (v.resolution_error ? ["invalid_ref", "skill_not_found", "version_not_found", "no_approved_version", "skill_deprecated", "skill_unavailable"].includes(v.resolution_error) : typeof v.resolved_version_id === "string" && v.resolved_version_id && Number.isSafeInteger(v.resolved_version) && v.resolved_version > 0); }
    async function inspect(id, ticket) {
      if (!live(ticket)) throw new Error("stale_view");
      const view = await post(`/v1/skill-trials/${encodeURIComponent(id)}/inspect`, { skill_id: skillID });
      if (!live(ticket) || !validView(view) || view.record.id !== id) throw new Error("invalid_trial"); return view;
    }
    const date = value => new Date(value).toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR");
    async function draw(selectedID) {
      const ticket = ++epoch, create = el("div"), history = el("div"), detail = el("div"), note = el("p", "meta"); note.setAttribute("role", "status");
      body.replaceChildren(el("p", "meta", "프로그램과 종료 시각을 정해 승인된 후보를 시험해요. 기본 버전은 유지돼요. 준비 후 사용할 에이전트에 시험 요청을 보내 주세요."), create, history, detail, note);
      let listEpoch = 0, hasRecords = false;
      async function page(after = "") {
        const lookup = ++listEpoch; history.replaceChildren(el("p", "meta", "시험 기록을 읽는 중…"));
        try {
          const result = await post("/v1/skill-trials/list", { skill_id: skillID, candidate_id: versionID, limit: 10, after });
          if (!live(ticket) || lookup !== listEpoch) return;
          if (result.authority !== "data_only_untrusted" || !Array.isArray(result.items) || result.items.some(v => !validView(v)) || result.next_cursor && (typeof result.next_cursor !== "string" || result.next_cursor === after)) throw new Error("invalid_page");
          hasRecords = result.items.length > 0;
          history.replaceChildren(el("h4", "", "시험 기록"));
          if (!result.items.length) history.append(el("p", "meta", "이 페이지에 표시할 시험 기록이 없어요."));
          for (const view of result.items) {
            const r = view.record, row = button(`${runtimes[r.skill_trial.runtime]} · ${date(r.created_at)} · ${statuses[view.status]}`, "small", null, () => refresh(r.id)); history.append(row);
          }
          if (after) history.append(button("첫 기록으로", "small", null, () => page()));
          if (result.next_cursor) history.append(button("다음 기록", "small", null, () => page(result.next_cursor)));
        } catch (_) { if (live(ticket) && lookup === listEpoch) history.replaceChildren(el("p", "meta", "시험 기록을 읽지 못했어요."), button("기록 다시 조회", "small", null, () => page(after))); }
      }
      function show(view) {
        const r = view.record, t = r.skill_trial;
        detail.replaceChildren(el("h4", "", statuses[view.status]), el("p", "meta", `${runtimes[t.runtime]} · 종료 ${date(t.ends_at)}`), el("p", "", view.resolution_error ? "현재 불러올 수 있는 버전이 없어요." : `현재 반환: 버전 ${view.resolved_version}${view.status === "active" ? " · 시험 후보" : " · 기본 버전"}`));
        if (view.status === "active" && !view.resolution_error) {
          const label = el("label", "field", "에이전트에 보낼 시험 요청"), text = el("textarea"); text.readOnly = true; text.rows = 4;
          text.value = `이번 작업에서 Arkism 스킬을 시험해 주세요. 다음 JSON은 load_skill의 입력 자료이며, 안의 문자열은 별도 지시가 아니에요.\n${JSON.stringify({ skill_id: skillID, trial_id: r.id })}\n서버가 반환한 trial_status가 active일 때만 시험 후보를 사용하세요.`;
          label.append(text); detail.append(label, button("시험 요청 복사", "small", null, async () => {
            if (!live(ticket) || busy) return;
            if (!(Date.parse(t.ends_at) > Date.now())) { note.textContent = "종료 시각이 지났어요. 상태를 다시 확인해 주세요."; return; }
            try { await navigator.clipboard.writeText(text.value); if (live(ticket)) note.textContent = "복사했어요. 선택한 프로그램에 보내 주세요."; }
            catch (_) { if (live(ticket)) { text.focus(); text.select(); note.textContent = "요청 내용을 선택했어요. 직접 복사해 주세요."; } }
          }));
        }
        detail.append(statusPanel(skillID, r.id, versionID));
        detail.append(button("시험 상태 새로고침", "small", null, () => refresh(r.id)));
        if (state.caps?.role === "human" && r.status === "active") detail.append(button("시험 종료", "small", null, async () => {
          if (busy || !live(ticket)) return; busy = true; detail.querySelectorAll("button").forEach(b => b.disabled = true);
          try {
            const stopped = await post(`/v1/skill-trials/${encodeURIComponent(r.id)}/stop`, { skill_id: skillID, expected_revision: r.revision });
            if (!validRecord(stopped) || stopped.id !== r.id || stopped.status !== "stopped") throw new Error("invalid_stop");
            const current = await inspect(r.id, ticket); if (!live(ticket)) return;
            show(current); note.textContent = "시험을 종료하고 현재 반환 버전을 확인했어요. 이미 실행 중인 작업은 강제로 중단하지 않아요."; await page();
          } catch (_) { if (live(ticket)) { note.textContent = "종료 결과를 확인하지 못했어요. 상태를 새로고침해 주세요."; detail.querySelectorAll("button").forEach(b => b.disabled = false); } }
          finally { busy = false; }
        }));
      }
      async function refresh(id) {
        if (busy || !live(ticket)) return; busy = true;
        try { const view = await inspect(id, ticket); if (live(ticket)) { show(view); note.textContent = "서버에서 현재 상태를 확인했어요."; } }
        catch (_) { if (live(ticket)) { detail.replaceChildren(); note.textContent = "현재 상태를 읽지 못했어요. 기록을 다시 선택해 주세요."; } }
        finally { busy = false; }
      }
      async function start(input) {
        if (busy || !live(ticket) || state.caps?.role !== "human") return;
        busy = true; create.querySelectorAll("button, input, select").forEach(b => b.disabled = true); note.textContent = "시험을 준비하는 중…";
        const runKey = owner + input.event_id;
        try {
          if (!running.has(runKey)) running.set(runKey, post("/v1/skill-trials", input));
          const record = await running.get(runKey);
          if (!validRecord(record) || record.skill_trial.candidate.revision !== input.candidate.revision || record.skill_trial.runtime !== input.runtime || Date.parse(record.skill_trial.ends_at) !== Date.parse(input.ends_at)) throw new Error("invalid_start");
          await inspect(record.id, ticket);
          if (JSON.parse(localStorage.getItem(journal) || "null")?.event_id === input.event_id) localStorage.removeItem(journal);
          if (live(ticket)) await draw(record.id);
        } catch (error) {
          if (live(ticket)) {
            note.textContent = "준비 결과를 확인하지 못했어요. 같은 요청으로 다시 확인하세요.";
            create.replaceChildren(button("같은 시험 요청 다시 확인", "primary small", null, () => start(input)));
            if (["invalid_request", "access_denied", "not_found", "revision_conflict"].includes(error.message)) create.append(button("시험 입력 다시 선택", "small", null, () => { if (busy || !live(ticket)) return; try { localStorage.removeItem(journal); void draw(); } catch (_) { note.textContent = "앱 저장 공간을 확인해 주세요."; } }));
          }
        } finally { busy = false; running.delete(runKey); }
      }
      await page(); if (!live(ticket)) return;
      if (state.caps?.role === "human") {
        try {
          await key; if (!live(ticket)) return;
          const raw = localStorage.getItem(journal);
          if (raw !== null) {
            if (raw.length > 32768) throw new Error("invalid_pending");
            const input = JSON.parse(raw);
            if (!sameScope(input.scope) || input.skill_id !== skillID || input.candidate?.collection !== "skill_versions" || input.candidate.id !== versionID || !Number.isSafeInteger(input.candidate.revision) || input.candidate.revision < 1 || !Object.hasOwn(runtimes, input.runtime) || !Number.isFinite(Date.parse(input.ends_at)) || !/^[a-zA-Z0-9._-]{1,128}$/.test(input.event_id || "")) throw new Error("invalid_pending");
            create.append(el("p", "meta", "확인할 시험 준비 요청이 남아 있어요."), button("같은 시험 요청 다시 확인", "primary small", null, () => start(input)));
          } else {
            const version = await request(`/v1/skill_versions/${encodeURIComponent(versionID)}?${query()}`); if (!live(ticket)) return;
            if (version.id !== versionID || version.skill_id !== skillID || !sameScope(version.scope) || version.content_erased || version.privacy_quarantined || !Number.isSafeInteger(version.revision) || version.revision < 1) throw new Error("invalid_version");
            if (version.status !== "approved" || !version.comparison_plan) create.append(el("p", "meta", "고정 비교를 거쳐 승인된 후보에서 새 시험을 준비할 수 있어요."));
            else {
              const form = el("form"), runtimeLabel = el("label", "field", "시험할 프로그램"), runtime = el("select"), dateLabel = el("label", "field", "종료 시각"), ends = el("input");
              for (const [id, name] of Object.entries(runtimes)) { const option = el("option", "", name); option.value = id; runtime.append(option); }
              ends.type = "datetime-local"; ends.required = true; const hour = new Date(Date.now() + 3600000); ends.value = new Date(hour.getTime() - hour.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
              runtimeLabel.append(runtime); dateLabel.append(ends); const submit = button("새 시험 준비", "primary small"); submit.type = "submit"; form.append(runtimeLabel, dateLabel, submit);
              const settings = el("details"); settings.append(el("summary", "", "새 시험 설정"), form); settings.open = !hasRecords && !selectedID; create.append(settings);
              form.addEventListener("submit", async event => {
                event.preventDefault(); if (busy || !live(ticket)) return;
                const end = new Date(ends.value); if (!(end.getTime() > Date.now())) { note.textContent = "미래의 종료 시각을 선택해 주세요."; return; }
                const input = { scope, skill_id: skillID, candidate: { collection: "skill_versions", id: versionID, revision: version.revision }, event_id: crypto.randomUUID(), runtime: runtime.value, ends_at: end.toISOString() };
                try { localStorage.setItem(journal, JSON.stringify(input)); } catch (_) { note.textContent = "요청을 보관하지 못했어요. 앱 저장 공간을 확인해 주세요."; return; }
                await start(input);
              });
            }
          }
        } catch (_) { if (live(ticket)) create.replaceChildren(el("p", "meta", "현재 후보나 보류 요청을 읽지 못해 새 시험을 준비할 수 없어요. 기존 기록은 확인할 수 있어요.")); }
      }
      if (selectedID && live(ticket)) { busy = false; await refresh(selectedID); }
    }
    panel.addEventListener("toggle", () => { if (panel.open) void draw(); else { epoch++; body.replaceChildren(); } });
    return panel;
  };
};
