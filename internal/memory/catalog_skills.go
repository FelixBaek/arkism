package memory

import (
	"context"
	"errors"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

var catalogPluginPath = regexp.MustCompile(`/plugins?/`)

type catalogSkill struct {
	Item     SkillListItem
	Count    int
	Last     time.Time
	Quality  float64
	Security int
	Category string
	Update   bool
	Agents   int
}

func (s *Service) QuerySkillCatalog(ctx context.Context, token string, in CatalogQueryInput) (SkillCatalogPage, error) {
	out := SkillCatalogPage{Items: []SkillListItem{}, Authority: AuthorityDataOnly, Facets: newCatalogFacets()}
	in, err := normalizeCatalog(in, "skills")
	if err != nil {
		return out, err
	}
	ctx, actor, err := s.catalogActor(ctx, token, actionReadSkills, in.Scope)
	if err != nil {
		return out, err
	}
	binding := catalogBinding(in, "skills", actor.PrincipalID+"/"+actor.Role)
	if _, err = decodeCatalogCursor(in, binding); err != nil {
		return out, err
	}
	rows, err := s.catalogSkillRows(ctx, token, in)
	if err != nil {
		return out, err
	}
	usage, err := s.SkillUsage(ctx, token, in.Scope, 60)
	if err != nil {
		return out, err
	}
	uses := map[string]SkillUse{}
	for _, u := range usage.Items {
		uses[u.SkillID] = u
	}
	tools, err := s.catalogTools(ctx, token, in.Scope)
	if err != nil {
		return out, err
	}
	all := []catalogSkill{}
	for _, item := range rows {
		if ctx.Err() != nil {
			return out, ErrUnavailable
		}
		if !strings.Contains(strings.ToLower(item.Name+" "+item.ID+" "+item.Description), strings.ToLower(in.Query)) {
			continue
		}
		entry, e := s.catalogSkillEntry(ctx, token, in, item, uses[item.ID], tools)
		if e != nil {
			return out, e
		}
		out.Facets.Categories[entry.Category]++
		if !skillCatalogMatches(entry, in) {
			continue
		}
		all = append(all, entry)
	}
	sort.SliceStable(all, func(i, j int) bool { return skillCatalogLess(all[i], all[j], in) })
	items := make([]SkillListItem, 0, len(all))
	for _, entry := range all {
		items = append(items, entry.Item)
	}
	out.Total = len(items)
	out.Items, out.NextCursor, err = catalogWindow(items, in, binding)
	return out, err
}
func catalogSkillCategory(item SkillListItem, categories []CatalogCategory) string {
	for _, category := range categories {
		for _, rule := range category.Rules {
			if catalogCategoryRule(item, rule) {
				return category.ID
			}
		}
	}
	return ""
}
func catalogCategoryRule(item SkillListItem, rule []string) bool {
	if strings.TrimSpace(rule[1]) == "" {
		return false
	}
	pattern := "(?i)" + strings.ReplaceAll(regexp.QuoteMeta(strings.TrimSpace(rule[1])), `\*`, ".*")
	text := item.Description
	switch rule[0] {
	case "name":
		pattern = "^(?:" + pattern + ")$"
		text = item.Name
	case "path":
		text = item.ID + " " + item.SourcePath
	}
	ok, _ := regexp.MatchString(pattern, text)
	return ok
}

func skillCatalogMatches(entry catalogSkill, in CatalogQueryInput) bool {
	item := entry.Item
	if in.Category != "" && in.Category != "all" && entry.Category != in.Category && !(in.Category == "none" && entry.Category == "") {
		return false
	}
	if in.Model != "" {
		agent := strings.TrimPrefix(in.Model, "!")
		allowed := item.SkillCompatibility[agent] != "unsupported"
		if allowed == strings.HasPrefix(in.Model, "!") {
			return false
		}
	}
	switch in.State {
	case "unused":
		return entry.Count == 0
	case "plugin":
		return catalogPluginPath.MatchString(item.SourcePath)
	case "update":
		return entry.Update
	case "caution":
		return entry.Security == 0 || entry.Security == 1
	case "missing":
		return entry.Agents < len(in.Agents)
	default:
		return true
	}
}
func catalogSkillUpdate(item SkillListItem, tools *AgentTools) bool {
	if tools == nil {
		return false
	}
	parts := strings.Split(item.SourcePath, "/")
	folder := item.Name
	if len(parts) > 1 {
		folder = parts[len(parts)-2]
	}
	for _, update := range tools.SkillUpdates {
		if update.Name == folder && strings.Contains(item.SourcePath, strings.TrimPrefix(update.Repo, "~")+"/") {
			return true
		}
	}
	return false
}
func skillCatalogLess(a, b catalogSkill, in CatalogQueryInput) bool {
	direction := in.Direction
	if direction == 0 {
		direction = -1
		if in.Sort == "name" {
			direction = 1
		}
	}
	difference := 0
	switch in.Sort {
	case "name":
		difference = strings.Compare(strings.ToLower(a.Item.Name), strings.ToLower(b.Item.Name))
	case "recent":
		difference = a.Last.Compare(b.Last)
	case "updated":
		difference = a.Item.UpdatedAt.Compare(b.Item.UpdatedAt)
	case "quality":
		difference = compareCatalogNumber(a.Quality, b.Quality)
	case "security":
		difference = a.Security - b.Security
	case "agents":
		difference = a.Agents - b.Agents
	default:
		difference = a.Count - b.Count
	}
	if difference != 0 {
		return difference*direction < 0
	}
	if a.Item.Name != b.Item.Name {
		return a.Item.Name < b.Item.Name
	}
	return a.Item.ID < b.Item.ID
}
func compareCatalogNumber(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
func catalogSkillChecks(version Record, tools *AgentTools, name string) (float64, int) {
	quality, security := -1.0, -1
	if a := version.Analysis; a != nil {
		present := []bool{a.Problem != "", len(a.UseWhen) > 0, len(a.AvoidWhen) > 0, a.OutputFormat != "", len(a.SuccessCriteria) > 0, len(a.Verification) > 0}
		filled := 0
		for _, yes := range present {
			if yes {
				filled++
			}
		}
		quality = math.Round(float64(filled) * 100 / 6)
		security = catalogVersionSecurity(version)
	}
	return catalogHostSkillChecks(tools, name, quality, security)
}
func catalogVersionSecurity(version Record) int {
	a := version.Analysis
	security := 2
	if a.ExternalTransfer || len(a.SecretNames) > 0 || len(a.IrreversibleActions) > 0 {
		security = 1
	}
	if version.Inspection == nil {
		return security
	}
	if version.Inspection.PersonalData {
		security = min(security, 1)
	}
	if version.Inspection.Redacted {
		return 0
	}
	return security
}
func catalogHostSkillChecks(tools *AgentTools, name string, quality float64, security int) (float64, int) {
	if tools == nil {
		return quality, security
	}
	checks := []SkillCheck{}
	for _, check := range tools.SkillChecks {
		if check.Name == name {
			checks = append(checks, check)
		}
	}
	ranks := map[string]int{"SAFE": 0, "CAUTION": 1, "DO_NOT_INSTALL": 2}
	sort.SliceStable(checks, func(i, j int) bool { return catalogHostCheckLess(checks[i], checks[j], ranks) })
	qKnown, sKnown := false, false
	for _, check := range checks {
		if !qKnown && check.QualityScore != nil {
			quality = math.Round(*check.QualityScore)
			qKnown = true
		}
		if !sKnown && slices.Contains([]string{"SAFE", "CAUTION", "DO_NOT_INSTALL"}, check.Verdict) {
			security = 2 - ranks[check.Verdict]
			sKnown = true
		}
	}
	return quality, security
}

func catalogHostCheckLess(a, b SkillCheck, ranks map[string]int) bool {
	ar, aok := ranks[a.Verdict]
	br, bok := ranks[b.Verdict]
	if !aok {
		ar = -1
	}
	if !bok {
		br = -1
	}
	if ar != br {
		return ar > br
	}
	aq, bq := 101.0, 101.0
	if a.QualityScore != nil {
		aq = *a.QualityScore
	}
	if b.QualityScore != nil {
		bq = *b.QualityScore
	}
	return aq < bq
}

func (s *Service) catalogSkillRows(ctx context.Context, token string, in CatalogQueryInput) ([]SkillListItem, error) {
	return collectCatalogPages(ctx, func(cursor string) ([]SkillListItem, string, error) {
		after, err := decodeSkillCursor(in.Scope, cursor)
		if err != nil {
			return nil, "", err
		}
		if in.State == "trash" {
			page, e := s.ListTrashedSkills(ctx, token, in.Scope, 500, after)
			return page.Items, page.NextCursor, e
		}
		page, e := s.ListSkills(ctx, token, in.Scope, 500, after)
		return page.Items, page.NextCursor, e
	})
}
func (s *Service) catalogSkillEntry(ctx context.Context, token string, in CatalogQueryInput, item SkillListItem, use SkillUse, tools *AgentTools) (catalogSkill, error) {
	entry := catalogSkill{Item: item, Count: use.Count, Last: use.LastUsed, Quality: -1, Security: -1, Category: catalogSkillCategory(item, in.Categories)}
	if in.Sort == "quality" || in.Sort == "security" || in.State == "caution" {
		version, e := s.Get(ctx, token, colSkillVersions, in.Scope, item.VersionID)
		if e != nil && !errors.Is(e, ErrDenied) && !errors.Is(e, ErrNotFound) {
			return entry, e
		}
		if catalogActorChanged(ctx) {
			return entry, ErrDenied
		}
		entry.Quality, entry.Security = catalogSkillChecks(version, tools, item.Name)
	} else {
		entry.Quality, entry.Security = catalogSkillChecks(Record{}, tools, item.Name)
	}
	entry.Update = catalogSkillUpdate(item, tools)
	for _, agent := range in.Agents {
		if item.SkillCompatibility[agent] != "unsupported" {
			entry.Agents++
		}
	}
	return entry, nil
}
