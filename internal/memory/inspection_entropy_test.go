package memory

import (
	"encoding/base64"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
)

// Synthetic token formats: values are generated per run, so no key-shaped
// literal is committed. Names describe the shape, not a vendor.
var entropyFormats = []struct {
	name, prefix, alphabet string
	length                 int
}{
	{"cloud secret access key, 40 base64", "", b64Alphabet, 40},
	{"live secret key, prefix + 24 base62", "key_live_", b62Alphabet, 24},
	{"webhook signing secret, prefix + 32 base64", "whk_", b64Alphabet, 32},
	{"registry token, prefix + 36 base62", "reg_", b62Alphabet, 36},
	{"generic api key, 32 base62", "", b62Alphabet, 32},
	{"generic api key, 48 base62", "", b62Alphabet, 48},
	{"32-byte base64url token", "", b64URLAlphabet, 43},
	{"64-byte base64 secret", "", b64Alphabet, 86},
	{"session secret, 64 base62", "", b62Alphabet, 64},
	{"lowercase + digit key, 40", "", "abcdefghijklmnopqrstuvwxyz0123456789", 40},
	{"uppercase base32 seed, 32", "", "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 32},
	{"token with an unnamed prefix, 4 + 43", "hub_", b62Alphabet, 43},
	{"short prefix + 40 base62", "dsh", b62Alphabet, 40},
}

const (
	b62Alphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b64Alphabet    = b62Alphabet + "+/"
	b64URLAlphabet = b62Alphabet + "-_"
)

func randomToken(r *rand.Rand, prefix, alphabet string, n int) string {
	b := []byte(prefix)
	for range n {
		b = append(b, alphabet[r.IntN(len(alphabet))])
	}
	return string(b)
}

// Each format is caught at least 95% of the time on its own and inside Korean,
// English and JSON text; the printed miss count is the measured FN rate (a
// 24-character random part is the weakest, about 4%; 40+ characters under 1%).
func TestEntropyDetectorCatchesUnnamedTokenFormats(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, f := range entropyFormats {
		missed := 0
		const runs = 2000
		for i := range runs {
			tok := randomToken(r, f.prefix, f.alphabet, f.length)
			text := []string{tok, "배포 키는 " + tok + " 입니다", "use " + tok + " for the deploy", `{"value":"` + tok + `"}`}[i%4]
			if !containsSecret(text) {
				missed++
			}
		}
		t.Logf("%-45s missed %d/%d", f.name, missed, runs)
		if missed*100 > runs*5 {
			t.Errorf("%s: missed %d of %d", f.name, missed, runs)
		}
	}
}

func TestEntropyDetectorKeepsHashesIdsImagesAndText(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 600)...)
	for i := range png[16:] {
		png[16+i] = byte(r.IntN(256))
	}
	safe := []string{
		"commit " + randomToken(r, "", "0123456789abcdef", 40) + " fixed it",
		"sha256 " + randomToken(r, "", "0123456789abcdef", 64),
		"id 550e8400-e29b-41d4-a716-446655440000 and " + strings.ToUpper("550e8400-e29b-41d4-a716-446655440000"),
		"source-" + ID() + " artifact-" + ContentHash("x") + " chatcmpl_" + ID(),
		"mcp__7e4b0024-e1dd-4a2b-9c3d-0123456789ab__search_files",
		"rollout-2026-11-01T19-27-58-01a196a6-d46c-78d3-bb96-af0a534454b6",
		"SHA" + ContentHash("y") + " revision",
		"![chart](data:image/png;base64," + base64.StdEncoding.EncodeToString(png[:200]) + ")",
		base64.StdEncoding.EncodeToString(png),
		"integrity sha512-" + base64.StdEncoding.EncodeToString(png[16:80]),
		"ssh-ed25519 " + base64.StdEncoding.EncodeToString(append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00 "), png[16:48]...)) + " user@host",
		base64.StdEncoding.EncodeToString([]byte(`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[]}`)),
		"diff-" + base64.RawURLEncoding.EncodeToString([]byte("internal/host/crew/keys.go")),
		"TestPolicyP1OldRefCannotFillMissingNewRef ICoreWebView2NewWindowRequestedEventArgs2",
		"isboundedby64MiBrawand32depth fromacceptedDYN05registry coreHTTPPostV1SkillTrialsIdClaimCheck",
		"2026-10-30-roadmap-capability-evidence-fixtures exp01_REF_semantic_sha256 16lowerhexIEEE754binary64finite",
		"/usr/lib/x86_64-linux-gnu/libLLVM-17.so.1 https://example.com/a/b/c?page=2&sort=created_at",
		"사용자는 한국어로 짧게 보고받기를 원한다. 배포는 매주 화요일 오후 3시에 한다. 버전 0.1.7, 스키마 5.",
		strings.Repeat("기억 저장과 검색 품질을 확인한다 ", 200),
		"The API key is stored in ~/.config/arkism/anthropic-api.key and never printed.",
	}
	for _, text := range safe {
		if containsSecret(text) {
			t.Errorf("false positive: %.120q", text)
		}
	}
}

func TestSecretReferencesAllowedValuesBlocked(t *testing.T) {
	for _, text := range []string{
		"token: ~/.config/arkism/token",
		"api_key=/run/secrets/api_key",
		"API_KEY=$ARKISM_API_KEY",
		"password=${DB_PASSWORD}",
		`{"api_key": "<your-key>"}`,
		"secret=%APP_SECRET%",
		"password: ****",
		"비밀번호: ~/.secrets/db.txt",
		"access_token = ./local/token.txt",
	} {
		if containsSecret(text) {
			t.Errorf("reference blocked: %q", text)
		}
	}
	for _, text := range []string{
		"token: abcd1234efgh", `api_key="realvalue123"`, "password: hunter2", "비밀번호: 아주짧음",
		`{\"token\":\"plainvalue\"}`, "secret='x'", `password: "unterminated`, `{"password":{}}`, "token=[REDACTED]",
	} {
		if !containsSecret(text) {
			t.Errorf("value allowed: %q", text)
		}
	}
}

func TestRedactSecretsReplacesEntropyTokensOnly(t *testing.T) {
	tok := randomToken(rand.New(rand.NewPCG(5, 6)), "", b62Alphabet, 40)
	got := redactSecrets("배포 키 " + tok + " 와 커밋 " + ContentHash("z") + ", password: hunter2")
	if got != "배포 키 [REDACTED] 와 커밋 "+ContentHash("z")+", [REDACTED]" {
		t.Fatalf("%q", got)
	}
	f := newFixture(t)
	r, e := f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "key " + tok, OnSecret: "redact"}))
	if e != nil || r.Text != "key [REDACTED]" || !r.Inspection.Redacted || r.Inspection.RulesVersion != inspectionVersion {
		t.Fatalf("redaction: %v %q", e, r.Text)
	}
	if _, e = f.svc.Create(testCtx, f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "key " + tok})); !errors.Is(e, ErrSecretDetected) {
		t.Fatal("entropy token stored", e)
	}
	if safeTransfer("token: ~/.config/x/token") == false || safeTransfer("key "+tok) {
		t.Fatal("transfer verdict")
	}
}
