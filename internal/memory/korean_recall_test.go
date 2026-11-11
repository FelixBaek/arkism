package memory

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

type koreanRecallQuery struct {
	Category, Text string
	Relevant       []string
}

// Korean requests against Korean memories and skills through PostgreSQL's
// keyword lanes: a name keeps matching whatever particle is glued to it, and an
// unrelated request returns nothing. No model is contacted, so paraphrases
// (the meaning lane's job) are counted, not required.
func TestPostgresKoreanRecallQuality(t *testing.T) {
	raw, err := os.ReadFile("testdata/korean_recall_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Memories []struct{ ID, Text string }
		Skills   []struct {
			ID, Name, Description string
			Examples              []string
		}
		MemoryQueries []koreanRecallQuery `json:"memory_queries"`
		SkillQueries  []koreanRecallQuery `json:"skill_queries"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || len(data.MemoryQueries) < 16 || len(data.SkillQueries) < 16 {
		t.Fatal("fixture changed", err)
	}
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	ids := map[string]string{}
	for _, m := range data.Memories {
		ids[f.activeMemory(m.Text).ID] = m.ID
	}
	for _, s := range data.Skills {
		body := "# " + s.Name + "\n" + s.Description
		hash := ContentHash(body)
		a := &SkillAnalysis{SkillID: s.ID, Version: hash[:16], ContentHash: hash, Name: s.Name, Description: s.Description, Problem: s.Description,
			UseWhen: []string{s.Description}, ExampleRequests: s.Examples, InputFormat: "요청", OutputFormat: "결과",
			SuccessCriteria: []string{"지시를 따른다"}, Verification: []string{"결과를 확인한다"},
			SourcePath: "/skills/" + s.ID + "/SKILL.md", SourceFile: "SKILL.md"}
		res, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: s.ID, Path: a.SourcePath, Content: body, Analysis: a})
		if err != nil {
			t.Fatal(s.ID, err)
		}
		ids[res.Record.ID] = s.ID
	}
	for purpose, queries := range map[string][]koreanRecallQuery{"answer": data.MemoryQueries, "skills": data.SkillQueries} {
		stats := koreanRecallCounts(t, f, ids, purpose, queries)
		t.Logf("%s %+v", purpose, stats)
		for _, category := range []string{"particle", "rare_particle"} {
			if c := stats[category]; c.Queries == 0 || c.Found != c.Queries {
				t.Errorf("%s %s: a particle hid the word: %+v", purpose, category, c)
			}
		}
		if c := stats["negative"]; c.Queries == 0 || c.Found+c.Wrong != 0 {
			t.Errorf("%s: an unrelated request returned a row: %+v", purpose, c)
		}
	}
}

type koreanRecallCount struct{ Queries, Found, Wrong int }

func koreanRecallCounts(t *testing.T, f *fixture, ids map[string]string, purpose string, queries []koreanRecallQuery) map[string]koreanRecallCount {
	stats := map[string]koreanRecallCount{}
	for _, q := range queries {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: q.Text, Purpose: purpose, Limit: 3, MaxChars: 6000, Modes: []string{"exact", "fts"}})
		if err != nil {
			t.Fatal(q.Text, err)
		}
		c := stats[q.Category]
		c.Queries++
		got := []string{}
		for _, item := range out.Items {
			got = append(got, ids[item.ID])
		}
		found := slices.ContainsFunc(got, func(id string) bool { return slices.Contains(q.Relevant, id) })
		if found {
			c.Found++
		} else if len(got) > 0 {
			c.Wrong++
		}
		if found == (len(q.Relevant) == 0) || len(q.Relevant) == 0 && len(got) > 0 {
			t.Logf("%s %s %q want %v got %v", purpose, q.Category, q.Text, q.Relevant, got)
		}
		stats[q.Category] = c
	}
	return stats
}
