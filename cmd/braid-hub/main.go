// braid-hub stores your projects' encrypted timelines and passes them
// between your PCs. It can't read anything it stores.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/store"
)

func main() {
	data := flag.String("data", "braid-data", "folder for the hub's data")
	addr := flag.String("addr", ":8490", "address to listen on")
	updates := flag.String("updates", "", "folder of signed braid builds to offer PCs (default: \"update\" next to this program)")
	flag.Parse()
	if err := os.MkdirAll(*data, 0o700); err != nil {
		log.Fatal(err)
	}
	token := os.Getenv("BRAID_TOKEN")
	if token == "" {
		tf := filepath.Join(*data, "token")
		b, err := os.ReadFile(tf)
		if errors.Is(err, os.ErrNotExist) {
			r := make([]byte, 24)
			rand.Read(r)
			b = []byte(hex.EncodeToString(r))
			if err := os.WriteFile(tf, b, 0o600); err != nil {
				log.Fatal(err)
			}
		} else if err != nil {
			log.Fatal(err)
		}
		token = strings.TrimSpace(string(b))
	}
	log.Printf("braid hub on %s, data in %s", *addr, *data)
	log.Printf("connect a PC with: braid setup http://<this-server>%s %s", *addr, token)
	log.Printf("web app: http://<this-server>%s (sign in with the same token and your passphrase)", *addr)
	srv := &http.Server{Addr: *addr, Handler: handler(store.Handler(&store.Dir{Root: *data}, token), updatesDir(*updates)), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

//go:embed web
var webFS embed.FS

// handler serves the web app at / and the store API under its own paths.
// The web app's files are public; everything it shows is encrypted on the
// hub and decrypted only in the browser.
func handler(api http.Handler, updates string) http.Handler {
	web, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(web)
	gz := map[string][]byte{} // compressed once: the engine is several MB
	for _, name := range []string{"braid.wasm", "app.js", "wasm_exec.js", "index.html"} {
		if b, err := fs.ReadFile(web, name); err == nil {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(b)
			zw.Close()
			gz[name] = buf.Bytes()
		}
	}
	types := map[string]string{"braid.wasm": "application/wasm", "app.js": "text/javascript; charset=utf-8",
		"wasm_exec.js": "text/javascript; charset=utf-8", "index.html": "text/html; charset=utf-8"}
	mux := http.NewServeMux()
	for _, p := range []string{"/o/", "/r/", "/refs", "/n/", "/notes"} {
		mux.Handle(p, api)
	}
	// Signed builds for PCs. Public: a PC checks the signature, not the hub.
	if updates != "" {
		files := http.StripPrefix("/update/", http.FileServer(http.Dir(updates)))
		mux.HandleFunc("/update/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-cache")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if b, ok := gz[name]; ok && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Type", types[name])
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Write(b)
			return
		}
		files.ServeHTTP(w, r)
	})
	return mux
}

func updatesDir(flagged string) string {
	if flagged != "" {
		return flagged
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	d := filepath.Join(filepath.Dir(exe), "update")
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		log.Printf("offering PCs the builds in %s", d)
		return d
	}
	return ""
}
