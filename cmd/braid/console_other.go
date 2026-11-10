//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func detachConsole() {}

// detach keeps a started braid running after this terminal closes.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
