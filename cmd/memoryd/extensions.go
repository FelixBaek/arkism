package main

import "github.com/FelixBaek/arkism/internal/memory"

// daemonExtensions are compiled-in hooks (memory.Extensions); this build has none.
// To build with your own, add a file to this package, for example
// extensions_local.go behind a build tag, whose init sets the fields, then
// build with `go build -tags yourtag ./cmd/memoryd`.
var daemonExtensions memory.Extensions
