package timeline

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Never recorded: things a project recreates on its own. .git is recorded
// (that's the point); a project's .braidignore can add or un-skip ("!dist/").
var defaultIgnores = []string{
	"node_modules/", ".venv/", "venv/", "__pycache__/", "build/", "dist/", "target/",
	".pio/", ".gradle/", ".next/", "bin/Debug/", "bin/Release/", "obj/",
	".DS_Store", "Thumbs.db", "desktop.ini", "*.swp", "~$*", "*" + tmpSuffix,
}

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	base    string
}

// Matcher applies gitignore-style rules: the built-in skips, then the
// project's root .braidignore.
type Matcher struct{ rules []rule }

func NewMatcher(root string) *Matcher {
	m := &Matcher{}
	for _, p := range defaultIgnores {
		m.add(p, "")
	}
	if f, err := os.Open(filepath.Join(root, ".braidignore")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			m.add(sc.Text(), "")
		}
		f.Close()
	}
	return m
}

func (m *Matcher) add(line, base string) {
	line = strings.TrimRight(line, " \r")
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	r := rule{base: base}
	if strings.HasPrefix(line, "!") {
		r.negate, line = true, line[1:]
	}
	line = strings.TrimPrefix(line, `\`)
	if strings.HasSuffix(line, "/") {
		r.dirOnly, line = true, strings.TrimSuffix(line, "/")
	}
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	re := globToRegexp(line)
	if anchored {
		re = "^" + re + "$"
	} else {
		re = "^(?:.*/)?" + re + "$"
	}
	compiled, err := regexp.Compile(re)
	if err != nil {
		return
	}
	r.re = compiled
	m.rules = append(m.rules, r)
}

func globToRegexp(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "/**"):
			b.WriteString("(?:/.*)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			j := strings.IndexByte(g[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + class + "]")
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// Ignored reports whether rel (slash path from the project root) is skipped.
// The last matching rule wins, as in git.
func (m *Matcher) Ignored(rel string, dir bool) bool {
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !dir {
			continue
		}
		p := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			p = strings.TrimPrefix(rel, r.base+"/")
		}
		if r.re.MatchString(p) {
			ignored = !r.negate
		}
	}
	return ignored
}

// windowsBadName reports names Windows can't create.
