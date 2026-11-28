package main

import (
	"github.com/FelixBaek/arkism/internal/host"
	"os/exec"
)

func runLocalModel(cmd *exec.Cmd) error {
	finish, err := host.StartProcessTree(cmd)
	if err != nil {
		return err
	}
	defer finish()
	return cmd.Wait()
}
