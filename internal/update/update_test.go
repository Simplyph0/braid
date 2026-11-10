package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release serves a signed release containing build for this platform.
func release(t *testing.T, version string, build []byte, tamper func(files map[string][]byte)) string {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := PublicKey
	PublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKey = old })
	sum := sha256.Sum256(build)
	m := Manifest{Version: version, Files: map[string]File{Platform(): {Name: "braid-" + Platform(), SHA256: hex.EncodeToString(sum[:])}}}
	raw, _ := json.Marshal(m)
	files := map[string][]byte{
		"manifest.json":       raw,
		"manifest.sig":        []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))),
		"braid-" + Platform(): build,
	}
	if tamper != nil {
		tamper(files)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/update/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func fakeExe(t *testing.T) string {
	exe := filepath.Join(t.TempDir(), "braid")
	os.WriteFile(exe, []byte("old build"), 0o755)
	return exe
}

func setVersion(t *testing.T, v string) {
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func TestInstallsNewerSignedBuild(t *testing.T) {
	setVersion(t, "v0.1.0")
	exe := fakeExe(t)
	v, err := Install(release(t, "v0.2.0", []byte("new build"), nil), exe)
	if err != nil || v != "v0.2.0" {
		t.Fatalf("got %q, %v", v, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new build" {
		t.Errorf("exe holds %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old build" {
		t.Errorf("old build not kept: %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode()&0o100 == 0 {
		t.Error("not executable")
	}
}

func TestRefusesBadUpdates(t *testing.T) {
	setVersion(t, "v0.1.0")
	cases := map[string]func(map[string][]byte){
		"changed build": func(f map[string][]byte) { f["braid-"+Platform()] = []byte("evil") },
		"changed manifest": func(f map[string][]byte) {
			f["manifest.json"] = []byte(strings.Replace(string(f["manifest.json"]), "v0.2.0", "v0.3.0", 1))
		},
		"no signature":      func(f map[string][]byte) { delete(f, "manifest.sig") },
		"garbage signature": func(f map[string][]byte) { f["manifest.sig"] = []byte("AAAA") },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			exe := fakeExe(t)
			if v, err := Install(release(t, "v0.2.0", []byte("new build"), tamper), exe); err == nil && v != "" {
				t.Fatalf("installed: %q", v)
			}
			if b, _ := os.ReadFile(exe); string(b) != "old build" {
				t.Fatalf("exe changed to %q", b)
			}
		})
	}
}

func TestNothingToDo(t *testing.T) {
	exe := fakeExe(t)
	for _, tc := range []struct{ running, offered string }{{"v0.2.0", "v0.2.0"}, {"v0.3.0", "v0.2.0"}, {"dev", "v9.0.0"}} {
		setVersion(t, tc.running)
		if v, err := Install(release(t, tc.offered, []byte("x"), nil), exe); v != "" || err != nil {
			t.Errorf("%s offered %s: %q %v", tc.running, tc.offered, v, err)
		}
	}
	setVersion(t, "v0.1.0")
	srv := httptest.NewServer(http.NotFoundHandler()) // a hub with no updates folder
	defer srv.Close()
	if v, err := Install(srv.URL, exe); v != "" || err != nil {
		t.Errorf("no updates: %q %v", v, err)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v0.2.0", "v0.1.9", true}, {"v0.10.0", "v0.9.0", true}, {"v1.0.0", "v1.0.0", false}, {"v0.1.0", "v0.2.0", false}, {"dev", "v0.1.0", false}, {"v1.2", "v1.0.0", false}} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
