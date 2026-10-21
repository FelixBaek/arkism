package main

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

func newDaemonService(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, store, standalone, syntheticHandoff string, auth memory.Authorizer, embed memory.Embedder) (*memory.Service, error) {
	candidateLimit := 0
	if raw := os.Getenv("MEMORY_CANDIDATE_DAILY_LIMIT"); raw != "" {
		var e error
		candidateLimit, e = strconv.Atoi(raw)
		if e != nil || candidateLimit < 0 || candidateLimit > 100000 {
			return nil, memory.ErrInvalid
		}
	}
	costRates, e := costRatesFromEnv()
	if e != nil {
		return nil, e
	}
	google, err := googleLogin()
	if err != nil {
		return nil, err
	}
	svc := &memory.Service{GoogleLogin: google, SyntheticHandoff: syntheticHandoff == "1", BlobDirectory: os.Getenv("MEMORY_BLOB_DIRECTORY"), Repo: pg, Auth: auth, Embedder: embed, CandidateDailyLimit: candidateLimit, CostRates: costRates}

	if !cfg.Recovery {
		if path := os.Getenv("MEMORY_REVIEW_CLASSIFIER"); path != "" {
			classifier, err := memory.LoadReviewClassifier(path)
			if err != nil {
				return nil, err
			}
			svc.ReviewClassifier = classifier
		}
	}
	if err := configureDaemonPolicies(svc); err != nil {
		return nil, err
	}
	if err := configureDaemonStorage(ctx, svc, cfg, store); err != nil {
		return nil, err
	}
	if err := configureDaemonProviders(svc, cfg); err != nil {
		return nil, err
	}
	return svc, nil
}
func configureDaemonPolicies(svc *memory.Service) error {
	var e error
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
	return nil
}
func configureDaemonStorage(ctx context.Context, svc *memory.Service, cfg daemonOptions, store string) error {
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
	e := svc.CheckBlobStorage(blobReady)
	cancelBlobReady()
	if e != nil {
		return e
	}
	return nil
}
func configureDaemonProviders(svc *memory.Service, cfg daemonOptions) error {
	key := os.Getenv("MEMORY_UNSPLASH_ACCESS_KEY")
	if key == "" {
		key = os.Getenv("ARKISM_UNSPLASH_KEY") // same key, the shorter name the app docs use
	}
	if key != "" {
		svc.Unsplash = &memory.Unsplash{Key: key}
	}
	if !cfg.Recovery && os.Getenv("SKILL_LLM_API_KEY") != "" {
		config := memory.LLMConfig{Endpoint: os.Getenv("SKILL_LLM_ENDPOINT_URL"), APIKey: os.Getenv("SKILL_LLM_API_KEY"), Model: os.Getenv("SKILL_LLM_MODEL")}
		svc.Analyzer = &memory.ChatSkillAnalyzer{Config: config}
		svc.Drafter = &memory.ChatSkillDrafter{Config: config}
		if os.Getenv("ARKISM_WIKI") == "on" { // optional LLM-Wiki jobs, off by default
			svc.Wiki = &memory.ChatWikiLLM{Config: config}
		}
	}
	if cfg.Recovery {
		return nil
	}
	if err := configureDaemonAnthropic(svc); err != nil {
		return err
	}
	configureDaemonRouting(svc)
	return nil
}
func configureDaemonAnthropic(svc *memory.Service) error {
	if os.Getenv("ARKISM_ANTHROPIC_SKILL_DRAFTER") != "1" {
		return nil
	}

	keyFile := os.Getenv("ARKISM_ANTHROPIC_KEY_FILE")
	key, err := os.ReadFile(keyFile)
	if err != nil || strings.TrimSpace(string(key)) == "" || os.Getenv("ARKISM_ANTHROPIC_WORKSPACE_ID") == "" || svc.ModelBudgetPolicy == nil {
		return errors.New("Anthropic skill drafter requires key file, workspace and model budget policy")
	}
	svc.Drafter = &memory.AnthropicSkillDrafter{Service: svc, APIKey: strings.TrimSpace(string(key)), WorkspaceID: os.Getenv("ARKISM_ANTHROPIC_WORKSPACE_ID"), BudgetScope: svc.ModelBudgetPolicy.Scope}
	return nil
}
func configureDaemonRouting(svc *memory.Service) {

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
