package store

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPCsAreOnlineUntilTheyGoQuiet(t *testing.T) {
	d := &Dir{Root: t.TempDir()}
	p := NewPresence(d.Root, time.Minute)
	clock := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	if !p.Beat("Laptop") || p.Beat("../evil") || p.Beat("") {
		t.Fatal("names not checked")
	}
	if l := p.List(); len(l) != 1 || !l[0].Online {
		t.Fatalf("just beat: %+v", l)
	}
	clock = clock.Add(59 * time.Second)
	if !p.List()[0].Online {
		t.Error("59 s of quiet is still online")
	}
	clock = clock.Add(2 * time.Second)
	if p.List()[0].Online {
		t.Error("61 s of quiet should be offline")
	}
	// A hub restart remembers who it heard.
	if l := NewPresence(d.Root, time.Minute).List(); len(l) != 1 || l[0].Name != "Laptop" {
		t.Errorf("not kept across a restart: %+v", l)
	}
	if NewPresence(d.Root, time.Second).OfflineAfter != 5*time.Second {
		t.Error("the offline time has a floor")
	}
}

func TestHubReportsWhoPhonedHome(t *testing.T) {
	srv := httptest.NewServer(HandlerWith(&Dir{Root: t.TempDir()}, 30*time.Second))
	defer srv.Close()
	h := &HTTP{URL: srv.URL}
	if err := h.Claim(strings.Repeat("ab", 32)); err != nil {
		t.Fatal(err)
	}
	h.Token = strings.Repeat("ab", 32)
	if err := h.Beat("Desktop"); err != nil {
		t.Fatal(err)
	}
	pcs, err := h.PCs()
	if err != nil || len(pcs) != 1 || pcs[0].Name != "Desktop" || !pcs[0].Online {
		t.Fatalf("got %+v, %v", pcs, err)
	}
	if err := (&HTTP{URL: srv.URL, Token: "wrong"}).Beat("Evil"); err == nil {
		t.Error("someone without the passphrase phoned home")
	}
}

func TestHubSettingsApplyAtOnce(t *testing.T) {
	d := &Dir{Root: t.TempDir()}
	srv := httptest.NewServer(HandlerWith(d, time.Minute))
	defer srv.Close()
	h := &HTTP{URL: srv.URL}
	h.Claim(strings.Repeat("cd", 32))
	h.Token = strings.Repeat("cd", 32)
	got, err := h.HubSettings()
	if err != nil || got.OfflineSeconds != 60 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	if err := h.SetHubSettings(HubSettings{OfflineSeconds: 5, CheckMinutes: 1, AutoFetch: false}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.HubSettings(); got.OfflineSeconds != 5 || got.CheckMinutes != 1 || got.AutoFetch {
		t.Errorf("not saved: %+v", got)
	}
	if err := h.SetHubSettings(HubSettings{OfflineSeconds: 1, CheckMinutes: 1}); err == nil {
		t.Error("an offline time under 5 s was accepted")
	}
	if err := h.SetHubSettings(HubSettings{OfflineSeconds: 60, CheckMinutes: 0}); err == nil {
		t.Error("an update check under 1 min was accepted")
	}
	if d.Settings(HubSettings{}).OfflineSeconds != 5 {
		t.Error("settings aren't kept on disk")
	}
	if err := (&HTTP{URL: srv.URL, Token: "nope"}).SetHubSettings(HubSettings{OfflineSeconds: 60, CheckMinutes: 60}); err == nil {
		t.Error("someone without the passphrase changed the settings")
	}
	// A hub restart keeps them.
	srv2 := httptest.NewServer(HandlerWith(d, time.Minute))
	defer srv2.Close()
	if pcs := (&HTTP{URL: srv2.URL, Token: h.Token}); true {
		got, _ := pcs.HubSettings()
		if got.OfflineSeconds != 5 {
			t.Errorf("after a restart: %+v", got)
		}
	}
}
