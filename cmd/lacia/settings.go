package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/self"
)

// setting is one option of lacia serve. The table below is the one place an
// option is defined: the flags, the files that may set it, lacia config, and
// the tables of docs/design/settings.md all come from it.
type setting struct {
	name, group string
	// def is the default: a string, a bool, an int or a time.Duration. A
	// string option without one has nil.
	def any
	// shown is the default as people are told it, where it is decided
	// later than the flags are made or depends on other settings.
	shown string
	where where
	// path marks a file outside the records' layout: a relative value in
	// the records' file counts from the records.
	path bool
	help string
}

// where says which files may set an option.
type where int

const (
	// machine is the command line and the file of the machine.
	machine where = iota
	// records adds the records' own file (.lacia/serve.conf). The
	// conversations write to the records, so nothing that would loosen
	// what holds them is set there: where the server listens, which
	// commands it starts, the sandbox, pushing, what it connects to.
	records
	// commandLine is the command line alone.
	commandLine
)

var placeNames = map[where]string{machine: "端末", records: "端末、記録", commandLine: "コマンドライン"}

// The groups, in the order of the document.
const (
	groupBasic    = "基本"
	groupRequests = "依頼と会話"
	groupCalendar = "予定"
	groupSelf     = "わたしのこと"
	groupPeers    = "複数のプロファイルと Discord"
)

var settingGroups = []string{groupBasic, groupRequests, groupCalendar, groupSelf, groupPeers}

// The defaults the server decides after the flags are read.
const (
	nightAt      = "04:00"
	nightAtInbox = "03:30" // the side that answers its peer rewrites first
	off          = "off"
)

var serveSettings = []setting{
	{name: "config", group: groupBasic, shown: "`~/.lacia/config/lacia.conf`（あれば）", where: commandLine, help: "端末の設定のファイルです。サーバーの設定を「名前 = 値」の形で1行ずつ書きます。[名前] で始まる節は、そのプロファイルだけに当てはまります"},
	{name: "profile", group: groupBasic, where: commandLine, help: "設定のファイルの中の、このサーバーが動かすプロファイルの名前です。共通の設定と同じ項目があれば、プロファイルの値を使います"},
	{name: "addr", group: groupBasic, def: "127.0.0.1:10148", help: "待ち受けるアドレスです。ローカル環境で使うときは既定のまま、別の端末から使うときは接続できるアドレスを指定します"},
	{name: "root", group: groupBasic, shown: "自動で探します", help: "記録のリポジトリーの場所です。~/ で始めて書けます。端末の設定の共通の部分に書くと、記録の外で実行したコマンド（lacia codex、lacia claude、lacia open など）も、この記録を使います。サーバーで省くと、環境変数 LACIA_TASKS を使い、それもなければ、いまのディレクトリーから親を順にたどって探します"},
	{name: "state", group: groupBasic, shown: "`~/.lacia/state`", help: "依頼の記録や、会話との接続の情報を置くディレクトリーです。記録のリポジトリーの外に置きます"},
	{name: "rescan", group: groupBasic, def: 10 * time.Second, help: "ファイルの変更を取り込む間隔です。サーバーは同じ間隔で、タスクが待っている日時やほかのタスクの条件も確かめます"},
	{name: "site", group: groupBasic, help: "ブラウザーからこのサーバーに接続するための公開のアドレスです。予定の連携の戻り先と、外からの呼び出しの見分けに使います"},
	{name: "pages", group: groupBasic, def: true, help: "false にすると、通常の画面と API を使えるのは、サーバーの手元からと、site のアドレスからです。プロファイル間のやり取りと稼働確認には、別の入口があります"},
	{name: "git-sync", group: groupBasic, def: false, help: "変更を Git へコミットして、プッシュします"},
	{name: "user", group: groupBasic, shown: "オーナーさん", help: "わたしがオーナーさんを呼ぶ名前です（例: 田中さん）。省くと、環境変数 LACIA_USER、記録の .lacia/config.json の user の順に探します"},
	{name: "label", group: groupBasic, def: self.DefaultLabel, where: records, help: "このプロファイルの呼び名です。画面、「わたしのこと」の文書、相手のプロファイルへの伝言に出ます"},
	{name: "people", group: groupBasic, shown: self.DefaultPeople, help: "オーナーさんのほかに話す方々を、まとめて呼ぶときの名前です（例: ご家族）。伝言、引き継ぎ、自分についての記録の依頼の文面に出ます"},

	{name: "codex", group: groupRequests, help: "Codex を起動するコマンドです（例: codex-work）。指定すると、依頼を受け付けます"},
	{name: "codex-sandbox", group: groupRequests, help: "Codex のサンドボックスの設定です（例: danger-full-access）"},
	{name: "codex-model", group: groupRequests, shown: "Codex の設定のまま", help: "Codex に使わせるモデルです。別のログインで起動する Codex（lacia codex-login）にも、同じモデルを渡します"},
	{name: "codex-effort", group: groupRequests, shown: "Codex の設定のまま", help: "Codex の推論の強さです（例: medium）"},
	{name: "claude", group: groupRequests, help: "Claude Code を起動するコマンドです（例: claude）。指定すると、依頼を受け付けます。codex と一緒にも指定できます"},
	{name: "claude-model", group: groupRequests, shown: "Claude Code の既定", help: "Claude Code に使わせるモデルです"},
	{name: "claude-permission", group: groupRequests, shown: "acceptEdits", help: "Claude Code の権限のモードです。既定の acceptEdits では、追加の許可がなくてもファイルを編集でき、シェルでは lacia コマンドが使えます"},
	{name: "default-kind", group: groupRequests, shown: "Codex があれば codex", help: "種類を省いた依頼や予約を渡す会話です。codex か claude を指定します"},
	{name: "places", group: groupRequests, help: "別の端末で動かす場所と、その端末の登録名、既定の種類、秘密のファイルを、JSON で指定します"},
	{name: "instructions", group: groupRequests, help: "会話への指示の文書です。指定すると、実行ファイルに入っている指示の代わりに使います"},
	{name: "instructions-extra", group: groupRequests, where: records, path: true, help: "会話への指示の最後に足す文書です。その環境だけに当てはまる決まりを書きます"},
	{name: "now", group: groupRequests, def: "memory/now.md", where: records, help: "ふだんと違う状況を書いた文書の、記録のリポジトリーの中でのパスです。読めた内容を予約の依頼に添えます"},
	{name: "waits", group: groupRequests, def: true, help: "クラスタの Job などの終了を、会話の外で待つ仕組みを使います"},
	{name: "routine-explicit-matters", group: groupRequests, def: false, help: "定期タスクでは、ご本人への用件が書かれた回だけ、受付を作ります"},

	{name: "calendars", group: groupCalendar, where: records, path: true, shown: "記録の `.lacia/calendars.json`", help: "表示して読むカレンダーの一覧です。JSON のファイルに account、calendar_id、label を書きます"},
	{name: "calendar-direct", group: groupCalendar, def: true, help: "サーバーが Google Calendar から予定を読みます。false にすると、これまでの予定のファイルを読みます"},
	{name: "google-client", group: groupCalendar, where: records, path: true, shown: "なし。Web の「予定の連携」のページで入力します", help: "Google Calendar を読むための OAuth クライアントの JSON です。Google Cloud の「認証情報」でダウンロードしたファイルを指定します"},
	{name: "calendar-write", group: groupCalendar, def: false, where: records, help: "予定の連携で、読み取りに加えて、予定の作成や変更の許可も求めます。サーバーが自分から予定を書き換えることはありません"},
	{name: "calendar-holidays", group: groupCalendar, def: "jp", where: records, help: "日本の祝日を表示するときは jp、表示しないときは off を指定します"},
	{name: "calendar", group: groupCalendar, where: records, path: true, shown: "記録の `calendar/snapshot.json`", help: "予定のスナップショットのファイルです。以前の予約が作ったデータを取り込むときに使います"},
	{name: "calendar-overrides", group: groupCalendar, def: calendarOverridesDefault, where: records, help: "予定の表示の上書きを書いた JSON の、記録のリポジトリーの中でのパスです。ファイルがなければ、上書きはしません"},

	{name: "self", group: groupSelf, def: "memory/self.md", where: records, help: "「わたしのこと」の文書の、記録のリポジトリーの中でのパスです。書き留めとページに使います。off にすると、夜の処理も、書き留めも、ページも止めます"},
	{name: "self-at", group: groupSelf, shown: nightAt + "。inbox のときは " + nightAtInbox, where: records, help: "夜の処理（夜の整理と、「わたしのこと」の書き直し）を毎日行う時刻です。記録のタイムゾーンで HH:MM と書きます。off にすると、夜の処理だけを止めます"},
	{name: "self-claude", group: groupSelf, shown: "claude の設定の実行ファイル。なければ claude を探します", help: "夜の処理に使う Claude Code の実行ファイルです。引数は付けられません。Claude Code も Codex も見つからない夜は、夜の処理を行わず、その理由を履歴に残します"},
	{name: "self-agent", group: groupSelf, def: nightAuto, where: records, help: "夜の処理を動かすものです。auto では、夜ごとに Claude Code を探し、見つからなければ Codex で動かします。claude か codex にすると、そちらだけで動かします。Codex では、codex の設定の実行ファイル（引数は使いません）と、codex-model、codex-effort を使います"},
	{name: "self-model", group: groupSelf, def: "fable", where: records, help: "夜の処理を Claude Code で動かすときのモデルです"},
	{name: "self-exchanges", group: groupSelf, def: true, where: records, help: "夜の処理に、最近の依頼と返事（最長72時間分）を含め、記録のリポジトリーを読むことを許します。false のときも、前の版、書き留め、共有する節の変更は材料に残ります"},
	{name: "self-tidy", group: groupSelf, def: true, help: "夜の書き直しの前に、その日の話題と、memory/ に覚えておく候補を整理し、候補を窓口へ知らせます。self-exchanges が必要です。discord-log があれば、その日のログにも書きます"},
	{name: "self-improve", group: groupSelf, def: false, where: records, help: "夜の書き直しで、「わたしのこと」の項目のうち、仕組みで直せそうなものを候補に挙げ、窓口へ知らせます。タスクを持つプロファイルでだけ動きます"},
	{name: "self-tidy-prompt", group: groupSelf, help: "夜の整理の指示文です。指定すると、実行ファイルに入っている指示文の代わりに使います"},
	{name: "notice-log", group: groupSelf, where: records, help: "書き留めを日ごとに書き足すディレクトリーの、記録のリポジトリーの中でのパスです（例: log/notices）。discord-log と同じにすると、やり取りと同じファイルに入ります"},

	{name: "peer", group: groupPeers, help: "伝言と引き継ぎの相手にするプロファイルの名前です。相手の呼び名は、そのプロファイルの label から読みます。設定のファイルと一緒に使います"},
	{name: "url", group: groupPeers, help: "ほかのプロファイルが、このサーバーへの接続に使うアドレスです（例: http://lacia-chat）"},
	{name: "inbox", group: groupPeers, def: false, help: "相手からの伝言を受けて、返事を返す側になります。false なら、伝言を送る側です"},
	{name: "discord", group: groupPeers, def: false, help: "Discord から依頼を受けます。接続先は、環境変数で指定します"},
	{name: "discord-members", group: groupPeers, help: "Discord で発言を受け付ける方の対応表（JSON のファイル）です。discord を使うときは、必ず指定します"},
	{name: "discord-log", group: groupPeers, help: "Discord のやり取りを日ごとに書き足すディレクトリーの、記録のリポジトリーの中でのパスです（例: log/discord）"},
	{name: "discord-log-entry", group: groupPeers, def: daylog.DefaultEntryLimit, help: "日ごとのログに残す、1件の本文の上限（字）です"},
	{name: "discord-log-day", group: groupPeers, def: daylog.DefaultDayLimit, help: "1日分のログに本文を残す上限（バイト）です。上限を超える発言は、見出しと、本文を省いたことだけを書きます"},
}

// settingNamed returns the option of the name, or false for a name the
// table does not know.
func settingNamed(name string) (setting, bool) {
	for _, s := range serveSettings {
		if s.name == name {
			return s, true
		}
	}
	return setting{}, false
}

// options are the values of the settings, once made into flags.
type options struct {
	fs     *flag.FlagSet
	values map[string]any
}

func serveOptions() options {
	o := options{fs: commandFlags("serve"), values: map[string]any{}}
	for _, s := range serveSettings {
		help := s.help
		if s.shown != "" {
			help += "（既定: " + strings.ReplaceAll(s.shown, "`", "") + "）"
		}
		switch def := s.def.(type) {
		case bool:
			o.values[s.name] = o.fs.Bool(s.name, def, help)
		case int:
			o.values[s.name] = o.fs.Int(s.name, def, help)
		case time.Duration:
			o.values[s.name] = o.fs.Duration(s.name, def, help)
		case string:
			o.values[s.name] = o.fs.String(s.name, def, help)
		default:
			o.values[s.name] = o.fs.String(s.name, "", help)
		}
	}
	return o
}

func (o options) str(name string) *string         { return o.values[name].(*string) }
func (o options) on(name string) *bool            { return o.values[name].(*bool) }
func (o options) num(name string) *int            { return o.values[name].(*int) }
func (o options) span(name string) *time.Duration { return o.values[name].(*time.Duration) }
func (o options) value(name string) string        { return o.fs.Lookup(name).Value.String() }

// settingsTable is the table of a group, as docs/design/settings.md has it.
func settingsTable(group string) string {
	var b strings.Builder
	b.WriteString("| 名前 | 既定 | 書ける場所 | 意味 |\n| --- | --- | --- | --- |\n")
	for _, s := range serveSettings {
		if s.group != group {
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", s.name, s.defaultText(), placeNames[s.where], s.help)
	}
	return b.String()
}

// defaultText is the default as the document says it.
func (s setting) defaultText() string {
	switch {
	case s.shown != "":
		return s.shown
	case s.def == nil || s.def == "":
		return "なし"
	}
	return fmt.Sprintf("`%v`", s.def)
}
