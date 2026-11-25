package main

import (
	"github.com/FelixBaek/arkism/internal/host"
	_ "github.com/FelixBaek/arkism/internal/host/skills"
	"os"
)

func main() {
	os.Exit(host.Main(append([]string{"skill-review", "compare-selected"}, os.Args[1:]...), os.Stdin, os.Stdout, os.Stderr))
}
