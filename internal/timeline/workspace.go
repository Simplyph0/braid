package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Simplyph0/braid/internal/store"
)

// Workspace is one PC's folder for a project: it records the folder into the
// timeline and shows a moment of it.
type Workspace struct {
	Repo      *Repo
	PC        string
	Folder    string
	StatePath string
	Settle    time.Duration // a file changed this recently is still being written
	Merger    Merger
	Now       func() time.Time

	st wsState
}

type wsState struct {
	At       string          `json:"at"`                 // the moment the folder matched when last recorded or written
	Detached bool            `json:"detached,omitempty"` // jumped to a moment: don't follow main
	Seen     map[string]seen `json:"seen"`
}

// Report says what a Sync did.
type Report struct {
	Recorded  string     // a new moment from this folder
	Braided   string     // a braid made by this sync
	Moved     bool       // the folder was changed to show a newer moment
	Busy      int        // paths left alone because they're being edited
	Conflicts []Conflict // couldn't braid: this PC's work waits on its own strand
	At        string
}

func Open(r *Repo, pc, folder, statePath string) (*Workspace, error) {
	w := &Workspace{Repo: r, PC: pc, Folder: folder, StatePath: statePath, Settle: 3 * time.Second, Now: time.Now}
	b, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(b, &w.st); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if w.st.Seen == nil {
		w.st.Seen = map[string]seen{}
	}
	return w, nil
}

func (w *Workspace) At() string     { return w.st.At }
func (w *Workspace) Detached() bool { return w.st.Detached }

func (w *Workspace) strand() string { return "pc/" + safeName(w.PC) }

func (w *Workspace) save() error {
	b, err := json.Marshal(w.st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.StatePath), 0o755); err != nil {
		return err
	}
	tmp := w.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.StatePath)
}

// record saves the folder as a new moment if it changed since At.
func (w *Workspace) record(intent string) (string, error) {
	if gitBusy(w.Folder) {
		return "", ErrGitBusy
	}
	prev, err := w.Repo.MomentTree(w.st.At)
	if err != nil {
		return "", err
	}
	t, err := w.Repo.scan(w.Folder, prev, w.st.Seen, w.Settle, w.Now())
	if err != nil {
		return "", err
	}
	tid, err := w.Repo.PutTree(t)
	if err != nil {
		return "", err
	}
	if w.st.At != "" {
		if m, err := w.Repo.Moment(w.st.At); err == nil && m.Tree == tid {
			return "", w.save()
		}
	} else if len(t.Files) == 0 {
		return "", nil // an empty new folder: nothing to record yet
	}
	var parents []string
	if w.st.At != "" {
		parents = []string{w.st.At}
	}
	id, err := w.Repo.PutMoment(&Moment{Tree: tid, Parents: parents, PC: w.PC, Time: w.Now().UTC(), Intent: intent})
	if err != nil {
		return "", err
	}
	w.st.At = id
	return id, w.save()
}

// show changes the folder from At to moment id.
func (w *Workspace) show(id string) (int, error) {
	cur, err := w.Repo.MomentTree(w.st.At)
	if err != nil {
		return 0, err
	}
	target, err := w.Repo.MomentTree(id)
	if err != nil {
		return 0, err
	}
	busy, err := w.Repo.apply(w.Folder, cur, target, w.st.Seen)
	if err != nil {
		return busy, err
	}
	w.st.At = id
	return busy, w.save()
}

// Sync records this folder's changes, publishes them, braids if another PC
// moved main meanwhile, and brings the folder up to date. It never asks
// anything: when a braid isn't possible, this PC's work waits on its own
// strand and the conflicts are reported.
func (w *Workspace) Sync(ctx context.Context) (Report, error) {
	var rep Report
	if fi, err := os.Stat(w.Folder); err != nil || !fi.IsDir() {
		return rep, ErrFolderMissing // missing is not deleted: nothing is recorded
	}
	rec, err := w.record("")
	if err != nil {
		return rep, err
	}
	rep.Recorded = rec
	if w.st.Detached {
		if rec != "" {
			old, _ := w.Repo.Tip(w.strand())
			if err := w.Repo.SetTip(w.strand(), old, w.st.At); err != nil {
				return rep, err
			}
		}
		rep.At = w.st.At
		return rep, nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		main, err := w.Repo.Tip("main")
		if err != nil {
			return rep, err
		}
		at := w.st.At
		if at == main || at == "" && main == "" {
			break
		}
		behind, err := w.Repo.IsAncestor(at, main)
		if err != nil {
			return rep, err
		}
		if behind { // nothing new here: just show main
			busy, err := w.show(main)
			rep.Busy += busy
			rep.Moved = true
			if err != nil {
				return rep, err
			}
			break
		}
		ahead := main == ""
		if !ahead {
			if ahead, err = w.Repo.IsAncestor(main, at); err != nil {
				return rep, err
			}
		}
		if ahead {
			err := w.Repo.SetTip("main", main, at)
			if errors.Is(err, store.ErrMoved) {
				continue
			}
			if err != nil {
				return rep, err
			}
			w.dropStrand()
			break
		}
		// Both moved on: braid.
		b, conflicts, err := w.Repo.Braid(ctx, main, at, w.PC, w.Merger)
		if err != nil {
			return rep, err
		}
		if len(conflicts) > 0 {
			old, _ := w.Repo.Tip(w.strand())
			if err := w.Repo.SetTip(w.strand(), old, at); err != nil {
				return rep, err
			}
			rep.Conflicts = conflicts
			break
		}
		err = w.Repo.SetTip("main", main, b)
		if errors.Is(err, store.ErrMoved) {
			continue
		}
		if err != nil {
			return rep, err
		}
		rep.Braided = b
		busy, err := w.show(b)
		rep.Busy += busy
		rep.Moved = true
		if err != nil {
			return rep, err
		}
		w.dropStrand()
		break
	}
	rep.At = w.st.At
	return rep, nil
}

func (w *Workspace) dropStrand() {
	if old, _ := w.Repo.Tip(w.strand()); old != "" {
		_ = w.Repo.SetTip(w.strand(), old, "")
	}
}

// Jump shows moment id in this folder and stops following main. Whatever the
// folder held is recorded first, so the jump can always be undone.
func (w *Workspace) Jump(ctx context.Context, id string) (Report, error) {
	if _, err := w.Repo.Moment(id); err != nil {
		return Report{}, fmt.Errorf("no such moment: %w", err)
	}
	rep, err := w.Sync(ctx)
	if err != nil {
		return rep, err
	}
	busy, err := w.show(id)
	rep.Busy, rep.Moved, rep.At = rep.Busy+busy, true, w.st.At
	if err != nil {
		return rep, err
	}
	w.st.Detached = true
	return rep, w.save()
}

// Follow goes back to following main; work done while jumped is braided in.
func (w *Workspace) Follow(ctx context.Context) (Report, error) {
	w.st.Detached = false
	if err := w.save(); err != nil {
		return Report{}, err
	}
	return w.Sync(ctx)
}

// Restore makes moment id the newest state of main, for every PC.
func (r *Repo) Restore(id, pc string) (string, error) {
	m, err := r.Moment(id)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 5; attempt++ {
		main, err := r.Tip("main")
		if err != nil {
			return "", err
		}
		var parents []string
		if main != "" {
			parents = []string{main}
		}
		nid, err := r.PutMoment(&Moment{Tree: m.Tree, Parents: parents, PC: pc, Time: time.Now().UTC(),
			Intent: "Restored the project to " + m.Time.Local().Format("Jan 2 15:04")})
		if err != nil {
			return "", err
		}
		err = r.SetTip("main", main, nid)
		if errors.Is(err, store.ErrMoved) {
			continue
		}
		return nid, err
	}
	return "", errors.New("main kept moving; try again")
}
