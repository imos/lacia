// Command lacia is the single binary of the Lacia system: the task
// commands, the web server and the helpers all live here.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/imos/lacia/internal/home"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/todo"
)

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
	noticeOldPlaces(os.Args[1:], os.Stderr, isTerminal(os.Stderr))
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "lacia: "+err.Error())
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(helpText())
		return nil
	}
	// The name the texts call the owner by. A command that is given its
	// repository another way (serve --root) sets it again.
	if root, err := findRoot(""); err == nil {
		task.SetOwner(root)
	} else {
		task.SetOwner("")
	}
	if c, ok := findCommand(args[0]); ok {
		return c.run(args[1:])
	}
	return fmt.Errorf("%q というコマンドはありません。lacia help で一覧を見られます", args[0])
}

// findRoot locates the task repository: an explicit path wins, then the
// environment, then the nearest ancestor that looks like one, and last the
// root in the settings of the machine, so that lacia works outside the
// records too.
func findRoot(explicit string) (string, error) {
	for _, candidate := range []string{explicit, os.Getenv("LACIA_TASKS")} {
		if candidate != "" {
			return filepath.Abs(resolvePath("", candidate))
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if root, ok := enclosingRecords(dir); ok {
		return root, nil
	}
	root, err := machineRoot()
	if err != nil {
		return "", err
	}
	if root != "" {
		return filepath.Abs(root)
	}
	return "", fmt.Errorf("記録のリポジトリーが見つかりません。記録の中で実行するか、--root か LACIA_TASKS で指定してください。端末の設定（%s）に「root = 記録の場所」と書くと、記録の外でも使えます", home.Settings(os.Getenv).New)
}

func enclosingRecords(dir string) (string, bool) {
	for {
		if _, err := os.Stat(filepath.Join(dir, task.ConfigFile)); err == nil {
			return dir, true
		}
		for _, area := range task.DefaultAreas {
			if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(task.AreaDir(area)), "tasks")); err == nil && info.IsDir() {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// machineRoot is the root of the common settings in the file of the
// machine, or "" when it has none. A profile's own root is for its server.
func machineRoot() (string, error) {
	path := home.Settings(os.Getenv).Path()
	if !fileExists(path) {
		return "", nil
	}
	o := serveOptions()
	if _, err := (configSource{path: path, only: "root"}).apply(o.fs); err != nil || *o.str("root") == "" {
		return "", err
	}
	return resolvePath("", *o.str("root")), nil
}

// requireCurrentRecords stops a command before it reads or writes records of
// an older layout.
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
