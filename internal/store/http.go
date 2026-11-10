package store

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBody = 8 << 20 // a 4 MiB chunk plus encryption overhead fits easily

// Handler serves a Store over HTTP to PCs holding the token.
func Handler(s Store, token string) http.Handler {
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
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "wrong token", http.StatusUnauthorized)
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

var ErrUnauthorized = errors.New("the hub refused the token")

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
