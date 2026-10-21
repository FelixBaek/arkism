package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// Deterministic public fixtures, never production credentials. Keep the real
// provisioning policy enabled in integration tests rather than adding a bypass.
func testPrincipalToken(label string) string {
	sum := sha256.Sum256([]byte("arkism-public-test-token:" + label))
	return hex.EncodeToString(sum[:])
}

func TestPrincipalTokenStrengthAndGeneration(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, err := NewPrincipalToken()
		if err != nil || !ValidPrincipalToken(token) || seen[token] {
			t.Fatal("generator returned invalid or duplicate token")
		}
		seen[token] = true
	}
	for _, token := range []string{"", "short", strings.Repeat("a", 32), strings.Repeat("a", 43), strings.Repeat("00", 32), strings.Repeat("ab", 32), testPrincipalToken("x") + "\n", " " + testPrincipalToken("x"), strings.Repeat("x", 4097)} {
		if ValidPrincipalToken(token) {
			t.Fatal("weak or malformed token accepted")
		}
	}
	if !ValidPrincipalToken(testPrincipalToken("hex")) {
		t.Fatal("256-bit hex format rejected")
	}
	for _, ttl := range []time.Duration{-1, time.Second, MaxPrincipalTokenTTL + 1} {
		if _, err := principalTokenTTL(ttl); err == nil {
			t.Fatal("invalid lifetime accepted")
		}
	}
	if got, err := principalTokenTTL(0); err != nil || got != MaxPrincipalTokenTTL {
		t.Fatal("default lifetime invalid")
	}
}
