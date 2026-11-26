package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/imos/lacia/internal/calendar"
)

func runCalendar(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("lacia calendar list を指定してください")
	}
	fs := commandFlags("calendar list")
	period := fs.String("period", "", "today（既定）、tomorrow、week")
	from := fs.String("from", "", "開始日（YYYY-MM-DD）")
	until := fs.String("until", "", "終了日（YYYY-MM-DD、その日の予定は含みません）")
	asJSON := fs.Bool("json", false, "取得状況と表示用の予定をJSONで出します")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("予定を読む範囲は --period、--from、--until で指定してください")
	}
	base := apiBase()
	if base == "" {
		return fmt.Errorf("%s", apiAddressHelp)
	}
	query := url.Values{"period": {*period}, "from": {*from}, "until": {*until}}
	var result calendar.Agenda
	if err := apiClient(base).call(http.MethodGet, "/api/calendar/list?"+query.Encode(), nil, &result); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("%s から %s の前日まで（%s）\n", result.Start, result.Until, result.Timezone)
	if result.Status != "ok" {
		fmt.Println("この期間の予定をすべては確認できていません。")
	} else if len(result.Events) == 0 {
		fmt.Println("この期間の予定は0件です。")
	}
	for _, src := range result.Sources {
		status := "取得済み"
		if src.Status != "ok" || src.Stale || !src.Covered {
			status = "一部または未確認"
		}
		fmt.Printf("%s（%s）", src.Label, status)
		if src.SucceededAt != nil {
			fmt.Printf(" 最後に取得できた時刻 %s", src.SucceededAt.Format("2006-01-02 15:04 MST"))
		}
		if src.AttemptedAt != nil {
			fmt.Printf(" 最後に試みた時刻 %s", src.AttemptedAt.Format("2006-01-02 15:04 MST"))
		}
		fmt.Println()
	}
	for _, event := range result.Events {
		stamp := event.Start.Format("2006-01-02 15:04")
		if event.AllDay {
			stamp = event.Start.Format("2006-01-02") + " 終日"
		}
		fmt.Printf("%s %s（%s）\n", stamp, event.Title, event.Calendar)
	}
	return nil
}
