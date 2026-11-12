package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Simplyph0/braid/internal/agent"
	"github.com/Simplyph0/braid/internal/claude"
)

// chatsStatus says what the last chat sync did, and whether Claude's desktop
// app is blocking some chats from being loaded.
func (s *Server) chatsStatus(w http.ResponseWriter, _ *http.Request) {
	c, err := agent.LoadConfig(s.Dir)
	if err != nil {
		reply(w, nil, err)
		return
	}
	st, _ := agent.ReadChatsStatus(s.Dir)
	st.AppRunning = claude.DesktopRunning() // as of now, not as of the last sync
	reply(w, map[string]any{"status": st, "enabled": !c.NoChats}, nil)
}

// chatsNow syncs chats right away.
func (s *Server) chatsNow(w http.ResponseWriter, r *http.Request) {
	if s.Agent == nil {
		reply(w, nil, errors.New("Braid isn't running its syncs"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	_, err := s.Agent.SyncChats(ctx)
	reply(w, nil, err)
}

// closeClaude closes the Claude desktop app, loads the chats that were
// waiting for it, and opens it again. Chats running in Claude stop.
func (s *Server) closeClaude(w http.ResponseWriter, r *http.Request) {
	if s.Agent == nil {
		reply(w, nil, errors.New("Braid isn't running its syncs"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	apps, _ := claude.RunningApps()
	if err := claude.Quit(ctx, apps, func(string, ...any) {}); err != nil {
		reply(w, nil, err)
		return
	}
	_, err := s.Agent.SyncChats(ctx)
	claude.ClearStaleLocks(claude.Detect("", "").DesktopDir) // a stopped Claude can leave its lock behind
	if rerr := claude.Relaunch(apps); rerr != nil && err == nil {
		err = errors.New("the chats were loaded, but Claude didn't open again: " + rerr.Error())
	}
	reply(w, nil, err)
}
