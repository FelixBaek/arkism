package memory

// LLM-Wiki output boundary. The model answers with FILE blocks:
//
//	<<<FILE path="wiki/concept/bleed.md" mode="append">>>
//	title: Bleed
//	type: concept
//	---
//	body with [[links]]
//	<<<END>>>
//
// Nothing here touches a filesystem: pages live in the DB. The path is only a
// claim the model makes, checked in layers: (1) a strict whitelist pattern,
// (2) the page identity is recomputed from type+title and must equal what the
// path claims, (3) the server derives the page id from the job's scope and the
// recomputed slug, and (4) the job applies a final boundary check (planned,
// grounded in the source, not locked) before any write.

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	wikiMaxBlocks     = 20
	wikiMaxBodyBytes  = 16 << 10
	wikiMaxSlugRunes  = 80
	wikiMaxTitleRunes = 120
	wikiMaxLinks      = 50
	// wikiRewriteMaxRunes: a full LLM rewrite is only allowed for small pages;
	// larger ones fall back to append so a merge cannot silently drop text.
	wikiRewriteMaxRunes = 4000
)

var (
	wikiTypes      = []string{"entity", "concept"}
	wikiMergeModes = []string{"append", "replace-section", "rewrite"}
	// Lowercase letters (any script), digits and single dashes; no dots, no
	// slashes, nothing that could mean "parent" or "absolute".
	wikiPathPattern   = regexp.MustCompile(`^wiki/(entity|concept)/([\p{Ll}\p{Lo}\p{N}](?:[\p{Ll}\p{Lo}\p{N}-]{0,79}))\.md$`)
	wikiHeaderPattern = regexp.MustCompile(`^<<<FILE((?: [a-z]+="[^"\x00-\x1f]*")*)>>>$`)
	wikiAttrPattern   = regexp.MustCompile(` ([a-z]+)="([^"]*)"`)
	wikiLinkPattern   = regexp.MustCompile(`\[\[([^\[\]|\n]{1,120})(?:\|([^\[\]\n]{1,120}))?\]\]`)
)

// wikiBlock is one parsed FILE block before the boundary check.
type wikiBlock struct {
	Path    string
	Mode    string
	Section string
	Type    string
	Title   string
	Slug    string
	Body    string
}

// wikiRejection explains why a block from the model was not used.
type wikiRejection struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// wikiSlug is the one normalization for page identity: same title, same page.
func wikiSlug(title string) string {
	var b strings.Builder
	dash := false
	n := 0
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if n >= wikiMaxSlugRunes {
			break
		}
		switch {
		case unicode.In(r, unicode.Ll, unicode.Lo, unicode.N):
			b.WriteRune(r)
			dash = false
			n++
		case r == ' ' || r == '-' || r == '_' || r == '\t':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
				n++
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func validWikiTitle(title string) bool {
	return validRunLabel(title, 512) && utf8.RuneCountInString(title) <= wikiMaxTitleRunes && !strings.ContainsAny(title, "<>[]|\"`") && wikiSlug(title) != ""
}

// parseWikiFiles splits model output into blocks. Text outside blocks is
// ignored. Every structural problem rejects that block only.
func parseWikiFiles(out string) ([]wikiBlock, []wikiRejection) {
	var blocks []wikiBlock
	var rejected []wikiRejection
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "<<<FILE") {
			continue
		}
		m := wikiHeaderPattern.FindStringSubmatch(line)
		end := -1
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if t == "<<<END>>>" {
				end = j
				break
			}
			if strings.HasPrefix(t, "<<<FILE") {
				break // nested or unterminated block
			}
		}
		if end < 0 {
			rejected = append(rejected, wikiRejection{Path: wikiClip(line), Reason: "unterminated_block"})
			continue
		}
		body := strings.Join(lines[i+1:end], "\n")
		i = end
		if m == nil {
			rejected = append(rejected, wikiRejection{Path: wikiClip(line), Reason: "bad_header"})
			continue
		}
		attrs := map[string]string{}
		bad := false
		for _, a := range wikiAttrPattern.FindAllStringSubmatch(m[1], -1) {
			if _, dup := attrs[a[1]]; dup || a[1] != "path" && a[1] != "mode" && a[1] != "section" {
				bad = true
			}
			attrs[a[1]] = a[2]
		}
		path := attrs["path"]
		if bad {
			rejected = append(rejected, wikiRejection{Path: wikiClip(path), Reason: "unknown_attribute"})
			continue
		}
		if len(blocks) >= wikiMaxBlocks {
			rejected = append(rejected, wikiRejection{Path: wikiClip(path), Reason: "too_many_blocks"})
			continue
		}
		b, reason := checkWikiBlock(path, attrs["mode"], attrs["section"], body)
		if reason != "" {
			rejected = append(rejected, wikiRejection{Path: wikiClip(path), Reason: reason})
			continue
		}
		blocks = append(blocks, b)
	}
	return blocks, rejected
}

// checkWikiBlock is layers 1 and 2: whitelist the claimed path, then recompute
// the identity from the block's own type and title and require they agree.
func checkWikiBlock(path, mode, section, raw string) (wikiBlock, string) {
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\%\x00") || path != strings.TrimSpace(path) {
		return wikiBlock{}, "path_outside_wiki"
	}
	m := wikiPathPattern.FindStringSubmatch(path)
	if m == nil {
		return wikiBlock{}, "path_outside_wiki"
	}
	if mode == "" {
		mode = "append"
	}
	if !slices.Contains(wikiMergeModes, mode) {
		return wikiBlock{}, "bad_mode"
	}
	if mode == "replace-section" && !validWikiTitle(section) || mode != "replace-section" && section != "" {
		return wikiBlock{}, "bad_section"
	}
	head, body, ok := strings.Cut(raw, "\n---\n")
	if !ok {
		return wikiBlock{}, "missing_header"
	}
	fields := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(head), "\n") {
		k, v, ok := strings.Cut(l, ":")
		k = strings.TrimSpace(k)
		if !ok || k != "title" && k != "type" {
			// id, scope, locked, status, sources ... are server-owned.
			return wikiBlock{}, "forbidden_field"
		}
		if _, dup := fields[k]; dup {
			return wikiBlock{}, "forbidden_field"
		}
		fields[k] = strings.TrimSpace(v)
	}
	title, typ := fields["title"], fields["type"]
	if !validWikiTitle(title) || !slices.Contains(wikiTypes, typ) {
		return wikiBlock{}, "bad_title_or_type"
	}
	slug := wikiSlug(title)
	if typ != m[1] || slug != m[2] {
		return wikiBlock{}, "path_identity_mismatch"
	}
	body = strings.TrimSpace(body)
	if body == "" || len(body) > wikiMaxBodyBytes || !utf8.ValidString(body) || strings.ContainsRune(body, 0) || strings.Contains(body, "<<<") {
		return wikiBlock{}, "bad_body"
	}
	if containsSecret(body) || containsSecret(title) {
		return wikiBlock{}, "secret_detected"
	}
	return wikiBlock{Path: path, Mode: mode, Section: section, Type: typ, Title: title, Slug: slug, Body: body}, ""
}

// wikiLinks returns the distinct target slugs of [[links]] in text, minus self.
func wikiLinks(text, self string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range wikiLinkPattern.FindAllStringSubmatch(text, -1) {
		slug := wikiSlug(m[1])
		if slug == "" || slug == self || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
		if len(out) >= wikiMaxLinks {
			break
		}
	}
	return out
}

// wikiUnlink turns [[target]] / [[target|label]] into plain text for one slug.
func wikiUnlink(text, slug string) string {
	return wikiLinkPattern.ReplaceAllStringFunc(text, func(s string) string {
		m := wikiLinkPattern.FindStringSubmatch(s)
		if wikiSlug(m[1]) != slug {
			return s
		}
		if m[2] != "" {
			return m[2]
		}
		return m[1]
	})
}

// wikiMerge applies one merge mode to an existing body. It returns the new
// body, the mode actually used, and whether anything changed.
func wikiMerge(old, add, mode, section string) (string, string, bool) {
	old = strings.TrimSpace(old)
	add = strings.TrimSpace(add)
	if old == "" {
		return add, mode, add != ""
	}
	// Rule 1 (no LLM needed): the new text is already there.
	if strings.Contains(old, add) {
		return old, mode, false
	}
	switch mode {
	case "rewrite":
		if utf8.RuneCountInString(old) <= wikiRewriteMaxRunes {
			return add, mode, add != old
		}
		mode = "append"
	case "replace-section":
		lines := strings.Split(old, "\n")
		start, stop := -1, len(lines)
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if start < 0 && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(t, "##")), section) && strings.HasPrefix(t, "## ") {
				start = i
				continue
			}
			if start >= 0 && (strings.HasPrefix(t, "## ") || strings.HasPrefix(t, "# ")) {
				stop = i
				break
			}
		}
		if start < 0 {
			return old + "\n\n## " + section + "\n\n" + add, mode, true
		}
		next := append(append(append([]string{}, lines[:start+1]...), "", add, ""), lines[stop:]...)
		merged := strings.TrimSpace(strings.Join(next, "\n"))
		return merged, mode, merged != old
	}
	return old + "\n\n" + add, "append", true
}

func wikiClip(s string) string {
	if utf8.RuneCountInString(s) <= 160 {
		return strings.ToValidUTF8(s, "?")
	}
	return string([]rune(s)[:160])
}
