package ui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Simplyph0/braid/internal/claude"
)

// Found is a folder you've used Claude Code in.
type Found struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Git  bool   `json:"git"`
}

// ScanFolders lists the folders you've used Claude Code in that you haven't
// added yet, newest first. have is the folders already added.
func ScanFolders(home string, have []string) []Found {
	added := map[string]bool{}
	for _, h := range have {
		added[filepath.Clean(h)] = true
	}
	covered := func(p string) bool { // already added, or inside / around something that is
		for a := range added {
			if p == a || strings.HasPrefix(p, a+string(filepath.Separator)) || strings.HasPrefix(a, p+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	found := map[string]*Found{}
	add := func(p string) {
		p = filepath.Clean(p)
		if p == filepath.Clean(home) || p == "/" || covered(p) {
			return
		}
		if found[p] == nil {
			_, gerr := os.Stat(filepath.Join(p, ".git"))
			found[p] = &Found{Path: p, Name: filepath.Base(p), Git: gerr == nil}
		}
	}
	for _, p := range claudeFolders(claude.Home(), home) {
		add(p)
	}
	out := make([]Found, 0, len(found))
	for p, f := range found {
		broad := false // a folder around other suggestions (like ~/Projects) is a collection, not a project
		for q := range found {
			if strings.HasPrefix(q, p+string(filepath.Separator)) {
				broad = true
				break
			}
		}
		if !broad {
			out = append(out, *f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

// claudeFolders lists the folders Claude Code has been used in, from the
// working folder each chat records.
func claudeFolders(claudeHome, home string) []string {
	files, _ := filepath.Glob(filepath.Join(claudeHome, "projects", "*", "*.jsonl"))
	seen := map[string]bool{}
	var out []string
	tmp := os.TempDir()
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for i := 0; i < 40 && sc.Scan(); i++ {
			var l struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(sc.Bytes(), &l) != nil || l.Cwd == "" {
				continue
			}
			cwd := filepath.Clean(l.Cwd)
			if !seen[cwd] {
				seen[cwd] = true
				if fi, err := os.Stat(cwd); err == nil && fi.IsDir() && !strings.HasPrefix(cwd, tmp) && !strings.Contains(cwd, string(filepath.Separator)+".") &&
					cwd != filepath.Clean(home) && (runtime.GOOS == "windows" || cwd != "/") {
					out = append(out, cwd)
				}
			}
			break
		}
		fh.Close()
	}
	return out
}
