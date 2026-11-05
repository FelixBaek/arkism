package memory

import (
	"context"
	"errors"
	"strings"
)

const (
	entityModelBatch     = 10
	entityModelMax       = 50
	entityModelTextRunes = 2000
)

const entityModelSystem = `You list the named entities in short notes. Entity types: person, project, repo, tool, product, file, service, place, organization, concept.
Copy each name exactly as written in its note. Skip generic words, secrets, e-mail addresses and URLs.
Reply with JSON only: {"items":[{"i":0,"entities":[{"name":"...","type":"..."}]}]}`

type entityModelAnswer struct {
	Items []struct {
		I        int `json:"i"`
		Entities []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entities"`
	} `json:"items"`
}

// modelEntityFinds asks the server model, in batches, for names the rules miss.
// Names must occur in the memory text; the budget gate is the wiki's.
func (s *Service) modelEntityFinds(ctx context.Context, token string, scope Scope, d Decision, memories []Record) ([]memoryEntities, int, error) {
	if s.Wiki == nil {
		return nil, 0, ErrModelNotConfigured
	}
	job := &WikiJob{MaxTotalTokens: wikiDefaultJobTokens}
	var out []memoryEntities
	for start := 0; start < len(memories); start += entityModelBatch {
		batch := memories[start:min(start+entityModelBatch, len(memories))]
		user, sent := entityModelPrompt(batch)
		if len(sent) == 0 {
			continue
		}
		t, err := s.authorizeAt(ctx, token, "generate:entities", scope, "", user, wikiDestination)
		if err != nil {
			return nil, job.Calls, err
		}
		if t.PrincipalID != d.PrincipalID || !t.ExternalTransfer || !t.ContentSafe {
			return nil, job.Calls, ErrDenied
		}
		answer, err := s.wikiCall(ctx, scope, job, "wiki_entities", entityModelSystem, user)
		if errors.Is(err, errWikiBudget) {
			break // keep what earlier batches found
		}
		if err != nil {
			return nil, job.Calls, err
		}
		out = append(out, parseEntityModelAnswer(answer, sent)...)
	}
	return out, job.Calls, nil
}

// entityModelPrompt numbers the notes it sends; private, restricted or secret-bearing notes are not sent.
func entityModelPrompt(batch []Record) (string, []Record) {
	type note struct {
		I    int    `json:"i"`
		Text string `json:"text"`
	}
	var notes []note
	var sent []Record
	for _, r := range batch {
		text := entityText(r)
		if r.Sensitivity == "private" || r.Sensitivity == "restricted" || !safeTransfer(text) {
			continue
		}
		text = runeCutString(text, entityModelTextRunes)
		notes = append(notes, note{I: len(sent), Text: text})
		sent = append(sent, r)
	}
	return structuredText(notes), sent
}

func parseEntityModelAnswer(answer string, sent []Record) []memoryEntities {
	var parsed entityModelAnswer
	if !decodeModelJSON(answer, &parsed) {
		return nil
	}
	var out []memoryEntities
	seen := map[int]bool{}
	for _, item := range parsed.Items {
		if item.I < 0 || item.I >= len(sent) || seen[item.I] {
			continue
		}
		seen[item.I] = true
		r := sent[item.I]
		lower := strings.ToLower(entityText(r))
		f := memoryEntities{Memory: r, Origin: originModel}
		for _, e := range item.Entities {
			name := normalizeEntityName(e.Name)
			if entityTypes[e.Type] && validEntityName(name) && strings.Contains(lower, strings.ToLower(name)) && len(f.Names) < entityMaxPerMemory {
				f.Names = append(f.Names, entityCandidate{Name: name, Type: e.Type})
			}
		}
		out = append(out, f)
	}
	return out
}
