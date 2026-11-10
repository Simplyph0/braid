// Package store keeps encrypted objects and strand pointers. The hub serves
// one; tests use one on disk as the shared "hub".
package store

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	ErrNotFound = errors.New("not found")
	ErrMoved    = errors.New("strand moved") // someone else updated it first
)

// Store holds sealed objects by kind and id, and named refs (strand tips).
type Store interface {
	Put(kind, id string, blob []byte) error
	Get(kind, id string) ([]byte, error)
	Has(kind, id string) (bool, error)
	Ref(name string) (string, error) // "" when there is none
	// SetRef moves name from old to new only if it still points at old
	// (compare-and-swap). new "" deletes it.
	SetRef(name, old, new string) error
	Refs(prefix string) (map[string]string, error)
	// Notes are small replaceable blobs (a PC's status), kept by name.
	SetNote(name string, blob []byte) error
	Note(name string) ([]byte, error)
	Notes(prefix string) ([]string, error)
}

const maxNote = 64 << 10

var (
	idRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	kindRe = regexp.MustCompile(`^[a-z]{1,16}$`)
	refRe  = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
)

func ValidRef(name string) bool {
	return refRe.MatchString(name) && !strings.Contains(name, "..") && len(name) <= 200
}

// Dir is a Store in a folder.
type Dir struct {
	Root string
	mu   sync.Mutex // shortcut: refs are only safe within one process; the hub is the one writer
}

func (d *Dir) obj(kind, id string) (string, error) {
	if !kindRe.MatchString(kind) || !idRe.MatchString(id) {
		return "", errors.New("bad object name")
	}
	return filepath.Join(d.Root, "objects", kind, id[:2], id), nil
}

func (d *Dir) Put(kind, id string, blob []byte) error {
	p, err := d.obj(kind, id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return writeAtomic(p, blob)
}

func (d *Dir) Get(kind, id string) ([]byte, error) {
	p, err := d.obj(kind, id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (d *Dir) Has(kind, id string) (bool, error) {
	p, err := d.obj(kind, id)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (d *Dir) refPath(name string) (string, error) {
	if !ValidRef(name) {
		return "", errors.New("bad ref name")
	}
	return filepath.Join(d.Root, "refs", filepath.FromSlash(name)), nil
}

func (d *Dir) Ref(name string) (string, error) {
	p, err := d.refPath(name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

func (d *Dir) SetRef(name, old, new string) error {
	if new != "" && !idRe.MatchString(new) {
		return errors.New("bad ref value")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur, err := d.Ref(name)
	if err != nil {
		return err
	}
	if cur != old {
		return ErrMoved
	}
	p, _ := d.refPath(name)
	if new == "" {
		return os.Remove(p)
	}
	return writeAtomic(p, []byte(new+"\n"))
}

func (d *Dir) Refs(prefix string) (map[string]string, error) {
	out := map[string]string{}
	root := filepath.Join(d.Root, "refs")
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		name := filepath.ToSlash(rel)
		if strings.HasPrefix(name, prefix) {
			if v, err := d.Ref(name); err == nil && v != "" {
				out[name] = v
			}
		}
		return nil
	})
	return out, err
}

func writeAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (d *Dir) notePath(name string) (string, error) {
	if !ValidRef(name) {
		return "", errors.New("bad note name")
	}
	return filepath.Join(d.Root, "notes", filepath.FromSlash(name)), nil
}

func (d *Dir) SetNote(name string, blob []byte) error {
	if len(blob) > maxNote {
		return errors.New("note too big")
	}
	p, err := d.notePath(name)
	if err != nil {
		return err
	}
	return writeAtomic(p, blob)
}

func (d *Dir) Note(name string) ([]byte, error) {
	p, err := d.notePath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (d *Dir) Notes(prefix string) ([]string, error) {
	var out []string
	root := filepath.Join(d.Root, "notes")
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if name := filepath.ToSlash(rel); strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
		return nil
	})
	return out, err
}
