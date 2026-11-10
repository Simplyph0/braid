package timeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// apply makes the folder go from tree cur (what it held when last read) to
// tree target. Only paths cur knows about are removed, so skipped files
// (node_modules, …) stay. A path that changed on disk since cur was read is
// left alone: someone is working on it, and the next record picks it up.
// It returns how many paths it left alone.
func (r *Repo) apply(root string, cur, target *Tree, cache map[string]seen) (int, error) {
	for rel := range target.Files {
		if !validPath(rel) {
			return 0, fmt.Errorf("refusing unsafe path %q", rel)
		}
	}
	busy := 0
	untouched := func(rel string) bool {
		e, known := cur.Files[rel]
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			return true // nothing there to lose
		}
		if err != nil {
			return false
		}
		if !known {
			return false // something new we never recorded: don't clobber it
		}
		if e.isFile() {
			c, ok := cache[rel]
			size, mt, _ := statKey(fi)
			return ok && fi.Mode().IsRegular() && c.Size == size && c.Mtime == mt
		}
		return true
	}

	// Removals first, deepest first, so folders are empty by the time they go.
	var gone []string
	for rel := range cur.Files {
		if t, ok := target.Files[rel]; !ok || kindChanged(cur.Files[rel], t) {
			gone = append(gone, rel)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(gone)))
	for _, rel := range gone {
		if !untouched(rel) {
			busy++
			continue
		}
		p, err := safeJoin(root, rel)
		if err != nil {
			return busy, err
		}
		if cur.Files[rel].Dir {
			_ = os.Remove(p) // only if empty: skipped files inside stay
		} else if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return busy, err
		}
		delete(cache, rel)
	}

	// Then writes, shallowest first.
	var put []string
	for rel, e := range target.Files {
		if c, ok := cur.Files[rel]; ok && c.same(e) && !kindChanged(c, e) {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
				continue
			}
		}
		put = append(put, rel)
	}
	sort.Strings(put)
	for _, rel := range put {
		if !untouched(rel) {
			if fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil && fi.IsDir() && target.Files[rel].Dir {
				continue // the folder is already there
			}
			busy++ // being edited, or something unrecorded is in the way
			continue
		}
		if err := r.write(root, rel, target.Files[rel], cache); err != nil {
			return busy, err
		}
	}
	return busy, nil
}

func kindChanged(a, b Entry) bool { return a.Dir != b.Dir || (a.Link != "") != (b.Link != "") }

func (r *Repo) write(root, rel string, e Entry, cache map[string]seen) error {
	p, err := safeJoin(root, rel)
	if err != nil {
		return err
	}
	switch {
	case e.Dir:
		return os.MkdirAll(p, 0o755)
	case e.Link != "":
		if !safeLink(rel, e.Link) {
			return nil // shortcut: a link pointing outside the project isn't made here; it shows as deleted at the next record
		}
		_ = os.Remove(p)
		return os.Symlink(filepath.FromSlash(e.Link), p)
	}
	data, err := r.read(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if e.Exec {
		mode = 0o755
	}
	tmp := p + tmpSuffix
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	_ = os.Chmod(tmp, mode)
	mt := e.Mtime
	if mt.IsZero() {
		mt = time.Now()
	}
	_ = os.Chtimes(tmp, mt, mt)
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	if fi, err := os.Stat(p); err == nil {
		size, t, x := statKey(fi)
		cache[rel] = seen{Size: size, Mtime: t, Exec: x, Entry: e}
	}
	return nil
}

// safeJoin refuses to go through a symlink on the way to rel, so a link in
// the project can't redirect a write outside it.
func safeJoin(root, rel string) (string, error) {
	parts := strings.Split(rel, "/")
	p := root
	for _, part := range parts[:len(parts)-1] {
		p = filepath.Join(p, part)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to write %q through a symlink", rel)
		}
	}
	return filepath.Join(p, parts[len(parts)-1]), nil
}

// safeLink allows relative symlinks that stay inside the project.
func safeLink(rel, target string) bool {
	if strings.HasPrefix(target, "/") || strings.Contains(target, ":") || strings.Contains(target, `\`) {
		return false
	}
	return !strings.HasPrefix(path.Join(path.Dir(rel), target)+"/", "../") && path.Join(path.Dir(rel), target) != ".."
}
