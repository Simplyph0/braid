// Package timeline is Braid's core: a project is a history of moments (whole
// states of the folder), PCs record into it and show a moment of it, and
// strands that grew apart are braided back together.
package timeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
)

const chunkSize = 4 << 20

// Entry is one path in a moment: a folder, a symlink or a file.
type Entry struct {
	Dir    bool      `json:"d,omitempty"`
	Link   string    `json:"l,omitempty"`
	Exec   bool      `json:"x,omitempty"`
	Size   int64     `json:"s,omitempty"`
	Chunks []string  `json:"c,omitempty"`
	Mtime  time.Time `json:"t,omitempty"` // when it last changed, for "newest wins"
}

// same reports equal content (times don't count).
func (e Entry) same(o Entry) bool {
	return e.Dir == o.Dir && e.Link == o.Link && e.Exec == o.Exec && e.Size == o.Size && slices.Equal(e.Chunks, o.Chunks)
}

func (e Entry) isFile() bool { return !e.Dir && e.Link == "" }

// Tree is every path in a moment, slash-separated from the project root.
type Tree struct {
	Files map[string]Entry `json:"files"`
}

// Moment is one recorded state of the project.
type Moment struct {
	Tree    string    `json:"tree"`
	Parents []string  `json:"parents,omitempty"`
	PC      string    `json:"pc"`
	Time    time.Time `json:"time"`
	Intent  string    `json:"intent,omitempty"`
}

// Repo is one project's timeline in a store.
type Repo struct {
	S       store.Store
	Project string // ref prefix, e.g. "pixelbit"
	key     []byte
	names   crypt.Namer

	mu      sync.Mutex
	moments map[string]*Moment
}

func NewRepo(s store.Store, project string, key []byte) (*Repo, error) {
	if !store.ValidRef(project) {
		return nil, fmt.Errorf("bad project name %q", project)
	}
	return &Repo{S: s, Project: project, key: key, names: crypt.NewNamer(key), moments: map[string]*Moment{}}, nil
}

// Strand names: "main", and "pc/<name>" for a PC's work that isn't braided in yet.
func (r *Repo) ref(strand string) string { return r.Project + "/" + strand }

func (r *Repo) Tip(strand string) (string, error) { return r.S.Ref(r.ref(strand)) }

func (r *Repo) SetTip(strand, old, new string) error { return r.S.SetRef(r.ref(strand), old, new) }

func (r *Repo) put(kind string, pt []byte) (string, error) {
	id := r.names.ID(kind, pt)
	if ok, err := r.S.Has(kind, id); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}
	blob, err := crypt.Seal(r.key, pt, []byte(kind+id))
	if err != nil {
		return "", err
	}
	return id, r.S.Put(kind, id, blob)
}

func (r *Repo) get(kind, id string) ([]byte, error) {
	blob, err := r.S.Get(kind, id)
	if err != nil {
		return nil, fmt.Errorf("%s %.12s: %w", kind, id, err)
	}
	pt, err := crypt.Open(r.key, blob, []byte(kind+id))
	if err != nil {
		return nil, err
	}
	if r.names.ID(kind, pt) != id {
		return nil, errors.New("object doesn't match its name")
	}
	return pt, nil
}

func (r *Repo) putJSON(kind string, v any) (string, error) {
	b, err := json.Marshal(v) // map keys come out sorted, so equal trees get equal ids
	if err != nil {
		return "", err
	}
	return r.put(kind, b)
}

func (r *Repo) PutTree(t *Tree) (string, error) { return r.putJSON("tree", t) }

func (r *Repo) Tree(id string) (*Tree, error) {
	t := &Tree{Files: map[string]Entry{}}
	if id == "" {
		return t, nil
	}
	b, err := r.get("tree", id)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, t); err != nil {
		return nil, err
	}
	if t.Files == nil {
		t.Files = map[string]Entry{}
	}
	return t, nil
}

func (r *Repo) PutMoment(m *Moment) (string, error) {
	id, err := r.putJSON("moment", m)
	if err == nil {
		r.mu.Lock()
		r.moments[id] = m
		r.mu.Unlock()
	}
	return id, err
}

func (r *Repo) Moment(id string) (*Moment, error) {
	r.mu.Lock()
	m := r.moments[id]
	r.mu.Unlock()
	if m != nil {
		return m, nil
	}
	b, err := r.get("moment", id)
	if err != nil {
		return nil, err
	}
	m = &Moment{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.moments[id] = m
	r.mu.Unlock()
	return m, nil
}

// MomentTree loads the tree of a moment ("" is the empty tree).
func (r *Repo) MomentTree(id string) (*Tree, error) {
	if id == "" {
		return r.Tree("")
	}
	m, err := r.Moment(id)
	if err != nil {
		return nil, err
	}
	return r.Tree(m.Tree)
}

// putFile stores a file's content in chunks.
func (r *Repo) putFile(f io.Reader) (size int64, chunks []string, err error) {
	buf := make([]byte, chunkSize)
	for {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			id, err := r.put("chunk", buf[:n])
			if err != nil {
				return 0, nil, err
			}
			chunks = append(chunks, id)
			size += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return size, chunks, nil
		}
		if rerr != nil {
			return 0, nil, rerr
		}
	}
}

func (r *Repo) putBytes(b []byte) (int64, []string, error) { return r.putFile(bytes.NewReader(b)) }

// read returns a file entry's content.
func (r *Repo) read(e Entry) ([]byte, error) {
	var out []byte
	for _, c := range e.Chunks {
		b, err := r.get("chunk", c)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	if int64(len(out)) != e.Size {
		return nil, errors.New("file content has the wrong size")
	}
	return out, nil
}

// Ancestors reports whether a is b or one of b's ancestors.
func (r *Repo) IsAncestor(a, b string) (bool, error) {
	if a == "" {
		return true, nil
	}
	seen := map[string]bool{}
	queue := []string{b}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == a {
			return true, nil
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		m, err := r.Moment(id)
		if err != nil {
			return false, err
		}
		queue = append(queue, m.Parents...)
	}
	return false, nil
}

// base finds the newest moment both a and b grew from ("" if none).
func (r *Repo) base(a, b string) (string, error) {
	inA := map[string]bool{}
	queue := []string{a}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == "" || inA[id] {
			continue
		}
		inA[id] = true
		m, err := r.Moment(id)
		if err != nil {
			return "", err
		}
		queue = append(queue, m.Parents...)
	}
	seen := map[string]bool{}
	queue = []string{b}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == "" || seen[id] {
			continue
		}
		if inA[id] {
			return id, nil
		}
		seen[id] = true
		m, err := r.Moment(id)
		if err != nil {
			return "", err
		}
		queue = append(queue, m.Parents...)
	}
	return "", nil
}

// History lists a strand's moments, newest first, following first parents.
func (r *Repo) History(tip string, max int) ([]string, error) {
	var out []string
	for id := tip; id != "" && len(out) < max; {
		out = append(out, id)
		m, err := r.Moment(id)
		if err != nil {
			return out, err
		}
		if len(m.Parents) == 0 {
			break
		}
		id = m.Parents[0]
	}
	return out, nil
}

// PutNote stores v, encrypted, as this project's note name (replacing it).
func (r *Repo) PutNote(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	full := r.ref(name)
	blob, err := crypt.Seal(r.key, b, []byte("note"+full))
	if err != nil {
		return err
	}
	return r.S.SetNote(full, blob)
}

// Note reads a note into v.
func (r *Repo) Note(name string, v any) error {
	full := r.ref(name)
	blob, err := r.S.Note(full)
	if err != nil {
		return err
	}
	b, err := crypt.Open(r.key, blob, []byte("note"+full))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// NoteNames lists this project's notes under prefix, without the project.
func (r *Repo) NoteNames(prefix string) ([]string, error) {
	l, err := r.S.Notes(r.ref(prefix))
	for i := range l {
		l[i] = strings.TrimPrefix(l[i], r.Project+"/")
	}
	return l, err
}

// SameContent reports whether two entries hold the same thing.
func SameContent(a, b Entry) bool { return a.same(b) }

// Change is one path a moment changed.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // added, changed or removed
}

// Changes says which files moment id changed compared with its first parent.
// The .git folder's many files are counted, not listed.
func (r *Repo) Changes(id string) (files []Change, git int, err error) {
	m, err := r.Moment(id)
	if err != nil {
		return nil, 0, err
	}
	parent := ""
	if len(m.Parents) > 0 {
		parent = m.Parents[0]
	}
	before, err := r.MomentTree(parent)
	if err != nil {
		return nil, 0, err
	}
	after, err := r.Tree(m.Tree)
	if err != nil {
		return nil, 0, err
	}
	files = []Change{}
	note := func(p, kind string) {
		if strings.HasPrefix(p, ".git/") {
			git++
		} else {
			files = append(files, Change{p, kind})
		}
	}
	for p, e := range after.Files {
		if e.Dir {
			continue
		}
		if b, had := before.Files[p]; !had {
			note(p, "added")
		} else if !b.same(e) {
			note(p, "changed")
		}
	}
	for p, e := range before.Files {
		if _, ok := after.Files[p]; !ok && !e.Dir {
			note(p, "removed")
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, git, nil
}
