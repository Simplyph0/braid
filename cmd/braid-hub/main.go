// braid-hub stores your projects' encrypted timelines and passes them
// between your PCs. It can't read anything it stores.
package main

import (
	"bytes"
	"compress/gzip"
	"embed"
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
	d := &store.Dir{Root: *data}
	log.Printf("braid hub on %s, data in %s", *addr, *data)
	if _, err := os.Stat(filepath.Join(*data, "auth")); err != nil {
		log.Printf("new hub: the first PC to run `braid setup http://<this-server>%s` sets its passphrase", *addr)
	}
	log.Printf("web app: http://<this-server>%s (sign in with your passphrase)", *addr)
	srv := &http.Server{Addr: *addr, Handler: handler(store.Handler(d), updatesDir(*updates)), ReadHeaderTimeout: 10 * time.Second}
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
	for _, p := range []string{"/o/", "/r/", "/refs", "/n/", "/notes", "/salt", "/claim"} {
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
