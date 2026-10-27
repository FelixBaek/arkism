// FLOW presents existing definitions together without rewriting identities or ownership.
export const FLOW_KINDS = [["all", "모든 실행 방식"], ["agent", "단독 실행"], ["team", "협업·회의"], ["workflow", "단계 구성"]];

export function flowItems(records) {
  const owned = new Set(records.filter((r) => r.crew?.type === "agent" && r.crew.workflow).map((r) => r.crew.workflow.replace(/^\$/, "")));
  return records.filter((r) => ["agent", "team", "workflow"].includes(r.crew?.type) && !(r.crew.type === "workflow" && owned.has(r.crew.handle)));
}

export function flowAlias(record, records) {
  const { type, handle } = record.crew;
  if (!handle) return "";
  // Legacy @role/$workflow collisions retain their existing unambiguous spelling.
  const collision = type === "workflow" && records.some((r) => ["agent", "team"].includes(r.crew?.type) && r.crew.handle?.toLocaleLowerCase() === handle.toLocaleLowerCase());
  return `${collision ? "$" : "@"}${handle}`;
}

export function flowKind(record) {
  return FLOW_KINDS.find(([key]) => key === record.crew?.type)?.[1] || "";
}
