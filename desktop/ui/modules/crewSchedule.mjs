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

  // A new schedule starts every day at 09:00; weekdays are one tap away in the picker.
  const DEFAULT_CRON = "0 9 * * *";
  // "m h * * dow" with plain numbers is what the picker edits; anything else stays a raw cron line.
  function simpleCron(cron) {
    const f = String(cron || "").trim().split(/\s+/);
    if (f.length !== 5 || !/^\d+$/.test(f[0]) || !/^\d+$/.test(f[1]) || f[2] !== "*" || f[3] !== "*") return null;
    const days = f[4] === "*" ? [0, 1, 2, 3, 4, 5, 6] : f[4] === "1-5" ? [1, 2, 3, 4, 5] : /^[0-7](,[0-7])*$/.test(f[4]) ? [...new Set(f[4].split(",").map((x) => Number(x) % 7))] : null;
    return days ? { min: Number(f[0]), hour: Number(f[1]), days } : null;
  }
  const cronOf = ({ min, hour, days }) => {
    const ds = [...new Set(days)].sort((a, b) => a - b);
    const dow = ds.length === 7 || !ds.length ? "*" : ds.join(",") === "1,2,3,4,5" ? "1-5" : ds.join(",");
    return `${min} ${hour} * * ${dow}`;
  };

  // The one 요일·시간 picker (popup, settings window, start node): day buttons and a time box;
  // the raw cron line sits under 자세히. onChange gets only valid cron lines.
  function picker(cron, onChange) {
    const doc = globalThis.document;
    const mk = (tag, cls, text) => { const x = doc.createElement(tag); if (cls) x.className = cls; if (text != null) x.textContent = text; return x; };
    const box = mk("div", "sched-pick");
    let cur = parseCron(cron) ? String(cron).trim() : DEFAULT_CRON;
    const set = (v) => { cur = v; onChange(v); draw(); };
    function draw() {
      box.replaceChildren();
      const sc = simpleCron(cur);
      if (sc) {
        const days = mk("div", "sched-days");
        days.setAttribute("role", "group");
        days.setAttribute("aria-label", "요일");
        DOW.forEach((label, i) => {
          const on = sc.days.includes(i);
          const b = mk("button", `sched-day${on ? " on" : ""}`, label);
          b.type = "button";
          b.setAttribute("aria-pressed", String(on));
          b.addEventListener("click", () => { const next = on ? sc.days.filter((x) => x !== i) : [...sc.days, i]; if (next.length) set(cronOf({ ...sc, days: next })); });
          days.append(b);
        });
        const time = mk("input", "sched-time");
        time.type = "time";
        time.setAttribute("aria-label", "시간");
        time.value = `${String(sc.hour).padStart(2, "0")}:${String(sc.min).padStart(2, "0")}`;
        time.addEventListener("change", () => { const [hh, mm] = time.value.split(":").map(Number); if (Number.isInteger(hh) && Number.isInteger(mm)) set(cronOf({ ...sc, hour: hh, min: mm })); });
        box.append(days, time);
      }
      const n = nextRun(cur);
      box.append(mk("small", "dim sched-next", `${cronText(cur)}${n ? ` · 다음 실행 ${when(n)}` : ""}`));
      const more = mk("details", "sched-more");
      more.open = !sc;
      const raw = mk("input", "mono");
      raw.value = cur;
      raw.maxLength = 64;
      raw.setAttribute("aria-label", "cron (분 시 일 월 요일)");
      const bad = mk("small", "dim", "분 시 일 월 요일 순서예요 · 예: 0 9 * * 1-5 는 평일 09:00");
      raw.addEventListener("change", () => { const v = raw.value.trim(); if (parseCron(v)) set(v); else { bad.textContent = "형식이 맞지 않아요 · 예: 0 9 * * 1-5"; raw.focus(); } });
      more.append(mk("summary", "", "자세히"), raw, bad);
      box.append(more);
    }
    draw();
    return box;
  }

  return {
    get cronText() { return cronText; },
    get DEFAULT_CRON() { return DEFAULT_CRON; },
    get simpleCron() { return simpleCron; },
    get cronOf() { return cronOf; },
    get picker() { return picker; },
    get nextRun() { return nextRun; },
    get parseCron() { return parseCron; },
    get triggered() { return triggered; },
    get triggerText() { return triggerText; },
    get when() { return when; }
  };
}
