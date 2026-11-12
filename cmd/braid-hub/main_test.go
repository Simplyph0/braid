package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Simplyph0/braid/internal/store"
)

func TestHubServesAppAndAPI(t *testing.T) {
	srv := httptest.NewServer(handler(store.Handler(&store.Dir{Root: t.TempDir()}), ""))
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
	tok := strings.Repeat("ab", 32)
	if r, err := http.Post(srv.URL+"/claim", "text/plain", strings.NewReader(tok)); err != nil || r.StatusCode != 200 {
		t.Fatalf("claim: %v %v", r, err)
	}
	if r := get("/refs", tok); r.StatusCode != 200 {
		t.Errorf("API with the token: %d", r.StatusCode)
	}
}

// Every API path the PCs and the web app use must be routed to the store
// (the beat and settings paths were once missing here).
func TestHubRoutesEveryAPIPath(t *testing.T) {
	srv := httptest.NewServer(handler(store.HandlerWith(&store.Dir{Root: t.TempDir()}, time.Minute), ""))
	defer srv.Close()
	h := &store.HTTP{URL: srv.URL}
	tok := strings.Repeat("ef", 32)
	if err := h.Claim(tok); err != nil {
		t.Fatal(err)
	}
	h.Token = tok
	if err := h.Beat("Desktop"); err != nil {
		t.Fatalf("beat: %v", err)
	}
	if pcs, err := h.PCs(); err != nil || len(pcs) != 1 || !pcs[0].Online {
		t.Fatalf("pcs: %v %v", pcs, err)
	}
	if _, err := h.HubSettings(); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if err := h.SetHubSettings(store.HubSettings{OfflineSeconds: 30, CheckMinutes: 5, AutoFetch: true}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if _, err := h.Refs(""); err != nil {
		t.Fatalf("refs: %v", err)
	}
	if _, err := h.Notes(""); err != nil {
		t.Fatalf("notes: %v", err)
	}
}
