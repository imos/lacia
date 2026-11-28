package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/home"
)

// setupOptions are what lacia setup is told on the command line or asks.
type setupOptions struct {
	repo, dir, name, addr  string
	clientID, clientSecret string
	autostart, open        bool
	notifier               bool
}

// runSetup brings this machine to the state the options describe, and may
// be run again: the first time it makes the records and the settings, and
// later it only puts the binary in place, keeps the server running and
// tells what it found (docs/design/install.md).
func runSetup(args []string) error {
	o, err := parseSetup(args)
	if err != nil {
		return err
	}
	// Git asks for nothing on a terminal it does not own: a missing
	// login is an error to explain, not a prompt hidden behind ours.
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	ctx := context.Background()
	if err := requireDeveloperTools(ctx, program.Git(), "--version"); err != nil {
		return err
	}
	tools, err := disk.OpenTools(home.Dir(os.Getenv))
	if err != nil {
		return err
	}
	defer tools.Close()
	bin, replaced, err := installBinary(tools)
	if err != nil {
		return err
	}
	if err := disk.LinkCommand(bin); err != nil {
		return err
	}
	root, err := machineRoot()
	if err != nil {
		return err
	}
	fresh := root == ""
	if fresh {
		if root, err = setupRecords(ctx, &o, newAsker()); err != nil {
			return err
		}
		if err := writeMachineSettings(tools, root, o.addr); err != nil {
			return err
		}
	} else if o.repo != "" || o.dir != "" {
		return fmt.Errorf("この端末は、すでに %s の記録を使っています。別の記録に切り替えるときは、端末の設定（%s）の root を直してください", root, home.Settings(os.Getenv).Path())
	}
	if o.clientID != "" {
		if err := writeGoogleClient(tools, o.clientID, o.clientSecret); err != nil {
			return err
		}
	}
	c, err := loadServeConfig([]string{"--root", root})
	if err != nil {
		return err
	}
	addr := c.value("addr")
	if err := keepServing(ctx, c, bin, o.autostart, !fresh && replaced); err != nil {
		return err
	}
	if o.notifier || !fresh && replaced && notifierInstalled() {
		if err := installNotifier(ctx, tools, bin, addr); err != nil {
			return err
		}
	}
	setupSummary(os.Stdout, c, o.notifier || notifierInstalled())
	if fresh && o.open && runtime.GOOS == "darwin" {
		program.Open().Output(ctx, "", "http://"+addr+"/")
	}
	return nil
}

func parseSetup(args []string) (setupOptions, error) {
	fs := commandFlags("setup")
	o := setupOptions{}
	fs.StringVar(&o.repo, "repo", "", "記録のリポジトリーの URL です。GitHub で作った、空の非公開のリポジトリーを指定します")
	fs.StringVar(&o.dir, "dir", "", "記録を置く場所です。既定は、ホームの下の、リポジトリーと同じ名前のフォルダーです")
	fs.StringVar(&o.name, "name", "", "わたしに呼んでほしい名前です（例: 田中さん）。新しい記録のときだけ使います")
	fs.StringVar(&o.addr, "addr", "", "サーバーが待ち受けるアドレスです。新しく書く端末の設定に入れます。既定は 127.0.0.1:10148 です")
	fs.StringVar(&o.clientID, "google-client-id", os.Getenv("LACIA_GOOGLE_CLIENT_ID"), "予定の連携に使う OAuth クライアントの ID です。環境変数 LACIA_GOOGLE_CLIENT_ID でも渡せます")
	fs.StringVar(&o.clientSecret, "google-client-secret", os.Getenv("LACIA_GOOGLE_CLIENT_SECRET"), "OAuth クライアントのシークレットです。環境変数 LACIA_GOOGLE_CLIENT_SECRET でも渡せます")
	noAutostart := fs.Bool("no-autostart", false, "ログインのときにサーバーを起動する登録をしません。登録があれば外します")
	noOpen := fs.Bool("no-open", false, "終わったあとに、ブラウザーを開きません")
	fs.BoolVar(&o.notifier, "notifier", false, "Mac の通知アプリも入れます")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, usageError("setup")
	}
	if (o.clientID == "") != (o.clientSecret == "") {
		return o, errors.New("OAuth クライアントの ID とシークレットは、2つそろえて渡してください")
	}
	o.autostart, o.open = !*noAutostart, !*noOpen
	return o, nil
}

// installBinary puts the running binary at ~/.lacia/bin/lacia, keeping the
// one it replaces as lacia.previous. The new file is moved into place by a
// rename, so a server that starts at that moment finds one or the other
// whole, and a signed file is never changed where it lies.
func installBinary(tools *disk.Dir) (string, bool, error) {
	target := filepath.Join(tools.Path(), "bin", "lacia")
	self, err := os.Executable()
	if err != nil {
		return "", false, err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", false, err
	}
	if installed, err := filepath.EvalSymlinks(target); err == nil && installed == self {
		return target, false, nil
	}
	if err := copyInto(tools, self, "bin/lacia.next"); err != nil {
		return "", false, err
	}
	if fileExists(target) {
		if err := copyInto(tools, target, "bin/lacia.previous"); err != nil {
			return "", false, err
		}
	}
	return target, true, tools.Rename("bin/lacia.next", "bin/lacia")
}

func copyInto(tools *disk.Dir, from, name string) error {
	f, err := os.Open(from)
	if err != nil {
		return err
	}
	defer f.Close()
	return tools.WriteFrom(name, f, 0o755)
}

// writeMachineSettings writes the settings of this machine. Codex is the
// conversation when both agents are installed, as the template did.
func writeMachineSettings(tools *disk.Dir, root, addr string) error {
	var b strings.Builder
	b.WriteString("# lacia setup が書いた、この端末の設定です。設定の一覧は lacia config で読めます。\n")
	fmt.Fprintf(&b, "root = %s\n", root)
	agent := ""
	if _, err := program.Codex("codex").Find(); err == nil {
		agent = "codex"
	} else if _, err := program.ClaudeCode("claude").Find(); err == nil {
		agent = "claude"
	}
	if agent != "" {
		fmt.Fprintf(&b, "%s = %s\n", agent, agent)
	} else {
		fmt.Println("Codex も Claude Code も見つかりませんでした。どちらかを入れたら、端末の設定に「claude = claude」か「codex = codex」を書いて、lacia setup をもう1度実行してください。それまでは、依頼を受け付けません。")
	}
	b.WriteString("git-sync = true\n")
	if addr != "" {
		fmt.Fprintf(&b, "addr = %s\n", addr)
	}
	return tools.WriteFile("config/lacia.conf", []byte(b.String()), 0o600)
}

// writeGoogleClient keeps the OAuth client of an organization outside the
// records, and points the settings of the machine at it. The client is of
// the desktop kind, whose secret Google does not treat as one
// (internal/gcal), so it may come from the environment.
func writeGoogleClient(tools *disk.Dir, id, secret string) error {
	data := fmt.Sprintf(`{"installed":{"client_id":%q,"client_secret":%q,"auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","redirect_uris":["http://localhost"]}}`+"\n", id, secret)
	if err := tools.WriteFile("config/google-client.json", []byte(data), 0o600); err != nil {
		return err
	}
	settings := home.Settings(os.Getenv).Path()
	current, err := os.ReadFile(settings)
	if err != nil {
		return err
	}
	line := "google-client = " + filepath.Join(tools.Path(), "config", "google-client.json")
	if strings.Contains(string(current), line) {
		return nil
	}
	if settings != home.Settings(os.Getenv).New {
		return fmt.Errorf("端末の設定が、以前の版の置き場所（%s）にあります。lacia config に出る手順で移してから、もう1度実行してください", settings)
	}
	fmt.Println("予定の連携に使う OAuth クライアントを、" + filepath.Join(tools.Path(), "config", "google-client.json") + " に置きました。")
	return tools.WriteFile("config/lacia.conf", []byte(strings.TrimRight(string(current), "\n")+"\n"+line+"\n"), 0o600)
}

// asker reads answers from the terminal, even when lacia was started by a
// script piped into sh, as rustup does.
type asker struct {
	in  *bufio.Reader
	out io.Writer
}

func newAsker() *asker {
	in := io.Reader(os.Stdin)
	if !isTerminal(os.Stdin) {
		if tty, err := os.Open("/dev/tty"); err == nil {
			in = tty
		}
	}
	return &asker{in: bufio.NewReader(in), out: os.Stderr}
}

// ask returns the answer, or the default for an empty one. With nothing to
// read, it says which option to give instead.
func (a *asker) ask(question, def, option string) (string, error) {
	if def != "" {
		question += "（Enter で " + def + "）"
	}
	fmt.Fprintf(a.out, "%s\n> ", question)
	line, err := a.in.ReadString('\n')
	if answer := strings.TrimSpace(line); answer != "" {
		return answer, nil
	}
	if err != nil && def == "" {
		return "", fmt.Errorf("答えを読み取れませんでした。%s で指定してください", option)
	}
	return def, nil
}

// setupSummary is the end of lacia setup: where things are, and what the
// nightly rewrite will do, as lacia config tells it.
func setupSummary(out io.Writer, c *serveConfig, notifier bool) {
	fmt.Fprintf(out, "\n導入が済みました。\n  記録: %s\n  Web の画面: http://%s/\n  端末の設定: %s\n  ログ: %s\n", c.root, c.value("addr"), *c.str("config"), serveLog())
	fmt.Fprintf(out, "\n夜の処理\n  %s\n", nightSummary(c))
	fmt.Fprintln(out, "\nlacia codex か lacia claude で、わたしと会話を始められます。lacia doctor で、この端末の状態を確かめられます。")
	if !notifier && runtime.GOOS == "darwin" {
		fmt.Fprintln(out, "Mac の通知アプリを入れるときは、lacia setup --notifier を実行してください。")
	}
}

// serveLog is where the server started at login writes.
func serveLog() string { return filepath.Join(home.Dir(os.Getenv), "logs", "serve.log") }
