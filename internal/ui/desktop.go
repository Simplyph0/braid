package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PickFolder shows the system's folder picker. "" means cancelled.
func PickFolder() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell", "-NoProfile", "-STA", "-Command",
			`Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description = 'Choose a folder for Braid'; if ($d.ShowDialog() -eq 'OK') { $d.SelectedPath }`)
	default:
		switch {
		case have("zenity"):
			cmd = exec.Command("zenity", "--file-selection", "--directory", "--title=Choose a folder for Braid")
		case have("kdialog"):
			cmd = exec.Command("kdialog", "--getexistingdirectory", os.Getenv("HOME"), "--title", "Choose a folder for Braid")
		case have("yad"):
			cmd = exec.Command("yad", "--file", "--directory", "--title=Choose a folder for Braid")
		default:
			return "", errors.New("no folder picker found (install zenity or kdialog); type the folder's path instead")
		}
	}
	out, err := cmd.Output()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		return "", nil // closed without choosing
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func have(name string) bool { _, err := exec.LookPath(name); return err == nil }

// OpenPath shows a folder in the file manager.
func OpenPath(p string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("explorer", p).Start()
	}
	return exec.Command("xdg-open", p).Start()
}

// OpenURL opens a web address in the default browser.
func OpenURL(u string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}

// OpenWindow opens u as an app window (no tabs or address bar) when a
// Chromium-based browser is around, else in the default browser.
func OpenWindow(u string) error {
	for _, b := range browsers() {
		if err := exec.Command(b, "--app="+u, "--window-size=880,680").Start(); err == nil {
			return nil
		}
	}
	return OpenURL(u)
}

func browsers() []string {
	if runtime.GOOS == "windows" {
		var out []string
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			for _, rel := range []string{`Microsoft\Edge\Application\msedge.exe`, `Google\Chrome\Application\chrome.exe`} {
				p := filepath.Join(os.Getenv(env), rel)
				if _, err := os.Stat(p); err == nil {
					out = append(out, p)
				}
			}
		}
		return out
	}
	var out []string
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser", "vivaldi"} {
		if p, err := exec.LookPath(b); err == nil {
			out = append(out, p)
		}
	}
	return out
}
