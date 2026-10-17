package memory

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Reach suggestions (ST-825 / FR-012): the system proposes a memory's reach and a person decides.
// A suggestion is stored on a new candidate or memory and copied to the memory on approval; it never
// changes Reach. Acceptance is the human-only POST /v1/memories/{id}/reach (ST-824); a dismissal only
// hides that one suggestion. Reasons are fixed phrases: no memory text, query or path goes into them.

type ReachSuggestion struct {
	Reach        string  `json:"reach" bson:"reach"`
	ReachProject string  `json:"reach_project,omitempty" bson:"reach_project,omitempty"`
	Reason       string  `json:"reason" bson:"reason"`
	Confidence   float64 `json:"confidence" bson:"confidence"`
}

var (
	reachPathPattern = regexp.MustCompile(`[\w.-]+/[\w./-]*\.[A-Za-z0-9]{1,5}\b`)
	// Case-sensitive names, so the English verb "go" is not a language.
	reachLanguagePattern = regexp.MustCompile(`\b(Go|Rust|Python|TypeScript|JavaScript|Java|Kotlin|Swift|Ruby|PHP|Tauri|React|Vue|Svelte)\b|(?i:\b(golang|cargo|npm|pnpm|yarn|gofmt|rustfmt|pytest|eslint)\b)`)
)

// repoTag is the capture's repo:<key> tag (ST-824 kept it), lowercased like a project key.
func repoTag(tags []string) string {
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, "repo:"); ok {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" && validProjectKey(v) {
				return v
			}
		}
	}
	return ""
}

// suggestReach is the save-time suggestion from kind, memory_context, the repo tag and the text.
func suggestReach(r Record) ReachSuggestion {
	if r.Kind == "user" {
		return ReachSuggestion{Reach: ReachPersonal, Reason: "사람에 대한 선호(kind user)", Confidence: 0.9}
	}
	if r.MemoryContext != nil && slices.Equal(r.MemoryContext.Domains, []string{"personal"}) {
		return ReachSuggestion{Reach: ReachPersonal, Reason: "개인 맥락으로만 분류됨", Confidence: 0.7}
	}
	repo := repoTag(r.Tags)
	text := r.Text + "\n" + r.Summary
	project := func(reason string, confidence float64) ReachSuggestion {
		if repo == "" {
			return ReachSuggestion{Reach: ReachProject, Reason: reason + ", 어느 프로젝트인지는 사람이 고름", Confidence: confidence - 0.1}
		}
		return ReachSuggestion{Reach: ReachProject, ReachProject: repo, Reason: reason, Confidence: confidence}
	}
	if _, name, ok := strings.Cut(repo, "/"); ok && len(name) >= 3 && strings.Contains(strings.ToLower(text), name) {
		return project("본문이 저장소 이름을 가리킴", 0.8)
	}
	if reachPathPattern.MatchString(text) {
		return project("본문에 파일 경로가 있음", 0.65)
	}
	if reachLanguagePattern.MatchString(text) {
		return project("본문이 특정 언어·도구를 가리킴", 0.55)
	}
	return ReachSuggestion{Reach: ReachGeneral, Reason: "특정 저장소·언어 표시 없음", Confidence: 0.6}
}

// currentReachSuggestion: two or more projects used a project memory → general (FR-012 promotion);
// otherwise the stored suggestion, or one computed for memories saved before ST-825.
func currentReachSuggestion(r Record, usedProjects []string) ReachSuggestion {
	if memoryReach(r) == ReachProject && len(usedProjects) >= 2 {
		return ReachSuggestion{Reach: ReachGeneral, Reason: fmt.Sprintf("%d개 프로젝트에서 불러와 쓰임", len(usedProjects)), Confidence: min(0.5+0.1*float64(len(usedProjects)), 0.9)}
	}
	sg := suggestReach(r)
	if r.ReachSuggestion != nil {
		sg = *r.ReachSuggestion
	}
	if sg.Reach == ReachProject && sg.ReachProject == "" {
		if memoryReach(r) == ReachProject { // a person already picked the project
			sg.ReachProject = r.ReachProject
		} else if len(usedProjects) == 1 {
			sg.ReachProject = usedProjects[0]
		}
	}
	return sg
}

func reachSuggestionKey(reach, project string) string { return reach + "|" + project }

type ReachSuggestionItem struct {
	MemoryID     string          `json:"memory_id"`
	Revision     int64           `json:"revision"`
	Kind         string          `json:"kind"`
	Text         string          `json:"text"`
	Reach        string          `json:"reach"`
	ReachProject string          `json:"reach_project,omitempty"`
	UsedProjects int             `json:"used_projects"`
	Suggestion   ReachSuggestion `json:"suggestion"`
}
type ReachSuggestionPage struct {
	Items      []ReachSuggestionItem `json:"items"`
	NextCursor string                `json:"next_cursor"`
	Authority  string                `json:"authority"`
}

// ReachSuggestions pages active memories (the memory list's checks) and returns those whose
// suggestion differs from their reach and was not dismissed. Read-only: nothing is changed.
func (s *Service) ReachSuggestions(ctx context.Context, token string, in MemoryListInput) (ReachSuggestionPage, error) {
	in.Status, in.IncludeInactive = "", false
	page, e := s.ListMemories(ctx, token, in)
	if e != nil {
		return ReachSuggestionPage{}, e
	}
	if _, e = s.authorize(ctx, token, "read:usage", in.Scope, "", ""); e != nil {
		return ReachSuggestionPage{}, e
	}
	used := map[string][]string{}
	if len(page.Items) > 0 {
		ids := make([]string, len(page.Items))
		for i, r := range page.Items {
			ids[i] = r.ID
		}
		counts, e := s.Repo.MemoryUseCounts(ctx, in.Scope, ids)
		if e != nil {
			return ReachSuggestionPage{}, e
		}
		for _, c := range counts {
			used[c.MemoryID] = c.Projects
		}
	}
	out := ReachSuggestionPage{Items: []ReachSuggestionItem{}, NextCursor: page.NextCursor, Authority: AuthorityDataOnly}
	for _, r := range page.Items {
		sg := currentReachSuggestion(r, used[r.ID])
		key := reachSuggestionKey(sg.Reach, sg.ReachProject)
		if key == reachSuggestionKey(memoryReach(r), r.ReachProject) || key == r.ReachSuggestionDismissed {
			continue
		}
		out.Items = append(out.Items, ReachSuggestionItem{MemoryID: r.ID, Revision: r.Revision, Kind: r.Kind, Text: r.Text, Reach: memoryReach(r), ReachProject: r.ReachProject, UsedProjects: len(used[r.ID]), Suggestion: sg})
	}
	return out, nil
}

// ReachSuggestionDismissInput names the suggestion a person turned down.
type ReachSuggestionDismissInput struct {
	Scope            Scope  `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reach            string `json:"reach"`
	ReachProject     string `json:"reach_project,omitempty"`
}

// DismissReachSuggestion hides one suggestion on one memory. Only a human may (ADR-0022). Like the
// reach migration it keeps the revision: the memory's content and reach do not change.
func (s *Service) DismissReachSuggestion(ctx context.Context, token, id string, in ReachSuggestionDismissInput) (Record, error) {
	in.ReachProject = strings.ToLower(strings.TrimSpace(in.ReachProject))
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.ExpectedRevision < 1 || !validRunLabel(id, 256) || containsSecret(id) ||
		!slices.Contains([]string{ReachPersonal, ReachGeneral, ReachProject}, in.Reach) || in.Reach != ReachProject && in.ReachProject != "" || !validProjectKey(in.ReachProject) {
		return Record{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "transition:memories", in.Scope, id, "")
	if e != nil {
		return Record{}, e
	}
	if actor.Role != roleHuman {
		return Record{}, ErrDenied
	}
	if s.Repo == nil {
		return Record{}, ErrUnavailable
	}
	var out Record
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, e := tx.Get(ctx, colMemories, in.Scope, id)
		if e != nil {
			return e
		}
		if r.PrivacyQuarantined || r.ContentErased {
			return ErrNotFound
		}
		if r.Revision != in.ExpectedRevision || !live(r) {
			return ErrConflict
		}
		r.ReachSuggestionDismissed = reachSuggestionKey(in.Reach, in.ReachProject)
		if e = tx.Replace(ctx, colMemories, r, r.Revision); e != nil {
			return e
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "dismiss_reach_suggestion:memories", id); e != nil {
			return e
		}
		out = r
		return nil
	})
	if e != nil {
		return Record{}, e
	}
	return out, nil
}
