// @ts-check
// Owns cron parsing and display. Dependencies are explicit live ports, never a global scope.
export function createCrewSchedule(_deps) {


  // ---------------------------------------------------------------- cron (5 fields, same rules as the run engine)
  const DOW = ["일", "월", "화", "수", "목", "금", "토"];

  function cronField(f, min, max) {
    const out = new Set();
    for (const part of f.split(",")) {
      const [range, stepRaw] = part.split("/");
      const step = Number(stepRaw || 1);
      let [a, b] = range === "*" ? [min, max] : range.split("-").map(Number);
      if (b === undefined) b = stepRaw ? max : a;
      if (!(step > 0) || Number.isNaN(a) || Number.isNaN(b)) return null;
      for (let v = a; v <= b; v += step) out.add(max === 7 && v === 7 ? 0 : v);
    }
    return out;
  }

  function parseCron(expr) {
    const f = (expr || "").trim().split(/\s+/);
    if (f.length !== 5) return null;
    const p = [cronField(f[0], 0, 59), cronField(f[1], 0, 23), cronField(f[2], 1, 31), cronField(f[3], 1, 12), cronField(f[4], 0, 7)];
    return p.some((x) => !x) ? null : { p, f };
  }

  function nextRun(expr, from = new Date()) {
    const c = parseCron(expr);
    if (!c) return null;
    const t = new Date(from);
    t.setSeconds(0, 0);
    for (let i = 0; i < 60 * 24 * 32; i++) {
      t.setMinutes(t.getMinutes() + 1);
      const [mi, h, dom, mon, dow] = c.p;
      const domAny = c.f[2] === "*", dowAny = c.f[4] === "*";
      const dayOk = domAny && dowAny ? true : domAny ? dow.has(t.getDay()) : dowAny ? dom.has(t.getDate()) : dom.has(t.getDate()) || dow.has(t.getDay());
      if (mi.has(t.getMinutes()) && h.has(t.getHours()) && mon.has(t.getMonth() + 1) && dayOk) return t;
    }
    return null;
  }

  function cronText(expr) {
    const c = parseCron(expr);
    if (!c) return expr || "—";
    const [m, h, dom, mon, dow] = c.f;
    if (!/^\d+$/.test(m) || !/^\d+$/.test(h) || mon !== "*") return expr;
    const at = `${h.padStart(2, "0")}:${m.padStart(2, "0")}`;
    if (dom === "*" && dow === "*") return `매일 ${at}`;
    if (dom === "*" && dow === "1-5") return `평일 ${at}`;
    if (dom === "*" && /^[0-7](,[0-7])*$/.test(dow)) return `${dow.split(",").map((d) => DOW[Number(d) % 7]).join("·")} ${at}`;
    if (dow === "*" && /^\d+$/.test(dom)) return `매달 ${dom}일 ${at}`;
    return expr;
  }

  const when = (d) => (d ? new Intl.DateTimeFormat(globalThis.ArkismI18n?.locale() || "ko-KR", { month: "long", day: "numeric", weekday: "short", hour: "2-digit", minute: "2-digit" }).format(d) : "—");

  function triggerText(c) {
    const t = c.trigger || {};
    if (t.kind === "schedule") return `일정 · ${cronText(t.cron)}`;
    if (t.kind === "webhook") return `웹훅${t.label ? ` · ${t.label}` : ""}`;
    return "";
  }

  const triggered = (x) => !!(x.trigger && x.enabled);

  return {
    get cronText() { return cronText; },
    get nextRun() { return nextRun; },
    get parseCron() { return parseCron; },
    get triggered() { return triggered; },
    get triggerText() { return triggerText; },
    get when() { return when; }
  };
}
