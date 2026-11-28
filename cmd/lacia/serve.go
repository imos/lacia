package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/home"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
)

func runServe(args []string) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	c, err := loadServeConfig(args)
	if err != nil {
		return err
	}
	if tools, err := disk.OpenTools(home.Dir(os.Getenv)); err == nil {
		if err := rotateServeLog(tools, maxServeLog, os.Stdout, os.Stderr); err != nil {
			slog.Warn("log: could not set the log aside", "error", err)
		}
		tools.Close()
	}
	if c.stale {
		slog.Warn("config: read from the old place", "detail", oldPlacesText(home.Moves(os.Getenv)), "file", *c.str("config"))
	}
	s, err := openServe(c)
	if err != nil {
		return err
	}
	s.openCalendarSnapshot()
	if err := s.openState(); err != nil {
		return err
	}
	handler, err := s.server.Handler()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s.ctx, s.stop = ctx, stop
	restart := &idleRestart{last: time.Now()}
	restart.state = s.state
	s.restart = restart
	s.server.RestartWhenIdle = func() error { return requestIdleRestart(s.state) }
	s.restartBusy = func() bool { return fileExists(filepath.Join(s.stateDir, "drain")) }
	if s.takesRequests() {
		requests, err := s.state.MakeSub("requests", 0o700)
		if err != nil {
			return err
		}
		if s.queue, err = request.Open(requests); err != nil {
			return err
		}
		agents, err := s.startAgents()
		if err != nil {
			return err
		}
		if err := s.startChat(); err != nil {
			return err
		}
		if err := s.startSelf(); err != nil {
			return err
		}
		defer agents.close()
		if err := s.startSchedules(agents); err != nil {
			return err
		}
	}
	if err := s.startCalendarFetcher(); err != nil {
		return err
	}
	s.followCalendarSnapshot()
	s.server.Machine = machineWatch(c)
	s.startGitSync()
	go s.followRecords()
	return s.serve(handler)
}

// serve listens until the server is told to stop, and waits for the last
// save.
func (s *serveRun) serve(handler http.Handler) error {
	addr, restart := *s.c.str("addr"), s.restart
	srv := &http.Server{Addr: addr, Handler: restart.handler(handler), ReadHeaderTimeout: 10 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-s.ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if s.takesRequests() {
		// Commands run in this repository find the server through this
		// note, so they need no environment variable to reach it.
		removeNote := announceServer(s.store.Records(), listener.Addr())
		defer func() {
			if !restart.stopping.Load() {
				removeNote()
			}
		}()
	}
	go restart.run(s.ctx, s.stop, s.restartBusy)
	slog.Info("serving", "detail", "Web サーバーを起動しました。", "addr", addr, "root", s.dir, "tasks", len(s.store.List()), "version", s.server.Version)
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	s.syncing.Wait() // let the last save finish
	return nil
}

// Where the overrides of how events are shown live when the settings do not say.
const calendarOverridesDefault = ".lacia/calendar-overrides.json"

// ownRulesNote follows the instructions in a repository that keeps its rules
// in AGENTS.md. The instructions leave to the rules what the rules of the
// binary say about these matters, and such a repository may say nothing of
// them.
const ownRulesNote = "この会話では、記録の `AGENTS.md` を決まりの本文として使います。「目的に合わせる」「わたしのこと」「ブラウザーで公開ページを読む」について、そこに決まりがなければ、`lacia rules` の該当する節を読んでください。別のプロファイルとのやり取りについても、決まりがなければ `lacia rules peers` を読んでください。内容が食い違うときは `AGENTS.md` に従ってください。\n"

// windowInstructions fits the instructions of a conversation the server
// starts to the repository it works in. The text marks where the owner's
// name goes (rules.OwnerMark); the name comes from the environment or the
// repository's settings, and is rules.DefaultOwner when neither says. The
// lines for a repository with tasks and for one without are chosen by its
// settings (rules.Fit). A repository that leaves its rules to the binary also
// gets the rules themselves, so the conversation need not fetch them first;
// one that keeps its own is told where the rest is (ownRulesNote). Every
// repository also receives the shared naming and recipient policy.
func windowInstructions(text, root string) string {
	return resolveWindowInstructions(text, root, sessionRules(context.Background(), root, ""))
}

// serverNote is where a server that takes requests leaves its address, for
// the commands run in the same repository. It lives under .git so that it
// is never committed.
const serverNote = ".git/lacia-server"

// announceServer writes the note and returns what removes it again. A
// repository without .git gets none.
func announceServer(records *disk.Dir, addr net.Addr) func() {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return func() {}
	}
	host := tcp.IP.String()
	if tcp.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	name := filepath.FromSlash(serverNote)
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(tcp.Port))
	if err := records.WriteFileNoSync(name, []byte(url+"\n"), 0o644); err != nil {
		return func() {}
	}
	return func() {
		// Leave a note that a newer server has already replaced.
		if data, err := records.ReadFile(name); err == nil && strings.TrimSpace(string(data)) == url {
			records.Remove(name)
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// exchangesSince returns what people said to Lacia after since and what she
// answered, oldest first. Notices from the system are left out.
func exchangesSince(queue *request.Queue, since time.Time) []self.Exchange {
	var out []self.Exchange
	recent := queue.Recent(0, "", false)
	for i := len(recent) - 1; i >= 0; i-- {
		r := recent[i]
		if r.Created.After(since) && r.Status == request.Done && strings.TrimSpace(r.Reply) != "" {
			out = append(out, self.Exchange{ID: r.ID, At: r.Created, From: r.From, Source: r.Source, Task: r.TaskID, Text: r.Text, Reply: r.Reply})
		}
	}
	return out
}

// version reports the commit the binary was built from.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "（未コミットの変更あり）"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev + dirty
}

// guardCalendar keeps the last saved events of a calendar when a new
// snapshot lost most of them. See calendar.Guard.
func guardCalendar(records *disk.Dir, path string) {
	rel, err := filepath.Rel(records.Path(), path)
	if path == "" || err != nil || !filepath.IsLocal(rel) {
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return
	}
	previous, err := program.Git().Output(context.Background(), records.Path(), "show", "HEAD:"+filepath.ToSlash(rel))
	if err != nil {
		return
	}
	fixed, changed, err := calendar.Guard(previous, current)
	if err != nil {
		slog.Warn("calendar guard", "detail", "保存してあった予定と新しい一覧を照合できませんでした。", "error", err)
		return
	}
	if changed {
		slog.Warn("calendar guard kept the previous events", "detail", "今回の一覧に以前の予定を残して保存します。")
		if err := records.WriteFileNoSync(rel, fixed, 0o644); err != nil {
			slog.Error("calendar guard", "detail", "以前の予定を残した一覧を保存できませんでした。", "error", err)
		}
	}
}

// routeRunner keeps the queue's main and side ordering but chooses the
// conversation kind pinned to the request, when one was specified.
func routeRunner(runners map[string]request.Runner, defaultKind string) request.Runner {
	return func(ctx context.Context, text string) (string, string, error) {
		kind := request.Current(ctx).Agent
		if kind == "" {
			kind = defaultKind
		}
		run := runners[kind]
		if run == nil {
			return "", "", fmt.Errorf("宛先の会話 %q を起動する設定がありません", kind)
		}
		return run(ctx, text)
	}
}

func scheduleServerURL(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
