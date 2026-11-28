package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/agent"
	"github.com/imos/lacia/internal/effect/network"
	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/notify"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// localSite is the server in the local environment, used when the config names no other.
const localSite = "http://127.0.0.1:10148"

// agentConfig is the part of the notifier's config.json that the agent
// itself reads. The planning options are parsed by the notify package.
type agentConfig struct {
	Transport struct {
		// URL names a server reached directly, such as one in the local environment
		// (http://127.0.0.1:10148). When set, no port-forward is kept and
		// the fields below are not used.
		URL        string `json:"url"`
		Kubectl    string `json:"kubectl"`
		Context    string `json:"context"`
		Namespace  string `json:"namespace"`
		Port       int    `json:"port"`
		Target     string `json:"target"`      // e.g. service/lacia
		RemotePort int    `json:"remote_port"` // the port of the target
	} `json:"transport"`
	Site      string               `json:"site"`       // base URL opened from a notification; default transport.url, then localSite
	TasksRepo string               `json:"tasks_repo"` // read-only copy to keep up to date
	Place     string               `json:"place"`      // this agent's place for handovers; default local
	Schedule  *agentScheduleConfig `json:"schedule,omitempty"`
}

// runAgent talks to lacia serve locally or on another server. It starts a
// port-forward only when configured, reads tasks and the calendar, and writes the
// notifications to show into manifest.json, which the menu-bar app reads.
// It is started by that app and stops when the app goes away.
func runAgent(args []string) error {
	fs := commandFlags("agent")
	configPath := fs.String("config", "", "設定ファイル（config.json）。接続先を省くと、手元で動いている lacia serve へつなぎます")
	stateDir := fs.String("state", "", "端末の状態を記録するディレクトリー")
	parentPID := fs.Int("parent-pid", 0, "この番号のプロセスが親でなくなったら、lacia agent を終了します")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" || *stateDir == "" {
		return errors.New("--config と --state を指定してください")
	}
	logFile, err := openLog(filepath.Join(*stateDir, "worker.log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	slog.SetDefault(slog.New(slog.NewTextHandler(logFile, nil)))

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	var cfg agentConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("設定ファイルを読めませんでした: %w", err)
	}
	t := cfg.Transport
	if (t.Kubectl != "" || t.Context != "") && (t.Target == "" || t.RemotePort == 0) {
		return errors.New("設定の transport に、つなぐ先（target）とそのポート（remote_port）を書いてください")
	}
	direct := strings.TrimRight(t.URL, "/")
	if direct == "" && t.Kubectl == "" && t.Context == "" {
		// No transport is configured: the server in the local environment.
		direct = localSite
	}
	if cfg.Site == "" && direct != "" {
		cfg.Site = direct
	}
	if cfg.Site == "" {
		cfg.Site = localSite
	}
	if cfg.Place == "" {
		cfg.Place = "local"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	forward := &agent.Forward{Kubectl: t.Kubectl, Context: t.Context, Namespace: t.Namespace, Target: t.Target, LocalPort: t.Port, RemotePort: t.RemotePort}
	manifestPath := filepath.Join(*stateDir, "manifest.json")
	statePath := filepath.Join(*stateDir, "agent-state.json")
	state, err := notify.LoadState(statePath)
	if err != nil {
		slog.Error("state", "detail", "通知の状態を読み取れませんでした。", "error", err)
		return err
	}
	mirror := mirrorPoll{dir: cfg.TasksRepo, site: cfg.Site, statePath: statePath, state: state}
	writeOffline := func() {
		now := time.Now()
		configData, _ := os.ReadFile(*configPath)
		config, _ := notify.ParseConfig(configData)
		mirror.fail(now)
		manifest := notify.Manifest{Version: notify.Version, Health: notify.Health{
			CheckedAt: float64(now.Unix()), Tasks: notify.HealthDisabled,
			Calendar: notify.HealthDisabled, Transport: notify.HealthFailed,
		}}
		mirror.display(&manifest, now, config.Enabled)
		if err := notify.WriteManifest(manifestPath, manifest); err != nil {
			slog.Error("write manifest", "detail", "通知アプリに渡す通知の一覧を保存できませんでした。", "error", err)
		}
	}
	var forwardReady atomic.Bool
	base := forward.URL
	alive := func() bool { return forwardReady.Load() && forward.Alive() }
	if direct != "" {
		// The server is reached as it is; whether it answers shows in
		// each poll.
		base, alive = func() string { return direct }, func() bool { return true }
		slog.Info("agent started", "url", direct, "version", version())
	} else {
		defer forward.Stop()
		slog.Info("agent started", "target", t.Target, "port", t.Port, "version", version())
	}

	client := network.Server(15 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) ([]byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base()+path, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("%s が %d を返しました", path, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return data, resp.Header.Get(task.RevisionHeader), err
	}

	// Waits: look at the cluster jobs conversations registered, and start
	// the conversation that continues when one ends.
	watcher := &agent.Watcher{Kubectl: t.Kubectl, Base: base, Client: client, Dir: cfg.TasksRepo, LogDir: filepath.Join(*stateDir, "waits"), Runners: map[string][]string{}, Place: cfg.Place}
	watcher.Host, _ = os.Hostname()
	// The logins of this machine (the same list `lacia codex --login`
	// reads), the default one first. Without any, Codex as it is installed.
	list, err := login.LoadList(login.ListPath(os.Getenv))
	if err != nil {
		return err
	}
	for _, name := range list.Names() {
		if entry := list.Logins[name]; len(entry.Codex) > 0 {
			watcher.Logins = append(watcher.Logins, agent.Login{Name: name, Label: entry.Label, Command: entry.Codex, Home: entry.CodexHome, Unattended: entry.Unattended})
		}
	}
	if len(watcher.Logins) == 0 {
		watcher.Logins = agent.DefaultLogins()
	}
	if _, command, err := list.DefaultCommand("claude"); err == nil {
		watcher.Runners["claude"] = append(append([]string{}, command...), "-p")
	}
	if self, err := os.Executable(); err == nil {
		watcher.Path = append(watcher.Path, filepath.Dir(self))
	}
	if home, err := os.UserHomeDir(); err == nil {
		watcher.Path = append(watcher.Path, filepath.Join(home, ".local", "bin"))
	}
	watcher.Path = append(watcher.Path, list.CommandDirs()...)
	if t.Kubectl != "" {
		watcher.Path = append(watcher.Path, filepath.Dir(t.Kubectl))
	}
	watcher.Path = append(watcher.Path, "/opt/homebrew/bin", "/usr/local/bin")
	// The launchers of Codex are scripts that look for their own tools; a
	// menu-bar app starts this process with a short PATH.
	os.Setenv("PATH", strings.Join(append(append([]string{}, watcher.Path...), os.Getenv("PATH")), string(os.PathListSeparator)))
	defer watcher.Stop()
	if cfg.Schedule != nil {
		conversations := scheduleConversations{root: cfg.TasksRepo, state: filepath.Join(*stateDir, "schedule-conversations"), base: base(), place: cfg.Place, logins: list}
		worker := &schedule.Worker{
			Config: schedule.WorkerConfig{URL: base(), Device: cfg.Schedule.Device, Root: cfg.TasksRepo,
				SecretFile: resolvePath(filepath.Dir(*configPath), cfg.Schedule.SecretFile), AuthCheck: cfg.Schedule.AuthCheck},
			State: filepath.Join(*stateDir, "schedule-worker.json"), Client: client, Available: alive, RunConversation: conversations.run, SelectProfile: conversations.selection,
		}
		if err := worker.Validate(); err != nil {
			slog.Error("scheduled work misconfigured", "detail", "この端末で予約を実行できません。通知アプリの予約の設定を確かめてください。", "error", err)
		} else {
			watcher.HandoverSecret = worker.Credential
			done := make(chan struct{})
			go func() {
				defer close(done)
				if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
					slog.Error("scheduled work stopped", "detail", "この端末で予約を受け取る処理が止まりました。", "error", err)
				}
			}()
			defer func() { stop(); <-done }()
		}
	}

	var lastSnapshot string
	var lastHealth notify.Health
	seen := map[string]bool{}
	for {
		if *parentPID > 1 && os.Getppid() != *parentPID {
			slog.Info("parent is gone; stopping", "detail", "起動元のアプリが終了したため、通知を用意する処理を止めます。")
			return nil
		}
		if direct == "" && !alive() {
			forwardReady.Store(false)
			if err := forward.Start(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Error("port-forward", "detail", "サーバーへのポート転送を開始できませんでした。", "error", err)
				writeOffline()
				// Keep the enrollment alive so the authentication probe can
				// record what happened even before the API is reachable.
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(20 * time.Second):
					continue
				}
			}
			forwardReady.Store(true)
		}
		raw, err := os.ReadFile(*configPath)
		config := notify.DefaultConfig()
		if err == nil {
			config, err = notify.ParseConfig(raw)
		}
		if err != nil {
			slog.Error("config", "detail", "通知アプリの設定を読み取れませんでした。", "error", err)
		}
		if repoConfig, err := task.LoadConfig(cfg.TasksRepo); err == nil {
			config.Location = repoConfig.Location()
		}
		checks := notify.MeetingRechecks(state, time.Now())
		if config.Enabled && config.MeetingNotifications && config.MeetingSource == "linked" && len(checks) > 0 {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, base()+"/api/calendar/refresh", nil)
			if err == nil {
				var response *http.Response
				response, err = client.Do(req)
				if err == nil {
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						err = fmt.Errorf("予定の更新先が HTTP %d を返しました", response.StatusCode)
					}
				}
			}
			notify.RecordMeetingRechecks(state, checks)
			slog.Info("meeting cancellation check", "checkpoints", checks, "error", errText(err))
		}
		in := notify.Input{Config: config, Now: time.Now(), SiteURL: cfg.Site, Transport: "ok"}
		data, revision, err := get("/api/tasks")
		if err != nil {
			in.TasksErr = err
		} else if err := json.Unmarshal(data, &in.Tasks); err != nil {
			in.TasksErr = err
		} else if in.Tasks == nil {
			in.Tasks = []task.Task{}
		}
		// A server too old to list them has no alerts to tell about.
		if data, _, err := get("/api/schedule-troubles"); err == nil {
			in.AlertsOK = json.Unmarshal(data, &in.Alerts) == nil
		}
		if config.Enabled && config.TaskNotifications {
			if data, _, err := get("/api/routine-notices?action=1"); err == nil {
				if err := json.Unmarshal(data, &in.RoutineNotices); err != nil {
					in.RoutineNotices = nil
				}
			}
		}
		in.Snapshot, _, in.SnapshotErr = get("/api/calendar/snapshot")
		if in.SnapshotErr == nil {
			var observed struct {
				UpdatedAt string `json:"updated_at"`
				Sources   []struct {
					Account   string `json:"account"`
					Calendar  string `json:"calendar_id"`
					UpdatedAt string `json:"updated_at"`
					Status    string `json:"status"`
				} `json:"sources"`
			}
			if json.Unmarshal(in.Snapshot, &observed) == nil && observed.UpdatedAt != lastSnapshot {
				slog.Info("calendar snapshot observed", "updated_at", observed.UpdatedAt, "sources", observed.Sources)
				lastSnapshot = observed.UpdatedAt
			}
		}
		// The server says which calendars it shows. One too old to say
		// produces no meeting notices.
		if data, _, err := get("/api/calendar/scope"); err == nil {
			var scope []struct {
				Account    string `json:"account"`
				CalendarID string `json:"calendar_id"`
			}
			if json.Unmarshal(data, &scope) == nil {
				in.Config.Sources = [][2]string{}
				for _, entry := range scope {
					in.Config.Sources = append(in.Config.Sources, [2]string{entry.Account, entry.CalendarID})
				}
			}
		}
		mirror.observe(ctx, in.Now, revision, in.TasksErr)
		manifest := notify.Plan(in, state)
		mirror.display(&manifest, in.Now, config.Enabled)
		stateErr := notify.SaveState(statePath, state)
		if stateErr != nil {
			slog.Error("save state", "detail", "通知の状態を保存できませんでした。", "error", stateErr)
		}
		manifestErr := notify.WriteManifest(manifestPath, manifest)
		if manifestErr != nil {
			slog.Error("write manifest", "detail", "通知アプリに渡す通知の一覧を保存できませんでした。", "error", manifestErr)
		}
		if stateErr == nil && manifestErr == nil {
			ackRoutineNotices(ctx, client, base(), in.RoutineNotices, manifest)
		}
		// Log what changed, so that a missing notification can be traced.
		h := manifest.Health
		if h.Tasks != lastHealth.Tasks || h.Calendar != lastHealth.Calendar || h.Transport != lastHealth.Transport || h.Sync != lastHealth.Sync {
			slog.Info("health", "tasks", h.Tasks, "calendar", h.Calendar, "transport", h.Transport, "sync", h.Sync, "tasks_error", errText(in.TasksErr), "calendar_error", errText(in.SnapshotErr))
			lastHealth = h
		}
		current := map[string]bool{}
		for _, n := range manifest.Notices {
			current[n.ID] = true
			if !seen[n.ID] {
				slog.Info("notice planned", "id", n.ID, "kind", n.Kind, "fire", time.Unix(int64(n.Fire), 0).In(commandLocation()).Format("01-02 15:04:05"), "body", n.Body)
			}
		}
		for id := range seen {
			if !current[id] {
				slog.Info("notice dropped", "id", id)
			}
		}
		seen = current

		if cfg.TasksRepo != "" {
			// Looking at clusters may be slow; the notices do not wait for it.
			go watcher.Tick(ctx)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(notify.NextMeetingCheck(state, time.Now())):
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// openLog appends to the log, starting over when it has grown large.
func openLog(path string) (*os.File, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > 2<<20 {
		os.Rename(path, path+".old")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
}
