// MEMORY › 개요 (Figma v4 04 · Brain › 개요, 566:41431): the memory pipeline drawn on a brain.
// Eight stages (저장: 대화 저장·분석·분류·승인 대기 / 사용: 불러오기·사용 기록·검증 / 드리밍), each a card tied to a
// brain region, plus a stage × last-24-hours grid. Numbers come from data the app already loads (memories,
// candidates, maintenance proposals, GET /v1/memory_usage, the host's memory_use report) and the session list
// (GET /v1/usage/sessions). A stage with no timestamped source leaves its grid row dark instead of guessing.
// Motion follows the approved brain-motion.html: flowing nerve lines, random firing, card links with particles.
window.ArkismOverview = (ctx) => {
  "use strict";
  const { state, $, el, button, request, query, ago, go, render } = ctx;
  const HOUR = 3600000, W = 1157, H = 930;
    const SVG = "http://www.w3.org/2000/svg";
  const svg = (tag, attrs = {}) => { const n = document.createElementNS(SVG, tag); for (const k in attrs) n.setAttribute(k, attrs[k]); return n; };
  const num = (n) => Number(n || 0).toLocaleString("ko-KR");
  const time = (v) => { const t = new Date(v || 0).getTime(); return Number.isNaN(t) ? 0 : t; };

  // ---------------------------------------------------------------- data
  const S = { sessions: null, total: 0, cursor: "", at: 0, loading: false };
  let sessionEpoch = 0;
  async function loadSessions() {
    if (S.loading || Date.now() - S.at < 60000) return;
    S.loading = true;
    const ticket = ++sessionEpoch, generation = state.connection.generation;
    try {
      const doc = await request(`/v1/usage/sessions?${query({ range: "7d", limit: 50 })}`);
      if (ticket !== sessionEpoch || generation !== state.connection.generation) return;
      S.sessions = Array.isArray(doc?.items) ? doc.items : []; S.total = doc.total || 0; S.cursor = doc.next_cursor || "";
    } catch { if (ticket !== sessionEpoch || generation !== state.connection.generation) return; S.sessions = S.sessions || []; }
    S.at = Date.now(); S.loading = false;
    if (state.view === "overview") render();
    ctx.onSessions?.(); // 연결 상태 card shows the same stages
  }
  // counts[i] = events in the clock hour slot i (0 = 23 hours ago, 23 = this hour so far), matching the HH:00 ticks.
  function slots(times) {
    const c = new Array(24).fill(0), base = new Date().setMinutes(0, 0, 0), now = Date.now();
    for (const t of times) { if (!t || t > now) continue; const d = t >= base ? 0 : Math.ceil((base - t) / HOUR); if (d < 24) c[23 - d]++; }
    return c;
  }
  const today = (t) => t >= new Date().setHours(0, 0, 0, 0);
  const latest = (times) => Math.max(0, ...times);

  function stages() {
    const d = state.data, now = Date.now();
    const sessions = (S.sessions || []).map((s) => time(s.ended_at || s.started_at)).filter(Boolean);
    const waiting = [...(d.candidates || []), ...(d.conflicted || [])];
    const extracted = [...waiting, ...(d.memories || [])].map((m) => time(m.created_at)).filter(Boolean);
    const classified = waiting.map((m) => time(m.classification?.at)).filter(Boolean);
    const unclassified = waiting.filter((m) => !m.classification);
    const recalls = [...(state.usage?.values?.() || [])];
    const recallTimes = recalls.map((u) => time(u.last)).filter(Boolean);
    const w = d.tools?.memory_use || null, reported = time(d.tools?.collected_at);
    const proposals = (d.maintenance || []).map((p) => time(p.created_at)).filter(Boolean);
    const nProp = (d.maintenance || []).length; // a proposal without created_at still counts
    const lastSession = latest(sessions), lastExtract = latest(extracted);
    // the stall clock starts at the first session that ended after the last extraction (the oldest session if never extracted);
    // extraction runs on finished sessions, so a session that ended after it is still unprocessed even if it started before
    const pending = lastExtract ? sessions.filter((t) => t > lastExtract) : sessions;
    const since = pending.length ? Math.min(...pending) : lastExtract;
    // 분석 stops when sessions keep coming but nothing new became a candidate for 6 hours.
    const stalled = sessions.length > 0 && since > 0 && lastSession - since > 6 * HOUR && now - since > 6 * HOUR;
    const stalledHours = Math.floor((now - since) / HOUR);
    const rate = w?.injections ? Math.round((w.used / w.injections) * 100) : null;
    const tidy = (d.memories || []).filter((m) => { const u = w?.items?.find((x) => x.id === m.id); return u && u.injected >= 3 && !u.used; }).length;
    const oldest = waiting.length ? Math.min(...waiting.map((m) => time(m.created_at)).filter(Boolean)) : 0;
    const days = oldest ? Math.floor((now - oldest) / 86400000) : 0;

    const row = (times, tone) => slots(times).map((n) => (n ? tone : ""));
    const extractRow = row(extracted, "n");
    if (stalled) slots(sessions).forEach((n, i) => { if (n && new Date().setMinutes(0, 0, 0) - (23 - i) * HOUR > since) extractRow[i] = "r"; });
    const classRow = row(classified, "n"); slots(unclassified.map((m) => time(m.created_at))).forEach((n, i) => { if (n && !classRow[i]) classRow[i] = "d"; });
    const waitRow = row(waiting.map((m) => time(m.created_at)), "a");
    const reportRow = row(reported ? [reported] : [], "n");

    const n24 = (arr) => num(slots(arr).reduce((x, y) => x + y, 0));
    const todayCount = (arr) => arr.map((m) => time(m.created_at)).filter(today).length;
    const BADGE = { collect: [lastSession ? "정상" : "기록 없음", "n"], extract: stalled ? [`${stalledHours}시간째 멈춤`, "r"] : ["정상", "n"],
      classify: [unclassified.length ? `대기 ${unclassified.length}` : "모두 분류됨", "n"], review: [waiting.length ? `${waiting.length}건` : "비어 있음", waiting.length ? "a" : "n"],
      recall: ["정상", "n"], trace: [reported ? "정상" : "보고 없음", "n"], judge: [tidy ? `확인 전 ${tidy}` : "확인 끝", "n"], dream: [nProp ? `제안 ${nProp}` : "제안 없음", "n"] };
    const TILES = {
      collect: [[`${num(sessions.filter(today).length)}건`, "오늘 저장"], [lastSession ? ago(lastSession) : "—", "마지막 저장"], [`${num(S.total)}건`, "최근 7일"]],
      extract: [[stalled ? `${stalledHours}시간` : `${num(extracted.filter(today).length)}건`, stalled ? "멈춘 시간" : "오늘 추출"], [`${n24(extracted)}건`, "최근 24시간 추출"], [lastExtract ? ago(lastExtract) : "—", "마지막 추출"]],
      classify: [[`${num(unclassified.length)}건`, "대기"], [classified.length ? ago(latest(classified)) : "—", "마지막 처리"], [`${n24(classified)}건`, "최근 24시간 분류"]],
      review: [[`${num(waiting.length)}건`, "대기"], [oldest ? (days ? `${days}일` : ago(oldest)) : "—", "제일 오래된 것"], [`${num(todayCount(waiting))}건`, "오늘 들어온 것"]],
      recall: [[`${num(recalls.reduce((a, u) => a + (u.d7 || 0), 0))}회`, "최근 7일 불러오기"], [recallTimes.length ? ago(latest(recallTimes)) : "—", "마지막"], [`${num(recalls.filter((u) => u.d7 > 0).length)}개`, "7일 안에 불러온 기억"]],
      trace: [[reported ? ago(reported) : "—", "마지막 기록"], [w ? `${num(w.sessions)}` : "—", w ? `세션 · ${w.days}일` : "보고 없음"], [rate === null ? "—" : `${rate}%`, "쓰인 비율"]],
      judge: [[`${num(tidy)}건`, "정리할 기억"], [rate === null ? "—" : `${rate}%`, "쓰인 비율"], [w ? `${num(w.injections)}회` : "—", "넣은 횟수"]],
      dream: [[`${num(nProp)}건`, "정리 제안"], [nProp ? ago(latest(proposals)) : "—", "마지막 제안"], [`${num((d.maintenance || []).filter((p) => p.maintenance?.type === "contradiction").length)}건`, "충돌 제안"]]
    };
    // dot health — g 정상 · y 곧 확인 · o 확인 필요 · r 멈춤: last event <24h g, <3d y, older/none o (불러오기 <7d y); waiting work: none g, 분류 <6h y else o, 승인 <3d y else o, 검증·드리밍 y; 분석 멈춤 r
    const age = (t, yMax = 3 * 24 * HOUR) => !t ? "o" : now - t < 24 * HOUR ? "g" : now - t < yMax ? "y" : "o";
    const oldestUnclassified = unclassified.length ? Math.min(...unclassified.map((m) => time(m.created_at) || now)) : 0;
    const HEALTH = { collect: age(lastSession), extract: stalled ? "r" : age(lastExtract), classify: !unclassified.length ? "g" : now - oldestUnclassified < 6 * HOUR ? "y" : "o",
      review: !waiting.length ? "g" : now - oldest < 3 * 24 * HOUR ? "y" : "o", recall: age(latest(recallTimes), 7 * 24 * HOUR), trace: age(reported),
      judge: !w ? "o" : tidy ? "y" : "g", dream: nProp ? "y" : "g" };
    const withItems = (list) => list.map((s) => ({ ...s, tiles: TILES[s.key], badge: BADGE[s.key], health: HEALTH[s.key] }));
    return withItems([
      { key: "collect", group: "save", label: "대화 저장", region: "두정엽", tone: "n", note: ago(lastSession),
        big: `${num(sessions.filter(today).length)}건`, bigSub: "오늘", step: lastSession ? ago(lastSession) : "기록 없음",
        row: row(sessions, "n"), counts: slots(sessions), open: () => go("sessions") },
      { key: "extract", group: "save", label: "분석", region: "전전두엽", tone: stalled ? "r" : "n",
        big: stalled ? `${stalledHours}시간` : `${num(extracted.filter(today).length)}건`, bigSub: stalled ? "멈춘 시간" : "오늘 추출",
        small: stalled ? `${num(slots(extracted).reduce((a, b) => a + b, 0))}건` : ago(lastExtract), smallSub: stalled ? "최근 24시간" : "마지막 추출",
        step: stalled ? `${stalledHours}시간째 멈춤` : lastExtract ? ago(lastExtract) : "기록 없음",
        row: extractRow, counts: slots(extracted), open: () => go("sessions") },
      { key: "classify", group: "save", label: "분류", region: "측두엽", tone: "n",
        big: `${num(unclassified.length)}건`, bigSub: "대기", small: classified.length ? ago(latest(classified)) : "—", smallSub: "마지막 처리",
        step: unclassified.length ? `대기 ${num(unclassified.length)}` : "모두 분류됨", row: classRow, counts: slots(classified), open: () => go("memories", "candidate") },
      { key: "review", group: "save", label: "승인 대기", region: "안와전두엽", tone: waiting.length ? "a" : "n",
        big: `${num(waiting.length)}건`, bigSub: "대기 중", small: oldest ? (days ? `${days}일` : ago(oldest)) : "—", smallSub: "제일 오래된 것",
        step: `${num(waiting.length)}건`, row: waitRow, counts: slots(waiting.map((m) => time(m.created_at))), open: () => go("memories", "candidate") },
      { key: "recall", group: "use", label: "불러오기", region: "운동 피질", tone: "n",
        big: `${num(recalls.reduce((a, u) => a + (u.d7 || 0), 0))}회`, bigSub: "최근 7일", small: recallTimes.length ? ago(latest(recallTimes)) : "—", smallSub: "마지막",
        step: recallTimes.length ? ago(latest(recallTimes)) : "기록 없음", row: row(recallTimes, "n"), counts: slots(recallTimes), open: () => go("usage") },
      { key: "trace", group: "use", label: "사용 기록", region: "해마", tone: "n",
        big: reported ? ago(reported) : "—", bigSub: "마지막 기록", small: w ? `${num(w.sessions)}` : "—", smallSub: w ? `세션 · ${w.days}일` : "보고 없음",
        step: reported ? ago(reported) : "보고 없음", row: reportRow, counts: slots(reported ? [reported] : []), open: () => go("usage") },
      { key: "judge", group: "use", label: "검증", region: "소뇌", tone: "n",
        big: `${num(tidy)}건`, bigSub: "정리할 기억", small: rate === null ? "—" : `${rate}%`, smallSub: "쓰인 비율",
        step: tidy ? `대기 ${num(tidy)}` : "확인 끝", row: new Array(24).fill(""), counts: new Array(24).fill(0), open: () => go("memories", "idle") },
      { key: "dream", group: "dream", label: "드리밍", region: "뇌간", tone: "n",
        big: `${num(nProp)}건`, bigSub: "정리 제안", small: nProp ? ago(latest(proposals)) : "—", smallSub: "마지막 제안",
        step: nProp ? `제안 ${num(nProp)}` : "제안 없음", row: row(proposals, "n"), counts: slots(proposals), open: () => go("memories", "merge") }
    ]);
  }

  // ---------------------------------------------------------------- layout (Figma coordinates, 1157 × 930 panel)
  // [stage key, card side, card top, brain node x, y]
  const PLACE = [["recall", "l", 143, 540, 234], ["extract", "l", 333, 310, 404], ["review", "l", 523, 340, 594], ["classify", "l", 713, 490, 684],
    ["collect", "r", 143, 750, 264], ["trace", "r", 333, 640, 634], ["judge", "r", 523, 840, 704], ["dream", "r", 713, 660, 824]];
  const BRAIN = { x: 206, y: 195, w: 748, h: 707 };
  // Nerve points in the brain image's own 1497 × 1415 grid (brain-motion.html).
  const PTS = [[230, 435], [368, 570], [480, 325], [547, 370], [828, 350], [1006, 355], [1160, 462], [1318, 600], [822, 672], [1062, 678], [1233, 857], [483, 657],
    [330, 250], [660, 220], [900, 210], [1150, 290], [1330, 410], [690, 500], [950, 520], [600, 840], [900, 860], [1110, 960], [1360, 770], [400, 820], [200, 640], [1400, 560]];
  const PAIRS = [[0, 1], [1, 11], [11, 8], [8, 9], [9, 10], [2, 3], [3, 4], [4, 5], [5, 6], [6, 7], [1, 3], [8, 4], [9, 6], [0, 2], [4, 9], [7, 10], [2, 1],
    [12, 2], [12, 0], [13, 3], [13, 14], [14, 5], [15, 6], [15, 16], [16, 7], [17, 3], [17, 8], [18, 4], [18, 9], [19, 11], [19, 20], [20, 9], [21, 10], [21, 9],
    [22, 7], [22, 10], [23, 11], [23, 1], [24, 0], [24, 23], [25, 7], [17, 18], [14, 4]];
  const noise = (i, r) => { const x = Math.sin(i * 12.9898 + r * 78.233) * 43758.5453; return x - Math.floor(x); };

  let root = null, ui = null, timer = 0, sized = null;
  function build() {
    root = el("div", "ov-wrap");
    const stage = el("div", "ov-stage");
    root.append(stage);
    ui = { stage, steps: {}, cards: {}, nodes: {}, links: {}, rows: {} };

    // top stepper: 저장 ─ 사용 ─ 드리밍
    const top = el("div", "ov-steps");
    for (const [g, label] of [["save", "저장"], ["use", "사용"], ["dream", "드리밍"]]) {
      const grp = el("div", `ov-group ov-group-${g}`), head = el("div", "ov-group-head"); head.append(el("span", "", label), el("i"));
      const line = el("div", "ov-group-line"); grp.append(head, line); top.append(grp); ui.steps[g] = line;
    }
    stage.append(top);

    // brain + nerves
    const brain = el("div", "ov-brain"); Object.assign(brain.style, { left: `${BRAIN.x}px`, top: `${BRAIN.y}px`, width: `${BRAIN.w}px`, height: `${BRAIN.h}px` });
    const img = el("img"); img.src = "img/brain.webp"; img.alt = ""; img.draggable = false;
    const nerves = svg("svg", { viewBox: "0 0 1497 1415", "aria-hidden": "true" });
    const paths = PAIRS.map(([a, b], i) => { const [x1, y1] = PTS[a], [x2, y2] = PTS[b]; return `M${x1} ${y1} Q${(x1 + x2) / 2 + (i % 2 ? 30 : -30)} ${(y1 + y2) / 2 + (i % 2 ? -25 : 25)} ${x2} ${y2}`; });
    paths.forEach((d) => nerves.append(svg("path", { d, class: "ov-n-base" })));
    paths.forEach((d, i) => {
      const p = svg("path", { d, class: "ov-n-flow", pathLength: 1, "stroke-width": i % 4 === 0 ? 2.6 : 1.8, "stroke-dasharray": i % 2 ? "0.06 0.27" : "0.1 0.4" });
      Object.assign(p.style, { animationDuration: `${2.2 + (i % 5) * 0.55}s`, animationDelay: `${-i * 0.37}s`, animationDirection: i % 3 === 0 ? "reverse" : "normal" });
      nerves.append(p);
    });
    ui.fires = paths.map((d) => { const p = svg("path", { d, class: "ov-n-fire" }); nerves.append(p); return p; });
    brain.append(img, nerves);
    ui.sparks = PTS.slice(0, 12).map(([x, y], i) => {
      const s = el("i", "ov-spark"), size = i % 3 === 0 ? 7 : 5;
      Object.assign(s.style, { left: `${(x / 1497) * 100}%`, top: `${(y / 1415) * 100}%`, width: `${size}px`, height: `${size}px`, animationDuration: `${2.2 + (i % 4) * 0.7}s`, animationDelay: `${(i * 0.53) % 3}s` });
      brain.append(s); return s;
    });
    stage.append(brain);

    // card links, particles and region nodes (stage coordinates)
    const links = svg("svg", { class: "ov-links", viewBox: `0 0 ${W} ${H}`, "aria-hidden": "true" });
    stage.append(links);
    PLACE.forEach(([key, side, top, nx, ny], i) => {
      const ax = side === "l" ? 282 : 876, ay = top + 24, dx = (ax - nx) * 0.55;
      const d = `M${nx} ${ny} C${nx + dx} ${ny} ${ax - dx} ${ay} ${ax} ${ay}`;
      const line = svg("path", { d, class: "ov-link", pathLength: 1 }); line.style.animationDelay = `${0.6 + i * 0.12}s`; links.append(line);
      const pt = el("i", "ov-particle");
      Object.assign(pt.style, { offsetPath: `path('${d}')`, animationDuration: `${2.6 + (i % 3) * 0.5}s`, animationDelay: `${1.8 + i * 0.37}s` });
      const node = el("div", "ov-node"); Object.assign(node.style, { left: `${nx}px`, top: `${ny}px` });
      node.append(el("i", "ov-ring"), el("i", "ov-halo"), el("i", "ov-frame"), el("i", "ov-core"));
      node.style.setProperty("--ring", `${2.6 + (i % 3) * 0.4}s`); node.style.setProperty("--delay", `${i * 0.3}s`);
      stage.append(pt, node);
      ui.links[key] = { line, pt, node };

      const card = el("button", `ov-card ov-card-${side}`); card.type = "button"; card.style.top = `${top}px`;
      const head = el("div", "ov-card-head"), name = el("span", "ov-card-name"), note = el("span", "ov-card-note"), region = el("span", "ov-card-region");
      const dot = el("i", "ov-dot"); dot.setAttribute("role", "img"); head.append(dot, name, note, region);
      const body = el("div", "ov-card-body"), big = el("div", "ov-big"), small = el("div", "ov-small"), chart = el("div", "ov-chart");
      big.append(el("strong"), el("small")); small.append(el("strong"), el("small"));
      for (let k = 0; k < 7; k++) chart.append(el("i"));
      body.append(big, small, chart); card.append(head, body); stage.append(card);
      ui.cards[key] = { card, dot, name, note, region, big, small, chart };
    });
    const mid = el("div", "ov-node ov-node-white"); Object.assign(mid.style, { left: "624px", top: "692px" });
    mid.append(el("i", "ov-ring"), el("i", "ov-halo"), el("i", "ov-frame"), el("i", "ov-core")); stage.append(mid);

    // 기억 처리: stage × last 24 hours
    const grid = el("div", "ov-grid"), gh = el("div", "ov-grid-head");
    ui.badge = el("span", "ov-badge"); gh.append(el("i", "ov-dot"), el("strong", "", "기억 처리"), ui.badge);
    const body = el("div", "ov-grid-body");
    for (const [, label, keys] of [["save", "저장", ["collect", "extract", "classify", "review"]], ["use", "사용", ["recall", "trace", "judge"]], ["dream", "드리밍", ["dream"]]]) {
      body.append(el("div", "ov-grid-sec", label));
      for (const k of keys) { const r = el("div", "ov-grid-row"), name = el("span", "ov-grid-name"), cells = el("div", "ov-cells"); for (let c = 0; c < 24; c++) cells.append(el("i")); r.append(name, cells); body.append(r); ui.rows[k] = { name, cells }; }
    }
    ui.ticks = el("div", "ov-ticks");
    grid.append(gh, body, ui.ticks); stage.append(grid);

    // 입구 줄
    ui.chips = el("div", "ov-chips"); stage.append(ui.chips);

    
  }
  // Scale the 1157 × 930 stage to the free area of the window (width and height) so the whole overview fits without scrolling;
  // the footer under the panel is subtracted too. MIN_SCALE only guards against a nonsense size in a tiny window.
  const MIN_SCALE = 0.4, MAX_SCALE = 1.7;
  function fit() {
    const host = root?.closest(".main");
    if (!host || !root.isConnected) return;
    const cs = getComputedStyle(host), padX = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight), padB = parseFloat(cs.paddingBottom);
    const box = root.getBoundingClientRect(), foot = host.querySelector(".page-foot");
    const top = box.top - host.getBoundingClientRect().top + host.scrollTop;
    const below = foot?.offsetParent ? Math.max(0, foot.getBoundingClientRect().bottom - box.bottom) : 0; // gap + footer, independent of the scale
    const s = Math.min(MAX_SCALE, Math.max(MIN_SCALE, Math.min((host.clientWidth - padX) / W, (host.clientHeight - top - below - padB) / H)));
    ui.stage.style.transform = `scale(${s})`;
    // Cards and labels shrink less than the brain (effective scale √s); 1.12 keeps the grown cards clear of the
    // nearest region nodes (extract at x 310, judge at x 840), which also draw above the cards.
    ui.stage.style.setProperty("--ov-k", s < 1 ? Math.min(1.12, 1 / Math.sqrt(s)).toFixed(3) : "1");
    root.style.width = `${Math.floor(W * s)}px`; root.style.height = `${Math.floor(H * s)}px`;
  }

  let win = null;
  const windows = () => (win = win || window.ArkismOverviewWindows(ctx, { stages, sessions: () => S.sessions || [], reloadSessions: () => { S.at = 0; return loadSessions(); } }));
  const openStage = (key) => windows().open(key);
  const openWide = (kind) => windows().wide(kind);
  // A separate window (popup-drag.js) reopens the stage or wide window that was dragged out of the app.
  window.addEventListener("arkism:open-overview-window", (e) => { const { key, expanded, wide, args = [] } = e.detail || {}; go("overview"); if (wide) windows().wide(wide, ...args); else windows().open(key, expanded); });

  // ---------------------------------------------------------------- paint
  function chip(label, value, open) {
    const b = el("button", "ov-chip"); b.type = "button"; const i = el("i", "ov-dot");
    b.append(i, el("strong", "", label), el("span", "", value), el("span", "ov-chev", "›")); b.addEventListener("click", open); return b;
  }
  const HEALTH_WORD = { g: "정상", y: "곧 확인", o: "확인 필요", r: "멈춤" };
  const healthDot = (dot, h) => { dot.setAttribute("aria-label", HEALTH_WORD[h]); dot.title = HEALTH_WORD[h]; };
  function paint() {
    const list = stages(), by = Object.fromEntries(list.map((s) => [s.key, s]));
    for (const g of ["save", "use", "dream"]) {
      const line = ui.steps[g]; line.replaceChildren();
      list.filter((s) => s.group === g).forEach((s, i) => {
        if (i) line.append(el("span", `ov-step-join${s.tone === "r" || list.filter((x) => x.group === g)[i - 1].tone === "r" ? " r" : ""}`));
        const b = el("button", `ov-step ov-t-${s.tone} ov-h-${s.health}`); b.type = "button";
        const t = el("span", "ov-step-name"), dot = el("i", "ov-dot"); healthDot(dot, s.health); t.append(dot, document.createTextNode(s.label));
        b.append(t, el("span", "ov-step-sub", s.step)); b.addEventListener("click", () => openStage(s.key)); line.append(b);
      });
    }
    for (const s of list) {
      const c = ui.cards[s.key];
      c.card.className = `ov-card ov-card-${PLACE.find((p) => p[0] === s.key)[1]} ov-t-${s.tone} ov-h-${s.health}`; healthDot(c.dot, s.health);
      c.card.onclick = () => openStage(s.key);
      c.name.textContent = s.label; c.note.textContent = s.note || ""; c.region.textContent = s.region;
      c.big.firstChild.textContent = s.big; c.big.lastChild.textContent = s.bigSub;
      c.small.hidden = !s.small; c.small.firstChild.textContent = s.small || ""; c.small.lastChild.textContent = s.smallSub || "";
      const last7 = s.counts.slice(-7), max = Math.max(1, ...last7);
      [...c.chart.children].forEach((bar, k) => { bar.style.height = `${8 + (last7[k] / max) * 24}px`; bar.classList.toggle("now", k === 6); bar.classList.toggle("none", !last7[k]); });
      const l = ui.links[s.key]; l.node.className = `ov-node ov-t-${s.tone} ov-h-${s.health}${s.tone === "r" ? " alert" : ""}`; l.line.setAttribute("class", `ov-link ov-t-${s.tone}`); l.pt.className = `ov-particle ov-t-${s.tone}`;
      const r = ui.rows[s.key]; r.name.textContent = s.label; r.name.className = `ov-grid-name${s.tone === "r" ? " r" : ""}`;
      [...r.cells.children].forEach((cell, k) => { cell.className = s.row[k] ? `ov-c-${s.row[k]}` : ""; });
    }
    const stopped = list.filter((s) => s.tone === "r").length;
    ui.badge.className = `ov-badge${stopped ? " r" : ""}`; ui.badge.textContent = stopped ? `${stopped}곳 멈춤` : "모두 움직임";
    const h = new Date().getHours();
    ui.ticks.replaceChildren(...Array.from({ length: 24 }, (_, i) => { const hour = (h - 23 + i + 24) % 24; return el("span", i === 23 ? "now" : "", i === 23 ? "지금" : hour % 6 === 0 ? `${String(hour).padStart(2, "0")}:00` : ""); }));
    const todaySessions = (S.sessions || []).filter((x) => today(time(x.ended_at || x.started_at))).length;
    // every active memory on the server (the map draws them all), not the loaded first page
    const memN = ctx.memoryCount().replace(/\d+/, (n) => num(n));
    ui.chips.replaceChildren(chip("쓰는 중인 기억", memN, () => openWide("active")),
      chip("세션 기록", `오늘 ${num(todaySessions)}`, () => openWide("sessions")),
      chip("사용량", `7일 ${num(by.recall.big.replace(/\D/g, ""))}회`, () => openWide("usage")),
      chip("기억 지도", `점 ${memN}`, () => openWide("map")));
  }

  // Random firing: three nerve paths every 520 ms; stops itself once the screen is gone or hidden.
  let tick = 0;
  const still = window.matchMedia?.("(prefers-reduced-motion: reduce)");
  function stopFiring() {
    clearInterval(timer); timer = 0;
    ui?.fires?.forEach((p) => p.classList.remove("on")); ui?.sparks?.forEach((sp) => sp.classList.remove("hot"));
  }
  function startFiring() {
    if (timer || still?.matches || document.hidden || !root?.isConnected) return;
    timer = setInterval(() => {
      if (!root?.isConnected || document.hidden) { stopFiring(); return; }
      tick++;
      const on = new Set([0, 1, 2].map((k) => Math.floor(noise(tick, 20 + k) * PAIRS.length))), hot = new Set();
      ui.fires.forEach((p, i) => { const f = on.has(i); p.classList.toggle("on", f); if (f) { hot.add(PAIRS[i][0]); hot.add(PAIRS[i][1]); } });
      ui.sparks.forEach((s, i) => s.classList.toggle("hot", hot.has(i)));
    }, 520);
  }

  // follow the reduced-motion setting live: stop firing on reduce, resume only while the screen is shown
  still?.addEventListener?.("change", () => {
    if (!still.matches) { startFiring(); return; }
    stopFiring();
  });
  // a hidden window keeps no timer; startFiring resumes only while the overview is on screen
  document.addEventListener("visibilitychange", () => { if (document.hidden) stopFiring(); else startFiring(); });

  function renderView() {
    const table = $("#table");
    table.className = "ov-host";
    $("#title-count").textContent = "";
    if (!root) build();
    if (table.firstChild !== root || table.childNodes.length !== 1) table.replaceChildren(root); // re-adding would restart the motion
    void loadSessions();
    paint(); fit(); startFiring();
    const host = root.closest(".main");
    if (host && sized?.host !== host) { sized?.ro.disconnect(); sized = { host, ro: new ResizeObserver(fit) }; sized.ro.observe(host); }
  }

  return {
    reset() { sessionEpoch++; Object.assign(S, { sessions:null,total:0,cursor:"",at:0,loading:false }); win?.reset(); },
    stages, loadSessions, openStage, // 연결 상태 card (app.js renderStatusStages)
    views: { overview: { title: "개요", render: renderView, head: () => [button("새로고침", "ghost", "arrows-clockwise", () => { S.at = 0; void loadSessions(); })] } }
  };
};
