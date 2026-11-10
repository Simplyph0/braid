package timeline

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// MergeInput is one file changed in the same places on two strands.
type MergeInput struct {
	Path                     string
	Base, Ours, Theirs       []byte // Base is empty when both sides added the file
	OursPC, TheirsPC         string
	IntentOurs, IntentTheirs string // what each side was doing, from its moments
	Hint                     string // why the previous attempt was rejected (the check's output)
}

// Merger joins such a file, saying in a few words what it combined. Braid's
// Claude merger plugs in here.
type Merger interface {
	Merge(ctx context.Context, in MergeInput) (merged []byte, summary string, err error)
}

// Check runs the project's check (build, tests) on a braid in dir.
type Check func(ctx context.Context, dir string) error

// Conflict is a path that couldn't be braided.
type Conflict struct {
	Path string
	Why  string
}

// Braid joins strands a and b into one new moment made by pc. When it can't
// (overlapping text changes and no merger, or the merger failed) it returns
// the conflicts and makes nothing: both strands stay as they are.
//
// When the merger joined any file and check is set, the braid is checked
// first; if the check fails, the merger gets one more try with its output.
func (r *Repo) Braid(ctx context.Context, a, b, pc string, m Merger, check Check) (string, []Conflict, error) {
	base, err := r.base(a, b)
	if err != nil {
		return "", nil, err
	}
	switch base {
	case b:
		return a, nil, nil
	case a:
		return b, nil, nil
	}
	ma, err := r.Moment(a)
	if err != nil {
		return "", nil, err
	}
	mb, err := r.Moment(b)
	if err != nil {
		return "", nil, err
	}
	ta, err := r.Tree(ma.Tree)
	if err != nil {
		return "", nil, err
	}
	tb, err := r.Tree(mb.Tree)
	if err != nil {
		return "", nil, err
	}
	t0, err := r.MomentTree(base)
	if err != nil {
		return "", nil, err
	}
	// The newer side wins wherever only one version can be kept.
	aWins := ma.Time.After(mb.Time) || (ma.Time.Equal(mb.Time) && a > b)
	winner, loser := ta, tb
	loserPC := mb.PC
	if !aWins {
		winner, loser, loserPC = tb, ta, ma.PC
	}

	intentA, err := r.sideIntent(base, a)
	if err != nil {
		return "", nil, err
	}
	intentB, err := r.sideIntent(base, b)
	if err != nil {
		return "", nil, err
	}
	out := &Tree{Files: map[string]Entry{}}
	var conflicts []Conflict
	merged := map[string]MergeInput{} // files the merger joined
	var summaries []string
	paths := map[string]bool{}
	for _, t := range []*Tree{ta, tb, t0} {
		for p := range t.Files {
			paths[p] = true
		}
	}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		ea, inA := ta.Files[p]
		eb, inB := tb.Files[p]
		e0, in0 := t0.Files[p]
		eq := func(x Entry, inX bool, y Entry, inY bool) bool { return inX == inY && (!inX || x.same(y)) }
		take := func(e Entry, in bool) {
			if in {
				out.Files[p] = e
			}
		}
		switch {
		case eq(ea, inA, eb, inB):
			take(ea, inA)
		case eq(ea, inA, e0, in0): // only b changed it
			take(eb, inB)
		case eq(eb, inB, e0, in0): // only a changed it
			take(ea, inA)
		case strings.HasPrefix(p, ".git/"):
			we, wIn := winner.Files[p]
			take(we, wIn)
			le, lIn := loser.Files[p]
			if lIn && strings.HasPrefix(p, ".git/refs/heads/") {
				out.Files[".git/refs/heads/braid/"+safeName(loserPC)+"/"+strings.TrimPrefix(p, ".git/refs/heads/")] = le
			}
		case !inA || !inB: // deleted on one side, changed on the other: keep the change
			take(ea, inA)
			take(eb, inB)
		case ea.isFile() && eb.isFile():
			e, c, in, sum, err := r.mergeFile(ctx, p, e0, in0, ea, eb, aWins, m,
				MergeInput{OursPC: ma.PC, TheirsPC: mb.PC, IntentOurs: intentA, IntentTheirs: intentB})
			if err != nil {
				return "", nil, err
			}
			if c != "" {
				conflicts = append(conflicts, Conflict{Path: p, Why: c})
				continue
			}
			if in != nil {
				merged[p] = *in
				summaries = append(summaries, sum)
			}
			out.Files[p] = e
		default: // a folder on one side, a file or link on the other
			we, wIn := winner.Files[p]
			take(we, wIn)
		}
	}
	if len(conflicts) > 0 {
		return "", conflicts, nil
	}
	// A folder kept on one side may still hold a file from the other: make sure
	// every file's parent folders exist as folders.
	for p := range out.Files {
		for d := pathDir(p); d != ""; d = pathDir(d) {
			if e, ok := out.Files[d]; !ok || !e.Dir {
				out.Files[d] = Entry{Dir: true}
			}
		}
	}
	if err := r.braidPackedRefs(out, winner, loser, t0, loserPC); err != nil {
		return "", nil, err
	}
	if len(merged) > 0 && check != nil {
		cerr := r.check(ctx, out, check)
		if cerr != nil { // one more try, telling the merger what went wrong
			summaries = nil
			for p, in := range merged {
				in.Hint = cerr.Error()
				data, sum, err := m.Merge(ctx, in)
				if err != nil {
					return "", []Conflict{{Path: p, Why: "the merge failed the project's check: " + cerr.Error()}}, nil
				}
				size, chunks, err := r.putBytes(data)
				if err != nil {
					return "", nil, err
				}
				e := out.Files[p]
				e.Size, e.Chunks = size, chunks
				out.Files[p] = e
				summaries = append(summaries, sum)
			}
			if cerr = r.check(ctx, out, check); cerr != nil {
				for p := range merged {
					conflicts = append(conflicts, Conflict{Path: p, Why: "the merge failed the project's check: " + cerr.Error()})
				}
				sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })
				return "", conflicts, nil
			}
		}
	}
	intent := fmt.Sprintf("Braided %s's work with %s's", ma.PC, mb.PC)
	if len(summaries) > 0 {
		sort.Strings(summaries)
		intent += ": " + strings.Join(summaries, "; ")
	}
	tid, err := r.PutTree(out)
	if err != nil {
		return "", nil, err
	}
	mid, err := r.PutMoment(&Moment{Tree: tid, Parents: []string{a, b}, PC: pc, Time: time.Now().UTC(),
		Intent: intent})
	return mid, nil, err
}

func pathDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string {
	s = strings.Trim(unsafeName.ReplaceAllString(s, "-"), "-.")
	if s == "" {
		return "pc"
	}
	return s
}

// mergeFile joins two changed versions of one file. It returns a reason
// instead when it can't.
// in is set when the merger did the joining.
func (r *Repo) mergeFile(ctx context.Context, p string, e0 Entry, in0 bool, ea, eb Entry, aWins bool, m Merger, in MergeInput) (e Entry, why string, used *MergeInput, summary string, err error) {
	newest := func() Entry {
		if aWins {
			return ea
		}
		return eb
	}
	da, err := r.read(ea)
	if err != nil {
		return Entry{}, "", nil, "", err
	}
	db, err := r.read(eb)
	if err != nil {
		return Entry{}, "", nil, "", err
	}
	var d0 []byte
	if in0 && e0.isFile() {
		if d0, err = r.read(e0); err != nil {
			return Entry{}, "", nil, "", err
		}
	}
	if !isText(da) || !isText(db) {
		return newest(), "", nil, "", nil // can't merge pictures and such: the newer one wins, the other stays in the timeline
	}
	merged, ok := merge3(d0, da, db)
	why = "both sides changed the same lines"
	if !ok && m != nil {
		in.Path, in.Base, in.Ours, in.Theirs = p, d0, da, db
		out, sum, merr := m.Merge(ctx, in)
		if merr == nil {
			merged, ok, used, summary = out, true, &in, sum
		} else {
			why += "; Claude couldn't join them: " + merr.Error()
		}
	}
	if !ok {
		return Entry{}, why, nil, "", nil
	}
	size, chunks, err := r.putBytes(merged)
	if err != nil {
		return Entry{}, "", nil, "", err
	}
	mt := ea.Mtime
	if eb.Mtime.After(mt) {
		mt = eb.Mtime
	}
	return Entry{Size: size, Chunks: chunks, Exec: ea.Exec || eb.Exec, Mtime: mt}, "", used, summary, nil
}

func isText(b []byte) bool {
	n := min(len(b), 8000)
	return bytes.IndexByte(b[:n], 0) < 0
}

// braidPackedRefs keeps the losing side's packed branch tips: where the
// winner's packed-refs (kept as is) points a branch somewhere else, the
// loser's tip is saved as a branch braid/<pc>/<name>.
func (r *Repo) braidPackedRefs(out, winner, loser, base *Tree, loserPC string) error {
	const pr = ".git/packed-refs"
	le, ok := loser.Files[pr]
	if !ok {
		return nil
	}
	if be, ok := base.Files[pr]; ok && be.same(le) {
		return nil // the loser didn't change it
	}
	wantW := map[string]string{}
	if we, ok := winner.Files[pr]; ok {
		b, err := r.read(we)
		if err != nil {
			return err
		}
		wantW = parsePacked(b)
	}
	b, err := r.read(le)
	if err != nil {
		return err
	}
	for ref, sha := range parsePacked(b) {
		name, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok || wantW[ref] == sha || !validPath(name) {
			continue
		}
		if e, ok := out.Files[".git/"+ref]; ok {
			if d, err := r.read(e); err == nil && strings.TrimSpace(string(d)) == sha {
				continue
			}
		}
		size, chunks, err := r.putBytes([]byte(sha + "\n"))
		if err != nil {
			return err
		}
		out.Files[".git/refs/heads/braid/"+safeName(loserPC)+"/"+name] = Entry{Size: size, Chunks: chunks, Mtime: time.Now().UTC()}
	}
	return nil
}

func parsePacked(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		l := sc.Text()
		if l == "" || l[0] == '#' || l[0] == '^' {
			continue
		}
		if sha, ref, ok := strings.Cut(l, " "); ok {
			out[ref] = sha
		}
	}
	return out
}

// sideIntent sums up what a strand did since base: the intents of its
// moments, newest first.
func (r *Repo) sideIntent(base, tip string) (string, error) {
	var parts []string
	seen := map[string]bool{}
	for id := tip; id != "" && id != base && len(parts) < 8; {
		m, err := r.Moment(id)
		if err != nil {
			return "", err
		}
		if m.Intent != "" && !seen[m.Intent] {
			seen[m.Intent] = true
			parts = append(parts, m.Intent)
		}
		if len(m.Parents) == 0 {
			break
		}
		id = m.Parents[0]
	}
	return strings.Join(parts, " · "), nil
}

// check writes the tree to a scratch folder and runs the project's check there.
func (r *Repo) check(ctx context.Context, t *Tree, check Check) error {
	dir, err := os.MkdirTemp("", "braid-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err := r.apply(dir, &Tree{Files: map[string]Entry{}}, t, map[string]seen{}); err != nil {
		return err
	}
	return check(ctx, dir)
}
