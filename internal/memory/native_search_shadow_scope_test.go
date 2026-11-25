package memory

import "testing"

func TestNativeSearchMetricsOnlyCountRequestedScope(t *testing.T) {
	scope := Scope{WorkspaceID: "test", ProjectID: "project", UserID: "first"}
	other := scope
	other.UserID = "second"
	key := nativeSearchHash("Scope", scope)
	runtime := &NativeSearchShadowRuntime{
		config: NativeSearchShadowConfig{Mode: "shadow", Scopes: []Scope{scope}},
		records: []NativeSearchShadowObservation{
			{ScopeSHA: key, Status: "selected"},
			{ScopeSHA: key, Status: "fallback"},
			{ScopeSHA: nativeSearchHash("Scope", other), Status: "selected", Served: true},
		},
	}
	got := runtime.recentMetrics(scope)
	if got["recent_completed"] != 1 || got["recent_fallback"] != 1 || got["recent_served"] != 0 {
		t.Fatalf("unexpected scoped counts: %v", got)
	}
	if runtime.enabledFor(other) || !runtime.enabledFor(scope) {
		t.Fatal("shadow configuration did not respect its scope")
	}
}
