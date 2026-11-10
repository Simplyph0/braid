// Package ui is Braid's window on each PC: a small web page that `braid run`
// serves to this PC only, opened as an app window, plus a tray icon.
package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/agent"
	"github.com/Simplyph0/braid/internal/update"
)

//go:embed web
var webFS embed.FS

// Server is the window's backend.
type Server struct {
	Dir       string              // braid's config folder
	Autostart func(on bool) error // turn start-at-login on or off
	IsAuto    func() bool         // whether it's on
	key       string              // the secret in the window's address
}

// KeyFile holds the secret that opens the window; only this user can read it.
func KeyFile(dir string) string { return filepath.Join(dir, "window-key") }

// Serve answers on l (127.0.0.1 only) until it's closed.
func (s *Server) Serve(l net.Listener) error {
	b := make([]byte, 16)
	rand.Read(b)
	s.key = hex.EncodeToString(b)
	if err := os.WriteFile(KeyFile(s.Dir), []byte(s.key), 0o600); err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(l.Addr().String()), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(l)
}

// Handler guards the API: only this PC, only a browser that was given the
// secret (any program on the PC could otherwise drive it), and no requests
// from other websites.
func (s *Server) Handler(addr string) http.Handler {
	web, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(web)
	_, port, _ := net.SplitHostPort(addr)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/add", s.add)
	mux.HandleFunc("POST /api/pick", s.pick)
	mux.HandleFunc("GET /api/scan", s.scan)
	mux.HandleFunc("POST /api/addmany", s.addMany)
	mux.HandleFunc("POST /api/project", s.project)
	mux.HandleFunc("POST /api/settings", s.settings)
	mux.HandleFunc("POST /api/open", s.open)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Another site can point its own name at 127.0.0.1; only our own name is accepted.
		if r.Host != "127.0.0.1:"+port && r.Host != "localhost:"+port {
			http.Error(w, "wrong host", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if k := r.URL.Query().Get("k"); k != "" { // opened from braid: remember the secret in a cookie
			if subtle.ConstantTimeCompare([]byte(k), []byte(s.key)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: "braid", Value: s.key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie("braid")
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.key)) != 1 {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("Open Braid from its tray icon or with the braid command."))
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Braid") != "1" {
			http.Error(w, "missing header", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if v == nil {
		v = map[string]bool{"ok": true}
	}
	json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(v)
}

type projectView struct {
	agent.Project
	Status agent.Status `json:"status"`
}

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, map[string]any{"setUp": false, "version": update.Version}, nil)
		return
	}
	out := map[string]any{"setUp": true, "pc": c.PC, "hub": c.Hub, "version": update.Version,
		"noClaude": c.NoClaude, "noUpdate": c.NoUpdate, "updateMinutes": int(c.UpdateEvery().Minutes()), "autostart": s.IsAuto != nil && s.IsAuto()}
	ps := []projectView{}
	for _, p := range c.Projects {
		st, _ := agent.ReadStatus(s.Dir, p.Name)
		ps = append(ps, projectView{p, st})
	}
	out["projects"] = ps
	reply(w, out, nil)
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var req struct{ Hub, Passphrase string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	if len(req.Passphrase) < 8 {
		reply(w, nil, errors.New("use a passphrase of at least 8 characters"))
		return
	}
	hub := strings.TrimRight(strings.TrimSpace(req.Hub), "/")
	if hub == "" {
		reply(w, nil, errors.New("enter your hub's address"))
		return
	}
	if !strings.Contains(hub, "://") {
		hub = "https://" + hub
	}
	key, token, err := agent.Connect(hub, req.Passphrase)
	if err != nil {
		reply(w, nil, err)
		return
	}
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		c = &agent.Config{}
	}
	if c.PC == "" {
		c.PC, _ = os.Hostname()
	}
	c.Hub, c.Key, c.Token = hub, hex.EncodeToString(key), token
	reply(w, nil, c.Save(s.Dir))
}

var nameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var req struct{ Folder string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, nil, AddFolder(s.Dir, req.Folder, ""))
}

// AddFolder starts keeping folder in the timeline named name (default: the
// folder's name). Shared by the window and `braid add`.
func AddFolder(dir, folder, name string) error {
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	folder, err = filepath.Abs(strings.TrimSpace(folder))
	if err != nil || folder == "" {
		return errors.New("choose a folder")
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return errors.New(folder + " isn't a folder")
		}
	}
	if name == "" {
		name = filepath.Base(folder)
	}
	name = strings.Trim(nameRe.ReplaceAllString(name, "-"), "-.")
	if name == "" {
		return errors.New("that folder needs a name")
	}
	for _, p := range c.Projects {
		if p.Name == name {
			return errors.New("there's already a project called " + name + " here (" + p.Folder + ")")
		}
		if p.Folder == folder {
			return errors.New("that folder is already project " + p.Name)
		}
	}
	c.Projects = append(c.Projects, agent.Project{Name: name, Folder: folder})
	return c.Save(dir)
}

// scan lists project folders found on this PC that aren't added yet.
func (s *Server) scan(w http.ResponseWriter, _ *http.Request) {
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var have []string
	for _, p := range c.Projects {
		have = append(have, p.Folder)
	}
	home, _ := os.UserHomeDir()
	reply(w, map[string]any{"folders": ScanFolders(home, have)}, nil)
}

// addMany adds several folders; it says which ones couldn't be.
func (s *Server) addMany(w http.ResponseWriter, r *http.Request) {
	var req struct{ Folders []string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	var failed []string
	for _, f := range req.Folders {
		if err := AddFolder(s.Dir, f, ""); err != nil {
			failed = append(failed, filepath.Base(f)+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		reply(w, nil, errors.New(strings.Join(failed, "; ")))
		return
	}
	reply(w, nil, nil)
}

func (s *Server) pick(w http.ResponseWriter, _ *http.Request) {
	folder, err := PickFolder()
	reply(w, map[string]string{"folder": folder}, err)
}

// project pauses, resumes or removes one project.
func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Do string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	for i, p := range c.Projects {
		if p.Name != req.Name {
			continue
		}
		switch req.Do {
		case "pause":
			c.Projects[i].Paused = true
		case "resume":
			c.Projects[i].Paused = false
		case "remove":
			c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
		default:
			reply(w, nil, errors.New("unknown action"))
			return
		}
		reply(w, nil, c.Save(s.Dir))
		return
	}
	reply(w, nil, errors.New("no project called "+req.Name))
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NoClaude, NoUpdate, Autostart *bool
		UpdateMinutes                 *int
	}
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if req.NoClaude != nil {
		c.NoClaude = *req.NoClaude
	}
	if req.NoUpdate != nil {
		c.NoUpdate = *req.NoUpdate
	}
	if req.UpdateMinutes != nil {
		if *req.UpdateMinutes < 1 || *req.UpdateMinutes > 1440 {
			reply(w, nil, errors.New("choose between 1 minute and 1 day"))
			return
		}
		c.UpdateMinutes = *req.UpdateMinutes
	}
	if err := c.Save(s.Dir); err != nil {
		reply(w, nil, err)
		return
	}
	if req.Autostart != nil && s.Autostart != nil {
		err = s.Autostart(*req.Autostart)
	}
	reply(w, nil, err)
}

// open shows a project's folder, or the hub's web app ("timeline").
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, What string }
	if err := decode(r, &req); err != nil {
		reply(w, nil, err)
		return
	}
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if req.What == "timeline" {
		reply(w, nil, OpenURL(c.Hub))
		return
	}
	for _, p := range c.Projects {
		if p.Name == req.Name {
			reply(w, nil, OpenPath(p.Folder))
			return
		}
	}
	reply(w, nil, errors.New("no project called "+req.Name))
}
