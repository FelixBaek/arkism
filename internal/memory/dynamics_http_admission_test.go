package memory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func dynamicsOutcomeHTTP(t *testing.T, p *nativePGFixture) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(p.in)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/memory-outcomes", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+p.checkerToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Handler(p.checker).ServeHTTP(w, r)
	return w
}

func TestDynamicsHTTPAtomicAdmissionPG(t *testing.T) {
	p, _, _ := dynamicsFunctionalSetup(t)
	first := p.in
	var firstRef Ref
	for i := 0; i < 4; i++ {
		p.in.EventID = "http-load-" + string(rune('a'+i))
		w := dynamicsOutcomeHTTP(t, p)
		if w.Code != http.StatusOK {
			t.Fatalf("admission %d: %d %s", i, w.Code, w.Body.String())
		}
		if i == 0 {
			first = p.in
			if err := json.Unmarshal(w.Body.Bytes(), &firstRef); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := nativeSearchRawDatabaseSnapshot(t, p.admin)
	p.in.EventID = "http-load-full"
	w := dynamicsOutcomeHTTP(t, p)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "" {
		t.Fatalf("full response: %d %s", w.Code, w.Body.String())
	}
	var failure map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || len(failure) != 1 || failure["code"] != "dynamics_queue_full" {
		t.Fatalf("full wire code: %s", w.Body.String())
	}
	if !reflect.DeepEqual(before, nativeSearchRawDatabaseSnapshot(t, p.admin)) {
		t.Fatal("rejected HTTP request changed canonical database")
	}
	p.in = first
	w = dynamicsOutcomeHTTP(t, p)
	var retry Ref
	if err := json.Unmarshal(w.Body.Bytes(), &retry); err != nil || w.Code != http.StatusOK || retry != firstRef {
		t.Fatalf("free retry: %d %s", w.Code, w.Body.String())
	}
	p.in.Passed = !first.Passed
	w = dynamicsOutcomeHTTP(t, p)
	if w.Code != http.StatusConflict {
		t.Fatalf("changed event: %d %s", w.Code, w.Body.String())
	}
}
