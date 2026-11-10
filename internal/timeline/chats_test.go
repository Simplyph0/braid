package timeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
)

func chatLine(uuid, cwd, text string) string {
	return `{"type":"user","uuid":"` + uuid + `","cwd":"` + jsonPath(cwd) + `","message":{"content":"` + text + `"}}` + "\n"
}

func TestChatsTravelAndJoin(t *testing.T) {
	r, _ := NewRepo(&store.Dir{Root: t.TempDir()}, "p", crypt.NewKey())
	type pc struct{ name, folder, dir string }
	a := pc{"Desktop", `/home/alex/Projects/X`, t.TempDir()}
	b := pc{"Laptop", `/Users/j/code/X`, t.TempDir()}
	later := time.Now().Add(time.Hour) // everything has settled
	sync := func(p pc) ChatReport {
		t.Helper()
		rep, err := r.SyncChats(p.dir, p.folder, p.name, 30*time.Second, later)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	read := func(p pc, name string) string { b, _ := os.ReadFile(filepath.Join(p.dir, name)); return string(b) }
	write := func(p pc, name, s string) { os.WriteFile(filepath.Join(p.dir, name), []byte(s), 0o600) }

	write(a, "s1.jsonl", chatLine("u1", a.folder, "hi")+chatLine("u2", a.folder+"/src", "fix it"))
	if rep := sync(a); rep.Pushed != 1 {
		t.Fatalf("desktop: %+v", rep)
	}
	sync(b)
	got := read(b, "s1.jsonl")
	if !strings.Contains(got, jsonPath(b.folder)+`/src"`) || strings.Contains(got, "alex") {
		t.Fatalf("paths not rewritten for the laptop: %s", got)
	}
	// Both continue the chat apart: every message is kept, once.
	write(a, "s1.jsonl", read(a, "s1.jsonl")+chatLine("u3", a.folder, "from desktop"))
	write(b, "s1.jsonl", got+chatLine("u4", b.folder, "from laptop"))
	sync(a)
	sync(b)
	sync(a)
	for _, p := range []pc{a, b} {
		s := read(p, "s1.jsonl")
		for _, u := range []string{"u1", "u2", "u3", "u4"} {
			if strings.Count(s, `"uuid":"`+u+`"`) != 1 {
				t.Errorf("%s: %s appears %d times", p.name, u, strings.Count(s, u))
			}
		}
	}
	if rep := sync(a); rep.Pulled+rep.Pushed != 0 {
		t.Errorf("nothing to do, but %+v", rep)
	}
	// A chat being written right now is left alone.
	write(b, "s2.jsonl", chatLine("u5", b.folder, "half")+`{"type":"user","uuid":"u6"`)
	if rep, _ := r.SyncChats(b.dir, b.folder, b.name, 0, later); rep.Pushed != 0 {
		t.Error("a half-written chat was sent")
	}
	write(b, "s3.jsonl", chatLine("u7", b.folder, "now"))
	if rep, _ := r.SyncChats(b.dir, b.folder, b.name, 30*time.Second, time.Now()); rep.Pushed != 0 {
		t.Error("a chat written seconds ago was sent")
	}
}
