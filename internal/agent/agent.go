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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/claude"
	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
	"github.com/Simplyph0/braid/internal/timeline"
	"github.com/Simplyph0/braid/internal/update"
)

// Project is a folder on this PC and the timeline it shows.
type Project struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Check  string `json:"check,omitempty"` // command that must pass on a braid Claude merged, e.g. "go build ./..."
}

// Config is this PC's setup. It holds the key: it's written readable by
// this user only.
type Config struct {
	Hub      string    `json:"hub"`
	Token    string    `json:"token"`
	PC       string    `json:"pc"`
	Key      string    `json:"key"`                // shortcut: the master key sits in this 0600 file; move it to the OS keyring when there's an installer
	NoClaude bool      `json:"noClaude,omitempty"` // don't ask Claude to merge on this PC
	NoUpdate bool      `json:"noUpdate,omitempty"` // don't install updates from the hub
	Projects []Project `json:"projects"`
}

// Status is what the agent last did for a project, for `braid status`.
type Status struct {
	PC        string              `json:"pc"`
	Folder    string              `json:"folder"`
	Updated   time.Time           `json:"updated"` // when this PC last reported
	Version   string              `json:"version"`
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

	ws        map[string]*timeline.Workspace
	published map[string]published
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
		a.publish(p, c, st)
	}
	return nil
}

// publish tells the hub (encrypted) how this PC is doing with a project, for
// the web app: at once when something changed, else once a minute.
func (a *Agent) publish(p Project, c *Config, st Status) {
	w := a.ws[p.Name]
	if w == nil {
		return
	}
	st.PC, st.Folder, st.Version = c.PC, p.Folder, update.Version
	cmp := st
	cmp.LastSync, cmp.Updated = time.Time{}, time.Time{}
	key, _ := json.Marshal(cmp)
	if a.published == nil {
		a.published = map[string]published{}
	}
	if last := a.published[p.Name]; last.key == string(key) && time.Since(last.at) < time.Minute {
		return
	}
	st.Updated = time.Now()
	if err := w.Repo.PutNote("status/"+timeline.SafeName(c.PC), st); err != nil {
		return // the next tick tries again
	}
	a.published[p.Name] = published{string(key), time.Now()}
}

type published struct {
	key string
	at  time.Time
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
	w.Merger, w.Check = nil, nil
	if !c.NoClaude && claude.Available() {
		w.Merger = &claude.Merger{}
	}
	if p.Check != "" {
		w.Check = shellCheck(p.Check)
	}
	folder := p.Folder
	w.Intent = func(since, until time.Time) string { return claude.Intent(claude.Home(), folder, since, until) }
	// What to show is set on the hub (by `braid jump` or the web app), so it
	// can be changed from anywhere.
	pin, err := w.Repo.Tip(PinStrand(c.PC))
	if err != nil {
		return err
	}
	var rep timeline.Report
	said := true // the pin cases describe what they did themselves
	switch {
	case pin != "" && st.Pinned != pin:
		rep, err = w.Jump(ctx, pin)
		if err != nil {
			return err
		}
		st.Last, st.Pinned = fmt.Sprintf("showing moment %.12s", pin), pin
		a.Log.Printf("%s: now showing moment %.12s", p.Name, pin)
	case pin == "" && w.Detached():
		rep, err = w.Follow(ctx)
		st.Last, st.Pinned = "following main again", ""
		a.Log.Printf("%s: following main again", p.Name)
	default:
		rep, err = w.Sync(ctx)
		said = false
	}
	if err != nil {
		return err
	}
	hadConflicts := len(st.Conflicts)
	st.At, st.Detached, st.Conflicts = w.At(), w.Detached(), rep.Conflicts
	switch {
	case len(rep.Conflicts) > 0:
		st.Last = fmt.Sprintf("waiting on its own strand: %d file(s) changed in the same places on two PCs", len(rep.Conflicts))
		if hadConflicts != len(rep.Conflicts) { // once, not every few seconds
			a.Log.Printf("%s: %s", p.Name, st.Last)
		}
	case rep.Braided != "":
		st.Last = "braided this PC's work with the others'"
		if m, err := w.Repo.Moment(rep.Braided); err == nil && m.Intent != "" {
			st.Last = m.Intent
		}
		a.Log.Printf("%s: %s", p.Name, st.Last)
	case said:
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

// PinStrand is the hub ref holding the moment a PC should show ("" = follow main).
func PinStrand(pc string) string { return "pin/" + timeline.SafeName(pc) }

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

// shellCheck runs a project's check command in the folder it's given.
func shellCheck(command string) timeline.Check {
	return func(ctx context.Context, dir string) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", command)
		}
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			if len(out) > 2000 {
				out = out[len(out)-2000:]
			}
			return fmt.Errorf("%q failed (%v): %s", command, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}
