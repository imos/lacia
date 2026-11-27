package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

const waitUsage = `使い方:
  lacia wait add --task ID (--job <context>/<名前空間>/<Job名> | --file <context>/<名前空間>/<Pod>:<パス>)
                 --by <期限> [--next <宛先|owner|none>] <終わったらすること>
  lacia wait list [--task ID] [--all]
  lacia wait show <待ちか申し送りのID>
  lacia wait ack <待ちか申し送りのID>       続きを受け取ったことを知らせます。会話を特定できたときは、その会話がタスクの担当になります。
  lacia wait cancel <待ちか申し送りのID>

期限は、いまから待つ時間（6h、90m など）か、日時（2026-11-19 06:00 など）で指定します。
--file では、処理が最後に書くファイルを終了の印にします。先頭の空白や改行を除いて fail で始まると、英字の大小を問わず失敗と判断します。
--next を省くと、登録した会話と同じ場所、同じ種類の会話に続きを届けます。登録した会話を特定できなければ、続きの会話は始めず、終了待ちの結果が出た時点で外部待ちのタスクを「確認待ち」にします。--next none を指定したときも同じです。`

// waitFlags are shared by `wait add` and `job run`.
type waitFlags struct {
	task, by, next *string
}

func addWaitFlags(fs *flag.FlagSet) waitFlags {
	return waitFlags{
		task: fs.String("task", "", "終了待ちを登録するタスクのID"),
		by:   fs.String("by", "", "処理の終了を待つ期限（6h、2026-11-19 06:00 など）"),
		next: fs.String("next", "", "続きを届ける宛先（例: local-codex）。owner を指定すると、登録した会話と同じ場所、同じ種類の会話に届けます。続きの会話を始めないときは none を指定します"),
	}
}

// readThen is the text after the flags, or standard input for "-".
func readThen(args []string) (string, error) {
	text := strings.Join(args, " ")
	if text == "-" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	return strings.TrimSpace(text), nil
}

func registerWait(in map[string]string) error {
	var out struct {
		Wait  wait.Wait `json:"wait"`
		Added bool      `json:"added"`
	}
	if err := apiCall("POST", "/api/waits", in, &out); err != nil {
		return err
	}
	w := out.Wait
	if !out.Added {
		fmt.Printf("同じ対象の終了待ち（%s）がすでにあります。\n", w.ID)
		return nil
	}
	fmt.Printf("%s の終了を待ちます。待ちのIDは %s、期限は %s です。\n", w.Target(), w.ID, task.FormatWhenIn(w.By, commandLocation()))
	fmt.Printf("タスク %s は「外部待ち」です。この会話で待ち続ける必要はありません。\n", w.TaskID)
	switch w.Next {
	case wait.NextNone:
		fmt.Printf("処理の終了を確かめたとき、対象が見つからなくなったとき、期限を過ぎたときは、外部待ちのタスク %s を「確認待ち」にします。続きの会話は始めません。\n", w.TaskID)
	default:
		fmt.Printf("処理の終了を確かめたら、%s の会話に続きを依頼します。対象が見つからなくなったとき、期限までに終了を確かめられなかったとき、宛先から受け取りの知らせがないときは、外部待ちのタスク %s を「確認待ち」にします。\n", w.Next, w.TaskID)
	}
	return nil
}

func runWait(args []string) error {
	if len(args) == 0 {
		return errors.New(waitUsage)
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	switch args[0] {
	case "add":
		fs := commandFlags("wait add")
		common := addWaitFlags(fs)
		job := fs.String("job", "", "待つ Job（<context>/<名前空間>/<Job名>）")
		file := fs.String("file", "", "処理の終了を知らせるファイル（<context>/<名前空間>/<Pod>:<パス>）")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		then, err := readThen(fs.Args())
		if err != nil {
			return err
		}
		in := map[string]string{"task": *common.task, "by": *common.by, "next": *common.next, "then": then}
		switch {
		case (*job == "") == (*file == ""):
			return errors.New("--job と --file のどちらか1つを指定してください")
		case *job != "":
			parts := strings.SplitN(*job, "/", 3)
			if len(parts) != 3 {
				return errors.New("--job は、<context>/<名前空間>/<Job名> の形で書いてください")
			}
			in["kind"], in["context"], in["namespace"], in["name"] = wait.Job, parts[0], parts[1], parts[2]
		default:
			target, path, ok := strings.Cut(*file, ":")
			parts := strings.SplitN(target, "/", 3)
			if !ok || len(parts) != 3 {
				return errors.New("--file は、<context>/<名前空間>/<Pod>:<パス> の形で書いてください。Pod は deploy/<名前> でも書けます")
			}
			in["kind"], in["context"], in["namespace"], in["name"], in["path"] = wait.File, parts[0], parts[1], parts[2], path
		}
		return registerWait(in)
	case "list":
		fs := commandFlags("wait list")
		taskID := fs.String("task", "", "表示する待ちと申し送りを、このタスクに絞ります")
		all := fs.Bool("all", false, "終わった待ちと申し送りも表示します")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path := "/api/waits?task=" + *taskID
		if *all {
			path += "&all=1"
		}
		var waits []wait.Wait
		if err := apiCall("GET", path, nil, &waits); err != nil {
			var status *apiStatusError
			if !errors.As(err, &status) || status.code != 404 {
				return err
			}
		}
		var handovers []handover.Handover
		if err := apiCall("GET", strings.Replace(path, "/api/waits", "/api/handovers", 1), nil, &handovers); err != nil {
			return err
		}
		fmt.Println("待ち:")
		if len(waits) == 0 {
			fmt.Println("待っているものはありません。")
		}
		for _, w := range waits {
			printWait(w, false)
		}
		fmt.Println("申し送り:")
		if len(handovers) == 0 {
			fmt.Println("申し送りはありません。")
		}
		for _, h := range handovers {
			printHandover(h, false)
		}
		return nil
	case "show", "ack", "cancel":
		if len(args) != 2 {
			return errors.New(waitUsage)
		}
		path := "/api/waits/" + args[1]
		if strings.HasPrefix(args[1], "H") {
			path = "/api/handovers/" + args[1]
		}
		if args[0] == "show" && !strings.HasPrefix(args[1], "H") {
			var raw json.RawMessage
			if err := apiCall("GET", path, nil, &raw); err != nil {
				return err
			}
			var w wait.Wait
			if err := json.Unmarshal(raw, &w); err != nil {
				return err
			}
			if strings.HasPrefix(w.ID, "H") {
				var h handover.Handover
				if err := json.Unmarshal(raw, &h); err != nil {
					return err
				}
				printHandover(h, true)
				return nil
			}
			printWait(w, true)
			if w.HandoverID != "" {
				var h handover.Handover
				if err := apiCall("GET", "/api/handovers/"+w.HandoverID, nil, &h); err != nil {
					return err
				}
				printHandover(h, true)
			}
			return nil
		}
		var h handover.Handover
		method := "GET"
		var in any
		if args[0] != "show" {
			method, in = "PATCH", map[string]bool{args[0]: true}
		}
		if err := apiCall(method, path, in, &h); err != nil {
			return err
		}
		switch args[0] {
		case "ack":
			if h.Acked.IsZero() {
				return fmt.Errorf("%s は、まだ受け取れないか、取り消されています", args[1])
			}
			fmt.Printf("申し送り %s の受け取りを記録しました。\n", h.ID)
		case "cancel":
			fmt.Printf("%s を取り消しました。タスクの状態は変えていません。\n", args[1])
		}
		if args[0] != "cancel" {
			printHandover(h, true)
		}
		return nil
	}
	return errors.New(waitUsage)
}

func printWait(w wait.Wait, full bool) {
	line := fmt.Sprintf("%s  %s  %s  ", w.ID, w.TaskID, w.Target())
	if w.State == wait.Waiting {
		line += w.SeenLabel()
		if !w.SeenAt.IsZero() {
			line += "（" + w.SeenAt.In(commandLocation()).Format("01-02 15:04") + " 時点）"
		}
		line += "。期限は " + task.FormatWhenIn(w.By, commandLocation()) + " です。"
	} else {
		line += w.OutcomeIn(commandLocation())
	}
	fmt.Println(line)
	if full {
		if w.Detail != "" {
			fmt.Println("  見えたこと: " + w.Detail)
		}
		fmt.Printf("  登録した会話: %s（%s）　続きを届ける宛先: %s\n", w.From, w.Created.In(commandLocation()).Format("2006-01-02 15:04"), w.Next)
		if w.Then != "" {
			fmt.Println("  終わったらすること: " + strings.ReplaceAll(w.Then, "\n", "\n    "))
		}
	}
}

func printHandover(h handover.Handover, full bool) {
	fmt.Printf("%s  %s  %s  %s\n", h.ID, h.TaskID, h.Target(), h.StatusText())
	if !full {
		return
	}
	fmt.Printf("  登録した会話: %s（%s）　届ける宛先: %s\n", h.From, h.Created.In(commandLocation()).Format("2006-01-02 15:04"), h.Next)
	if h.WaitID != "" {
		fmt.Println("  元の待ち: " + h.WaitID)
	}
	if h.Via != "" {
		fmt.Println("  届け方の記録: " + h.Via)
	}
	if action := h.RequestedAction(); action != "" {
		fmt.Println("  お願いすること: " + action)
	}
	if h.Then != "" {
		fmt.Println("  依頼: " + strings.ReplaceAll(h.Then, "\n", "\n    "))
	}
}

const tellUsage = `使い方:
  lacia tell --task ID [--to <宛先>] [--owner-action <必要な操作>] <依頼の文章|->

--to には local-claude のように、宛先の場所と会話の種類を指定します。
登録済みの端末の Codex に送るときは、動いている会話があればそこへ届けます。
サーバーを宛先にすると、サーバーが指定した種類の会話を起動します。
--to を省くと、Claude Code からは Codex に、Codex からは Claude Code に送ります。
相手は lacia wait ack で受け取りを知らせます。サーバーがその会話を特定できれば、タスクの担当が替わります。
20分たっても知らせがなければ、受け取りの遅れを記録します。
受け取りが遅れたとき、ご本人にお願いしたい操作があれば、--owner-action にその操作を書いてください。`

// runTell hands a request to the named conversation.
// The task's record stays the only thing the two conversations share: the
// request is stored with the task, and the receiver acknowledges it there.
func runTell(args []string) error {
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	fs := commandFlags("tell")
	taskID := fs.String("task", "", "依頼の対象のタスクのID")
	to := fs.String("to", "", "届ける宛先（例: local-claude）。省くと、Claude Code の会話からは Codex、Codex の会話からは Claude Code を選びます")
	ownerAction := fs.String("owner-action", "", "受け取りが遅れたとき、ご本人にお願いしたい操作")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if text == "" {
		return errors.New(tellUsage)
	}
	text, err := readThen(fs.Args())
	if err != nil {
		return err
	}
	var out struct {
		Handover handover.Handover `json:"handover"`
	}
	if err := apiCall("POST", "/api/handovers", map[string]string{"task": *taskID, "next": *to, "then": text, "owner_action": *ownerAction}, &out); err != nil {
		return err
	}
	w := out.Handover
	fmt.Printf("依頼 %s を預かりました。宛先は %s です。\n", w.ID, w.Next)
	fmt.Printf("lacia wait show %s で、申し送りの状況と、だれが受け取りを知らせたかを確かめられます。サーバーが受け手の会話を特定できれば、その会話がタスクを引き継ぎます。\n", w.ID)
	fmt.Println("20分たっても受け取りの知らせがなければ、遅れを記録します。")
	return nil
}

const jobUsage = `使い方:
  lacia job run --task ID --context <context> [-n <名前空間>] -f <Job の YAML>
                --by <期限> [--next <宛先|owner|none>] <終わったらすること>

kubectl apply で Job を投入して、その終了待ちを登録します。YAML には、Job を1つだけ書きます。`

// runJob submits a Job and registers the wait for it in one step, so that
// a job is never running without anybody set to notice its end.
func runJob(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New(jobUsage)
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	fs := commandFlags("job run")
	common := addWaitFlags(fs)
	kubeContext := fs.String("context", "", "kubectl のコンテキスト")
	var namespace, file string
	fs.StringVar(&namespace, "n", "", "Job の名前空間（省くと YAML の指定、それもなければコンテキストの既定値を使います）")
	fs.StringVar(&namespace, "namespace", "", "名前空間")
	fs.StringVar(&file, "f", "", "Job の YAML")
	kubectl := fs.String("kubectl", "kubectl", "kubectl のコマンド")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	then, err := readThen(fs.Args())
	if err != nil {
		return err
	}
	switch {
	case *common.task == "" || *kubeContext == "" || file == "" || *common.by == "":
		return errors.New(jobUsage)
	case *common.next != wait.NextNone && then == "":
		return errors.New("終わったら何をするのかを、最後に文章で書いてください")
	}
	// Refuse before submitting what could not be registered afterwards.
	var t task.Task
	if err := apiCall("GET", "/api/tasks/"+*common.task, nil, &t); err != nil {
		return err
	}
	if t.Status.Closed() {
		return fmt.Errorf("タスク %s はすでに完了したか、取りやめになっています", t.ID)
	}
	var destination struct{ Next string }
	if err := apiCall("GET", "/api/wait-destination?next="+url.QueryEscape(*common.next), nil, &destination); err != nil {
		return err
	}
	base := []string{"--context", *kubeContext}
	if namespace != "" {
		base = append(base, "-n", namespace)
	}
	// A client-side dry run shows what the file holds without submitting.
	jobs, err := kubectlJobs(*kubectl, append(base, "apply", "--dry-run=client", "-f", file, "-o", "json"))
	if err != nil {
		return err
	}
	if len(jobs) != 1 {
		return fmt.Errorf("%s には、Job が %d 個あります。Job を1つだけ書いた YAML を指定するか、kubectl で投入してから lacia wait add で登録してください", file, len(jobs))
	}
	jobs, err = kubectlJobs(*kubectl, append(base, "apply", "-f", file, "-o", "json"))
	if err != nil {
		return err
	}
	if len(jobs) != 1 || jobs[0].Namespace == "" {
		return fmt.Errorf("投入はしましたが、Job の名前を読み取れませんでした。lacia wait add --task %s --job ... で、終了待ちを登録してください", t.ID)
	}
	job := jobs[0]
	fmt.Printf("%s の %s に、Job %s を投入しました。\n", *kubeContext, job.Namespace, job.Name)
	err = registerWait(map[string]string{"task": t.ID, "kind": wait.Job, "context": *kubeContext, "namespace": job.Namespace, "name": job.Name, "by": *common.by, "next": destination.Next, "then": then})
	if err != nil {
		return fmt.Errorf("Job は投入しましたが、終了待ちを登録できませんでした: %w\nlacia wait add --task %s --job %s/%s/%s ... で登録し直してください", err, t.ID, *kubeContext, job.Namespace, job.Name)
	}
	return nil
}

type jobRef struct{ Name, Namespace string }

// kubectlJobs runs kubectl and returns the Jobs in its JSON output.
func kubectlJobs(kubectl string, args []string) ([]jobRef, error) {
	cmd := exec.Command(kubectl, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl %s が失敗しました: %s", strings.Join(args, " "), strings.TrimSpace(errOut.String()))
	}
	type object struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Items []json.RawMessage `json:"items"`
	}
	var refs []jobRef
	var visit func(raw []byte) error
	visit = func(raw []byte) error {
		var o object
		if err := json.Unmarshal(raw, &o); err != nil {
			return errors.New("kubectl の出力を読めませんでした")
		}
		if o.Kind == "Job" {
			refs = append(refs, jobRef{o.Metadata.Name, o.Metadata.Namespace})
		}
		for _, item := range o.Items {
			if err := visit(item); err != nil {
				return err
			}
		}
		return nil
	}
	// Several documents in one file come back as several JSON values.
	dec := json.NewDecoder(&out)
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, errors.New("kubectl の出力を読めませんでした")
		}
		if err := visit(raw); err != nil {
			return nil, err
		}
	}
	return refs, nil
}
