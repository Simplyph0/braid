package ui

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Simplyph0/braid/internal/agent"
	"github.com/Simplyph0/braid/internal/store"
	"github.com/Simplyph0/braid/internal/timeline"
)

func TestWindowIsOnlyForThisUser(t *testing.T) {
	dir := t.TempDir()
	(&agent.Config{Hub: "https://hub", PC: "Desk", Key: strings.Repeat("00", 32)}).Save(dir)
	s := &Server{Dir: dir, key: "secret"}
	srv := httptest.NewServer(s.Handler("127.0.0.1:1234"))
	defer srv.Close()
	do := func(method, path, host, cookie, body string, header bool) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Host = host
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "braid", Value: cookie})
		}
		if header {
			req.Header.Set("X-Braid", "1")
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	const host = "127.0.0.1:1234"
	if r := do("GET", "/api/state", "evil.example:1234", "secret", "", false); r.StatusCode != 403 {
		t.Errorf("another host name: %d", r.StatusCode)
	}
	if r := do("GET", "/api/state", host, "", "", false); r.StatusCode != 403 {
		t.Errorf("no secret: %d", r.StatusCode)
	}
	if r := do("GET", "/?k=wrong", host, "", "", false); len(r.Cookies()) != 0 {
		t.Error("a wrong secret got a cookie")
	}
	if r := do("GET", "/?k=secret", host, "", "", false); len(r.Cookies()) != 1 || r.Cookies()[0].Value != "secret" {
		t.Error("the right secret didn't get a cookie")
	}
	folder := filepath.Join(t.TempDir(), "My Project")
	if r := do("POST", "/api/add", host, "secret", `{"Folder":"`+folder+`"}`, false); r.StatusCode != 403 {
		t.Errorf("a post without the header (another site's form): %d", r.StatusCode)
	}
	if r := do("POST", "/api/add", host, "secret", `{"Folder":"`+folder+`"}`, true); r.StatusCode != 200 {
		t.Fatalf("add: %d", r.StatusCode)
	}
	if r := do("POST", "/api/project", host, "secret", `{"Name":"My-Project","Do":"pause"}`, true); r.StatusCode != 200 {
		t.Fatalf("pause: %d", r.StatusCode)
	}
	r := do("GET", "/api/state", host, "secret", "", false)
	var st struct {
		SetUp    bool
		Projects []struct {
			Name, Folder string
			Paused       bool
		}
	}
	json.NewDecoder(r.Body).Decode(&st)
	if !st.SetUp || len(st.Projects) != 1 || st.Projects[0].Name != "My-Project" || !st.Projects[0].Paused || st.Projects[0].Folder != folder {
		t.Fatalf("state: %+v", st)
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		t.Error("the folder wasn't made")
	}
	if r := do("POST", "/api/project", host, "secret", `{"Name":"My-Project","Do":"remove"}`, true); r.StatusCode != 200 {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	if c, _ := agent.LoadConfig(dir); len(c.Projects) != 0 {
		t.Error("not removed")
	}
	if _, err := os.Stat(folder); err != nil {
		t.Error("removing deleted the folder")
	}
}

func TestIcons(t *testing.T) {
	for s := range colors {
		if b := icon(s); len(b) < 50 {
			t.Errorf("%s icon is %d bytes", s, len(b))
		}
	}
}

func TestHistoryRestoreAndHubProjects(t *testing.T) {
	hub := httptest.NewServer(store.Handler(&store.Dir{Root: t.TempDir()}))
	defer hub.Close()
	key, token, err := agent.Connect(hub.URL, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	mkPC := func(name string) (*Server, *agent.Config, string) {
		dir := t.TempDir()
		c := &agent.Config{Hub: hub.URL, Token: token, PC: name, Key: hex.EncodeToString(key)}
		c.Save(dir)
		return &Server{Dir: dir, key: "k"}, c, dir
	}
	desk, dc, _ := mkPC("Desk")
	folder := filepath.Join(t.TempDir(), "Proj")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "a.txt"), []byte("v1"), 0o644)
	dc.Projects = []agent.Project{{Name: "Proj", Folder: folder}}
	dc.Save(desk.Dir)
	// Record two moments from the desktop.
	repo, _ := dc.Repo(dc.Store(), "Proj")
	ws, _ := timeline.Open(repo, "Desk", folder, filepath.Join(t.TempDir(), "st.json"))
	ws.Settle = 0
	if _, err := ws.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := ws.At()
	os.WriteFile(filepath.Join(folder, "a.txt"), []byte("v2"), 0o644)
	os.WriteFile(filepath.Join(folder, "b.txt"), []byte("new"), 0o644)
	time.Sleep(20 * time.Millisecond)
	if _, err := ws.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	lap, _, _ := mkPC("Lap")
	call := func(s *Server, method, path string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req := httptest.NewRequest(method, path, rd)
		rec := httptest.NewRecorder()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/hub", s.hubProjects)
		mux.HandleFunc("POST /api/restoreproject", s.restoreProject)
		mux.HandleFunc("GET /api/timeline", s.timeline)
		mux.HandleFunc("GET /api/changes", s.changes)
		mux.HandleFunc("POST /api/moment", s.moment)
		mux.HandleFunc("POST /api/check", s.check)
		mux.ServeHTTP(rec, req)
		var out map[string]any
		json.NewDecoder(rec.Body).Decode(&out)
		return rec.Code, out
	}
	// The laptop sees the project on the hub and restores it into a folder.
	if code, out := call(lap, "GET", "/api/hub", nil); code != 200 || len(out["projects"].([]any)) != 1 {
		t.Fatalf("hub projects: %d %v", code, out)
	}
	target := filepath.Join(t.TempDir(), "Restored")
	if code, out := call(lap, "POST", "/api/restoreproject", map[string]string{"Name": "Proj", "Folder": target}); code != 200 {
		t.Fatalf("restore project: %d %v", code, out)
	}
	if code, out := call(lap, "GET", "/api/hub", nil); code != 200 || len(out["projects"].([]any)) != 0 {
		t.Fatalf("it should be gone from the list once added: %v", out)
	}
	if c, _ := agent.LoadConfig(lap.Dir); len(c.Projects) != 1 || c.Projects[0].Name != "Proj" || c.Projects[0].Folder != target {
		t.Fatalf("config: %+v", c.Projects)
	}
	// History: two moments, the newer one added b.txt and changed a.txt.
	code, out := call(lap, "GET", "/api/timeline?name=Proj", nil)
	moments, _ := out["moments"].([]any)
	if code != 200 || len(moments) != 2 {
		t.Fatalf("timeline: %d %v", code, out)
	}
	newest := moments[0].(map[string]any)["id"].(string)
	if _, ch := call(lap, "GET", "/api/changes?name=Proj&id="+newest, nil); len(ch["files"].([]any)) != 2 {
		t.Fatalf("changes: %v", ch)
	}
	// Show the first moment here (sets this PC's pin), then follow again.
	if code, _ := call(lap, "POST", "/api/moment", map[string]string{"Name": "Proj", "ID": first, "Do": "show"}); code != 200 {
		t.Fatal("show failed")
	}
	if _, out := call(lap, "GET", "/api/timeline?name=Proj", nil); out["pinned"] != true {
		t.Errorf("not pinned: %v", out)
	}
	call(lap, "POST", "/api/moment", map[string]string{"Name": "Proj", "Do": "follow"})
	if _, out := call(lap, "GET", "/api/timeline?name=Proj", nil); out["pinned"] != false {
		t.Errorf("still pinned: %v", out)
	}
	// Restore the first moment for everyone: main moves to a new moment holding v1.
	if code, _ := call(lap, "POST", "/api/moment", map[string]string{"Name": "Proj", "ID": first, "Do": "restore"}); code != 200 {
		t.Fatal("restore failed")
	}
	if _, out := call(lap, "GET", "/api/timeline?name=Proj", nil); len(out["moments"].([]any)) != 3 {
		t.Errorf("a restore adds a moment: %v", out)
	}
	if code, _ := call(lap, "POST", "/api/check", map[string]string{"Name": "Proj", "Check": " go build ./... "}); code != 200 {
		t.Fatal("check failed")
	}
	if c, _ := agent.LoadConfig(lap.Dir); c.Projects[0].Check != "go build ./..." {
		t.Errorf("check: %q", c.Projects[0].Check)
	}
}
