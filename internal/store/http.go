package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxBody = 8 << 20 // a 4 MiB chunk plus encryption overhead fits easily

// Handler serves a hub's store over HTTP. There's no separate password: each
// PC proves it knows the passphrase by sending a key derived from it (see
// crypt.AuthToken), which can't be turned back into the encryption key. The
// hub keeps only a hash of that key, set by the first PC to connect.
func Handler(d *Dir) http.Handler { return HandlerWith(d, time.Minute) }

// HandlerWith is Handler with how long a PC may stay quiet before the hub
// calls it offline.
func HandlerWith(d *Dir, offlineAfter time.Duration) http.Handler {
	var s Store = d
	presence := NewPresence(d.Root, d.Settings(HubSettings{OfflineSeconds: int(offlineAfter.Seconds())}).offline())
	authFile := filepath.Join(d.Root, "auth")
	var mu sync.Mutex
	authHash, _ := os.ReadFile(authFile)
	authorized := func(r *http.Request) bool {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(tok))
		mu.Lock()
		defer mu.Unlock()
		return ok && len(authHash) == len(sum) && subtle.ConstantTimeCompare(authHash, sum[:]) == 1
	}
	public := http.NewServeMux()
	// The salt for deriving keys from the passphrase. Not a secret.
	public.HandleFunc("GET /salt", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		salt, err := s.Get("meta", SaltID)
		if errors.Is(err, ErrNotFound) {
			salt = make([]byte, 32)
			rand.Read(salt)
			err = s.Put("meta", SaltID, salt)
		}
		if !writeErr(w, err) {
			w.Write(salt)
		}
	})
	// The first PC to connect sets the passphrase; later ones must match it.
	public.HandleFunc("POST /claim", func(w http.ResponseWriter, r *http.Request) {
		tok, _ := io.ReadAll(io.LimitReader(r.Body, 256))
		sum := sha256.Sum256(tok)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case len(tok) < 32:
			http.Error(w, "bad request", http.StatusBadRequest)
		case authHash == nil:
			if err := os.WriteFile(authFile, sum[:], 0o600); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			authHash = sum[:]
			log.Printf("this hub now belongs to the passphrase of the PC that just set it up")
		case subtle.ConstantTimeCompare(authHash, sum[:]) != 1:
			http.Error(w, "different passphrase", http.StatusForbidden)
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/o/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		kind, id := r.PathValue("kind"), r.PathValue("id")
		switch r.Method {
		case http.MethodGet:
			b, err := s.Get(kind, id)
			if writeErr(w, err) {
				return
			}
			w.Write(b)
		case http.MethodHead:
			ok, err := s.Has(kind, id)
			if writeErr(w, err) {
				return
			}
			if !ok {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodPut:
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				http.Error(w, "too big", http.StatusRequestEntityTooLarge)
				return
			}
			writeErr(w, s.Put(kind, id, b))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("GET /r/{name...}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Ref(r.PathValue("name"))
		if !writeErr(w, err) {
			io.WriteString(w, v)
		}
	})
	mux.HandleFunc("POST /r/{name...}", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Old, New string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		writeErr(w, s.SetRef(r.PathValue("name"), req.Old, req.New))
	})
	// PCs phone home every 30 seconds; the hub says who is online.
	mux.HandleFunc("POST /beat", func(w http.ResponseWriter, r *http.Request) {
		name, _ := io.ReadAll(io.LimitReader(r.Body, 256))
		if !presence.Beat(string(name)) {
			http.Error(w, "bad name", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("GET /pcs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"offlineAfterSeconds": int(presence.After().Seconds()), "pcs": presence.List()})
	})
	// The hub's own settings, for the web app.
	mux.HandleFunc("GET /hubsettings", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(d.Settings(HubSettings{OfflineSeconds: int(presence.After().Seconds()), CheckMinutes: 60, AutoFetch: true}))
	})
	mux.HandleFunc("PUT /hubsettings", func(w http.ResponseWriter, r *http.Request) {
		var hs HubSettings
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&hs); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := d.SaveSettings(hs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		presence.SetAfter(time.Duration(hs.OfflineSeconds) * time.Second)
	})
	mux.HandleFunc("GET /refs", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.Refs(r.URL.Query().Get("prefix"))
		if !writeErr(w, err) {
			json.NewEncoder(w).Encode(m)
		}
	})
	mux.HandleFunc("GET /n/{name...}", func(w http.ResponseWriter, r *http.Request) {
		b, err := s.Note(r.PathValue("name"))
		if !writeErr(w, err) {
			w.Write(b)
		}
	})
	mux.HandleFunc("PUT /n/{name...}", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNote))
		if err != nil {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		writeErr(w, s.SetNote(r.PathValue("name"), b))
	})
	mux.HandleFunc("GET /notes", func(w http.ResponseWriter, r *http.Request) {
		l, err := s.Notes(r.URL.Query().Get("prefix"))
		if !writeErr(w, err) {
			json.NewEncoder(w).Encode(l)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/salt" || r.URL.Path == "/claim" {
			public.ServeHTTP(w, r)
			return
		}
		if !authorized(r) {
			http.Error(w, "wrong passphrase", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrMoved):
		http.Error(w, "moved", http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
	return true
}

// HTTP is a Store on a hub.
type HTTP struct {
	URL, Token string
	Client     *http.Client
}

var (
	ErrUnauthorized    = errors.New("the hub doesn't accept this passphrase")
	ErrOtherPassphrase = errors.New("this hub is already set up with a different passphrase")
)

// SaltID names the hub's salt object.
const SaltID = "034a55acca7742398afc3ac5c4a6da8dbd570fef420fbf3385a4058a885268cf" // sha256("braid-salt"), as before

// Salt fetches the hub's salt (it makes one on first use).
func (h *HTTP) Salt() ([]byte, error) {
	b, _, err := h.do("GET", "/salt", nil)
	return b, err
}

// Claim proves this PC knows the passphrase; the first PC sets it.
func (h *HTTP) Claim(token string) error {
	_, code, err := h.do("POST", "/claim", []byte(token))
	if code == http.StatusForbidden {
		return ErrOtherPassphrase
	}
	return err
}

func (h *HTTP) do(method, path string, body []byte) ([]byte, int, error) {
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("can't reach the hub: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch resp.StatusCode {
	case http.StatusOK:
		return b, 200, err
	case http.StatusNotFound:
		return nil, 404, ErrNotFound
	case http.StatusConflict:
		return nil, 409, ErrMoved
	case http.StatusUnauthorized:
		return nil, 401, ErrUnauthorized
	case http.StatusForbidden:
		return nil, 403, ErrOtherPassphrase
	}
	return nil, resp.StatusCode, fmt.Errorf("hub: %s: %s", resp.Status, strings.TrimSpace(string(b)))
}

func obj(kind, id string) string { return "/o/" + url.PathEscape(kind) + "/" + url.PathEscape(id) }

func (h *HTTP) Put(kind, id string, blob []byte) error {
	_, _, err := h.do("PUT", obj(kind, id), blob)
	return err
}

func (h *HTTP) Get(kind, id string) ([]byte, error) {
	b, _, err := h.do("GET", obj(kind, id), nil)
	return b, err
}

func (h *HTTP) Has(kind, id string) (bool, error) {
	_, _, err := h.do("HEAD", obj(kind, id), nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (h *HTTP) Ref(name string) (string, error) {
	if !ValidRef(name) {
		return "", errors.New("bad ref name")
	}
	b, _, err := h.do("GET", "/r/"+name, nil)
	return strings.TrimSpace(string(b)), err
}

func (h *HTTP) SetRef(name, old, new string) error {
	if !ValidRef(name) {
		return errors.New("bad ref name")
	}
	body, _ := json.Marshal(map[string]string{"Old": old, "New": new})
	_, _, err := h.do("POST", "/r/"+name, body)
	return err
}

func (h *HTTP) Refs(prefix string) (map[string]string, error) {
	b, _, err := h.do("GET", "/refs?prefix="+url.QueryEscape(prefix), nil)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(b, &m)
}

func (h *HTTP) SetNote(name string, blob []byte) error {
	if !ValidRef(name) {
		return errors.New("bad note name")
	}
	_, _, err := h.do("PUT", "/n/"+name, blob)
	return err
}

func (h *HTTP) Note(name string) ([]byte, error) {
	if !ValidRef(name) {
		return nil, errors.New("bad note name")
	}
	b, _, err := h.do("GET", "/n/"+name, nil)
	return b, err
}

func (h *HTTP) Notes(prefix string) ([]string, error) {
	b, _, err := h.do("GET", "/notes?prefix="+url.QueryEscape(prefix), nil)
	if err != nil {
		return nil, err
	}
	var l []string
	return l, json.Unmarshal(b, &l)
}

// Beat tells the hub this PC is alive.
func (h *HTTP) Beat(pc string) error {
	_, _, err := h.do("POST", "/beat", []byte(pc))
	return err
}

// PCs asks the hub which PCs are online.
func (h *HTTP) PCs() ([]PC, error) {
	b, _, err := h.do("GET", "/pcs", nil)
	if err != nil {
		return nil, err
	}
	var out struct{ PCs []PC }
	return out.PCs, json.Unmarshal(b, &out)
}

func (s HubSettings) offline() time.Duration { return time.Duration(s.OfflineSeconds) * time.Second }

// HubSettings reads the hub's settings.
func (h *HTTP) HubSettings() (HubSettings, error) {
	var s HubSettings
	b, _, err := h.do("GET", "/hubsettings", nil)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// SetHubSettings changes them; the hub applies them at once.
func (h *HTTP) SetHubSettings(s HubSettings) error {
	b, _ := json.Marshal(s)
	_, _, err := h.do("PUT", "/hubsettings", b)
	return err
}
