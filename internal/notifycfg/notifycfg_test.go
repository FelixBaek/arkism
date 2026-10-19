package notifycfg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tok  = "123456:AAH3abcdefghijklmnopqrstuvwxyz"
	hook = "https://hooks.slack.com/services/T000/B000/abcdefghijklmnopqrst"
)

func TestSaveListMaskRemoveKeepsOthers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notify.json")
	_ = os.WriteFile(p, []byte(`{"email":{"default":{"host":"smtp.x"}}}`), 0o600)
	if err := Save(p, "telegram", "", map[string]string{"bot_token": tok, "chat_id": "-100123"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, "slack", "", map[string]string{"webhook_url": hook}); err != nil {
		t.Fatal(err)
	}
	l, _ := List(p)
	raw, _ := json.Marshal(l)
	if len(l) != 2 || l[1].Last4 != "wxyz" || l[1].ChatID != "-100123" || strings.Contains(string(raw), "AAH3") || strings.Contains(string(raw), "hooks.slack") {
		t.Fatalf("list leaked or wrong: %s", raw)
	}
	// Changing only the chat keeps the stored token.
	if err := Save(p, "telegram", "", map[string]string{"chat_id": "@felixchat"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(p, "slack", "default"); err != nil {
		t.Fatal(err)
	}
	disk, _ := os.ReadFile(p)
	if !strings.Contains(string(disk), "smtp.x") || !strings.Contains(string(disk), tok) || strings.Contains(string(disk), "hooks.slack") {
		t.Fatalf("file after edits: %s", disk)
	}
	if st, _ := os.Stat(p); st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "notify.json")
	for _, c := range []struct {
		ch string
		f  map[string]string
	}{
		{"telegram", map[string]string{"bot_token": "nope", "chat_id": "-100123"}},
		{"telegram", map[string]string{"bot_token": tok, "chat_id": "x y"}},
		{"telegram", map[string]string{"chat_id": "-100123"}}, // no stored token
		{"slack", map[string]string{"webhook_url": "http://evil.example/hook"}},
		{"email", nil},
	} {
		if err := Save(p, c.ch, "", c.f); err != ErrInvalid {
			t.Errorf("%s %v: %v", c.ch, c.f, err)
		}
	}
	if Save(p, "slack", "Bad Name", map[string]string{"webhook_url": hook}) != ErrInvalid {
		t.Error("name accepted")
	}
}

func TestTestSendsAndHidesSecrets(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = r.URL.Path + " " + string(b)
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	old := TelegramAPI
	TelegramAPI = srv.URL
	defer func() { TelegramAPI = old }()
	p := filepath.Join(t.TempDir(), "notify.json")
	_ = Save(p, "telegram", "", map[string]string{"bot_token": tok, "chat_id": "-100123"})
	if err := Test(context.Background(), p, "telegram", "default"); err != nil || !strings.Contains(got, "/bot"+tok+"/sendMessage") || !strings.Contains(got, "-100123") {
		t.Fatalf("err=%v got=%s", err, got)
	}
	_ = Save(p, "telegram", "bad", map[string]string{"bot_token": "123456:badbadbadbadbadbadbadbad", "chat_id": "-100123"})
	err := Test(context.Background(), p, "telegram", "bad")
	if err == nil || strings.Contains(err.Error(), "badbad") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err=%v", err)
	}
	if Test(context.Background(), p, "slack", "default") != ErrInvalid {
		t.Error("missing dest should be invalid")
	}
}
