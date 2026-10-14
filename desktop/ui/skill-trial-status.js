"use strict";
window.ArkismSkillTrialStatus = ({ state, el, button, request }) => (skillID, trialID, candidateID) => {
  const panel=el("section","skill-trial-status"), body=el("div");
  const scope={...state.scope}, owner=JSON.stringify([state.server,state.token,scope,skillID]);let epoch=0;
  const states={active:["시험 진행 중","실행과 독립 평가가 모두 확인되어야 다음 단계로 넘어가요."],invalidated:["시험 조건 변경","근거나 기준판이 달라져 현재 시험 후보를 사용할 수 없어요."],stopped:["시험 종료","종료는 시험 통과를 뜻하지 않아요."],stopped_regression:["회귀 관측으로 시험 중단","문제가 관측되어 시험 후보 사용이 중단됐어요."],expired:["시험 기간 종료","기간이 끝났다는 이유만으로 성공 처리하지 않아요."],promoted:["기본 버전으로 적용됨","승격 뒤 회귀 여부는 계속 별도 관측으로 확인해요."],recovery_pending:["회귀 버전 차단 · 복구 미완료","문제가 있는 버전은 차단됐지만 기준판 복구는 아직 확인되지 않았어요."],rolled_back:["기준판으로 복구됨","서버에서 기준판 선택을 확인했어요. 이미 실행 중인 작업의 복구를 뜻하지는 않아요."]};
  const schedules={pending:"확인 대기",running:"자동 확인 중",retry:"다음 확인 대기",failed:"자동 확인 실패 · 재확인 필요",done:"확인 작업 종료"};
  const sameScope=value=>["workspace_id","project_id","user_id","agent_id"].every(k=>(value?.[k]||"")===(scope[k]||""));
  const live=t=>panel.isConnected && epoch===t && state.view==="skills" && state.selected===`skill:${skillID}` && owner===JSON.stringify([state.server,state.token,state.scope,skillID]);
  async function load(){const ticket=++epoch;body.replaceChildren(el("p","meta","현재 처리 상태를 확인하는 중…"));
    try{const value=await request(`/v1/skill-trials/${encodeURIComponent(trialID)}/status`,{method:"POST",body:JSON.stringify({scope,skill_id:skillID})});if(!live(ticket))return;
      if(value.authority!=="data_only_untrusted"||!sameScope(value.scope)||value.skill_id!==skillID||value.trial?.collection!=="skill_trials"||value.trial.id!==trialID||!Number.isSafeInteger(value.trial.revision)||value.candidate?.collection!=="skill_versions"||value.candidate.id!==candidateID||!Object.hasOwn(states,value.state)||value.observation_state!=="not_available")throw Error("invalid_status");
      const [title,explanation]=states[value.state];body.replaceChildren(el("strong","",title),el("p","meta",explanation),el("p","meta","독립 평가 결과는 이 조회에서 확인되지 않아요."));
      if(value.scheduler){const job=value.scheduler;if(!Object.hasOwn(schedules,job.status)||!Number.isSafeInteger(job.attempts)||job.attempts<0)throw Error("invalid_schedule");body.append(el("p","meta",`자동 확인   ${schedules[job.status]}`));if(job.attempts>0)body.append(el("p","meta",`현재 시도   ${job.attempts}회`));if(job.next_check&&["pending","retry"].includes(job.status)){const date=new Date(job.next_check);if(!Number.isFinite(date.getTime()))throw Error("invalid_date");body.append(el("p","meta",`다음 확인   ${date.toLocaleString(globalThis.ArkismI18n?.locale() || "ko-KR")}`));}}
      else body.append(el("p","meta","자동 확인 작업이 등록되어 있지 않아요."));
    }catch(_){if(live(ticket))body.replaceChildren(el("strong","","처리 상태를 확인하지 못했어요"),el("p","meta","권한이나 서버 상태를 확인한 뒤 다시 조회해 주세요."));}
  }
  panel.append(el("h4","","자동 처리 상태"),body,button("처리 상태 다시 조회","small",null,load));
  queueMicrotask(load);return panel;
};
