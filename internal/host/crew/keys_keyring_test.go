package crew

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
	"github.com/FelixBaek/arkism/internal/memory"
)

// Every crew test runs against an unavailable store (the server's file behaviour) unless it opts in, so no test
// ever touches the real OS credential store.
func init() { keystore.UseMock(errors.New("no credential store")) }

func useKeyring(t *testing.T, err error) {
	t.Helper()
	keystore.UseMock(err)
	_ = os.Remove(host.KeysFile)
	t.Cleanup(func() {
		_ = os.Remove(host.KeysFile)
		keystore.UseMock(errors.New("no credential store"))
	})
}

func keysFileText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(host.KeysFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestKeyringAddStoresSecretOutsideFile(t *testing.T) {
	useKeyring(t, nil)
	secret := "sk-ring-0123456789abcdef"
	pub, err := addKey("openai", "업무", secret, "", nil, "key_ring00000001")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := pub.Get("storage"); v != "keyring" {
		t.Fatal(pub)
	}
	text := keysFileText(t)
	if strings.Contains(text, secret) || !strings.Contains(text, `"secret_ref": "keyring:`+keystore.Service+`/key_ring00000001"`) ||
		!strings.Contains(text, `"storage": "keyring"`) {
		t.Fatal(text)
	}
	if v, err := keystore.Get(keystore.Ref("key_ring00000001")); err != nil || v != secret {
		t.Fatal(v, err)
	}
	keys, err := loadKeys()
	if err != nil || len(keys) != 1 || keys[0]["key"] != secret {
		t.Fatal(keys, err)
	}
	if v, _ := publicKey(keys[0]).Get("last4"); v != "cdef" {
		t.Fatal(v)
	}
}

func TestKeyringMigratesPlaintextRow(t *testing.T) {
	useKeyring(t, nil)
	secret := "sk-plain-0123456789abcdef"
	if err := os.WriteFile(host.KeysFile, []byte(`{"keys":[{"id":"key_plain0000001","provider":"openai","name":"a","key":"`+secret+`"},`+
		`{"provider":"google","key":"no-id-row-secret"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := loadKeys()
	if err != nil || len(keys) != 2 || keys[0]["key"] != secret || keys[0]["storage"] != "keyring" {
		t.Fatal(keys, err)
	}
	text := keysFileText(t)
	if strings.Contains(text, secret) || !strings.Contains(text, keystore.Ref("key_plain0000001")) {
		t.Fatal(text)
	}
	if !strings.Contains(text, "no-id-row-secret") { // a row without an id has nothing to file it under: it stays
		t.Fatal(text)
	}
	if st, _ := os.Stat(host.KeysFile); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows keeps access in ACLs
		t.Fatal(st.Mode())
	}
	again, err := loadKeys()
	if err != nil || again[0]["key"] != secret {
		t.Fatal(again, err)
	}
}

func TestKeyringUnavailableFallsBackToFile(t *testing.T) {
	useKeyring(t, errors.New("secret service down"))
	secret := "sk-file-0123456789abcdef"
	pub, err := addKey("anthropic", "a", secret, "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := pub.Get("storage"); v != "file" {
		t.Fatal(pub)
	}
	text := keysFileText(t)
	if !strings.Contains(text, secret) || !strings.Contains(text, `"storage": "file"`) || strings.Contains(text, "secret_ref") {
		t.Fatal(text)
	}
	keys, err := loadKeys()
	if err != nil || keys[0]["key"] != secret || keys[0]["storage"] != "file" {
		t.Fatal(keys, err)
	}
	if text2 := keysFileText(t); text2 != text { // loading must not rewrite the file when nothing moved
		t.Fatal(text2)
	}
}

func TestKeyringUnresolvableRefKeepsPlaintext(t *testing.T) {
	useKeyring(t, nil)
	secret := "sk-both-0123456789abcdef"
	os.WriteFile(host.KeysFile, []byte(`{"keys":[{"id":"key_both00000001","provider":"openai","name":"a","key":"`+secret+
		`","secret_ref":"keyring:`+keystore.Service+`/key_missing"}]}`), 0o600)
	keys, err := loadKeys()
	if err != nil || keys[0]["key"] != secret {
		t.Fatal(keys, err)
	}
	if err := removeKey("nope"); err == nil {
		t.Fatal("removed a missing key")
	}
	keys, _ = loadKeys()
	if keys[0]["key"] != secret {
		t.Fatal(keys)
	}
}

func TestKeyringRemoveDeletesBoth(t *testing.T) {
	useKeyring(t, nil)
	if _, err := addKey("openai", "a", "sk-gone-0123456789abcdef", "", nil, "key_gone00000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := addKey("openai", "b", "sk-stay-0123456789abcdef", "", nil, "key_stay00000001"); err != nil {
		t.Fatal(err)
	}
	if err := removeKey("key_gone00000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := keystore.Get(keystore.Ref("key_gone00000001")); !errors.Is(err, keystore.ErrNotFound) {
		t.Fatal(err)
	}
	if strings.Contains(keysFileText(t), "key_gone00000001") {
		t.Fatal(keysFileText(t))
	}
	keys, _ := loadKeys()
	if len(keys) != 1 || keys[0]["key"] != "sk-stay-0123456789abcdef" {
		t.Fatal(keys)
	}
}

func TestKeystoreParseRef(t *testing.T) {
	if s, id, ok := keystore.ParseRef("keyring:svc/key_1"); !ok || s != "svc" || id != "key_1" {
		t.Fatal(s, id, ok)
	}
	for _, bad := range []string{"", "keyring:", "keyring:svc", "keyring:/id", "file:svc/id"} {
		if _, _, ok := keystore.ParseRef(bad); ok {
			t.Fatal(bad)
		}
	}
}

func TestKeyRowDecodesAsServerProviderKey(t *testing.T) {
	pub := host.Obj{{Key: "id", Value: "k1"}, {Key: "name", Value: "main"}, {Key: "last4", Value: "abcd"},
		{Key: "storage", Value: "keyring"}, {Key: "created_at", Value: "2026-10-15T00:00:00Z"}}
	raw, err := json.Marshal(keyRow(pub))
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var k memory.ProviderKey
	if err := d.Decode(&k); err != nil {
		t.Fatalf("server would reject the inventory: %v", err)
	}
}

// A custom endpoint saved without a key keeps no key and sends no Authorization; its extra headers are stored like a
// key (credential store when one answers, else the 0600 file), shown by name only, and sent on every provider call.
func TestKeylessEndpointWithHeaders(t *testing.T) {
	for _, ring := range []bool{true, false} {
		t.Run(map[bool]string{true: "keyring", false: "file"}[ring], func(t *testing.T) { keylessEndpoint(t, ring) })
	}
}

func keylessEndpoint(t *testing.T, ring bool) {
	keyFiles(t)
	if ring {
		useKeyring(t, nil)
	}
	const secret = "gateway-secret-value-123"
	var seen []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.Write([]byte(`{"data":[{"id":"qwen3"}]}`))
	}))
	res, err := carryOut(map[string]any{"request": map[string]any{"id": "r1", "kind": "key_add", "provider": "custom", "name": "집",
		"key_id": "key_local0000001", "base_url": up.URL + "/v1"}, "headers": map[string]any{"X-Gateway-Token": secret}}, nil, nil)
	if err != nil || res["status"] != "done" {
		t.Fatal(ring, res, err)
	}
	if text := keysFileText(t); ring == strings.Contains(text, secret) || strings.Contains(text, "secret_ref") {
		t.Fatal(ring, text)
	}
	keys, _ := loadKeys()
	pub := keyRow(publicKey(keys[0]))
	if raw, _ := json.Marshal(pub); strings.Contains(string(raw), secret) || !strings.Contains(string(raw), `"header_names":["X-Gateway-Token"]`) ||
		!strings.Contains(string(raw), `"last4":""`) {
		t.Fatal(string(raw))
	}
	if k, err := customKeyFor("qwen3", keys, host.CustomPID(keys[0])); err != nil || k == nil {
		t.Fatal("a keyless endpoint must still serve its models", k, err)
	}
	if len(seen) == 0 {
		t.Fatal("no /models call")
	}
	for _, h := range seen {
		if h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" || h.Get("X-Gateway-Token") != secret {
			t.Fatal(ring, h)
		}
	}
	env, err := customEnv(map[string]string{"ANTHROPIC_CUSTOM_HEADERS": "X-Old: leak"}, "qwen3", keys[0])
	if err != nil || env["ANTHROPIC_CUSTOM_HEADERS"] != "X-Gateway-Token: "+secret || env["ANTHROPIC_AUTH_TOKEN"] != "no-key" {
		t.Fatal(env, err)
	}
	if out := redact("echo "+secret, env); strings.Contains(out, secret) {
		t.Fatal(out)
	}
	if err := removeKey("key_local0000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := keystore.Get(keystore.Ref("key_local0000001.headers")); ring && !errors.Is(err, keystore.ErrNotFound) {
		t.Fatal("header secret left behind", err)
	}
	up.Close()
}
