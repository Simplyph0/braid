// braid-hub stores your projects' encrypted timelines and passes them
// between your PCs. It can't read anything it stores.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
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
	srv := &http.Server{Addr: *addr, Handler: store.Handler(&store.Dir{Root: *data}, token), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
