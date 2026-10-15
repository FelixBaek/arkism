package gateway

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The Gemini entrance with google/ and group/ models over local
// TCP against a synthetic Google provider.

// gxFollow is body plus the model's answer and a function result.
func gxFollow(t *testing.T, body string, first any, result string) string {
	follow := gxObj(t, body)
	contents := append(at(follow, "contents").([]any), at(first, "candidates", 0, "content"),
		M{"role": "user", "parts": []any{M{"functionResponse": M{"name": "echo", "response": M{"result": result}}}}})
	follow.Set("contents", contents)
	return dumpJSON(follow)
}

func TestGoogleGatewayAPIKeyOptionsToolsAndSignaturesAreNativeAndCallerKeysStayLocal(t *testing.T) {
	g := gxSetup(t)
	g.setMode("tool")
	reply := g.post(gxBody, "x-goog-api-key", "caller-google", "x-private", "private")
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	bridgeSameJSON(t, parsed(t, reply.body), gxResult(true))
	sent := g.providerAt(-1)
	if sent.path != "/api/models/gemini-test:generateContent" {
		t.Fatal(sent.path)
	}
	bridgeSameJSON(t, sent.body, gxObj(t, gxBody))
	if sent.header.Get("x-goog-api-key") != "host-api-key" || sent.header.Get("authorization") != "" ||
		sent.header.Get("x-private") != "" || strings.Contains(dumpJSON(sent.header), "caller") {
		t.Fatalf("headers %v", sent.header)
	}
}

func TestGoogleGatewayOAuthRefreshProjectAndToolResultRoundTripDoNotChangeLogin(t *testing.T) {
	g := gxSetup(t)
	original := g.oauth()
	g.setMode("tool")
	reply := g.post(gxBody)
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	follow := gxFollow(t, gxBody, parsed(t, reply.body), "한글")
	g.setMode("json")
	if got := g.post(follow); got.status != 200 {
		t.Fatalf("follow %d %s", got.status, got.body)
	}
	same(t, g.providerPaths(), `["/token","/assist:loadCodeAssist","/assist:generateContent","/assist:generateContent"]`)
	sent := g.providerAt(-1).body
	bridgeSameJSON(t, at(sent, "request"), gxObj(t, follow))
	if _, ok := sent.(host.Obj).Get("enabled_credit_types"); ok || at(sent, "project") != "synthetic-project" {
		t.Fatalf("sent %s", dumpJSON(sent))
	}
	if now, _ := os.ReadFile(g.login); string(now) != string(original) {
		t.Fatal("login changed")
	}
	if strings.Contains(string(reply.body), "host-") {
		t.Fatalf("leaked %s", reply.body)
	}
}

func TestGoogleGatewayGroupPinsSignedHistoryAndRejectsOtherGoogleCaller(t *testing.T) {
	g := gxSetup(t)
	g.endpoint("group/test", "generateContent")
	g.setMode("tool")
	reply := g.post(gxBody, "x-goog-api-key", "caller-one")
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	follow := gxFollow(t, gxBody, parsed(t, reply.body), "결과")
	decided := g.decisionCount()
	if got := g.post(follow, "x-goog-api-key", "caller-two").status; got != 409 {
		t.Fatalf("other caller %d", got)
	}
	alias := strings.Replace(follow, `"thoughtSignature"`, `"thought_signature"`, 1)
	if got := g.post(alias, "x-goog-api-key", "caller-two").status; got != 409 {
		t.Fatalf("alias %d", got)
	}
	if g.decisionCount() != decided {
		t.Fatal("routed a rejected history")
	}
	g.route("google/gemini-other", "google/gemini-test")
	g.setMode("json")
	if got := g.post(follow, "x-goog-api-key", "caller-one"); got.status != 200 {
		t.Fatalf("owner %d %s", got.status, got.body)
	}
	if path := g.providerAt(-1).path; path != "/api/models/gemini-test:generateContent" {
		t.Fatal(path)
	}
	if n := g.reportCount(); n != 2 {
		t.Fatalf("reports %d", n)
	}
}

func TestGoogleGatewayNativeStreamAndTruncationNeverFallBackAfterText(t *testing.T) {
	g := gxSetup(t)
	g.endpoint("group/test", "streamGenerateContent")
	g.setMode("tool")
	reply := g.post(gxBody)
	if reply.status != 200 || reply.err != nil {
		t.Fatalf("%d %v %s", reply.status, reply.err, reply.body)
	}
	pages := events(t, reply.body)
	bridgeSameJSON(t, last(pages), gxResult(true))
	same(t, at(pages[0], "candidates", 0, "content", "parts"), `[{"text":"先"}]`)
	g.setMode("truncated")
	g.clearAll()
	g.route("google/gemini-test", "google/gemini-other")
	reply = g.post(gxBody)
	raw := string(reply.body)
	if reply.status != 200 || strings.Contains(raw, "functionCall") || strings.Contains(raw, "finishReason") || g.providerCount() != 1 {
		t.Fatalf("%d %d %s", reply.status, g.providerCount(), raw)
	}
	// Go drops the connection after a started response fails; Python ended with an error event.
	if reply.err == nil && !strings.Contains(raw, `"error"`) {
		t.Fatalf("no error %s", raw)
	}
	if g.lastReportOK() != false {
		t.Fatal("reported ok")
	}
}

func TestGoogleGatewayConfirmedGroupRejectionUsesOneNextCandidate(t *testing.T) {
	g := gxSetup(t)
	g.endpoint("group/test", "generateContent")
	g.route("google/gemini-reject", "google/gemini-test")
	if got := g.post(gxBody); got.status != 200 {
		t.Fatalf("%d %s", got.status, got.body)
	}
	if g.providerCount() != 2 {
		t.Fatal(g.providerPaths())
	}
	same(t, g.reportOKs(), `[false,true]`)
}

func TestGoogleGatewayCountTokensIsMeasuredAndNeverGeneratesOrRoutes(t *testing.T) {
	g := gxSetup(t)
	g.endpoint("google/gemini-test", "countTokens")
	reply := g.post(dumpJSON(M{"contents": at(gxObj(t, gxBody), "contents")}))
	if reply.status != 200 {
		t.Fatalf("%d %s", reply.status, reply.body)
	}
	same(t, parsed(t, reply.body), `{"totalTokens":137}`)
	if path := g.providerAt(-1).path; path != "/api/models/gemini-test:countTokens" || g.decisionCount() != 0 {
		t.Fatal(path, g.decisionCount())
	}
	if got := g.post(gxBody).status; got != 400 {
		t.Fatalf("full body counted: %d", got)
	}
	if g.providerCount() != 1 {
		t.Fatal(g.providerPaths())
	}
}

// GoogleGatewayReviewTest: the body's model field and provider HTTP failures.

const gxReviewBody = `{"contents":[{"role":"user","parts":[{"text":"안녕하세요 🌱"}]}],"generationConfig":{"maxOutputTokens":256}}`

func TestGoogleGatewayMatchingBodyModelIsRemovedForDirectAndGroupRequests(t *testing.T) {
	g := gxSetup(t)
	for _, model := range []string{"google/gemini-test", "group/test"} {
		methods := []string{"generateContent", "streamGenerateContent"}
		if strings.HasPrefix(model, "google/") {
			methods = append(methods, "countTokens")
		}
		for _, method := range methods {
			for _, stated := range []string{model, "models/" + model} {
				name := model + " " + method + " " + stated
				g.endpoint(model, method)
				g.clearAll()
				native := gxObj(t, gxReviewBody)
				if method == "countTokens" {
					native = host.Obj{{Key: "contents", Value: at(native, "contents")}}
				}
				body := gxClone(t, native).(host.Obj)
				body.Set("model", stated)
				reply := g.post(dumpJSON(body))
				if reply.status != 200 || g.providerCount() != 1 {
					t.Fatalf("%s: %d %d %s", name, reply.status, g.providerCount(), reply.body)
				}
				suffix := ""
				if method == "streamGenerateContent" {
					suffix = "?alt=sse"
				}
				sent := g.providerAt(0)
				if sent.path != "/api/models/gemini-test:"+method+suffix {
					t.Fatalf("%s: %s", name, sent.path)
				}
				bridgeSameJSON(t, sent.body, native)
				if method == "countTokens" {
					same(t, parsed(t, reply.body), `{"totalTokens":137}`)
				}
				want := 0
				if strings.HasPrefix(model, "group/") {
					want = 1
				}
				if g.decisionCount() != want || g.reportCount() != want {
					t.Fatalf("%s: decisions %d reports %d", name, g.decisionCount(), g.reportCount())
				}
			}
		}
	}
}

func TestGoogleGatewayMismatchedBodyModelIs400BeforeDirectOrGroupExecution(t *testing.T) {
	g := gxSetup(t)
	for _, model := range []string{"google/gemini-test", "group/test"} {
		g.endpoint(model, "generateContent")
		for _, stated := range []any{"google/gemini-other", "models/google/gemini-other", "gemini-test", "models/models/" + model, nil, 7, M{}} {
			body := gxObj(t, gxReviewBody)
			body.Set("model", stated)
			reply := g.post(dumpJSON(body))
			if reply.status != 400 || errorMessage(t, reply.body) != "invalid_request" {
				t.Fatalf("%s %v: %d %s", model, stated, reply.status, reply.body)
			}
		}
	}
	if g.providerCount() != 0 || g.decisionCount() != 0 || g.reportCount() != 0 {
		t.Fatal("executed")
	}
}

// gxRaw answers on the hijacked connection with raw bytes, then closes it.
func gxRaw(raw string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		rw.WriteString(raw)
		rw.Flush()
		conn.Close()
	}
}

// gxFault makes the next response body end with gReadError when armed, and keeps it.
type gxFault struct {
	base  http.RoundTripper
	armed *atomic.Bool
	body  *atomic.Pointer[gBody]
}

func (f gxFault) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f.base.RoundTrip(r)
	if err == nil && f.armed.CompareAndSwap(true, false) {
		body := &gBody{ReadCloser: gReadError(resp.Body)}
		resp.Body = body
		f.body.Store(body)
	}
	return resp, err
}

func TestGoogleGatewayGroupGenerationHTTPExceptionsNeverUseFallback(t *testing.T) {
	g := gxSetup(t)
	g.route("google/gemini-test", "google/gemini-other")
	// The read case fails the body with a raw error after its last byte (http.client.IncompleteRead).
	var fault atomic.Bool
	var faulted atomic.Pointer[gBody]
	swap(t, &googleClient, &http.Client{Transport: gxFault{googleClient.Transport, &fault, &faulted}, CheckRedirect: googleClient.CheckRedirect})
	for _, method := range []string{"generateContent", "streamGenerateContent"} {
		for _, failure := range []string{"read", "open", "header"} {
			name := method + " " + failure
			g.endpoint("group/test", method)
			g.clearAll()
			code := "upstream_unreachable"
			switch failure {
			case "read":
				code = "upstream_invalid_response"
				data, ctype := dumpJSON(gxResult(true)), "application/json"
				if method == "streamGenerateContent" {
					data, ctype = "data: "+data+"\n\n", "text/event-stream"
				}
				// gxRaw closes its hijacked socket. Announce that before the
				// complete body is pooled: otherwise the next non-retryable POST
				// can race the peer close and fail before reaching this fixture.
				g.next(gxRaw(fmt.Sprintf("HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s", ctype, len(data), data)))
				fault.Store(true)
			case "open":
				g.next(gxRaw(""))
			case "header":
				g.next(gxRaw("host-api-key private-diagnostic\r\n\r\n"))
			}
			reply := g.post(gxReviewBody)
			raw := string(reply.body)
			if reply.status != 502 || errorMessage(t, reply.body) != code {
				t.Fatalf("%s: %d %s", name, reply.status, raw)
			}
			if g.providerCount() != 1 || !strings.Contains(g.providerAt(0).path, "gemini-test:"+method) {
				t.Fatalf("%s: %v", name, g.providerPaths())
			}
			if g.reportCount() != 1 || g.lastReportOK() != false {
				t.Fatalf("%s: reports %v", name, g.reportOKs())
			}
			for _, leak := range []string{"host-api-key", "private-diagnostic", "functionCall", "thoughtSignature"} {
				if strings.Contains(raw, leak) {
					t.Fatalf("%s: leaked %s in %s", name, leak, raw)
				}
			}
			if failure == "read" {
				body := faulted.Swap(nil)
				for i := 0; i < 100 && body != nil && !body.closed.Load(); i++ {
					time.Sleep(10 * time.Millisecond)
				}
				if body == nil || !body.closed.Load() {
					t.Fatalf("%s: provider body not closed", name)
				}
			}
		}
	}
}

// GooglePinTest: an active signed history outlives the first turn, an idle one expires.
func TestGooglePinActiveSignedHistoryOutlivesFirstTurnButIdleHistoryExpires(t *testing.T) {
	gReset()
	t.Cleanup(gReset)
	clock := 0.0
	swap(t, &googleMono, func() float64 { return clock })
	response := gxResult(true)
	history := host.Obj{{Key: "contents", Value: []any{at(response, "candidates", 0, "content")}}}
	if err := googleRemember(response, "caller", "google/gemini-test"); err != nil {
		t.Fatal(err)
	}
	for _, now := range []float64{899, 1700} {
		clock = now
		if model, err := googleContinuationModel(history, "caller"); err != nil || model != "google/gemini-test" {
			t.Fatal(now, model, err)
		}
	}
	clock = 2601
	_, err := googleContinuationModel(history, "caller")
	wantGoogle(t, err, "conversation_mismatch", 409)
}
