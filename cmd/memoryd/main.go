// memoryd runs the API and the leased embedding worker against a dedicated
// PostgreSQL database. It never connects to Design databases or Qdrant.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
	"github.com/jackc/pgx/v5"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("memoryd stopped: configuration or dependency failure (raw errors suppressed)")
		os.Exit(1)
	}
}
func run() error {
	receiptCauses := flag.Bool("receipt-causes", false, "include receipt origin evidence in offline v5/v6 recovery snapshots")
	writeGate := flag.String("recovery-write-gate", "", "offline administrator: status|pause|resume canonical database writes")
	expectedStorage := flag.String("expected-storage-identity", "", "independently verified storage identity for write gate administration")
	expectedGeneration := flag.String("expected-write-generation", "", "current gate generation required for pause/resume, or paused generation required for snapshot export")
	inspectRestore := flag.Bool("inspect-restored-recovery", false, "offline administrator: inspect all-scope requests in a publication-blocked restore")
	exportSnapshot := flag.Bool("export-recovery-snapshot", false, "offline administrator: export all-scope recovery ledger from one read-only database snapshot")
	adoptBlobs := flag.Bool("adopt-restored-blobs", false, "verify quarantined restored files and bind to the restored DB; stopped writers required")
	backupExpiry := flag.String("privacy-backup-expiry", "", "RFC3339 time of the oldest retained backup; finish privacy deletions parked before it, then exit")
	collectBlobs := flag.Bool("collect-blobs", false, "collect unreferenced blob files and abandoned temporary files; service maintenance authorization required")
	initDB := flag.Bool("init-db", false, "create Memory collections and ordinary indexes")
	semanticIndex := flag.String("semantic-index", "", "create or drop the opt-in pgvector knowledge index: create|drop")
	checkIndexes := flag.Bool("check-indexes", false, "verify the Postgres schema, search indexes and RLS are ready")
	recovery := flag.Bool("recovery", false, "isolated deletion recovery only; loopback API or erasure-only worker")
	worker := flag.Bool("worker", false, "run embedding worker instead of HTTP API")
	reindex := flag.Bool("reindex", false, "queue embedding jobs for Postgres records not in the configured embedder's space, then exit")
	listen := flag.String("listen", "127.0.0.1:8091", "HTTP listen address")
	flag.Parse()
	if *receiptCauses && !*exportSnapshot && !*inspectRestore {
		return memory.ErrInvalid
	}
	if *writeGate != "" {
		count := 3
		if *writeGate == "status" {
			count = 2
		}
		if flag.NFlag() != count || flag.NArg() != 0 || *expectedStorage == "" || (*writeGate != "status" && *expectedGeneration == "") || (*writeGate == "status" && *expectedGeneration != "") {
			return memory.ErrInvalid
		}
	} else if *expectedStorage != "" || (*expectedGeneration != "" && !*exportSnapshot && !*inspectRestore) {
		return memory.ErrInvalid
	}
	if *exportSnapshot || *inspectRestore {
		count := 1
		if *receiptCauses {
			count++
		}
		if *expectedGeneration != "" {
			count++
		}
		if flag.NFlag() != count || flag.NArg() != 0 {
			return memory.ErrInvalid
		}
		info, err := os.Stdout.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return memory.ErrInvalid
		}
	}
	standalone := os.Getenv("MEMORY_LOCAL_STANDALONE")
	if standalone != "" && standalone != "true" {
		return memory.ErrInvalid
	}
	if standalone == "true" && !*worker {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return memory.ErrInvalid
		}
	}
	if (*adoptBlobs || *collectBlobs) && ((*adoptBlobs && *collectBlobs) || *initDB || *checkIndexes || *recovery || *worker) {
		return memory.ErrInvalid
	}
	if *recovery {
		host, _, err := net.SplitHostPort(*listen)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || *initDB || *checkIndexes {
			return memory.ErrInvalid
		}
	}
	store, e := configuredStore()
	if e != nil {
		return e
	}

	syntheticHandoff := os.Getenv("MEMORY_SYNTHETIC_HANDOFF")
	if syntheticHandoff != "" && syntheticHandoff != "1" {
		return memory.ErrInvalid
	}
	if syntheticHandoff == "1" {
		host, _, err := net.SplitHostPort(*listen)
		if (!*worker && (err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) || store != "postgres" {
			return memory.ErrInvalid
		}
		if err := syntheticEmbedderAllowed(); err != nil {
			return err
		}
		databaseConfig, err := pgx.ParseConfig(os.Getenv("MEMORY_DATABASE_URL"))
		if err != nil {
			return memory.ErrInvalid
		}
		dbhost := databaseConfig.Host
		if dbhost != "localhost" && (net.ParseIP(dbhost) == nil || !net.ParseIP(dbhost).IsLoopback()) {
			return memory.ErrInvalid
		}
		for _, fallback := range databaseConfig.Fallbacks {
			host := fallback.Host
			if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
				return memory.ErrInvalid
			}
		}
		for _, key := range []string{"SKILL_LLM_API_KEY", "TYPESAFE_API_KEY", "LANGSMITH_API_KEY", "OTEL_EXPORTER_OTLP_ENDPOINT"} {
			if os.Getenv(key) != "" {
				return memory.ErrInvalid
			}
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var repo memory.Repository
	op, cancel := context.WithTimeout(ctx, 15*time.Second)
	pg, err := memory.NewPostgres(op, os.Getenv("MEMORY_DATABASE_URL"))
	cancel()
	if err != nil {
		return err
	}
	defer pg.Close()
	if *writeGate != "" {
		op, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		status, err := pg.ManageRecoveryWrites(op, *writeGate, *expectedStorage, *expectedGeneration)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	if *exportSnapshot || *inspectRestore {
		op, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		var data []byte
		if *receiptCauses {
			data, err = pg.RecoveryReviewSnapshot(op, *inspectRestore, *expectedGeneration)
		} else if *expectedGeneration != "" {
			data, err = pg.RecoverySnapshotAtWriteGate(op, *inspectRestore, *expectedGeneration)
		} else if *inspectRestore {
			data, err = pg.InspectRestoredRecovery(op)
		} else {
			data, err = pg.ExportRecoverySnapshot(op)
		}
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	}
	pg.SetRecoveryMode(*recovery)
	if raw := os.Getenv("MEMORY_VECTOR_MAX_DISTANCE"); raw != "" {
		d, err := strconv.ParseFloat(raw, 64)
		if err != nil || d <= 0 || d >= 2 {
			return fmt.Errorf("MEMORY_VECTOR_MAX_DISTANCE must be a cosine distance in (0,2)")
		}
		pg.SetVectorMaxDistance(d)
	}
	if raw := os.Getenv("MEMORY_TOPIC_DIGEST_MAX"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return fmt.Errorf("MEMORY_TOPIC_DIGEST_MAX must be 1-100")
		}
		memory.TopicDigestCap = n
	}
	repo = pg
	if *initDB {
		op, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return pg.Init(op)
	}
	if *semanticIndex != "" {
		if *semanticIndex != "create" && *semanticIndex != "drop" || os.Getenv("MEMORY_SEMANTIC_RETRIEVAL") != "1" {
			return memory.ErrInvalid
		}
		op, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		return pg.InitSemanticIndex(op, *semanticIndex == "create")
	}
	if *checkIndexes {
		return pg.SearchReady(ctx)
	}
	if *worker || *collectBlobs || *adoptBlobs || *reindex || *backupExpiry != "" {
		ctx = memory.ContextWithPostgresToken(ctx, os.Getenv("MEMORY_WORKER_TOKEN"))
	}
	if *backupExpiry != "" {
		oldest, err := time.Parse(time.RFC3339, *backupExpiry)
		if err != nil {
			return memory.ErrInvalid
		}
		auth, err := configuredAuthorizer(repo, standalone)
		if err != nil {
			return err
		}
		svc := &memory.Service{Repo: repo, Auth: auth}
		op, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		advanced, err := svc.ExpirePrivacyBackups(op, os.Getenv("MEMORY_WORKER_TOKEN"), oldest)
		if err != nil {
			return err
		}
		fmt.Printf("{\"advanced\":%d}\n", advanced)
		return nil
	}
	if *collectBlobs || *adoptBlobs {
		auth, err := configuredAuthorizer(repo, standalone)
		if err != nil {
			return err
		}
		svc := &memory.Service{Repo: repo, Auth: auth, BlobDirectory: os.Getenv("MEMORY_BLOB_DIRECTORY")}
		if *adoptBlobs {
			if err = svc.AdoptRestoredBlobs(ctx, os.Getenv("MEMORY_WORKER_TOKEN")); err != nil {
				return err
			}
			fmt.Println("restored file inventory bound; publication blocked pending deletion and expiry review")
			return nil
		}
		op, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		result, err := svc.CollectBlobGarbage(op, os.Getenv("MEMORY_WORKER_TOKEN"))
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if !*recovery {
		if e = pg.CheckRestorePublication(ctx); e != nil {
			return e
		}
		ready, cancelReady := context.WithTimeout(ctx, 30*time.Second)
		e = repo.SearchReady(ready)
		cancelReady()
		if e != nil {
			return e
		}
	}
	auth, e := configuredAuthorizer(repo, standalone)
	if e != nil {
		return e
	}
	embed, e := configuredEmbedder(*worker, *recovery)
	if e != nil {
		return e
	}
	if *reindex {
		pg, ok := repo.(*memory.Postgres)
		if !ok || embed == nil {
			return memory.ErrInvalid
		}
		queued, err := pg.EnqueueReindex(ctx, embed.Space())
		if err != nil {
			return err
		}
		fmt.Printf("{\"queued\":%d,\"space\":%q}\n", queued, embed.Space())
		return nil
	}
	candidateLimit := 0
	if raw := os.Getenv("MEMORY_CANDIDATE_DAILY_LIMIT"); raw != "" {
		candidateLimit, e = strconv.Atoi(raw)
		if e != nil || candidateLimit < 0 || candidateLimit > 100000 {
			return memory.ErrInvalid
		}
	}
	costRates, e := costRatesFromEnv()
	if e != nil {
		return e
	}
	google, err := googleLogin()
	if err != nil {
		return err
	}
	svc := &memory.Service{GoogleLogin: google, SyntheticHandoff: syntheticHandoff == "1", BlobDirectory: os.Getenv("MEMORY_BLOB_DIRECTORY"), Repo: repo, Auth: auth, Embedder: embed, CandidateDailyLimit: candidateLimit, CostRates: costRates}
	svc.ModelBudgetPolicy, e = memory.ParseModelBudgetPolicy(os.Getenv("MEMORY_MODEL_BUDGET_POLICY"))
	if e != nil {
		return e
	}
	svc.MemoryReviewPolicies, e = memory.ParseMemoryReviewPolicies(os.Getenv("MEMORY_REVIEW_POLICIES"))
	if e != nil {
		return e
	}
	svc.SkillAutomationPolicies, e = memory.ParseSkillAutomationPolicies(os.Getenv("MEMORY_SKILL_AUTOMATION_POLICIES"))
	if e != nil {
		return e
	}
	if key := os.Getenv("MEMORY_SESSION_ARCHIVE_KEY"); key != "" {
		decoded, err := hex.DecodeString(key)
		if err != nil || len(decoded) != 32 {
			return memory.ErrInvalid
		}
		svc.SessionArchiveKey = decoded
	}
	blobReady, cancelBlobReady := context.WithTimeout(ctx, 10*time.Second)
	if store == "postgres" {
		// Startup binding is maintenance work; HTTP requests still bind their
		// own principal (including an explicit empty token) in the handler.
		blobReady = memory.ContextWithPostgresToken(blobReady, os.Getenv("MEMORY_WORKER_TOKEN"))
		svc.ReadinessToken = os.Getenv("MEMORY_WORKER_TOKEN")
		// Failed skill.usage traces are evaluated by this service principal (operator-approved 2026-09-23).
		svc.ReflectionToken = os.Getenv("MEMORY_WORKER_TOKEN")
		// MEMORY_SKILL_SCOPE=workspace/project/user: canonical skills other runtimes resolve from.
		if parts := strings.Split(os.Getenv("MEMORY_SKILL_SCOPE"), "/"); len(parts) == 3 {
			svc.SkillScope = memory.Scope{WorkspaceID: parts[0], ProjectID: parts[1], UserID: parts[2]}
		}
	}
	e = svc.CheckBlobStorage(blobReady)
	cancelBlobReady()
	if e != nil {
		return e
	}
	if key := os.Getenv("MEMORY_UNSPLASH_ACCESS_KEY"); key != "" {
		svc.Unsplash = &memory.Unsplash{Key: key}
	}
	if !*recovery && os.Getenv("SKILL_LLM_API_KEY") != "" {
		config := memory.LLMConfig{Endpoint: os.Getenv("SKILL_LLM_ENDPOINT_URL"), APIKey: os.Getenv("SKILL_LLM_API_KEY"), Model: os.Getenv("SKILL_LLM_MODEL")}
		svc.Analyzer = &memory.ChatSkillAnalyzer{Config: config}
		svc.Drafter = &memory.ChatSkillDrafter{Config: config}
		if os.Getenv("ARKISM_WIKI") == "on" { // optional LLM-Wiki jobs, off by default
			svc.Wiki = &memory.ChatWikiLLM{Config: config}
		}
	}
	if !*recovery && os.Getenv("ARKISM_ANTHROPIC_SKILL_DRAFTER") == "1" {
		keyFile := os.Getenv("ARKISM_ANTHROPIC_KEY_FILE")
		key, err := os.ReadFile(keyFile)
		if err != nil || strings.TrimSpace(string(key)) == "" || os.Getenv("ARKISM_ANTHROPIC_WORKSPACE_ID") == "" || svc.ModelBudgetPolicy == nil {
			return errors.New("Anthropic skill drafter requires key file, workspace and model budget policy")
		}
		svc.Drafter = &memory.AnthropicSkillDrafter{Service: svc, APIKey: strings.TrimSpace(string(key)), WorkspaceID: os.Getenv("ARKISM_ANTHROPIC_WORKSPACE_ID"), BudgetScope: svc.ModelBudgetPolicy.Scope}
	}
	if !*recovery {
		// Skill routing: Cloudflare Clef-flash when its account and token are set, with Jev as the spare; else Jev alone.
		var jev memory.MeteredSkillJudge
		if key := os.Getenv("TYPESAFE_API_KEY"); key != "" {
			jev = &memory.JevJudge{APIKey: key}
		}
		if account, token := os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_AI_TOKEN"); account != "" && token != "" {
			clef := memory.NewClefJudge(account, token)
			svc.Router = &memory.FallbackJudge{Primary: clef, Secondary: jev}
			svc.ModelPicker = clef // 라우팅: Clef picks the model; without it tasks go to the fixed or first model
		} else if jev != nil {
			svc.Router = jev
			svc.ModelPicker = jev.(*memory.JevJudge)
		}
		// 라우팅 with the user's own chat model (Ollama, OpenAI, OpenRouter…): replaces Clef or Jev for picking models.
		if endpoint, model := os.Getenv("ARKISM_ROUTING_PICKER_URL"), os.Getenv("ARKISM_ROUTING_PICKER_MODEL"); endpoint != "" && model != "" {
			svc.ModelPicker = &memory.ChatModelPicker{Endpoint: endpoint, APIKey: os.Getenv("ARKISM_ROUTING_PICKER_KEY"), Model: model}
		}
	}
	if *worker {
		token := os.Getenv("MEMORY_WORKER_TOKEN")
		if token == "" {
			return memory.ErrInvalid
		}
		noAgentRuntime := os.Getenv("MEMORY_PRIVACY_NO_AGENT_RUNTIME") == "true"
		for {
			op, c := context.WithTimeout(ctx, 90*time.Second)
			if noAgentRuntime {
				if _, perr := svc.RunPrivacyRuntimeStep(op, token); perr != nil && !errors.Is(perr, memory.ErrNotFound) {
					slog.Warn("privacy runtime step deferred")
				}
			}
			err := svc.WorkOnce(op, token)
			c()
			if err != nil && !errors.Is(err, memory.ErrNotFound) {
				slog.Warn("memory job deferred; see scoped job status")
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	}
	handler := memory.Handler(svc)
	if *recovery {
		handler = memory.RecoveryHandler(svc)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = server.Shutdown(shutdown)
	}()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	slog.Info("memory API starting", "address", listener.Addr().String(), "recovery", *recovery)
	e = server.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

func costRatesFromEnv() (memory.CostRates, error) {
	perMillion := func(name string) (int64, error) {
		raw := os.Getenv(name)
		if raw == "" {
			return 0, nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || value > 1000 {
			return 0, memory.ErrInvalid
		}
		return int64(math.Round(value * 1_000_000)), nil
	}
	perRequest := func(name string) (int64, error) {
		raw := os.Getenv(name)
		if raw == "" {
			return 0, nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || value > 1000 {
			return 0, memory.ErrInvalid
		}
		return int64(math.Round(value * 1_000_000_000_000)), nil
	}
	var rates memory.CostRates
	var err error
	rates.Revision = os.Getenv("MEMORY_PRICING_REVISION")
	if rates.SkillLLMInputTokenPicoUSD, err = perMillion("SKILL_LLM_INPUT_USD_PER_MILLION_TOKENS"); err != nil {
		return rates, err
	}
	if rates.SkillLLMOutputTokenPicoUSD, err = perMillion("SKILL_LLM_OUTPUT_USD_PER_MILLION_TOKENS"); err != nil {
		return rates, err
	}
	if rates.JEVRequestPicoUSD, err = perRequest("JEV_USD_PER_REQUEST"); err != nil {
		return rates, err
	}
	if rates.Revision == "" && (rates.SkillLLMInputTokenPicoUSD > 0 || rates.SkillLLMOutputTokenPicoUSD > 0 || rates.JEVRequestPicoUSD > 0) {
		return rates, memory.ErrInvalid
	}
	return rates, nil
}

// googleLogin turns on desktop Google sign-in when all four values are set; a
// partial configuration is refused at start rather than half working.
func googleLogin() (*memory.GoogleLogin, error) {
	id, secret := os.Getenv("MEMORY_GOOGLE_CLIENT_ID"), os.Getenv("MEMORY_GOOGLE_CLIENT_SECRET")
	emails, token := os.Getenv("MEMORY_GOOGLE_ALLOWED_EMAILS"), os.Getenv("MEMORY_DESKTOP_TOKEN")
	if id == "" && secret == "" && emails == "" && token == "" {
		return nil, nil
	}
	if id == "" || secret == "" || emails == "" || len(token) < 32 {
		return nil, fmt.Errorf("google login needs MEMORY_GOOGLE_CLIENT_ID, MEMORY_GOOGLE_CLIENT_SECRET, MEMORY_GOOGLE_ALLOWED_EMAILS and MEMORY_DESKTOP_TOKEN together")
	}
	allowed := map[string]bool{}
	for _, e := range strings.Split(emails, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			allowed[e] = true
		}
	}
	return &memory.GoogleLogin{ClientID: id, ClientSecret: secret, Allowed: allowed, DesktopToken: token}, nil
}
