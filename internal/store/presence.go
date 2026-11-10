package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Presence is the hub's record of when each PC last phoned home, by the
// hub's own clock (so a PC with a wrong clock still shows correctly). A PC
// counts as online while its last beat is newer than OfflineAfter.
type Presence struct {
	OfflineAfter time.Duration
	file         string
	mu           sync.Mutex
	seen         map[string]time.Time
	now          func() time.Time
}

// PC is one PC as the hub sees it.
type PC struct {
	Name     string    `json:"name"`
	LastSeen time.Time `json:"lastSeen"`
	Online   bool      `json:"online"`
}

var pcNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// NewPresence loads the record kept in dir (so a hub restart doesn't make
// every PC look new). Too small an offlineAfter is raised to 5 seconds.
func NewPresence(dir string, offlineAfter time.Duration) *Presence {
	p := &Presence{OfflineAfter: max(offlineAfter, 5*time.Second), file: filepath.Join(dir, "presence.json"), seen: map[string]time.Time{}, now: time.Now}
	if b, err := os.ReadFile(p.file); err == nil {
		json.Unmarshal(b, &p.seen)
	}
	return p
}

// Beat records that name just phoned home.
func (p *Presence) Beat(name string) bool {
	if !pcNameRe.MatchString(name) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[name] = p.now()
	if b, err := json.Marshal(p.seen); err == nil {
		writeAtomic(p.file, b)
	}
	return true
}

// List returns every PC the hub has heard from, with whether it's online.
func (p *Presence) List() []PC {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	out := []PC{}
	for n, t := range p.seen {
		out = append(out, PC{Name: n, LastSeen: t, Online: now.Sub(t) <= p.OfflineAfter})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
