package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
)

type FlowChildRef struct {
	Kind     string `json:"kind" bson:"kind"`
	ID       string `json:"id" bson:"id"`
	CrewID   string `json:"crew_id" bson:"crew_id"`
	NodeID   string `json:"node_id" bson:"node_id"`
	Attempt  int    `json:"attempt" bson:"attempt"`
	Required bool   `json:"required" bson:"required"`
}
type FlowDefinition struct {
	ID       string `json:"id" bson:"id"`
	Revision int64  `json:"revision" bson:"revision"`
	Crew     Crew   `json:"crew" bson:"crew"`
}
type FlowPolicy struct {
	MaxDepth      int     `json:"max_depth" bson:"max_depth"`
	MaxChildren   int     `json:"max_children" bson:"max_children"`
	MaxConcurrent int     `json:"max_concurrent" bson:"max_concurrent"`
	MaxTokens     int64   `json:"max_tokens,omitempty" bson:"max_tokens,omitempty"`
	MaxCostUSD    float64 `json:"max_cost_usd,omitempty" bson:"max_cost_usd,omitempty"`
}

func effectiveFlowPolicy(c Crew) FlowPolicy {
	p := FlowPolicy{MaxDepth: 3, MaxChildren: 32, MaxConcurrent: 4}
	if c.FlowPolicy != nil {
		p = *c.FlowPolicy
	}
	return p
}
func validFlowPolicy(p *FlowPolicy) bool {
	return p == nil || p.MaxDepth >= 1 && p.MaxDepth <= 8 && p.MaxChildren >= 1 && p.MaxChildren <= 100 && p.MaxConcurrent >= 1 && p.MaxConcurrent <= 16 && p.MaxConcurrent <= p.MaxChildren && p.MaxTokens >= 0 && p.MaxCostUSD >= 0 && !math.IsNaN(p.MaxCostUSD) && !math.IsInf(p.MaxCostUSD, 0)
}

type FlowCapabilities struct {
	Supported   bool     `json:"supported"`
	Unsupported []string `json:"unsupported"`
	Modes       []string `json:"modes"`
}

func flowUnsupported(c Crew) []string {
	out := []string{}
	// These fields remain editable in compatibility mode. Refuse a new unified
	// run rather than silently discarding a requested execution constraint.
	fields := map[string]bool{"tools": len(c.Tools) > 0, "skill_rules": len(c.SkillRules) > 0, "memory": len(c.Memory) > 0, "scripts": len(c.Scripts) > 0, "temperature": c.Temperature != nil, "image_model": c.ImageModel != "", "max_output_tokens": c.MaxOutputTokens > 0, "file_edits": c.FileEdits == "ask" || c.FileEdits == "auto", "backend": c.Backend != "", "run_mode": c.RunMode == "caller", "automatic_notifications": flowAutomaticNotifications(c.Notify)}
	for k, v := range fields {
		if v {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
func flowAutomaticNotifications(n *CrewNotify) bool {
	if n == nil {
		return false
	}
	for _, c := range n.Channels {
		if c.Kind == "telegram" || c.Kind == "slack" {
			return true
		}
	}
	return false
}
func (s *Service) FlowCapabilities(ctx context.Context, token string, scope Scope, id string) (FlowCapabilities, error) {
	if !validScope(scope) || !validRunLabel(id, 256) {
		return FlowCapabilities{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
		return FlowCapabilities{}, err
	}
	r, err := s.Repo.Get(ctx, colCrew, scope, id)
	if err != nil {
		return FlowCapabilities{}, err
	}
	if !liveFlowTarget(r) {
		return FlowCapabilities{}, ErrNotFound
	}
	defs, err := s.flowDefinitions(ctx, token, scope, id)
	if err != nil {
		return FlowCapabilities{}, err
	}
	out := flowDefinitionUnsupported(defs)
	return FlowCapabilities{Supported: len(out) == 0, Unsupported: out, Modes: []string{"flow", "legacy"}}, nil
}
func flowDefinitionUnsupported(defs map[string]FlowDefinition) []string {
	out := []string{}
	for id, d := range defs {
		for _, field := range flowUnsupported(d.Crew) {
			out = append(out, id+":"+field)
		}
		if d.Crew.Type == "workflow" && !flowCallable(d.Crew) {
			out = append(out, id+":graph_capability")
		}
	}
	slices.Sort(out)
	return out
}
func liveFlowTarget(r Record) bool {
	return r.Status == statusActive && !r.ContentErased && !r.PrivacyQuarantined && r.Crew != nil && slices.Contains([]string{"workflow", "agent", "team"}, r.Crew.Type)
}
func clientFlowStart(r CrewRun) bool {
	return r.ResultRevision == 0 && len(r.Effects) == 0 && !r.CancelRequested && r.Definition == nil && len(r.Definitions) == 0 && r.RootID == "" && r.ParentID == "" && r.ParentNodeID == "" && r.Depth == 0 && len(r.Children) == 0 && r.Policy == nil && r.SourceType == ""
}
func checkFlowDefinitions(ctx context.Context, tx Tx, scope Scope, defs map[string]FlowDefinition) error {
	for id, d := range defs {
		r, err := tx.Get(ctx, colCrew, scope, id)
		if err != nil {
			return err
		}
		if r.Status != statusActive || r.ContentErased || r.PrivacyQuarantined || r.Revision != d.Revision {
			return ErrConflict
		}
	}
	return nil
}
func cloneFlowCrew(c Crew) Crew {
	b, _ := json.Marshal(c)
	var out Crew
	_ = json.Unmarshal(b, &out)
	return out
}
func flowGraphFor(c Crew, id string, defs map[string]FlowDefinition) (Crew, error) {
	if len(flowUnsupported(c)) > 0 {
		return Crew{}, ErrInvalid
	}
	out := cloneFlowCrew(c)
	if c.Type == "workflow" {
		return out, nil
	}
	node := CrewNode{ID: "work", Type: "expert", Title: c.Name, Config: map[string]string{"expert": id, "access": "read", "goal": "{{input}}"}}
	switch {
	case c.Type == "team":
		node.Type = "team"
		node.Config = map[string]string{"team": id, "access": "read", "topic": "{{input}}"}
	case c.Workflow != "":
		target := flowDefinitionByHandle(defs, c.Workflow)
		if target.ID == "" {
			return Crew{}, ErrInvalid
		}
		node.Type = "subflow"
		node.Config = map[string]string{"workflow": target.Crew.Handle, "task": "{{input}}"}
	}
	out.Type = "workflow"
	out.Graph = &CrewGraph{Nodes: []CrewNode{{ID: "start", Type: "start"}, node, {ID: "end", Type: "end"}}, Edges: []CrewEdge{{From: "start", To: "work"}, {From: "work", To: "end"}}}
	return out, nil
}
func flowDefinitionByHandle(defs map[string]FlowDefinition, handle string) FlowDefinition {
	for _, d := range defs {
		if d.Crew.Type == "workflow" && crewKey(d.Crew.Handle) == crewKey(handle) {
			return d
		}
	}
	return FlowDefinition{}
}

func (s *Service) flowDefinitions(ctx context.Context, token string, scope Scope, root string) (map[string]FlowDefinition, error) {
	page, err := s.ListCrew(ctx, token, scope)
	if err != nil {
		return nil, err
	}
	available := map[string]FlowDefinition{}
	for _, r := range page.Items {
		if r.Crew != nil && !r.ContentErased && !r.PrivacyQuarantined {
			available[r.ID] = FlowDefinition{r.ID, r.Revision, cloneFlowCrew(*r.Crew)}
		}
	}
	out := map[string]FlowDefinition{}
	var add func(string) error
	add = func(id string) error {
		if _, ok := out[id]; ok {
			return nil
		}
		d, ok := available[id]
		if !ok || len(out) >= 100 {
			return ErrInvalid
		}
		if _, err := s.authorize(ctx, token, "read:"+colCrew, scope, id, ""); err != nil {
			return err
		}
		if err := s.pinFlowSkills(ctx, token, scope, &d.Crew); err != nil {
			return err
		}
		out[id] = d
		for _, ref := range flowDefinitionRefs(d.Crew, available) {
			if err := add(ref); err != nil {
				return err
			}
		}
		return nil
	}
	if err = add(root); err != nil {
		return nil, err
	}
	return out, nil
}
func flowDefinitionRefs(c Crew, available map[string]FlowDefinition) []string {
	out := []string{}
	if c.Workflow != "" {
		out = append(out, flowDefinitionByHandle(available, c.Workflow).ID)
	}
	for _, m := range c.Members {
		out = append(out, m.AgentID)
	}
	if c.Graph == nil {
		return out
	}
	for _, n := range c.Graph.Nodes {
		for _, key := range []string{"expert", "team", "persona"} {
			if id := n.Config[key]; id != "" {
				out = append(out, id)
			}
		}
		out = append(out, flowWorkerRefs(n)...)
		if n.Type == "subflow" {
			out = append(out, flowDefinitionByHandle(available, n.Config["workflow"]).ID)
		}
	}
	return out
}
func flowWorkerRefs(n CrewNode) []string {
	var workers []struct {
		Expert string `json:"expert"`
	}
	if n.Config["collab"] != "on" || json.Unmarshal([]byte(n.Config["workers"]), &workers) != nil {
		return nil
	}
	out := []string{}
	for _, w := range workers {
		if w.Expert != "" {
			out = append(out, w.Expert)
		}
	}
	return out
}
func (s *Service) pinFlowSkills(ctx context.Context, token string, scope Scope, c *Crew) error {
	var err error
	skills := []string{}
	for _, ref := range c.Skills {
		id, _, _ := strings.Cut(ref, "@")
		if !slices.Contains(c.SkillsOff, id) && !slices.Contains(c.SkillsOff, ref) {
			skills = append(skills, ref)
		}
	}
	c.Skills = skills
	c.Skills, err = s.pinFlowSkillRefs(ctx, token, scope, c.Skills)
	if err != nil {
		return err
	}
	if c.Graph == nil {
		return nil
	}
	for i := range c.Graph.Nodes {
		n := &c.Graph.Nodes[i]
		refs := strings.FieldsFunc(n.Config["skills"], func(r rune) bool { return r == ',' || r == '\n' })
		if len(refs) == 0 {
			continue
		}
		pins, e := s.pinFlowSkillRefs(ctx, token, scope, refs)
		if e != nil {
			return e
		}
		n.Config["skills"] = strings.Join(pins, ",")
	}
	return nil
}
func (s *Service) pinFlowSkillRefs(ctx context.Context, token string, scope Scope, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return refs, nil
	}
	query := make([]string, len(refs))
	for i, ref := range refs {
		query[i] = ref
		if !strings.Contains(ref, "@") {
			query[i] += "@latest-approved"
		}
	}
	result, err := s.ResolveSkills(ctx, token, SkillResolveInput{Scope: scope, Refs: query})
	if err != nil {
		return nil, err
	}
	if len(result.Items) != len(refs) {
		return nil, ErrInvalid
	}
	out := make([]string, len(refs))
	for i, item := range result.Items {
		if item.Error != "" || !slices.Contains([]string{"approved", "active", "superseded"}, item.ValidationStatus) {
			return nil, ErrInvalid
		}
		out[i] = fmt.Sprintf("%s@%d", item.SkillID, item.Version)
	}
	return out, nil
}
func flowCommonRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/flow/capabilities/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.FlowCapabilities(r.Context(), bearer(r), scopeQuery(r), r.PathValue("id"))
		respond(w, out, e)
	})
}
