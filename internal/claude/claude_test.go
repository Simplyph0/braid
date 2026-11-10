package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Simplyph0/braid/internal/crypt"
	"github.com/Simplyph0/braid/internal/store"
	"github.com/Simplyph0/braid/internal/timeline"
)

// fakeClaude writes a stand-in for the claude command that checks it was
// started with no tools, saves the prompt, and answers with reply.
func fakeClaude(t *testing.T, reply string) (bin, promptFile string) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	d := t.TempDir()
	promptFile = filepath.Join(d, "prompt")
	bin = filepath.Join(d, "claude")
	script := "#!/bin/sh\n" +
		"case \"$*\" in *'--tools  --json-schema'*) ;; *) echo '{\"is_error\":true,\"result\":\"tools were not disabled\"}'; exit 0;; esac\n" +
		"cat > " + promptFile + "\n" +
		"cat <<'EOF'\n" + reply + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, promptFile
}

func TestMergeReadsStructuredOutput(t *testing.T) {
	bin, pf := fakeClaude(t, `{"type":"result","is_error":false,"result":"","structured_output":{"merged":"a\nb\n","summary":"shuffle (Laptop) with seek fix (Desktop)"}}`)
	m := &Merger{Bin: bin}
	out, sum, err := m.Merge(context.Background(), timeline.MergeInput{Path: "player.js", Base: []byte("x\n"), Ours: []byte("a\n"), Theirs: []byte("b\n"),
		OursPC: "Desktop", TheirsPC: "Laptop", IntentOurs: "fix seek bar", IntentTheirs: "add shuffle"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a\nb\n" || !strings.Contains(sum, "shuffle") {
		t.Errorf("got %q / %q", out, sum)
	}
	p, _ := os.ReadFile(pf)
	for _, want := range []string{"player.js", "fix seek bar", "add shuffle", "===== BASE START =====\nx\n"} {
		if !strings.Contains(string(p), want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

func TestMergeErrorsAreReported(t *testing.T) {
	bin, _ := fakeClaude(t, `{"type":"result","is_error":true,"result":"Not logged in · Please run /login"}`)
	_, _, err := (&Merger{Bin: bin}).Merge(context.Background(), timeline.MergeInput{Ours: []byte("a"), Theirs: []byte("b")})
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("got %v", err)
	}
	if _, _, err := (&Merger{Bin: bin}).Merge(context.Background(), timeline.MergeInput{Ours: make([]byte, maxInput+1)}); err == nil {
		t.Fatal("a huge file was sent")
	}
}

// End to end: two PCs change the same line; Claude's merge must pass the
// project's check, and gets one retry with the check's output.
func TestBraidWithClaudeAndCheck(t *testing.T) {
	bin, pf := fakeClaude(t, `{"is_error":false,"structured_output":{"merged":"value = 3\n","summary":"kept both"}}`)
	hub := &store.Dir{Root: t.TempDir()}
	r, _ := timeline.NewRepo(hub, "p", crypt.NewKey())
	mk := func(name string) *timeline.Workspace {
		f := filepath.Join(t.TempDir(), name)
		os.MkdirAll(f, 0o755)
		w, err := timeline.Open(r, name, f, filepath.Join(t.TempDir(), "st.json"))
		if err != nil {
			t.Fatal(err)
		}
		w.Settle = 0
		return w
	}
	a, b := mk("Desktop"), mk("Laptop")
	write := func(w *timeline.Workspace, s string) {
		os.WriteFile(filepath.Join(w.Folder, "conf.txt"), []byte(s), 0o644)
	}
	ctx := context.Background()
	write(a, "value = 1\n")
	a.Sync(ctx)
	b.Sync(ctx)
	write(a, "value = 2\n")
	a.Sync(ctx)
	time.Sleep(10 * time.Millisecond)
	write(b, "value = 4\n")
	b.Merger = &Merger{Bin: bin}
	calls := 0
	b.Check = func(_ context.Context, dir string) error {
		calls++
		d, _ := os.ReadFile(filepath.Join(dir, "conf.txt"))
		if calls == 1 {
			return &checkErr{"value must be odd, got " + strings.TrimSpace(string(d))}
		}
		return nil
	}
	rep, err := b.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Braided == "" || calls != 2 {
		t.Fatalf("not braided after a retry: %+v, %d checks", rep, calls)
	}
	if p, _ := os.ReadFile(pf); !strings.Contains(string(p), "value must be odd") {
		t.Error("the retry didn't tell Claude what failed")
	}
	if d, _ := os.ReadFile(filepath.Join(b.Folder, "conf.txt")); string(d) != "value = 3\n" {
		t.Errorf("folder has %q", d)
	}
	m, _ := r.Moment(rep.Braided)
	if !strings.Contains(m.Intent, "kept both") {
		t.Errorf("braid intent: %q", m.Intent)
	}
	// A check that never passes: the work waits on its strand instead.
	a.Sync(ctx) // the desktop takes the braid first
	write(a, "value = 5\n")
	a.Sync(ctx)
	write(b, "value = 7\n")
	b.Check = func(context.Context, string) error { return &checkErr{"broken"} }
	rep, _ = b.Sync(ctx)
	if len(rep.Conflicts) != 1 || !strings.Contains(rep.Conflicts[0].Why, "check") {
		t.Fatalf("expected a check conflict: %+v", rep)
	}
}

type checkErr struct{ s string }

func (e *checkErr) Error() string { return e.s }

func TestIntentFromTranscripts(t *testing.T) {
	home := t.TempDir()
	folder := filepath.Join(t.TempDir(), "PixelBit")
	d := filepath.Join(home, "projects", "-x-PixelBit")
	os.MkdirAll(d, 0o755)
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	line := func(ts time.Time, cwd, content string) string {
		return `{"type":"user","cwd":"` + cwd + `","timestamp":"` + ts.Format(time.RFC3339) + `","message":{"role":"user","content":` + content + `}}`
	}
	lines := []string{
		line(t0.Add(-time.Hour), folder, `"too old"`),
		line(t0.Add(time.Minute), folder, `"add a shuffle button\nmore detail"`),
		line(t0.Add(2*time.Minute), folder+"/src", `[{"type":"text","text":"fix the seek bar"}]`),
		line(t0.Add(3*time.Minute), folder, `[{"type":"tool_result","content":"ok"}]`),
		line(t0.Add(4*time.Minute), folder, `"<command-name>/clear</command-name>"`),
		line(t0.Add(5*time.Minute), "/elsewhere", `"other project"`),
		`not json`,
	}
	os.WriteFile(filepath.Join(d, "s.jsonl"), []byte(strings.Join(lines, "\n")), 0o644)
	got := Intent(home, folder, t0, t0.Add(time.Hour))
	if got != "add a shuffle button · fix the seek bar" {
		t.Errorf("got %q", got)
	}
	if got := Intent(home, folder, t0.Add(time.Hour), t0.Add(2*time.Hour)); got != "" {
		t.Errorf("nothing in that window, got %q", got)
	}
}
