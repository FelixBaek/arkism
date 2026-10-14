package host

import (
	"encoding/json"
	"fmt"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// CrewError is a refusal with a Korean message that is safe to show (never a key or a prompt).
type CrewError struct{ Msg string }

func (e *CrewError) Error() string { return e.Msg }

// Crewf builds a *CrewError.
func Crewf(format string, args ...any) error { return &CrewError{fmt.Sprintf(format, args...)} }

// Crew runner settings shared with the gateway.
var (
	KeysFile        = Env("ARKISM_CREW_KEYS", filepath.Join(AgentsState, "provider-keys.json"))
	RunnerTokenFile = Env("ARKISM_CREW_TOKEN_FILE", filepath.Join(AgentsState, "crew-runner.token"))
	// PCProxy is the optional proxy for CLIs whose API refuses some regions (e.g. socks5://127.0.0.1:18080); "" runs them directly.
	PCProxy = os.Getenv("ARKISM_PC_PROXY")
	// APIMode lists CLIs that use a stored provider key instead of their own login: "codex,gemini", "all" or "".
	APIMode = splitSet(os.Getenv("ARKISM_CREW_API_MODE"))
)

// ModelsURL is each provider's model-list endpoint.
var ModelsURL = map[string]string{
	"anthropic": "https://api.anthropic.com/v1", "openai": "https://api.openai.com/v1", "xai": "https://api.x.ai/v1",
	"google": "https://generativelanguage.googleapis.com/v1beta", "zai": "https://api.z.ai/api/paas/v4",
}

// CLI is one row of the table that maps a model to its CLI.
type CLI struct {
	Name     string // "claude", "codex", ...
	Pattern  *regexp.Regexp
	BinEnv   string // env override for the binary
	BinName  string
	Provider string
	KeyEnv   string // env var that carries a stored key ("" = none)
	BaseEnv  string // env var that carries the provider address
	Effort   string // "flag", "config" or "" (no reasoning switch)
	Proxy    bool   // runs through PCProxy when it is set
}

// CLIS: order matters, the first pattern that matches wins (claude names are tested before
// the codex "o<digit>" names, so "opus" never reads as an OpenAI o-model).
var CLIS = []CLI{
	{"claude", regexp.MustCompile(`^(opus|sonnet|haiku|fable|claude-)`), "ARKISM_CLAUDE_BIN", "claude", "anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "flag", false},
	{"codex", regexp.MustCompile(`^(gpt-|o\d|codex)`), "ARKISM_CODEX_BIN", "codex", "openai", "OPENAI_API_KEY", "OPENAI_BASE_URL", "config", false},
	{"gemini", regexp.MustCompile(`^gemini`), "ARKISM_GEMINI_BIN", "gemini", "google", "GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL", "", false},
	{"glm", regexp.MustCompile(`^glm`), "ARKISM_GLM_BIN", "claude-glm", "zai", "ZAI_API_KEY", "ZAI_BASE_URL", "", false},
	{"grok", regexp.MustCompile(`^grok`), "ARKISM_GROK_BIN", "grok", "xai", "XAI_API_KEY", "XAI_BASE_URL", "flag", false},
	// antigravity models share names with Gemini and Claude ones, so they carry a prefix; its API
	// refuses some regions, so the run can go out through PCProxy.
	{"agy", regexp.MustCompile(`^antigravity-`), "ARKISM_AGY_BIN", "agy", "google", "", "", "flag", true},
}

// CustomCLI runs a 직접 추가 provider's models in Claude Code against its Anthropic-compatible endpoint.
var CustomCLI = CLI{Name: "custom", BinEnv: "ARKISM_CLAUDE_BIN", BinName: "claude", Provider: "custom",
	KeyEnv: "ANTHROPIC_AUTH_TOKEN", BaseEnv: "ANTHROPIC_BASE_URL"}

// CLIByName returns the CLIS row (or CustomCLI) for name.
func CLIByName(name string) (CLI, bool) {
	if name == CustomCLI.Name {
		return CustomCLI, true
	}
	for _, c := range CLIS {
		if c.Name == name {
			return c, true
		}
	}
	return CLI{}, false
}

// Bin is the CLI path: the env override, ~/.local/bin/<name>, or the CLI's own ~/.<name>/bin/<name>.
func Bin(env, name string) string {
	if v := os.Getenv(env); v != "" {
		v = expandUser(v)
		if p, err := exec.LookPath(v); err == nil {
			return p
		}
		return v
	}
	local, own := filepath.Join(Home, ".local/bin", name), filepath.Join(Home, "."+name, "bin", name)
	if exists(local) {
		return local
	}
	if exists(own) {
		return own
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return local
}

// AnthropicURL is a provider's Anthropic-compatible endpoint: <address>/anthropic, unless given.
func AnthropicURL(base string) string {
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	if strings.HasSuffix(base, "/anthropic") {
		return base
	}
	return base + "/anthropic"
}

var bareAlias = regexp.MustCompile(`^[a-z]+$`)
var spaceDots = regexp.MustCompile(`[\s.]+`)

// ClaudeModel is the id Claude Code takes for a display name: "opus 5.5" → claude-opus-5-5.
func ClaudeModel(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(name, "claude-") || bareAlias.MatchString(name) {
		return strings.TrimSpace(model)
	}
	return "claude-" + spaceDots.ReplaceAllString(name, "-")
}

// CustomPID is a 직접 추가 key's provider id: custom-<first 12 of the key id>.
func CustomPID(key map[string]any) string {
	id, _ := key["id"].(string)
	id = strings.TrimPrefix(id, "key_")
	if r := []rune(id); len(r) > 12 {
		id = string(r[:12])
	}
	return "custom-" + strings.ToLower(id)
}

// APIModeOn reports whether cli uses stored provider keys.
func APIModeOn(cli string) bool { return APIMode["all"] || APIMode[cli] }

// LoadKeys reads the provider keys file; it refuses a file others can read.
func LoadKeys() ([]map[string]any, error) {
	st, err := os.Stat(KeysFile)
	if err != nil {
		return nil, nil
	}
	// Windows keeps access in ACLs, not mode bits (every file reads as 0666 there).
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, Crewf("키 파일 권한이 너무 넓어요: chmod 600 %s", KeysFile)
	}
	raw, err := os.ReadFile(KeysFile)
	if err != nil {
		return nil, Crewf("키 파일을 읽지 못했어요")
	}
	var file struct {
		Keys []map[string]any `json:"keys"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&file) != nil {
		return nil, Crewf("키 파일을 읽지 못했어요")
	}
	// Rows whose secret moved to the OS credential store carry a secret_ref instead of the key.
	keystore.Resolve(file.Keys)
	return file.Keys, nil
}

func splitSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out[part] = true
		}
	}
	return out
}

func expandUser(p string) string {
	if p == "~" {
		return Home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(Home, p[2:])
	}
	return p
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
