package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func (r *entityTestRepo) MemoryTags(ctx context.Context, scope Scope, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range ids {
		if m, err := r.Repository.Get(ctx, colMemories, scope, id); err == nil {
			out[id] = m.Tags
		}
	}
	return out, nil
}

func entityByName(repo *entityTestRepo, name string) NamedEntity {
	for _, e := range repo.ents {
		if e.Name == name {
			return e
		}
	}
	return NamedEntity{}
}

func TestTrigramSimilarityAndCompatibility(t *testing.T) {
	sim := func(a, b string) float64 { return trigramSimilarity(trigramSet(a), trigramSet(b)) }
	if sim("GPT-4", "gpt 4") != 1 || sim("", "") != 0 {
		t.Fatal("punctuation and case must not count")
	}
	if s := sim("PostgreSQL", "Postgres"); s < 0.66 || s > 0.67 {
		t.Fatal("pg_trgm value", s)
	}
	cases := []struct {
		a, b string
		ok   bool
	}{
		{"John Smith", "Jane Smith", false},
		{"Room 101", "Room 102", false},
		{"Python", "Python 3", true},
		{"Arkism Server", "Arkism", true},
		{"PostgreSQL", "Postgres", true},
	}
	for _, c := range cases {
		if entityNamesCompatible(c.a, c.b) != c.ok {
			t.Errorf("%q/%q compatible = %v", c.a, c.b, !c.ok)
		}
	}
}

func TestEntitySimilarNamesBecomeProposals(t *testing.T) {
	f, repo := entityFixture(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	entityMemory(t, f, repo, "m1", "We run PostgreSQL here.", day)
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	entityMemory(t, f, repo, "m2", "We tuned Postgres today.", day)
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	pg, full := entityByName(repo, "Postgres"), entityByName(repo, "PostgreSQL")
	if len(pg.Similar) != 1 || pg.Similar[0].ID != full.ID || pg.Similar[0].Score != 0.667 {
		t.Fatal("default mode must propose, not merge", pg, repo.ents)
	}
	if list, err := f.svc.ListEntities(testCtx, f.human, EntityListInput{Scope: scopeA, Similar: true}); err != nil || list.Total != 1 || list.Items[0].ID != pg.ID {
		t.Fatal("people list proposals", list, err)
	}
	if list, _ := f.svc.ListEntities(testCtx, f.agent, EntityListInput{Scope: scopeA, Similar: true}); list.Total != 0 {
		t.Fatal("agents never see proposals", list)
	}
	if _, err := f.svc.PatchEntity(testCtx, f.agent, pg.ID, EntityPatchInput{Scope: scopeA, DismissSimilar: true}); !errors.Is(err, ErrDenied) {
		t.Fatal("agent dismissed", err)
	}
	if _, err := f.svc.PatchEntity(testCtx, f.human, pg.ID, EntityPatchInput{Scope: scopeA, DismissSimilar: true}); err != nil || repo.ents[pg.ID].Similar != nil {
		t.Fatal("dismiss", err, repo.ents[pg.ID])
	}
}

func TestEntityResolutionModes(t *testing.T) {
	for _, c := range []struct {
		mode        string
		newEntities int
		similar     bool
	}{{entityResolveExact, 1, false}, {entityResolvePropose, 1, true}, {entityResolveSimilar, 0, false}} {
		f, repo := entityFixture(t)
		m1 := entityMemory(t, f, repo, "m1", "Arkism Server notes", time.Now().UTC())
		m2 := entityMemory(t, f, repo, "m2", "server arkism notes", time.Now().UTC())
		cfg := SpaceConfig{EntityResolution: c.mode}
		first := []memoryEntities{{Memory: m1, Origin: originRule, Names: []entityCandidate{{Name: "Arkism Server", Type: entityTypeConcept}}}}
		if _, err := f.svc.applyEntityExtraction(testCtx, scopeA, "human-1", cfg, first, false); err != nil {
			t.Fatal(err)
		}
		second := []memoryEntities{{Memory: m2, Origin: originRule, Names: []entityCandidate{{Name: "server arkism", Type: entityTypeConcept}}}}
		out, err := f.svc.applyEntityExtraction(testCtx, scopeA, "human-1", cfg, second, false)
		if err != nil || out.NewEntities != c.newEntities || (len(entityByName(repo, "server arkism").Similar) > 0) != c.similar {
			t.Fatal(c.mode, out, err, repo.ents)
		}
		// Caller-stated names are taken literally in every mode.
		input := []memoryEntities{{Memory: m2, Origin: originInput, Names: []entityCandidate{{Name: "Arkism Servers", Type: entityTypeConcept}}}}
		if _, err := f.svc.applyEntityExtraction(testCtx, scopeA, "human-1", cfg, input, false); err != nil || entityByName(repo, "Arkism Servers").Similar != nil {
			t.Fatal(c.mode, "input resolved by similarity", err)
		}
	}
}

func labelConfig() SpaceConfig {
	return SpaceConfig{EntityLabels: []EntityLabelGroup{
		{Key: "priority", Tag: true, Values: []EntityLabelValue{{Value: "High"}, {Value: "Low"}}},
		{Key: "topic", Type: labelValueMany, Values: []EntityLabelValue{{Value: "infra"}, {Value: "ui"}}},
		{Key: "owner", Type: labelTextOne},
	}}
}

func TestEntityLabelSchema(t *testing.T) {
	cfg := labelConfig()
	if !validEntityLabels(cfg.EntityLabels) {
		t.Fatal("valid schema rejected")
	}
	for _, bad := range [][]EntityLabelGroup{
		{{Key: "Bad Key", Values: []EntityLabelValue{{Value: "x"}}}},
		{{Key: "a", Type: "map"}},
		{{Key: "a"}},
		{{Key: "a", Type: labelTextOne, Values: []EntityLabelValue{{Value: "x"}}}},
		{{Key: "a", Values: []EntityLabelValue{{Value: "x"}, {Value: "X"}}}},
		{{Key: "a", Type: labelTextOne}, {Key: "a", Type: labelTextOne}},
	} {
		if validEntityLabels(bad) {
			t.Errorf("accepted %+v", bad)
		}
	}
	got := candidateNames(cfg.labelCandidates([]string{"priority:high", "priority:low", "topic:infra", "topic:ui", "topic:db", "owner:mina", "owner:jun", "color:red"}))
	want := map[string]string{"priority:High": "label", "topic:infra": "label", "topic:ui": "label", "owner:mina": "label"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Fatal("missing", k, got)
		}
	}
	tags := cfg.labelTags([]EntityInput{{Name: "priority:high", Type: "label"}, {Name: "topic:ui", Type: "label"}, {Name: "priority:High"}})
	if !slices.Equal(tags, []string{"priority:High"}) {
		t.Fatal("tag groups only", tags)
	}
	if !strings.Contains(cfg.entityModelGuidance(), "LABEL_SCHEMA") || (SpaceConfig{}).entityModelGuidance() != "" {
		t.Fatal("prompt guidance")
	}
	off := false
	cfg.EntitiesAllowFreeForm = &off
	sent := []Record{{ID: "m1", Text: "mina fixed the infra"}}
	found := parseEntityModelAnswer(`{"items":[{"i":0,"entities":[{"name":"mina","type":"person"}],"labels":["topic:infra","topic:nope"]}]}`, sent, cfg)
	if len(found) != 1 || !slices.Equal(slices.Collect(func(yield func(string) bool) {
		for _, n := range found[0].Names {
			yield(n.Name)
		}
	}), []string{"topic:infra"}) {
		t.Fatal("labels only when free form is off", found)
	}
	if !validEntityInputs([]EntityInput{{Name: "topic:infra", Type: "label"}, {Name: "Mina", Type: "person"}}) ||
		validEntityInputs([]EntityInput{{Name: "infra", Type: "label"}}) || validEntityInputs([]EntityInput{{Name: "x", Type: "Bad Type"}}) {
		t.Fatal("input validation")
	}
}

func TestCandidateEntitiesAndLabelTags(t *testing.T) {
	f, repo := entityFixture(t)
	cfg := labelConfig()
	putSettings(t, f, scopeA, SpaceConfigInput{EntityLabels: &cfg.EntityLabels})
	src := f.source()
	in := candidateInput(CreateInput{Scope: scopeA, Text: "fixed the login page", Sources: refs(src),
		Entities: []EntityInput{{Name: "priority:high", Type: "label"}, {Name: "Mina Park", Type: "person"}, {Name: "Odd", Type: "gadget"}}})
	c := f.create(f.human, "memory_candidates", in)
	if !slices.Contains(c.Tags, "priority:High") || len(c.Entities) != 3 {
		t.Fatal("candidate keeps entities and gets tag-group tags", c.Tags, c.Entities)
	}
	if _, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	label, person := entityByName(repo, "priority:High"), entityByName(repo, "Mina Park")
	if label.Type != entityTypeLabel || person.Type != "person" || entityByName(repo, "Odd").ID != "" {
		t.Fatal("input entities", repo.ents)
	}
	for _, m := range repo.edges {
		if m.Origin != originInput || m.Relation != relMentions {
			t.Fatal("input edges", repo.edges)
		}
	}
	if len(repo.edges) != 2 {
		t.Fatal(repo.edges)
	}
	// Free types let the stated type through; a label never changes type.
	on := true
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, EntityFreeTypes: &on})
	if _, err := f.svc.PatchEntity(testCtx, f.human, person.ID, EntityPatchInput{Scope: scopeA, Type: ptr("gadget")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PatchEntity(testCtx, f.human, label.ID, EntityPatchInput{Scope: scopeA, Type: ptr("concept")}); !errors.Is(err, ErrInvalid) {
		t.Fatal("label changed type", err)
	}
	bad := CreateInput{Scope: scopeA, Text: "x", Sources: refs(src), Entities: []EntityInput{{Name: "x", Type: "label"}}}
	if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(bad)); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad entity input accepted", err)
	}
}

func TestEntityGraphTagFiltersAndMinCount(t *testing.T) {
	f, repo := entityFixture(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, m := range []struct{ id, text, tag string }{
		{"m1", "Arkism stores data in Postgres.", "infra"},
		{"m2", "We tuned Postgres for Arkism.", "infra"},
		{"m3", "We cache Arkism data in Redis.", "cache"},
	} {
		r := entityMemory(t, f, repo, m.id, m.text, day)
		r.Tags = []string{m.tag}
		if err := repo.Repository.Replace(testCtx, colMemories, r, r.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	edges := func(in EntityGraphInput) []int {
		in.Scope = scopeA
		g, err := f.svc.EntityGraphView(testCtx, f.human, in)
		if err != nil {
			t.Fatal(err)
		}
		var w []int
		for _, e := range g.Edges {
			w = append(w, e.Weight)
		}
		slices.Sort(w)
		return w
	}
	if w := edges(EntityGraphInput{}); !slices.Equal(w, []int{1, 2}) {
		t.Fatal("all", w)
	}
	if w := edges(EntityGraphInput{MinCount: 2}); !slices.Equal(w, []int{2}) {
		t.Fatal("min_count", w)
	}
	if w := edges(EntityGraphInput{Tags: []string{"cache"}}); !slices.Equal(w, []int{1}) {
		t.Fatal("tags", w)
	}
	if w := edges(EntityGraphInput{TagGroups: [][]TagTest{{{Tags: []string{"infra"}, Not: true}}}}); !slices.Equal(w, []int{1}) {
		t.Fatal("tag_groups", w)
	}
	if _, err := f.svc.EntityGraphView(testCtx, f.human, EntityGraphInput{Scope: scopeA, MinCount: 1001}); !errors.Is(err, ErrInvalid) {
		t.Fatal("min_count bound", err)
	}
	in, err := parseEntityGraphQuery("workspace_id=ws-a&project_id=proj-a&tags=a,b&tags_match=all&min_count=3&tag_groups=" + `[[{"tags":["x"],"not":true}]]`)
	if err != nil || !slices.Equal(in.Tags, []string{"a", "b"}) || in.TagsMatch != "all" || in.MinCount != 3 || len(in.TagGroups) != 1 || !in.TagGroups[0][0].Not {
		t.Fatal(in, err)
	}
	if _, err := parseEntityGraphQuery("workspace_id=ws-a&project_id=proj-a&tag_groups=nope"); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad tag_groups", err)
	}
}

func TestSpaceEntitySettings(t *testing.T) {
	f := newFixture(t)
	free := true
	f.svc.SpaceTemplate = &SpaceTemplate{Config: SpaceConfig{EntityFreeTypes: &free, EntityLabels: labelConfig().EntityLabels}}
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil || view.Config.EntityResolution != entityResolvePropose || !*view.Config.EntityFreeTypes || len(view.Config.EntityLabels) != 3 || !*view.Config.EntitiesAllowFreeForm {
		t.Fatal("server and template defaults", view.Config, err)
	}
	mode, off := entityResolveExact, false
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, EntityResolution: &mode, EntityFreeTypes: &off})
	view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if view.Config.EntityResolution != entityResolveExact || *view.Config.EntityFreeTypes {
		t.Fatal("space overrides", view.Config)
	}
	var reset SpaceConfigInput
	if err := reset.UnmarshalJSON([]byte(`{"expected_revision":-1,"entity_free_types":null}`)); err != nil {
		t.Fatal(err)
	}
	putSettings(t, f, scopeA, reset)
	if view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA); !*view.Config.EntityFreeTypes {
		t.Fatal("null resets to the template", view.Config)
	}
	weird := "fuzzy"
	badLabels := []EntityLabelGroup{{Key: "a"}}
	for _, in := range []SpaceConfigInput{{ExpectedRevision: -1, EntityResolution: &weird}, {ExpectedRevision: -1, EntityLabels: &badLabels}} {
		if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, in, false); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid entity setting accepted", err)
		}
	}
}
