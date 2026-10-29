package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/FelixBaek/arkism/internal/buildinfo"
)

func TestHealthzReportsVersionAPILevelAndFeatures(t *testing.T) {
	mux := http.NewServeMux()
	registerHealthCoreHTTP(mux, &Service{}, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	var got buildinfo.Health
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatal(w.Code, err)
	}
	if got.Status != "ok" || got.Version != buildinfo.Version || got.APILevel != buildinfo.APILevel || !slices.Contains(got.Features, "forget_both") || !slices.Contains(got.Features, "role_model_picks") {
		t.Fatalf("%+v", got)
	}
}
