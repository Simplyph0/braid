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

// Found is a folder that looks like a project.
type Found struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Git    bool   `json:"git"`
	Claude bool   `json:"claude"` // you've used it with Claude Code
}

// Files whose presence marks a folder as a project (besides .git).
var markers = []string{"package.json", "go.mod", "Cargo.toml", "pyproject.toml", "requirements.txt", "pom.xml", "build.gradle",
	"CMakeLists.txt", "Makefile", "platformio.ini", "composer.json", "Gemfile", "pubspec.yaml", "deno.json", "CLAUDE.md"}

var roots = []string{"Projects", "projects", "Project", "dev", "Dev", "code", "Code", "src", "repos", "workspace", "work", "git", "GitHub",
	"Documents/GitHub", "Documents/Projects", "Documents/Code", "Developer", "source/repos", "Sites"}

var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "target": true, "build": true, "dist": true, "venv": true, "__pycache__": true}

// ScanFolders looks for project folders you haven't added yet: inside the
// usual code folders (three levels deep), and wherever you've used Claude
// Code. have is the folders already added.
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
	add := func(p string, claudeUsed bool) {
		p = filepath.Clean(p)
		if p == filepath.Clean(home) || p == "/" || covered(p) {
			return
		}
		if f := found[p]; f != nil {
			f.Claude = f.Claude || claudeUsed
			return
		}
		_, gerr := os.Stat(filepath.Join(p, ".git"))
		found[p] = &Found{Path: p, Name: filepath.Base(p), Git: gerr == nil, Claude: claudeUsed}
	}
	isProject := func(dir string) bool {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return true
			}
		}
		return false
	}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || skipDirs[name] {
				continue
			}
			p := filepath.Join(dir, name)
			if isProject(p) {
				add(p, false) // a project: don't look inside it for more
			} else if depth < 3 {
				walk(p, depth+1)
			}
		}
	}
	for _, r := range roots {
		walk(filepath.Join(home, filepath.FromSlash(r)), 1)
	}
	for _, p := range claudeFolders(claude.Home(), home) {
		add(p, true)
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Claude != out[j].Claude {
			return out[i].Claude
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
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
