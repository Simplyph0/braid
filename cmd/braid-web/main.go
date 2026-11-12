//go:build js && wasm

// braid-web is the web app's engine, compiled to WebAssembly. It runs in your
// browser: your passphrase and the decrypted timelines never leave it; the
// hub only ever sends and receives encrypted data.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/Simplyph0/braid/internal/agent"
	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
	"github.com/Simplyph0/braid/internal/timeline"
)

var (
	mu    sync.Mutex
	cfg   *agent.Config
	st    store.Store
	repos = map[string]*timeline.Repo{}
)

func main() {
	js.Global().Set("braid", js.ValueOf(map[string]any{
		"login":          async(login),
		"resume":         async(resume),
		"overview":       async(overview),
		"changes":        async(changes),
		"restore":        async(restore),
		"pin":            async(pin),
		"hubSettings":    async(hubSettings),
		"setHubSettings": async(setHubSettings),
	}))
	select {}
}

// async wraps fn as a JS function returning a Promise.
func async(fn func(args []js.Value) (any, error)) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any {
		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, p []js.Value) any {
			resolve, reject := p[0], p[1]
			go func() {
				v, err := fn(args)
				if err != nil {
					reject.Invoke(js.Global().Get("Error").New(err.Error()))
					return
				}
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				resolve.Invoke(v)
			}()
			return nil
		}))
	})
}

func connect(hub, keyHex string) {
	mu.Lock()
	defer mu.Unlock()
	key, _ := hex.DecodeString(keyHex)
	cfg = &agent.Config{Hub: hub, Token: crypt.AuthToken(key), Key: keyHex}
	st = cfg.Store()
	repos = map[string]*timeline.Repo{}
}

// login(hub, passphrase) → the master key (hex), to remember if asked.
func login(a []js.Value) (any, error) {
	key, _, err := agent.Connect(a[0].String(), a[1].String())
	if err != nil {
		return nil, err
	}
	k := hex.EncodeToString(key)
	connect(a[0].String(), k)
	return k, nil
}

// resume(hub, keyHex) reconnects with a remembered key.
func resume(a []js.Value) (any, error) {
	connect(a[0].String(), a[1].String())
	_, err := st.Refs("")
	return nil, err
}

func repo(name string) (*timeline.Repo, error) {
	mu.Lock()
	defer mu.Unlock()
	if cfg == nil {
		return nil, errors.New("not signed in")
	}
	if r := repos[name]; r != nil {
		return r, nil
	}
	r, err := cfg.Repo(st, name)
	if err == nil {
		repos[name] = r
	}
	return r, err
}

type momentView struct {
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
	PC     string    `json:"pc"`
	Intent string    `json:"intent"`
	Braid  bool      `json:"braid"`
}

type pcView struct {
	agent.Status
	Key      string    `json:"key"` // the PC's name as used on the hub
	Pin      string    `json:"pin"`
	Known    bool      `json:"known"`    // the hub says whether it's online (an older hub doesn't)
	Online   bool      `json:"online"`   // phoned home recently, by the hub's clock
	LastSeen time.Time `json:"lastSeen"` // when it last phoned home
}

type projectView struct {
	Name    string       `json:"name"`
	Main    string       `json:"main"`
	Moments []momentView `json:"moments"`
	PCs     []pcView     `json:"pcs"`
	Waiting []string     `json:"waiting"` // PCs whose work waits on their own strand
}

// overview() → every project: its newest moments and each PC's state.
func overview(_ []js.Value) (any, error) {
	mu.Lock()
	s := st
	mu.Unlock()
	if s == nil {
		return nil, errors.New("not signed in")
	}
	refs, err := s.Refs("")
	if err != nil {
		return nil, err
	}
	seen := map[string]store.PC{}
	if h, ok := s.(*store.HTTP); ok {
		if pcs, err := h.PCs(); err == nil {
			for _, p := range pcs {
				seen[p.Name] = p
			}
		}
	}
	out := []projectView{}
	for ref, tip := range refs {
		name, ok := strings.CutSuffix(ref, "/main")
		if !ok || strings.Contains(name, "/") || strings.HasPrefix(name, "_") { // "_chats" is Braid's own
			continue
		}
		r, err := repo(name)
		if err != nil {
			return nil, err
		}
		pv := projectView{Name: name, Main: tip, Moments: []momentView{}, PCs: []pcView{}, Waiting: []string{}}
		ids, err := r.History(tip, 60)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			m, err := r.Moment(id)
			if err != nil {
				return nil, err
			}
			pv.Moments = append(pv.Moments, momentView{ID: id, Time: m.Time, PC: m.PC, Intent: m.Intent, Braid: len(m.Parents) > 1})
		}
		notes, _ := r.NoteNames("status/")
		for _, n := range notes {
			var ps agent.Status
			if r.Note(n, &ps) != nil {
				continue
			}
			key := strings.TrimPrefix(n, "status/")
			pin, _ := r.Tip("pin/" + key)
			v := pcView{Status: ps, Key: key, Pin: pin}
			if p, ok := seen[key]; ok {
				v.Known, v.Online, v.LastSeen = true, p.Online, p.LastSeen
			} else if len(seen) > 0 {
				v.Known = true // the hub has never heard this PC phone home
			}
			pv.PCs = append(pv.PCs, v)
		}
		sort.Slice(pv.PCs, func(i, j int) bool { return pv.PCs[i].PC < pv.PCs[j].PC })
		for ref := range refs {
			if pc, ok := strings.CutPrefix(ref, name+"/pc/"); ok {
				pv.Waiting = append(pv.Waiting, pc)
			}
		}
		out = append(out, pv)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := time.Time{}, time.Time{}
		if len(out[i].Moments) > 0 {
			ti = out[i].Moments[0].Time
		}
		if len(out[j].Moments) > 0 {
			tj = out[j].Moments[0].Time
		}
		return ti.After(tj)
	})
	return json.Marshal(out)
}

// changes(project, moment) → what that moment changed since the one before.
func changes(a []js.Value) (any, error) {
	r, err := repo(a[0].String())
	if err != nil {
		return nil, err
	}
	files, git, err := r.Changes(a[1].String())
	if err != nil {
		return nil, err
	}
	type change struct{ Path, Kind string }
	list := []change{}
	for _, f := range files {
		list = append(list, change{f.Path, f.Kind})
	}
	return json.Marshal(map[string]any{"files": list, "git": git})
}

// restore(project, moment) makes that moment the newest, for every PC.
func restore(a []js.Value) (any, error) {
	r, err := repo(a[0].String())
	if err != nil {
		return nil, err
	}
	return r.Restore(a[1].String(), "web app")
}

// pin(project, pcKey, moment) makes a PC show a moment; "" follows main again.
func pin(a []js.Value) (any, error) {
	r, err := repo(a[0].String())
	if err != nil {
		return nil, err
	}
	ref := "pin/" + a[1].String()
	old, err := r.Tip(ref)
	if err != nil {
		return nil, err
	}
	if old == a[2].String() {
		return nil, nil
	}
	return nil, r.SetTip(ref, old, a[2].String())
}

// hubSettings() → the hub's own settings.
func hubSettings(_ []js.Value) (any, error) {
	h, err := httpStore()
	if err != nil {
		return nil, err
	}
	hs, err := h.HubSettings()
	if err != nil {
		return nil, err
	}
	return json.Marshal(hs)
}

// setHubSettings(offlineSeconds, checkMinutes, autoFetch)
func setHubSettings(a []js.Value) (any, error) {
	h, err := httpStore()
	if err != nil {
		return nil, err
	}
	return nil, h.SetHubSettings(store.HubSettings{OfflineSeconds: a[0].Int(), CheckMinutes: a[1].Int(), AutoFetch: a[2].Bool()})
}

func httpStore() (*store.HTTP, error) {
	mu.Lock()
	defer mu.Unlock()
	h, ok := st.(*store.HTTP)
	if !ok {
		return nil, errors.New("not signed in")
	}
	return h, nil
}
