// Package config ships the harness model plan and Haiku quality evidence inside the binary, so an
// installed host has the same defaults as the repository (ST-620, FR-012).
package config

// The blank import enables the //go:embed directives below.
import _ "embed"

//go:embed harness-models.json
var HarnessModels []byte

//go:embed harness-quality.json
var HarnessQuality []byte
