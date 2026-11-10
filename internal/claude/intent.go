package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Home is where Claude Code keeps its chats.
func Home() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

// Intent sums up what you asked Claude to do in folder between since and
// until: your last few prompts there, one line each. "" if none.
func Intent(home, folder string, since, until time.Time) string {
	files, _ := filepath.Glob(filepath.Join(home, "projects", "*", "*.jsonl"))
	type prompt struct {
		at   time.Time
		text string
	}
	var found []prompt
	for _, f := range files {
		if fi, err := os.Stat(f); err != nil || fi.ModTime().Before(since) {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 32<<20)
		for sc.Scan() {
			var e struct {
				Type      string    `json:"type"`
				Cwd       string    `json:"cwd"`
				Time      time.Time `json:"timestamp"`
				IsMeta    bool      `json:"isMeta"`
				Sidechain bool      `json:"isSidechain"`
				Message   struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "user" || e.IsMeta || e.Sidechain {
				continue
			}
			if !e.Time.After(since) || e.Time.After(until) || !inside(e.Cwd, folder) {
				continue
			}
			if t := promptText(e.Message.Content); t != "" {
				found = append(found, prompt{e.Time, t})
			}
		}
		fh.Close()
	}
	if len(found) == 0 {
		return ""
	}
	// newest last, keep the last three
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j].at.Before(found[j-1].at); j-- {
			found[j], found[j-1] = found[j-1], found[j]
		}
	}
	if len(found) > 3 {
		found = found[len(found)-3:]
	}
	parts := make([]string, len(found))
	for i, p := range found {
		parts[i] = p.text
	}
	return strings.Join(parts, " · ")
}

// promptText is the first line of what you typed (not tool results or
// system notes), cut to a readable length.
func promptText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var parts []struct{ Type, Text string }
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		for _, p := range parts {
			if p.Type == "text" {
				s = p.Text
				break
			}
		}
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "<") {
		return ""
	}
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:99]) + "…"
	}
	return s
}

func inside(cwd, folder string) bool {
	if cwd == "" {
		return false
	}
	c, f := filepath.Clean(cwd), filepath.Clean(folder)
	if runtime.GOOS == "windows" {
		c, f = strings.ToLower(c), strings.ToLower(f)
	}
	return c == f || strings.HasPrefix(c, f+string(filepath.Separator))
}
