package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/gcal"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
	"github.com/imos/lacia/internal/web"
)

// serveRun is what the parts of lacia serve share, from reading the settings
// until the server stops.
type serveRun struct {
	c        *serveConfig
	dir      string
	stateDir string
	// state is stateDir, opened once; the parts write in it by names
	// relative to it.
	state *disk.Dir
	// here is the place this server runs at, as the routing names it.
	here   string
	family members
	// peerURL is empty without a peer.
	peerURL, peerLabel string
	// answers is the side of a pair that takes messages from its peer and
	// replies, as the profile that answers on a chat does.
	answers bool
	// kinds are the conversations this server can start. Without any, it
	// takes no requests.
	kinds                []string
	routing              schedule.Routing
	store                *task.Store
	hideCalendarHolidays bool
	server               *web.Server

	// snapshotState is nil when the server shows no calendar.
	snapshotState  *calendar.SnapshotState
	snapshotInput  string
	directCalendar bool
	fetcher        *gcal.Fetcher

	ctx         context.Context
	stop        context.CancelFunc
	restart     *idleRestart
	restartBusy func() bool
	syncing     sync.WaitGroup

	// queue is nil when the server takes no requests.
	queue *request.Queue
	// imageBot and chatLog are nil without Discord.
	imageBot discordImagePreparer
	chatLog  *daylog.Log
}

// openServe reads what the settings name and opens the records. Nothing
// runs in the background yet.
func openServe(c *serveConfig) (*serveRun, error) {
	s := &serveRun{c: c, dir: c.root, stateDir: *c.str("state")}
	if err := s.loadFamily(); err != nil {
		return nil, err
	}
	if err := s.findPeer(); err != nil {
		return nil, err
	}
	if err := s.openRecords(); err != nil {
		return nil, err
	}
	if err := s.loadRouting(); err != nil {
		return nil, err
	}
	s.newWebServer()
	var err error
	if s.state, err = disk.OpenTools(s.stateDir); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *serveRun) loadFamily() error {
	if *s.c.on("discord") {
		var err error
		if s.family, err = loadMembers(*s.c.str("discord-members")); err != nil {
			return err
		}
	}
	if s.family.Mail != nil && *s.c.str("codex") == "" {
		return errors.New("Discordからメールを確認するときは、メール連携を使えるCodexを指定してください")
	}
	return nil
}

func (s *serveRun) findPeer() error {
	peer := *s.c.str("peer")
	if peer == "" {
		return nil
	}
	if s.peerURL = strings.TrimRight(s.c.profiles[peer]["url"], "/"); s.peerURL == "" {
		return fmt.Errorf("相手のプロファイル「%s」の url が、設定ファイルにありません", peer)
	}
	// The peer is called by the label its own section gives it.
	if s.peerLabel = s.c.profiles[peer]["label"]; s.peerLabel == "" {
		s.peerLabel = self.DefaultPeerLabel
	}
	return nil
}

// loadRouting decides where requests and scheduled jobs run. What a profile
// is follows from its settings alone; the name carries no meaning here.
func (s *serveRun) loadRouting() error {
	s.answers = *s.c.on("inbox") && s.peerURL != ""
	s.here = *s.c.str("profile")
	if s.here == "" {
		s.here = "local"
	}
	if *s.c.str("codex") != "" {
		s.kinds = append(s.kinds, caller.Codex)
	}
	if *s.c.str("claude") != "" {
		s.kinds = append(s.kinds, caller.Claude)
	}
	var err error
	s.routing, err = schedule.LoadRouting(*s.c.str("places"), filepath.Dir(*s.c.str("config")), s.here, *s.c.str("default-kind"), s.kinds...)
	return err
}

func (s *serveRun) newWebServer() {
	c := s.c
	// Without pages, only the doors for the peer are open from outside.
	s.server = &web.Server{Store: s.store, Version: version(), Label: *c.str("label"), PeerLabel: s.peerLabel, PeerURL: s.peerURL, People: *c.str("people"), Site: *c.str("site"), DoorOnly: !*c.on("pages"), CalendarPath: *c.str("calendar"),
		HideCalendarHolidays: s.hideCalendarHolidays,
		Place:                s.here, ResolveDestination: s.routing.Resolve, AuthorizeHandover: func(place, token string) bool {
			_, device, err := s.routing.Route(place)
			return err == nil && device != schedule.LocalDevice && s.routing.Authorized(device, token)
		}}
}

func (s *serveRun) openState() error {
	var err error
	// Which conversations ran commands, kept across restarts.
	if s.server.Sessions, err = caller.OpenRegistry(s.state, "sessions.json"); err != nil {
		return err
	}
	handovers, err := s.state.MakeSub("handovers", 0o700)
	if err != nil {
		return err
	}
	if s.server.Handovers, err = handover.Open(handovers); err != nil {
		return err
	}
	if *s.c.on("waits") {
		// What is waited for outside a conversation, such as cluster jobs.
		waits, err := s.state.MakeSub("waits", 0o700)
		if err != nil {
			return err
		}
		if s.server.Waits, err = wait.Open(waits); err != nil {
			return err
		}
	}
	return nil
}

func (s *serveRun) takesRequests() bool { return len(s.kinds) > 0 }

// draining reports whether the server was asked to start no new work.
func (s *serveRun) draining() bool {
	_, err := os.Stat(filepath.Join(s.stateDir, "drain"))
	return err == nil
}
