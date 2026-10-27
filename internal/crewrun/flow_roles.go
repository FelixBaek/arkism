package crewrun

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
)

func flowContext(ctx context.Context) bool {
	_, ok := ctx.Value(flowCallScopeKey{}).(flowCallScope)
	return ok
}

func (x *runExec) stepBackend(n Node) string {
	if x.run.State.InvocationMode != "flow" || n.Type != "expert" || n.Config["backend"] != "" {
		return x.e.stepBackend(n)
	}
	return backendName(x.run.State.Definitions[n.Config["expert"]].Crew.Backend)
}

func checkFlowAI(ctx context.Context, spec aiSpec, backend string) error {
	if !flowContext(ctx) {
		return nil
	}
	if !spec.Once && spec.Access != "read" || backendName(backend) != "" {
		return failf("flow_unsupported", "FLOW writes and external runners require an effect gateway")
	}
	return nil
}

// Node choices override model and effort; workflow instructions and selected
// pinned skills are shared defaults. Compiled agent roots already apply their
// own role through runExpert, so they do not inject it a second time.
func (x *runExec) aiConfig(ctx context.Context, own map[string]string) map[string]string {
	if !flowContext(ctx) || x.run.State.SourceType == "agent" {
		return own
	}
	c := maps.Clone(own)
	if c == nil {
		c = map[string]string{}
	}
	role := x.item.Crew
	if c["model"] == "" && role.ModelMode == "auto" {
		c["model"] = "auto"
	}
	c["effort"] = cmp.Or(c["effort"], role.Effort)
	c["instructions"] = joinText(role.Role, role.Instructions, c["instructions"])
	if format := cmp.Or(c["output_format"], role.OutputFormat); format == "json" || format == "markdown" {
		c["instructions"] = joinText(c["instructions"], "답은 "+format+" 형식으로 써.")
	}
	c["skills"] = strings.Join(mergeSkills(role.Skills, skillList(c["skills"])), ",")
	c["fallback_model"] = cmp.Or(c["fallback_model"], role.FallbackModel)
	return c
}

func mergeSkills(base, added []string) []string {
	out := slices.Clone(base)
	for _, ref := range added {
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

func (e *Engine) collabWorkers(ctx context.Context, workers []collabWorker) ([]string, error) {
	var names []string
	for i, worker := range workers {
		if worker.Expert != "" {
			if err := e.collabRole(ctx, &workers[i]); err != nil {
				return nil, err
			}
		}
		names = append(names, workers[i].Name)
	}
	return names, nil
}

func (e *Engine) collabRole(ctx context.Context, worker *collabWorker) error {
	it, ok := e.roleItem(ctx, worker.Expert)
	if !ok || !slices.Contains([]string{"agent", "persona", "template"}, it.Crew.Type) {
		if flowContext(ctx) {
			return failf("child_definition_missing", "the collaboration role is not pinned")
		}
		return nil
	}
	worker.Name = cmp.Or(worker.Name, it.Crew.Name)
	model := it.Crew.Model
	if flowContext(ctx) && it.Crew.ModelMode == "auto" {
		model = "auto"
	}
	worker.Model = cmp.Or(worker.Model, model)
	worker.Instructions = joinText(it.Crew.Instructions, worker.Instructions)
	if flowContext(ctx) {
		worker.Instructions = joinText(it.Crew.Role, worker.Instructions)
		worker.skills = it.Crew.Skills
	}
	return nil
}
