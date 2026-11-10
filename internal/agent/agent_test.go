package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Simplyph0/braid/internal/store"
)

type pc struct {
	t      *testing.T
	dir    string
	folder string
	a      *Agent
}

func newPC(t *testing.T, url, token, name, pass string) *pc {
	dir := t.TempDir()
	s := &store.HTTP{URL: url, Token: token}
	key, err := MasterKey(s, pass)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(folder, 0o755)
	c := &Config{Hub: url, Token: token, PC: name, Key: hex.EncodeToString(key), NoClaude: true, Projects: []Project{{Name: "proj", Folder: folder}}}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	return &pc{t: t, dir: dir, folder: folder, a: &Agent{Dir: dir, Store: s, Log: log.New(io.Discard, "", 0), Settle: -1}}
}

func (p *pc) tick() Status {
	p.t.Helper()
	if err := p.a.Tick(context.Background()); err != nil {
		p.t.Fatal(err)
	}
	st, err := ReadStatus(p.dir, "proj")
	if err != nil {
		p.t.Fatal(err)
	}
	return st
}

func (p *pc) write(rel, s string) { os.WriteFile(filepath.Join(p.folder, rel), []byte(s), 0o644) }
func (p *pc) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(p.folder, rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (p *pc) setPin(pin string) {
	c, _ := LoadConfig(p.dir)
	c.Projects[0].Pin = pin
	c.Save(p.dir)
}

func TestTwoPCsThroughAHub(t *testing.T) {
	hub := httptest.NewServer(store.Handler(&store.Dir{Root: t.TempDir()}, "tok"))
	defer hub.Close()
	a := newPC(t, hub.URL, "tok", "Desktop", "correct horse")
	b := newPC(t, hub.URL, "tok", "Laptop", "correct horse")

	a.write("a.txt", "from desktop")
	if st := a.tick(); st.Error != "" || st.At == "" {
		t.Fatalf("desktop: %+v", st)
	}
	b.tick()
	if got := b.read("a.txt"); got != "from desktop" {
		t.Fatalf("laptop got %q", got)
	}
	first, _ := ReadStatus(b.dir, "proj")

	// Edit on the laptop, follow on the desktop.
	b.write("a.txt", "from laptop")
	b.tick()
	a.tick()
	if got := a.read("a.txt"); got != "from laptop" {
		t.Fatalf("desktop got %q", got)
	}

	// Jump the desktop back by prefix, then follow again.
	a.setPin(first.At[:10])
	if st := a.tick(); !st.Detached || a.read("a.txt") != "from desktop" {
		t.Fatalf("jump: %+v %q", st, a.read("a.txt"))
	}
	a.tick() // stays put, doesn't jump again
	a.setPin("")
	if st := a.tick(); st.Detached || a.read("a.txt") != "from laptop" {
		t.Fatalf("follow: %+v %q", st, a.read("a.txt"))
	}

	// Same line on both: the laptop's work waits, and status says so.
	a.write("a.txt", "desktop again")
	b.write("a.txt", "laptop again")
	a.tick()
	if st := b.tick(); len(st.Conflicts) != 1 || st.Error != "" {
		t.Fatalf("expected a waiting conflict: %+v", st)
	}

	// A missing folder is a problem in status, not a deletion.
	os.RemoveAll(a.folder)
	if st := a.tick(); st.Error == "" {
		t.Fatal("no error for a missing folder")
	}
	if b.read("a.txt") != "laptop again" {
		t.Fatal("laptop changed")
	}
}

func TestWrongPassphraseAndToken(t *testing.T) {
	hub := httptest.NewServer(store.Handler(&store.Dir{Root: t.TempDir()}, "tok"))
	defer hub.Close()
	if _, err := MasterKey(&store.HTTP{URL: hub.URL, Token: "tok"}, "first passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := MasterKey(&store.HTTP{URL: hub.URL, Token: "tok"}, "other passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("want ErrWrongPassphrase, got %v", err)
	}
	if _, err := MasterKey(&store.HTTP{URL: hub.URL, Token: "nope"}, "first passphrase"); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}
