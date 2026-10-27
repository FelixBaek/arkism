package memory

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Providers: the model settings screen. The host runner (`arkism crew`
// providers watch) reports which provider CLIs are logged in, their quota, the
// models each offers and the API keys it keeps; the owner turns models on or off
// here and asks for keys to be added, tested or removed.
//
// Keys never touch the database or disk on this side. A key the owner types is
// held in this process's memory only until the runner collects it (once, within
// providerKeyTTL); the runner writes it to its own 0600 file on the host and
// reports back the last four characters. Connection tests run on the host too,
// so memoryd makes no outbound calls with a key.
const (
	colProviders         = "providers"
	providerKeyTTL       = 10 * time.Minute
	providerCLIUpdateTTL = 20 * time.Minute
	// The runner's own permission: only it may collect keys waiting for it.
	actionProviderHandoff = "handoff:" + colProviders
)

var providerKinds = []string{"subscription", "api"}
var providerAccountStates = []string{"connected", "expired", "missing"}
var providerIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
var providerModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/ -]{0,79}$`)
var providerCLIVersionRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
var providerCLIVias = []string{"npm", "brew", "self"}
var providerRequestIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
var errProviderRequestExists = errors.New("provider request already exists")

type ProviderQuota struct {
	Window   string     `json:"window"` // e.g. "5h", "week"
	Model    string     `json:"model,omitempty"`
	LeftPct  *int       `json:"left_pct,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

type ProviderCredits struct {
	Balance   *float64 `json:"balance,omitempty"`
	Unlimited *bool    `json:"unlimited,omitempty"`
}

type ProviderCLIInfo struct {
	Version string `json:"version,omitempty"`
	Latest  string `json:"latest,omitempty"`
	Via     string `json:"via,omitempty"`
	Update  bool   `json:"update"`
}

type ProviderAccount struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	CLI     string         `json:"cli,omitempty"`
	Status  string         `json:"status"`
	Default bool           `json:"default,omitempty"`
	Quota   *ProviderQuota `json:"quota,omitempty"`
	// Every window the CLI reports (5시간·주간); Quota stays the first for older screens.
	Quotas         []ProviderQuota  `json:"quotas,omitempty"`
	Plan           string           `json:"plan,omitempty"`
	UsageStatus    string           `json:"usage_status,omitempty"`
	UsageCheckedAt *time.Time       `json:"usage_checked_at,omitempty"`
	Credits        *ProviderCredits `json:"credits,omitempty"`
	CLIInfo        *ProviderCLIInfo `json:"cli_info,omitempty"`
}

// ProviderKey is what the screen may know about a stored key: never the key.
type ProviderKey struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Last4            string    `json:"last4"`
	BaseURL          string    `json:"base_url,omitempty"`
	MonthlyBudgetUSD *float64  `json:"monthly_budget_usd,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type Provider struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	Custom   bool              `json:"custom,omitempty"`
	Accounts []ProviderAccount `json:"accounts"`
	Keys     []ProviderKey     `json:"keys"`
	Models   []string          `json:"models"`
}

type ProviderRequest struct {
	ID               string     `json:"id"`
	Kind             string     `json:"kind"`
	Provider         string     `json:"provider"`
	CLI              string     `json:"cli,omitempty"`
	ExpectedVersion  string     `json:"expected_version,omitempty"`
	ExpectedVia      string     `json:"expected_via,omitempty"`
	KeyID            string     `json:"key_id,omitempty"`
	Name             string     `json:"name,omitempty"`
	Last4            string     `json:"last4,omitempty"`
	BaseURL          string     `json:"base_url,omitempty"`
	MonthlyBudgetUSD *float64   `json:"monthly_budget_usd,omitempty"`
	Status           string     `json:"status"` // pending | done | failed | expired
	Error            string     `json:"error,omitempty"`
	Models           []string   `json:"models,omitempty"` // what a key test found
	Model            string     `json:"model,omitempty"`  // model_test: the model the runner tries
	Note             string     `json:"note,omitempty"`   // e.g. how long the test answer took
	RequestedBy      string     `json:"requested_by"`
	RequestedAt      time.Time  `json:"requested_at"`
	DoneAt           *time.Time `json:"done_at,omitempty"`
}

type Providers struct {
	CollectedAt *time.Time        `json:"collected_at,omitempty"`
	Providers   []Provider        `json:"providers"`
	Requests    []ProviderRequest `json:"requests"`
	// "provider/model" the owner turned off; kept across reports.
	Disabled []string `json:"disabled"`
	// "provider/model" the owner added by hand (모델 추가); listed with the reported ones.
	Extra []string `json:"extra,omitempty"`
	// The owner's names (연필): "account:<provider>/<id>" or "model:<provider>/<model>" -> name.
	Labels map[string]string `json:"labels,omitempty"`
	// The model that answers AI 도움 (fill·polish·classify…); empty means the runner's default.
	AssistModel string `json:"assist_model,omitempty"`
	// The provider the AI 도움 model was picked under: several providers offer the same model name.
	AssistProvider string `json:"assist_provider,omitempty"`
}

// ProviderAssistModel picks the AI 도움 model; an empty Model goes back to the runner's default.
type ProviderAssistModel struct {
	Scope    Scope  `json:"scope"`
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

// ProviderLabel names an account or a model on the screen; an empty Label goes back to the default.
type ProviderLabel struct {
	Scope    Scope  `json:"scope"`
	Provider string `json:"provider"`
	Kind     string `json:"kind"` // account | model
	ID       string `json:"id"`
	Label    string `json:"label"`
}

type ProviderModelSwitch struct {
	Scope    Scope  `json:"scope"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Enabled  bool   `json:"enabled"`
	// Add lists a model the runner did not report; Remove takes a hand-added one off the list.
	Add    bool `json:"add,omitempty"`
	Remove bool `json:"remove,omitempty"`
}

// ProviderKeyInput carries a key once, from the owner to this process's memory.
type ProviderKeyInput struct {
	Scope            Scope    `json:"scope"`
	Provider         string   `json:"provider"`
	Name             string   `json:"name"`
	Key              string   `json:"key"`
	BaseURL          string   `json:"base_url,omitempty"`
	MonthlyBudgetUSD *float64 `json:"monthly_budget_usd,omitempty"`
	TestOnly         bool     `json:"test_only,omitempty"`
}

type ProviderKeyRemoval struct {
	Scope    Scope  `json:"scope"`
	Provider string `json:"provider"`
	KeyID    string `json:"key_id"`
}

// ProviderHandoff is one request the runner must carry out, with the key when it has one.
type ProviderHandoff struct {
	Request ProviderRequest `json:"request"`
	Key     string          `json:"key,omitempty"`
}

type ProviderHandoffPage struct {
	Items []ProviderHandoff `json:"items"`
}

type ProviderResult struct {
	ID     string   `json:"id"`
	Status string   `json:"status"`
	Error  string   `json:"error,omitempty"`
	Models []string `json:"models,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// ProviderModelTest asks the runner to send one short prompt to a model through its CLI (연결 시험).
type ProviderModelTest struct {
	Scope    Scope  `json:"scope"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ProviderModelsRefresh asks the runner to reread every provider's model list now (모델 새로고침).
type ProviderModelsRefresh struct {
	Scope Scope `json:"scope"`
}

type ProviderCLIUpdate struct {
	Scope           Scope  `json:"scope"`
	Provider        string `json:"provider"`
	CLI             string `json:"cli"`
	RequestID       string `json:"request_id"`
	ExpectedVersion string `json:"expected_version"`
	ExpectedVia     string `json:"expected_via"`
}

type ProvidersReport struct {
	Scope Scope `json:"scope"`
	// Absent keeps the last inventory: a watch round may only settle requests.
	Providers *[]Provider      `json:"providers,omitempty"`
	Results   []ProviderResult `json:"results"`
}

type heldKey struct {
	scope Scope
	key   string
	until time.Time
}

// providerKeyHold keeps typed keys in memory only, keyed by request ID.
type providerKeyHold struct {
	mu   sync.Mutex
	keys map[string]heldKey
}

func (h *providerKeyHold) put(id string, scope Scope, key string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.keys == nil {
		h.keys = map[string]heldKey{}
	}
	for k, v := range h.keys {
		if now.After(v.until) {
			delete(h.keys, k)
		}
	}
	h.keys[id] = heldKey{scope: scope, key: key, until: now.Add(providerKeyTTL)}
}

// take hands a key out once; a second call, or one after the TTL, gets nothing.
func (h *providerKeyHold) take(id string, scope Scope, now time.Time) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.keys[id]
	if !ok || v.scope != scope {
		return "", false
	}
	delete(h.keys, id)
	return v.key, !now.After(v.until)
}

const providerRequestsKept = 100

func validProviderCLIVersion(v string) bool {
	return len(v) <= 60 && providerCLIVersionRe.MatchString(v)
}

func providerRequestExpired(q ProviderRequest, now time.Time) bool {
	ttl := providerKeyTTL
	if q.Kind == "cli_update" {
		ttl = providerCLIUpdateTTL
	}
	return q.Status == "pending" && q.Kind != "key_remove" && now.Sub(q.RequestedAt) > ttl
}

func validProviderKey(k string) bool {
	return len(k) >= 8 && len(k) <= 512 && strings.IndexFunc(k, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validProviderBaseURL(u string) bool {
	return u == "" || len(u) <= 200 && (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) && strings.IndexFunc(u, unicode.IsSpace) < 0
}

// validNewKeyBaseURL also refuses a plain-http Z.ai address on new keys (#171); stored keys keep
// validProviderBaseURL so an old record does not invalidate the settings, and the host ignores it.
func validNewKeyBaseURL(provider, u string) bool {
	return validProviderBaseURL(u) && (provider != "zai" || u == "" || strings.HasPrefix(u, "https://"))
}

func validProviderModels(models []string) bool {
	if len(models) > 300 {
		return false
	}
	for _, m := range models {
		if !providerModelRe.MatchString(m) {
			return false
		}
	}
	return true
}

func validProviders(list []Provider) bool {
	if len(list) > 40 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range list {
		if seen[p.ID] || !validProvider(p) {
			return false
		}
		seen[p.ID] = true
	}
	return true
}

func validProvider(p Provider) bool {
	if !providerIDRe.MatchString(p.ID) || !validRunLabel(p.Name, 60) || !slices.Contains(providerKinds, p.Kind) ||
		len(p.Accounts) > 20 || len(p.Keys) > 20 || !validProviderModels(p.Models) {
		return false
	}
	accounts := map[string]bool{}
	for _, a := range p.Accounts {
		if accounts[a.ID] || !validProviderAccount(a) {
			return false
		}
		accounts[a.ID] = true
	}
	for _, k := range p.Keys {
		if !validRunLabel(k.ID, 64) || !validRunLabel(k.Name, 60) || len(k.Last4) > 4 || !validProviderBaseURL(k.BaseURL) ||
			k.MonthlyBudgetUSD != nil && (*k.MonthlyBudgetUSD < 0 || *k.MonthlyBudgetUSD > 1e6) {
			return false
		}
	}
	return true
}

func validProviderAccount(a ProviderAccount) bool {
	if !validRunLabel(a.ID, 64) || !validRunLabel(a.Label, 120) || len(a.CLI) > 40 || !slices.Contains(providerAccountStates, a.Status) ||
		a.Plan != "" && (!validRunLabel(a.Plan, 240) || runes(a.Plan) > 60) ||
		a.UsageStatus != "" && !slices.Contains([]string{"ok", "unavailable", "expired"}, a.UsageStatus) {
		return false
	}
	if info := a.CLIInfo; info != nil && !validProviderCLIInfo(info) {
		return false
	}
	return validProviderAccountUsage(a) && validProviderQuotas(a)
}

func validProviderAccountUsage(a ProviderAccount) bool {
	if a.UsageCheckedAt != nil {
		_, offset := a.UsageCheckedAt.Zone()
		if a.UsageCheckedAt.IsZero() || a.UsageCheckedAt.Year() < 1 || a.UsageCheckedAt.Year() > 9999 || offset != 0 {
			return false
		}
	}
	if a.Credits != nil && a.Credits.Balance != nil {
		balance := *a.Credits.Balance
		if balance < 0 || math.IsNaN(balance) || math.IsInf(balance, 0) {
			return false
		}
	}
	return true
}

func validProviderCLIInfo(info *ProviderCLIInfo) bool {
	return !(info.Version != "" && !validProviderCLIVersion(info.Version) || info.Latest != "" && !validProviderCLIVersion(info.Latest) ||
		info.Via != "" && !slices.Contains(providerCLIVias, info.Via) ||
		info.Update && (info.Version == "" || info.Latest == "" || info.Via == ""))
}

func validProviderQuotas(a ProviderAccount) bool {
	qs := a.Quotas
	if a.Quota != nil {
		qs = append([]ProviderQuota{*a.Quota}, qs...)
	}
	if len(a.Quotas) > 12 {
		return false
	}
	for _, q := range qs {
		if !validRunLabel(q.Window, 20) || q.Model != "" && (!validRunLabel(q.Model, 40) || !providerModelRe.MatchString(q.Model)) ||
			q.LeftPct != nil && (*q.LeftPct < 0 || *q.LeftPct > 100) {
			return false
		}
	}
	return true
}

// Providers returns the scope's document, with key requests the runner never
// collected marked expired: their keys are gone from memory.
func (s *Service) Providers(ctx context.Context, token string, scope Scope) (Record, error) {
	if !validScope(scope) {
		return Record{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colProviders, scope, providersID(scope), ""); err != nil {
		return Record{}, err
	}
	r, err := s.Repo.Get(ctx, colProviders, scope, providersID(scope))
	if errors.Is(err, ErrNotFound) {
		return Record{ID: providersID(scope), Scope: scope, Providers: &Providers{Providers: []Provider{}, Requests: []ProviderRequest{}, Disabled: []string{}}}, nil
	}
	if err != nil || r.Providers == nil {
		return r, err
	}
	p := *r.Providers
	p.Requests = append([]ProviderRequest(nil), p.Requests...)
	now := time.Now()
	for i, q := range p.Requests {
		if providerRequestExpired(q, now) {
			p.Requests[i].Status = "expired"
		}
	}
	r.Providers = &p
	return r, nil
}

func (s *Service) updateProviders(ctx context.Context, scope Scope, change func(*Providers, time.Time) error, auditAction, actor string) (Record, error) {
	var out Record
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		r, err := tx.Get(ctx, colProviders, scope, providersID(scope))
		fresh := errors.Is(err, ErrNotFound)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			r = Record{ID: providersID(scope), Scope: scope, Kind: colProviders, Status: statusActive, Revision: 1, CreatedAt: now, Text: "model settings"}
		}
		p := Providers{Providers: []Provider{}, Disabled: []string{}}
		if r.Providers != nil {
			p = *r.Providers
			p.Requests = append([]ProviderRequest(nil), p.Requests...)
			p.Disabled = append([]string{}, p.Disabled...)
			p.Extra = append([]string{}, p.Extra...)
		}
		// Store every expiry the read view shows: a stale pending request is never trimmed, so 100 of them
		// (lost keys, unanswered model tests) would turn every new request into a conflict.
		for i, q := range p.Requests {
			if providerRequestExpired(q, now) {
				p.Requests[i].Status = "expired"
			}
		}
		if err := change(&p, now); err != nil {
			if err == errProviderRequestExists {
				out = r
				out.Providers = &p
				return nil
			}
			return err
		}
		if over := len(p.Requests) - providerRequestsKept; over > 0 {
			// Never discard work the runner still needs to collect or settle.
			p.Requests = slices.DeleteFunc(p.Requests, func(q ProviderRequest) bool {
				if over > 0 && q.Status != "pending" {
					over--
					return true
				}
				return false
			})
			if over > 0 {
				return ErrConflict
			}
		}
		r.Providers = &p
		r.UpdatedAt = now
		if auditAction != "" {
			if err := audit(ctx, tx, scope, actor, auditAction, providersID(scope)); err != nil {
				return err
			}
		}
		if fresh {
			out = r
			return tx.Insert(ctx, colProviders, r)
		}
		prev := r.Revision
		r.Revision++
		out = r
		return tx.Replace(ctx, colProviders, r, prev)
	})
	return out, err
}

func (s *Service) humanProviders(ctx context.Context, token string, scope Scope) (Decision, error) {
	d, err := s.authorize(ctx, token, "write:"+colProviders, scope, providersID(scope), "")
	if err != nil {
		return d, err
	}
	if d.Role != roleHuman {
		return d, ErrDenied
	}
	return d, nil
}

// SwitchProviderModel turns one model on or off for experts and teams.
func (s *Service) SwitchProviderModel(ctx context.Context, token string, in ProviderModelSwitch) (Record, error) {
	if !validScope(in.Scope) || !providerIDRe.MatchString(in.Provider) || !providerModelRe.MatchString(in.Model) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	if in.Add && in.Remove {
		return Record{}, ErrInvalid
	}
	key := in.Provider + "/" + in.Model
	return s.updateProviders(ctx, in.Scope, func(p *Providers, _ time.Time) error {
		p.Disabled = slices.DeleteFunc(p.Disabled, func(v string) bool { return v == key })
		if in.Remove {
			p.Extra = slices.DeleteFunc(p.Extra, func(v string) bool { return v == key })
			return nil
		}
		if in.Add && !slices.Contains(p.Extra, key) {
			if len(p.Extra) >= 100 {
				return ErrInvalid
			}
			p.Extra = append(p.Extra, key)
		}
		if !in.Enabled {
			p.Disabled = append(p.Disabled, key)
		}
		return nil
	}, "", d.PrincipalID)
}

// AddProviderKey holds a typed key in memory for the runner, which stores it
// (or, with TestOnly, only tries it) on the host. The record keeps the last four.
func (s *Service) AddProviderKey(ctx context.Context, token string, in ProviderKeyInput) (Record, error) {
	in.Key = strings.TrimSpace(in.Key)
	if !validScope(in.Scope) || !providerIDRe.MatchString(in.Provider) || !validProviderKey(in.Key) || !validNewKeyBaseURL(in.Provider, in.BaseURL) ||
		!in.TestOnly && !validRunLabel(in.Name, 60) || in.TestOnly && in.Name != "" && !validRunLabel(in.Name, 60) ||
		in.MonthlyBudgetUSD != nil && (*in.MonthlyBudgetUSD < 0 || *in.MonthlyBudgetUSD > 1e6) {
		return Record{}, ErrInvalid
	}
	// The key may look like a secret; the name and address must not.
	if containsSecret(in.Name) || containsSecret(in.BaseURL) {
		return Record{}, ErrSecretDetected
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	kind, action := "key_add", "add:provider_key"
	if in.TestOnly {
		kind, action = "key_test", "test:provider_key"
	}
	req := ProviderRequest{ID: ID(), Kind: kind, Provider: in.Provider, Name: in.Name, Last4: in.Key[len(in.Key)-4:], BaseURL: in.BaseURL,
		MonthlyBudgetUSD: in.MonthlyBudgetUSD, Status: "pending", RequestedBy: d.PrincipalID}
	if !in.TestOnly {
		req.KeyID = "key_" + ID()
	}
	out, err := s.updateProviders(ctx, in.Scope, func(p *Providers, now time.Time) error {
		req.RequestedAt = now
		p.Requests = append(p.Requests, req)
		return nil
	}, action, d.PrincipalID)
	if err == nil {
		s.providerKeys.put(req.ID, in.Scope, in.Key, time.Now())
	}
	return out, err
}

// TestProviderModel queues a model test; no key travels, the runner uses what the host already has.
func (s *Service) TestProviderModel(ctx context.Context, token string, in ProviderModelTest) (Record, error) {
	if !validScope(in.Scope) || !providerIDRe.MatchString(in.Provider) || !providerModelRe.MatchString(in.Model) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	return s.updateProviders(ctx, in.Scope, func(p *Providers, now time.Time) error {
		p.Requests = append(p.Requests, ProviderRequest{ID: ID(), Kind: "model_test", Provider: in.Provider, Model: in.Model,
			Status: "pending", RequestedBy: d.PrincipalID, RequestedAt: now})
		return nil
	}, "", d.PrincipalID)
}

// RefreshProviderModels queues one model-list reread on the host; a pending one is not doubled.
func (s *Service) RefreshProviderModels(ctx context.Context, token string, in ProviderModelsRefresh) (Record, error) {
	if !validScope(in.Scope) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	return s.updateProviders(ctx, in.Scope, func(p *Providers, now time.Time) error {
		for _, q := range p.Requests {
			if q.Kind == "models_refresh" && q.Status == "pending" && !providerRequestExpired(q, now) {
				return ErrConflict
			}
		}
		p.Requests = append(p.Requests, ProviderRequest{ID: ID(), Kind: "models_refresh", Provider: "all",
			Status: "pending", RequestedBy: d.PrincipalID, RequestedAt: now})
		return nil
	}, "", d.PrincipalID)
}

// UpdateProviderCLI queues a host-only update of the reported installation.
func (s *Service) UpdateProviderCLI(ctx context.Context, token string, in ProviderCLIUpdate) (Record, error) {
	if !validScope(in.Scope) || !(in.Provider == "anthropic" && in.CLI == "claude" || in.Provider == "openai" && in.CLI == "codex") ||
		!providerRequestIDRe.MatchString(in.RequestID) || !validProviderCLIVersion(in.ExpectedVersion) || !slices.Contains(providerCLIVias, in.ExpectedVia) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in); err != nil {
		return Record{}, err
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	return s.updateProviders(ctx, in.Scope, func(p *Providers, now time.Time) error {
		for _, q := range p.Requests {
			if q.ID == in.RequestID {
				if q.Kind == "cli_update" && q.Provider == in.Provider && q.CLI == in.CLI &&
					q.ExpectedVersion == in.ExpectedVersion && q.ExpectedVia == in.ExpectedVia {
					return errProviderRequestExists
				}
				return ErrConflict
			}
		}
		for _, q := range p.Requests {
			if q.Kind == "cli_update" && q.CLI == in.CLI && q.Status == "pending" {
				return ErrConflict
			}
		}
		if !providerCLIUpdateMatches(p, in) {
			return ErrConflict
		}
		p.Requests = append(p.Requests, ProviderRequest{ID: in.RequestID, Kind: "cli_update", Provider: in.Provider,
			CLI: in.CLI, ExpectedVersion: in.ExpectedVersion, ExpectedVia: in.ExpectedVia,
			Status: "pending", RequestedBy: d.PrincipalID, RequestedAt: now})
		return nil
	}, "update:provider_cli", d.PrincipalID)
}

// providerCLIUpdateMatches reports whether a reported installation offers this update.
func providerCLIUpdateMatches(p *Providers, in ProviderCLIUpdate) bool {
	matched := false
	for _, provider := range p.Providers {
		if provider.ID != in.Provider {
			continue
		}
		for _, a := range provider.Accounts {
			if a.CLI == in.CLI && a.CLIInfo != nil && a.CLIInfo.Update &&
				a.CLIInfo.Version == in.ExpectedVersion && a.CLIInfo.Via == in.ExpectedVia {
				matched = true
			}
		}
	}
	return matched
}

func validProviderLabel(in ProviderLabel) bool {
	idOK := in.Kind == "account" && providerIDRe.MatchString(in.ID) || in.Kind == "model" && providerModelRe.MatchString(in.ID)
	return validScope(in.Scope) && providerIDRe.MatchString(in.Provider) && idOK && (in.Label == "" || validRunLabel(in.Label, 120) && runes(in.Label) <= 40)
}

// LabelProvider stores or clears the owner's name for an account or a model.
func (s *Service) LabelProvider(ctx context.Context, token string, in ProviderLabel) (Record, error) {
	if !validProviderLabel(in) {
		return Record{}, ErrInvalid
	}
	if containsSecret(in.Label) {
		return Record{}, ErrSecretDetected
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	key := in.Kind + ":" + in.Provider + "/" + in.ID
	return s.updateProviders(ctx, in.Scope, func(p *Providers, _ time.Time) error {
		labels := map[string]string{}
		for k, v := range p.Labels {
			labels[k] = v
		}
		if in.Label == "" {
			delete(labels, key)
		} else if _, ok := labels[key]; ok || len(labels) < 300 {
			labels[key] = in.Label
		} else {
			return ErrInvalid
		}
		p.Labels = labels
		return nil
	}, "", d.PrincipalID)
}

// SetAssistModel stores which model answers AI 도움 requests.
func (s *Service) SetAssistModel(ctx context.Context, token string, in ProviderAssistModel) (Record, error) {
	if !validScope(in.Scope) || in.Model != "" && !providerModelRe.MatchString(in.Model) || in.Provider != "" && (in.Model == "" || !providerIDRe.MatchString(in.Provider)) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	return s.updateProviders(ctx, in.Scope, func(p *Providers, _ time.Time) error {
		p.AssistModel, p.AssistProvider = in.Model, in.Provider
		return nil
	}, "set:assist_model", d.PrincipalID)
}

// RemoveProviderKey asks the runner to delete a stored key.
func (s *Service) RemoveProviderKey(ctx context.Context, token string, in ProviderKeyRemoval) (Record, error) {
	if !validScope(in.Scope) || !providerIDRe.MatchString(in.Provider) || !validRunLabel(in.KeyID, 64) {
		return Record{}, ErrInvalid
	}
	d, err := s.humanProviders(ctx, token, in.Scope)
	if err != nil {
		return Record{}, err
	}
	return s.updateProviders(ctx, in.Scope, func(p *Providers, now time.Time) error {
		p.Requests = append(p.Requests, ProviderRequest{ID: ID(), Kind: "key_remove", Provider: in.Provider, KeyID: in.KeyID,
			Status: "pending", RequestedBy: d.PrincipalID, RequestedAt: now})
		return nil
	}, "remove:provider_key", d.PrincipalID)
}

// ProviderHandoff gives the runner the pending requests, each held key once.
func (s *Service) ProviderHandoff(ctx context.Context, token string, scope Scope) (ProviderHandoffPage, error) {
	if !validScope(scope) {
		return ProviderHandoffPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, actionProviderHandoff, scope, providersID(scope), ""); err != nil {
		return ProviderHandoffPage{}, err
	}
	r, err := s.Repo.Get(ctx, colProviders, scope, providersID(scope))
	if errors.Is(err, ErrNotFound) {
		return ProviderHandoffPage{Items: []ProviderHandoff{}}, nil
	}
	if err != nil {
		return ProviderHandoffPage{}, err
	}
	page := ProviderHandoffPage{Items: []ProviderHandoff{}}
	now := time.Now()
	for _, q := range r.Providers.Requests {
		if q.Status != "pending" {
			continue
		}
		// An expired request already reads as expired, and a refresh may have a newer one queued.
		if providerRequestExpired(q, now) {
			continue
		}
		if q.Kind == "key_remove" || q.Kind == "model_test" || q.Kind == "cli_update" || q.Kind == "models_refresh" {
			page.Items = append(page.Items, ProviderHandoff{Request: q})
			continue
		}
		if key, ok := s.providerKeys.take(q.ID, scope, now); ok {
			page.Items = append(page.Items, ProviderHandoff{Request: q, Key: key})
		}
	}
	return page, nil
}

func validProvidersReport(in ProvidersReport) bool {
	if !validScope(in.Scope) || in.Providers != nil && !validProviders(*in.Providers) || len(in.Results) > providerRequestsKept {
		return false
	}
	for _, res := range in.Results {
		if res.ID == "" || res.Status != "done" && res.Status != "failed" || len(res.Error) > 300 || runes(res.Note) > 120 || !validProviderModels(res.Models) {
			return false
		}
	}
	return true
}

// ReportProviders records what the runner sees and the outcome of requests it carried out.
func (s *Service) ReportProviders(ctx context.Context, token string, in ProvidersReport) (Record, error) {
	if !validProvidersReport(in) {
		return Record{}, ErrInvalid
	}
	if err := noSecrets(in); err != nil {
		return Record{}, err
	}
	if _, err := s.authorize(ctx, token, actionProviderHandoff, in.Scope, providersID(in.Scope), ""); err != nil {
		return Record{}, err
	}
	return s.updateProviders(ctx, in.Scope, func(p *Providers, now time.Time) error {
		if in.Providers != nil {
			p.Providers, p.CollectedAt = *in.Providers, &now
		}
		index := map[string]ProviderResult{}
		for _, res := range in.Results {
			index[res.ID] = res
		}
		for i, q := range p.Requests {
			if res, ok := index[q.ID]; ok && q.Status == "pending" {
				p.Requests[i].Status, p.Requests[i].Error, p.Requests[i].Models, p.Requests[i].Note, p.Requests[i].DoneAt = res.Status, res.Error, res.Models, res.Note, &now
			}
		}
		return nil
	}, "", "")
}

func providersRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/providers", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.Providers(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers", func(w http.ResponseWriter, r *http.Request) {
		var in ProvidersReport
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReportProviders(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/providers/handoff", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.ProviderHandoff(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/models", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderModelSwitch
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SwitchProviderModel(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/keys", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderKeyInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.AddProviderKey(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/labels", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderLabel
		if !decode(w, r, &in) {
			return
		}
		out, e := s.LabelProvider(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/test", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderModelTest
		if !decode(w, r, &in) {
			return
		}
		out, e := s.TestProviderModel(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/models_refresh", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderModelsRefresh
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RefreshProviderModels(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/cli_update", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderCLIUpdate
		if !decode(w, r, &in) {
			return
		}
		out, e := s.UpdateProviderCLI(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/assist_model", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderAssistModel
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SetAssistModel(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/providers/keys/remove", func(w http.ResponseWriter, r *http.Request) {
		var in ProviderKeyRemoval
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RemoveProviderKey(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

// providersID is the one model-settings record of a scope; record IDs are
// unique across scopes, so it is derived from the scope.
func providersID(scope Scope) string { return "providers-" + ContentHash(structuredText(scope)) }
