// braid is the program on each PC. `braid run` keeps your project folders in
// step with their timelines; the other commands set it up and steer it.
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Simplyph0/braid/internal/agent"
	"github.com/Simplyph0/braid/internal/update"
	"golang.org/x/term"
)

const usage = `braid keeps a project the same on all your PCs, as one shared timeline.

  braid setup <hub-url>             connect this PC to your hub (asks for your passphrase)
  braid add <folder> [name]         start keeping a folder in its project's timeline
  braid remove <name>               stop (files and timeline are kept)
  braid status                      what each project is doing
  braid log <name> [count]          the project's moments, newest first
  braid jump <name> <moment>        show an older moment here (your work is kept first)
  braid follow <name>               go back to showing the newest moment
  braid restore <name> <moment>     make an older moment the newest, on every PC
  braid check <name> "<command>"    a command that must pass before a Claude merge is kept ("" to clear)
  braid run                         keep everything in step (what the background service runs)
  braid install                     start braid run at login (Linux, Windows)
  braid uninstall                   stop starting it at login
  braid version                     this build's version (it updates itself from the hub)
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	dir := agent.Dir()
	if d := os.Getenv("BRAID_DIR"); d != "" {
		dir = d
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "setup":
		err = setup(dir, args)
	case "add":
		err = add(dir, args)
	case "remove":
		err = edit(dir, args, 1, func(p *agent.Project, c *agent.Config) {
			for i := range c.Projects {
				if c.Projects[i].Name == p.Name {
					c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
					return
				}
			}
		})
	case "status":
		err = status(dir)
	case "log":
		err = history(dir, args)
	case "jump":
		err = pin(dir, args, 2)
	case "follow":
		err = pin(dir, args, 1)
	case "check":
		err = edit(dir, args, 2, func(p *agent.Project, _ *agent.Config) { p.Check = args[1] })
	case "restore":
		err = restore(dir, args)
	case "run":
		err = run(dir)
	case "version":
		fmt.Println(update.Version)
	case "install":
		err = install()
	case "uninstall":
		err = uninstall()
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal("braid: ", err)
	}
}

func setup(dir string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: braid setup <hub-url>")
	}
	c, err := agent.LoadConfig(dir)
	if err != nil {
		c = &agent.Config{}
	}
	c.Hub = strings.TrimRight(args[0], "/")
	if !strings.Contains(c.Hub, "://") {
		c.Hub = "https://" + c.Hub // a bare address means the usual HTTPS one
	}
	if c.PC == "" {
		c.PC, _ = os.Hostname()
	}
	pass := os.Getenv("BRAID_PASSPHRASE")
	if pass == "" {
		fmt.Print("Passphrase (the same on all your PCs; it encrypts everything): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			b = []byte(strings.TrimSpace(line))
		}
		pass = string(b)
		fmt.Print("Again, to be sure: ")
		b2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil && string(b2) != pass {
			return errors.New("the two didn't match; nothing was changed")
		}
	}
	if len(pass) < 8 {
		return errors.New("use a passphrase of at least 8 characters")
	}
	key, token, err := agent.Connect(c.Hub, pass)
	if err != nil {
		return err
	}
	c.Key, c.Token = hex.EncodeToString(key), token
	if err := c.Save(dir); err != nil {
		return err
	}
	fmt.Printf("This PC (%s) is connected. Next: braid add <folder>\n", c.PC)
	return nil
}

var nameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func add(dir string, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: braid add <folder> [name]")
	}
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	folder, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return fmt.Errorf("%s isn't a folder", folder)
		}
	}
	name := filepath.Base(folder)
	if len(args) == 2 {
		name = args[1]
	}
	name = strings.Trim(nameRe.ReplaceAllString(name, "-"), "-.")
	if name == "" {
		return errors.New("give the project a name: braid add <folder> <name>")
	}
	for _, p := range c.Projects {
		if p.Name == name {
			return fmt.Errorf("there's already a project called %s here (%s)", name, p.Folder)
		}
		if p.Folder == folder {
			return fmt.Errorf("that folder is already project %s", p.Name)
		}
	}
	c.Projects = append(c.Projects, agent.Project{Name: name, Folder: folder})
	if err := c.Save(dir); err != nil {
		return err
	}
	fmt.Printf("Added %s as project %q. If another PC already has it, this folder fills in at the next sync.\n", folder, name)
	return nil
}

// edit changes one project's entry in the config; the agent applies it.
func edit(dir string, args []string, n int, f func(*agent.Project, *agent.Config)) error {
	if len(args) != n {
		fmt.Print(usage)
		os.Exit(2)
	}
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	for i := range c.Projects {
		if c.Projects[i].Name == args[0] {
			f(&c.Projects[i], c)
			if err := c.Save(dir); err != nil {
				return err
			}
			fmt.Println("Done. The agent applies it at its next sync (a few seconds if braid run is running).")
			return nil
		}
	}
	return fmt.Errorf("no project called %s", args[0])
}

func status(dir string) error {
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	fmt.Printf("%s → %s\n\n", c.PC, c.Hub)
	if len(c.Projects) == 0 {
		fmt.Println("No projects yet: braid add <folder>")
	}
	for _, p := range c.Projects {
		st, err := agent.ReadStatus(dir, p.Name)
		fmt.Printf("%s  %s\n", p.Name, p.Folder)
		if err != nil {
			fmt.Println("   not synced yet (is braid run running?)")
			continue
		}
		where := "following main"
		if st.Detached {
			where = "showing an older moment (braid follow " + p.Name + " to come back)"
		}
		fmt.Printf("   %s · at %.12s · last sync %s\n", where, st.At, ago(st.LastSync))
		if st.Last != "" {
			fmt.Printf("   last: %s\n", st.Last)
		}
		if st.Error != "" {
			fmt.Printf("   problem: %s\n", st.Error)
		}
		for _, cf := range st.Conflicts {
			fmt.Printf("   waiting: %s (%s)\n", cf.Path, cf.Why)
		}
	}
	return nil
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return time.Since(t).Round(time.Second).String() + " ago"
}

func history(dir string, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: braid log <name> [count]")
	}
	n := 20
	if len(args) > 1 {
		n, _ = strconv.Atoi(args[1])
	}
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	r, err := c.Repo(c.Store(), args[0])
	if err != nil {
		return err
	}
	tip, err := r.Tip("main")
	if err != nil {
		return err
	}
	ids, err := r.History(tip, n)
	for _, id := range ids {
		m, err := r.Moment(id)
		if err != nil {
			return err
		}
		intent := m.Intent
		if len(m.Parents) > 1 && intent == "" {
			intent = "braid"
		}
		fmt.Printf("%.12s  %s  %-12s %s\n", id, m.Time.Local().Format("Jan 02 15:04:05"), m.PC, intent)
	}
	return err
}

// pin sets, on the hub, which moment this PC shows (none: follow main).
func pin(dir string, args []string, n int) error {
	if len(args) != n {
		fmt.Print(usage)
		os.Exit(2)
	}
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	r, err := c.Repo(c.Store(), args[0])
	if err != nil {
		return err
	}
	id := ""
	if n == 2 {
		if id, err = (&agent.Agent{}).Resolve(r, args[1]); err != nil {
			return err
		}
	}
	old, err := r.Tip(agent.PinStrand(c.PC))
	if err != nil {
		return err
	}
	if old == id {
		fmt.Println("Already so.")
		return nil
	}
	if err := r.SetTip(agent.PinStrand(c.PC), old, id); err != nil {
		return err
	}
	fmt.Println("Done. The agent applies it at its next sync (a few seconds if braid run is running).")
	return nil
}

func restore(dir string, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: braid restore <name> <moment>")
	}
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return err
	}
	r, err := c.Repo(c.Store(), args[0])
	if err != nil {
		return err
	}
	a := &agent.Agent{}
	id, err := a.Resolve(r, args[1])
	if err != nil {
		return err
	}
	nid, err := r.Restore(id, c.PC)
	if err != nil {
		return err
	}
	fmt.Printf("Moment %.12s is now the newest (%.12s). Every PC following main switches to it.\n", id, nid)
	return nil
}

func run(dir string) error {
	if slices.Contains(os.Args, "--background") {
		detachConsole() // started at sign-in on Windows: no console window
	}
	// One braid run per setup: two would write the same folders and state.
	h := fnv.New32a()
	h.Write([]byte(dir))
	lock, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 47000+h.Sum32()%1000))
	if err != nil {
		return errors.New("braid is already running on this PC (see braid status)")
	}
	defer lock.Close()
	if _, err := agent.LoadConfig(dir); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "braid.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	lg := log.New(io.MultiWriter(os.Stderr, f), "", log.LstdFlags)
	c, _ := agent.LoadConfig(dir)
	lg.Printf("braid running on %s, hub %s", c.PC, c.Hub)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go selfUpdate(ctx, dir, lg)
	(&agent.Agent{Dir: dir, Store: c.Store(), Log: lg}).Run(ctx, 5*time.Second)
	return nil
}

// selfUpdate installs newer signed builds from the hub: at start, then hourly.
func selfUpdate(ctx context.Context, dir string, lg *log.Logger) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return
	}
	for {
		if c, err := agent.LoadConfig(dir); err == nil && !c.NoUpdate {
			v, err := update.Install(c.Hub, exe)
			switch {
			case err != nil:
				lg.Printf("update: %v", err)
			case v != "":
				lg.Printf("updated %s → %s; restarting", update.Version, v)
				if err := update.Restart(exe); err != nil {
					lg.Printf("update: restart: %v (the new version starts next time)", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func install() error {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("reg", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "Braid", "/t", "REG_SZ",
			"/d", `"`+exe+`" run --background`, "/f").CombinedOutput()
		if err != nil {
			return fmt.Errorf("reg: %v: %s", err, out)
		}
		if err := exec.Command(exe, "run", "--background").Start(); err != nil {
			return err
		}
		fmt.Println("Braid now starts when you sign in to Windows, and is running. Its log: " + filepath.Join(agent.Dir(), "braid.log"))
		return nil
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		unit := filepath.Join(home, ".config", "systemd", "user", "braid.service")
		os.MkdirAll(filepath.Dir(unit), 0o755)
		body := "[Unit]\nDescription=Braid: keep project folders in step\n\n[Service]\nExecStart=" + exe + " run\nRestart=on-failure\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n"
		if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
			return err
		}
		out, err := exec.Command("systemctl", "--user", "enable", "--now", "braid.service").CombinedOutput()
		if err == nil {
			fmt.Println("Braid now starts when you log in, and is running. Its log: journalctl --user -u braid")
			return nil
		}
		fmt.Printf("systemd didn't take it (%s); using your desktop's startup apps instead.\n", strings.TrimSpace(string(out)))
		os.Remove(unit)
	}
	// No systemd user session: the desktop's autostart (freedesktop.org standard).
	auto := filepath.Join(home, ".config", "autostart", "braid.desktop")
	os.MkdirAll(filepath.Dir(auto), 0o755)
	body := "[Desktop Entry]\nType=Application\nName=Braid\nComment=Keep project folders in step\nExec=\"" + exe + "\" run\nTerminal=false\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(auto, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Println("Braid starts when you next log in to your desktop. To start it now: braid run &")
	fmt.Println("(No desktop? Start `braid run` from your init system or a crontab @reboot line.)")
	return nil
}

func uninstall() error {
	if runtime.GOOS == "windows" {
		exec.Command("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "Braid", "/f").Run()
		fmt.Println("Braid no longer starts when you sign in. (A copy running now keeps going until you sign out.)")
		return nil
	}
	home, _ := os.UserHomeDir()
	if _, err := exec.LookPath("systemctl"); err == nil {
		exec.Command("systemctl", "--user", "disable", "--now", "braid.service").Run()
	}
	os.Remove(filepath.Join(home, ".config", "systemd", "user", "braid.service"))
	os.Remove(filepath.Join(home, ".config", "autostart", "braid.desktop"))
	fmt.Println("Braid no longer starts at login. Your folders, timelines and settings are untouched.")
	return nil
}
