// @ts-check
// Preserve the existing Dice/Jaccard rule and stable top-20 order. Large loaded
// catalogs yield between chunks; only 20 results are retained, even if all match.
function features(skill) {
  const text = (skill.description || "").toLowerCase().replace(/[^a-z0-9가-힣]/g, ""), desc = new Map();
  for (let i = 0; i < text.length - 1; i++) { const pair = text.slice(i, i + 2); desc.set(pair, (desc.get(pair) || 0) + 1); }
  return { desc, size: Math.max(0, text.length - 1), name: new Set(skill.name.toLowerCase().split(/[-_]/).filter(word => word.length > 1)) };
}
function score(a, b) {
  let shared = 0;
  for (const word of a.name) if (b.name.has(word)) shared++;
  const union = a.name.size + b.name.size - shared, name = union ? shared / union : 0;
  const size = a.size + b.size;
  // This upper bound skips only descriptions that cannot reach the threshold.
  if (!size || 2 * Math.min(a.size, b.size) / size < 0.6) return name;
  let intersection = 0;
  for (const [pair, count] of a.desc) intersection += Math.min(count, b.desc.get(pair) || 0);
  return Math.max(name, 2 * intersection / size);
}
function retain(top, pair) {
  const rank = top.findIndex(item => pair.score > item.score);
  if (rank < 0 && top.length >= 20) return;
  top.splice(rank < 0 ? top.length : rank, 0, pair);
  if (top.length > 20) top.pop();
}
function* compare(skills, dismissed, top) {
  const sets = [];
  for (const skill of skills) { sets.push(features(skill)); if (sets.length % 64 === 0) yield; }
  let checks = 0;
  for (let i = 0; i < skills.length; i++) {
    for (let j = i + 1; j < skills.length; j++) {
      const a = skills[i], b = skills[j], similarity = score(sets[i], sets[j]);
      if (similarity >= 0.6) {
        const key = [a.id, b.id].sort((x, y) => x < y ? -1 : x > y ? 1 : 0).join("|");
        if (!dismissed.has(key)) retain(top, { a, b, score: similarity, key });
      }
      if (++checks % 256 === 0) yield;
    }
  }
}
export function createSkillSimilarity({ notify, pause = () => new Promise(resolve => setTimeout(resolve, 0)) }) {
  let epoch = 0, key = "", items = [], busy = false;
  async function finish(iterator, top, ticket) {
    let began = performance.now();
    while (!iterator.next().done) {
      if (ticket !== epoch) return;
      if (performance.now() - began >= 6) { await pause(); began = performance.now(); }
    }
    if (ticket === epoch) { items = top; busy = false; queueMicrotask(() => { if (ticket === epoch) notify(); }); }
  }
  function read(records, dismissed) {
    const live = records.filter(skill => skill.status !== "deprecated");
    const next = JSON.stringify([live.map(skill => [skill.id, skill.name, skill.description]), dismissed]);
    if (next === key) return items;
    key = next; const ticket = ++epoch, top = [], iterator = compare(live, new Set(dismissed), top);
    items = []; busy = live.length > 150;
    if (busy) void finish(iterator, top, ticket);
    else { while (!iterator.next().done) { /* small catalogs retain synchronous rendering */ } items = top; }
    return items;
  }
  return { read, get busy() { return busy; }, reset() { epoch++; key = ""; items = []; busy = false; } };
}
