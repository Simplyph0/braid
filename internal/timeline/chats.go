package timeline

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/store"
)

// Every Claude chat on a PC travels through one hidden project, "_chats":
// Claude Code's transcripts (and the files beside them) for every folder,
// and the Claude desktop app's session records. A transcript only ever grows,
// so two PCs' copies are joined by taking every message from both: there is
// nothing to decide. Paths to your folders are stored as placeholders and
// written back as each PC's own paths.

// ChatsProject is the hidden project that holds the chats.
const ChatsProject = "_chats"

// ChatSet describes this PC's Claude data.
type ChatSet struct {
	ClaudeHome  string            // ~/.claude
	SessionsDir string            // the desktop app's claude-code-sessions ("" if there's no desktop app)
	ScratchRoot string            // the desktop app's scratch-workspaces
	Home        string            // the user's home
	Projects    map[string]string // Braid project name -> folder on this PC
	AppRunning  bool              // the desktop app is open: its records can't be written
}

// ChatReport says what a chat sync did.
type ChatReport struct {
	Pulled, Pushed int
	Pending        int // desktop-app records waiting for the app to be closed
	Files          int // chat files on this PC
}

// ChatState is what a chat sync remembers between runs (saved by the caller).
type ChatState struct {
	Files map[string]chatFile `json:"files"`          // local path -> what it was when last read or written
	Dirs  map[string]string   `json:"dirs,omitempty"` // folder hash -> portable folder, as read from the hub
}

type chatFile struct {
	Size  int64  `json:"s"`
	Mtime int64  `json:"t"`
	Entry Entry  `json:"e"`
	Role  string `json:"r"`
}

type root struct{ token, path string }

func (s ChatSet) roots() []root {
	var out []root
	for n, p := range s.Projects {
		out = append(out, root{"{{p:" + n + "}}", filepath.Clean(p)})
	}
	if s.ScratchRoot != "" {
		out = append(out, root{"{{scratch}}", filepath.Clean(s.ScratchRoot)})
	}
	if s.Home != "" {
		out = append(out, root{"{{home}}", filepath.Clean(s.Home)})
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].path) > len(out[j].path) })
	return out
}

// portablePath turns a local folder into a placeholder form shared by all PCs.
func portablePath(p string, roots []root) string {
	p = filepath.Clean(p)
	for _, r := range roots {
		if p == r.path {
			return r.token
		}
		if strings.HasPrefix(p, r.path+string(filepath.Separator)) {
			return r.token + "/" + filepath.ToSlash(p[len(r.path)+1:])
		}
	}
	return filepath.ToSlash(p)
}

var tokenRe = regexp.MustCompile(`^(\{\{p:[^}/\\]+\}\}|\{\{scratch\}\}|\{\{home\}\})(/.*)?$`)

// localPath is the reverse. ok is false when it points into something this PC
// doesn't have (a project that isn't added here).
func localPath(q string, roots []root) (string, bool) {
	if m := tokenRe.FindStringSubmatch(q); m != nil {
		for _, r := range roots {
			if r.token == m[1] {
				return filepath.Join(r.path, filepath.FromSlash(strings.TrimPrefix(m[2], "/"))), true
			}
		}
		return "", false
	}
	if strings.HasPrefix(q, "{{") || strings.ContainsAny(q, "\x00\n") || !filepath.IsAbs(filepath.FromSlash(q)) {
		return "", false
	}
	return filepath.FromSlash(q), true
}

func isBoundary(c byte) bool {
	return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.')
}

// replacePaths swaps each root's path (as it appears inside JSON text) for
// its token, or back; only whole paths are replaced, not prefixes of longer names.
func replacePaths(text []byte, roots []root, toToken bool) []byte {
	for _, r := range roots {
		from, to := []byte(jsonPath(r.path)), []byte(r.token)
		if !toToken {
			from, to = to, from
		}
		var out bytes.Buffer
		for {
			i := bytes.Index(text, from)
			if i < 0 {
				break
			}
			end := i + len(from)
			okBefore := !toToken || i == 0 || isBoundary(text[i-1])
			okAfter := end == len(text) || isBoundary(text[end])
			out.Write(text[:i])
			if okBefore && okAfter {
				out.Write(to)
			} else {
				out.Write(from)
			}
			text = text[end:]
		}
		out.Write(text)
		text = out.Bytes()
	}
	return text
}

func jsonPath(p string) string {
	b, _ := json.Marshal(p)
	return string(b[1 : len(b)-1])
}

func textual(rel string) bool {
	return strings.HasSuffix(rel, ".jsonl") || strings.HasSuffix(rel, ".json")
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// encodeDir is how Claude Code names a folder's chat folder.
func encodeDir(folder string) string { return nonAlnum.ReplaceAllString(filepath.Clean(folder), "-") }

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:12])
}

// chatCwd reads the working folder a chat folder was used for, from the
// first lines of its newest transcript.
func chatCwd(dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Slice(files, func(i, j int) bool {
		a, _ := os.Stat(files[i])
		b, _ := os.Stat(files[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for i := 0; i < 60 && sc.Scan(); i++ {
			var l struct{ Cwd string }
			if json.Unmarshal(sc.Bytes(), &l) == nil && l.Cwd != "" {
				fh.Close()
				return l.Cwd
			}
		}
		fh.Close()
	}
	return ""
}

type localChat struct {
	abs, role string
	fi        fs.FileInfo
	entry     Entry
}

// scanChats lists this PC's chat files by their place in the shared tree.
func (r *Repo) scanChats(set ChatSet, st *ChatState, now time.Time, settle time.Duration, busy map[string]bool) (map[string]*localChat, map[string]string, error) {
	roots := set.roots()
	local := map[string]*localChat{}
	meta := map[string]string{} // tree path "m/<h>" -> portable folder
	add := func(tp, abs, role string) error {
		fi, err := os.Lstat(abs)
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		lc := &localChat{abs: abs, role: role, fi: fi}
		if c, ok := st.Files[abs]; ok && c.Size == fi.Size() && c.Mtime == fi.ModTime().UnixNano() {
			lc.entry = c.Entry
		} else {
			b, err := os.ReadFile(abs)
			if err != nil {
				return nil
			}
			if role == "t" && strings.HasSuffix(tp, ".jsonl") && (now.Sub(fi.ModTime()) < settle || (len(b) > 0 && b[len(b)-1] != '\n')) {
				busy[tp] = true // being written right now: next time
				return nil
			}
			if textual(tp) {
				b = replacePaths(b, roots, true)
			}
			size, chunks, err := r.putBytes(b)
			if err != nil {
				return err
			}
			lc.entry = Entry{Size: size, Chunks: chunks, Mtime: fi.ModTime().UTC()}
			st.Files[abs] = chatFile{Size: fi.Size(), Mtime: fi.ModTime().UnixNano(), Entry: lc.entry, Role: role}
		}
		local[tp] = lc
		return nil
	}
	projDir := filepath.Join(set.ClaudeHome, "projects")
	dirs, _ := os.ReadDir(projDir)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(projDir, d.Name())
		cwd := chatCwd(dir)
		if cwd == "" {
			continue
		}
		q := portablePath(cwd, roots)
		h := shortHash(q)
		meta["m/"+h] = q
		err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(p, dir+string(filepath.Separator)))
			if !validPath(rel) || strings.HasSuffix(rel, tmpSuffix) {
				return nil
			}
			return add("t/"+h+"/"+rel, p, "t")
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if set.SessionsDir != "" {
		files, _ := filepath.Glob(filepath.Join(set.SessionsDir, "*", "*", "local_*.json"))
		for _, p := range files {
			rel := filepath.ToSlash(strings.TrimPrefix(p, set.SessionsDir+string(filepath.Separator)))
			if validPath(rel) {
				if err := add("d/"+rel, p, "d"); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	return local, meta, nil
}

// SyncAllChats joins every chat on this PC with the hub's copy. A chat
// written within settle of now is left for the next run: Claude may be
// writing it.
func (r *Repo) SyncAllChats(set ChatSet, pc string, settle time.Duration, now time.Time, st *ChatState) (ChatReport, error) {
	var rep ChatReport
	if st.Files == nil {
		st.Files = map[string]chatFile{}
	}
	if st.Dirs == nil {
		st.Dirs = map[string]string{}
	}
	for attempt := 0; attempt < 3; attempt++ {
		busy := map[string]bool{}
		local, meta, err := r.scanChats(set, st, now, settle, busy)
		if err != nil {
			return rep, err
		}
		rep.Files = len(local)
		tip, err := r.Tip("main")
		if err != nil {
			return rep, err
		}
		hub, err := r.MomentTree(tip)
		if err != nil {
			return rep, err
		}
		next := &Tree{Files: map[string]Entry{}}
		for k, v := range hub.Files {
			next.Files[k] = v
		}
		pushed, pulled := 0, 0
		pending := 0
		roots := set.roots()
		for mp, q := range meta { // folders this PC knows, for others to find
			if _, ok := next.Files[mp]; !ok {
				size, chunks, err := r.putBytes([]byte(q))
				if err != nil {
					return rep, err
				}
				next.Files[mp] = Entry{Size: size, Chunks: chunks}
				pushed++
			}
		}
		paths := map[string]bool{}
		for p := range local {
			paths[p] = true
		}
		for p := range hub.Files {
			if !strings.HasPrefix(p, "m/") {
				paths[p] = true
			}
		}
		keys := make([]string, 0, len(paths))
		for p := range paths {
			keys = append(keys, p)
		}
		sort.Strings(keys)
		for _, tp := range keys {
			if busy[tp] {
				continue
			}
			l := local[tp]
			h, inHub := hub.Files[tp]
			dst, kind, err := r.chatDest(set, roots, st, hub, meta, tp)
			if err != nil {
				return rep, err
			}
			switch {
			case l != nil && inHub && l.entry.same(h):
				continue
			case l != nil && !inHub: // only here: back it up
				next.Files[tp] = l.entry
				pushed++
			case l == nil && inHub: // only on the hub: bring it here (without reading it if it can't be placed)
				switch kind {
				case destSkip:
				case destPending:
					pending++
				default:
					data, err := r.read(h)
					if err != nil {
						return rep, err
					}
					if wrote, err := r.writeChat(set, roots, st, dst, tp, data, h); err != nil {
						return rep, err
					} else if wrote {
						pulled++
					}
				}
			default: // both, and different
				data, entry, push, writeLocal, err := r.joinChat(tp, l, h)
				if err != nil {
					return rep, err
				}
				if push {
					next.Files[tp] = entry
					pushed++
				}
				if writeLocal {
					switch kind {
					case destPending:
						pending++
					case destOK:
						if wrote, err := r.writeChat(set, roots, st, dst, tp, data, entry); err != nil {
							return rep, err
						} else if wrote {
							pulled++
						}
					}
				} else { // this PC already has everything: remember it matches
					st.Files[l.abs] = chatFile{Size: l.fi.Size(), Mtime: l.fi.ModTime().UnixNano(), Entry: entry, Role: l.role}
				}
			}
		}
		if pushed > 0 {
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
			if err := r.SetTip("main", tip, mid); errors.Is(err, store.ErrMoved) {
				continue // another PC got there first: join again with its version
			} else if err != nil {
				return rep, err
			}
		}
		rep.Pushed, rep.Pulled, rep.Pending = pushed, pulled, pending
		return rep, nil
	}
	return rep, errors.New("the chats kept changing on your hub; trying again next time")
}

// joinChat joins this PC's and the hub's copy of one file. data is what both
// should hold afterwards; push says the hub's copy changes, writeLocal that
// this PC's copy does.
func (r *Repo) joinChat(tp string, l *localChat, h Entry) (data []byte, entry Entry, push, writeLocal bool, err error) {
	hb, err := r.read(h)
	if err != nil {
		return nil, Entry{}, false, false, err
	}
	lb, err := r.read(l.entry)
	if err != nil {
		return nil, Entry{}, false, false, err
	}
	var merged []byte
	switch {
	case strings.HasSuffix(tp, ".jsonl"): // chats only grow: keep every message from both
		merged = joinChats(hb, lb)
	case l.role == "d": // a desktop-app record: the more recently used copy wins
		merged = hb
		if lastActivity(lb) > lastActivity(hb) {
			merged = lb
		}
	default: // anything else: the newer file wins
		merged = hb
		if l.entry.Mtime.After(h.Mtime) {
			merged = lb
		}
	}
	entry, writeLocal = h, !bytes.Equal(merged, lb)
	if !bytes.Equal(merged, hb) {
		size, chunks, err := r.putBytes(merged)
		if err != nil {
			return nil, Entry{}, false, false, err
		}
		entry, push = Entry{Size: size, Chunks: chunks, Mtime: l.entry.Mtime}, true
	}
	return merged, entry, push, writeLocal, nil
}

func lastActivity(b []byte) float64 {
	var v struct {
		LastActivityAt float64 `json:"lastActivityAt"`
	}
	json.Unmarshal(b, &v)
	return v.LastActivityAt
}

type destKind int

const (
	destSkip    destKind = iota // can't be placed on this PC (a project it doesn't have, no desktop app)
	destPending                 // a desktop-app record while the app is open
	destOK
)

var hashRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// chatDest says where a file of the shared tree goes on this PC.
func (r *Repo) chatDest(set ChatSet, roots []root, st *ChatState, hub *Tree, meta map[string]string, tp string) (string, destKind, error) {
	parts := strings.SplitN(tp, "/", 3)
	if len(parts) != 3 {
		return "", destSkip, nil
	}
	switch parts[0] {
	case "t": // t/<hash>/<rel>
		if !hashRe.MatchString(parts[1]) || !validPath(parts[2]) {
			return "", destSkip, nil
		}
		q := meta["m/"+parts[1]]
		if q == "" {
			q = st.Dirs[parts[1]]
		}
		if q == "" {
			me, ok := hub.Files["m/"+parts[1]]
			if !ok {
				return "", destSkip, nil
			}
			b, err := r.read(me)
			if err != nil {
				return "", destSkip, err
			}
			q = string(b)
			st.Dirs[parts[1]] = q
		}
		cwd, ok := localPath(q, roots)
		if !ok {
			return "", destSkip, nil
		}
		return filepath.Join(set.ClaudeHome, "projects", encodeDir(cwd), filepath.FromSlash(parts[2])), destOK, nil
	case "d": // d/<account>/<org>/local_x.json
		sub := strings.SplitN(tp[2:], "/", 3)
		if set.SessionsDir == "" || len(sub) != 3 || !validPath(sub[0]) || !validPath(sub[1]) || !strings.HasPrefix(sub[2], "local_") || !validPath(sub[2]) {
			return "", destSkip, nil
		}
		if set.AppRunning {
			return "", destPending, nil
		}
		acct, org := localAccount(set.SessionsDir, sub[0], sub[1])
		return filepath.Join(set.SessionsDir, acct, org, sub[2]), destOK, nil
	}
	return "", destSkip, nil
}

// writeChat puts one file from the shared tree onto this PC, with this PC's
// paths. It remembers the result, so it isn't mistaken for a change.
func (r *Repo) writeChat(set ChatSet, roots []root, st *ChatState, dst, tp string, stored []byte, e Entry) (bool, error) {
	data, mode := stored, fs.FileMode(0o600)
	if textual(tp) {
		data = replacePaths(stored, roots, false)
		if bytes.Contains(data, []byte("{{p:")) || (bytes.Contains(data, []byte("{{scratch}}")) && set.ScratchRoot == "") {
			return false, nil // mentions a project this PC doesn't have
		}
	}
	if strings.HasPrefix(tp, "d/") {
		mode = 0o644
		var rec struct{ Cwd string }
		if json.Unmarshal(data, &rec) == nil && rec.Cwd != "" && set.ScratchRoot != "" && strings.HasPrefix(rec.Cwd, set.ScratchRoot+string(filepath.Separator)) {
			os.MkdirAll(rec.Cwd, 0o755) // a folderless chat's working folder
		}
	}
	if err := writeChatFile(dst, data, mode); err != nil {
		return false, err
	}
	if fi, err := os.Stat(dst); err == nil {
		role := "t"
		if strings.HasPrefix(tp, "d/") {
			role = "d"
		}
		st.Files[dst] = chatFile{Size: fi.Size(), Mtime: fi.ModTime().UnixNano(), Entry: e, Role: role}
	}
	return true, nil
}

// localAccount picks the account and organisation folders on this PC: the
// same names if they exist, else the only ones there are, else the same names.
func localAccount(sessions, acct, org string) (string, string) {
	if fi, err := os.Stat(filepath.Join(sessions, acct, org)); err == nil && fi.IsDir() {
		return acct, org
	}
	pairs, _ := filepath.Glob(filepath.Join(sessions, "*", "*"))
	var dirs []string
	for _, p := range pairs {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			dirs = append(dirs, p)
		}
	}
	if len(dirs) == 1 {
		rel, _ := filepath.Rel(sessions, dirs[0])
		a, o, _ := strings.Cut(filepath.ToSlash(rel), "/")
		return a, o
	}
	return acct, org
}

func writeChatFile(dst string, data []byte, mode fs.FileMode) error {
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + tmpSuffix
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
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
