// Command lacia is the single binary of the Lacia system: the task
// commands, the web server and the helpers all live here.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/todo"
)

const usage = `使い方: lacia <コマンド> [引数]

タスクと記録
  task list [--status 状態,...] [--area 領域] [--all] [--json]
    タスクの一覧を表示します。
  task show <ID> [--json]                                 タスクの本文、成果物、子のタスクを表示します。
  task log <ID> [--tail N]                                タスクの履歴を表示します。
  task new --area 領域 --title 題名 [--parent ID]         タスクを作ります。
  task set <ID> <状態> [--note 補足]
    タスクの状態を変えます。
    waiting（外部待ち）にするには、先に、戻る条件（resume_on、review_at、after、after_run、lacia wait add のどれか）を登録します。
    外部の変化を繰り返し確かめながら待つときは、task recheck を使います。
  task field <ID> <項目> [値]
    期限、親のタスク、プロジェクトなどを設定します。
    resume_on と review_at には日時を指定します。after にほかのタスクのIDを指定すると、そのすべてが完了か取りやめになるまで待ちます。
    条件がすべてそろうと、通常はこのタスクが「要対応」に出ます。on_ready に start を指定しておくと、そのときわたしが着手します。
    start のあとに、着手する会話の宛先も指定できます。
    project にはプロジェクトのディレクトリー名を指定します。
  task recheck <ID> --every 間隔 --until 期限 [--next 最初の日時]
    タスクを外部待ちにして、外部の変化を繰り返し確かめます。間隔は 1d（毎日）や 3h のように書きます。
    サーバーが次の確認日時を決め、別の会話に外部の状況を読み取るよう頼みます。期限が来ると「要対応」に出ます。
  task rechecked <ID> <same|changed|unknown> <文章>
    変化がなければ same、変化があれば changed、確かめられなければ unknown を指定します。changed のときはタスクを「確認待ち」にします。
  task claim <ID> [--note 補足]                           この会話がタスクを引き継ぎ、状態を「進行中」にします。
  task note <ID> <文章>                                   タスクの履歴に書き足します。
  task write <ID> <task.md|notes.md> [ファイル]
    ファイルか標準入力から新しい文章を読み込み、タスクの本文か補足を書き換えます。
    task.md の冒頭の項目（状態など）はそのままです。読み取り専用のコピーからも使えます。
  task attach <ID> <ファイル>... [--to artifacts]         タスクに成果物を置きます。既定の files/ は Git の管理外です。
  task lint [--state DIR] [ID...]                         タスクの文章と状態を点検します。
  task path <ID>                                          タスクのディレクトリーを表示します。
  project note <名前> "文章"                              プロジェクトの履歴に書きます。名前にはディレクトリー名か <領域>/<名前> を指定します。
  knowledge list [--area AREA] [--json]                   知識の文書ごとに、題名、読む場面、更新日を表示します。
  procedures list [--area AREA] [--json]                  手順の文書ごとに、題名、読む場面、更新日を表示します。
  todo [--root DIR]                                       タスクの一覧から TODO.md を作り直します。

依頼と引き継ぎ
  ask [--task ID] [--keep-status] [--wait] <文章>         わたしの窓口に依頼を送ります。
  ask --resume <依頼ID> [--wait]
    ご本人が窓口に直接送ったタスクの依頼は、状態が「結果不明」か「失敗」なら続きを頼めます。
    元の依頼に --keep-status を付けた場合や、タスクが完了か取りやめになったあとは再開できません。
  tell --task ID [--to 宛先] [--owner-action 操作] <文章>
    指定した会話に申し送りを届けます。同じ端末でも、別の端末でも受け取れます。
    相手が受け取ったかは lacia wait show <ID> で確かめられます。
  handoff --title 題名 --from 依頼した人 <文章>           チャットで受けた依頼を、タスクを管理する側に引き継ぎます。
  handoff status <引き継ぎのID>                           引き継ぎ先のタスクの状態を確かめます。
  peer tell [--task ID] [--discord] [--wait] <文章>       相手のプロファイル（設定の peer）のレイシアへ伝言を送ります。
  peer status <伝言のID>                                  伝言への返事を確かめます。
  wait add --task ID --job <context>/<名前空間>/<Job名> --by <期限> [--next 宛先] <終わったらすること>
    クラスタの Job の終了待ちを登録します。この会話を閉じても、サーバーに登録が残ります。
    続きを届ける宛先があれば、処理が終わるとその会話へ申し送ります。詳しい使い方は lacia wait で読めます。
  wait list | show <ID> | ack <ID> | cancel <ID>          終了待ちや申し送りを調べ、受け取りを知らせたり取り消したりできます。
  job run --task ID --context <context> -f <YAML> --by <期限> <終わったらすること>
    クラスタに Job の実行を依頼し、その終了を待つよう登録します。

自分についての記録と Discord
  notice [--task ID] <文章>                               心が動いたことを書き留めます。夜の書き直しの材料になります。
  self [show]                                             自分についての記録と、次の書き直しに使う書き留めを表示します。
  self tidy YYYY-MM-DD                                    指定した日の話題と覚えておく候補を表示します。
  self improve                                            仕組みで直せそうなことの候補を表示します。
  self improve <候補のID> task <タスクのID>               候補をタスクにしたことを残します。
  self improve <候補のID> skip "理由"                     候補を見送ったことを残します。
  self sleep [--force]                                    夜の書き直しをいま始めます。
  self backfill --state DIR [--root DIR] [--dry-run]      残っている書き留めを、日ごとのログへ書き足します。
  discord attach --request ID --file <相対パス.pdf>       サーバーの local/ にある PDF を、元の Discord の依頼へ添付して返します。
  discord members                                         Discord で発言を受け付ける方の一覧を表示します。
  discord pause <名前かID> <分>                           指定した時間、お子さんからの発言の受け付けを止めます。
  discord resume <名前かID>                               止めていた発言の受け付けを再開します。
  discord backfill --state DIR [--root DIR] [--dry-run]   保存してある依頼を読み、過去の Discord の発言とわたしの投稿を、残っている書き留めと一緒にログへ書き足します。

起動と設定
  calendar list [--period today|tomorrow|week] [--json]   サーバーが保存した予定と取得状況を読みます。日付は --from、--until で指定できます。
  init [--user 名前] [--calendar メール]                  ひな形から作った記録に、呼び名と、予定を読むアカウントを設定します。
  rules [名前 | list]
    会話が従う決まりを表示します。名前を省くと、いつも守る決まり（core）を表示します。
    記録に rules/<名前>.md があれば、その文書を使います。
  docs [名前]
    実行ファイルに入っている設計文書を読みます。名前を省くと一覧を出します。
  codex [引数]、claude [引数]
    Codex か Claude Code でわたしとの会話を始めます。決まりと、きょうの状況を渡します。
    --login <名前>、codex-<名前>、claude-<名前> でログインを選べます。--yolo は承認とサンドボックスを外します。
    自動メモリーは既定で無効です。--memory を付けると、この起動で有効になります。--memory=false で無効を指定できます。
    残りの引数は、起動するコマンドに渡します。
  codex-login <名前> [--yolo] [引数]                      LACIA_LOGINS のファイルから指定したログインを選び、Codex を起動します。
  who                                                     この会話の名前と、最近コマンドを使った会話や進行中のタスクを受け持つ会話を表示します。
  restart --when-idle                                    サーバーに再起動を予約します。処理が終わるとサーバーが終了し、監督プロセスが起動し直します。
  busy [--within 5m]                                      サーバーの再起動前に、処理中や順番待ちの依頼と、最近コマンドを使ったほかの会話を調べます。1件でもあれば終了コードは1です。
  schedules [--root DIR]                                  予約ごとに、設定した時刻や間隔と、使う会話を表示します。停止中の予約や設定の誤りも分かります。
  doctor [--root DIR] [--links] [--state DIR]              記録の形とタスクの状態を点検し、誤りとお知らせを表示します。--links を付けると相対リンクの行き先も確かめます。
  doctor local [--root DIR] [--json]                      この端末にある local/ の使用量を読み取ります。
  migrate [--root DIR] [--moves FILE] [--dry-run]         記録を次の形式へ移します。移した結果をコミットする前に確かめられます。
  migrate --undo [--root DIR]                             移す前の形に戻します。
  agent --config FILE --state DIR
    macOS の通知アプリが使います。設定で指定したサーバーに接続します。接続先を省くと、手元のサーバーを使います。

  serve [--root DIR] [--state DIR] [--codex COMMAND] [--claude COMMAND]
        [--config FILE [--profile 名前]] [--addr HOST:PORT]
    Web サーバーを起動します。既定のアドレスは 127.0.0.1:10148 です。ほかの設定は lacia serve -h で見られます。
  shims <DIR>                                             lacia-browse など、実行ファイルに含まれるコマンドを書き出します。

タスクの状態は todo、doing、waiting、review、closing、done、cancelled のどれかです。
記録のリポジトリーは --root か環境変数 LACIA_TASKS で指定します。どちらも省くと、いまのディレクトリーから順に親ディレクトリーをたどって探します。
同じ記録を使う lacia serve が動いていれば、タスクへの変更はそのサーバーに送ります。接続先がなく、手元の記録に書き込めるときは、コマンドが直接ファイルを更新します。
別のサーバーに接続するには、LACIA_API に URL を指定してください。ask など、依頼を届けるコマンドには lacia serve への接続が必要です。
`

func commandFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "lacia %s のオプション\n", name)
		fs.PrintDefaults()
	}
	return fs
}

func main() {
	noticeRulesChange(os.Args[1:], os.Stderr)
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "lacia: "+err.Error())
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return nil
	}
	// The name the texts call the owner by. A command that is given its
	// repository another way (serve --root) sets it again.
	if root, err := findRoot(""); err == nil {
		task.SetOwner(root)
	} else {
		task.SetOwner("")
	}
	switch args[0] {
	case "codex-login":
		// On success this never returns: the process becomes Codex. On
		// failure it says nothing of paths, logins or the prompt.
		if err := login.Run(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "codex_login_failed")
			os.Exit(1)
		}
		return nil
	case "agent":
		return runAgent(args[1:])
	case "task":
		return runTask(args[1:])
	case "serve":
		return runServe(args[1:])
	case "shims":
		return runShims(args[1:])
	case "ask":
		return runAsk(args[1:])
	case "handoff":
		return runHandoff(args[1:])
	case "peer":
		return runPeer(args[1:])
	case "discord":
		return runDiscord(args[1:])
	case "notice":
		return runNotice(args[1:])
	case "self":
		return runSelf(args[1:])
	case "todo":
		return runTodo(args[1:])
	case "who":
		return runWho(args[1:])
	case "rules":
		return runRules(args[1:])
	case "docs":
		return runDesignDocs(args[1:])
	case "init":
		return runInit(args[1:])
	case "codex", "claude":
		return runLaunch(args[0], "", args[1:])
	case "restart":
		return runIdleRestart(args[1:])
	case "busy":
		return runBusy(args[1:])
	case "wait":
		return runWait(args[1:])
	case "job":
		return runJob(args[1:])
	case "tell":
		return runTell(args[1:])
	case "schedules":
		return runSchedules(args[1:])
	case "run":
		return runRoutine(args[1:])
	case "knowledge", "procedures":
		return runDocuments(args[0], args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "calendar":
		return runCalendar(args[1:])
	case "migrate":
		return runMigrate(args[1:])
	case "project":
		return runProject(args[1:])
	case "codex-stop":
		return runCodexStop(args[1:])
	case "codex-probe":
		return runCodexProbe(args[1:])
	}
	// `lacia codex-<login>` and `lacia claude-<login>` are short for
	// `--login <login>`.
	for _, kind := range []string{"codex", "claude"} {
		if name, ok := strings.CutPrefix(args[0], kind+"-"); ok && name != "" {
			return runLaunch(kind, name, args[1:])
		}
	}
	return fmt.Errorf("%q というコマンドはありません。lacia help で一覧を見られます", args[0])
}

// findRoot locates the task repository: an explicit path wins, then the
// environment, then the nearest ancestor that looks like one.
func findRoot(explicit string) (string, error) {
	for _, candidate := range []string{explicit, os.Getenv("LACIA_TASKS")} {
		if candidate != "" {
			return filepath.Abs(candidate)
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, task.ConfigFile)); err == nil {
			return dir, nil
		}
		for _, area := range task.DefaultAreas {
			for _, format := range []int{0, task.LatestFormat} {
				if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(task.AreaDir(format, area)), "tasks")); err == nil && info.IsDir() {
					return dir, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("記録のリポジトリーが見つかりません。--root か LACIA_TASKS で指定してください")
		}
		dir = parent
	}
}

// requireCurrentRecords leaves older layouts available to read-only tools
// and migration while keeping normal commands on one layout.
func requireCurrentRecords(root string) error {
	cfg, err := task.LoadConfig(root)
	if err != nil {
		return err
	}
	return cfg.RequireCurrentFormat()
}

// runTodo regenerates TODO.md. The server does this before every save; the
// command is for a repository no server is running on.
func runTodo(args []string) error {
	fs := commandFlags("todo")
	root := fs.String("root", "", "記録のリポジトリー")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	if err := requireCurrentRecords(dir); err != nil {
		return err
	}
	store, err := task.Open(dir)
	if err != nil {
		return err
	}
	return todo.Write(store, time.Now())
}

// runSchedules lists the scheduled jobs and reports definitions that
// cannot be used, so a mistake is found before the time comes.
func runSchedules(args []string) error {
	fs := commandFlags("schedules")
	root := fs.String("root", "", "記録のリポジトリー")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	broken := 0
	for _, job := range schedule.Load(dir) {
		lane := "窓口"
		if job.Side {
			lane = "定期処理用"
		}
		switch {
		case job.Err != "":
			broken++
			fmt.Printf("%-28s 定義に問題があります: %s\n", job.Name, job.Err)
		case !job.Enabled:
			fmt.Printf("%-28s 止めてあります\n", job.Name)
		default:
			fmt.Printf("%-28s %s（%s）%s\n", job.Name, job.When(), lane, job.Title)
		}
	}
	if broken > 0 {
		return fmt.Errorf("%d件の定義に問題があります", broken)
	}
	return nil
}
