// AI 도움 (fill·polish·suggest·draft·install_skill·explain·comfy·fix): the desktop asks Arkism, the host runner
// answers with a fill persona. Saved personas (crew type persona) double as the personas workflow AI nodes load.
// The first JSON object or array in a model's answer: from the first { or [ to the last } or ].
function parseJSON(t = "") {
  const start = t.search(/[[{]/);
  const end = Math.max(t.lastIndexOf("}"), t.lastIndexOf("]"));
  if (start < 0 || end < start) return null;
  try { return JSON.parse(t.slice(start, end + 1)); } catch { return null; } // not JSON: the caller says so
}

window.ArkismAssist = (ctx) => {
  "use strict";
  const { state, request, query } = ctx;
  // Fill personas (13). Built-ins are used until the owner saves their own for that purpose.
  const BUILTIN_PERSONAS = [
    { id: "builtin:workflow", crew: { type: "persona", name: "워크플로우 설계자", role: "workflow", instructions: "너는 워크플로우 설계자다. 할 일을 단계로 나누고, if 문으로 판정되는 칸은 코드로, 판단이 필요한 칸만 AI로 둔다." } },
    { id: "builtin:skill", crew: { type: "persona", name: "스킬 제작 전문가", role: "skill", instructions: "너는 스킬 제작 전문가다. 반복된 요청을 SKILL.md로 만들고, 단계는 5개 이하로 쓴다." } },
    { id: "builtin:prompt", crew: { type: "persona", name: "프롬프트 작가", role: "prompt", instructions: "너는 프롬프트 작가다. 목적과 톤을 묻고, 바꿔 넣을 값을 {변수}로 뺀다." } },
    { id: "builtin:category", crew: { type: "persona", name: "카테고리 분류가", role: "category", instructions: "너는 분류가다. 스킬 이름·설명·사용 기록으로 가장 가까운 카테고리 하나를 고른다." } }
  ];
  const saved = () => (state.data.crew || []).filter((r) => r.crew.type === "persona");
  const personas = () => [...saved(), ...BUILTIN_PERSONAS];
  const personaFor = (purpose) => personas().find((p) => p.crew.role === purpose) || BUILTIN_PERSONAS[0];

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  async function assist(kind, prompt, opts = {}) {
    // A cut would break install_skill's JSON; free text is cut, structured requests are refused when too long.
    if (["install_skill", "fix", "comfy"].includes(kind) && prompt.length > 20000) throw new Error("내용이 너무 길어요 (2만 자까지)");
    const body = { kind, prompt: prompt.slice(0, 20000), persona: (opts.persona || personaFor(opts.purpose || "workflow")).crew.instructions, ...(opts.model ? { model: opts.model } : {}), ...(opts.expert ? { expert: opts.expert } : {}) }; // try: the agent's @이름
    const r = await request("/v1/crew/assists", { method: "POST", body: JSON.stringify({ scope: state.scope, assist: body }) });
    for (let i = 0; i < 400; i++) {
      await sleep(i < 10 ? 1000 : 2000);
      const x = await request(`/v1/crew/assists/${encodeURIComponent(r.id)}?${query()}`);
      if (x.crew_assist.status !== "pending") return x.crew_assist;
    }
    return { status: "failed", error: "너무 오래 걸려요. 잠시 뒤 다시 해 주세요." };
  }
  const assistError = (a) => (a.error || "").startsWith("expired")
    ? "서버의 모델 설정 실행기가 꺼져 있어요. arkism-providers 서비스를 켜 주세요."
    : `AI가 답하지 못했어요 (${(a.error || "실패").slice(0, 120)}).`;

  return { assist, assistError, personas, parseJSON };
};
