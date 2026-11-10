// Package agent is what runs on each PC: it keeps every project folder in
// step with its timeline on the hub.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
	"github.com/Simplyph0/braid/internal/timeline"
)

// Project is a folder on this PC and the timeline it shows.
type Project struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Pin    string `json:"pin,omitempty"` // show this moment instead of following main
}

// Config is this PC's setup. It holds the key: it's written readable by
// this user only.
type Config struct {
	Hub      string    `json:"hub"`
	Token    string    `json:"token"`
	PC       string    `json:"pc"`
	Key      string    `json:"key"` // shortcut: the master key sits in this 0600 file; move it to the OS keyring when there's an installer
	Projects []Project `json:"projects"`
}

// Status is what the agent last did for a project, for `braid status`.
type Status struct {
	At        string              `json:"at"`
	Detached  bool                `json:"detached,omitempty"`
	Pinned    string              `json:"pinned,omitempty"` // the Pin last applied
	LastSync  time.Time           `json:"lastSync"`
	Error     string              `json:"error,omitempty"`
	Conflicts []timeline.Conflict `json:"conflicts,omitempty"`
	Last      string              `json:"last,omitempty"` // the last thing that happened, in words
}

func Dir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "braid")
}

func LoadConfig(dir string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("not set up yet: run `braid setup` first")
	}
	if err != nil {
		return nil, err
	}
	c := &Config{}
	return c, json.Unmarshal(b, c)
}

func (c *Config) Save(dir string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}

func (c *Config) Store() store.Store { return &store.HTTP{URL: c.Hub, Token: c.Token} }

func fixedID(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var ErrWrongPassphrase = errors.New("wrong passphrase: it doesn't match the one your other PCs use")

// MasterKey derives the key all your PCs share from the passphrase. The salt
// lives on the hub (the first PC makes it). A check value proves the
// passphrase is the same one the other PCs used.
func MasterKey(s store.Store, passphrase string) ([]byte, error) {
	saltID := fixedID("braid-salt")
	salt, err := s.Get("meta", saltID)
	if errors.Is(err, store.ErrNotFound) {
		salt = crypt.NewKey()
		if err := s.Put("meta", saltID, salt); err != nil {
			return nil, err
		}
		salt, err = s.Get("meta", saltID) // another PC may have got there first
	}
	if err != nil {
		return nil, err
	}
	key := crypt.DeriveKey(passphrase, salt)
	checkID := fixedID("braid-check")
	check, err := s.Get("meta", checkID)
	if errors.Is(err, store.ErrNotFound) {
		sealed, err := crypt.Seal(key, []byte("braid"), []byte("check"))
		if err != nil {
			return nil, err
		}
		if err := s.Put("meta", checkID, sealed); err != nil {
			return nil, err
		}
		check, err = s.Get("meta", checkID)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err := crypt.Open(key, check, []byte("check")); err != nil {
		return nil, ErrWrongPassphrase
	}
	return key, nil
}

// ProjectKey returns a project's own key (made on first use, kept on the
// hub sealed with the master key). Erasing a project destroys this key.
func ProjectKey(s store.Store, master []byte, project string) ([]byte, error) {
	id := fixedID("braid-project-key\x00" + project)
	ad := []byte("pkey" + id)
	blob, err := s.Get("pkey", id)
	if errors.Is(err, store.ErrNotFound) {
		sealed, err := crypt.Seal(master, crypt.NewKey(), ad)
		if err != nil {
			return nil, err
		}
		if err := s.Put("pkey", id, sealed); err != nil {
			return nil, err
		}
		blob, err = s.Get("pkey", id) // use whichever PC's key landed first
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	k, err := crypt.Open(master, blob, ad)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return k, nil
}

// Repo opens a project's timeline.
func (c *Config) Repo(s store.Store, project string) (*timeline.Repo, error) {
	master, err := hex.DecodeString(c.Key)
	if err != nil || len(master) != crypt.KeySize {
		return nil, errors.New("the key in the config is damaged: run `braid setup` again")
	}
	k, err := ProjectKey(s, master, project)
	if err != nil {
		return nil, err
	}
	return timeline.NewRepo(s, project, k)
}

// Agent runs the projects in a config.
type Agent struct {
	Dir    string
	Store  store.Store
	Log    *log.Logger
	Settle time.Duration

	ws map[string]*timeline.Workspace
}

func (a *Agent) statusPath(name string) string {
	return filepath.Join(a.Dir, "status", name+".json")
}

func ReadStatus(dir, name string) (Status, error) {
	var st Status
	b, err := os.ReadFile(filepath.Join(dir, "status", name+".json"))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func (a *Agent) writeStatus(name string, st Status) {
	b, _ := json.MarshalIndent(st, "", "  ")
	os.MkdirAll(filepath.Dir(a.statusPath(name)), 0o700)
	os.WriteFile(a.statusPath(name), b, 0o600)
}

// Tick syncs every project once. The config is re-read each time, so
// `braid add`, `braid jump` and the like take effect at the next tick.
func (a *Agent) Tick(ctx context.Context) error {
	c, err := LoadConfig(a.Dir)
	if err != nil {
		return err
	}
	if a.ws == nil {
		a.ws = map[string]*timeline.Workspace{}
	}
	for _, p := range c.Projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st, _ := ReadStatus(a.Dir, p.Name)
		prevErr := st.Error
		err := a.syncOne(ctx, c, p, &st)
		st.Error = ""
		if err != nil {
			st.Error = err.Error()
			if st.Error != prevErr { // say it once, not every few seconds
				a.Log.Printf("%s: %v", p.Name, err)
			}
		} else {
			st.LastSync = time.Now()
		}
		a.writeStatus(p.Name, st)
	}
	return nil
}

func (a *Agent) syncOne(ctx context.Context, c *Config, p Project, st *Status) error {
	w := a.ws[p.Name]
	if w == nil || w.Folder != p.Folder {
		repo, err := c.Repo(a.Store, p.Name)
		if err != nil {
			return err
		}
		w, err = timeline.Open(repo, c.PC, p.Folder, filepath.Join(a.Dir, "state", p.Name+".json"))
		if err != nil {
			return err
		}
		if a.Settle != 0 { // negative: no settling (tests)
			w.Settle = max(a.Settle, 0)
		}
		a.ws[p.Name] = w
	}
	var rep timeline.Report
	var err error
	switch {
	case p.Pin != "" && st.Pinned != p.Pin:
		id, err := a.Resolve(w.Repo, p.Pin)
		if err != nil {
			return err
		}
		rep, err = w.Jump(ctx, id)
		if err != nil {
			return err
		}
		st.Last, st.Pinned = "showing moment "+p.Pin, p.Pin
		a.Log.Printf("%s: now showing moment %.12s", p.Name, id)
	case p.Pin == "" && w.Detached():
		rep, err = w.Follow(ctx)
		st.Last, st.Pinned = "following main again", ""
		a.Log.Printf("%s: following main again", p.Name)
	default:
		rep, err = w.Sync(ctx)
	}
	if err != nil {
		return err
	}
	st.At, st.Detached, st.Conflicts = w.At(), w.Detached(), rep.Conflicts
	switch {
	case len(rep.Conflicts) > 0:
		st.Last = fmt.Sprintf("waiting on its own strand: %d file(s) changed in the same places on two PCs", len(rep.Conflicts))
		a.Log.Printf("%s: %s", p.Name, st.Last)
	case rep.Braided != "":
		st.Last = "braided this PC's work with the others'"
		a.Log.Printf("%s: %s", p.Name, st.Last)
	case rep.Recorded != "":
		st.Last = "recorded a new moment"
	case rep.Moved:
		st.Last = "updated to the newest moment"
		a.Log.Printf("%s: %s", p.Name, st.Last)
	}
	if rep.Busy > 0 {
		a.Log.Printf("%s: left %d file(s) alone while they're being edited", p.Name, rep.Busy)
	}
	return nil
}

// resolve turns a moment id or a unique prefix of one into the full id.
func (a *Agent) Resolve(r *timeline.Repo, prefix string) (string, error) {
	tip, err := r.Tip("main")
	if err != nil {
		return "", err
	}
	ids, err := r.History(tip, 100000)
	if err != nil {
		return "", err
	}
	found := ""
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			if found != "" && found != id {
				return "", fmt.Errorf("%q matches more than one moment", prefix)
			}
			found = id
		}
	}
	if found == "" {
		return "", fmt.Errorf("no moment %q on main", prefix)
	}
	return found, nil
}

// Run ticks until ctx ends.
func (a *Agent) Run(ctx context.Context, every time.Duration) {
	for {
		if err := a.Tick(ctx); err != nil && ctx.Err() == nil {
			a.Log.Printf("%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
