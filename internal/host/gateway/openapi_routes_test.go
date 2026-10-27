package gateway

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// Every path the gateway answers by an exact match (path == "..." or the entrances set) must be described under x-local-gateway in api/openapi.yaml.
func TestGatewayPathsAreInOpenAPI(t *testing.T) {
	data, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Gateway struct {
			Paths map[string]json.RawMessage `json:"paths"`
		} `json:"x-local-gateway"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("gateway.go")
	if err != nil {
		t.Fatal(err)
	}
	set := regexp.MustCompile(`entrances\s*=\s*set\(([^)]*)\)`).FindSubmatch(src)
	if set == nil {
		t.Fatal("entrances set not found in gateway.go; the scan needs updating")
	}
	quoted := regexp.MustCompile(`"(/[^"]*)"`)
	matches := append(regexp.MustCompile(`path == "(/[^"]*)"`).FindAllStringSubmatch(string(src), -1), quoted.FindAllStringSubmatch(string(set[1]), -1)...)
	seen := 0
	for _, m := range matches {
		if m[1] == "/v1beta" { // prefix test for the /v1beta/models/... paths documented one by one
			continue
		}
		seen++
		if _, ok := doc.Gateway.Paths[m[1]]; !ok {
			t.Errorf("gateway.go answers %s but x-local-gateway does not describe it", m[1])
		}
	}
	if seen == 0 {
		t.Fatal("no exact path match found in gateway.go; the scan needs updating")
	}
}
