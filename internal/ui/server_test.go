package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Simplyph0/braid/internal/agent"
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
