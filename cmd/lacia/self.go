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
	// The backfill reads and writes files on the machine it runs on.
	if len(args) > 0 && args[0] == "backfill" {
		return runSelfBackfill(args[1:])
	}
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
	if args[0] != "sleep" {
		return errors.New("使い方: lacia self [show]、lacia self tidy YYYY-MM-DD、lacia self sleep [--force]、lacia self backfill --state <ディレクトリー>")
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
			return errors.New("今回は新しい自分についての記録を採用しませんでした。" + night.Reason)
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
