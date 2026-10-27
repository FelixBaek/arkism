package memory

import (
	"encoding/json"
	"net"
	"net/url"
	"slices"
	"strings"
)

const researchStrategy = "sources-and-gaps-v1"

const researchReportInstruction = `

[Arkism 연구 검증 v1]
기존 보고서 JSON의 title, summary, sections, table, gaps, sources를 유지하고 needs_more_evidence 불리언을 추가한다.
질문에 필요한 공개 자료를 아직 확인하지 못했거나 공개 여부가 불확실하면 needs_more_evidence=true로 쓴다. gaps가 없거나 남은 항목이 원래 비공개인 정보뿐일 때만 false로 쓴다. 원래 비공개라는 판단의 이유도 gaps에 명시한다. 미기입하면 gaps가 있는 보고서는 보수적으로 추가 조사가 필요한 것으로 처리된다.
실제로 읽은 출처만 sources에 넣는다. 출처를 얻지 못했으면 빈 배열로 쓰며 조사 성공이라고 주장하지 않는다. sources에는 실제 http(s) 원문 URL과 제목이 필요하다. 근거 부족 여부는 경과 시간과 무관하다.
코드는 보고서 구조와 출처 형식을 검사할 뿐 주장이나 비공개 여부의 진실성을 검증하지 않는다. 최종 JSON은 저장 한도 20,000자를 넘지 않도록 작성한다. 초과하면 잘라서 성공 처리하지 않고 실패한다.`

func copyResearchCrew(c Crew) (Crew, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return Crew{}, err
	}
	var out Crew
	err = json.Unmarshal(b, &out)
	return out, err
}

func researchOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return "", false
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func researchHelperPath(raw, origin string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme+"://"+u.Host != origin {
		return ""
	}
	if slices.Contains([]string{"/search", "/deep", "/deep-gap"}, u.Path) && u.RawPath == "" {
		return u.Path
	}
	return ""
}

// ProposeResearch upgrades a copy of an existing research definition. It never
// writes a record or embeds a user's saved prompts in a built-in template.
// Models, graph edges, approval questions, search helper calls and budgets stay
// as saved; the added gate prevents a plan edge from bypassing its approval.
func ProposeResearch(c Crew) (Crew, []string, error) {
	if c.Type != "workflow" || c.Graph == nil {
		return Crew{}, nil, ErrInvalid
	}
	out, err := copyResearchCrew(c)
	if err != nil {
		return Crew{}, nil, err
	}
	byID := researchNodeIndex(out.Graph)
	quick, deep, err := researchEntrypoints(out.Graph, byID)
	if err != nil {
		return Crew{}, nil, err
	}
	origin, deepHTTP, err := researchHelperConfiguration(out.Graph)
	if err != nil {
		return Crew{}, nil, err
	}
	approval, err := researchApprovalNode(out.Graph, byID, deepHTTP)
	if err != nil {
		return Crew{}, nil, err
	}
	alias := "딥리서치"
	if c.Research != nil && c.Research.DeepAlias != "" {
		alias = c.Research.DeepAlias
	}
	out.Research = &CrewResearch{QuickNode: quick, DeepNode: deep, DeepAlias: alias, EvidenceStrategy: researchStrategy, HelperOrigin: origin}
	applyResearchConfig(out.Graph, out.Research, approval)
	if !validResearchPolicy(out) {
		return Crew{}, nil, ErrInvalid
	}
	changes := []string{"기존 단계·모델·승인·검색 연결·예산을 유지", "출처와 근거 부족 보고에 따른 심화·보완 분기를 코드로 결정", "심화 검색 전 실제 승인 확인", "직접 딥리서치는 같은 정의의 심화 진입점 사용", "출처 없는 최종 보고서·잘못된 결과·저장 한도 초과는 실패"}
	return out, changes, nil
}

func researchNodeIndex(g *CrewGraph) map[string]CrewNode {
	out := map[string]CrewNode{}
	for _, n := range g.Nodes {
		out[n.ID] = n
	}
	return out
}

func researchEntrypoints(g *CrewGraph, nodes map[string]CrewNode) (quick, deep string, err error) {
	for _, edge := range g.Edges {
		if edge.Port != "deep" {
			continue
		}
		if quick != "" && (quick != edge.From || deep != edge.To) {
			return "", "", ErrInvalid
		}
		quick, deep = edge.From, edge.To
	}
	if nodes[quick].Type != "ai" || nodes[deep].Type != "ai_once" {
		return "", "", ErrInvalid
	}
	return quick, deep, nil
}

func researchHelperConfiguration(g *CrewGraph) (origin, deepHTTP string, err error) {
	for _, n := range g.Nodes {
		if n.Type != "http" {
			continue
		}
		o, ok := researchOrigin(n.Config["url"])
		if !ok || origin != "" && origin != o || researchHelperPath(n.Config["url"], o) == "" {
			return "", "", ErrInvalid
		}
		origin = o
		if researchHelperPath(n.Config["url"], o) == "/deep" {
			if deepHTTP != "" {
				return "", "", ErrInvalid
			}
			deepHTTP = n.ID
		}
	}
	if origin == "" {
		return "", "", ErrInvalid
	}
	return origin, deepHTTP, nil
}

func researchApprovalNode(g *CrewGraph, nodes map[string]CrewNode, deepHTTP string) (string, error) {
	approval := ""
	for _, edge := range g.Edges {
		if edge.To != deepHTTP || edge.Port != "approve" || nodes[edge.From].Type != "approval" {
			continue
		}
		if approval != "" && approval != edge.From {
			return "", ErrInvalid
		}
		approval = edge.From
	}
	if approval == "" {
		return "", ErrInvalid
	}
	return approval, nil
}

func applyResearchConfig(g *CrewGraph, policy *CrewResearch, approval string) {
	for i := range g.Nodes {
		n := &g.Nodes[i]
		stage, need := researchReportStage(*n, g.Edges, policy.QuickNode)
		gated := n.Type == "http" && researchHelperPath(n.Config["url"], policy.HelperOrigin) != "/search"
		if !gated && stage == "" {
			continue
		}
		if n.Config == nil {
			n.Config = map[string]string{}
		}
		if gated {
			n.Config["research_approval"] = approval
		}
		if stage == "" {
			continue
		}
		n.Config["validator"], n.Config["research_stage"], n.Config["research_need_port"] = "research-report-v1", stage, need
		if !strings.Contains(n.Config["goal"], "[Arkism 연구 검증 v1]") {
			n.Config["goal"] += researchReportInstruction
		}
	}
}

func researchReportStage(n CrewNode, edges []CrewEdge, quick string) (stage, need string) {
	if n.Type != "ai" {
		return "", ""
	}
	if n.ID == quick {
		return "quick", "deep"
	}
	leaf := true
	for _, edge := range edges {
		if edge.From == n.ID {
			leaf = false
			if edge.Port == "more" {
				return "review", "more"
			}
		}
	}
	if leaf {
		return "final", ""
	}
	return "", ""
}

func validResearchPolicy(c Crew) bool {
	if !validResearchPolicyHeader(c) {
		return false
	}
	p, byID := c.Research, researchNodeIndex(c.Graph)
	if byID[p.DeepNode].Type != "ai_once" {
		return false
	}
	deepFound, reportFound := false, false
	for _, n := range c.Graph.Nodes {
		if !validResearchNode(n, p.HelperOrigin, byID) {
			return false
		}
		if n.Type == "http" && researchHelperPath(n.Config["url"], p.HelperOrigin) == "/deep" {
			deepFound = true
		}
		stage, need := researchReportStage(n, c.Graph.Edges, p.QuickNode)
		if stage == "" {
			continue
		}
		reportFound = true
		if n.Config["validator"] != "research-report-v1" || n.Config["research_stage"] != stage || n.Config["research_need_port"] != need {
			return false
		}
	}
	return deepFound && reportFound
}

func validResearchPolicyHeader(c Crew) bool {
	p := c.Research
	if c.Type != "workflow" || c.Graph == nil || p == nil || p.EvidenceStrategy != researchStrategy || !crewHandleRe.MatchString(p.DeepAlias) || p.QuickNode == "" || p.DeepNode == "" {
		return false
	}
	origin, ok := researchOrigin(p.HelperOrigin)
	return ok && origin == p.HelperOrigin
}

func validResearchNode(n CrewNode, origin string, byID map[string]CrewNode) bool {
	switch n.Type {
	case "start", "webhook", "schedule", "end", "condition", "merge", "wait", "approval":
		return true
	case "ai", "ai_once":
		if n.Config["collab"] == "on" || n.Config["backend"] != "" && n.Config["backend"] != "local" {
			return false
		}
		if n.Type == "ai" {
			return n.Config["access"] == "read"
		}
		return n.Config["access"] == "" || n.Config["access"] == "read"
	case "http":
		return validResearchHTTP(n, origin, byID)
	default:
		return false
	}
}

func validResearchHTTP(n CrewNode, origin string, byID map[string]CrewNode) bool {
	path := researchHelperPath(n.Config["url"], origin)
	if path == "" || n.Config["method"] != "POST" || n.Config["key"] != "" || n.Config["headers"] != "" {
		return false
	}
	return path == "/search" || byID[n.Config["research_approval"]].Type == "approval"
}

// ResearchDefinition derives a per-run snapshot. Deep mode bypasses only the
// quick-search branch; it keeps planning, the human approval and every later
// search/review step. The saved workflow remains a single unchanged record.
func ResearchDefinition(c Crew, mode string) (Crew, error) {
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "deep" || !validResearchPolicy(c) {
		return Crew{}, ErrInvalid
	}
	out, err := copyResearchCrew(c)
	if err != nil {
		return out, err
	}
	if mode == "auto" {
		if !slices.ContainsFunc(out.Graph.Nodes, func(n CrewNode) bool { return n.ID == out.Research.QuickNode }) {
			return Crew{}, ErrInvalid
		}
		return out, nil
	}
	entry, err := researchGraphEntry(out.Graph)
	if err != nil {
		return Crew{}, err
	}
	out.Graph = researchDeepGraph(out.Graph, entry, out.Research.DeepNode)
	if !validResearchPolicy(out) {
		return Crew{}, ErrInvalid
	}
	return out, nil
}

func researchGraphEntry(g *CrewGraph) (string, error) {
	incoming := map[string]bool{}
	for _, edge := range g.Edges {
		if edge.Max == 0 {
			incoming[edge.To] = true
		}
	}
	entry := ""
	for _, n := range g.Nodes {
		if incoming[n.ID] || !slices.Contains([]string{"start", "webhook", "schedule"}, n.Type) {
			continue
		}
		if entry != "" {
			return "", ErrInvalid
		}
		entry = n.ID
	}
	if entry == "" {
		return "", ErrInvalid
	}
	return entry, nil
}

func researchDeepNodes(g *CrewGraph, entry, deep string) map[string]bool {
	keep := map[string]bool{entry: true}
	queue := []string{deep}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if keep[id] {
			continue
		}
		keep[id] = true
		for _, edge := range g.Edges {
			if edge.From == id && edge.Max == 0 {
				queue = append(queue, edge.To)
			}
		}
	}
	return keep
}

func researchDeepGraph(g *CrewGraph, entry, deep string) *CrewGraph {
	keep, graph := researchDeepNodes(g, entry, deep), &CrewGraph{}
	for _, n := range g.Nodes {
		if !keep[n.ID] {
			continue
		}
		if n.Type == "approval" && n.Config != nil {
			n.Config["question"] = strings.Replace(n.Config["question"], "빠른 검색으로는 근거가 부족해요", "직접 딥리서치를 요청하셨어요", 1)
		}
		graph.Nodes = append(graph.Nodes, n)
	}
	for _, edge := range g.Edges {
		if keep[edge.From] && keep[edge.To] && edge.From != entry {
			graph.Edges = append(graph.Edges, edge)
		}
	}
	graph.Edges = append(graph.Edges, CrewEdge{From: entry, To: deep})
	return graph
}
