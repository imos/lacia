package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/imos/lacia/internal/login"
)

// command is one command of lacia. The table below is the one place a
// command is defined: run dispatches through it, and lacia help and the
// usage a command gives back come from it.
type command struct {
	name, group string
	run         func(args []string) error
	// uses are the lines of lacia help, one per form of the command. A
	// command without them is for lacia's own use and is not listed.
	uses []use
}

// use is a form of a command: its arguments, and what it does. A help of
// more than one line is shown below the arguments.
type use struct{ args, help string }

// The groups, in the order of lacia help.
const (
	groupTasks    = "タスクと記録"
	groupAsk      = "依頼と引き継ぎ"
	groupSelfCmds = "自分についての記録と Discord"
	groupStart    = "起動と設定"
)

var commandGroups = []string{groupTasks, groupAsk, groupSelfCmds, groupStart}

// commandTable is a function, not a variable, because the commands it
// holds lead back to run, which reads the table.
func commandTable() []command {
	return []command{
		{name: "task", group: groupTasks, run: runTask, uses: []use{
			{"list [--status 状態,...] [--area 領域] [--all] [--json]", "タスクの一覧を表示します。"},
			{"show <ID> [--json]", "タスクの本文、成果物、子のタスクを表示します。"},
			{"log <ID> [--tail N]", "タスクの履歴を表示します。"},
			{"new --area 領域 --title 題名 [--parent ID]", "タスクを作ります。"},
			{"set <ID> <状態> [--note 補足]", "タスクの状態を変えます。\nwaiting（外部待ち）にするには、先に、戻る条件（resume_on、review_at、after、after_run、lacia wait add のどれか）を登録します。\n外部の変化を繰り返し確かめながら待つときは、task recheck を使います。"},
			{"field <ID> <項目> [値]", "期限、親のタスク、プロジェクトなどを設定します。\nresume_on と review_at には日時を指定します。after にほかのタスクのIDを指定すると、そのすべてが完了か取りやめになるまで待ちます。\n条件がすべてそろうと、通常はこのタスクが「要対応」に出ます。on_ready に start を指定しておくと、そのときわたしが着手します。\nstart のあとに、着手する会話の宛先も指定できます。\nproject にはプロジェクトのディレクトリー名を指定します。"},
			{"recheck <ID> --every 間隔 --until 期限 [--next 最初の日時]", "タスクを外部待ちにして、外部の変化を繰り返し確かめます。間隔は 1d（毎日）や 3h のように書きます。\nサーバーが次の確認日時を決め、別の会話に外部の状況を読み取るよう頼みます。期限が来ると「要対応」に出ます。"},
			{"rechecked <ID> <same|changed|unknown> <文章>", "変化がなければ same、変化があれば changed、確かめられなければ unknown を指定します。changed のときはタスクを「確認待ち」にします。"},
			{"claim <ID> [--note 補足]", "この会話がタスクを引き継ぎ、状態を「進行中」にします。"},
			{"note <ID> <文章>", "タスクの履歴に書き足します。"},
			{"write <ID> <task.md|notes.md> [ファイル]", "ファイルか標準入力から新しい文章を読み込み、タスクの本文か補足を書き換えます。\ntask.md の冒頭の項目（状態など）はそのままです。読み取り専用のコピーからも使えます。"},
			{"attach <ID> <ファイル>... [--to artifacts]", "タスクに成果物を置きます。既定の files/ は Git の管理外です。"},
			{"lint [--state DIR] [ID...]", "タスクの文章と状態を点検します。"},
			{"path <ID>", "タスクのディレクトリーを表示します。"},
		}},
		{name: "project", group: groupTasks, run: runProject, uses: []use{{`note <名前> "文章"`, "プロジェクトの履歴に書きます。名前にはディレクトリー名か <領域>/<名前> を指定します。"}}},
		{name: "todo", group: groupTasks, run: runTodo, uses: []use{{"[--root DIR]", "タスクの一覧から TODO.md を作り直します。"}}},

		{name: "ask", group: groupAsk, run: runAsk, uses: []use{
			{"[--task ID] [--keep-status] [--wait] <文章>", "わたしの窓口に依頼を送ります。"},
			{"--resume <依頼ID> [--wait]", "ご本人が窓口に直接送ったタスクの依頼は、状態が「結果不明」か「失敗」なら続きを頼めます。\n元の依頼に --keep-status を付けた場合や、タスクが完了か取りやめになったあとは再開できません。"},
		}},
		{name: "tell", group: groupAsk, run: runTell, uses: []use{{"--task ID [--to 宛先] [--owner-action 操作] <文章>", "指定した会話に申し送りを届けます。同じ端末でも、別の端末でも受け取れます。\n相手が受け取ったかは lacia wait show <ID> で確かめられます。"}}},
		{name: "handoff", group: groupAsk, run: runHandoff, uses: []use{
			{"--title 題名 --from 依頼した人 <文章>", "チャットで受けた依頼を、タスクを管理する側に引き継ぎます。"},
			{"status <引き継ぎのID>", "引き継ぎ先のタスクの状態を確かめます。"},
		}},
		{name: "peer", group: groupAsk, run: runPeer, uses: []use{
			{"tell [--task ID] [--discord] [--wait] <文章|->", "相手のプロファイル（設定の peer）のレイシアへ伝言を送ります。"},
			{"status <伝言のID>", "伝言への返事を確かめます。"},
		}},
		{name: "wait", group: groupAsk, run: runWait, uses: []use{
			{"add --task ID --job <context>/<名前空間>/<Job名> --by <期限> [--next 宛先] <終わったらすること>", "クラスタの Job の終了待ちを登録します。この会話を閉じても、サーバーに登録が残ります。\n続きを届ける宛先があれば、処理が終わるとその会話へ申し送ります。詳しい使い方は lacia wait で読めます。"},
			{"list [--task ID] [--all]", "終了待ちと申し送りの一覧を表示します。"},
			{"show <ID>", "終了待ちか申し送りの中身と、いまの状態を表示します。"},
			{"ack <ID>", "続きや申し送りを受け取ったことを知らせます。"},
			{"cancel <ID>", "終了待ちか申し送りを取り消します。"},
		}},
		{name: "job", group: groupAsk, run: runJob, uses: []use{{"run --task ID --context <context> -f <YAML> --by <期限> <終わったらすること>", "クラスタに Job の実行を依頼し、その終了を待つよう登録します。"}}},
		{name: "run", group: groupAsk, run: runRoutine, uses: []use{{"report <定期タスクID> <実行ID> <JSONファイル|->", "定期タスクの実行の結果を保存します。定期タスクを受け持つ会話が使います。"}}},

		{name: "notice", group: groupSelfCmds, run: runNotice, uses: []use{{"[--task ID] <文章>", "心が動いたことを書き留めます。夜の書き直しの材料になります。"}}},
		{name: "self", group: groupSelfCmds, run: runSelf, uses: []use{
			{"[show]", "自分についての記録と、次の書き直しに使う書き留めを表示します。"},
			{"tidy YYYY-MM-DD", "指定した日の話題と覚えておく候補を表示します。"},
			{"improve", "仕組みで直せそうなことの候補を表示します。"},
			{"improve <候補のID> task <タスクのID>", "候補をタスクにしたことを残します。"},
			{`improve <候補のID> skip "理由"`, "候補を見送ったことを残します。"},
			{"sleep [--force]", "夜の書き直しをいま始めます。"},
			{"dates", "自分についての記録の項目のうち、日付のないものに日付を付けます。本文は変えません。"},
		}},
		{name: "discord", group: groupSelfCmds, run: runDiscord, uses: []use{
			{"attach --request ID --file <相対パス.pdf>", "サーバーの local/ にある PDF を、元の Discord の依頼へ添付して返します。"},
			{"members", "Discord で発言を受け付ける方の一覧を表示します。"},
			{"pause <名前かID> <分>", "指定した時間、お子さんからの発言の受け付けを止めます。"},
			{"resume <名前かID>", "止めていた発言の受け付けを再開します。"},
		}},

		{name: "setup", group: groupStart, run: runSetup, uses: []use{{"[--repo URL] [--dir 場所] [--name 呼び名] [--notifier] [--no-autostart]", "この端末を、レイシアを使える状態にそろえます。何度実行しても構いません。\nはじめは、記録のリポジトリーの URL、記録を置く場所、呼び名を尋ね、空のリポジトリーに記録の初期の中身を置いてプッシュします。\n実行ファイルを ~/.lacia/bin に置き、ログインのときにサーバーが起動するよう登録します（macOS）。--notifier で通知アプリも入れます。\n予定の連携に使う OAuth クライアントは、--google-client-id と --google-client-secret か、環境変数 LACIA_GOOGLE_CLIENT_ID と LACIA_GOOGLE_CLIENT_SECRET で渡します。"}}},
		{name: "update", group: groupStart, run: runUpdate, uses: []use{{"", "導入のときと同じ入口をもう1度実行して、新しい版にします。~/.lacia/src にクローンがあればそこでビルドし、なければ導入のスクリプトを使います。"}}},
		{name: "uninstall", group: groupStart, run: runUninstall, uses: []use{{"[--yes]", "サーバーの登録、通知アプリ、~/.local/bin/lacia のリンク、~/.lacia を消します。記録は残します。"}}},
		{name: "calendar", group: groupStart, run: runCalendar, uses: []use{{"list [--period today|tomorrow|week] [--json]", "サーバーが保存した予定と取得状況を読みます。日付は --from、--until で指定できます。"}}},
		{name: "init", group: groupStart, run: runInit, uses: []use{{"[--user 名前] [--calendar メール]", "ひな形から作った記録に、呼び名と、予定を読むアカウントを設定します。"}}},
		{name: "config", group: groupStart, run: runConfig, uses: []use{{"[serve の引数]", "lacia serve の設定ごとに、いまの値と、その値を決めた場所を表示します。夜の処理が動くかも分かります。読むだけです。"}}},
		{name: "version", group: groupStart, run: runVersion, uses: []use{{"", "版と、もとになったコミットを表示します。"}}},
		{name: "rules", group: groupStart, run: runRules, uses: []use{{"[名前 | list]", "会話が従う決まりを表示します。名前を省くと、いつも守る決まり（core）を表示します。\n記録に rules/<名前>.md があれば、その文書を使います。"}}},
		{name: "docs", group: groupStart, run: runDesignDocs, uses: []use{{"[名前]", "実行ファイルに入っている設計文書を読みます。名前を省くと一覧を出します。"}}},
		{name: "codex", group: groupStart, run: func(args []string) error { return runLaunch("codex", "", args) }, uses: []use{{"[--here] [--login 名前] [--memory] [引数]", "Codex でわたしとの会話を始めます。決まりと、きょうの状況を渡します。\n記録の外で実行すると、記録の場所へ移って始めます。--here を付けると、その場で始めます。\n--login <名前> か codex-<名前> でログインを選べます。--yolo は承認とサンドボックスを外します。\n自動メモリーは既定で無効です。--memory を付けると、この起動で有効になります。--memory=false で無効を指定できます。\n残りの引数は、起動するコマンドに渡します。"}}},
		{name: "claude", group: groupStart, run: func(args []string) error { return runLaunch("claude", "", args) }, uses: []use{{"[--here] [--login 名前] [--memory] [引数]", "Claude Code でわたしとの会話を始めます。引数は codex と同じです。ログインは claude-<名前> でも選べます。"}}},
		{name: "codex-login", group: groupStart, run: runCodexLogin, uses: []use{{"<名前> [--yolo] [引数]", "LACIA_LOGINS のファイルから指定したログインを選び、Codex を起動します。"}}},
		{name: "open", group: groupStart, run: runOpen, uses: []use{{"[タスクのID]", "ブラウザーで、Web のホームの画面を開きます。タスクの ID を付けると、そのタスクのページを開きます。"}}},
		{name: "root", group: groupStart, run: runRoot, uses: []use{{"", `記録の場所を表示します。cd "$(lacia root)" のように使えます。`}}},
		{name: "shell", group: groupStart, run: runShell, uses: []use{{"", "記録の場所で、シェルを開きます。シェルを抜けると、元の場所に戻ります。"}}},
		{name: "who", group: groupStart, run: runWho, uses: []use{{"", "この会話の名前と、最近コマンドを使った会話や進行中のタスクを受け持つ会話を表示します。"}}},
		{name: "restart", group: groupStart, run: runIdleRestart, uses: []use{{"--when-idle", "サーバーに再起動を予約します。処理が終わるとサーバーが終了し、監督プロセスが起動し直します。"}}},
		{name: "busy", group: groupStart, run: runBusy, uses: []use{{"[--within 5m]", "サーバーの再起動前に、処理中や順番待ちの依頼と、最近コマンドを使ったほかの会話を調べます。1件でもあれば終了コードは1です。"}}},
		{name: "schedules", group: groupStart, run: runSchedules, uses: []use{{"[--root DIR]", "予約ごとに、設定した時刻や間隔と、使う会話を表示します。停止中の予約や設定の誤りも分かります。"}}},
		{name: "doctor", group: groupStart, run: runDoctor, uses: []use{
			{"[--root DIR] [--links] [--state DIR]", "記録の形とタスクの状態を点検し、誤りとお知らせを表示します。--links を付けると相対リンクの行き先も確かめます。\nlacia setup で入れた端末の記録なら、端末の導入の状態も点検し、直すことを表示します。"},
			{"local [--root DIR] [--json]", "この端末にある local/ の使用量を読み取ります。"},
		}},
		{name: "agent", group: groupStart, run: runAgent, uses: []use{{"--config FILE --state DIR", "macOS の通知アプリが使います。設定で指定したサーバーに接続します。接続先を省くと、手元のサーバーを使います。"}}},
		{name: "serve", group: groupStart, run: runServe, uses: []use{{"[--config FILE [--profile 名前]] [--root DIR] [--state DIR] [--addr HOST:PORT]", "Web サーバーを起動します。既定のアドレスは 127.0.0.1:10148 です。--config を省くと、~/.lacia/config/lacia.conf があれば読みます。\nほかの設定は lacia serve -h で、いまの値は lacia config で見られます。"}}},
		{name: "shims", group: groupStart, run: runShims, uses: []use{{"<DIR>", "lacia-browse など、実行ファイルに含まれるコマンドを書き出します。"}}},

		// Run by the server and by checks of a machine, not by people.
		{name: "codex-stop", run: runCodexStop},
		{name: "codex-probe", run: runCodexProbe},
	}
}

// findCommand returns the command of the name. `lacia codex-<login>` and
// `lacia claude-<login>` are short for `--login <login>`.
func findCommand(name string) (command, bool) {
	for _, c := range commandTable() {
		if c.name == name {
			return c, true
		}
	}
	for _, kind := range []string{"codex", "claude"} {
		if login, ok := strings.CutPrefix(name, kind+"-"); ok && login != "" {
			return command{name: name, run: func(args []string) error { return runLaunch(kind, login, args) }}, true
		}
	}
	return command{}, false
}

// runCodexLogin becomes Codex on success. On failure it says nothing of
// paths, logins or the prompt.
func runCodexLogin(args []string) error {
	if err := login.Run(args); err != nil {
		fmt.Fprintln(os.Stderr, "codex_login_failed")
		os.Exit(1)
	}
	return nil
}

// helpColumn is where the help of a short form starts.
const helpColumn = 58

const helpHeader = "使い方: lacia <コマンド> [引数]\n"

const helpFooter = `
タスクの状態は todo、doing、waiting、review、closing、done、cancelled のどれかです。
記録のリポジトリーは --root か環境変数 LACIA_TASKS で指定します。どちらも省くと、いまのディレクトリーから順に親ディレクトリーをたどって探します。
見つからないときは、端末の設定（~/.lacia/config/lacia.conf）の root を使います。
同じ記録を使う lacia serve が動いていれば、タスクへの変更はそのサーバーに送ります。接続先がなく、手元の記録に書き込めるときは、コマンドが直接ファイルを更新します。
別のサーバーに接続するには、LACIA_API に URL を指定してください。ask など、依頼を届けるコマンドには lacia serve への接続が必要です。
`

func helpText() string {
	var b strings.Builder
	b.WriteString(helpHeader)
	table := commandTable()
	for _, group := range commandGroups {
		fmt.Fprintf(&b, "\n%s\n", group)
		for _, c := range table {
			if c.group == group {
				writeUses(&b, "", c)
			}
		}
	}
	b.WriteString(helpFooter)
	return b.String()
}

// usageError is what a command says when its arguments fit none of its
// forms.
func usageError(name string) error {
	c, _ := findCommand(name)
	var b strings.Builder
	b.WriteString("使い方:\n")
	writeUses(&b, "lacia ", c)
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

func writeUses(b *strings.Builder, prefix string, c command) {
	for _, u := range c.uses {
		head := "  " + prefix + strings.TrimSpace(c.name+" "+u.args)
		if !strings.Contains(u.help, "\n") && textWidth(head) < helpColumn {
			fmt.Fprintf(b, "%s%s%s\n", head, strings.Repeat(" ", helpColumn-textWidth(head)), u.help)
			continue
		}
		b.WriteString(head + "\n")
		for _, line := range strings.Split(u.help, "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
}

// textWidth counts the columns a terminal gives the text: two for the
// letters of Japanese, one for the rest.
func textWidth(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x1100 {
			n++
		}
	}
	return n
}

// subcommands are the words that may follow the command, as its forms
// list them.
func subcommands(name string) []string {
	c, _ := findCommand(name)
	var words []string
	for _, u := range c.uses {
		word, _, _ := strings.Cut(u.args, " ")
		if strings.HasPrefix(word, "[") && strings.HasSuffix(word, "]") {
			word = word[1 : len(word)-1]
		}
		if word != "" && word[0] >= 'a' && word[0] <= 'z' && !slices.Contains(words, word) {
			words = append(words, word)
		}
	}
	return words
}
