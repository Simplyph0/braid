//go:build !windows && !js

package update

import (
	"os"
	"syscall"
)

// Restart runs the new program in this process's place.
func Restart(exe string) error { return syscall.Exec(exe, os.Args, os.Environ()) }
