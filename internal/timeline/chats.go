package timeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/store"
)

// Claude Code chats travel with their project, outside the folder's own
// moments: a chat is an append-only list of messages, so two PCs' copies are
// joined by taking every message from both, and there's never anything to
// decide. The hub keeps them on the project's "chats" strand.

// projectMark stands for the project folder inside stored chats, so each PC
// sees its own path (C:\Users\James\Projects\X here, ~/Projects/X there).
const projectMark = "@@braid-project@@"

// ChatReport says what a chat sync did.
type ChatReport struct {
	Pulled, Pushed int
}

// SyncChats joins this PC's chats in dir (Claude Code's folder for the
// project) with the hub's. A chat written to within settle of now is left
// for later: Claude may be writing it.
func (r *Repo) SyncChats(dir, folder, pc string, settle time.Duration, now time.Time) (ChatReport, error) {
	var rep ChatReport
	tip, err := r.Tip("chats")
	if err != nil {
		return rep, err
	}
	hub, err := r.MomentTree(tip)
	if err != nil {
		return rep, err
	}
	local := map[string][]byte{}
	busy := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || !validPath(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()) < settle || (len(b) > 0 && b[len(b)-1] != '\n') {
			busy[e.Name()] = true // being written: read it next time
			continue
		}
		local[e.Name()] = toStored(b, folder)
	}

	next := &Tree{Files: map[string]Entry{}}
	for k, v := range hub.Files {
		next.Files[k] = v
	}
	names := map[string]bool{}
	for k := range local {
		names[k] = true
	}
	for k := range hub.Files {
		names[k] = true
	}
	for name := range names {
		if busy[name] || !validPath(name) || strings.Contains(name, "/") {
			continue
		}
		var hb []byte
		if e, ok := hub.Files[name]; ok {
			if hb, err = r.read(e); err != nil {
				return rep, err
			}
		}
		lb, haveLocal := local[name]
		merged := joinChats(hb, lb)
		if !bytes.Equal(merged, hb) {
			size, chunks, err := r.putBytes(merged)
			if err != nil {
				return rep, err
			}
			next.Files[name] = Entry{Size: size, Chunks: chunks, Mtime: now.UTC()}
			rep.Pushed++
		}
		if !haveLocal || !bytes.Equal(merged, lb) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return rep, err
			}
			p := filepath.Join(dir, name)
			tmp := p + tmpSuffix
			if err := os.WriteFile(tmp, fromStored(merged, folder), 0o600); err != nil {
				return rep, err
			}
			if err := os.Rename(tmp, p); err != nil {
				return rep, err
			}
			rep.Pulled++
		}
	}
	if rep.Pushed == 0 {
		return rep, nil
	}
	tid, err := r.PutTree(next)
	if err != nil {
		return rep, err
	}
	var parents []string
	if tip != "" {
		parents = []string{tip}
	}
	mid, err := r.PutMoment(&Moment{Tree: tid, Parents: parents, PC: pc, Time: now.UTC(), Intent: "chats"})
	if err != nil {
		return rep, err
	}
	if err := r.SetTip("chats", tip, mid); errors.Is(err, store.ErrMoved) {
		return rep, nil // another PC got there first; joined again next time
	} else if err != nil {
		return rep, err
	}
	return rep, nil
}

// joinChats keeps every line of a, then the lines of b that a lacks. Lines
// are known by their message id; lines without one by their whole text.
func joinChats(a, b []byte) []byte {
	seen := map[string]bool{}
	var out []byte
	for _, part := range [][]byte{a, b} {
		for _, line := range bytes.SplitAfter(part, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if line[len(line)-1] != '\n' {
				line = append(line, '\n')
			}
			var id struct {
				UUID string `json:"uuid"`
			}
			k := string(line)
			if json.Unmarshal(line, &id) == nil && id.UUID != "" {
				k = "uuid:" + id.UUID
			}
			if !seen[k] {
				seen[k] = true
				out = append(out, line...)
			}
		}
	}
	return out
}

// jsonPath is how a path appears inside a JSON string.
func jsonPath(p string) string {
	b, _ := json.Marshal(filepath.Clean(p))
	return string(b[1 : len(b)-1])
}

// shortcut: only exact spellings of the folder are rewritten (not other case or slash styles), enough for Claude Code's own records
func toStored(b []byte, folder string) []byte {
	return bytes.ReplaceAll(b, []byte(jsonPath(folder)), []byte(projectMark))
}

func fromStored(b []byte, folder string) []byte {
	return bytes.ReplaceAll(b, []byte(projectMark), []byte(jsonPath(folder)))
}
