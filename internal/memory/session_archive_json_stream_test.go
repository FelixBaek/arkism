package memory

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
)

func jsonStreamTestSource(raw []byte) *archiveJSONSource {
	return &archiveJSONSource{open: func(start, end int64) (io.Reader, error) {
		if end < 0 {
			end = int64(len(raw))
		}
		return io.NewSectionReader(bytes.NewReader(raw), start, end-start), nil
	}}
}
func TestArchiveStreamingJSONMatchesSafeParser(t *testing.T) {
	cases := []string{
		`{"role":"user","content":"한국어 😀 \\ \" \n\t\b\f\r \uD83D\uDE00 \uD800x\uDC00"}`,
		`{"role":"user","content":[{"type":"text","text":"첫"},null,{"type":"thinking","text":"숨김"},{"type":"text"},{"type":"output_text","text":"끝"}]}`,
		`{"role":"user","content":"노출","role":"tool"}`,
		`{"role":"user","content":"노출","channel":"analysis"}`,
		`{"channel":"analysis","payload":{"role":"assistant","content":"숨김"}}`,
		`{"table":"messages","record":{"data":"{\"role\":\"assistant\",\"content\":\"겹친 원문\"}"}}`,
		`{"message":{"role":"user","content":"앞"},"payload":{"role":"assistant","content":"뒤"}}`,
		`{"role":"assistant","content":"x","ignored":[-1.02e+3,true,false,null,{"x":[]} ]}`,
		`{"role":"user","content":"x","late":01}`,
		`{"role":"user","content":"x","late":[1,]}`,
		`{"role":"user","content":"x","late":"\uZZZZ"}`,
		`{"role":"user","content":"x"} {}`,
		`{"role":"user","content":[1]}`,
		`{"role":"user","content":"\q"}`,
	}
	for _, value := range cases {
		expected, expectedRole, valid := archiveSafeMessage([]byte(value))
		role, open, err := archiveJSONSafeText(archiveJSONSpan{jsonStreamTestSource([]byte(value)), 0, -1, '{'})
		actual := ""
		if err == nil && open != nil {
			reader, e := open()
			err = e
			if err == nil {
				var b []byte
				b, err = io.ReadAll(reader)
				_ = reader.Close()
				actual = string(b)
			}
		}
		if !valid {
			if err == nil {
				t.Fatalf("invalid accepted: %s", value)
			}
			continue
		}
		if err != nil || actual != expected || (actual != "" && role != expectedRole) {
			t.Fatalf("parity %q: %q/%q vs %q/%q error=%v", value, actual, role, expected, expectedRole, err)
		}
	}
	for _, suffix := range []string{"\xff", "\xc2", "\xed\xa0\x80"} {
		value := []byte(`{"role":"user","content":"` + strings.Repeat("x", 32767) + suffix + `"}`)
		if _, _, err := archiveJSONSafeText(archiveJSONSpan{jsonStreamTestSource(value), 0, -1, '{'}); err == nil {
			t.Fatal("invalid UTF-8 accepted")
		}
	}
}
func TestArchiveStreamingSecretAndWindowParity(t *testing.T) {
	for _, text := range []string{"한국어 안전 원문", strings.Repeat("x", 40000) + " password: hidden", "비밀번호 : test", "-----BEGIN PRIVATE KEY-----", "https://user:pass@host", "Bearer abc.def", "앞 " + strings.Repeat("a", 10000) + " 단서 뒤"} {
		observer := newArchiveTextObserver(strings.NewReader(text), "단서", 0, 4096, false)
		got := archiveStreamSecrets.MatchReader(observer)
		if observer.failure != nil || got != containsSecret(text) {
			t.Fatal("secret parity", got, observer.failure)
		}
		if !got && strings.Contains(text, "단서") && !strings.Contains(string(observer.output), "단서") {
			t.Fatal("lost match")
		}
	}
	text := strings.Repeat("가나다", 2000) + "끝"
	for _, offset := range []int{0, 1, 3, len(text) - 3, len(text)} {
		observer := newArchiveTextObserver(strings.NewReader(text), "", offset, 64, true)
		if archiveStreamSecrets.MatchReader(observer) {
			t.Fatal("unexpected secret")
		}
		if offset == 1 && !observer.invalid {
			t.Fatal("middle rune accepted")
		}
		if offset == len(text) && observer.found {
			t.Fatal("past end accepted")
		}
	}
}

func FuzzArchiveStreamingJSON(f *testing.F) {
	for _, seed := range []string{`{"role":"user","content":"안전 문장"}`, `{"role":"assistant","content":[{"type":"text","text":"a"}]}`, `{"table":"messages","record":{"data":"{\"role\":\"user\",\"content\":\"x\"}"}}`, `{"role":"user","content":"\uD800x","role":"tool"}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 64<<10 {
			return
		}
		expected, expectedRole, _ := archiveSafeMessage([]byte(value))
		role, open, err := archiveJSONSafeText(archiveJSONSpan{jsonStreamTestSource([]byte(value)), 0, -1, '{'})
		actual := ""
		if err == nil && open != nil {
			reader, e := open()
			err = e
			if err == nil {
				var b []byte
				b, err = io.ReadAll(reader)
				_ = reader.Close()
				actual = string(b)
			}
		}
		if (expected != "" && err != nil) || (err == nil && (actual != expected || (actual != "" && role != expectedRole))) {
			t.Fatalf("safe parser differs: expected=%q/%q got=%q/%q err=%v input=%q", expected, expectedRole, actual, role, err, value)
		}
	})
}

// Force boundaries independently of bufio's requested read size.
type archiveSplitReader struct {
	io.Reader
	width int
}

func (r archiveSplitReader) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), r.width)])
}
func TestArchiveStreamingBlockBoundaries(t *testing.T) {
	for _, width := range []int{1, 2, 7, 255, 4096, 32768} {
		for _, query := range []string{"단서", strings.Repeat("q", 33000) + "끝"} {
			body := strings.Repeat("가", 11001) + query + strings.Repeat("나", 2000)
			observer := newArchiveTextObserver(archiveSplitReader{strings.NewReader(body), width}, query, 0, 4096, false)
			if _, err := io.Copy(io.Discard, observer); err != nil {
				t.Fatal(err)
			}
			start := strings.Index(body, query) - 256
			for start > 0 && body[start]&0xc0 == 0x80 {
				start--
			}
			if !observer.found || observer.start != start || string(observer.output) != body[start:start+4096] {
				t.Fatalf("window mismatch width=%d queryBytes=%d start=%d expected=%d", width, len(query), observer.start, start)
			}
		}
	}
}
func TestArchiveSecretPrefixBoundaries(t *testing.T) {
	cases := []string{
		"비밀번호: value",
		"-----BEGIN RSA PRIVATE KEY-----body-----END RSA PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		"postgresql://user:pass@host",
		"Bearer abc.def", "Basic abc", "eyJabc.eyJdef.abc",
		"AKIA1234567890123456", "AIza" + strings.Repeat("a", 25),
		"github_pat_" + strings.Repeat("a", 25), "xoxb-" + strings.Repeat("a", 15),
		"sk-" + strings.Repeat("a", 15), "API_KEY: value", "paſſword: value",
	}
	for _, secret := range cases {
		if !containsSecret(secret) {
			t.Fatalf("test fixture is not a secret: %q", secret)
		}
		for _, width := range []int{1, 2, 7, 32, 8192} {
			body := strings.Repeat(" ", 8190) + secret
			observer := newArchiveTextObserver(archiveSplitReader{strings.NewReader(body), width}, "", 0, 32, true)
			if _, err := io.Copy(io.Discard, observer); err != nil || !observer.candidate {
				t.Fatalf("missed prefix width=%d: %q (%v)", width, secret, err)
			}
		}
	}
}
func FuzzArchiveSecretPrefix(f *testing.F) {
	for _, value := range []string{"password: value", "한국어", "Bearer abc", "https://a:b@c", "-----BEGIN PRIVATE KEY-----"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 65536 {
			return
		}
		expected := containsSecret(value)
		observer := newArchiveTextObserver(archiveSplitReader{strings.NewReader(value), 31}, "", 0, 0, true)
		if _, err := io.Copy(io.Discard, observer); err != nil {
			t.Fatal(err)
		}
		got := observer.candidate && archiveStreamSecrets.MatchString(value[observer.candidateOffset:])
		if got != expected {
			t.Fatalf("secret prefix/suffix mismatch: %q offset=%d", value, observer.candidateOffset)
		}
	})
}

// The capture hook's Codex prompt is its input_text blocks joined with nothing (capture
// TestPromptEvidenceCodexMultiBlockPromptFromHookToArchiveCheck); both server parsers agree.
func TestArchiveCodexInputTextBlocksJoinLikeHookPrompt(t *testing.T) {
	first, second := "\n# Browser comments:\n\n## User Comment 1\n버튼 위치", "The next image is page evidence for Comment 1."
	row := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":` +
		strconv.Quote(first) + `},{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":` + strconv.Quote(second) + `}]}}`
	text, role, valid := archiveSafeMessage([]byte(row))
	if !valid || role != "user" || text != first+second {
		t.Fatalf("%q %q %v", text, role, valid)
	}
	_, open, err := archiveJSONSafeText(archiveJSONSpan{jsonStreamTestSource([]byte(row)), 0, -1, '{'})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := open()
	if err != nil {
		t.Fatal(err)
	}
	streamed, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(streamed) != first+second {
		t.Fatalf("%q %v", streamed, err)
	}
	// Claude text blocks, and any pair that is not two input_text blocks, keep their newline.
	if text, _, _ := archiveSafeMessage([]byte(`{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"input_text","text":"c"}]}`)); text != "a\nb\nc" {
		t.Fatalf("%q", text)
	}
}

// Capture's locator (TestPromptEvidenceJoinsBlocksLikeServer) hashes these same strings.
func TestArchiveBlocksJoinLikeCapture(t *testing.T) {
	for content, want := range map[string]string{
		`[{"type":"text","text":null},{"type":"text","text":"a"}]`:       "\na",
		`[{"type":"output_text","text":"a"},{"type":"text","text":"b"}]`: "a\nb",
	} {
		row := `{"role":"user","content":` + content + `}`
		if text, _, valid := archiveSafeMessage([]byte(row)); !valid || text != want {
			t.Fatalf("search %s = %q %v", content, text, valid)
		}
		_, open, err := archiveJSONSafeText(archiveJSONSpan{jsonStreamTestSource([]byte(row)), 0, -1, '{'})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := open()
		if err != nil {
			t.Fatal(err)
		}
		streamed, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || string(streamed) != want {
			t.Fatalf("stream %s = %q %v", content, streamed, err)
		}
	}
}
