package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/imos/lacia/internal/self"
)

// runNotice jots down something that caught Lacia's attention. It does not
// change her view at once: the nightly rewrite reads the notes and decides.
func runNotice(args []string) error {
	fs := commandFlags("notice")
	taskID := fs.String("task", "", "そのとき扱っていたタスクのID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if text == "-" || text == "" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return err
		}
		text = string(data)
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	if err := apiCall("POST", "/api/self/notes", map[string]string{"text": text, "task": *taskID}, nil); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "書き留めました。")
	return nil
}

type selfState struct {
	Document string
	Notes    []self.Note
	Nights   []self.Night
}

// runSelf shows her view, or starts the rewrite now.
func runSelf(args []string) error {
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	var got selfState
	if len(args) == 0 || args[0] == "show" {
		if err := apiCall("GET", "/api/self", nil, &got); err != nil {
			return err
		}
		if got.Document == "" {
			fmt.Println("自分についての記録はまだ書かれていません。")
		} else {
			fmt.Println(strings.TrimSpace(got.Document))
		}
		if len(got.Notes) > 0 {
			fmt.Print("\n## 書き直しに使う書き留め\n\n")
			for _, n := range got.Notes {
				who := ""
				if n.From != "" {
					who = n.From + ": "
				}
				fmt.Printf("- %s　%s%s\n", n.At.In(commandLocation()).Format("15:04"), who, n.Text)
			}
		}
		return nil
	}
	if len(args) == 2 && args[0] == "tidy" {
		return showTidyDay(args[1])
	}
	if args[0] == "improve" {
		return runSelfImprove(args[1:])
	}
	if args[0] == "dates" && len(args) == 1 {
		var out struct{ Changed bool }
		if err := apiCall("POST", "/api/self/dates", nil, &out); err != nil {
			return err
		}
		if out.Changed {
			fmt.Println("項目に日付を付けました。本文は変えていません。")
		} else {
			fmt.Println("どの項目にも日付がありました。")
		}
		return nil
	}
	if args[0] != "sleep" {
		return usageError("self")
	}
	fs := commandFlags("self sleep")
	force := fs.Bool("force", false, "書き留めなどの材料がない日も自分についての記録を書き直します")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	started := time.Now()
	if err := apiCall("POST", "/api/self/sleep", map[string]bool{"force": *force}, nil); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "書き直しを始めました。数分かかります。")
	for deadline := started.Add(65 * time.Minute); time.Now().Before(deadline); {
		time.Sleep(5 * time.Second)
		if err := apiCall("GET", "/api/self", nil, &got); err != nil {
			return err
		}
		if len(got.Nights) == 0 || got.Nights[0].At.Before(started.Add(-time.Second)) {
			continue
		}
		switch night := got.Nights[0]; {
		case night.Accepted:
			fmt.Println("自分についての記録を書き直しました。" + night.Summary)
			if night.Sharing != "" {
				fmt.Println(night.Sharing)
			}
			return nil
		case night.Skipped:
			fmt.Println("今回は書き直す材料がなかったので、自分についての記録の更新を見送りました。")
			return nil
		default:
			return errors.New("今回は「わたしのこと」の書き直しを見送りました。" + night.Reason)
		}
	}
	return errors.New("夜の処理を65分待ちましたが、結果はまだ分かりません。Web の「レイシアのこと」で確かめてください")
}

// showTidyDay reads the archived topic and candidates from the current server.
func showTidyDay(day string) error {
	if parsed, err := time.Parse(time.DateOnly, day); err != nil || parsed.Format(time.DateOnly) != day {
		return errors.New("日付は YYYY-MM-DD の形で指定してください")
	}
	var got self.TidyDay
	if err := apiCall("GET", "/api/self/tidy/"+day, nil, &got); err != nil {
		return err
	}
	fmt.Printf("# %s の夜の整理\n", got.Day)
	if got.Topic != "" {
		fmt.Printf("\n## 話題\n\n%s\n", got.Topic)
	}
	for _, section := range []struct {
		name  string
		items []string
	}{{"覚えておく候補", got.Items}, {"1週間の見直しの候補", got.Review}} {
		if len(section.items) > 0 {
			fmt.Printf("\n## %s\n\n", section.name)
			for _, item := range section.items {
				fmt.Printf("- %s\n", item)
			}
		}
	}
	return nil
}

// runSelfImprove lists the candidates for mending the mechanism, or records
// what the conversation made of one.
func runSelfImprove(args []string) error {
	if len(args) == 0 {
		var got struct{ Improvements []self.Improvement }
		if err := apiCall("GET", "/api/self/improvements", nil, &got); err != nil {
			return err
		}
		if len(got.Improvements) == 0 {
			fmt.Println("仕組みで直せそうなことの候補は、まだありません。")
			return nil
		}
		for _, c := range got.Improvements {
			state := "タスクにするか、まだ決まっていません"
			switch c.Outcome {
			case self.ImproveTask:
				state = "タスク " + c.Task + " になりました"
			case self.ImproveSkipped:
				state = "見送りました。" + c.Note
			}
			fmt.Printf("- %s　%s　%s（%s）\n", c.ID, c.Day, c.Title, state)
			if c.Outcome == "" {
				for _, part := range []struct{ label, text string }{{"もとになった項目", c.Item}, {"仕組みで直せると考えた理由", c.Reason}, {"直し方の見当", c.Fix}} {
					if part.text != "" {
						fmt.Printf("    %s: %s\n", part.label, part.text)
					}
				}
			}
		}
		return nil
	}
	usage := errors.New("使い方: lacia self improve、lacia self improve <候補のID> task <タスクのID>、lacia self improve <候補のID> skip \"理由\"")
	if len(args) < 3 {
		return usage
	}
	in := map[string]string{}
	switch args[1] {
	case "task":
		in["outcome"], in["task"] = self.ImproveTask, args[2]
	case "skip":
		in["outcome"], in["note"] = self.ImproveSkipped, strings.Join(args[2:], " ")
	default:
		return usage
	}
	var got self.Improvement
	if err := apiCall("POST", "/api/self/improvements/"+args[0], in, &got); err != nil {
		return err
	}
	if got.Outcome == self.ImproveTask {
		fmt.Fprintf(os.Stderr, "候補 %s の対応をタスク %s で進めることを記録しました。\n", got.ID, got.Task)
	} else {
		fmt.Fprintf(os.Stderr, "候補 %s を見送ったことを記録しました。\n", got.ID)
	}
	return nil
}
