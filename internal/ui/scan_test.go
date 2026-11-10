package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanUsesClaudeCodeFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TMPDIR", "/nonexistent-scratch") // the scan skips folders under the system's temp dir; the test's own are there
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	mk := func(rel string) string {
		p := filepath.Join(home, filepath.FromSlash(rel))
		os.MkdirAll(p, 0o755)
		return p
	}
	alpha, beta, have := mk("Projects/alpha"), mk("Music/beta"), mk("Projects/already")
	mk("Projects/never-used-with-claude/.git") // looks like a project, but Claude was never used there
	broad := mk("Projects")                    // used with Claude too, but only wraps other suggestions
	hidden := mk(".cache/x")
	os.MkdirAll(filepath.Join(alpha, ".git"), 0o755)
	d := filepath.Join(home, ".claude", "projects", "x")
	os.MkdirAll(d, 0o755)
	for i, cwd := range []string{alpha, beta, have, broad, hidden, filepath.Join(home, "deleted"), home} {
		os.WriteFile(filepath.Join(d, string(rune('a'+i))+".jsonl"), []byte(`{"type":"summary"}`+"\n"+`{"type":"user","cwd":"`+cwd+`"}`+"\n"), 0o644)
	}
	got := ScanFolders(home, []string{have})
	if len(got) != 2 || got[0].Path != alpha || !got[0].Git || got[1].Path != beta {
		t.Fatalf("got %+v", got)
	}
}
