package memory

import (
	"context"
	"errors"
	"slices"
	"sort"
)

// Tags are reviewer-owned labels on active memories. Editing them never
// touches text or approval; each change is one audited revision.

type TagCount struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

type TagList struct {
	Items []TagCount `json:"items"`
}

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
	More    bool `json:"more"`
}

// validTagInput is validTags plus the ingestion privacy filters.
func validTagInput(tags []string) bool {
	if !validTags(tags) {
		return false
	}
	for _, t := range tags {
		if containsSecret(t) || sessionPrivateOpenRE.MatchString(t) {
			return false
		}
	}
	return true
}

// ListTags counts tags over a scope's active memories, most used first.
func (s *Service) ListTags(ctx context.Context, token string, scope Scope) (TagList, error) {
	out := TagList{Items: []TagCount{}}
	if _, err := s.opsAuthorize(ctx, token, "read:memories", scope, "", roleHuman, roleService, roleAgent, rolePipeline); err != nil {
		return out, err
	}
	st, err := s.opsStore()
	if err != nil {
		return out, err
	}
	rows, err := st.OpsCounts(ctx, opsCountQuery{Scope: scope, Collection: colMemories, By: "tag", Statuses: []string{"active"}})
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, TagCount{Tag: opsText(r.Key), Count: r.Count})
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].Count > out.Items[j].Count })
	return out, nil
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
		len(in.Add)+len(in.Remove) == 0 || !validTagInput(in.Add) || !validTags(in.Remove) {
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
// scope per call; More reports that another call is needed.
func (s *Service) RenameTag(ctx context.Context, token string, in TagRenameInput) (TagRenameResult, error) {
	var out TagRenameResult
	if !validScope(in.Scope) || in.From == in.To || !validTags([]string{in.From}) || !validTagInput([]string{in.To}) {
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
	if len(rows) > tagRenameBatch {
		rows, out.More = rows[:tagRenameBatch], true
	}
	for _, r := range rows {
		// Each memory is its own audited revision; a memory changed meanwhile is skipped, not overwritten.
		_, err := s.editLiveMemory(ctx, token, r.ID, in.Scope, r.Revision, "rename_tag:memories", func(m *Record) error {
			if !slices.Contains(m.Tags, in.From) {
				return errMemoryUnchanged
			}
			next := []string{}
			for _, t := range m.Tags {
				if t == in.From {
					t = in.To
				}
				if !slices.Contains(next, t) {
					next = append(next, t)
				}
			}
			m.Tags = next
			return nil
		})
		if err == nil {
			out.Renamed++
		} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
			return out, err
		}
	}
	return out, nil
}
