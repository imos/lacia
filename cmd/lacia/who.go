package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/imos/lacia/internal/web"
)

// runWho prints which conversation this is, and which conversations the
// server has heard from lately with the tasks they are working on.
func runWho(_ []string) error {
	fmt.Println("この会話: " + me().Describe())
	if apiBase() == "" {
		fmt.Println("サーバーにつながっていないので、ほかの会話は分かりません。")
		return nil
	}
	var rows []web.SessionRow
	if err := apiCall("GET", "/api/sessions", nil, &rows); err != nil {
		return err
	}
	key := me().Key()
	fmt.Println("\nこの12時間にコマンドを使った会話と、進行中のタスクを受け持っている会話:")
	for _, row := range rows {
		line := fmt.Sprintf("%s  最後の記録 %s", pad(row.Label, 16), row.Last.In(commandLocation()).Format("01-02 15:04"))
		if row.Key == key {
			line += "  （この会話）"
		} else if !row.Active {
			line += fmt.Sprintf("  （%d時間、記録がありません）", int(time.Since(row.Last).Hours()))
		}
		fmt.Println(line)
		if row.Details != "" {
			fmt.Println("    " + row.Details)
		}
		for _, t := range row.Tasks {
			fmt.Printf("    %s %s\n", t.ID, t.Title)
		}
	}
	if len(rows) == 0 {
		fmt.Println("  ありません。")
	}
	return nil
}

const defaultBusyWindow = 5 * time.Minute

// runBusy says what a restart of the server would cut into: requests the
// agent has not finished, and other conversations that ran a command a
// moment ago. It fails when there is any, so that a script can stop there.
func runBusy(args []string) error {
	fs := commandFlags("busy")
	within := fs.Duration("within", defaultBusyWindow, "サーバーの会話一覧から、最近コマンドを使ったほかの会話を探す期間。0以下を指定すると既定の5分です")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	var busy web.Busy
	if err := apiCall("GET", "/api/busy?within="+url.QueryEscape(within.String()), nil, &busy); err != nil {
		return err
	}
	key, count := me().Key(), 0
	for _, r := range busy.Requests {
		count++
		label := "待っている依頼"
		if r.Status == "running" {
			label = "処理中の依頼"
		}
		line := fmt.Sprintf("%s: %s（%s に受け付けました）", label, r.ID, r.Created.In(commandLocation()).Format("15:04"))
		if r.TaskID != "" {
			line += " " + r.TaskID
		}
		fmt.Println(line)
	}
	for _, row := range busy.Sessions {
		if row.Key == key && me().Known() {
			continue
		}
		count++
		fmt.Printf("最近コマンドを使った会話: %s（最後の記録 %s）\n", row.Label, row.Last.In(commandLocation()).Format("15:04"))
		if row.Details != "" {
			fmt.Println("    " + row.Details)
		}
		for _, t := range row.Tasks {
			fmt.Printf("    %s %s\n", t.ID, t.Title)
		}
	}
	if count == 0 {
		fmt.Printf("処理中や順番待ちの依頼はありません。サーバーの会話一覧にも、過去%s間にコマンドを使ったほかの会話は見つかりませんでした。\n", minutes(*within))
		return nil
	}
	return fmt.Errorf("いまサーバーを起動し直すと、上の%d件に影響するおそれがあります", count)
}

// minutes writes a duration for people: "5分".
func minutes(d time.Duration) string {
	if d <= 0 {
		d = defaultBusyWindow
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d分", d/time.Minute)
	}
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "秒"
}
