// Package update keeps the braid program on each PC current. The hub serves
// the latest build with a manifest signed by the release key; a PC installs
// a build only if the signature and the file's checksum both match, so a
// hub (or anyone in between) can't slip it a program of their own.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version is set at build time (-X …/update.Version=v1.2.3). "dev" builds
// never update themselves.
var Version = "dev"

// PublicKey checks release signatures (base64, ed25519). The private half
// lives only in the GitHub repo's secrets.
var PublicKey = "yFCAz7bM0cTD7428ojv5CtRYA+vgf17hYuIMVTy/Rkk="

// Manifest lists one release's files.
type Manifest struct {
	Version string          `json:"version"`
	Files   map[string]File `json:"files"` // by "<os>-<arch>"
}

type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// Newer reports whether version a is newer than b ("v1.2.3" style).
func Newer(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(url string, max int64) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoUpdates
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if int64(len(b)) > max {
		return nil, errors.New("download too big")
	}
	return b, err
}

var errNoUpdates = errors.New("the hub has no updates to offer")

// Latest fetches and verifies the hub's manifest.
func Latest(hub string) (*Manifest, error) {
	m, _, _, err := fetchManifest(strings.TrimRight(hub, "/") + "/update/")
	return m, err
}

// fetchManifest gets base+manifest.json and its signature, and checks it.
func fetchManifest(base string) (m *Manifest, raw, sig []byte, err error) {
	if raw, err = get(base+"manifest.json", 1<<20); err != nil {
		return nil, nil, nil, err
	}
	if sig, err = get(base+"manifest.sig", 1024); err != nil {
		return nil, nil, nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, nil, nil, errors.New("this build has no valid release key")
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(pub, raw, s) {
		return nil, nil, nil, errors.New("the update's signature doesn't match: not installing it")
	}
	m = &Manifest{}
	return m, raw, sig, json.Unmarshal(raw, m)
}

// Mirror copies a newer signed release from src (a folder of release files,
// such as GitHub's latest release) into dir, which a hub serves to PCs. So a
// hub hands out new versions without being updated itself. It returns the
// version copied, or "" when dir was already current.
func Mirror(src, dir string) (string, error) {
	m, raw, sig, err := fetchManifest(strings.TrimRight(src, "/") + "/")
	if err != nil {
		return "", err
	}
	var cur Manifest
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		json.Unmarshal(b, &cur)
	}
	if cur.Version != "" && !Newer(m.Version, cur.Version) {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, f := range m.Files {
		if strings.ContainsAny(f.Name, `/\`) || f.Name == "" || strings.HasPrefix(f.Name, ".") {
			return "", fmt.Errorf("bad file name %q in the release", f.Name)
		}
		data, err := get(strings.TrimRight(src, "/")+"/"+f.Name, 200<<20)
		if err != nil {
			return "", err
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != f.SHA256 {
			return "", fmt.Errorf("%s doesn't match the signed checksum", f.Name)
		}
		if err := writeFile(filepath.Join(dir, f.Name), data); err != nil {
			return "", err
		}
	}
	if err := writeFile(filepath.Join(dir, "manifest.sig"), sig); err != nil {
		return "", err
	}
	return m.Version, writeFile(filepath.Join(dir, "manifest.json"), raw) // last: PCs see the new version only once everything is there
}

func writeFile(p string, data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Install downloads this platform's build from the hub if it's newer than
// the running one and puts it in place of exe. It returns the new version,
// or "" when there was nothing to do.
func Install(hub, exe string) (string, error) {
	if Version == "dev" {
		return "", nil
	}
	m, err := Latest(hub)
	if errors.Is(err, errNoUpdates) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !Newer(m.Version, Version) {
		return "", nil
	}
	f, ok := m.Files[Platform()]
	if !ok || strings.ContainsAny(f.Name, `/\`) || f.Name == "" || f.Name == ".." {
		return "", fmt.Errorf("version %s has no build for %s", m.Version, Platform())
	}
	data, err := get(strings.TrimRight(hub, "/")+"/update/"+f.Name, 200<<20)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		return "", errors.New("the downloaded update is damaged or was changed: not installing it")
	}
	return m.Version, replace(exe, data)
}

// replace swaps exe for data. The old program stays as exe.old until the
// next update, so a broken update can be undone by hand.
func replace(exe string, data []byte) error {
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, "."+filepath.Base(exe)+".new")
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("can't write next to %s: %w", exe, err)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil { // Windows can rename a running program, not overwrite it
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	return nil
}
