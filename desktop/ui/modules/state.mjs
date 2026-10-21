/**
 * Ownership boundary during migration of the existing screen factories.
 * Connection identity belongs to the app; memory/skills/crew own their filters.
 * Flat accessors keep older factories compatible without duplicating values.
 * Cursors and agent drafts live inside their respective module factories.
 */
export function createAppState(initial) {
  const connectionKeys = new Set(["defaults", "server", "token", "ssh", "email", "scope", "caps", "mode"]);
  const memoryKeys = new Set(["memTab", "kind", "repo", "sort", "query"]);
  const skillKeys = new Set(["skillTab", "skillModel", "skillSort", "skillDir", "openCopies", "skillCat", "versions", "versionsDone", "usage", "usageDone", "pending"]);
  const crewKeys = new Set(["crewTab", "crewCat"]);
  const connection = { generation: 0 };
  const screens = { memory: {}, skills: {}, crew: {}, shell: {} };
  const state = {};
  for (const key of new Set([...Object.keys(initial), ...connectionKeys])) {
    const value = initial[key];
    const owner = connectionKeys.has(key) ? connection : memoryKeys.has(key) ? screens.memory : skillKeys.has(key) ? screens.skills : crewKeys.has(key) ? screens.crew : screens.shell;
    owner[key] = value;
    Object.defineProperty(state, key, { enumerable: true, get: () => owner[key], set: next => { owner[key] = next; } });
  }
  Object.defineProperties(state, { connection: { value: connection }, screens: { value: screens } });
  return state;
}
