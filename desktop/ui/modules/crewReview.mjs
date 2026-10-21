// Owns AI review and freshness checks. Dependencies are explicit live ports, never a global scope.
export function createCrewReview(deps) {



  // ---------------------------------------------------------------- 구조 검토 (03c)
  // Rule checks run in code; AI suggestions come from the assist runner as JSON and apply with one click.
  function reviewPanel() {
    const box = deps.el("aside", "g-right review");
    const head = deps.el("div", "g-right-head");
    const checks = deps.ruleChecks(deps.ed.rec.crew, deps.ed.graph);
    const t = deps.el("div", "");
    t.append(deps.el("strong", "", "구조 검토"), deps.el("small", "dim", `규칙 검사 ${checks.filter((c) => c.ok).length}/${checks.length} 통과${deps.ed.suggestions ? ` · 제안 ${deps.ed.suggestions.length}개` : ""}`));
    const close = deps.el("button", "icon-btn plain");
    close.type = "button";
    close.setAttribute("aria-label", "구조 검토 닫기");
    close.append(deps.icon("x"));
    close.addEventListener("click", () => { deps.ed.review = false; deps.render(false); });
    head.append(t, close);
    const h4 = deps.el("h4", "", "규칙 검사");
    h4.append(deps.el("small", "", `코드 · ${checks.filter((c) => c.ok).length}개 통과`));
    box.append(head, h4);
    // Passed checks read as small chips (the detail on hover); the ones that failed keep their sentence.
    const list = deps.el("div", "wf-checks");
    for (const c of checks) {
      const r = deps.el("div", `wf-check${c.ok ? "" : " bad"}`);
      r.title = c.detail;
      r.append(deps.icon(c.ok ? "check" : "warning-circle"), deps.el("strong", "", c.label));
      if (!c.ok) r.append(deps.el("small", "", c.detail));
      list.append(r);
    }
    box.append(list, reviewAI());
    box.append(deps.el("p", "meta", "[적용]을 누르면 그래프가 바로 바뀌고, 나머지 제안은 그대로 남아요. 실행 기록이 쌓이면 자주 다시 도는 노드도 짚어요."));
    return box;
  }

  // AI 제안 (ai-chat.js): the same button → bubble → purple list as the expert window; 적용 changes the graph at once.
  const REVIEW_TAG = { tokens: "토큰 줄이기", order: "순서", stop: "멈추는 조건", safety: "안전" };

  function reviewAI() {
    deps.ed.reviewAI ||= {};
    const st = deps.ed.reviewAI;
    const asProp = (s) => ({ s, label: REVIEW_TAG[s.tag] || s.tag || "제안", from: null, to: `${s.title}\n${s.body}`, apply: () => {
      if (!s.action) { deps.notice("이 제안은 직접 고쳐 주세요. 노드를 눌러 바꿀 수 있어요."); return false; }
      if (!canApply(s.action) || !fresh(s)) { deps.notice("그 노드나 선이 바뀌어 다시 받아야 해요"); return false; }
      applyAction(s.action);
      deps.ed.suggestions = (deps.ed.suggestions || []).filter((x) => x !== s);
      deps.render(false);
      deps.notice("적용했어요. 저장하면 반영돼요.");
    } });
    // a node deleted since keeps its suggestion out of the list (deleteNode filters ed.suggestions)
    st.props = (st.props || []).filter((p) => p.applied || (deps.ed.suggestions || []).includes(p.s));
    return window.ArkismAIChat.button({ state: st, label: "AI 제안", title: "AI 제안", sub: "토큰을 줄일 칸, 순서 문제, 멈추는 조건을 짚어 줘요",
      greeting: "이 그래프를 읽고 고칠 곳을 4개까지 짚어 드려요. 특히 볼 곳이 있으면 말해 주세요. 실행할 때는 검토하지 않아요.",
      placeholder: "예: 비용이 많이 드는 곳을 먼저 봐 줘", quick: ["전체를 검토해 줘", "토큰 줄일 곳", "멈추는 조건 확인"],
      onDrop: (p) => { deps.ed.suggestions = (deps.ed.suggestions || []).filter((x) => x !== p.s); deps.render(false); },
      ask: async (want) => {
        const list = await aiReview(want);
        return { say: list.length ? `제안 ${list.length}개를 찾았어요. 버튼 아래 목록에서 보고 적용하세요.` : "고칠 곳을 찾지 못했어요.", props: list.map(asProp) };
      } });
  }

  async function aiReview(want) {
    const record = deps.ed.rec, graph = deps.ed.graph;
    const session = JSON.stringify([deps.state.server, deps.state.scope, deps.state.connection?.generation]);
    const g = { nodes: deps.ed.graph.nodes.map(({ id, type, title, config }) => ({ id, type, title, config })), edges: deps.ed.graph.edges };
    // The graph as the AI reads it, taken from the live node objects (so a node deleted and re-added under the
    // same id while it thinks counts as a different node): edits made meanwhile make its suggestions stale.
    const asked = { nodes: Object.fromEntries(deps.ed.graph.nodes.map((n) => [n.id, nodePrint(n)])), edges: structuredClone(deps.ed.graph.edges) };
    const prompt = `Review the workflow graph below and give at most 4 improvements. Principles: steps an if can decide become code nodes (0 tokens); only steps that need judgement use AI. Cheap checks first. Back edges only where truly needed, with a count limit. Parallel branches must not need each other. A human check before any irreversible action.
The owner asks: ${want}
Shape: {"suggestions":[{"title":"short Korean title","body":"two plain Korean sentences","tag":"tokens|order|stop|safety","action":null or {"type":"set_model","node":"id","model":"..."} or {"type":"set_edge_max","from":"id","to":"id","max":2} or {"type":"set_access","node":"id","access":"read|write"} or {"type":"insert_before","node":"id","new":{"type":"code|ai_once|approval","title":"...","config":{}}}}]}
Workflow: ${deps.ed.rec.crew.name} ($${deps.ed.rec.crew.handle}) — ${deps.ed.rec.crew.role || ""}
Default worker model: ${deps.ed.rec.crew.model || "none"}
Models allowed for set_model (only these): ${[...new Set(deps.models?.enabledModels() || [])].join(", ") || "none — do not suggest set_model"}
Graph: ${JSON.stringify(g).slice(0, 14000)}`;
    const a = await deps.kit.assist("review", prompt, { purpose: "workflow" });
    if (record !== deps.ed.rec || graph !== deps.ed.graph || session !== JSON.stringify([deps.state.server, deps.state.scope, deps.state.connection?.generation])) return [];
    if (a.status !== "done") throw new Error(deps.kit.assistError(a));
    const j = deps.kit.parseJSON(a.output);
    const list = Array.isArray(j) ? j : j?.suggestions;
    if (!Array.isArray(list)) throw new Error("AI 답을 읽지 못했어요. 다시 해 주세요.");
    deps.ed.suggestions = list.filter((s) => s?.title).slice(0, 4).map((s) => {
      const action = s.action || null;
      // What the targets looked like when the AI read them: a suggestion only applies to that same state.
      const seen = stateOf(action, asked);
      return { title: String(s.title).slice(0, 80), body: String(s.body || "").slice(0, 400), tag: String(s.tag || "").slice(0, 20), action, seen };
    });
    deps.render(false); // the flagged nodes on the canvas and the counts in the head
    return deps.ed.suggestions;
  }

  const nodeIn = (id) => deps.ed.graph.nodes.find((n) => n.id === id);

  const targetsOf = (a) => (a ? [a.node, a.from, a.to].filter(Boolean) : []);

  // Each node object gets its own number on first sight, so a deleted and re-added node with the same id,
  // title and settings still differs from the one the AI read.
  const nodeTokens = new WeakMap();

  let nodeTokenSeq = 0;

  const nodePrint = (n) => {
    if (!nodeTokens.has(n)) nodeTokens.set(n, ++nodeTokenSeq);
    return JSON.stringify([nodeTokens.get(n), n.type, n.title, n.config]);
  };

  // The edges a suggestion depends on: between two nodes for a back-edge count, into the node for an insert.
  const edgePrint = (a, edges) => JSON.stringify(edges.filter((e) => (a.type === "set_edge_max" ? e.from === a.from && e.to === a.to : !deps.isBack(e) && e.to === a.node))
    .map((e) => [e.from, e.to, e.port || "", e.max || 0]));

  // A suggestion's targets and edges, from the live graph or from the snapshot taken when the AI was asked.
  // Node prints and edge prints live apart, so a node whose id happens to be "edges" cannot hide either.
  function stateOf(a, snap) {
    const nodes = Object.fromEntries(targetsOf(a).map((id) => {
      if (snap) return [id, snap.nodes[id] ?? null];
      const n = nodeIn(id);
      return [id, n ? nodePrint(n) : null];
    }));
    if (a?.type !== "set_edge_max" && a?.type !== "insert_before") return { nodes, edges: "" };
    const edgeList = snap ? snap.edges : deps.ed.graph.edges;
    return { nodes, edges: edgePrint(a, edgeList) };
  }

  // Still the graph the AI looked at: every target exists and is unchanged since the review.
  const fresh = (s) => {
    const now = stateOf(s.action);
    return Object.values(now.nodes).every((v) => v !== null) && Object.entries(now.nodes).every(([k, v]) => s.seen?.nodes?.[k] === v) && now.edges === s.seen?.edges;
  };

  function canApply(a) {
    if (a.type === "set_model" || a.type === "set_access") return !!nodeIn(a.node);
    if (a.type === "set_edge_max") return deps.ed.graph.edges.some((e) => e.from === a.from && e.to === a.to && deps.isBack(e)) && a.max >= 1 && a.max <= 10;
    if (a.type === "insert_before") return !!nodeIn(a.node) && ["code", "ai_once", "approval"].includes(a.new?.type);
    return false;
  }

  function applyAction(a) {
    const g = deps.ed.graph;
    if (a.type === "set_model") nodeIn(a.node).config.model = String(a.model || "").slice(0, 80);
    if (a.type === "set_access") nodeIn(a.node).config.access = a.access === "write" ? "write" : "read";
    if (a.type === "set_edge_max") g.edges.filter((e) => e.from === a.from && e.to === a.to && deps.isBack(e)).forEach((e) => { e.max = a.max; });
    if (a.type === "insert_before") {
      const target = nodeIn(a.node);
      const id = deps.uid(a.new.type === "ai_once" ? "once" : a.new.type);
      const config = Object.fromEntries(Object.entries(a.new.config || {}).map(([k, v]) => [k, String(v)]));
      if (a.new.type === "code") { config.lang ||= "go"; config.code ||= "package main\n\n// 입력(stdin JSON)을 걸러 {\"ok\": true/false, ...}를 출력해요.\nfunc main() {}\n"; }
      g.nodes.push({ id, type: a.new.type, title: String(a.new.title || deps.NODES[a.new.type].label).slice(0, 80), x: target.x - 40, y: target.y + 220, config });
      g.edges.filter((e) => e.to === target.id && !deps.isBack(e)).forEach((e) => { e.to = id; });
      g.edges.push({ from: id, to: target.id });
    }
    deps.touch();
  }

  return {
    get nodeIn() { return nodeIn; },
    get reviewPanel() { return reviewPanel; },
    get targetsOf() { return targetsOf; }
  };
}
