package ui

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/agent"
	"github.com/Simplyph0/braid/internal/timeline"
)

// repo opens project name's timeline on the hub.
func (s *Server) repo(name string) (*agent.Config, *timeline.Repo, error) {
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		return nil, nil, err
	}
	r, err := c.Repo(c.Store(), name)
	return c, r, err
}

// hubProjects lists the projects on the hub that this PC doesn't have yet.
func (s *Server) hubProjects(w http.ResponseWriter, _ *http.Request) {
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	refs, err := c.Store().Refs("")
	if err != nil {
		reply(w, nil, err)
		return
	}
	mine := map[string]bool{}
	for _, p := range c.Projects {
		mine[p.Name] = true
	}
	type hp struct {
		Name string    `json:"name"`
		Time time.Time `json:"time"`
		PC   string    `json:"pc"`
	}
	out := []hp{}
	for ref := range refs {
		name, ok := strings.CutSuffix(ref, "/main")
		if !ok || strings.Contains(name, "/") || strings.HasPrefix(name, "_") || mine[name] {
			continue
		}
		h := hp{Name: name}
		if r, err := c.Repo(c.Store(), name); err == nil {
			if m, err := r.Moment(refs[ref]); err == nil {
				h.Time, h.PC = m.Time, m.PC
			}
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	reply(w, map[string]any{"projects": out}, nil)
}

// restoreProject brings a project from the hub onto this PC, into folder.
func (s *Server) restoreProject(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Folder string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, nil, AddFolder(s.Dir, req.Folder, req.Name))
}

type momentOut struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	PC     string    `json:"pc"`
	Intent string    `json:"intent"`
	Braid  bool      `json:"braid"`
}

// timeline lists a project's moments, newest first, and where this PC is.
func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	c, repo, err := s.repo(name)
	if err != nil {
		reply(w, nil, err)
		return
	}
	tip, err := repo.Tip("main")
	if err != nil {
		reply(w, nil, err)
		return
	}
	ids, err := repo.History(tip, 100)
	if err != nil {
		reply(w, nil, err)
		return
	}
	out := []momentOut{}
	for _, id := range ids {
		m, err := repo.Moment(id)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out = append(out, momentOut{id, m.Time, m.PC, m.Intent, len(m.Parents) > 1})
	}
	st, _ := agent.ReadStatus(s.Dir, name)
	pin, _ := repo.Tip(agent.PinStrand(c.PC))
	reply(w, map[string]any{"moments": out, "main": tip, "here": st.At, "pinned": pin != ""}, nil)
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	_, repo, err := s.repo(r.URL.Query().Get("name"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	files, git, err := repo.Changes(r.URL.Query().Get("id"))
	reply(w, map[string]any{"files": files, "git": git}, err)
}

// moment shows a moment on this PC ("show"), goes back to the newest
// ("follow"), or makes a moment the newest everywhere ("restore").
func (s *Server) moment(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, ID, Do string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	c, repo, err := s.repo(req.Name)
	if err != nil {
		reply(w, nil, err)
		return
	}
	switch req.Do {
	case "restore":
		_, err = repo.Restore(req.ID, c.PC)
	case "show", "follow":
		id := req.ID
		if req.Do == "follow" {
			id = ""
		} else if _, err = repo.Moment(id); err != nil {
			reply(w, nil, errors.New("no such moment"))
			return
		}
		var old string
		if old, err = repo.Tip(agent.PinStrand(c.PC)); err == nil && old != id {
			err = repo.SetTip(agent.PinStrand(c.PC), old, id)
		}
	default:
		err = errors.New("unknown action")
	}
	reply(w, nil, err)
}

// check sets the command a Claude merge must pass in a project.
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Check string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	for i := range c.Projects {
		if c.Projects[i].Name == req.Name {
			c.Projects[i].Check = strings.TrimSpace(req.Check)
			reply(w, nil, c.Save(s.Dir))
			return
		}
	}
	reply(w, nil, errors.New("no project called "+req.Name))
}

// log returns the end of Braid's activity log.
func (s *Server) log(w http.ResponseWriter, _ *http.Request) {
	b, err := os.ReadFile(filepath.Join(s.Dir, "braid.log"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		reply(w, nil, err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	reply(w, map[string]any{"lines": lines}, nil)
}
