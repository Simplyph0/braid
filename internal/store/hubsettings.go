package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// HubSettings are the hub's own settings, changed from the web app and kept
// in its data folder. They apply at once, with no restart.
type HubSettings struct {
	OfflineSeconds int  `json:"offlineSeconds"` // call a PC offline after this long without a beat (5 to 86400)
	CheckMinutes   int  `json:"checkMinutes"`   // how often to look for new PC builds (1 to 1440)
	AutoFetch      bool `json:"autoFetch"`      // fetch new signed builds from GitHub
}

// Valid says what's wrong with s, if anything.
func (s HubSettings) Valid() error {
	switch {
	case s.OfflineSeconds < 5 || s.OfflineSeconds > 86400:
		return errors.New("offline time: 5 seconds to 1 day")
	case s.CheckMinutes < 1 || s.CheckMinutes > 1440:
		return errors.New("update check: 1 minute to 1 day")
	}
	return nil
}

func (d *Dir) settingsFile() string { return filepath.Join(d.Root, "settings.json") }

// Settings returns the saved settings, or def where none were saved.
func (d *Dir) Settings(def HubSettings) HubSettings {
	var s HubSettings
	if b, err := os.ReadFile(d.settingsFile()); err == nil && json.Unmarshal(b, &s) == nil && s.Valid() == nil {
		return s
	}
	return def
}

// SaveSettings stores s after checking it.
func (d *Dir) SaveSettings(s HubSettings) error {
	if err := s.Valid(); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return writeAtomic(d.settingsFile(), b)
}

// Every is how often to look for new builds.
func (s HubSettings) Every() time.Duration { return time.Duration(s.CheckMinutes) * time.Minute }
