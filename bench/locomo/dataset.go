package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LoCoMo (Maharana et al., 2024), snap-research/locomo, CC BY-NC 4.0. The data is never
// committed: it is fetched at run time from a pinned commit and checked against a pinned sha256.
const (
	datasetCommit = "cbfbc1dba6bc53d00625212a0f22d55ffee7c1fc"
	datasetURL    = "https://raw.githubusercontent.com/snap-research/locomo/" + datasetCommit + "/data/locomo10.json"
	datasetSHA256 = "79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4"
)

// categoryNames follows the labels in the released data (mem0 uses categories 1-4 and drops 5).
var categoryNames = map[int]string{1: "multi-hop", 2: "temporal", 3: "open-domain", 4: "single-hop", 5: "adversarial"}

type Turn struct {
	Speaker string `json:"speaker"`
	DiaID   string `json:"dia_id"`
	Text    string `json:"text"`
	Caption string `json:"blip_caption"`
}

type Session struct {
	N       int
	DateRaw string
	Date    time.Time
	Turns   []Turn
}

type QA struct {
	Index    int
	Question string
	Answer   string
	Evidence []string
	Category int
}

type Conversation struct {
	SampleID string
	SpeakerA string
	SpeakerB string
	Sessions []Session
	QA       []QA
}

var sessionKey = regexp.MustCompile(`^session_(\d+)$`)
var evidenceSession = regexp.MustCompile(`D(\d+):`)

// parseDate reads LoCoMo's "1:56 pm on 8 May, 2023" as UTC.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"3:04 pm on 2 January, 2006", "3:04 pm on 2 Jan, 2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized session date %q", s)
}

// ParseDataset reads locomo10.json.
func ParseDataset(data []byte) ([]Conversation, error) {
	var raw []struct {
		SampleID     string                     `json:"sample_id"`
		Conversation map[string]json.RawMessage `json:"conversation"`
		QA           []struct {
			Question string   `json:"question"`
			Answer   any      `json:"answer"`
			Evidence []string `json:"evidence"`
			Category int      `json:"category"`
		} `json:"qa"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("dataset json: %w", err)
	}
	out := make([]Conversation, 0, len(raw))
	for _, r := range raw {
		c := Conversation{SampleID: r.SampleID}
		_ = json.Unmarshal(r.Conversation["speaker_a"], &c.SpeakerA)
		_ = json.Unmarshal(r.Conversation["speaker_b"], &c.SpeakerB)
		for k, v := range r.Conversation {
			m := sessionKey.FindStringSubmatch(k)
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			s := Session{N: n}
			if err := json.Unmarshal(v, &s.Turns); err != nil {
				return nil, fmt.Errorf("%s %s: %w", r.SampleID, k, err)
			}
			_ = json.Unmarshal(r.Conversation[k+"_date_time"], &s.DateRaw)
			t, err := parseDate(s.DateRaw)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", r.SampleID, k, err)
			}
			s.Date = t
			c.Sessions = append(c.Sessions, s)
		}
		sort.Slice(c.Sessions, func(i, j int) bool { return c.Sessions[i].N < c.Sessions[j].N })
		for i, q := range r.QA {
			ans := ""
			switch a := q.Answer.(type) {
			case nil:
			case string:
				ans = a
			case float64:
				ans = strconv.FormatFloat(a, 'f', -1, 64)
			default:
				ans = fmt.Sprint(a)
			}
			c.QA = append(c.QA, QA{Index: i, Question: q.Question, Answer: ans, Evidence: q.Evidence, Category: q.Category})
		}
		out = append(out, c)
	}
	return out, nil
}

// EvidenceSessions is the set of session numbers a question's evidence points at.
func (q QA) EvidenceSessions() []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range q.Evidence {
		for _, m := range evidenceSession.FindAllStringSubmatch(e, -1) {
			n, _ := strconv.Atoi(m[1])
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Ints(out)
	return out
}

// SelectQuestions keeps categories 1-4 (5, adversarial, is excluded as in mem0). With
// maxSession > 0 only questions whose evidence lies wholly in sessions 1..maxSession stay.
// limit > 0 caps the count, in dataset order.
func SelectQuestions(qa []QA, maxSession, limit int) []QA {
	var out []QA
	for _, q := range qa {
		if q.Category < 1 || q.Category > 4 || strings.TrimSpace(q.Answer) == "" {
			continue
		}
		if maxSession > 0 {
			ev := q.EvidenceSessions()
			if len(ev) == 0 || ev[len(ev)-1] > maxSession {
				continue
			}
		}
		out = append(out, q)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// SessionText is one session as the extractor and the full-context baseline see it:
// one "Speaker: text" line per turn, shared images by their caption.
func SessionText(s Session) string {
	var b strings.Builder
	for _, t := range s.Turns {
		line := strings.TrimSpace(t.Text)
		if c := strings.TrimSpace(t.Caption); c != "" {
			line += " [shares a photo: " + c + "]"
		}
		fmt.Fprintf(&b, "%s: %s\n", t.Speaker, line)
	}
	return strings.TrimRight(b.String(), "\n")
}

// LoadDataset returns the pinned locomo10.json, downloading it into cacheDir when needed.
// A local path (--dataset) is accepted only if its sha256 matches the pin.
func LoadDataset(cacheDir, local string) ([]byte, string, error) {
	check := func(data []byte) error {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != datasetSHA256 {
			return fmt.Errorf("dataset sha256 %s != pinned %s", got, datasetSHA256)
		}
		return nil
	}
	if local != "" {
		data, err := os.ReadFile(local)
		if err != nil {
			return nil, "", err
		}
		return data, local, check(data)
	}
	path := filepath.Join(cacheDir, "locomo10-"+datasetSHA256[:12]+".json")
	if data, err := os.ReadFile(path); err == nil && check(data) == nil {
		return data, path, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, "", err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(datasetURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("dataset download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	if err := check(data); err != nil {
		return nil, "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, "", err
	}
	return data, path, os.Rename(tmp, path)
}
