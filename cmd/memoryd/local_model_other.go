//go:build !windows

package main

import "os/exec"

func runLocalModel(cmd *exec.Cmd) error { return cmd.Run() }
