package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Simplyph0/braid/internal/store"
)

func TestHubServesAppAndAPI(t *testing.T) {
	srv := httptest.NewServer(handler(store.Handler(&store.Dir{Root: t.TempDir()}, "tok"), ""))
	defer srv.Close()
	get := func(path, auth string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := get("/", ""); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Security-Policy"), "wasm-unsafe-eval") {
		t.Errorf("index: %d %v", r.StatusCode, r.Header)
	}
	if r := get("/app.js", ""); r.StatusCode != 200 {
		t.Errorf("app.js: %d", r.StatusCode)
	}
	if r := get("/refs", ""); r.StatusCode != 401 {
		t.Errorf("API without the token: %d", r.StatusCode)
	}
	if r := get("/refs", "tok"); r.StatusCode != 200 {
		t.Errorf("API with the token: %d", r.StatusCode)
	}
}
