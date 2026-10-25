// @ts-check
// Owns graph traversal and validation. Dependencies are explicit live ports, never a global scope.
export function createCrewModel(deps) {


  // ---------------------------------------------------------------- graph helpers
  const WORK = (n) => !["start", "schedule", "webhook", "end"].includes(n.type);

  const isBack = (e) => (e.max || 0) > 0;

  const fwd = (g) => (g?.edges || []).filter((e) => !isBack(e));

  // Edges by the node they leave, so walks over the graph stay linear in its size.
  const outsOf = (edges) => {
    const m = new Map();
    for (const e of edges) (m.get(e.from) || m.set(e.from, []).get(e.from)).push(e);
    return m;
  };

  const nodeTitle = (n) => n.title || deps.NODES[n.type]?.label || n.type;

  const stepCount = (g) => (g?.nodes || []).filter(WORK).length;

  const aiNodes = (g) => (g?.nodes || []).filter((n) => deps.IS_AI(n.type));

  // One AI step and nothing else that works: the editor shows the simple form instead of the canvas (03d).
  const simpleNode = (g) => {
    const work = (g?.nodes || []).filter(WORK);
    return work.length === 1 && deps.IS_AI(work[0].type) && !(g.edges || []).some(isBack) ? work[0] : null;
  };

  const modelOf = (n, x) => n.config?.model || (n.type === "expert" && deps.byId(n.config?.expert)?.crew.model) || x?.model || "기본 모델";

  function levels(graph) {
    const g = graph || { nodes: [], edges: [] };
    const edges = fwd(g);
    const depth = {};
    const incoming = {};
    g.nodes.forEach((n) => { incoming[n.id] = 0; });
    edges.forEach((e) => { incoming[e.to] = (incoming[e.to] || 0) + 1; });
    const queue = g.nodes.filter((n) => !incoming[n.id]).map((n) => n.id);
    queue.forEach((id) => { depth[id] = 0; });
    const left = { ...incoming };
    const outs = outsOf(edges);
    for (const id of queue) { // the array iterator also visits ids pushed during the loop
      for (const e of outs.get(id) || []) {
        depth[e.to] = Math.max(depth[e.to] || 0, depth[id] + 1);
        if (--left[e.to] === 0) queue.push(e.to);
      }
    }
    const out = [];
    for (const n of g.nodes) {
      if (depth[n.id] === undefined) continue;
      (out[depth[n.id]] ||= []).push(n);
    }
    return out.filter(Boolean);
  }

  // Would a new forward edge from → to close a circle of forward edges? Then it has to be a back edge.
  function wouldCycle(graph, from, to) {
    if (from === to) return true;
    const seen = new Set();
    const outs = outsOf(fwd(graph));
    const walk = (id) => {
      if (id === from) return true;
      if (seen.has(id)) return false;
      seen.add(id);
      return (outs.get(id) || []).some((e) => walk(e.to));
    };
    return walk(to);
  }

  // The ports a node's outgoing edges can use. AI nodes name their own branches (the AI picks one), plus a free port.
  function portsOf(n, g) {
    if (deps.PORTS[n.type]) return deps.PORTS[n.type];
    if (n.type === "end") return [];
    if (deps.IS_AI(n.type)) {
      const named = [...new Set((g.edges || []).filter((e) => e.from === n.id && e.port).map((e) => e.port))];
      return [...named.map((p) => [p, p]), ["", named.length ? "다른 갈래" : ""]];
    }
    return [["", ""]];
  }

  function defaultGraph(kind, x) {
    const tpl = kind.startsWith("tpl:") ? deps.byId(kind.slice(4))?.crew : null;
    if (tpl) {
      // The template's instructions·model·effort·skills go into the one AI node; the engine reads the same keys.
      const config = { model: tpl.model || x.model || "", goal: "{{input}}", access: "read" };
      if (tpl.instructions) config.instructions = tpl.instructions;
      if (tpl.effort) config.effort = tpl.effort;
      // Templates name skills; the node keeps hub skill ids (a name with no match stays as written).
      const hub = deps.state.data.skills || [];
      if (tpl.skills?.length) config.skills = tpl.skills.map((n) => (hub.find((k) => k.id === n || k.name === n)?.id || n)).join(",");
      return { nodes: [{ id: "start", type: "start", title: "시작", x: 40, y: 160 },
        { id: "ai1", type: "ai", title: tpl.name || "AI 에이전트", x: 320, y: 120, config },
        { id: "end", type: "end", title: "보고하고 끝", x: 620, y: 160, config: {} }],
      edges: [{ from: "start", to: "ai1" }, { from: "ai1", to: "end" }] };
    }
    if (kind === "simple") {
      return { nodes: [{ id: "start", type: "start", title: "시작", x: 40, y: 160 },
        { id: "ai1", type: "ai", title: x.name || "AI 에이전트", x: 320, y: 120, config: { model: x.model || "", goal: "{{input}}", access: "read" } },
        { id: "end", type: "end", title: "보고하고 끝", x: 620, y: 160, config: {} }],
      edges: [{ from: "start", to: "ai1" }, { from: "ai1", to: "end" }] };
    }
    return { nodes: [{ id: "start", type: "start", title: "시작", x: 40, y: 160 }, { id: "end", type: "end", title: "보고하고 끝", x: 420, y: 160, config: {} }], edges: [{ from: "start", to: "end" }] };
  }


  // 구조 검토 · 규칙 검사 (03c): what code can decide on its own, before any AI is asked.
  // POST is how most search APIs read, so only PUT·PATCH·DELETE count as changing something elsewhere.
  const IRREVERSIBLE = (n) => n.type === "github" || n.type === "notify" || (n.type === "http" && ["PUT", "PATCH", "DELETE"].includes((n.config?.method || "").toUpperCase()));

  function ruleChecks(x, g = x.graph) {
    const nodes = g?.nodes || [];
    const edges = fwd(g);
    const out = [];
    const targets = new Set(edges.map((e) => e.to));
    const outs = outsOf(edges);
    const roots = nodes.filter((n) => !targets.has(n.id));
    const seen = new Set();
    const walk = (id) => { if (seen.has(id)) return; seen.add(id); (outs.get(id) || []).forEach((e) => walk(e.to)); };
    roots.forEach((n) => walk(n.id));
    const orphan = nodes.filter((n) => !seen.has(n.id));
    const ends = nodes.filter((n) => !outs.has(n.id));
    out.push({ ok: !orphan.length && roots.length <= 1, label: "시작→끝 도달", detail: orphan.length ? `닿지 않는 노드: ${orphan.map(nodeTitle).join(", ")}` : roots.length > 1 ? `시작이 ${roots.length}개예요` : `끝 ${ends.length}곳` });
    const backs = (g?.edges || []).filter(isBack);
    out.push({ ok: true, label: "되돌아가기 제한", detail: backs.length ? backs.map((e) => `${nodeTitle(nodes.find((n) => n.id === e.from) || {})} → ${nodeTitle(nodes.find((n) => n.id === e.to) || {})} 최대 ${e.max}번`).join(" · ") : "되돌아가는 선이 없어요" });
    const selfCall = nodes.filter((n) => n.type === "subflow" && (n.config?.workflow || "").replace(/^\$/, "") === x.handle);
    out.push({ ok: !selfCall.length, label: "고리 없음", detail: selfCall.length ? "하위 워크플로우가 자기 자신을 불러요" : "하위 워크플로우가 자기를 부르지 않아요" });
    const noModel = aiNodes(g).filter((n) => !n.config?.model && !x.model);
    out.push({ ok: !noModel.length, label: "AI 노드 모델", detail: noModel.length ? `모델이 비었어요: ${noModel.map(nodeTitle).join(", ")} (기본 워커 모델을 정해도 돼요)` : `${aiNodes(g).length}개 모두 정해졌어요` });
    const risky = nodes.filter(IRREVERSIBLE).filter((n) => !upstream(g, n.id).some((u) => u.type === "approval"));
    if (risky.length) out.push({ ok: false, label: "되돌릴 수 없는 동작", detail: `${risky.map(nodeTitle).join(", ")} 앞에 사람 확인이 없어요` });
    const writers = levels(g).filter((lv) => lv.filter((n) => deps.IS_AI(n.type) && n.config?.access === "write").length > 1);
    if (writers.length) out.push({ ok: true, label: "쓰기 노드", detail: "쓰기 노드끼리는 갈래가 나뉘어도 차례로 돌아요" });
    return out;
  }

  function upstream(g, id) {
    const out = new Map();
    const walk = (n) => fwd(g).filter((e) => e.to === n).forEach((e) => { if (!out.has(e.from)) { out.set(e.from, g.nodes.find((x) => x.id === e.from)); walk(e.from); } });
    walk(id);
    return [...out.values()].filter(Boolean);
  }

  return {
    get aiNodes() { return aiNodes; },
    get defaultGraph() { return defaultGraph; },
    get fwd() { return fwd; },
    get isBack() { return isBack; },
    get levels() { return levels; },
    get modelOf() { return modelOf; },
    get nodeTitle() { return nodeTitle; },
    get portsOf() { return portsOf; },
    get ruleChecks() { return ruleChecks; },
    get simpleNode() { return simpleNode; },
    get stepCount() { return stepCount; },
    get upstream() { return upstream; },
    get WORK() { return WORK; },
    get wouldCycle() { return wouldCycle; }
  };
}
