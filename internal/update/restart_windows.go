package update

import (
	"os"
	"os/exec"
)

// Restart starts the new program and ends this one.
func Restart(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
