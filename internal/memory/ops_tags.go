package memory

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
)

// Tags are reviewer-owned labels on active memories. Editing them never
// touches text or approval; each change is one audited revision.

type TagCount struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

type TagList struct {
	Items []TagCount `json:"items"`
	// Total counts the tags matching Q before Offset and Limit cut the page.
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// TagListInput filters ListTags. Q is a case-insensitive pattern where * matches
// any run of characters ("user:*", "*-admin"); without * it must match the whole
// tag. Source picks the tags of active memories (default) or of mental models.
type TagListInput struct {
	Scope  Scope
	Q      string
	Source string
	Limit  int
	Offset int
}

const (
	tagPageDefault = 100
	tagPageMax     = 1000
	tagPatternMax  = 256
)

type MemoryTagsInput struct {
	Scope            Scope    `json:"scope"`
	ExpectedRevision int64    `json:"expected_revision,omitempty"`
	Add              []string `json:"add,omitempty"`
	Remove           []string `json:"remove,omitempty"`
}

type TagRenameInput struct {
	Scope Scope  `json:"scope"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type TagRenameResult struct {
	Renamed int  `json:"renamed"`
	Skipped int  `json:"skipped"`
	More    bool `json:"more"`
}

// systemTagPrefix marks capture provenance that session archive use checks
// read (session_archive.go); a human edit must not forge or cut that link.
const systemTagPrefix = "session:"

// validTagEdit accepts plain labels only: valid, and no system provenance tag.
func validTagEdit(tags []string) bool {
	return validTags(tags) && !slices.ContainsFunc(tags, func(t string) bool { return strings.HasPrefix(t, systemTagPrefix) })
}

// validTagInput is validTagEdit plus the ingestion privacy filters.
func validTagInput(tags []string) bool {
	if !validTagEdit(tags) {
		return false
	}
	for _, t := range tags {
		if containsSecret(t) || sessionPrivateOpenRE.MatchString(t) {
			return false
		}
	}
	return true
}

// ListTags counts tags over a scope's active memories (or mental models), most used first.
func (s *Service) ListTags(ctx context.Context, token string, in TagListInput) (TagList, error) {
	if in.Limit == 0 {
		in.Limit = tagPageDefault
	}
	out := TagList{Items: []TagCount{}, Limit: in.Limit, Offset: in.Offset}
	collection := map[string]string{"": colMemories, "memories": colMemories, "mental_models": colMentalModels}[in.Source]
	if collection == "" || in.Limit < 1 || in.Limit > tagPageMax || in.Offset < 0 || len(in.Q) > tagPatternMax {
		return out, ErrInvalid
	}
	if _, err := s.opsAuthorize(ctx, token, "read:"+collection, in.Scope, "", roleHuman, roleService, roleAgent, rolePipeline); err != nil {
		return out, err
	}
	st, err := s.opsStore()
	if err != nil {
		return out, err
	}
	rows, err := st.OpsCounts(ctx, opsCountQuery{Scope: in.Scope, Collection: collection, By: "tag", Statuses: []string{"active"}})
	if err != nil {
		return out, err
	}
	all := []TagCount{}
	for _, r := range rows {
		if tag := opsText(r.Key); tagMatches(in.Q, tag) {
			all = append(all, TagCount{Tag: tag, Count: r.Count})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Count > all[j].Count })
	out.Total = len(all)
	page, _ := offsetPage(all, in.Offset, in.Limit)
	out.Items = append(out.Items, page...)
	return out, nil
}

// tagMatches: "" matches every tag; otherwise the pattern is a whole-tag glob (globMatch), case-insensitive.
func tagMatches(pattern, tag string) bool {
	return pattern == "" || globMatch(strings.ToLower(pattern), strings.ToLower(tag))
}

// editTags returns the tag list after removing then adding, keeping order.
func editTags(tags, add, remove []string) []string {
	out := []string{}
	for _, t := range tags {
		if !slices.Contains(remove, t) {
			out = append(out, t)
		}
	}
	for _, t := range add {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// SetMemoryTags adds and removes tags on one active memory. Human only.
func (s *Service) SetMemoryTags(ctx context.Context, token, id string, in MemoryTagsInput) (Record, error) {
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || !validRunLabel(id, 256) || containsSecret(id) || in.ExpectedRevision < 0 ||
		len(in.Add)+len(in.Remove) == 0 || !validTagInput(in.Add) || !validTagEdit(in.Remove) {
		return Record{}, ErrInvalid
	}
	return s.editLiveMemory(ctx, token, id, in.Scope, in.ExpectedRevision, "set_tags:memories", func(r *Record) error {
		next := editTags(r.Tags, in.Add, in.Remove)
		if slices.Equal(next, r.Tags) {
			return errMemoryUnchanged
		}
		if !validTags(next) {
			return ErrInvalid
		}
		r.Tags = next
		return nil
	})
}

// RenameTag replaces one tag on up to tagRenameBatch active memories of the
// scope per call; More reports that another call can make progress.
func (s *Service) RenameTag(ctx context.Context, token string, in TagRenameInput) (TagRenameResult, error) {
	var out TagRenameResult
	if !validScope(in.Scope) || in.From == in.To || !validTagEdit([]string{in.From}) || !validTagInput([]string{in.To}) {
		return out, ErrInvalid
	}
	if _, err := s.opsAuthorize(ctx, token, "transition:memories", in.Scope, "", roleHuman); err != nil {
		return out, err
	}
	st, err := s.opsStore()
	if err != nil {
		return out, err
	}
	rows, err := st.OpsRecords(ctx, opsRecordQuery{Scope: in.Scope, Collection: colMemories, Statuses: []string{"active"}, Tag: in.From, Limit: tagRenameBatch + 1})
	if err != nil {
		return out, err
	}
	more := len(rows) > tagRenameBatch
	rows = rows[:min(len(rows), tagRenameBatch)]
	for _, r := range rows {
		changed := false
		// Each memory is its own audited revision; a memory changed meanwhile is skipped, not overwritten.
		_, err := s.editLiveMemory(ctx, token, r.ID, in.Scope, r.Revision, "rename_tag:memories", func(m *Record) error {
			if changed = slices.Contains(m.Tags, in.From); !changed {
				return errMemoryUnchanged
			}
			m.Tags = renamedTags(m.Tags, in.From, in.To)
			return nil
		})
		switch {
		case err == nil && changed:
			out.Renamed++
		case err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound):
			out.Skipped++
		default:
			return out, err
		}
	}
	// shortcut: rows that stay unrenamable (held conflict, not yet effective) keep the head of the
	// newest-first query; a full batch without progress ends with More=false and Skipped tells the
	// operator. Upgrade to a returned keyset cursor if such rows ever fill a batch.
	out.More = more && out.Renamed > 0
	return out, nil
}

// renamedTags replaces from with to, keeping order and dropping a duplicate the rename creates.
func renamedTags(tags []string, from, to string) []string {
	next := []string{}
	for _, t := range tags {
		if t == from {
			t = to
		}
		if !slices.Contains(next, t) {
			next = append(next, t)
		}
	}
	return next
}
