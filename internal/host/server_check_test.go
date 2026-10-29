package host

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForVersionWaitsForTheRestartedServer(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 { // the old process is still answering
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"0.1.7","api_level":1}`))
	}))
	defer srv.Close()
	got, err := waitForVersion(srv.URL, "0.1.7", time.Second, 10*time.Millisecond)
	if err != nil || got.APILevel != 1 {
		t.Fatal(got, err)
	}
	if _, err := waitForVersion(srv.URL, "9.9.9", 50*time.Millisecond, 10*time.Millisecond); err == nil {
		t.Fatal("a server that never reports the wanted version must fail")
	}
	if _, err := waitForVersion(srv.URL, "", 50*time.Millisecond, 10*time.Millisecond); err != nil {
		t.Fatal("an empty want accepts any healthy answer:", err)
	}
}
