package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/task"
)

func runTask(args []string) error {
	if len(args) == 0 {
		return errors.New("task のあとに " + strings.Join(subcommands("task"), "、") + " のどれかを指定してください")
	}
	c, err := parseTaskCommand(args)
	if err != nil {
		return err
	}
	if readOnlyCopy(c.dir) {
		if done, err := c.readFromServer(); done {
			return err
		}
	}
	if err := c.open(); err != nil {
		return err
	}
	switch c.name {
	case "list":
		return outputTaskList(c.store.List(), *c.status, *c.area, *c.all, *c.asJSON)
	case "show":
		return c.showTask()
	case "log":
		return c.printHistory()
	case "new":
		return c.createTask()
	case "set":
		return c.setStatus()
	case "claim":
		return c.claimTask()
	case "field":
		return c.setField()
	case "recheck":
		return c.setRecheck()
	case "rechecked":
		return c.recordRecheck()
	case "attach":
		return c.attachFiles()
	case "note":
		return c.addNote()
	case "write":
		return c.writeDoc()
	case "lint":
		return c.lint()
	case "path":
		return c.printPath()
	default:
		return fmt.Errorf("task %s というコマンドはありません。task のあとには %s のどれかを指定してください", args[0], strings.Join(subcommands("task"), "、"))
	}
}

// taskCommand is one run of `lacia task`: the subcommand, its arguments and
// flags, and the records it works on.
type taskCommand struct {
	name       string
	positional []string
	dir        string
	api        apiClient
	store      *task.Store
	// actor is who the history names; owner is the conversation that
	// takes a task on.
	actor string
	owner task.Owner

	root, state, status, area, title, parent, slug, note, to, as, every, until, next *string
	all, asJSON                                                                      *bool
	tail                                                                             *int
}

func parseTaskCommand(args []string) (*taskCommand, error) {
	fs := commandFlags("task " + args[0])
	c := &taskCommand{name: args[0],
		root:   fs.String("root", "", "記録のリポジトリー"),
		state:  fs.String("state", "", "待ちの登録状況や会話の最終利用を点検するときの、サーバーの状態ディレクトリー"),
		status: fs.String("status", "", "表示する状態（複数ならコンマで区切ります）"),
		area:   fs.String("area", "", "領域"),
		all:    fs.Bool("all", false, "完了したタスクと取りやめたタスクも表示します"),
		tail:   fs.Int("tail", 0, "履歴の末尾から表示する行数"),
		asJSON: fs.Bool("json", false, "list と show では JSON で表示します。log では履歴の各行をそのまま表示します"),
		title:  fs.String("title", "", "題名"),
		parent: fs.String("parent", "", "親のタスクのID"),
		slug:   fs.String("slug", "", "ディレクトリー名の末尾に付ける名前（英小文字、数字、ハイフン）"),
		note:   fs.String("note", "", "履歴に残す補足"),
		to:     fs.String("to", task.FilesDir, "成果物の置き場所。files は Git の管理外に置き、artifacts は Git に保存します"),
		as:     fs.String("as", "", "成果物に付ける名前（省くと元のファイル名を使います）"),
		every:  fs.String("every", "", "繰り返し確かめる間隔です。1d や 3h のように、日か時間で指定します"),
		until:  fs.String("until", "", "繰り返しの確認をいつまで続けるかを、日付か日時で指定します"),
		next:   fs.String("next", "", "最初に確かめる日時です。省くと、いまから --every で指定した間隔のあとに確かめます"),
	}
	// Flags may come before or after the positional arguments.
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		c.positional = append(c.positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	var err error
	if c.dir, err = findRoot(*c.root); err != nil {
		return nil, err
	}
	if *c.root != "" {
		task.SetOwner(c.dir)
	}
	c.api = apiForRoot(c.dir)
	return c, nil
}

func (c *taskCommand) need(n int, what string) error {
	if len(c.positional) < n {
		return errors.New(what + "を指定してください")
	}
	return nil
}

// readFromServer shows the list and a task from the server on a read-only
// copy, which lags behind it. It reports false when the records here are to
// be shown instead.
func (c *taskCommand) readFromServer() (bool, error) {
	switch c.name {
	case "list":
		var tasks []task.Task
		if err := c.api.readTask("/api/tasks", &tasks); err == nil {
			return true, outputTaskList(tasks, *c.status, *c.area, *c.all, *c.asJSON)
		} else if !warnLocalTaskRead(err) {
			return true, err
		}
	case "show":
		if err := c.need(1, "ID"); err != nil {
			return true, err
		}
		var detail task.Detail
		err := c.api.readTask("/api/tasks/"+url.PathEscape(c.positional[0])+"?detail=1", &detail)
		if err == nil && detail.Markdown != "" {
			if *c.asJSON {
				return true, json.NewEncoder(os.Stdout).Encode(detail)
			}
			printTaskDetail(detail)
			return true, nil
		}
		if !warnLocalTaskRead(err) {
			return true, err
		}
	}
	return false, nil
}

// open reads the records here and finds who the history names.
func (c *taskCommand) open() error {
	if err := requireCurrentRecords(c.dir); err != nil {
		return err
	}
	store, err := task.Open(c.dir)
	if err != nil {
		return err
	}
	c.store = store
	for _, p := range store.Problems() {
		fmt.Fprintf(os.Stderr, "読み込めないタスクがあります: %s: %s\n", p.Path, p.Err)
	}
	// Without a server, the waits and handovers it keeps are read from its
	// state directory when the caller says where that is.
	store.SetLiveCondition(func(id string) bool {
		live, err := localWaitConditions(*c.state, false)
		return err == nil && live[id]
	})
	// The server names the conversation itself; only an explicit name is
	// sent. Without a server, the name comes from the caller found here.
	c.actor = os.Getenv("LACIA_ACTOR")
	c.owner = task.Owner{Label: me().Name(), Session: me().Key()}
	if c.actor == "" && c.api == "" {
		if c.actor = c.owner.Label; c.actor == "" {
			c.actor = "コマンド"
		}
	}
	return nil
}

func rank(s task.Status) int {
	for i, v := range task.Statuses {
		if v == s {
			return i
		}
	}
	return len(task.Statuses)
}

// pad right-pads to a display width, counting full-width characters as two.
func pad(s string, width int) string {
	w := 0
	for _, r := range s {
		if r > 0x7f {
			w += 2
		} else {
			w++
		}
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// warnLocalTaskRead keeps a server-side not found or permission decision authoritative.
func warnLocalTaskRead(err error) bool {
	var status *apiStatusError
	if errors.As(err, &status) && status.code >= http.StatusBadRequest && status.code < http.StatusInternalServerError {
		return false
	}
	fmt.Fprintln(os.Stderr, "サーバーから最新のタスクを読めなかったため、手元にある記録を表示します。")
	return true
}

func outputTaskList(tasks []task.Task, statuses, area string, all, asJSON bool) error {
	selected := []task.Task{}
	wanted := map[task.Status]bool{}
	for _, s := range strings.Split(statuses, ",") {
		if s = strings.TrimSpace(s); s != "" {
			if !task.Status(s).Valid() {
				return fmt.Errorf("%q という状態はありません", s)
			}
			wanted[task.Status(s)] = true
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return rank(tasks[i].Status) < rank(tasks[j].Status) })
	for _, t := range tasks {
		switch {
		case area != "" && t.Area != area:
		case len(wanted) > 0 && !wanted[t.Status]:
		case len(wanted) == 0 && !all && t.Status.Closed():
		default:
			selected = append(selected, t)
			if asJSON {
				continue
			}
			line := fmt.Sprintf("%-6s %s  %s", t.ID, pad(t.Status.Label(), 8), t.Title)
			if t.Status == task.Doing && t.Owner != "" {
				line += "（" + t.Owner + "）"
			}
			fmt.Println(line)
		}
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(selected)
	}
	return nil
}

func printTaskDetail(detail task.Detail) {
	if detail.Routine {
		fmt.Printf("定期タスク「%s」の記録です。題名と定義は routine.md にあります。\n\n", detail.Title)
	}
	fmt.Print(detail.Markdown)
	fmt.Printf("\n場所: %s\n", detail.Dir)
	artifacts := false
	for _, name := range detail.Files {
		if !strings.HasPrefix(name, task.ArtifactsDir+"/") {
			fmt.Printf("ほかのファイル: %s\n", name)
			continue
		}
		if !artifacts {
			fmt.Println("成果物:")
			artifacts = true
		}
		fmt.Println("  " + name)
	}
	if len(detail.Children) > 0 {
		fmt.Println("子のタスク:")
		for _, child := range detail.Children {
			fmt.Printf("  %-6s %s  %s\n", child.ID, pad(child.Status.Label(), 8), child.Title)
		}
	}
}

// resumeReminder is the line shown after writing to a task that is not marked
// as being worked on. Notes and artifacts usually mean that work has resumed,
// and nothing else points out that the status still says whose turn it was
// before. The status is left alone: a note on a waiting task can be only a note.
func resumeReminder(t task.Task) string {
	// A routine's task waits for the next run between pieces of work.
	if t.Routine && t.Status == task.Waiting {
		return ""
	}
	switch t.Status {
	case task.Todo, task.Waiting, task.Review, task.Closing:
		return fmt.Sprintf("%s は「%s」のままです。これから作業を始めるときや、途中の作業を再開したときは、`lacia task set %s doing` を実行してください。", t.ID, t.Status.Label(), t.ID)
	}
	return ""
}

// closingReminder is the line shown after a task was moved to review. The
// two ways of waiting on the user are easy to mix up when work is handed
// over, and a task that only asks whether it may be closed belongs in
// "closing".
func closingReminder(t task.Task) string {
	if t.Status != task.Review || t.Routine {
		return ""
	}
	return fmt.Sprintf("お願いが「これで完了にしてよいか」の確認だけのときは、`lacia task set %s closing` で「完了確認待ち」にしてください。判断、回答、操作のお願いが残るときは、このままで構いません。", t.ID)
}

// updateReply is what the server says after a change.
type updateReply struct {
	Task           task.Task
	Changed        bool
	Warning        string
	WaitingWarning string `json:"waiting_warning"`
}

func printWaitingGuidance(id, warning string) {
	fmt.Fprintln(os.Stderr, "注意: "+warning)
	fmt.Fprintf(os.Stderr, "外部の変化を繰り返し確かめながら待つ場合は `lacia task recheck %s --every 1d --until 期限`、日時まで待つ場合は `lacia task field %s resume_on 日時`、ほかのタスクを待つ場合は `lacia task field %s after タスクID`、次の定期実行を待つ場合は `lacia task field %s after_run 定期タスクID` で登録できます。判断や操作をお願いする場合は、`lacia task set %s review` で要対応に出します。\n", id, id, id, id, id)
}

// warn passes on what the server noticed, such as another conversation
// working on the task.
func (r updateReply) warn() {
	if r.Warning != "" {
		fmt.Fprintln(os.Stderr, "注意: "+r.Warning)
	}
}

// noteActor is who wrote a note, unless it was the plain command.
func noteActor(actor string) string {
	if actor == "コマンド" {
		return ""
	}
	return actor
}

func warnOwned(t task.Task) {
	fmt.Fprintf(os.Stderr, "注意: %s は、いま %s が進めています。引き継ぐときは、lacia task claim %s を実行してください。\n", t.ID, t.Owner, t.ID)
}

// remindToResume prints resumeReminder for the task. It reads the status from
// the server when the command runs on a read-only copy, because the copy lags.
func remindToResume(api apiClient, store *task.Store, ref string) {
	var t task.Task
	if api != "" {
		if api.call("GET", "/api/tasks/"+ref, nil, &t) != nil {
			return
		}
	} else {
		var err error
		if t, err = store.Get(ref); err != nil {
			return
		}
	}
	if line := resumeReminder(t); line != "" {
		fmt.Println(line)
	}
}
