package ui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/Simplyph0/braid/internal/agent"
)

// Overall sums up every project for the tray: "ok", "busy", "warn", "bad"
// or "idle", and a sentence.
func Overall(dir string) (string, string) {
	c, err := agent.LoadConfig(dir)
	if err != nil {
		return "idle", "Not set up yet: click to connect this PC"
	}
	if len(c.Projects) == 0 {
		return "idle", "No folders yet"
	}
	bad, warn, busy := 0, 0, 0
	for _, p := range c.Projects {
		if p.Paused {
			continue
		}
		st, err := agent.ReadStatus(dir, p.Name)
		switch {
		case err != nil || st.LastSync.IsZero():
			busy++
		case st.Error != "":
			bad++
		case len(st.Conflicts) > 0:
			warn++
		}
	}
	switch {
	case bad > 0:
		return "bad", fmt.Sprintf("%d project(s) need a look", bad)
	case warn > 0:
		return "warn", fmt.Sprintf("%d project(s) waiting to be joined", warn)
	case busy > 0:
		return "busy", "Starting…"
	}
	return "ok", "Everything is up to date"
}

var colors = map[string]color.RGBA{
	"ok": {47, 138, 76, 255}, "busy": {58, 91, 217, 255}, "warn": {178, 106, 0, 255},
	"bad": {192, 57, 43, 255}, "idle": {140, 136, 129, 255},
}

// icon draws the tray icon: a ring in the state's colour with a dot inside.
func icon(state string) []byte {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	c := colors[state]
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			d := dx*dx + dy*dy
			if (d <= 15*15 && d >= 10.5*10.5) || d <= 6*6 {
				img.Set(x, y, c)
			}
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	if runtime.GOOS != "windows" {
		return b.Bytes()
	}
	// Windows wants .ico: a header and one entry pointing at the PNG.
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1})
	ico.Write([]byte{n, n, 0, 0})
	binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&ico, binary.LittleEndian, []uint32{uint32(b.Len()), 22})
	ico.Write(b.Bytes())
	return ico.Bytes()
}

// HaveTray reports whether this session can show a tray icon.
func HaveTray() bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	_, err := os.Stat(fmt.Sprintf("/run/user/%d/bus", os.Getuid()))
	return err == nil
}

// RunTray shows the tray icon until quit is closed or Quit is chosen. It
// must run on the main goroutine. open shows the window; onQuit stops braid.
func RunTray(dir string, open func(), onQuit func(), quit <-chan struct{}) {
	systray.Run(func() {
		systray.SetTitle("Braid")
		state, text := Overall(dir)
		systray.SetIcon(icon(state))
		systray.SetTooltip("Braid: " + text)
		show := systray.AddMenuItem("Open Braid", "Show your projects")
		line := systray.AddMenuItem(text, "")
		line.Disable()
		systray.AddSeparator()
		q := systray.AddMenuItem("Quit Braid", "Stop syncing until Braid is started again")
		go func() {
			t := time.NewTicker(3 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-show.ClickedCh:
					open()
				case <-q.ClickedCh:
					onQuit()
					systray.Quit()
					return
				case <-quit:
					systray.Quit()
					return
				case <-t.C:
					if s, txt := Overall(dir); s != state || txt != text {
						state, text = s, txt
						systray.SetIcon(icon(state))
						systray.SetTooltip("Braid: " + text)
						line.SetTitle(text)
					}
				}
			}
		}()
	}, func() {})
}

// AppIcon is Braid's icon as a PNG, for app menus.
func AppIcon() []byte {
	b := icon("busy")
	if runtime.GOOS == "windows" {
		return b[22:] // the PNG inside the .ico
	}
	return b
}
