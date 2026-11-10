package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFindsProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TMPDIR", "/nonexistent-scratch") // the scan skips folders under the system's temp dir; the test's own are there
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	mk := func(rel string, files ...string) string {
		p := filepath.Join(home, filepath.FromSlash(rel))
		os.MkdirAll(p, 0o755)
		for _, f := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(p, f)), 0o755)
			os.WriteFile(filepath.Join(p, f), nil, 0o644)
		}
		return p
	}
	gitp := mk("Projects/alpha", ".git/HEAD")
	mk("Projects/alpha/vendor/lib", "go.mod") // inside a project: not suggested
	mk("Projects/group/beta", "package.json") // two levels down
	mk("Projects/notes")                      // plain folder: not a project
	mk("Projects/node_modules/x", "package.json")
	have := mk("Projects/already", ".git/HEAD")
	used := mk("elsewhere/gamma") // no markers, but used with Claude
	broad := mk("elsewhere")      // used with Claude too, but only holds other suggestions
	mk("Documents/Pictures")
	os.MkdirAll(filepath.Join(home, ".claude", "projects", "x"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "projects", "x", "s.jsonl"),
		[]byte(`{"type":"summary"}`+"\n"+`{"type":"user","cwd":"`+used+`"}`+"\n"), 0o644)

	os.WriteFile(filepath.Join(home, ".claude", "projects", "x", "t.jsonl"), []byte(`{"type":"user","cwd":"`+broad+`"}`+"\n"), 0o644)
	got := ScanFolders(home, []string{have})
	want := map[string]bool{gitp: true, filepath.Join(home, "Projects/group/beta"): true, used: true}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, f := range got {
		if !want[f.Path] {
			t.Errorf("unexpected %s", f.Path)
		}
	}
	if !got[0].Claude || got[0].Path != used {
		t.Errorf("Claude folders should come first: %+v", got[0])
	}
	if got := ScanFolders(home, []string{filepath.Join(home, "Projects")}); len(got) != 1 {
		t.Errorf("everything under an added folder is covered, got %+v", got) // only elsewhere/gamma
	}
}
