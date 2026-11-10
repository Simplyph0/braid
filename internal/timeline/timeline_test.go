package timeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
)

// world is a hub (a shared store) and some PCs, each with its own folder.
type world struct {
	t   *testing.T
	dir string
	hub *store.Dir
	key []byte
	mu  sync.Mutex
	now time.Time
	pcs map[string]*pc
}

type pc struct {
	w    *world
	name string
	ws   *Workspace
}

func newWorld(t *testing.T) *world {
	d := t.TempDir()
	return &world{t: t, dir: d, hub: &store.Dir{Root: filepath.Join(d, "hub")}, key: crypt.NewKey(),
		now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC), pcs: map[string]*pc{}}
}

// clock: every reading is a second later, so later work is newer.
func (w *world) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(time.Second)
	return w.now
}

func (w *world) repo() *Repo {
	r, err := NewRepo(w.hub, "proj", w.key)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *world) pc(name string) *pc {
	folder := filepath.Join(w.dir, name)
	os.MkdirAll(folder, 0o755)
	ws, err := Open(w.repo(), name, folder, filepath.Join(w.dir, "state-"+name+".json"))
	if err != nil {
		w.t.Fatal(err)
	}
	ws.Settle, ws.Now = 0, w.clock
	p := &pc{w: w, name: name, ws: ws}
	w.pcs[name] = p
	return p
}

func (p *pc) path(rel string) string { return filepath.Join(p.ws.Folder, filepath.FromSlash(rel)) }

func (p *pc) write(rel, content string) {
	p.w.t.Helper()
	os.MkdirAll(filepath.Dir(p.path(rel)), 0o755)
	if err := os.WriteFile(p.path(rel), []byte(content), 0o644); err != nil {
		p.w.t.Fatal(err)
	}
}

func (p *pc) read(rel string) string {
	b, err := os.ReadFile(p.path(rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (p *pc) files() []string {
	var out []string
	filepath.WalkDir(p.ws.Folder, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(p.ws.Folder, path)
			rel = filepath.ToSlash(rel)
			if !strings.HasPrefix(rel, ".git/") {
				out = append(out, rel)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (p *pc) sync() Report {
	p.w.t.Helper()
	r, err := p.ws.Sync(context.Background())
	if err != nil {
		p.w.t.Fatalf("%s: sync: %v", p.name, err)
	}
	return r
}

func eq[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !equalAny(got, want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func equalAny(a, b any) bool {
	if x, ok := a.([]string); ok {
		return slices.Equal(x, b.([]string))
	}
	return a == b
}

func TestSwitchingPCs(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("src/main.c", "int main(){}\n")
	a.write("README.md", "hi\n")
	os.MkdirAll(a.path("empty"), 0o755)
	os.WriteFile(a.path("run.sh"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink("src/main.c", a.path("link.c"))
	a.write("node_modules/x/index.js", "skip me")
	if r := a.sync(); r.Recorded == "" {
		t.Fatal("nothing recorded")
	}
	b.sync()
	eq(t, "laptop files", b.files(), []string{"README.md", "link.c", "run.sh", "src/main.c"})
	if fi, err := os.Stat(b.path("run.sh")); err != nil || fi.Mode()&0o111 == 0 {
		t.Error("exec bit lost")
	}
	if tgt, _ := os.Readlink(b.path("link.c")); tgt != "src/main.c" {
		t.Errorf("symlink: %q", tgt)
	}
	if fi, err := os.Stat(b.path("empty")); err != nil || !fi.IsDir() {
		t.Error("empty folder missing")
	}

	// Laptop edits and deletes; Desktop follows. Its node_modules stays.
	b.write("README.md", "hello\n")
	os.Remove(b.path("src/main.c"))
	b.sync()
	a.sync()
	eq(t, "desktop sees edit", a.read("README.md"), "hello\n")
	eq(t, "desktop sees delete", a.read("src/main.c"), "<missing>")
	eq(t, "skipped files stay", a.read("node_modules/x/index.js"), "skip me")

	// Nothing changed: no new moment.
	if r := a.sync(); r.Recorded != "" || r.Moved {
		t.Errorf("idle sync did something: %+v", r)
	}
}

func TestOfflineWorkOnBothPCsIsBraided(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("a.txt", "1\n")
	a.sync()
	b.sync()
	// Apart: different files, and different lines of the same file.
	a.write("notes.md", "line1\nline2\nline3\nline4\nline5\n")
	a.sync()
	b.sync()
	a.write("only-desktop.txt", "d")
	a.write("notes.md", "LINE1\nline2\nline3\nline4\nline5\n")
	b.write("only-laptop.txt", "l")
	b.write("notes.md", "line1\nline2\nline3\nline4\nLINE5\n")
	a.sync() // Desktop gets there first: main moves on
	r := b.sync()
	if r.Braided == "" || len(r.Conflicts) > 0 {
		t.Fatalf("not braided: %+v", r)
	}
	a.sync()
	for _, p := range []*pc{a, b} {
		eq(t, p.name+" files", p.files(), []string{"a.txt", "notes.md", "only-desktop.txt", "only-laptop.txt"})
		eq(t, p.name+" merged lines", p.read("notes.md"), "LINE1\nline2\nline3\nline4\nLINE5\n")
	}
	if tip, _ := w.repo().Tip("pc/Laptop"); tip != "" {
		t.Error("braided strand left behind")
	}
	m, _ := w.repo().Moment(r.Braided)
	if len(m.Parents) != 2 {
		t.Errorf("a braid has two parents: %+v", m)
	}
}

func TestOverlapWaitsOnItsOwnStrand(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("x.txt", "one\n")
	a.sync()
	b.sync()
	a.write("x.txt", "desktop\n")
	b.write("x.txt", "laptop\n")
	a.sync()
	r := b.sync()
	if len(r.Conflicts) != 1 || r.Conflicts[0].Path != "x.txt" {
		t.Fatalf("expected a conflict on x.txt: %+v", r)
	}
	eq(t, "laptop keeps its work", b.read("x.txt"), "laptop\n")
	if tip, _ := w.repo().Tip("pc/Laptop"); tip != b.ws.At() {
		t.Error("laptop's work isn't on its strand")
	}
	// A merger (Claude, later) resolves it at the next sync.
	b.ws.Merger = fakeMerger{}
	r = b.sync()
	if r.Braided == "" {
		t.Fatalf("merger didn't braid: %+v", r)
	}
	eq(t, "merged", b.read("x.txt"), "desktop+laptop\n")
	a.sync()
	eq(t, "desktop follows", a.read("x.txt"), "desktop+laptop\n")
}

type fakeMerger struct{}

func (fakeMerger) Merge(_ context.Context, _ string, _, ours, theirs []byte, _, _ string) ([]byte, error) {
	return []byte(strings.TrimSpace(string(ours)) + "+" + string(theirs)), nil
}

func TestBinaryNewestWinsOtherKept(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("img.bin", "\x00base")
	a.sync()
	b.sync()
	a.write("img.bin", "\x00desktop")
	a.sync()
	b.write("img.bin", "\x00laptop") // recorded later: newer
	b.sync()
	a.sync()
	eq(t, "newest wins", a.read("img.bin"), "\x00laptop")
	// The older one is still in the timeline.
	r := w.repo()
	tip, _ := r.Tip("main")
	m, _ := r.Moment(tip)
	found := false
	for _, p := range m.Parents {
		t2, _ := r.MomentTree(p)
		if d, err := r.read(t2.Files["img.bin"]); err == nil && string(d) == "\x00desktop" {
			found = true
		}
	}
	if !found {
		t.Error("the desktop's version isn't kept in the timeline")
	}
}

func TestMissingFolderIsNotDeleted(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("a.txt", "1")
	a.sync()
	b.sync()
	os.RemoveAll(a.ws.Folder)
	if _, err := a.ws.Sync(context.Background()); !errors.Is(err, ErrFolderMissing) {
		t.Fatalf("want ErrFolderMissing, got %v", err)
	}
	b.sync()
	eq(t, "laptop untouched", b.read("a.txt"), "1")
}

func TestWaitsForGit(t *testing.T) {
	w := newWorld(t)
	a := w.pc("Desktop")
	a.write("a.txt", "1")
	a.write(".git/index.lock", "")
	if _, err := a.ws.Sync(context.Background()); !errors.Is(err, ErrGitBusy) {
		t.Fatalf("want ErrGitBusy, got %v", err)
	}
	os.Remove(a.path(".git/index.lock"))
	if r := a.sync(); r.Recorded == "" {
		t.Error("not recorded after git finished")
	}
}

func TestJumpRecordsFirstAndRestore(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("f.txt", "v1")
	v1 := a.sync().Recorded
	a.write("f.txt", "v2")
	a.sync()
	b.sync()
	b.write("f.txt", "v3 unsaved work")
	if _, err := b.ws.Jump(context.Background(), v1); err != nil {
		t.Fatal(err)
	}
	eq(t, "shows v1", b.read("f.txt"), "v1")
	tip, _ := w.repo().Tip("main")
	if d := treeText(t, w.repo(), tip, "f.txt"); d != "v3 unsaved work" {
		t.Errorf("work before the jump wasn't kept: %q", d)
	}
	// Restore v1 for everyone.
	if _, err := w.repo().Restore(v1, "phone"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	eq(t, "restored everywhere", a.read("f.txt"), "v1")
	b.ws.Follow(context.Background())
	eq(t, "laptop follows again", b.read("f.txt"), "v1")
}

func treeText(t *testing.T, r *Repo, moment, rel string) string {
	tr, err := r.MomentTree(moment)
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.read(tr.Files[rel])
	if err != nil {
		return "<missing>"
	}
	return string(d)
}

func TestEditInProgressIsNotOverwritten(t *testing.T) {
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	a.write("f.txt", "1")
	a.sync()
	b.sync()
	a.write("f.txt", "2")
	a.sync()
	b.write("f.txt", "typing...")
	b.ws.Settle = time.Hour // everything on the laptop counts as being written
	r := b.sync()
	eq(t, "not clobbered", b.read("f.txt"), "typing...")
	if r.Busy != 1 {
		t.Errorf("busy = %d", r.Busy)
	}
	b.ws.Settle = 0
	b.sync()
	eq(t, "the laptop's later edit is kept", b.read("f.txt"), "typing...")
	a.sync()
	eq(t, "and reaches the desktop", a.read("f.txt"), "typing...")
}

func TestHostileTreeIsRefused(t *testing.T) {
	w := newWorld(t)
	b := w.pc("Laptop")
	r := w.repo()
	size, chunks, _ := r.putBytes([]byte("pwned"))
	tid, _ := r.PutTree(&Tree{Files: map[string]Entry{"../evil.txt": {Size: size, Chunks: chunks}}})
	mid, _ := r.PutMoment(&Moment{Tree: tid, PC: "evil", Time: time.Now()})
	r.SetTip("main", "", mid)
	if _, err := b.ws.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "evil.txt")); err == nil {
		t.Fatal("wrote outside the folder")
	}
}

func TestWrongKeyCantRead(t *testing.T) {
	w := newWorld(t)
	a := w.pc("Desktop")
	a.write("a.txt", "secret")
	a.sync()
	other, _ := NewRepo(w.hub, "proj", crypt.NewKey())
	tip, _ := other.Tip("main")
	if _, err := other.Moment(tip); err == nil {
		t.Fatal("read with the wrong key")
	}
}

// Git travels inside the timeline: branch, commits and all.
func TestGitRepoTravelsAndBraids(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	w := newWorld(t)
	a, b := w.pc("Desktop"), w.pc("Laptop")
	git := func(p *pc, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", p.ws.Folder, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(a, "init", "-q", "-b", "main")
	a.write("a.txt", "1\n")
	git(a, "add", ".")
	git(a, "commit", "-qm", "first")
	a.sync()
	b.sync()
	eq(t, "laptop has the history", git(b, "log", "--format=%s"), "first")
	eq(t, "and a clean checkout", git(b, "status", "--porcelain"), "")

	// Apart, each PC commits.
	a.write("desk.txt", "d\n")
	git(a, "add", ".")
	git(a, "commit", "-qm", "desk")
	deskCommit := git(a, "rev-parse", "HEAD")
	b.write("lap.txt", "l\n")
	git(b, "add", ".")
	git(b, "commit", "-qm", "lap")
	lapCommit := git(b, "rev-parse", "HEAD")
	a.sync()
	if r := b.sync(); r.Braided == "" {
		t.Fatalf("not braided: %+v", r)
	}
	a.sync()
	for _, p := range []*pc{a, b} {
		eq(t, p.name+" main is the newer side", git(p, "rev-parse", "main"), lapCommit)
		eq(t, p.name+" desktop's branch is kept", git(p, "rev-parse", "braid/Desktop/main"), deskCommit)
		git(p, "cat-file", "-e", deskCommit)
		git(p, "fsck", "--no-progress")
		eq(t, p.name+" both files there", p.files(), []string{"a.txt", "desk.txt", "lap.txt"})
	}
}
