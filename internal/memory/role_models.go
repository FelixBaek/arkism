package memory

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// 설정 › 역할별 모델: the owner picks, per scope, what each background role runs. memoryd applies its own roles
// (route_judge, skill_judge, skill_candidates) on each request; the host reads the rest from GET /v1/providers
// (extract, reflect, skill_summary through the Claude CLI; skill_scan as engines to skip). A role without a pick,
// or whose pick cannot run, uses the server default. The embedder is one per server, so it is shown, not picked.

// ProviderRole is the owner's pick for one role.
type ProviderRole struct {
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Off      []string `json:"off,omitempty"` // skill_scan: engines not to run
}

// ProviderRoleModel sets or (all empty) clears one role's pick.
type ProviderRoleModel struct {
	Scope    Scope    `json:"scope"`
	Role     string   `json:"role"`
	Model    string   `json:"model,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Off      []string `json:"off,omitempty"`
}

var (
	// Roles the host runs; it reports their defaults.
	hostRoles = []string{"extract", "reflect", "skill_summary", "skill_scan"}
	// Engines memoryd can be told to use per role ("fixed" is the rules / fixed model, no engine).
	roleEngines = map[string][]string{"route_judge": {"fixed", "clef", "jev", "chat"}, "skill_judge": {"fixed", "clef", "jev"}}
	scanEngines = []string{"spector", "evaluator", "cisco"}
	// The host runs these through the Claude CLI, so only Anthropic models are accepted.
	cliRoles       = []string{"extract", "reflect", "skill_summary"}
	roleDefaultRe  = regexp.MustCompile(`^[A-Za-z0-9._:/ ,-]{0,120}$`)
	maxSkillPicked = 8
)

func validRoleModel(in ProviderRoleModel) bool {
	if !validScope(in.Scope) {
		return false
	}
	if in.Role == "skill_scan" {
		return validScanOff(in.Off) && in.Model == "" && in.Provider == ""
	}
	return len(in.Off) == 0 && validRolePick(in)
}

// validScanOff: known engines, each once.
func validScanOff(off []string) bool {
	seen := map[string]bool{}
	for _, k := range off {
		if seen[k] || !slices.Contains(scanEngines, k) {
			return false
		}
		seen[k] = true
	}
	return true
}

// validRolePick checks the model (and provider) picked for a role other than skill_scan.
func validRolePick(in ProviderRoleModel) bool {
	switch {
	case roleEngines[in.Role] != nil:
		return in.Provider == "" && (in.Model == "" || slices.Contains(roleEngines[in.Role], in.Model))
	case in.Role == "skill_candidates":
		n, err := strconv.Atoi(in.Model)
		return in.Provider == "" && (in.Model == "" || err == nil && n >= 1 && n <= maxSkillPicked && strconv.Itoa(n) == in.Model)
	case slices.Contains(cliRoles, in.Role):
		if in.Model == "" || in.Role == "skill_summary" && in.Model == "basic" {
			return in.Provider == ""
		}
		return providerModelRe.MatchString(in.Model) && in.Provider == "anthropic"
	}
	return false
}

func validRoleDefaults(d map[string]string) bool {
	if len(d) > len(hostRoles) {
		return false
	}
	for k, v := range d {
		if !slices.Contains(hostRoles, k) || !roleDefaultRe.MatchString(v) {
			return false
		}
	}
	return true
}

// SetRoleModel stores or clears the owner's pick for one role in 설정 › 역할별 모델.
func (s *Service) SetRoleModel(ctx context.Context, token string, in ProviderRoleModel) (Record, error) {
	if !validRoleModel(in) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	out, err := s.updateProviders(ctx, in.Scope, func(p *Providers, _ time.Time) error {
		roles := map[string]ProviderRole{}
		for k, v := range p.Roles {
			roles[k] = v
		}
		if in.Model == "" && len(in.Off) == 0 {
			delete(roles, in.Role)
		} else {
			roles[in.Role] = ProviderRole{Model: in.Model, Provider: in.Provider, Off: in.Off}
		}
		p.Roles = roles
		return nil
	}, "set:role_model", d.PrincipalID)
	if err == nil && out.Providers != nil {
		s.addRoleDefaults(out.Providers) // the screen redraws from this reply
	}
	return out, err
}

// role is the owner's pick for name; zero when none (or no record).
func (p *Providers) role(name string) ProviderRole {
	if p == nil {
		return ProviderRole{}
	}
	return p.Roles[name]
}

// skillJudge is the judge route_skills uses under pick: the picked engine, none for "fixed", else the default.
func (s *Service) skillJudge(pick ProviderRole) SkillJudge {
	if pick.Model == "fixed" {
		return nil
	}
	if j := s.Judges[pick.Model]; j != nil {
		return j
	}
	return s.Router
}

// modelPicker is the picker 라우팅 uses under pick: the picked engine, none for "fixed", else the default.
func (s *Service) modelPicker(pick ProviderRole) ModelPicker {
	if pick.Model == "fixed" {
		return nil
	}
	if m := s.Pickers[pick.Model]; m != nil {
		return m
	}
	return s.ModelPicker
}

// skillCandidates is how many candidates route_skills hands the judge under pick (0 = the built-in 8).
func (s *Service) skillCandidates(pick ProviderRole) int {
	if n, err := strconv.Atoi(pick.Model); err == nil && n >= 1 && n <= maxSkillPicked {
		return n
	}
	return s.SkillCandidates
}

// engineName is the key of v in engines, "fixed" for none, or "" when v is not one of them.
func engineName[T comparable](engines map[string]T, v T) string {
	var zero T
	if v == zero {
		return "fixed"
	}
	if t := reflect.TypeOf(v); t == nil || !t.Comparable() { // a func engine (tests) cannot be compared
		return ""
	}
	for k, e := range engines {
		if reflect.TypeOf(e) == reflect.TypeOf(v) && e == v {
			return k
		}
	}
	return ""
}

// addRoleDefaults fills in what memoryd's own roles run by default and which engines it can run.
func (s *Service) addRoleDefaults(p *Providers) {
	defaults := map[string]string{}
	for k, v := range p.RoleDefaults {
		defaults[k] = v
	}
	defaults["route_judge"] = engineName(s.Pickers, s.ModelPicker)
	defaults["skill_judge"] = engineName(s.Judges, s.Router)
	defaults["skill_candidates"] = strconv.Itoa(s.skillCandidates(ProviderRole{}))
	if defaults["skill_candidates"] == "0" {
		defaults["skill_candidates"] = strconv.Itoa(maxSkillPicked)
	}
	defaults["embed"] = "none"
	if s.Embedder != nil {
		defaults["embed"] = s.Embedder.Space()
	}
	options := map[string][]string{"embed": {defaults["embed"]}}
	for role, all := range roleEngines {
		have := []string{"fixed"}
		for _, k := range all[1:] {
			if role == "route_judge" && s.Pickers[k] != nil || role == "skill_judge" && s.Judges[k] != nil {
				have = append(have, k)
			}
		}
		options[role] = have
	}
	p.RoleDefaults, p.RoleOptions = defaults, options
}
