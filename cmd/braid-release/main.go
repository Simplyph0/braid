// braid-release signs a release: it writes manifest.json and manifest.sig
// for the braid builds in a folder. The key comes from BRAID_SIGNING_KEY
// (base64 ed25519 seed). `braid-release -genkey` makes a new key pair.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Simplyph0/braid/internal/update"
)

func main() {
	genkey := flag.Bool("genkey", false, "print a new key pair")
	dir := flag.String("dir", "dist/update", "folder with braid-<os>-<arch>[.exe] builds")
	version := flag.String("version", "", "release version, e.g. v0.2.0")
	flag.Parse()
	if *genkey {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		fmt.Println("public: " + base64.StdEncoding.EncodeToString(pub))
		fmt.Println("secret: " + base64.StdEncoding.EncodeToString(priv.Seed()))
		return
	}
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("BRAID_SIGNING_KEY"))
	if err != nil || len(seed) != ed25519.SeedSize {
		log.Fatal("BRAID_SIGNING_KEY isn't a base64 ed25519 seed")
	}
	if update.Newer("v0.0.0", *version) || !update.Newer(*version, "v0.0.0") {
		log.Fatal("give -version like v0.2.0")
	}
	m := update.Manifest{Version: *version, Files: map[string]update.File{}}
	names, _ := filepath.Glob(filepath.Join(*dir, "braid-*"))
	for _, p := range names {
		name := filepath.Base(p)
		plat := strings.TrimSuffix(strings.TrimPrefix(name, "braid-"), ".exe")
		if strings.Count(plat, "-") != 1 {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		sum := sha256.Sum256(b)
		m.Files[plat] = update.File{Name: name, SHA256: hex.EncodeToString(sum[:])}
	}
	if len(m.Files) == 0 {
		log.Fatal("no builds in ", *dir)
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), raw)
	if err := os.WriteFile(filepath.Join(*dir, "manifest.json"), raw, 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*dir, "manifest.sig"), []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("signed %s for %d platform(s)\n", *version, len(m.Files))
}
