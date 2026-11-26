package memory

import (
	"context"
	"slices"
	"strings"
	"unicode"
)

const reasonNamedSubject = "named_subject"

// A rare subject name on its own is insufficient evidence. Require both the
// name and a topic match in records that passed the original search filters.
func (s *Service) namedSubjectLane(ctx context.Context, token, collection string, in SearchInput, clean func([]Hit) ([]Hit, error)) ([]Hit, error) {
	if collection != colMemories || len(in.Modes) != 0 || in.IncludeInactive || in.RelatedTo != nil || in.contextView != nil || in.retrievalQuery != "" {
		return nil, nil
	}
	name := mixedQueryName(in.Query)
	if name == "" {
		return nil, nil
	}
	sub := in
	sub.Query, sub.Modes, sub.Limit = name, []string{"fts"}, 3
	lanes, err := s.searchLists(ctx, token, collection, sub, nil, "")
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, lane := range lanes {
		hits = append(hits, lane...)
	}
	if len(hits) == 0 || len(hits) > 2 {
		return nil, nil
	}
	hits, err = clean(hits)
	if err != nil {
		return nil, err
	}
	var named []Hit
	for _, h := range hits {
		if (mixedNameMatch(name, h.Text) || mixedNameMatch(name, h.Summary)) && mixedTopicMatch(name, in.Query, h) {
			h.Reasons = append(h.Reasons, reasonNamedSubject)
			named = append(named, h)
		}
	}
	return named, nil
}

func mixedQueryName(query string) string {
	if !strings.ContainsFunc(query, isHangul) {
		return ""
	}
	fields := strings.Fields(query)
	end := 0
	for _, word := range fields {
		if word[0] < 'A' || word[0] > 'Z' || strings.Trim(word, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
			break
		}
		end++
	}
	if end == 0 || end == len(fields) {
		return ""
	}
	rest := strings.Join(fields[end:], " ")
	if strings.ContainsAny(rest, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return ""
	}
	for _, v := range []string{"말고", "제외", "아니", "아닌", "빼고", "대신", "비교"} {
		if strings.Contains(rest, v) {
			return ""
		}
	}
	return strings.Join(fields[:end], " ")
}

func mixedNameMatch(name, text string) bool {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' })
	return strings.Contains(" "+strings.Join(words, " ")+" ", " "+strings.ToLower(name)+" ")
}

func mixedTopicMatch(name, query string, h Hit) bool {
	text := SearchTokens(h.Text + " " + h.Summary)
	topic := strings.Join(strings.Fields(query)[len(strings.Fields(name)):], " ")
	for _, term := range searchQueryTerms(topic) {
		if termInTokens(term, func(word string) bool { return slices.Contains(text, word) }) {
			return true
		}
		switch term {
		case "로드맵":
			if slices.Contains(text, "roadmap") || slices.Contains(text, "roadmaps") {
				return true
			}
		case "라벨", "레이블":
			if slices.Contains(text, "label") || slices.Contains(text, "labels") {
				return true
			}
		}
	}
	return false
}
