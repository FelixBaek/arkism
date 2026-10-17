package capture

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// serverKoreanID is the Korean mobile and resident-number branches of internal/memory's sensitivePattern.
var serverKoreanID = regexp.MustCompile(`(?i)\b01[016789][- ]?\d{3,4}[- ]?\d{4}\b|\b\d{6}-[1-4]\d{6}\b`)

// bigTranscriptText is a long assistant turn the way Claude Code archives it: Korean prose, code, quotes,
// tabs and a stray control byte, so JSON escaping grows some pieces.
func bigTranscriptText(n int) string {
	line := "배포 순서를 정리했습니다.\n\t- `go test ./internal/host/capture/` 통과 \"ok\" \x01 경로 C:\\srv\\arkism\n"
	text := strings.Repeat(line, n/len(line)+1)
	for !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

func TestChunkRecordsSplitsOversizedRecordIntoFittingParts(t *testing.T) {
	id := "archive-" + strings.Repeat("3f", 32)
	text := bigTranscriptText(100000)
	small := map[string]any{"source_id": "archive-" + strings.Repeat("a1", 32), "revision": 1, "role": "user", "text": "이 세션 요약해줘"}
	big := map[string]any{"source_id": id, "revision": 1, "role": "assistant", "text": text}
	chunks, err := chunkRecords([]any{small, big}, 32000)
	if err != nil {
		t.Fatal(err)
	}
	if !eq(chunks[0][0], small) {
		t.Fatal("a fitting record must stay whole", chunks[0][0])
	}
	var joined strings.Builder
	n := 0
	for _, chunk := range chunks {
		size := 0
		for _, row := range chunk {
			size += len(dumps(row))
			if row.(map[string]any)["split_of"] == nil {
				continue
			}
			n++
			if autosaveGet(row, "split_of") != id || autosaveGet(row, "source_id") != id+"-p"+str(n) || autosaveGet(row, "role") != "assistant" || autosaveGet(row, "revision") != 1 {
				t.Fatal(row)
			}
			joined.WriteString(str(autosaveGet(row, "text")))
		}
		if size > 32000 {
			t.Fatal("chunk over budget:", size)
		}
	}
	if n < 4 || joined.String() != text {
		t.Fatal("parts must cover the whole text in order:", n)
	}
	// Escape-heavy text (every byte grows to 2 or 6 in JSON) still fits.
	escaped := map[string]any{"source_id": id, "revision": 2, "role": "tool", "text": strings.Repeat("\"\x02\\", 20000)}
	chunks, err = chunkRecords([]any{escaped}, 32000)
	if err != nil || len(chunks) < 3 {
		t.Fatal(err, len(chunks))
	}
	for _, chunk := range chunks {
		if len(dumps(chunk[0])) > 32000 {
			t.Fatal(len(dumps(chunk[0])))
		}
	}
	// A budget too small to carry any text keeps the old terminal wait.
	if _, err := chunkRecords([]any{big}, 900); !pending(err) || errorCode(err) != "oversized_record_requires_artifact_reference" {
		t.Fatal(err)
	}
}

// sessionEcho is a model that cites the first record of whatever window it is sent.
func sessionEcho(t *testing.T, sent *[]string) func(string, string, string, time.Duration, string, string) (any, error) {
	return func(_, content, _ string, _ time.Duration, _, _ string) (any, error) {
		*sent = append(*sent, content)
		var in struct{ Data map[string]any }
		if err := json.Unmarshal([]byte(content), &in); err != nil {
			t.Fatal(err)
		}
		records := in.Data["records"].([]any)
		ids := []any{}
		for _, r := range records {
			ids = append(ids, autosaveGet(r, "source_id"))
		}
		first := records[0].(map[string]any)
		ref := map[string]any{"source_id": first["source_id"], "revision": 1, "quote": head(str(first["text"]), 12)}
		number := func(k string) int64 { return int64(in.Data[k].(float64)) }
		return map[string]any{"session_state": map[string]any{"goal": "배포 정리", "constraints": []any{}, "decisions": []any{}, "completed": []any{},
			"next_steps": []any{}, "uncertainties": []any{}, "artifacts": []any{}, "checks": []any{}, "source_refs": []any{ref}},
			"memory_candidates": []any{},
			"coverage": map[string]any{"start": number("start"), "end": number("end"), "expected_revision": number("expected_revision"),
				"schema_version": sessionSchemaVersion, "prompt_version": sessionPromptVersion, "source_ids": ids},
			"unresolved_refs": []any{}}, nil
	}
}

func TestSessionProcessCarriesOversizedRecordAcrossWindows(t *testing.T) {
	capFake(t)
	fakeUsage(t, nil, errors.New("no reading"))
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	id := "archive-" + strings.Repeat("9c", 32)
	ref := map[string]any{"archive_id": "arc-39be5193", "sha256": strings.Repeat("e", 64), "chunk_index": 0, "chunk_offset": 14363172}
	text := bigTranscriptText(90000)
	linked := sessionLinked
	t.Cleanup(func() { sessionLinked = linked })
	sessionLinked = func(map[string]any, string) ([]any, map[string][]any, error) {
		return []any{map[string]any{"source_id": id, "revision": 1, "role": "assistant", "text": text}}, map[string][]any{id: {ref}}, nil
	}
	var sent []string
	llm = sessionEcho(t, &sent)
	// The fake server keeps the checkpoint like the real one: the last save's package and archive refs.
	var checkpoint map[string]any
	authorized := 0
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		b := body.(map[string]any)
		switch {
		case strings.HasSuffix(path, "/work-checkpoints/get"):
			if checkpoint == nil {
				return nil, &host.HTTPError{Code: 404}
			}
			return checkpoint, nil
		case strings.HasSuffix(path, "/authorize-transfer"):
			authorized++
			if len(str(b["content"])) > sessionTransferMaxBytes || !eq(b["refs"], []any{ref}) {
				t.Fatal("transfer over the server limit or with foreign refs", len(str(b["content"])), b["refs"])
			}
			return map[string]any{"authorized": true, "content_hash": sha256Hex([]byte(str(b["content"])))}, nil
		case strings.HasSuffix(path, "/work-checkpoints/save"):
			revision, _ := host.Int(b["expected_revision"])
			checkpoint = map[string]any{"authority": "data_only_untrusted", "id": b["id"], "revision": revision + 1, "package": b["package"],
				"archive_messages": b["archive_messages"]}
			return map[string]any{"id": b["id"], "revision": revision + 1}, nil
		}
		t.Fatal("unexpected", path)
		return nil, nil
	}
	job := mustLoad(t, `{"users": [{"text": "배포 정리해줘", "at": "2026-10-17T02:00:00Z"}], "type": "task_finished", "session_id": "39be5193-e175-423e-80b1-1698a971bc92"}`)
	if err := sessionProcess(job, func() error { return nil }, "claude_code"); err != nil {
		t.Fatal(err)
	}
	parts, _ := chunkRecords([]any{map[string]any{"source_id": id, "revision": 1, "role": "assistant", "text": text}}, 32000)
	if len(parts) < 3 || len(sent) != len(parts) || authorized != len(parts) || job["session_cursor"] != len(parts) || job["session_processor_complete"] != true {
		t.Fatal("every part must reach the model once; later parts share the saved refs:", len(parts), len(sent), authorized, job)
	}
	if !strings.Contains(sent[1], `"previous_state": {`) || !strings.Contains(sent[1], id+`-p2`) {
		t.Fatal("the second part must go out with the first part's state")
	}
}

func TestSessionPrepareTrimsPreviousStateToTransferLimit(t *testing.T) {
	request, output := sessionFixture(t)
	capFake(t)
	records := request["records"].([]any)
	records[0].(map[string]any)["text"] = "한국어로 답해 " + strings.Repeat("세션 로그 ", 2600) // about 32 KB like a full window
	output["coverage"].(map[string]any)["expected_revision"] = 3
	llm = func(string, string, string, time.Duration, string, string) (any, error) { return output, nil }
	long := func(label string) []any {
		out := []any{}
		for i := range 40 {
			out = append(out, label+" "+str(i)+" "+strings.Repeat("결정 근거 ", 60))
		}
		return out
	}
	previous := map[string]any{"title": "배포 정리", "goal": "배포 정리", "decisions": long("decision"), "completed": long("done"),
		"next_steps": []any{"PR 병합 후 운영 배포"}, "source_refs": []any{}}
	var authorized string
	if _, err := sessionPrepare(map[string]any{}, "s", records, 0, 1, 3, previous, nil, "", func(c string) error { authorized = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(authorized) == 0 || len(authorized) > sessionTransferMaxBytes || !strings.Contains(authorized, "PR 병합 후 운영 배포") ||
		!strings.Contains(authorized, "decision 0 ") || strings.Contains(authorized, "decision 39 ") {
		t.Fatal("previous_state must lose its last entries only until it fits:", len(authorized))
	}
	if len(previous["decisions"].([]any)) != 40 || len(previous["completed"].([]any)) != 40 {
		t.Fatal("caller's checkpoint mutated")
	}
	// State with nothing left to trim is still refused before the transfer.
	previous = map[string]any{"goal": strings.Repeat("목표 ", 8000)}
	_, err := sessionPrepare(map[string]any{}, "s", records, 0, 1, 3, previous, nil, "", func(string) error {
		t.Fatal("oversized content reached authorize-transfer")
		return nil
	})
	if !pending(err) || errorCode(err) != "input_chunk_required" {
		t.Fatal(err)
	}
}

func TestSessionProcessFinishesMetadataOnlyWindow(t *testing.T) {
	capFake(t)
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	linked := sessionLinked
	t.Cleanup(func() { sessionLinked = linked })
	sessionLinked = func(map[string]any, string) ([]any, map[string][]any, error) { return []any{}, map[string][]any{}, nil }
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		t.Fatal("an empty window reached the model")
		return nil, nil
	}
	capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
		t.Fatal("an empty window reached the server:", path)
		return nil, nil
	}
	job := mustLoad(t, `{"users": [], "type": "session_end", "session_id": "9f036994-82a7-4ad6-9659-3c65e23ad18f",
		"archive_context": {"path": "x"}, "source_start": 900099, "source_end": 902576}`)
	if err := sessionProcess(job, func() error { return nil }, "claude_code"); err != nil || job["session_processor_complete"] != true {
		t.Fatal(err, job)
	}
}

func TestMaskSensitiveClearsKoreanIDsAndKeepsText(t *testing.T) {
	in := []any{
		map[string]any{"role": "user", "text": "연락처 010-1234-5678 로 문자 줘, 사무실은 011 234 5678"},
		host.Obj{{Key: "role", Value: "tool"}, {Key: "text", Value: `{"phone":"01098765432","rrn":"900101-1234567"}`}},
		map[string]any{"role": "assistant", "text": "번호01712345678 확인\n016-123-4567\t끝"},
		map[string]any{"role": "assistant", "text": "주문번호 2010-1234-5678, 0101234567890, 900101-5234567, v1.010.1234.5678 는 그대로"},
		map[string]any{"role": "user", "text": "메일 01012345678@atelierfelix.dev 확인"},
	}
	out := maskSensitive(in).([]any)
	content := dumps(map[string]any{"authority": "data_only_untrusted", "data": map[string]any{"records": out}})
	if m := serverKoreanID.FindString(strings.ReplaceAll(content, `\"`, `"`)); m != "" {
		t.Fatalf("server filter still matches %q in %s", m, content)
	}
	want := []string{
		"연락처 [phone] 로 문자 줘, 사무실은 [phone]",
		`{"phone":"[phone]","rrn":"[rrn]"}`,
		"번호[phone] 확인\n[phone]\t끝",
		"주문번호 2010-1234-5678, 0101234567890, 900101-5234567, v1.010.1234.5678 는 그대로",
		"메일 [email] 확인",
	}
	for i, w := range want {
		if got := autosaveGet(out[i], "text"); got != w {
			t.Fatalf("%d: %q", i, got)
		}
	}
	// The server would not match these either, so leaving them is not a bypass.
	if serverKoreanID.MatchString(want[3]) {
		t.Fatal("fixture drifted from the server pattern")
	}
	if autosaveGet(in[0], "text") != "연락처 010-1234-5678 로 문자 줘, 사무실은 011 234 5678" {
		t.Fatal("input mutated")
	}
}
