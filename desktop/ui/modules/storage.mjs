// @ts-check
// Connection settings are read defensively; credentials never enter localStorage.
export function readSSH(value) {
  if (!value) return null;
  try { const parsed = JSON.parse(value); if (parsed && typeof parsed.host === "string") return parsed; } catch { /* legacy address */ }
  return { host: value };
}
