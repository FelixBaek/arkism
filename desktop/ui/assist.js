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
    { id: "builtin:workflow", crew: { type: "persona", name: "워크플로우 설계자", role: "workflow", instructions: "You design workflows. Split the job into steps; steps an if can decide become code, and only steps that need judgement use AI." } },
    { id: "builtin:skill", crew: { type: "persona", name: "스킬 제작 전문가", role: "skill", instructions: "You write agent skills. Turn repeated requests into a SKILL.md with at most 5 steps." } },
    { id: "builtin:prompt", crew: { type: "persona", name: "프롬프트 작가", role: "prompt", instructions: "You write prompts. Pin down purpose and tone, and pull values that change out into {variables}." } },
    { id: "builtin:category", crew: { type: "persona", name: "카테고리 분류가", role: "category", instructions: "You classify skills. From a skill's name, description and usage, pick the single closest category." } }
  ];
  const saved = () => (state.data.crew || []).filter((r) => r.crew.type === "persona");
  const personas = () => [...saved(), ...BUILTIN_PERSONAS];
  const personaFor = (purpose) => personas().find((p) => p.crew.role === purpose) || BUILTIN_PERSONAS[0];

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  async function assist(kind, prompt, opts = {}) {
    // A cut would break install_skill's JSON; free text is cut, structured requests are refused when too long.
    if (["install_skill", "fix", "comfy", "json", "review", "build"].includes(kind) && prompt.length > 20000) throw new Error("내용이 너무 길어요 (2만 자까지)");
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
