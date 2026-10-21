package gateway

import (
	"context"
	"github.com/FelixBaek/arkism/internal/host"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestPassBodyRejectsDeclaredAllocationBeforeReading(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost/unknown", nil)
	r.ContentLength = 1 << 40
	x := exchange{r: r, target: "/unknown"}
	if _, err := x.body(); err == nil || err.Error() != "input_too_large" {
		t.Fatal("unbounded pass allocation", err)
	}
}

func TestChatUpstreamStopsWhenCallerCancels(t *testing.T) {
	entered := make(chan struct{})
	left := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(left)
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		for _, streamErr := range chatStream(ctx, s.URL, "test-key", host.Obj{{Key: "model", Value: "test"}}) {
			if streamErr != nil {
				break
			}
		}
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway retained cancelled upstream")
	}
	select {
	case <-left:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream retained cancelled request")
	}
}

func TestGatewaySignalDrainsActiveHTTPBeforeReturning(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte("complete"))
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	signals := make(chan os.Signal, 1)
	finished := make(chan error, 1)
	go func() { finished <- serveGatewayLifecycle(server, ln, nil, &claudeBridge{}, signals) }()
	response := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			response <- "failed"
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		response <- string(b)
	}()
	<-entered
	signals <- syscall.SIGTERM
	select {
	case <-finished:
		close(release)
		t.Fatal("process returned before active HTTP completed")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case body := <-response:
		if body != "complete" {
			t.Fatal("drained response lost", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP not drained")
	}
	select {
	case err := <-finished:
		if err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}
