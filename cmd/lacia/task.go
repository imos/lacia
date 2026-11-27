package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

func runTask(args []string) error {
	if len(args) == 0 {
		return errors.New("task のあとに list、show、log、new、set、claim、field、recheck、rechecked、note、write、attach、lint、path のどれかを指定してください")
	}
	fs := commandFlags("task " + args[0])
	root := fs.String("root", "", "記録のリポジトリー")
	state := fs.String("state", "", "待ちの登録状況や会話の最終利用を点検するときの、サーバーの状態ディレクトリー")
	status := fs.String("status", "", "表示する状態（複数ならコンマで区切ります）")
	area := fs.String("area", "", "領域")
	all := fs.Bool("all", false, "完了したタスクと取りやめたタスクも表示します")
	tail := fs.Int("tail", 0, "履歴の末尾から表示する行数")
	asJSON := fs.Bool("json", false, "list と show では JSON で表示します。log では履歴の各行をそのまま表示します")
	title := fs.String("title", "", "題名")
	parent := fs.String("parent", "", "親のタスクのID")
	slug := fs.String("slug", "", "ディレクトリー名の末尾に付ける名前（英小文字、数字、ハイフン）")
	note := fs.String("note", "", "履歴に残す補足")
	to := fs.String("to", task.FilesDir, "成果物の置き場所。files は Git の管理外に置き、artifacts は Git に保存します")
	as := fs.String("as", "", "成果物に付ける名前（省くと元のファイル名を使います）")
	every := fs.String("every", "", "繰り返し確かめる間隔です。1d や 3h のように、日か時間で指定します")
	until := fs.String("until", "", "繰り返しの確認をいつまで続けるかを、日付か日時で指定します")
	next := fs.String("next", "", "最初に確かめる日時です。省くと、いまから --every で指定した間隔のあとに確かめます")
	// Flags may come before or after the positional arguments.
	var positional []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	if *root != "" {
		task.SetOwner(dir)
	}
	api := apiForRoot(dir)
	need := func(n int, what string) error {
		if len(positional) < n {
			return errors.New(what + "を指定してください")
		}
		return nil
	}

	if readOnlyCopy(dir) {
		switch args[0] {
		case "list":
			var tasks []task.Task
			if err := api.readTask("/api/tasks", &tasks); err == nil {
				return outputTaskList(tasks, *status, *area, *all, *asJSON)
			} else if !warnLocalTaskRead(err) {
				return err
			}
		case "show":
			if err := need(1, "ID"); err != nil {
				return err
			}
			var detail task.Detail
			err := api.readTask("/api/tasks/"+url.PathEscape(positional[0])+"?detail=1", &detail)
			if err == nil && detail.Markdown != "" {
				if *asJSON {
					return json.NewEncoder(os.Stdout).Encode(detail)
				}
				printTaskDetail(detail)
				return nil
			}
			if !warnLocalTaskRead(err) {
				return err
			}
		}
	}

	if err := requireCurrentRecords(dir); err != nil {
		return err
	}
	store, err := task.Open(dir)
	if err != nil {
		return err
	}
	for _, p := range store.Problems() {
		fmt.Fprintf(os.Stderr, "読み込めないタスクがあります: %s: %s\n", p.Path, p.Err)
	}
	// Without a server, the waits and handovers it keeps are read from its
	// state directory when the caller says where that is.
	store.SetLiveCondition(func(id string) bool {
		live, err := localWaitConditions(*state, false)
		return err == nil && live[id]
	})
	// The server names the conversation itself; only an explicit name is
	// sent. Without a server, the name comes from the caller found here.
	actor := os.Getenv("LACIA_ACTOR")
	owner := task.Owner{Label: me().Name(), Session: me().Key()}
	if actor == "" && api == "" {
		if actor = owner.Label; actor == "" {
			actor = "コマンド"
		}
	}

	switch args[0] {
	case "list":
		return outputTaskList(store.List(), *status, *area, *all, *asJSON)
	case "show":
		if err := need(1, "ID"); err != nil {
			return err
		}
		t, err := store.Get(positional[0])
		if err != nil {
			return err
		}
		detail, err := store.Detail(t)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(detail)
		}
		printTaskDetail(detail)
	case "log":
		if err := need(1, "ID"); err != nil {
			return err
		}
		t, err := store.Get(positional[0])
		if err != nil {
			return err
		}
		if *asJSON {
			events, err := task.ReadEvents(filepath.Join(dir, t.Dir, task.HistoryFile))
			if err != nil {
				return err
			}
			if *tail > 0 && len(events) > *tail {
				events = events[len(events)-*tail:]
			}
			for _, e := range events {
				fmt.Println(e.Raw)
			}
			return nil
		}
		lines, err := store.HistoryLines(t, *tail)
		if err != nil {
			return err
		}
		fmt.Println(strings.Join(lines, "\n"))
	case "new":
		if api != "" {
			var t task.Task
			if err := api.call("POST", "/api/tasks", map[string]string{"area": *area, "title": *title, "status": *status, "parent": *parent, "slug": *slug, "actor": actor}, &t); err != nil {
				return err
			}
			fmt.Printf("%s を作りました: %s/task.md\n", t.ID, t.Dir)
			return nil
		}
		t, err := store.Create(task.NewTask{Area: *area, Title: *title, Status: task.Status(*status), Parent: *parent, Slug: *slug, Actor: actor})
		if err != nil {
			return err
		}
		fmt.Printf("%s を作りました: %s/task.md\n", t.ID, t.Dir)
	case "set":
		if err := need(2, "IDと状態"); err != nil {
			return err
		}
		var t task.Task
		var changed bool
		var waitingWarning string
		if api != "" {
			var out updateReply
			if err := api.call("POST", "/api/tasks/"+positional[0], map[string]string{"status": positional[1], "note": *note, "actor": actor}, &out); err != nil {
				return err
			}
			t, changed = out.Task, out.Changed
			out.warn()
			waitingWarning = out.WaitingWarning
		} else {
			var err error
			if before, _ := store.Get(positional[0]); positional[1] != string(task.Doing) || owner.Session == "" {
				t, changed, err = store.SetStatus(positional[0], task.Status(positional[1]), actor, *note)
			} else if before.Status == task.Doing && before.OwnerSession != "" && before.OwnerSession != owner.Session {
				t = before
				warnOwned(before)
			} else {
				t, changed, err = store.Claim(positional[0], owner, actor, *note)
			}
			if err != nil {
				return err
			}
			if t.MissingWaitingCondition(false) {
				live, err := localWaitConditions(*state, true)
				if err != nil {
					fmt.Fprintln(os.Stderr, "再開条件の登録状況を確かめられませんでした。", err)
				} else if !live[t.ID] {
					waitingWarning = task.WaitingConditionWarning
				}
			}
		}
		if waitingWarning != "" {
			printWaitingGuidance(t.ID, waitingWarning)
		}
		if !changed {
			fmt.Printf("%s はすでに「%s」です。\n", t.ID, t.Status.Label())
			return nil
		}
		fmt.Printf("%s を「%s」にしました。\n", t.ID, t.Status.Label())
		if line := closingReminder(t); line != "" {
			fmt.Println(line)
		}
	case "claim":
		if err := need(1, "ID"); err != nil {
			return err
		}
		var t task.Task
		var changed bool
		if api != "" {
			var out updateReply
			if err := api.call("POST", "/api/tasks/"+positional[0], map[string]any{"claim": true, "note": *note, "actor": actor}, &out); err != nil {
				return err
			}
			t, changed = out.Task, out.Changed
		} else {
			var err error
			if owner.Session == "" {
				return errors.New("担当にする会話が分かりません。Codex か Claude の中から実行してください")
			}
			if t, changed, err = store.Claim(positional[0], owner, actor, *note); err != nil {
				return err
			}
		}
		if !changed {
			fmt.Printf("%s は、すでにこの会話（%s）が進めています。\n", t.ID, t.Owner)
			return nil
		}
		fmt.Printf("%s の担当を、この会話（%s）にしました。\n", t.ID, t.Owner)
	case "field":
		if err := need(2, "IDと項目"); err != nil {
			return err
		}
		value := strings.Join(positional[2:], " ")
		if api != "" {
			var out updateReply
			if err := api.call("POST", "/api/tasks/"+positional[0], map[string]string{"field": positional[1], "value": value, "actor": actor}, &out); err != nil {
				return err
			}
			out.warn()
		} else {
			if positional[1] == "after_run" {
				var err error
				value, err = schedule.ResolveWait(dir, value)
				if err != nil {
					return err
				}
			}
			if _, _, err := store.SetField(positional[0], positional[1], value, actor); err != nil {
				return err
			}
		}
		fmt.Printf("%s の %s を更新しました。\n", strings.ToUpper(positional[0]), positional[1])
	case "recheck":
		// Registers how often and until when a waiting task is looked at.
		// The server keeps the time of the next look.
		if err := need(1, "ID"); err != nil {
			return err
		}
		var t task.Task
		var changed bool
		if api != "" {
			var out updateReply
			if err := api.call("POST", "/api/tasks/"+positional[0], map[string]any{"recheck": map[string]string{"every": *every, "until": *until, "next": *next}, "actor": actor}, &out); err != nil {
				return err
			}
			t, changed = out.Task, out.Changed
			out.warn()
		} else {
			var err error
			if t, changed, err = store.SetRecheck(positional[0], *every, *until, *next, actor); err != nil {
				return err
			}
		}
		if !changed {
			fmt.Printf("%s には、同じ確認がすでに登録されています。\n", t.ID)
			return nil
		}
		fmt.Printf("%s を外部待ちにしました。確認の予定は「%s」です。最初の確認は %s です。\n", t.ID, t.RecheckLabel(), t.ResumeOn)
		fmt.Println("「次にすること」に、何を確かめるのかと、何が起きたらお知らせするのかを書いてください。確認を担当する会話は、その記述をもとに確かめます。")
	case "rechecked":
		// Records what one look at a waiting task found.
		if err := need(3, "ID、結果（same、changed、unknown のどれか）、何を確かめてどうだったのか"); err != nil {
			return err
		}
		text := strings.Join(positional[2:], " ")
		var t task.Task
		if api != "" {
			var out updateReply
			if err := api.call("POST", "/api/tasks/"+positional[0], map[string]string{"rechecked": positional[1], "text": text, "actor": actor}, &out); err != nil {
				return err
			}
			t = out.Task
			out.warn()
		} else {
			var err error
			if t, _, err = store.Rechecked(positional[0], positional[1], text, actor); err != nil {
				return err
			}
		}
		switch positional[1] {
		case task.RecheckChanged:
			fmt.Printf("%s の結果を「変化あり」と記録して、確認待ちにしました。繰り返しの確認は止まりました。\n", t.ID)
		case task.RecheckUnknown:
			fmt.Printf("%s の結果を「確かめられなかった」と記録しました。外部待ちを続けます。\n%s\n", t.ID, t.RecheckState())
		default:
			fmt.Printf("%s の結果を「変化なし」と記録しました。外部待ちを続けます。\n%s\n", t.ID, t.RecheckState())
		}
	case "attach":
		if err := need(2, "IDと、置くファイルかディレクトリー"); err != nil {
			return err
		}
		// A directory is sent with its files under its own name, so that an
		// HTML page keeps working with the images and scripts next to it.
		type item struct{ path, name string }
		var items []item
		for _, src := range positional[1:] {
			info, err := os.Stat(src)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				name := filepath.Base(src)
				if *as != "" && len(positional) == 2 {
					name = *as
				}
				items = append(items, item{src, name})
				continue
			}
			prefix := filepath.Base(filepath.Clean(src))
			if *as != "" && len(positional) == 2 {
				prefix = *as
			}
			err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
					return err
				}
				rel, _ := filepath.Rel(src, p)
				items = append(items, item{p, filepath.ToSlash(filepath.Join(prefix, rel))})
				return nil
			})
			if err != nil {
				return err
			}
		}
		for _, it := range items {
			f, err := os.Open(it.path)
			if err != nil {
				return err
			}
			if api != "" {
				err = api.put("/api/tasks/"+positional[0]+"/files?"+url.Values{"to": {*to}, "name": {it.name}, "actor": {actor}}.Encode(), f)
			} else {
				_, err = store.PutFile(positional[0], *to, it.name, f, actor)
			}
			f.Close()
			if err != nil {
				return err
			}
			fmt.Printf("%s/%s を置きました。\n", *to, it.name)
		}
		remindToResume(api, store, positional[0])
	case "note":
		if err := need(2, "タスクのIDと履歴に書く文章"); err != nil {
			return err
		}
		entry := strings.Join(positional[1:], " ")
		if api != "" {
			var out updateReply
			if err := api.call("POST", "/api/tasks/"+positional[0], map[string]string{"log": entry, "actor": actor}, &out); err != nil {
				return err
			}
			out.warn()
		} else if err := store.Log(positional[0], task.Event{Type: task.EventNote, By: noteActor(actor), Text: strings.TrimSpace(entry)}); err != nil {
			return err
		}
		fmt.Println("履歴に書きました。")
		remindToResume(api, store, positional[0])
	case "write":
		// Replaces the body of task.md, or notes.md, with a file or with the
		// standard input. On a read-only copy this is how text is saved: the
		// server writes it, and no conversation has to type it in.
		if err := need(2, "タスクのIDと保存先のファイル名（task.md か notes.md）"); err != nil {
			return err
		}
		var content []byte
		var err error
		if len(positional) > 2 && positional[2] != "-" {
			content, err = os.ReadFile(positional[2])
		} else {
			content, err = io.ReadAll(os.Stdin)
		}
		if err != nil {
			return err
		}
		var t task.Task
		var advice []string
		if api != "" {
			var out struct {
				updateReply
				Advice []string
			}
			if err := api.putOut("/api/tasks/"+url.PathEscape(positional[0])+"/doc?"+url.Values{"name": {positional[1]}, "actor": {actor}}.Encode(), bytes.NewReader(content), &out); err != nil {
				return err
			}
			t, advice = out.Task, out.Advice
			out.warn()
		} else {
			if before, _ := store.Get(positional[0]); before.Status == task.Doing && before.OwnerSession != "" && owner.Session != "" && before.OwnerSession != owner.Session {
				warnOwned(before)
			}
			if t, err = store.WriteDoc(positional[0], positional[1], string(content), actor); err != nil {
				return err
			}
			if positional[1] == task.TaskFile {
				for _, w := range append(task.CheckFormat(t, store.Files(t), store.Format()), task.LintFormat(t.Body, store.Format())...) {
					advice = append(advice, w.String())
				}
			}
		}
		if api == "" && positional[1] == task.NotesFile {
			for _, w := range task.LintDocument("notes", string(content)) {
				advice = append(advice, w.String())
			}
		}
		fmt.Printf("%s の %s を保存しました。\n", t.ID, positional[1])
		for _, line := range advice {
			fmt.Printf("%s: %s\n", t.ID, line)
		}
		if line := resumeReminder(t); line != "" {
			fmt.Println(line)
		}
	case "lint":
		context, err := stateLintContext(*state)
		if err != nil {
			return err
		}
		byID := map[string]task.Task{}
		for _, t := range store.List() {
			byID[t.ID] = t
		}
		var targets []task.Task
		if len(positional) == 0 {
			for _, t := range store.List() {
				if !t.Status.Closed() {
					targets = append(targets, t)
				}
			}
		}
		for _, ref := range positional {
			t, err := store.Get(ref)
			if err != nil {
				return err
			}
			targets = append(targets, t)
		}
		waitingWarnings, waitingErr := lintWaitingConditions(api, *state, targets)
		count := 0
		if waitingErr != nil {
			fmt.Println("再開条件の登録状況を確かめられませんでした。", waitingErr)
			count++
		}
		for _, t := range targets {
			if advice := waitingWarnings[t.ID]; advice != "" {
				fmt.Printf("%s: %s\n", t.ID, advice)
				count++
			}
			for _, w := range task.StateWarnings(t, byID, context) {
				fmt.Printf("%s: %s\n", t.ID, w)
				count++
			}
			for _, w := range append(task.CheckFormat(t, store.Files(t), store.Format()), task.LintFormat(t.Body, store.Format())...) {
				fmt.Printf("%s: %s\n", t.ID, w)
				count++
			}
			for _, w := range task.LintNotesFile(filepath.Join(dir, t.Dir, "notes.md")) {
				fmt.Printf("%s notes.md: %s\n", t.ID, w)
				count++
			}
			for _, key := range t.Unknown {
				if strings.HasPrefix(key, "x_") {
					continue
				}
				fmt.Printf("%s: 冒頭の %s は定義されていない項目です。独自に加える項目の名前は x_ で始めてください。\n", t.ID, key)
				count++
			}
		}
		if len(positional) == 0 {
			for _, problem := range store.DependencyProblems() {
				fmt.Println(problem)
				count++
			}
		}
		for _, t := range targets {
			for _, id := range t.After {
				if _, err := store.Get(id); err != nil {
					fmt.Printf("%s: after にある %s というタスクはありません。このままでは着手できません。\n", t.ID, id)
					count++
				}
			}
		}
		if count == 0 {
			fmt.Println("気になる箇所はありませんでした。")
		}
	case "path":
		if err := need(1, "ID"); err != nil {
			return err
		}
		t, err := store.Get(positional[0])
		if err != nil {
			return err
		}
		fmt.Println(filepath.Join(dir, t.Dir))
	default:
		return fmt.Errorf("task %s というコマンドはありません", args[0])
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

func printTaskList(tasks []task.Task, statuses, area string, all bool) error {
	return outputTaskList(tasks, statuses, area, all, false)
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

// remindToResume prints resumeReminder for the task. It reads the status from
// the server when the command runs on a read-only copy, because the copy lags.
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
