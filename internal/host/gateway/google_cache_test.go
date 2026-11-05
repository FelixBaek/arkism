package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// gCacheRequest has a system instruction and tools long enough to cache.
var gCacheRequest = `{"contents":[{"role":"user","parts":[{"text":"q"}]}],"systemInstruction":{"parts":[{"text":"` +
	strings.Repeat("stable rules ", 400) + `"}]},"tools":[{"functionDeclarations":[{"name":"look"}]}],"generationConfig":{"temperature":0}}`

const gCacheName = "cachedContents/abc123"

var gCacheCreated = gJSON(`{"name":"` + gCacheName + `"}`)

// gWantCached checks a generate request points at the cache and carries no cached field.
func gWantCached(t *testing.T, s gSent, cached bool) {
	t.Helper()
	if !strings.HasSuffix(s.uri, ":generateContent") {
		t.Fatalf("not a generate request: %s", s.uri)
	}
	has := strings.Contains(s.body, `"cachedContent":"`+gCacheName+`"`)
	full := strings.Contains(s.body, `"systemInstruction"`) && strings.Contains(s.body, `"tools"`)
	if has != cached || full == cached || !strings.Contains(s.body, `"generationConfig"`) {
		t.Fatalf("cached=%v body=%.200s", cached, s.body)
	}
}

func TestGoogleContextCacheCreateReuseExpire(t *testing.T) {
	f := gSetup(t)
	f.api()
	t.Setenv("ARKISM_GOOGLE_CONTEXT_CACHE", "1")
	f.replies(gCacheCreated, gJSON(gFinal))
	gWantPages(t, gCacheRequest, false, false, gFinal)
	create := f.sent(0)
	if create.uri != "/v1beta/cachedContents" || create.header.Get("X-Goog-Api-Key") != gAPIKey ||
		!strings.Contains(create.body, `"model":"models/`+gModel+`"`) || !strings.Contains(create.body, `"ttl":"3300s"`) ||
		!strings.Contains(create.body, `"tools"`) || strings.Contains(create.body, `"contents"`) {
		t.Fatalf("create %s %.300s", create.uri, create.body)
	}
	gWantCached(t, f.sent(1), true)

	f.replies(gJSON(gFinal)) // reused: no second create
	gWantPages(t, gCacheRequest, false, false, gFinal)
	gWantCached(t, f.sent(0), true)

	f.clock.add(googleCacheTTL - googleCacheRefresh) // near expiry: recreate
	f.replies(gCacheCreated, gJSON(gFinal))
	gWantPages(t, gCacheRequest, false, false, gFinal)
	if f.count() != 2 || f.sent(0).uri != "/v1beta/cachedContents" {
		t.Fatal("expired entry was not recreated")
	}
}

func TestGoogleContextCacheRefusalFallsBackAndIsRemembered(t *testing.T) {
	f := gSetup(t)
	f.api()
	t.Setenv("ARKISM_GOOGLE_CONTEXT_CACHE", "1")
	refuse := gJSON(`{"error":{"message":"below minimum token count"}}`)
	f.replies(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); refuse(w, r) }, gJSON(gFinal))
	gWantPages(t, gCacheRequest, false, false, gFinal)
	gWantCached(t, f.sent(1), false)
	f.replies(gJSON(gFinal)) // the refusal is remembered for a while
	gWantPages(t, gCacheRequest, false, false, gFinal)
	gWantCached(t, f.sent(0), false)
	f.clock.add(googleCacheRefresh)
	f.replies(gCacheCreated, gJSON(gFinal))
	gWantPages(t, gCacheRequest, false, false, gFinal)
	gWantCached(t, f.sent(1), true)
}

func TestGoogleContextCacheRejectedNameIsForgotten(t *testing.T) {
	f := gSetup(t)
	f.api()
	t.Setenv("ARKISM_GOOGLE_CONTEXT_CACHE", "1")
	f.replies(gCacheCreated, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	gFail(t, gCacheRequest, false, false, "upstream_http_404", 404, false)
	f.replies(gCacheCreated, gJSON(gFinal))
	gWantPages(t, gCacheRequest, false, false, gFinal)
	if f.count() != 2 || f.sent(0).uri != "/v1beta/cachedContents" {
		t.Fatal("rejected cache was reused")
	}
}

func TestGoogleContextCacheOffShortOrLoginNeverCreates(t *testing.T) {
	f := gSetup(t)
	f.api()
	f.replies(gJSON(gFinal)) // off by default
	gWantPages(t, gCacheRequest, false, false, gFinal)
	gWantCached(t, f.sent(0), false)

	t.Setenv("ARKISM_GOOGLE_CONTEXT_CACHE", "1")
	short := `{"contents":[{"role":"user","parts":[{"text":"q"}]}],"systemInstruction":{"parts":[{"text":"short"}]}}`
	f.replies(gJSON(gFinal))
	gWantPages(t, short, false, false, gFinal)
	if f.count() != 1 || strings.Contains(f.sent(0).body, "cachedContent") {
		t.Fatal("short prefix was cached")
	}

	f.oauth() // the login endpoint has no cache resource
	f.replies(gSetupReply(""), gJSON(gWrapped(gFinal)))
	if _, err := gCollect(t, gCacheRequest, false, false); err != nil {
		t.Fatal(err)
	}
	for i := range f.count() {
		if strings.Contains(f.sent(i).uri, "cachedContents") || strings.Contains(f.sent(i).body, `"cachedContent"`) {
			t.Fatal("login mode used the cache")
		}
	}
}
