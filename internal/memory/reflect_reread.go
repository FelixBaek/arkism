package memory

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Rereading the conversation: a reviewed memory is a summary, so the exact date, number or
// name a question asks for may only be in the turns it was written from. Before the answer,
// a (cheaper) model copies the lines that help from those conversation sources, and only
// lines that really are in the sources reach the answer prompt.

const (
	reflectRereadBatches   = 10
	reflectRereadLookups   = 100
	reflectRereadLines     = 25
	reflectRereadMaxTokens = 4000  // a model that thinks first spends this too: 1500 cut its answer off before a line
	reflectRereadRunes     = 80000 // stays under reflectMaxPromptTokens with the question and system text
	reflectRereadTimeout   = 45 * time.Second
	// What a request with a deadline keeps for the rest of the answer: the prompt only has to be
	// stored, a model answer has to be asked for.
	reflectPromptReserve   = 5 * time.Second
	reflectAnswerReserve   = 15 * time.Second
	captureTurnsRef        = "capture-turns:" // source_ref the capture gives a batch's turns
	reflectRereadOperation = "source_reread"  // its own daily cap: a question spends one reflect call
	// reflectRereadDailyCallLimit is that cap per project unless Service.ReflectRereadDailyLimit sets another.
	reflectRereadDailyCallLimit = 1000
	reflectRereadSystem         = "You are given a question and transcripts of past conversations (each line: [date] Speaker: text).\n" +
		"Copy every transcript line that helps answer the question, exactly as written, one per line, keeping the [date] and speaker.\n" +
		"Include lines that carry a needed date, number, name or reason, and the line just before one when it is needed to understand it.\n" +
		"At most 25 lines. If no line helps, output exactly NONE. No commentary."
)

// verbatimLines keeps the output lines that are, after trimming, exactly one of the allowed
// lines: a line the model reworded or made up is dropped. Each line once, at most limit.
func verbatimLines(output string, allowed map[string]bool, limit int) []string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if len(kept) < limit && allowed[line] {
			kept = append(kept, line)
			allowed[line] = false
		}
	}
	return kept
}

// reflectReread fills run.sourceLines. It never fails the answer: off, nothing to read,
// a refused transfer, a model error or a timeout all leave the prompt as it was.
// A source changed after admission is an error, not an optional lookup failure. A request
// with a deadline gives the reread only the time left before reserve, so a slow reread is
// dropped and the answer still returns in time.
func (s *Service) reflectReread(ctx context.Context, token string, run *reflectRun, reserve time.Duration) (ProviderUsage, error) {
	if s.ReflectReread == nil || !s.probeAllowed(ctx, token, "read:sources", run.in.Scope) {
		return ProviderUsage{}, nil
	}
	budget := reflectRereadTimeout
	if d, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(d)-reserve)
	}
	if budget <= 0 {
		return ProviderUsage{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	previous := maps.Clone(run.sourceRecords)
	user, allowed := s.rereadTranscripts(ctx, token, run)
	if ctx.Err() != nil {
		run.sourceRecords = previous
		run.noteSourceFailure("source_reread", "lookup_unavailable")
		return ProviderUsage{}, nil
	}
	if len(allowed) == 0 {
		return ProviderUsage{}, nil
	}
	if err := s.verifyReflectSources(ctx, token, run); err != nil {
		if ctx.Err() != nil {
			run.sourceRecords = previous
			run.noteSourceFailure("source_reread", "lookup_unavailable")
			return ProviderUsage{}, nil
		}
		return ProviderUsage{}, err
	}
	text, usage, err := s.reflectCall(ctx, token, run.in, s.ReflectReread, reflectRereadOperation, reflectRereadSystem, user, reflectRereadMaxTokens)
	if err == nil {
		run.sourceLines = verbatimLines(text, allowed, reflectRereadLines)
	} else {
		run.noteSourceFailure("source_reread", "unavailable")
	}
	return usage, nil
}

// rereadRefs names the capture batches to reread: those the retrieved memories came from, then
// the other batches of their sessions, then those of the conversation turns the question matches best.
// shortcut: turns carry no session, so a session's other batches are found through its memories' tags (100 per session); store the session on the turns once a batch without a memory must be read.
func (s *Service) rereadRefs(ctx context.Context, run *reflectRun) []string {
	var refs, sessions []string
	events := func(tags []string) {
		for _, t := range tags {
			if event, ok := strings.CutPrefix(t, "event_id:"); ok && !slices.Contains(refs, captureTurnsRef+event) {
				refs = append(refs, captureTurnsRef+event)
			}
		}
	}
	for _, h := range run.hits {
		events(h.Tags)
		for _, t := range h.Tags {
			if strings.HasPrefix(t, "session:") && !slices.Contains(sessions, t) {
				sessions = append(sessions, t)
			}
		}
	}
	for _, session := range sessions {
		siblings, _ := s.Repo.ListMemories(ctx, MemoryListInput{Scope: run.in.Scope, Tag: session, Limit: 100})
		for _, m := range siblings {
			events(m.Tags)
		}
	}
	rows, _ := s.sideLane(ctx, "sources", SearchInput{Scope: run.in.Scope, Query: run.in.Query, space: run.in.space}, SearchCriteria{Kinds: []string{conversationSourceKind}})
	for _, row := range rows[:min(len(rows), conversationLaneHits)] {
		if r, err := s.Repo.Get(ctx, "sources", run.in.Scope, row.ID); err == nil && strings.HasPrefix(r.SourceRef, captureTurnsRef) && !slices.Contains(refs, r.SourceRef) {
			refs = append(refs, r.SourceRef)
		}
	}
	return refs
}

// rereadTranscripts is the reread prompt and the set of lines it holds.
func (s *Service) rereadTranscripts(ctx context.Context, token string, run *reflectRun) (string, map[string]bool) {
	allowed := map[string]bool{}
	var b strings.Builder
	b.WriteString("Question: " + run.in.Query)
	n := 0
	// A batch with no line the caller may read takes no place: the next one is read instead.
	// shortcut: at most reflectRereadLookups batches are looked up, raise it if sessions outgrow it.
	refs := s.rereadRefs(ctx, run)
	for _, ref := range refs[:min(len(refs), reflectRereadLookups)] {
		if n == reflectRereadBatches {
			break
		}
		lines, records := s.rereadBatch(ctx, token, run, ref)
		block := strings.Join(lines, "\n")
		if len(lines) == 0 || utf8.RuneCountInString(b.String())+utf8.RuneCountInString(block) > reflectRereadRunes {
			continue
		}
		n++
		run.keepSources(records, true)
		b.WriteString("\n\n--- conversation " + strconv.Itoa(n) + " ---\n" + block)
		for _, line := range lines {
			allowed[line] = true
		}
	}
	return b.String(), allowed
}

// rereadBatch is one capture batch's lines in order.
func (s *Service) rereadBatch(ctx context.Context, token string, run *reflectRun, ref string) ([]string, []Record) {
	rows, err := s.Repo.ListSources(ctx, SourceListInput{Scope: run.in.Scope, Kind: conversationSourceKind, SourceRef: ref, Limit: 100})
	if err != nil {
		run.noteSourceFailure("source_reread", "lookup_unavailable")
		return nil, nil
	}
	return s.turnLines(ctx, token, run, rows, true)
}

// turnLines is the conversation turns' lines in conversation order, from the turns the caller
// may read and the model may be sent (the conversation lane's own checks, recall_conversation.go).
func (s *Service) turnLines(ctx context.Context, token string, run *reflectRun, rows []Record, transfer bool) ([]string, []Record) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].ConversationOrder, rows[j].ConversationOrder
		if a == nil || b == nil {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return a.Index < b.Index
	})
	var lines []string
	var records []Record
	for _, snapshot := range rows {
		r, err := s.Repo.Get(ctx, "sources", run.in.Scope, snapshot.ID)
		if err != nil || r.Revision != snapshot.Revision || !s.reflectSourceReadable(ctx, token, run.in, r, transfer) {
			continue
		}
		start := len(lines)
		for _, line := range strings.Split(redactSessionPrivate(r.Text), "\n") {
			if line = strings.TrimSpace(line); line != "" && !containsSecret(line) {
				lines = append(lines, line)
			}
		}
		if len(lines) > start {
			records = append(records, r)
		}
	}
	return lines, records
}

// Passages: the conversation turns a question matches best go into the answer prompt as they
// were said, next to the retrieved memories and without taking a place from them. No model is
// called: the turns are found by the recall's own lanes (keyword, and vector when the first
// search could embed the question).

const (
	// ReflectPassagesMax bounds Service.ReflectPassages.
	ReflectPassagesMax = 50
	// reflectPassageRunes bounds the passages' text, so the prompt stays under reflectMaxPromptTokens.
	reflectPassageRunes = 40000
)

// reflectPassages fills run.passages with up to Service.ReflectPassages matched turns, each with
// the turn before and after it in its batch. Like the reread it never fails the answer.
func (s *Service) reflectPassages(ctx context.Context, token string, run *reflectRun, degraded []string) {
	if s.ReflectPassages <= 0 || !s.probeAllowed(ctx, token, "read:sources", run.in.Scope) {
		return
	}
	reserve := reflectAnswerReserve
	if run.in.PromptOnly {
		reserve = reflectPromptReserve
	}
	budget := 10 * time.Second
	if d, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(d)-reserve)
	}
	if budget <= 0 {
		run.noteSourceFailure("conversation_passages", "deadline")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	in := reflectSearch(run.in, run.in.Query)
	criteria := SearchCriteria{Kinds: []string{conversationSourceKind}, Filters: in.Filters}
	// The question is embedded again only when the first search could embed it.
	vector := !slices.Contains(degraded, "memory_vector_disabled") && !slices.Contains(degraded, "memory_vector_unavailable")
	if vector && s.Embedder != nil && safeTransfer(in.Query) {
		if batch, callErr, err := s.meteredEmbed(ctx, in.Scope, "embedding_query", []string{in.Query}, "query"); err == nil && callErr == nil {
			in.queryVector, in.querySpace = batch.Items[0].Vector, s.Embedder.Space()
		} else {
			run.noteSourceFailure("conversation_passages", "embedding_unavailable")
		}
	}
	rows, err := s.sideLane(ctx, "sources", in, criteria)
	if err != nil {
		run.noteSourceFailure("conversation_passages", "lookup_unavailable")
		return
	}
	shown, spare := map[string]bool{}, reflectPassageRunes
	for _, row := range rows {
		if len(run.passages) == min(s.ReflectPassages, ReflectPassagesMax) {
			break
		}
		r, err := s.Repo.Get(ctx, "sources", in.Scope, row.ID)
		if err != nil || shown[r.ID] {
			continue
		}
		part := s.passagePart(ctx, r, shown)
		lines, records := s.turnLines(ctx, token, run, part, !run.in.PromptOnly)
		block := strings.Join(lines, "\n")
		if n := utf8.RuneCountInString(block); n > 0 && n <= spare {
			spare -= n
			run.passages = append(run.passages, block)
			run.keepSources(records, !run.in.PromptOnly)
			for _, p := range part {
				shown[p.ID] = true
			}
		}
	}
}

// passagePart is a matched turn with its neighbours in the same batch that are not shown yet.
func (s *Service) passagePart(ctx context.Context, r Record, shown map[string]bool) []Record {
	part := []Record{r}
	store, ok := s.Repo.(conversationNeighborStore)
	if !ok {
		return part
	}
	neighbors, err := store.ConversationNeighbors(ctx, r)
	if err != nil || len(neighbors) > 2 {
		return part
	}
	for _, other := range neighbors {
		if other.ID != r.ID && !shown[other.ID] && other.Scope == r.Scope && other.SourceRef == r.SourceRef && other.OriginProduct == r.OriginProduct && adjacentConversation(r, other) {
			part = append(part, other)
		}
	}
	return part
}
