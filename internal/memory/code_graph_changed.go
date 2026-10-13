package memory

import (
	"context"
	"slices"
)

// 바뀐 함수의 영향 (code review): the functions and methods whose line range
// overlaps the changed lines of a pull request, each with what may break
// (callers, breadth first). The caller reads the diff; the server only matches
// line ranges against the indexed symbols, so the answer is about the indexed
// commit (CodeChangedAnswer.Commit), which can be older than the change.

const (
	codeChangedMaxRanges = 1000
	codeChangedDefLimit  = 30
	codeChangedMaxLimit  = 100
	codeChangedDefImpact = 30
	codeChangedDefDepth  = 2
	codeChangedLineMax   = 10_000_000
)

var codeChangedKinds = []string{"func", "method"}

// CodeLineRange is one changed span of a file, 1-based and inclusive.
type CodeLineRange struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type CodeChangedQuery struct {
	Scope       Scope           `json:"scope"`
	Repo        string          `json:"repo"`
	Ranges      []CodeLineRange `json:"ranges"`
	Depth       int             `json:"depth,omitempty"`        // impact depth, default 2, at most 8
	Limit       int             `json:"limit,omitempty"`        // changed symbols, default 30, at most 100
	ImpactLimit int             `json:"impact_limit,omitempty"` // impact items per symbol, default 30, at most 200
}

// CodeChangedSymbol is one changed function with its impact.
type CodeChangedSymbol struct {
	Symbol      CodeHit   `json:"symbol"`
	EndLine     int       `json:"end_line"`
	Impact      []CodeHit `json:"impact"`
	ImpactTotal int       `json:"impact_total"`
	Truncated   bool      `json:"truncated"`
	Reason      string    `json:"reason,omitempty"` // limit | node_cap
}

type CodeChangedAnswer struct {
	Op        string              `json:"op"`
	Repo      string              `json:"repo"`
	Commit    string              `json:"commit"` // the indexed commit the answer is about
	Depth     int                 `json:"depth"`
	Items     []CodeChangedSymbol `json:"items"`
	Truncated bool                `json:"truncated"`
	Reason    string              `json:"reason,omitempty"` // limit | depth_cap
	Authority string              `json:"authority"`
	Note      string              `json:"note,omitempty"`
}

func normalizeCodeChanged(in *CodeChangedQuery) error {
	if !validScope(in.Scope) || !validCodeRepo(in.Repo) || len(in.Ranges) == 0 || len(in.Ranges) > codeChangedMaxRanges ||
		in.Depth < 0 || in.Limit < 0 || in.Limit > codeChangedMaxLimit || in.ImpactLimit < 0 || in.ImpactLimit > codeQueryMaxLimit {
		return ErrInvalid
	}
	for _, r := range in.Ranges {
		if !validCodePath(r.Path) || r.StartLine < 1 || r.EndLine < r.StartLine || r.EndLine > codeChangedLineMax {
			return ErrInvalid
		}
	}
	if in.Limit == 0 {
		in.Limit = codeChangedDefLimit
	}
	if in.ImpactLimit == 0 {
		in.ImpactLimit = codeChangedDefImpact
	}
	return nil
}

// CodeChanged finds the functions and methods the given line ranges touch and
// the impact of each.
func (s *Service) CodeChanged(ctx context.Context, token string, in CodeChangedQuery) (CodeChangedAnswer, error) {
	if err := normalizeCodeChanged(&in); err != nil {
		return CodeChangedAnswer{}, err
	}
	store, _, err := s.codeGraph(ctx, token, codeGraphRead, in.Scope)
	if err != nil {
		return CodeChangedAnswer{}, err
	}
	in.Scope = projectScope(in.Scope)
	repos, err := store.CodeRepos(ctx, in.Scope)
	if err != nil {
		return CodeChangedAnswer{}, err
	}
	at := slices.IndexFunc(repos, func(r CodeRepo) bool { return r.Repo == in.Repo })
	if at < 0 {
		return CodeChangedAnswer{}, ErrNotFound
	}
	out := CodeChangedAnswer{Op: "changed", Repo: in.Repo, Commit: repos[at].Commit, Items: []CodeChangedSymbol{}, Authority: AuthorityDataOnly, Note: codeGraphNote}
	out.Depth = in.Depth
	if out.Depth == 0 {
		out.Depth = codeChangedDefDepth
	}
	if out.Depth > codeImpactMaxDepth {
		out.Depth, out.Truncated, out.Reason = codeImpactMaxDepth, true, "depth_cap"
	}
	syms, err := store.CodeSymbolsInRanges(ctx, in.Scope, in.Repo, in.Ranges, codeChangedKinds, in.Limit+1)
	if err != nil {
		return CodeChangedAnswer{}, err
	}
	if len(syms) > in.Limit {
		syms, out.Truncated, out.Reason = syms[:in.Limit], true, "limit"
	}
	for _, sym := range syms {
		hits, nodeCap, err := codeImpactWalk(ctx, store, in.Scope, in.Repo, sym.ID, out.Depth)
		if err != nil {
			return CodeChangedAnswer{}, err
		}
		item := CodeChangedSymbol{Symbol: codeHitOf(in.Repo, sym), EndLine: sym.EndLine, ImpactTotal: len(hits), Impact: hits}
		if item.Impact == nil {
			item.Impact = []CodeHit{}
		}
		if len(hits) > in.ImpactLimit {
			item.Impact, item.Truncated, item.Reason = hits[:in.ImpactLimit], true, "limit"
		}
		if nodeCap {
			item.Truncated, item.Reason = true, "node_cap"
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
