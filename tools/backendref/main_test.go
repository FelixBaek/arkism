package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func call(t *testing.T, srv *httptest.Server, method, path, key, body string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tk")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out
}

func TestReferenceRunner(t *testing.T) {
	r := &runner{token: "tk", delay: 50 * time.Millisecond, caps: map[string]bool{"status": true, "cancel": true, "idempotency": true}, tasks: map[string]*task{}, byKey: map[string]string{}}
	srv := httptest.NewServer(r)
	defer srv.Close()
	body := `{"protocol":"arkism.backend.v1","task":"hi","access":{"mode":"read"},"memory":{"items":[{"id":"m1"}]}}`
	a := call(t, srv, "POST", "/v1/tasks", "k1", body)
	b := call(t, srv, "POST", "/v1/tasks", "k1", body)
	if a["status"] != "accepted" || a["task_id"] != b["task_id"] || len(r.tasks) != 1 {
		t.Fatal("same key, same task:", a, b)
	}
	time.Sleep(60 * time.Millisecond)
	if c := call(t, srv, "POST", "/v1/tasks/"+a["task_id"].(string)+"/cancel", "", ""); c["status"] != "succeeded" {
		t.Fatal("a task past its delay was cancelled instead of succeeding:", c)
	}
	s := call(t, srv, "GET", "/v1/tasks/"+a["task_id"].(string), "", "")
	if s["status"] != "succeeded" || !strings.Contains(s["result"].(map[string]any)["text"].(string), "1 memory items") {
		t.Fatal(s)
	}
	if w := call(t, srv, "POST", "/v1/tasks", "k2", `{"protocol":"arkism.backend.v1","task":"x","access":{"mode":"full"}}`); w["error"] == nil {
		t.Fatal("write access accepted", w)
	}
}
