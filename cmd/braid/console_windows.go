package main

import (
	"os/exec"
	"syscall"
)

// detachConsole closes the console window Windows opens for a program
// started at sign-in.
func detachConsole() { syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole").Call() }

// detach starts braid without a console window.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008} } // DETACHED_PROCESS
