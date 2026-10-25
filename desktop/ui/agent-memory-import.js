// @ts-check
// Settings › Advanced › 에이전트 기억 가져오기: a dry run, a confirm, then the real import. A missing reply is a failure, not a crash.
(function (root) {
  "use strict";
  const reply = value => { if (!value || typeof value !== "object") throw new Error("import_reply_invalid"); return value; };
  async function run({ invoke, ask, notice }) {
    let plan;
    try { plan = reply(await invoke("import_agent_memories", { dryRun: true })); } catch (_) { notice("기억 파일을 읽지 못했어요."); return; }
    const n = plan.statements || 0;
    if (!n) { notice("새로 가져올 기억이 없어요."); return; }
    if (!await ask(`기억 ${n}개를 파일 ${plan.files || 0}개에서 찾았어요. 검토 대기로 가져올까요?`)) return;
    notice("가져오는 중이에요.");
    try {
      const r = reply(await invoke("import_agent_memories", { dryRun: false }));
      const added = r.candidates ?? r.sent ?? 0;
      notice(r.failed ? `검토 대기에 ${added}개를 올렸어요. ${r.failed}개는 실패했어요.` : `검토 대기에 ${added}개를 올렸어요.`);
    } catch (_) { notice("가져오지 못했어요. 잠시 뒤 다시 시도해 주세요."); }
  }
  if (typeof window === "undefined" && typeof module === "object" && module.exports) module.exports = { run };
  else root.ArkismAgentMemoryImport = { run };
})(typeof window === "object" ? window : globalThis);
