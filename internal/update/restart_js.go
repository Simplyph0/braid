//go:build js

package update

import "errors"

// Restart: the web app never updates itself.
func Restart(string) error { return errors.New("not supported here") }
