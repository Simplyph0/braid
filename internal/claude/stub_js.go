//go:build js

package claude

import (
	"context"
	"errors"
)

// The web app (WebAssembly) never starts or stops programs; these only let
// the shared code compile.

type App struct{ PID int32 }

func DesktopRunning() bool        { return false }
func RunningApps() ([]App, error) { return nil, nil }
func Quit(context.Context, []App, func(string, ...any)) error {
	return errors.New("not supported here")
}
func Relaunch([]App) error   { return nil }
func ClearStaleLocks(string) {}
