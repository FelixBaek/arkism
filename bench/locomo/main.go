// Command locomo runs the LoCoMo long-term conversational memory benchmark against a running
// Arkism server, through Arkism's real capture path (extraction prompt + POST /v1/capture),
// review API and recall API, and grades answers with an LLM judge (mem0 methodology:
// categories 1-4, adversarial category 5 excluded, binary CORRECT/WRONG).
//
//	go run ./bench/locomo --url http://127.0.0.1:PORT --token-file F [flags]
//
// See bench/locomo/README.md.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host/capture"
)

type config struct {
	URL              string `json:"url"`
	Workspace        string `json:"workspace"`
	ProjectTemplate  string `json:"project_template"`
	User             string `json:"user"`
	Conversations    int    `json:"conversations"`
	Sessions         int    `json:"sessions"`
	QuestionsPerConv int    `json:"questions_per_conv"`
	Variant          string `json:"variant"`
	AnswerModel      string `json:"answer_model"`
	JudgeModel       string `json:"judge_model"`
	ExtractModel     string `json:"extract_model"`
	FullContext      bool   `json:"full_context_baseline"`
	NoMemory         bool   `json:"no_memory_baseline"`
	CueFilter        bool   `json:"cue_filter"`
	Guidance         string `json:"guidance,omitempty"`
	SearchLimit      int    `json:"search_limit"`
	Concurrency      int    `json:"concurrency"`
	TimeoutSec       int    `json:"cli_timeout_sec"`
	Retries          int    `json:"cli_retries"`
	SettleSec        int    `json:"settle_sec"`
	RunID            string `json:"run_id"`
	ClaudeBin        string `json:"claude_bin"`
}

func itoa(n int) string { return strconv.Itoa(n) }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "locomo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fl := flag.NewFlagSet("locomo", flag.ContinueOnError)
	var c config
	tokenFile := fl.String("token-file", "", "file holding the owner token (never printed)")
	out := fl.String("out", "", "output dir (default ./locomo-out/<run-id>); reusing it resumes the run")
	cacheDir := fl.String("cache-dir", "", "dataset cache dir (default <user cache>/arkism-bench/locomo)")
	dataset := fl.String("dataset", "", "local locomo10.json instead of downloading (sha256 still checked)")
	fl.StringVar(&c.URL, "url", "", "Arkism server URL, e.g. http://127.0.0.1:8090")
	fl.StringVar(&c.Workspace, "workspace", "default", "scope workspace")
	fl.StringVar(&c.ProjectTemplate, "project-template", "locomo-{sample}", "scope project; {sample} becomes the conversation id (conv-26 ...)")
	fl.StringVar(&c.User, "user", "management:owner", "scope user")
	fl.IntVar(&c.Conversations, "conversations", 0, "first N conversations (0 = all 10)")
	fl.IntVar(&c.Sessions, "sessions", 0, "ingest only the first N sessions of each conversation and ask only questions whose evidence is in them (0 = all)")
	fl.IntVar(&c.QuestionsPerConv, "questions-per-conv", 0, "cap questions per conversation (0 = all)")
	fl.StringVar(&c.Variant, "variant", "approve-all", "approve-all (approve every candidate) | review (leave them as the server decides)")
	fl.StringVar(&c.AnswerModel, "answer-model", "haiku", "answer model (claude CLI --model)")
	fl.StringVar(&c.JudgeModel, "judge-model", "", "judge model (default: answer model)")
	fl.BoolVar(&c.FullContext, "full-context-baseline", false, "also answer from the whole conversation")
	noMem := fl.Bool("no-memory-baseline", true, "also answer with no memories")
	fl.BoolVar(&c.CueFilter, "cue-filter", false, "send only turns matching the drain's cue regex (as hooks do); default sends whole sessions")
	fl.StringVar(&c.Guidance, "guidance", "", "user guidance note appended to the extraction prompt (as capture.json instructions)")
	fl.IntVar(&c.SearchLimit, "search-limit", 10, "memories retrieved per question")
	fl.IntVar(&c.Concurrency, "concurrency", 2, "parallel CLI calls")
	fl.IntVar(&c.TimeoutSec, "timeout", 180, "seconds per CLI call")
	fl.IntVar(&c.Retries, "retries", 2, "retries per CLI call / API step")
	fl.IntVar(&c.SettleSec, "settle", 5, "seconds to wait after ingest for the worker")
	fl.StringVar(&c.RunID, "run-id", "", "run id (default: time-based; kept from run.json when resuming)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	c.NoMemory = *noMem
	if c.URL == "" || *tokenFile == "" {
		return errors.New("--url and --token-file are required")
	}
	if c.Variant != "approve-all" && c.Variant != "review" {
		return errors.New("--variant must be approve-all or review")
	}
	if c.JudgeModel == "" {
		c.JudgeModel = c.AnswerModel
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return errors.New("token file is empty")
	}
	c.ExtractModel = capture.CaptureModel()
	c.ClaudeBin = defaultClaudeBin()

	if c.RunID == "" {
		c.RunID = time.Now().UTC().Format("20060102-150405")
	}
	if *out == "" {
		*out = filepath.Join("locomo-out", c.RunID)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if prev, err := os.ReadFile(filepath.Join(*out, "run.json")); err == nil {
		var p config
		if err := json.Unmarshal(prev, &p); err != nil {
			return fmt.Errorf("run.json: %w", err)
		}
		if p.Variant != c.Variant || p.AnswerModel != c.AnswerModel || p.JudgeModel != c.JudgeModel || p.ExtractModel != c.ExtractModel ||
			p.Sessions != c.Sessions || p.ProjectTemplate != c.ProjectTemplate || p.Workspace != c.Workspace || p.CueFilter != c.CueFilter || p.Guidance != c.Guidance {
			return errors.New("resume: settings differ from run.json in --out; use a new --out")
		}
		c.RunID = p.RunID
		fmt.Printf("resuming run %s in %s\n", c.RunID, *out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if b, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(*out, "run.json"), b, 0o644)
	}

	if *cacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		*cacheDir = filepath.Join(base, "arkism-bench", "locomo")
	}
	data, dataPath, err := LoadDataset(*cacheDir, *dataset)
	if err != nil {
		return err
	}
	convs, err := ParseDataset(data)
	if err != nil {
		return err
	}
	if c.Conversations > 0 && c.Conversations < len(convs) {
		convs = convs[:c.Conversations]
	}
	if c.Sessions > 0 {
		for i := range convs {
			if len(convs[i].Sessions) > c.Sessions {
				convs[i].Sessions = convs[i].Sessions[:c.Sessions]
			}
		}
	}
	if len(convs) > 1 && !strings.Contains(c.ProjectTemplate, "{sample}") {
		return errors.New("--project-template must contain {sample} when more than one conversation runs")
	}

	state, _ := filepath.Abs(filepath.Join(*out, "state"))
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	capture.SetBenchState(state)
	ck, err := openCheckpoint(filepath.Join(*out, "checkpoint.jsonl"))
	if err != nil {
		return err
	}
	b := &bench{cfg: c, api: apiClient{base: strings.TrimRight(c.URL, "/"), token: token, http: httpClient()},
		cli: cliRunner{bin: c.ClaudeBin, dir: state, timeout: time.Duration(c.TimeoutSec) * time.Second, retries: c.Retries},
		ck:  ck, sources: map[string]string{}}
	started := time.Now()
	fmt.Printf("dataset %s (sha256 %s…), %d conversation(s), variant %s, extract=%s answer=%s judge=%s\n",
		dataPath, datasetSHA256[:12], len(convs), c.Variant, c.ExtractModel, c.AnswerModel, c.JudgeModel)

	if err := b.ingestAll(convs); err != nil {
		return err
	}
	ingestDone := time.Now()
	if c.SettleSec > 0 {
		time.Sleep(time.Duration(c.SettleSec) * time.Second)
	}
	failed := b.answerAll(convs)
	finished := time.Now()

	times, err := addTimes(filepath.Join(*out, "timings.json"), reportTimes{Ingest: ingestDone.Sub(started), QA: finished.Sub(ingestDone), Total: finished.Sub(started)})
	if err != nil {
		return err
	}
	rep := buildReport(c, ck.all(), convs, times, failed)
	if err := writeReport(*out, rep); err != nil {
		return err
	}
	fmt.Printf("wrote %s and %s\n", filepath.Join(*out, "results.json"), filepath.Join(*out, "report.md"))
	for _, s := range rep.Systems {
		sum := rep.Summary[s]
		fmt.Printf("  %-13s %.3f (%d/%d)\n", s, sum.Accuracy, sum.Correct, sum.N)
	}
	if failed > 0 {
		return fmt.Errorf("%d question(s) failed after retries; rerun with the same --out to resume", failed)
	}
	return nil
}

type bench struct {
	cfg     config
	api     apiClient
	cli     cliRunner
	ck      *checkpoint
	srcMu   sync.Mutex
	sources map[string]string // source id -> YYYY-MM-DD
}

func (b *bench) scope(conv Conversation) scope {
	return scope{WorkspaceID: b.cfg.Workspace, ProjectID: strings.ReplaceAll(b.cfg.ProjectTemplate, "{sample}", conv.SampleID), UserID: b.cfg.User}
}

// retry runs fn up to retries+1 times with backoff.
func (b *bench) retry(fn func() error) error {
	var err error
	for attempt := 0; attempt <= b.cfg.Retries; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(time.Duration((attempt+1)*(attempt+1)) * 2 * time.Second)
	}
	return err
}
