package memory

import (
	"regexp"
	"strings"
)

// aggregateRe matches a question that asks for every instance of something: a count ("how many",
// "how often", "몇 번"), a total ("in total", "모두"), a list ("list the ...", "목록", "어디어디"), or a
// plural or perfective wh-question ("which cities has ...", "what books has ... read", "where has
// ... camped"). Such an answer is
// spread over several turns, so a few top rows cannot hold it. Only the question's shape is read.
var aggregateRe = regexp.MustCompile(`(?i)` +
	`\b(?:how\s+(?:many|often)|in\s+total|altogether|all\s+(?:the|of|my|our)|every\s+time|everything|each\s+of|list\s+(?:of|all|the|every|my|our)|name\s+(?:all|every))\b` +
	`|^\s*list\b` +
	`|^\s*(?:what|which|where)\s+(?:\S+\s+)?\pL+s\s+(?:has|have|does|do|did|are|were)\b` +
	`|^\s*(?:what|which)\s+(?:kinds?|types?|sorts?)\s+of\b` +
	`|^\s*(?:what|where)\s+(?:has|have)\b` +
	`|^\s*what\s+are\b` +
	`|몇\s*(?:번|개|명|곳|권|가지|차례|군데|건|회|편)|모두|전부|모든|전체|목록|리스트|얼마나\s*자주` +
	`|어디\s*어디|누구\s*누구|뭐\s*뭐|무엇\s*무엇|어떤\s*\pL+들|\pL들(?:은|이|을|에는)?\s*(?:뭐|무엇|어디|누구)`)

func aggregateIntent(query string) bool {
	return aggregateRe.MatchString(strings.Join(strings.Fields(query), " "))
}

// conversationLaneLimit is how many conversation batches a recall shows: twice the lane for a
// question whose answer is spread over several turns.
func conversationLaneLimit(query string) int {
	if aggregateIntent(query) {
		return 2 * conversationLaneHits
	}
	return conversationLaneHits
}

// aggregateItemLimit is how many memories a recall returns: with the caller's aggregate_items
// opt-in, twice the limit (at most 50) for a question whose answer is spread over several turns;
// otherwise exactly the limit. max_chars still bounds the text.
func aggregateItemLimit(in SearchInput) int {
	if in.AggregateItems && aggregateIntent(in.Query) {
		return min(2*in.Limit, 50)
	}
	return in.Limit
}
