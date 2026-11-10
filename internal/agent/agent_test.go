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

func newPC(t *testing.T, url, name, pass string) *pc {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // never touch the real chats
	dir := t.TempDir()
	key, token, err := Connect(url, pass)
	if err != nil {
		t.Fatal(err)
	}
	s := &store.HTTP{URL: url, Token: token}
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

func (p *pc) setPin(prefix string) {
	p.t.Helper()
	c, _ := LoadConfig(p.dir)
	r, err := c.Repo(p.a.Store, "proj")
	if err != nil {
		p.t.Fatal(err)
	}
	id := ""
	if prefix != "" {
		if id, err = p.a.Resolve(r, prefix); err != nil {
			p.t.Fatal(err)
		}
	}
	old, _ := r.Tip(PinStrand(c.PC))
	if err := r.SetTip(PinStrand(c.PC), old, id); err != nil {
		p.t.Fatal(err)
	}
}

// status as the web app sees it: from the hub, decrypted.
func (p *pc) published() Status {
	p.t.Helper()
	c, _ := LoadConfig(p.dir)
	r, _ := c.Repo(p.a.Store, "proj")
	var st Status
	if err := r.Note("status/"+c.PC, &st); err != nil {
		p.t.Fatal(err)
	}
	return st
}

func TestTwoPCsThroughAHub(t *testing.T) {
	hub := httptest.NewServer(store.Handler(&store.Dir{Root: t.TempDir()}))
	defer hub.Close()
	a := newPC(t, hub.URL, "Desktop", "correct horse")
	b := newPC(t, hub.URL, "Laptop", "correct horse")

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

	if st := b.published(); len(st.Conflicts) != 1 || st.PC != "Laptop" || st.Folder != b.folder {
		t.Fatalf("hub doesn't show the laptop's state: %+v", st)
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

func TestOnlyThePassphraseGetsIn(t *testing.T) {
	hub := httptest.NewServer(store.Handler(&store.Dir{Root: t.TempDir()}))
	defer hub.Close()
	_, tok, err := Connect(hub.URL, "first passphrase") // sets the hub's passphrase
	if err != nil {
		t.Fatal(err)
	}
	if _, again, err := Connect(hub.URL, "first passphrase"); err != nil || again != tok {
		t.Fatalf("same passphrase again: %v", err)
	}
	if _, _, err := Connect(hub.URL, "other passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("want ErrWrongPassphrase, got %v", err)
	}
	if _, err := (&store.HTTP{URL: hub.URL, Token: "nope"}).Refs(""); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if _, err := (&store.HTTP{URL: hub.URL, Token: tok}).Refs(""); err != nil {
		t.Fatalf("the right token was refused: %v", err)
	}
}
