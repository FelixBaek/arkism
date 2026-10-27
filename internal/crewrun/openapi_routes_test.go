package crewrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every route the webhook listener registers must be described under x-local-crew-hooks in api/openapi.yaml.
func TestWebhookRoutesAreInOpenAPI(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks struct {
			Paths map[string]struct {
				Method string `json:"method"`
			} `json:"paths"`
		} `json:"x-local-crew-hooks"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("*.go")
	route := regexp.MustCompile(`\.HandleFunc\("([A-Z]+) (/[^"]*)"`)
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range route.FindAllStringSubmatch(string(src), -1) {
			seen++
			if got := doc.Hooks.Paths[m[2]].Method; got != m[1] {
				t.Errorf("%s: %s %s is not described under x-local-crew-hooks (documented method %q)", name, m[1], m[2], got)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no HandleFunc route found; the scan needs updating")
	}
}
