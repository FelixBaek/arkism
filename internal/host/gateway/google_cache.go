package gateway

import (
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"
	"sync"

	"github.com/FelixBaek/arkism/internal/host"
)

// Context cache (API key mode only, opt-in with ARKISM_GOOGLE_CONTEXT_CACHE=1): a long stable
// prefix (system instruction, tools, tool config) is stored once upstream as cached content and
// later requests point at it, so the provider bills those input tokens at its cached rate. The
// reply's cachedContentTokenCount already flows into the chat usage as cached_tokens.
var (
	googleCacheTTL      = 55 * 60.0 // seconds asked of the provider
	googleCacheRefresh  = 5 * 60.0  // recreate this long before expiry; also how long a refusal is remembered
	googleCacheWait     = 30.0      // longest create call; it runs under the lock
	googleCacheMinBytes = 4096      // shorter prefixes are below every model's cacheable minimum
	googleCacheMax      = 100
	googleCacheMu       sync.Mutex
	googleCaches        = map[[32]byte]googleCached{}
)

type googleCached struct {
	name string // "" remembers a refused create
	made float64
}

var googleCacheFields = []string{"systemInstruction", "tools", "toolConfig"}

var googleCacheName = regexp.MustCompile(`^cachedContents/[A-Za-z0-9_-]{1,200}$`)

// googleUseCache returns body pointing at a cached prefix and the cache name, or body unchanged and "".
// A failed or refused create never fails the request: it falls back to the full body.
func googleUseCache(auth *googleAuth, body host.Obj, model string, began float64) (host.Obj, string) {
	if auth.key == "" || os.Getenv("ARKISM_GOOGLE_CONTEXT_CACHE") != "1" || has(body, "cachedContent") || !has(body, "systemInstruction") {
		return body, ""
	}
	prefix := host.Obj{{Key: "model", Value: "models/" + model}}
	for _, field := range googleCacheFields {
		if value, ok := body.Get(field); ok {
			prefix = append(prefix, host.KV{Key: field, Value: value})
		}
	}
	text, err := googleEncoded(prefix)
	if err != nil || len(text) < googleCacheMinBytes {
		return body, ""
	}
	name := googleCacheLookup(sha256.Sum256([]byte(text)), prefix, auth.key, began)
	if name == "" {
		return body, ""
	}
	out := clone(body).(host.Obj)
	for _, field := range googleCacheFields {
		out.Del(field)
	}
	out.Set("cachedContent", name)
	return out, name
}

// googleCacheLookup reuses a fresh entry or creates one; creates are serialised so concurrent
// requests for one prefix create it once.
func googleCacheLookup(key [32]byte, prefix host.Obj, apiKey string, began float64) string {
	googleCacheMu.Lock()
	defer googleCacheMu.Unlock()
	now := googleMono()
	if entry, ok := googleCaches[key]; ok {
		life := googleCacheTTL - googleCacheRefresh
		if entry.name == "" {
			life = googleCacheRefresh
		}
		if now-entry.made < life {
			return entry.name
		}
		delete(googleCaches, key)
	}
	create := append(clone(prefix).(host.Obj), host.KV{Key: "ttl", Value: fmt.Sprintf("%ds", int(googleCacheTTL))})
	// Shorten the shared deadline so a hung create holds the lock at most googleCacheWait.
	capped := max(began, googleMono()-googleDeadline+googleCacheWait)
	reply, err := googleJSONPost(googleAPI+"/cachedContents", create, map[string]string{"x-goog-api-key": apiKey}, false, capped)
	name, _ := get(reply, "name").(string)
	if err != nil || !googleCacheName.MatchString(name) {
		name = ""
	}
	if len(googleCaches) >= googleCacheMax {
		googleCacheEvictOldest()
	}
	googleCaches[key] = googleCached{name: name, made: now}
	return name
}

func googleCacheEvictOldest() {
	var oldest [32]byte
	first := true
	for key, entry := range googleCaches {
		if first || entry.made < googleCaches[oldest].made {
			oldest, first = key, false
		}
	}
	delete(googleCaches, oldest)
}

// googleCacheForget drops a name the provider rejected so the next request recreates it.
func googleCacheForget(name string) {
	googleCacheMu.Lock()
	defer googleCacheMu.Unlock()
	for key, entry := range googleCaches {
		if entry.name == name {
			delete(googleCaches, key)
		}
	}
}
