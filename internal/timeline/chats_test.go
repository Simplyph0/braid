package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
)

type chatPC struct {
	t    *testing.T
	name string
	set  ChatSet
	st   *ChatState
	home string
}

func newChatPC(t *testing.T, name string) *chatPC {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	desk := filepath.Join(home, ".config", "Claude")
	os.MkdirAll(filepath.Join(desk, "claude-code-sessions", "acct", "org"), 0o755)
	return &chatPC{t: t, name: name, home: home, st: &ChatState{}, set: ChatSet{
		ClaudeHome: filepath.Join(home, ".claude"), SessionsDir: filepath.Join(desk, "claude-code-sessions"),
		ScratchRoot: filepath.Join(desk, "scratch-workspaces"), Home: home, Projects: map[string]string{},
	}}
}

func (p *chatPC) folder(rel string) string {
	f := filepath.Join(p.home, filepath.FromSlash(rel))
	os.MkdirAll(f, 0o755)
	return f
}

// chat writes a transcript for cwd. Its mtime is old, so it counts as settled.
func (p *chatPC) chat(cwd, session string, msgs ...string) string {
	dir := filepath.Join(p.set.ClaudeHome, "projects", nonAlnum.ReplaceAllString(cwd, "-"))
	os.MkdirAll(dir, 0o755)
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(`{"type":"user","uuid":"` + m + `","cwd":"` + jsonPath(cwd) + `","message":{"content":"edit ` + jsonPath(cwd) + `/main.go"}}` + "\n")
	}
	f := filepath.Join(dir, session+".jsonl")
	os.WriteFile(f, []byte(b.String()), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(f, old, old)
	return f
}

func (p *chatPC) sync(r *Repo) ChatReport {
	p.t.Helper()
	rep, err := r.SyncAllChats(p.set, p.name, 30*time.Second, time.Now(), p.st)
	if err != nil {
		p.t.Fatalf("%s: %v", p.name, err)
	}
	return rep
}

func (p *chatPC) read(cwd, session string) string {
	b, err := os.ReadFile(filepath.Join(p.set.ClaudeHome, "projects", nonAlnum.ReplaceAllString(cwd, "-"), session+".jsonl"))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

type counting struct {
	*store.Dir
	chunkGets int
}

func (c *counting) Get(kind, id string) ([]byte, error) {
	if kind == "chunk" {
		c.chunkGets++
	}
	return c.Dir.Get(kind, id)
}

func TestEveryChatTravelsToTheOtherPC(t *testing.T) {
	hub := &counting{Dir: &store.Dir{Root: t.TempDir()}}
	r, _ := NewRepo(hub, ChatsProject, crypt.NewKey())
	a, b := newChatPC(t, "Desktop"), newChatPC(t, "Laptop")
	aX := a.folder("Projects/X")
	a.set.Projects["X"] = aX
	aY := a.folder("code/Y") // not a Braid project: found through the home folder
	bX := b.folder("work/X")
	b.set.Projects["X"] = bX
	bY := b.folder("code/Y")

	a.chat(aX, "s1", "m1", "m2")
	a.chat(aY, "s2", "n1")
	os.MkdirAll(filepath.Join(a.set.ClaudeHome, "projects", nonAlnum.ReplaceAllString(aX, "-"), "s1", "subagents"), 0o755)
	side := filepath.Join(a.set.ClaudeHome, "projects", nonAlnum.ReplaceAllString(aX, "-"), "s1", "subagents", "agent-1.jsonl")
	os.WriteFile(side, []byte(`{"uuid":"sa1","cwd":"`+jsonPath(aX)+`"}`+"\n"), 0o600)
	os.Chtimes(side, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	// A desktop-app record for a chat in the project, and a folderless one.
	scratch := filepath.Join(a.set.ScratchRoot, "acct-1")
	rec := func(p *chatPC, id, cwd string, act int) string {
		b, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": cwd, "lastActivityAt": act, "title": "t " + id})
		f := filepath.Join(p.set.SessionsDir, "acct", "org", id+".json")
		os.WriteFile(f, b, 0o644)
		return f
	}
	rec(a, "local_one", aX, 100)
	rec(a, "local_two", scratch, 100)
	a.chat(scratch, "s3", "p1")

	if rep := a.sync(r); rep.Pushed == 0 || rep.Files < 5 {
		t.Fatalf("desktop: %+v", rep)
	}
	// The desktop app is open on the laptop: transcripts arrive, its records wait.
	b.set.AppRunning = true
	rep := b.sync(r)
	if rep.Pending != 2 {
		t.Fatalf("laptop should have 2 records waiting: %+v", rep)
	}
	got := b.read(bX, "s1")
	if !strings.Contains(got, jsonPath(bX)+"/main.go") || strings.Contains(got, aX) || strings.Count(got, `"uuid"`) != 2 {
		t.Fatalf("project chat not brought over with the laptop's paths:\n%s", got)
	}
	if got := b.read(bY, "s2"); !strings.Contains(got, jsonPath(bY)) || strings.Contains(got, aY) {
		t.Fatalf("a chat from a folder under home: %s", got)
	}
	if _, err := os.Stat(filepath.Join(b.set.ClaudeHome, "projects", nonAlnum.ReplaceAllString(bX, "-"), "s1", "subagents", "agent-1.jsonl")); err != nil {
		t.Error("the files beside a chat didn't come:", err)
	}
	if _, err := os.Stat(filepath.Join(b.set.SessionsDir, "acct", "org", "local_one.json")); err == nil {
		t.Fatal("wrote a desktop record while the app was open")
	}
	// The app is closed: the records are written, with the laptop's paths, and a folderless chat gets its folder.
	b.set.AppRunning = false
	if rep := b.sync(r); rep.Pending != 0 {
		t.Fatalf("still pending: %+v", rep)
	}
	var one struct{ Cwd string }
	data, err := os.ReadFile(filepath.Join(b.set.SessionsDir, "acct", "org", "local_one.json"))
	if err != nil || json.Unmarshal(data, &one) != nil || one.Cwd != bX {
		t.Fatalf("desktop record: %s %v", data, err)
	}
	if fi, err := os.Stat(filepath.Join(b.set.ScratchRoot, "acct-1")); err != nil || !fi.IsDir() {
		t.Error("the folderless chat's folder wasn't made")
	}

	// Nothing changed: a sync reads no file content from the hub.
	hub.chunkGets = 0
	for i := 0; i < 3; i++ {
		if rep := a.sync(r); rep.Pulled+rep.Pushed != 0 {
			t.Fatalf("idle: %+v", rep)
		}
		if rep := b.sync(r); rep.Pulled+rep.Pushed != 0 {
			t.Fatalf("idle: %+v", rep)
		}
	}
	if hub.chunkGets != 0 {
		t.Errorf("idle syncs downloaded %d chunks", hub.chunkGets)
	}

	// Both continue the same chat apart: every message ends up on both, once.
	a.chat(aX, "s1", "m1", "m2", "m3")
	b.chat(bX, "s1", "m1", "m2", "m4")
	a.sync(r)
	b.sync(r)
	a.sync(r)
	for _, got := range []string{a.read(aX, "s1"), b.read(bX, "s1")} {
		for _, m := range []string{"m1", "m2", "m3", "m4"} {
			if strings.Count(got, `"uuid":"`+m+`"`) != 1 {
				t.Errorf("%s appears %d times in\n%s", m, strings.Count(got, m), got)
			}
		}
	}
	// A chat written seconds ago is left alone.
	f := b.chat(bX, "s9", "z1")
	os.Chtimes(f, time.Now(), time.Now())
	if rep := b.sync(r); rep.Pushed != 0 {
		t.Errorf("a chat being written was sent: %+v", rep)
	}
}

func TestChatsForAProjectNotHereWaitWithoutBeingDownloaded(t *testing.T) {
	hub := &counting{Dir: &store.Dir{Root: t.TempDir()}}
	r, _ := NewRepo(hub, ChatsProject, crypt.NewKey())
	a, b := newChatPC(t, "Desktop"), newChatPC(t, "Laptop")
	aZ := a.folder("Projects/Z")
	a.set.Projects["Z"] = aZ
	a.chat(aZ, "s1", "m1")
	a.sync(r)
	b.sync(r) // reads which folder each chat is for, once
	hub.chunkGets = 0
	for i := 0; i < 3; i++ {
		b.sync(r)
	}
	if hub.chunkGets != 0 {
		t.Errorf("downloaded %d chunks for chats it can't place", hub.chunkGets)
	}
	bZ := b.folder("Projects/Z") // the project is added on the laptop later
	b.set.Projects["Z"] = bZ
	b.sync(r)
	if got := b.read(bZ, "s1"); !strings.Contains(got, `"uuid":"m1"`) || !strings.Contains(got, jsonPath(bZ)) {
		t.Fatalf("after adding the project: %q", got)
	}
}

func TestRootsReplaceWholePathsOnly(t *testing.T) {
	roots := []root{{"{{p:A}}", "/h/Projects/A"}, {"{{home}}", "/h"}}
	in := []byte(`"/h/Projects/A/x" "/h/Projects/AB/x" "/h/other" "x/h/y"`)
	got := string(replacePaths(in, roots, true))
	if got != `"{{p:A}}/x" "{{home}}/Projects/AB/x" "{{home}}/other" "x/h/y"` {
		t.Errorf("got %s", got)
	}
	if back := string(replacePaths([]byte(got), roots, false)); back != string(in) {
		t.Errorf("round trip: %s", back)
	}
}
