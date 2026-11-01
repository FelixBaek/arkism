// FLOW lists workflows (#481). Agents and teams are parts a workflow step runs; `arkism crew convert` wraps each
// named one in a workflow of its name. Until then a named agent or team stays listed so it is not lost.
const key = (h) => (h || "").replace(/^[@$]/, "").replace(/ /g, "").toLocaleLowerCase();

// wraps: a step of workflow crew w runs the agent or team id (the server's wrapsCrew).
export function wraps(w, id) {
  return (w.graph?.nodes || []).some((n) => (n.type === "expert" && n.config?.expert === id) || (n.type === "team" && n.config?.team === id));
}

function wrapped(record, records) {
  return records.some((r) => r.crew?.type === "workflow" && key(r.crew.handle) === key(record.crew.handle) && wraps(r.crew, record.id));
}

// worksByLive: the agent works by a workflow that is still listed (an archived one leaves a dangling link).
function worksByLive(r, records) {
  return r.crew.type === "agent" && r.crew.workflow && records.some((w) => w.crew?.type === "workflow" && key(w.crew.handle) === key(r.crew.workflow));
}

export function flowItems(records) {
  // An agent that works by its own live workflow is shown as that workflow.
  return records.filter((r) => r.crew?.type === "workflow" ||
    ["agent", "team"].includes(r.crew?.type) && r.crew.handle && !worksByLive(r, records) && !wrapped(r, records));
}

export function flowAlias(record, records) {
  const { type, handle } = record.crew;
  if (!handle) return "";
  // An owned workflow is called by its agent's handle; the server does not resolve @ on an owned workflow.
  const owner = type === "workflow" && records.find((r) => r.crew?.type === "agent" && r.crew.handle && key(r.crew.workflow) === key(handle));
  if (owner) return `@${owner.crew.handle}`;
  // Legacy @role/$workflow collisions retain their existing unambiguous spelling; a workflow wrapping its part is no collision.
  const collision = type === "workflow" && records.some((r) => ["agent", "team"].includes(r.crew?.type) && key(r.crew.handle) === key(handle) && !wraps(record.crew, r.id));
  return `${collision ? "$" : "@"}${handle}`;
}
