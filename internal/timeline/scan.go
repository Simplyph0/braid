package timeline

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const tmpSuffix = ".braid-tmp"

var (
	ErrFolderMissing = errors.New("the project folder isn't there (moved, deleted, or on a drive that isn't plugged in)")
	ErrGitBusy       = errors.New("git is in the middle of something; waiting for it to finish")
)

// seen is what a path looked like on disk when it was last read or written,
// so unchanged files aren't read again.
type seen struct {
	Size  int64 `json:"s"`
	Mtime int64 `json:"t"`
	Exec  bool  `json:"x,omitempty"`
	Entry Entry `json:"e"`
}

func statKey(fi fs.FileInfo) (int64, int64, bool) {
	return fi.Size(), fi.ModTime().UnixNano(), fi.Mode()&0o111 != 0
}

// gitBusy reports a git command in progress: its lock files.
func gitBusy(root string) bool {
	g := filepath.Join(root, ".git")
	for _, f := range []string{"index.lock", "HEAD.lock", "packed-refs.lock", "config.lock", "shallow.lock"} {
		if _, err := os.Lstat(filepath.Join(g, f)); err == nil {
			return true
		}
	}
	busy := false
	_ = filepath.WalkDir(filepath.Join(g, "refs"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".lock") {
			busy = true
			return filepath.SkipAll
		}
		return nil
	})
	return busy
}

// scan reads the folder into a tree, storing new content. A file changed
// within settle of now is still being written: its last recorded version
// (prev) stands in for it, so half-written files are never recorded.
func (r *Repo) scan(root string, prev *Tree, cache map[string]seen, settle time.Duration, now time.Time) (*Tree, error) {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil, ErrFolderMissing
	}
	m := NewMatcher(root)
	t := &Tree{Files: map[string]Entry{}}
	keep := func(rel string) {
		if e, ok := prev.Files[rel]; ok {
			t.Files[rel] = e
		}
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if p == root {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if m.Ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir():
			t.Files[rel] = Entry{Dir: true}
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err == nil {
				t.Files[rel] = Entry{Link: filepath.ToSlash(target)}
			}
		case info.Mode().IsRegular():
			size, mt, exec := statKey(info)
			if c, ok := cache[rel]; ok && c.Size == size && c.Mtime == mt && c.Exec == exec {
				t.Files[rel] = c.Entry
				return nil
			}
			if settle > 0 && now.Sub(info.ModTime()) < settle {
				keep(rel)
				return nil
			}
			e, ok, err := r.readFile(p, info)
			if err != nil {
				return err
			}
			if !ok { // changed while reading
				keep(rel)
				return nil
			}
			if old, had := prev.Files[rel]; had && old.same(e) {
				e = old // only touched: not a change
			}
			t.Files[rel] = e
			cache[rel] = seen{Size: size, Mtime: mt, Exec: exec, Entry: e}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for rel := range cache {
		if _, ok := t.Files[rel]; !ok {
			delete(cache, rel)
		}
	}
	return t, nil
}

// readFile stores one file. ok is false if it changed while being read.
func (r *Repo) readFile(p string, before fs.FileInfo) (Entry, bool, error) {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	defer f.Close()
	size, chunks, err := r.putFile(f)
	if err != nil {
		return Entry{}, false, err
	}
	after, err := os.Stat(p)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || size != before.Size() {
		return Entry{}, false, nil
	}
	return Entry{Size: size, Chunks: chunks, Exec: before.Mode()&0o111 != 0, Mtime: before.ModTime().UTC()}, true, nil
}

// validPath rejects paths that could land outside the project folder: a
// tree comes from the hub, so it's checked before anything is written.
func validPath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) || strings.Contains(rel, ":") || path.Clean(rel) != rel {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
