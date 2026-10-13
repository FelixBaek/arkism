package memory

// Structure-aware chunking for knowledge files. Markdown splits on heading
// boundaries first, then blank-line paragraphs, then a hard cut with a small
// overlap; every chunk carries the real heading path. Text and JSON have no
// headings, so they start at the paragraph step. The chunker version is part
// of the source version, so a document is always chunked by exactly one
// chunker and a new chunker re-chunks on the next ingest instead of mixing.
import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	knowledgeChunkMaxRunes     = 512
	knowledgeChunkOverlapRunes = 64
	// A hard cut prefers the last whitespace in this many trailing runes.
	knowledgeChunkBreakWindow  = 96
	knowledgeHeadingMaxRunes   = 120
	knowledgeSectionPathRunes  = 512
	knowledgeSectionPathJoiner = " > "
	knowledgeSectionDocument   = "document"
)

var knowledgeHeadingPattern = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*(?:[ \t]#+[ \t]*)?$`)

type knowledgeChunk struct {
	Text        string
	SectionPath string
}

type knowledgeSection struct {
	path  string
	lines []string
}

// knowledgeChunkText splits normalized file text (LF line ends, trimmed).
func knowledgeChunkText(normalized, ext string) []knowledgeChunk {
	sections := []knowledgeSection{{path: knowledgeSectionDocument, lines: strings.Split(normalized, "\n")}}
	markdown := ext == ".md"
	if markdown {
		sections = knowledgeMarkdownSections(normalized)
	}
	out := []knowledgeChunk{}
	for _, section := range sections {
		for _, text := range knowledgePackParagraphs(knowledgeParagraphs(section.lines, markdown)) {
			out = append(out, knowledgeChunk{Text: text, SectionPath: section.path})
		}
	}
	return out
}

// knowledgeMarkdownSections cuts at ATX headings outside fenced code. A
// heading with no body of its own (directly followed by another heading) is
// carried into the next section so no chunk is a bare title.
func knowledgeMarkdownSections(text string) []knowledgeSection {
	type heading struct {
		level int
		title string
	}
	stack := []heading{}
	path := func() string {
		if len(stack) == 0 {
			return knowledgeSectionDocument
		}
		titles := make([]string, len(stack))
		for i, h := range stack {
			titles[i] = h.title
		}
		return runeCutString(strings.Join(titles, knowledgeSectionPathJoiner), knowledgeSectionPathRunes)
	}
	out := []knowledgeSection{}
	current := knowledgeSection{path: knowledgeSectionDocument}
	carry := []string{}
	flush := func() {
		if knowledgeSectionHasBody(current.lines) {
			out = append(out, current)
			carry = nil
		} else {
			carry = append([]string{}, current.lines...)
		}
	}
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		if marker := knowledgeFenceMarker(line); marker != "" {
			if fence == "" {
				fence = marker
			} else if strings.HasPrefix(strings.TrimSpace(line), fence) {
				fence = ""
			}
		}
		if fence == "" {
			if m := knowledgeHeadingPattern.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[2]) != "" {
				flush()
				level := len(m[1])
				for len(stack) > 0 && stack[len(stack)-1].level >= level {
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, heading{level: level, title: runeCutString(knowledgeCleanTitle(m[2]), knowledgeHeadingMaxRunes)})
				current = knowledgeSection{path: path(), lines: append(append([]string{}, carry...), line)}
				continue
			}
		}
		current.lines = append(current.lines, line)
	}
	// A trailing bare heading stays searchable as its own small chunk.
	if strings.TrimSpace(strings.Join(current.lines, "\n")) != "" {
		out = append(out, current)
	}
	return out
}

func knowledgeSectionHasBody(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || knowledgeFenceMarker(line) == "" && knowledgeHeadingPattern.MatchString(line) {
			continue
		}
		return true
	}
	return false
}

func knowledgeCleanTitle(title string) string {
	title = strings.TrimSpace(title)
	return strings.Join(strings.FieldsFunc(title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

func knowledgeFenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return ""
	}
	for _, marker := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, marker) {
			return marker
		}
	}
	return ""
}

// knowledgeParagraphs splits on blank lines; in Markdown a fenced block stays
// one paragraph even when it contains blank lines.
func knowledgeParagraphs(lines []string, markdown bool) []string {
	out := []string{}
	current := []string{}
	fence := ""
	flush := func() {
		text := strings.Trim(strings.Join(current, "\n"), "\n")
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
		current = current[:0]
	}
	for _, line := range lines {
		if markdown {
			if marker := knowledgeFenceMarker(line); marker != "" {
				if fence == "" {
					fence = marker
				} else if strings.HasPrefix(strings.TrimSpace(line), fence) {
					fence = ""
				}
			}
		}
		if fence == "" && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return out
}

// knowledgePackParagraphs joins paragraphs up to the chunk size and hard-cuts
// any paragraph that alone exceeds it.
func knowledgePackParagraphs(paragraphs []string) []string {
	out := []string{}
	current := ""
	for _, p := range paragraphs {
		if utf8.RuneCountInString(p) > knowledgeChunkMaxRunes {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			out = append(out, knowledgeHardCut(p)...)
			continue
		}
		joined := p
		if current != "" {
			joined = current + "\n\n" + p
		}
		if utf8.RuneCountInString(joined) > knowledgeChunkMaxRunes {
			out = append(out, current)
			joined = p
		}
		current = joined
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

// knowledgeHardCut windows a long paragraph. Consecutive windows share up to
// knowledgeChunkOverlapRunes runes so a sentence on a cut is found from either
// side; a window ends at whitespace when one is close to the limit.
func knowledgeHardCut(text string) []string {
	runes := []rune(text)
	out := []string{}
	for start := 0; start < len(runes); {
		end := min(start+knowledgeChunkMaxRunes, len(runes))
		if end < len(runes) {
			for i := end - 1; i > end-knowledgeChunkBreakWindow && i > start+knowledgeChunkOverlapRunes; i-- {
				if unicode.IsSpace(runes[i]) {
					end = i + 1
					break
				}
			}
		}
		if piece := strings.TrimSpace(string(runes[start:end])); piece != "" {
			out = append(out, piece)
		}
		if end == len(runes) {
			break
		}
		start = max(end-knowledgeChunkOverlapRunes, start+1)
	}
	return out
}

func runeCutString(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
