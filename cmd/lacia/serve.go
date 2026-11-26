package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/claudecode"
	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/gcal"
	"github.com/imos/lacia/internal/gitsync"
	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/rules"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/todo"
	"github.com/imos/lacia/internal/wait"
	"github.com/imos/lacia/internal/web"
)

func runServe(args []string) error {
	fs := commandFlags("serve")
	configPath := fs.String("config", "", "設定ファイル。サーバーの設定を「名前 = 値」の形で1行ずつ書きます。[名前] で始まる節は、そのプロファイルだけに適用します。同じ項目をコマンドラインでも指定したときは、そちらを使います")
	profile := fs.String("profile", "", "このサーバーで使うプロファイル名。共通の設定と同じ項目があれば、プロファイルの値を使います")
	label := fs.String("label", self.DefaultLabel, "このプロファイルの呼び名。画面と「わたしのこと」の文書に表示し、相手への伝言にも使います")
	pages := fs.Bool("pages", true, "false にすると、通常の画面と API は手元か --site のアドレスから使います。プロファイル間のやり取りと稼働確認の入口には外部からも接続できます")
	waits := fs.Bool("waits", true, "クラスタの Job などの終了待ちを登録できます。会話を閉じたあとも終了を待ちます")
	peer := fs.String("peer", "", "伝言と引き継ぎの相手にするプロファイル名。相手の呼び名には、そのプロファイルの label を使います")
	fs.String("url", "", "ほかのプロファイルからこのサーバーに接続するためのアドレス（例: http://lacia-chat）")
	inbox := fs.Bool("inbox", false, "相手からの伝言に返事をする側になります。false のときは伝言を送る側になります")
	// 10148 reads as her name: 1, レ(0), イ(1), シ(4), ヤ(8).
	addr := fs.String("addr", "127.0.0.1:10148", "待ち受けるアドレス。ローカル環境で使う場合は既定のまま、別の端末から使う場合は接続できるアドレスを指定します")
	root := fs.String("root", "", "記録のリポジトリー")
	rescan := fs.Duration("rescan", 10*time.Second, "ファイルの変更を取り込む間隔")
	calendarOverrides := fs.String("calendar-overrides", "", "予定の表示を上書きする JSON ファイル。記録のリポジトリーからの相対パスで指定します。既定の場所は形式0では "+calendarOverridesDefault0+"、形式1では "+calendarOverridesDefault1+" です。ファイルがあれば、その設定を使います")
	calendarPath := fs.String("calendar", "", "予定のスナップショットのパス（省くと記録のリポジトリーから探します）")
	calendarDirect := fs.Bool("calendar-direct", true, "サーバーが直接取得します。false で従来の予定ファイルを再び読みます")
	site := fs.String("site", "", "ブラウザーからこの Web サーバーに接続するための URL（予定の連携などで使います）")
	googleClient := fs.String("google-client", "", "Google Calendar を直接読むための OAuth クライアント設定。Google Cloud の「認証情報」でダウンロードした JSON ファイルを指定します。公開用のアドレスがなくても、認証後に手元のアドレスへ戻って連携できます")
	calendars := fs.String("calendars", "", "表示するカレンダーの一覧。JSON ファイルに account、calendar_id、label を指定します")
	calendarHolidays := fs.String("calendar-holidays", "jp", "日本の祝日を表示する jp、または表示しない off")
	calendarWrite := fs.Bool("calendar-write", false, "予定の連携で、読み取りに加えて、予定の作成や変更の許可も求めます")
	codexCommand := fs.String("codex", "", "Codex を起動するコマンド（例: codex-work）。指定すると依頼を受け付けます")
	user := fs.String("user", "", "わたしがオーナーさんをお呼びするときの名前（例: 田中さん）。省略すると、LACIA_USER、記録の .lacia/config.json の user の順に探します")
	claudeCommand := fs.String("claude", "", "Claude Code を起動するコマンド（例: claude）。指定すると依頼を受け付けます")
	defaultKind := fs.String("default-kind", "", "宛先を省いたときに起動する会話の種類（codex か claude）")
	claudePermission := fs.String("claude-permission", "", "Claude Code の権限の設定。既定の acceptEdits では、追加の許可がなくてもファイルを編集でき、シェルでは lacia コマンドが使えます")
	claudeModel := fs.String("claude-model", "", "Claude Code で使うモデル（省くと Claude Code の既定のモデルを使います）")
	codexModel := fs.String("codex-model", "", "Codex で使うモデル（省くと Codex の設定を使います）")
	codexEffort := fs.String("codex-effort", "", "Codex の推論の強さ（例: medium）。省くと Codex の設定を使います")
	codexSandbox := fs.String("codex-sandbox", "", "Codex のサンドボックスの設定（例: danger-full-access）")
	places := fs.String("places", "", "宛先の場所と端末を対応づける JSON の設定")
	stateDir := fs.String("state", "", "依頼と Codex の接続情報を置くディレクトリー（リポジトリーの外）")
	useDiscord := fs.Bool("discord", false, "Discord から依頼を受けます（接続先は環境変数で指定します）")
	discordMembers := fs.String("discord-members", "", "Discord の参加者の登録表（JSON ファイル）。--discord を使うときに指定してください。登録された方の発言だけを受け付けます")
	discordLog := fs.String("discord-log", "", "Discord のやり取りを日ごとに記録するディレクトリー。記録のリポジトリーからの相対パスで指定します（例: log/discord）。省略すると書きません")
	noticeLog := fs.String("notice-log", "", "書き留めを日ごとに記録するディレクトリー。記録のリポジトリーからの相対パスで指定します（例: log/notices）。--discord-log と同じにすると、やり取りと同じファイルに入ります。省略すると書きません")
	discordLogEntry := fs.Int("discord-log-entry", daylog.DefaultEntryLimit, "日ごとのログに残す、1件の本文の上限（字）")
	discordLogDay := fs.Int("discord-log-day", daylog.DefaultDayLimit, "ログ1日分の上限（バイト）。上限を超える場合は見出しを残し、本文を省きます")
	gitSync := fs.Bool("git-sync", false, "変更を Git にコミットしてプッシュします")
	selfPath := fs.String("self", "", "「わたしのこと」の文書。記録のリポジトリーからの相対パスで指定します（例: memory/self.md）。指定すると書き留めを受け付けます")
	selfAt := fs.String("self-at", "", "自分についての記録を毎日書き直す時刻（記録のタイムゾーンで HH:MM）")
	selfClaude := fs.String("self-claude", "", "夜の整理と書き直しに使う Claude Code の実行ファイル（例: claude）。引数は付けられません")
	selfModel := fs.String("self-model", "fable", "自分についての記録を書き直すモデル")
	selfExchanges := fs.Bool("self-exchanges", true, "夜の整理と書き直しに、最近の依頼と返事や記録のリポジトリーを使います。false にすると、書き直しは書き留めなどをもとに行います")
	people := fs.String("people", "", "オーナーさんのほかにわたしが話す方々の呼び名（例: ご家族）。伝言や引き継ぎの文面に使います。省略すると「ほかの方」です")
	selfTidyPrompt := fs.String("self-tidy-prompt", "", "夜の整理の指示文。指定すると、実行ファイルに入っている指示文の代わりに使います")
	selfTidy := fs.Bool("self-tidy", true, "夜の書き直しの前に、その日の話題を保存し、memory/ に残す候補を窓口に知らせます（--self-exchanges が必要です）。--discord-log があれば、その日のログにも書きます")
	routineExplicitMatters := fs.Bool("routine-explicit-matters", false, "定期タスクでは、本人への用件が明示された回だけ受付を作ります")
	nowPath := fs.String("now", "memory/now.md", "ふだんと違う状況を書いた文書（記録のリポジトリーからの相対パス）。読めた内容を予約の依頼に添えます")
	instructionsPath := fs.String("instructions", "", "会話への指示の文書。指定すると、実行ファイルに入っている指示の代わりに使います")
	windowRollover := fs.String("window-rollover", "", "空いている窓口会話を新しくする時間帯（記録のタイムゾーンで HH:MM-HH:MM）。省略すると切り替えません")
	instructionsExtra := fs.String("instructions-extra", "", "会話への指示の最後に足す文書。その環境だけに当てはまる決まりを書きます")
	if err := fs.Parse(args); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	peerURL, peerLabel := "", ""
	if *configPath != "" {
		profiles, err := applyConfig(fs, *configPath, *profile)
		if err != nil {
			return err
		}
		if *peer != "" {
			if peerURL = strings.TrimRight(profiles[*peer]["url"], "/"); peerURL == "" {
				return fmt.Errorf("相手のプロファイル「%s」の url が、設定ファイルにありません", *peer)
			}
			// The peer is called by the label its own section gives it.
			if peerLabel = profiles[*peer]["label"]; peerLabel == "" {
				peerLabel = self.DefaultPeerLabel
			}
		}
	} else if *profile != "" || *peer != "" {
		return errors.New("--profile と --peer は、--config と一緒に使ってください")
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	// The repository's own settings come last, so the command line and the
	// file outside the repository win, and only what is safe to leave to
	// the repository is read from it.
	if conf := filepath.Join(dir, recordsConfig); fileExists(conf) {
		if _, err := (configSource{path: conf, allow: recordsSettings, base: dir}).apply(fs); err != nil {
			return err
		}
	}
	if *user != "" {
		os.Setenv("LACIA_USER", *user)
	}
	hideCalendarHolidays, err := calendar.HolidaySetting(*calendarHolidays)
	if err != nil {
		return err
	}
	task.SetOwner(dir)
	if *calendars == "" {
		// lacia init writes the list here, so that a copy of the template
		// needs no setting to read its owner's calendar.
		if path := filepath.Join(dir, calendarsFile); fileExists(path) {
			*calendars = path
		}
	}
	if *calendars != "" {
		if calendar.Scope, err = calendar.LoadScope(*calendars); err != nil {
			return fmt.Errorf("--calendars: %w", err)
		}
	}
	if cfg, err := task.LoadConfig(dir); err != nil {
		return err
	} else if err := calendar.SetHidden(cfg.CalendarHidden.Always, cfg.CalendarHidden.Week); err != nil {
		return err
	} else if *calendarOverrides == "" {
		*calendarOverrides = calendarOverridesDefault0
		if cfg.Format >= 1 {
			*calendarOverrides = calendarOverridesDefault1
		}
	}
	rollover, err := parseRolloverWindow(*windowRollover)
	if err != nil {
		return err
	}
	store, err := task.Open(dir)
	if err != nil {
		return err
	}
	// What a profile is follows from its settings alone; the name carries no
	// meaning here. answers is the side of a pair that takes messages from
	// its peer and replies, as the profile that answers on a chat does.
	mode, answers := *label, *inbox && peerURL != ""
	here := *profile
	if here == "" {
		here = "local"
	}
	var kinds []string
	if *codexCommand != "" {
		kinds = append(kinds, caller.Codex)
	}
	if *claudeCommand != "" {
		kinds = append(kinds, caller.Claude)
	}
	routing, err := schedule.LoadRouting(*places, filepath.Dir(*configPath), here, *defaultKind, kinds...)
	if err != nil {
		return err
	}
	// Without pages, only the doors for the peer are open from outside.
	server := &web.Server{Store: store, Version: version(), Label: mode, PeerLabel: peerLabel, PeerURL: peerURL, People: *people, Site: *site, DoorOnly: !*pages, CalendarPath: *calendarPath,
		HideCalendarHolidays: hideCalendarHolidays,
		Place:                here, ResolveDestination: routing.Resolve, AuthorizeHandover: func(place, token string) bool {
			_, device, err := routing.Route(place)
			return err == nil && device != schedule.LocalDevice && routing.Authorized(device, token)
		}}

	if server.CalendarPath == "" {
		for _, candidate := range []string{"calendar/snapshot.json", "results/calendar-web-snapshot.md"} {
			if _, err := os.Stat(filepath.Join(dir, candidate)); err == nil {
				server.CalendarPath = filepath.Join(dir, candidate)
				break
			}
		}
	}
	// Keep the served copy under persistent server state. The checkout path
	// remains an input for a scheduled producer that has not been replaced.
	var snapshotState *calendar.SnapshotState
	var snapshotInput string
	directCalendarConfigured := *calendarDirect && (*site != "" || *googleClient != "" || len(calendar.Scope) > 0) && *stateDir != ""
	if *stateDir != "" && (server.CalendarPath != "" || directCalendarConfigured ||
		fileExists(filepath.Join(*stateDir, "calendar", "snapshot.json")) ||
		fileExists(filepath.Join(dir, "schedules", "calendar.md"))) {
		snapshotInput = server.CalendarPath
		if snapshotInput == "" {
			snapshotInput = filepath.Join(dir, "calendar", "snapshot.json")
		}
		snapshotState = &calendar.SnapshotState{Path: filepath.Join(*stateDir, "calendar", "snapshot.json"), LegacyOnly: !*calendarDirect}
		if snapshotInput != snapshotState.Path {
			// During the transition, prefer the last committed version over
			// an unfinished write from the older producer.
			if !fileExists(snapshotState.Path) {
				if rel, err := filepath.Rel(dir, snapshotInput); err == nil {
					if seed, err := exec.Command("git", "-C", dir, "show", "HEAD:"+filepath.ToSlash(rel)).Output(); err == nil {
						if err := snapshotState.Publish(seed); err != nil {
							slog.Warn("calendar seed", "error", err)
						}
					}
				}
			}
			if err := snapshotState.Import(snapshotInput); err != nil {
				slog.Warn("calendar snapshot import", "error", err)
			}
		}
		server.CalendarPath = snapshotState.Path
	}
	if *stateDir != "" {
		// Which conversations ran commands, kept across restarts.
		if server.Sessions, err = caller.OpenRegistry(filepath.Join(*stateDir, "sessions.json")); err != nil {
			return err
		}
	}
	if *stateDir != "" {
		if server.Handovers, err = handover.Open(filepath.Join(*stateDir, "handovers")); err != nil {
			return err
		}
	}
	if *stateDir != "" && *waits {
		// What is waited for outside a conversation, such as cluster jobs.
		if server.Waits, err = wait.Open(filepath.Join(*stateDir, "waits")); err != nil {
			return err
		}
	}
	handler, err := server.Handler()
	if err != nil {
		return err
	}

	calendarIsDirect := func() bool { return false }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restart := &idleRestart{last: time.Now()}
	var restartBusy func() bool
	var rollWindow func()
	if *stateDir != "" {
		restart.path = filepath.Join(*stateDir, restartRequestFile)
		restartBusy = func() bool { return fileExists(filepath.Join(*stateDir, "drain")) }
	}
	takesRequests := len(kinds) > 0
	if takesRequests {
		if *stateDir == "" {
			return errors.New("--codex や --claude を使うときは --state も指定してください")
		}
		queue, err := request.Open(filepath.Join(*stateDir, "requests"))
		if err != nil {
			return err
		}
		// Codex and its sub-agents run the lacia command; point it at this
		// server so their changes go through one place.
		_, port, _ := net.SplitHostPort(*addr)
		env := []string{"LACIA_API=http://127.0.0.1:" + port, "LACIA_TASKS=" + dir, "LACIA_PROFILE=" + here}
		if *user != "" {
			// The commands the conversations run use the same name.
			env = append(env, "LACIA_USER="+*user)
		}
		// A Codex started under another login (codex-work, codex-personal)
		// runs on the same model.
		if *codexModel != "" {
			env = append(env, "LACIA_CODEX_MODEL="+*codexModel)
		}
		if *codexEffort != "" {
			env = append(env, "LACIA_CODEX_EFFORT="+*codexEffort)
		}
		executable, selfErr := os.Executable()
		if selfErr == nil {
			// The commands that travel in the binary (shims.go) go beside the
			// state, and in front of PATH with the binary itself.
			bin, err := filepath.Abs(filepath.Join(*stateDir, "bin"))
			if err == nil {
				err = writeShims(bin, executable)
			}
			if err != nil {
				return fmt.Errorf("コマンドを置けませんでした: %w", err)
			}
			// LACIA_BIN is the fallback for shells that reset PATH on start.
			env = append(env, "LACIA_BIN="+executable, "PATH="+strings.Join([]string{bin, filepath.Dir(executable), os.Getenv("PATH")}, string(os.PathListSeparator)))
			// A worker started by lacia codex-login takes its Git hooks
			// from here: pre-push refuses every push.
			if os.Getenv("LACIA_WORKER_GIT_HOOKS") == "" {
				env = append(env, "LACIA_WORKER_GIT_HOOKS="+bin)
			}
			// And it may not resume the server's own conversation, whose
			// ID is kept here.
			env = append(env, "LACIA_SERVER_THREAD="+filepath.Join(*stateDir, "codex", "thread.json"))
		}
		instructions := codex.Instructions
		if *instructionsPath != "" {
			own, err := os.ReadFile(*instructionsPath)
			if err != nil {
				return fmt.Errorf("--instructions の文書を読めませんでした: %w", err)
			}
			instructions = string(own)
		}
		if *instructionsExtra != "" {
			extra, err := os.ReadFile(*instructionsExtra)
			if err != nil {
				return fmt.Errorf("--instructions-extra の文書を読めませんでした: %w", err)
			}
			instructions = strings.TrimRight(instructions, "\n") + "\n\n" + strings.TrimSpace(string(extra)) + "\n"
		}
		instructions = rules.FillPeer(instructions, peerLabel)
		currentInstructions := func(text string) func(context.Context) (string, rules.Revision) {
			return func(ctx context.Context) (string, rules.Revision) {
				current := sessionRules(ctx, dir, executable)
				return resolveWindowInstructions(text, dir, current), current.Revision
			}
		}
		// main takes what people ask; side takes the scheduled jobs marked
		// "side", so a calendar fetch never makes a request wait. Each side
		// run gets a fresh conversation: these jobs carry no memory.
		mainRunners, sideRunners := map[string]request.Runner{}, map[string]request.Runner{}
		var windowSessions []windowSession
		localNow := func() time.Time { return time.Now().In(store.Location()) }
		var rolloverDay func() string
		if rollover.enabled() {
			rolloverDay = func() string { return rollover.night(localNow()) }
		}
		var imageBot discordImagePreparer
		closeAgents := func() {}
		if *claudeCommand != "" {
			claude := func(state, text string, ephemeral bool) *claudecode.Runner {
				load := currentInstructions(text)
				return claudecode.New(claudecode.Config{Command: strings.Fields(*claudeCommand), Workspace: dir, StateDir: filepath.Join(*stateDir, state),
					Env: env, CurrentInstructions: func(ctx context.Context) (string, rules.Revision) {
						instructions, rev := load(ctx)
						return strings.TrimRight(instructions, "\n") + "\n" + claudecode.Note, rev
					}, Permission: *claudePermission, Model: *claudeModel, Ephemeral: ephemeral})
			}
			withDiscordContext := func(run request.Runner) request.Runner {
				return func(ctx context.Context, text string) (string, string, error) {
					r := request.Current(ctx)
					if len(r.DiscordImages) > 0 {
						text += "\nこの Claude Code の会話には、Discord の添付画像を渡せません。画像は見られなかったと伝え、本文への対応を続けてください。"
					}
					result, err := runDiscordContext(ctx, imageBot, r, text, false, func(ctx context.Context, text, _ string, _ []string) (codex.Result, error) {
						reply, turn, err := run(ctx, text)
						return codex.Result{Reply: reply, TurnID: turn}, err
					})
					return result.Reply, result.TurnID, err
				}
			}
			window := claude("claude", instructions, false)
			roll := func(night string, ready func() bool) (bool, error) {
				if !ready() {
					return false, nil
				}
				return window.Rollover(night)
			}
			windowSessions = append(windowSessions, windowSession{caller.Claude, roll, func(_ context.Context, night string, ready func() bool) (bool, error) {
				return roll(night, ready)
			}})
			mainRunners[caller.Claude] = withDiscordContext(window.Run)
			sideRunners[caller.Claude] = withDiscordContext(claude("claude-side", codex.SideInstructions, true).Run)
		}
		if *codexCommand != "" {
			codexEnv := append(append([]string(nil), env...), rulesNoticeEnvironment(dir)...)
			bridge := codex.New(codex.Config{Command: strings.Fields(*codexCommand), Workspace: dir, StateDir: filepath.Join(*stateDir, "codex"), Model: *codexModel, Effort: *codexEffort, Sandbox: *codexSandbox, Env: codexEnv, RolloverDay: rolloverDay, CurrentInstructions: currentInstructions(instructions)})
			// A sub-agent finishing is told to the main conversation as a
			// notice, queued behind anything the user is waiting for.
			bridge.OnChild = func(c codex.Child) { enqueueChild(queue, c) }
			windowSessions = append(windowSessions, windowSession{caller.Codex, bridge.Rollover, bridge.RolloverBeforeRequest})
			go bridge.Maintain(ctx)
			sideBridge := codex.New(codex.Config{Command: strings.Fields(*codexCommand), Workspace: dir, StateDir: filepath.Join(*stateDir, "codex-side"),
				Model: *codexModel, Effort: *codexEffort, Sandbox: *codexSandbox, Env: codexEnv, Ephemeral: true, Threads: 2, CurrentInstructions: currentInstructions(codex.SideInstructions)})
			runner := func(b *codex.Bridge, fresh bool) request.Runner {
				return func(ctx context.Context, text string) (string, string, error) {
					res, err := runDiscordTurn(ctx, imageBot, request.Current(ctx), text, b.RunForImages)
					if errors.Is(err, codex.ErrDisconnected) {
						err = fmt.Errorf("%w: %v", request.ErrUncertain, err)
					}
					if fresh {
						b.Close()
					}
					return res.Reply, res.TurnID, err
				}
			}
			mainRunners[caller.Codex], sideRunners[caller.Codex] = runner(bridge, false), runner(sideBridge, true)
			closeAgents = func() { bridge.Close(); sideBridge.Close() }
		}
		defaultDestination, err := routing.Resolve("")
		if err != nil {
			return err
		}
		main := routeRunner(mainRunners, defaultDestination.Kind)
		side := routeRunner(sideRunners, defaultDestination.Kind)

		drain := filepath.Join(*stateDir, "drain")
		draining := func() bool { _, err := os.Stat(drain); return err == nil }
		if rollover.enabled() {
			queue.BeforeMain = func(ctx context.Context, r request.Request) {
				kind := r.Agent
				if kind == "" {
					kind = defaultDestination.Kind
				}
				rollover.beforeMain(ctx, localNow, queue, windowSessions, kind, r)
			}
			rollWindow = func() {
				rollover.run(ctx, store.Location(), func(night string) {
					if draining() {
						return
					}
					restart.runNightly(func() {
						queue.WhenMainIdle(func() {
							ready := func() bool { return rolloverDay() == night && !queue.MainQueued(isChildNotice) }
							rolloverSessions(windowSessions, night, ready)
						})
					})
				})
			}
		}
		// The logs kept in the repository (docs/design/discord.md).
		// Saving to Git follows within about a minute, as for any other
		// change there.
		for _, path := range []string{*discordLog, *noticeLog} {
			if path != "" && !filepath.IsLocal(filepath.FromSlash(path)) {
				return errors.New("--discord-log と --notice-log には、リポジトリーの中の相対パスを指定してください")
			}
		}
		dayLog := func(path string, kind func(string) *daylog.Log) *daylog.Log {
			l := kind(filepath.Join(dir, filepath.FromSlash(path)))
			l.Location = store.Location()
			l.EntryLimit, l.DayLimit = *discordLogEntry, *discordLogDay
			l.Changed = func() {
				if server.Changed != nil {
					server.Changed()
				}
			}
			return l
		}
		var chatLog *daylog.Log
		server.Requests = queue
		if peerURL != "" && !answers {
			sent, err := request.OpenRecords(filepath.Join(*stateDir, "told-peer"))
			if err != nil {
				return err
			}
			server.Peer = &web.Peer{URL: peerURL, Sent: sent}
		}
		if answers {
			// Replies people on the chat should also see go to the first channel.
			server.Inbox = &web.Inbox{}
			if channel, _, _ := strings.Cut(os.Getenv("DISCORD_CHANNEL_IDS"), ","); strings.TrimSpace(channel) != "" && *useDiscord {
				server.Inbox.Discord = "discord:" + strings.TrimSpace(channel)
			}
		}
		// A reply to a message from the task side goes back there at once.
		replyToWork := func(r request.Request) {
			if answers {
				web.ReplyToPeer(ctx, nil, peerURL, queue, r)
			}
		}
		queue.Finished = replyToWork
		if *useDiscord {
			bot, err := newDiscord(filepath.Join(*stateDir, "discord.json"), *discordMembers, queue, store.Location())
			if err != nil {
				return err
			}
			if *discordLog != "" {
				chatLog = dayLog(*discordLog, daylog.Discord)
				bot.Log = chatLog
			}
			imageBot = bot
			server.Members = bot
			server.Attachments = bot
			deliver := func(r request.Request) { deliverDiscordResult(ctx, bot, queue, r) }
			queue.Finished = func(r request.Request) { deliver(r); replyToWork(r) }
			go func() {
				// Replies that were ready when the server last stopped.
				for _, r := range queue.Undelivered(time.Now().Add(-2 * time.Hour)) {
					deliver(r)
				}
				if err := bot.Run(ctx); err != nil {
					slog.Error("discord stopped", "error", err)
					stop()
				}
			}()
		}
		if *selfPath != "" {
			keeper := &self.Keeper{Location: store.Location(), Path: filepath.Join(dir, filepath.FromSlash(*selfPath)), Dir: filepath.Join(*stateDir, "self"), Label: mode, PeerLabel: peerLabel, ChatSide: answers, At: *selfAt, People: *people}
			if command := strings.TrimSpace(*selfClaude); command != "" {
				// The nightly launcher owns the permission and workspace flags.
				executable, err := exec.LookPath(command)
				if err != nil {
					if len(strings.Fields(command)) > 1 {
						return errors.New("--self-claude には Claude Code の実行ファイルだけを指定してください。引数は付けられません")
					}
					// The executable can be installed after configuring the server.
					slog.Warn("self: the nightly rewrite is off; the command is missing", "command", command)
					keeper.At = ""
				} else {
					executable, err = filepath.Abs(executable)
					if err != nil {
						return err
					}
					keeper.OpenSession = self.Claude(executable, *selfModel, dir, *selfExchanges, *stateDir, *configPath, *discordMembers)
				}
			}
			if *selfExchanges {
				keeper.Exchanges = func(since time.Time) []self.Exchange { return exchangesSince(queue, since) }
				keeper.RecordChanges = self.RecentRecords(dir, keeper.Path)
			}
			if server.Peer != nil {
				keeper.PeerCore, keeper.ShareCore = server.Peer.Core, server.Peer.ShareCore
			}
			// The conversation runs for days and reads the document only at
			// the start, so it is told whenever the document changed.
			keeper.Saved = func(night self.Night) {
				if server.Changed != nil {
					server.Changed()
				}
				text := "自分についての記録（`" + *selfPath + "`）が新しくなりました。読み直して、これまでのやり取りを自分がどう受け止めてきたかを確かめてください。作業の許可については、これまでどおり決まりに従ってください。読み終えたら、「報告はありません。」とだけ答えてください。"
				if _, _, err := queue.Add(request.Request{ID: "self-" + night.At.UTC().Format("20060102-150405"), Kind: request.System, Source: "夜の書き直し", Text: text}); err != nil {
					slog.Error("self notice", "error", err)
				}
			}
			if *noticeLog != "" {
				// On the chat side the notes join the conversation's files.
				notices := chatLog
				if notices == nil || path.Clean(*noticeLog) != path.Clean(*discordLog) {
					notices = dayLog(*noticeLog, daylog.Notices)
				}
				keeper.Noted = func(n self.Note) {
					if _, err := notices.Add(daylog.Notice(n.At, n.From, n.Task, n.Text)); err != nil {
						slog.Error("notice log", "error", err)
					}
				}
			}
			if *selfTidyPrompt != "" {
				text, err := os.ReadFile(*selfTidyPrompt)
				if err != nil {
					return fmt.Errorf("--self-tidy-prompt の文書を読めませんでした: %w", err)
				}
				keeper.TidyPrompt = string(text)
			}
			if *selfTidy && *selfExchanges {
				wireTidy(keeper, queue, chatLog, *discordLog)
			}
			server.Self = keeper
			go func() {
				ticker := time.NewTicker(time.Minute)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case now := <-ticker.C:
						if !draining() {
							restart.runNightly(func() { keeper.Tick(ctx, now) })
						}
					}
				}
			}()
		}
		defer closeAgents()

		dispatch, err := schedule.OpenDispatch(filepath.Join(*stateDir, "schedule-dispatch.json"), routing)
		if err != nil {
			return err
		}
		dispatch.Paused = draining
		dispatch.HandoffTaskOpen = func(id string) bool {
			t, err := store.Get(id)
			return err == nil && t.ID == id && !t.Status.Closed()
		}
		dispatch.StartsPaused = restart.pending
		restartBusy = func() bool { return draining() || queue.Working() || dispatch.RunningRemotely() }
		dispatch.Active = func(current schedule.Job) bool {
			for _, job := range schedule.LoadIn(dir, store.Location()) {
				if job.ID != "" && job.OccurrenceName() == current.OccurrenceName() && job.Err == "" {
					return job.Enabled
				}
			}
			return true
		}
		server.ScheduleDispatch = dispatch
		reviews := routine.NewStore(dir)
		server.Routines = reviews
		runs := &schedule.RunLog{Root: dir, Location: store.Location(), State: filepath.Join(*stateDir, "schedule-runs.json"), Execution: dispatch.Get,
			ResolveName: func(name string) string {
				for _, job := range schedule.LoadIn(dir, store.Location()) {
					if job.LegacyName == name && job.ID != "" {
						return job.ID
					}
				}
				return name
			},
			KeepSuccess: func(name string, at time.Time) bool {
				for _, t := range store.List() {
					if t.Status.Closed() || t.AfterRun == "" {
						continue
					}
					since, err := time.Parse(time.RFC3339Nano, t.AfterRunSince)
					if err != nil || !at.After(since) {
						continue
					}
					if t.AfterRun == name {
						return true
					}
					if job, ok := schedule.Resolve(dir, t.AfterRun); ok && job.Name == name {
						return true
					}
				}
				return false
			},
			Review: func(id string, _ schedule.Run, report routine.Result) error {
				_, _, err := reviews.Accept(id, report)
				return err
			},
		}
		schedules := &schedule.Runner{Root: dir, Location: store.Location(), State: filepath.Join(*stateDir, "schedules.json"), Check: routing.Check, TaskClosed: func(id string) bool {
			t, err := store.Get(id)
			return err == nil && t.Status.Closed()
		}, Submit: func(id string, job schedule.Job) error {
			job.ExplicitMatters = *routineExplicitMatters && job.ID != ""

			label := job.Title
			if label == "" {
				label = job.Name
			}
			// A run that outlasts its interval must not pile up a backlog:
			// skip this occurrence while the previous one is unfinished.
			// Once the server reads the calendars itself, the Codex job that
			// used to produce the same file must not overwrite it.
			if job.OccurrenceName() == "calendar" && calendarIsDirect() {
				return nil
			}
			if restored, err := restoreScheduledExecution(queue, dispatch, runs, id, time.Now()); restored || err != nil {
				return err
			}
			to, destination, err := routing.Route(job.To)
			if err != nil {
				return err
			}
			if destination != schedule.LocalDevice {
				prompt := job.Prompt + job.Late + schedule.SituationIn(dir, *nowPath, time.Now(), store.Location()) + job.ReportInstruction(id)
				if err := dispatch.Enqueue(id, job, prompt, time.Now()); err != nil {
					if errors.Is(err, schedule.ErrDispatchBusy) {
						if err := dispatch.Skip(id, job, time.Now()); err != nil {
							return err
						}
						return runs.Skip(job, time.Now())
					}
					return err
				}
				return runs.Begin(job, id, time.Now())
			}
			if _, exists := queue.Get(id); exists {
				return runs.Begin(job, id, time.Now())
			}
			if queue.Busy("sched-" + job.OccurrenceName() + "-") {
				if err := dispatch.Skip(id, job, time.Now()); err != nil {
					return err
				}
				slog.Warn("schedule skipped; previous run unfinished", "job", job.Name, "id", id)
				if err := runs.Skip(job, time.Now()); err != nil {
					slog.Error("schedule run log", "job", job.Name, "error", err)
				}
				return nil
			}
			prompt := job.Prompt + job.Late + schedule.SituationIn(dir, *nowPath, time.Now(), store.Location()) + job.ReportInstruction(id)
			if err := dispatch.Enqueue(id, job, prompt, time.Now()); err != nil {
				return err
			}
			if _, _, err := queue.Add(request.Request{ID: id, Kind: request.System, Source: "予約「" + label + "」", Side: job.Side, Agent: to.Kind, ReplyTo: job.ReplyTo, Text: prompt}); err != nil {
				return err
			}
			// The log is a record of what ran; failing to write it must
			// not stop the job itself.
			if err := runs.Begin(job, id, time.Now()); err != nil {
				slog.Error("schedule run log", "job", job.Name, "error", err)
			}
			return nil
		}}
		settle := func(now time.Time) {
			if err := dispatch.SkipStopped(now); err != nil {
				slog.Error("schedule stop", "error", err)
			}
			for _, execution := range dispatch.List("") {
				if execution.Device != schedule.LocalDevice {
					continue
				}
				r, ok := queue.Get(execution.ID)
				if !ok {
					continue
				}
				if err := dispatch.SyncLocal(execution.ID, r.Status, schedule.Result{Settled: r.Status != request.Queued && r.Status != request.Running, Failed: r.Status != request.Done, Reply: r.Reply, Error: r.Error, Started: r.Started, Finished: r.Finished}); err != nil {
					slog.Error("schedule local result", "id", execution.ID, "error", err)
				}
			}
			// Import the durable outbox before settling its shared run log.
			// Repetition also recovers a crash between saving and delivery.
			for _, execution := range dispatch.List("") {
				if execution.Device == schedule.LocalDevice || execution.Status == "missed" || execution.Status == schedule.Skipped || !execution.Result.Settled {
					continue
				}
				status := request.Done
				if execution.Result.Failed {
					status = request.Failed
				}
				if execution.Status == schedule.Unknown {
					status = request.Unknown
				}
				label := execution.Job.Title
				if label == "" {
					label = execution.Job.Name
				}
				if err := queue.RecordExternal(request.Request{ID: execution.ID, Kind: request.System, Source: "予約「" + label + "」", Side: execution.Job.Side, Agent: execution.Kind, ReplyTo: execution.Job.ReplyTo, Text: execution.Prompt, Status: status, Reply: execution.Result.Reply, Error: execution.Result.Error, Created: execution.Queued, Started: execution.Started, Finished: execution.Result.Finished}); err != nil {
					slog.Error("schedule result import", "id", execution.ID, "error", err)
				}
			}
			err := runs.Settle(func(id string) (schedule.Result, bool) {
				if res, ok := dispatch.Result(id); ok {
					return res, true
				}
				r, ok := queue.Get(id)
				return schedule.Result{
					Settled: r.Status != request.Queued && r.Status != request.Running,
					Failed:  r.Status != request.Done,
					Reply:   r.Reply, Error: r.Error, Started: r.Started, Finished: r.Finished,
				}, ok
			}, now)
			if err != nil {
				slog.Error("schedule run log", "error", err)
			}
			if err := syncRoutineDeliveries(reviews, schedule.LoadIn(dir, store.Location()), queue.Get); err != nil {
				slog.Error("routine delivery receipt", "error", err)
			}
		}
		server.Schedules = func() ([]schedule.Job, map[string]string) {
			jobs, last := routing.Check(schedule.LoadIn(dir, store.Location())), schedules.Last()
			for _, job := range jobs {
				last[job.Name] = max(last[job.Name], last[job.OccurrenceName()])
			}
			return jobs, last
		}
		server.Runs = runs
		unlessStopped := func(run request.Runner) request.Runner {
			return func(ctx context.Context, text string) (string, string, error) {
				skipped, err := dispatch.SkipLocalIfStopped(request.Current(ctx).ID, time.Now())
				if err != nil {
					return "", "", err
				}
				if skipped {
					return "報告はありません。", "", nil
				}
				if e, ok := dispatch.Get(request.Current(ctx).ID); ok && e.Device == schedule.LocalDevice {
					device, _ := os.Hostname()
					if e.Job.Profile != "" {
						list, err := login.LoadList(login.ListPath(os.Getenv))
						if err != nil {
							return "", "", err
						}
						conversations := scheduleConversations{root: dir, state: filepath.Join(*stateDir, "schedule-conversations"), base: scheduleServerURL(*addr), place: *profile, logins: list}
						profile, loginName, err := conversations.selection(e)
						if err != nil {
							return "", "", err
						}
						if err := dispatch.SelectLocal(e.ID, device, profile, loginName); err != nil {
							return "", "", err
						}
						reply, err := conversations.run(ctx, e, text)
						return reply, "", err
					}
					if err := dispatch.SelectLocal(e.ID, device, "default", ""); err != nil {
						return "", "", err
					}
				}
				return run(ctx, text)
			}
		}
		deferNewWork := func() bool { return draining() || restart.pending() }
		go queue.Work(ctx, false, unlessStopped(main), deferNewWork)
		go queue.Work(ctx, true, unlessStopped(side), deferNewWork)
		go func() {
			ticker := time.NewTicker(20 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					if draining() {
						continue
					}
					// Write the runs that finished before queuing new ones.
					settle(now)
					if restart.pending() {
						continue
					}
					if err := schedules.Tick(now); err != nil {
						slog.Error("schedule", "error", err)
					}
				}
			}
		}()
	}
	// With every calendar connected, the server reads Google Calendar itself.
	var fetcher *gcal.Fetcher
	if directCalendarConfigured {
		clientID, clientSecret := "", ""
		if *googleClient != "" {
			if clientID, clientSecret, err = gcal.LoadClient(*googleClient); err != nil {
				return err
			}
		}
		// Google sends the user back to the public address, or, without
		// one, to this server on the loopback interface.
		_, port, _ := net.SplitHostPort(*addr)
		gstore := &gcal.Store{Path: filepath.Join(*stateDir, "secrets", "gcal.json")}
		gclient := &gcal.Client{Endpoints: gcal.Google}
		if server.CalendarPath == "" {
			server.CalendarPath = filepath.Join(dir, "calendar", "snapshot.json")
		}
		fetcher = &gcal.Fetcher{Location: store.Location(), Store: gstore, Client: gclient, Path: server.CalendarPath, Overrides: resolvePath(dir, filepath.FromSlash(*calendarOverrides))}
		if snapshotState != nil {
			fetcher.Publish = snapshotState.PublishDirect
		}
		kick := make(chan struct{}, 1)
		server.Calendars = &web.Calendars{Refresh: fetcher.Once, Store: gstore, Client: gclient, Site: *site, Local: "http://127.0.0.1:" + port,
			ClientID: clientID, ClientSecret: clientSecret, Write: *calendarWrite, Readers: func() map[[2]string]string {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return fetcher.Readers(ctx)
			}, Connected: func() {
				fetcher.Forget()
				select {
				case kick <- struct{}{}:
				default:
				}
			}}
		go fetcher.Run(ctx, 10*time.Minute, kick)
	}
	calendarIsDirect = func() bool { return fetcher != nil && fetcher.Ready() }
	if snapshotState != nil && snapshotInput != snapshotState.Path {
		go func() {
			ticker := time.NewTicker(20 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !calendarIsDirect() {
						if err := snapshotState.Import(snapshotInput); err != nil {
							slog.Warn("calendar snapshot import", "error", err)
						}
					}
				}
			}
		}()
	}

	var syncing sync.WaitGroup
	if *gitSync {
		syncer := &gitsync.Syncer{Dir: dir, Prepare: func() error {
			if err := store.Reload(); err != nil {
				return err
			}
			if snapshotState == nil {
				guardCalendar(dir, server.CalendarPath)
			}
			return todo.WriteLive(store, time.Now(), server.Waits, server.Requests, server.Handovers)
		}}
		server.Changed, server.Notice = syncer.Kick, syncer.Notice
		server.PublishedRevision = syncer.PublishedRevision
		syncing.Add(1)
		go func() {
			defer syncing.Done()
			syncer.Run(ctx)
		}()
	}
	// Codex edits task files directly, so the index follows the disk.
	go func() {
		var expired time.Time
		ticker := time.NewTicker(*rescan)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := store.Reload(); err != nil {
					slog.Error("reload", "error", err)
				}
				// Files kept outside Git go away some time after their task closed.
				if time.Since(expired) > time.Hour {
					expired = time.Now()
					for _, t := range store.ExpireFiles(expired) {
						slog.Info("files expired", "id", t.ID)
					}
				}
				// A wait past its deadline, or one nobody continued, goes to the user.
				server.TickWaits()
				// A task returns to review or starts when all its conditions are met.
				if woken := store.WakeWithRuns(now, func(name string, since time.Time) (task.RoutineCompletion, bool) {
					if job, ok := schedule.Resolve(dir, name); ok {
						name = job.Name
					}
					return schedule.FirstCompletion(dir, name, since, now, store.Location())
				}); len(woken) > 0 {
					for _, t := range woken {
						slog.Info("task woken", "id", t.ID, "on_ready", t.OnReady)
						if t.StartsWhenReady() {
							server.StartReady(t)
						}
					}
					if server.Changed != nil {
						server.Changed()
					}
				}
			}
		}
	}()

	var serverHandler http.Handler = handler
	if restartBusy != nil {
		serverHandler = restart.handler(handler)
	}
	srv := &http.Server{Addr: *addr, Handler: serverHandler, ReadHeaderTimeout: 10 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	if takesRequests {
		// Commands run in this repository find the server through this
		// note, so they need no environment variable to reach it.
		removeNote := announceServer(dir, listener.Addr())
		defer func() {
			if !restart.stopping.Load() {
				removeNote()
			}
		}()
	}
	if restartBusy != nil {
		go restart.run(ctx, stop, restartBusy)
	}
	if rollWindow != nil {
		go rollWindow()
	}
	slog.Info("serving", "addr", *addr, "root", dir, "tasks", len(store.List()), "version", server.Version)
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	syncing.Wait() // let the last save finish
	return nil
}

// Where the overrides of how events are shown live when the settings do
// not say, in a repository of format 0 and of format 1.
const (
	calendarOverridesDefault0 = "personal/knowledge/calendar-display-overrides.json"
	calendarOverridesDefault1 = ".lacia/calendar-overrides.json"
)

// ownRulesNote follows the instructions in a repository that keeps its rules
// in AGENTS.md. The instructions leave to the rules what the rules of the
// binary say about these matters, and such a repository may say nothing of
// them.
const ownRulesNote = "この記録は、決まりを `AGENTS.md` に自分で持っています。実行ファイルの決まりは、この指示に重ねていません。目的に合わせること、わたしのこと、ブラウザーで公開ページを読むこと、別のプロファイルとのやり取りについて `AGENTS.md` に決まりがないときは、`lacia rules` の同じ名前の節と、`lacia rules peers` を読んで従ってください。`AGENTS.md` と食い違うときは、`AGENTS.md` に従います。\n"

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
func announceServer(root string, addr net.Addr) func() {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return func() {}
	}
	host := tcp.IP.String()
	if tcp.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	path := filepath.Join(root, filepath.FromSlash(serverNote))
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(tcp.Port))
	if err := os.WriteFile(path, []byte(url+"\n"), 0o644); err != nil {
		return func() {}
	}
	return func() {
		// Leave a note that a newer server has already replaced.
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) == url {
			os.Remove(path)
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
func guardCalendar(repo, path string) {
	rel, err := filepath.Rel(repo, path)
	if path == "" || err != nil {
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return
	}
	previous, err := exec.Command("git", "-C", repo, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
	if err != nil {
		return
	}
	fixed, changed, err := calendar.Guard(previous, current)
	if err != nil {
		slog.Warn("calendar guard", "error", err)
		return
	}
	if changed {
		slog.Warn("calendar guard kept the previous events")
		if err := os.WriteFile(path, fixed, 0o644); err != nil {
			slog.Error("calendar guard", "error", err)
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
