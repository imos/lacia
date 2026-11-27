package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/doctor"
	"github.com/imos/lacia/internal/task"
)

// runDoctor checks the shape of the record repository and says what is
// wrong. It changes nothing.
func runDoctor(args []string) error {
	if len(args) > 0 && args[0] == "local" {
		return runDoctorLocal(args[1:])
	}
	fs := commandFlags("doctor")
	root := fs.String("root", "", "記録のリポジトリー")
	state := fs.String("state", "", "この記録を使うサーバーの状態ディレクトリー。指定すると会話の最終利用も点検します")
	links := fs.Bool("links", false, "Markdown の相対リンクの先があるかも確かめます")
	config := fs.String("config", "", "サーバーの設定ファイルです。指定すると、祝日の表示設定も点検します")
	profile := fs.String("profile", "", "サーバーの設定にあるプロファイル")
	holidays := fs.String("calendar-holidays", "jp", "日本の祝日を表示するときは jp、表示しないときは off を指定します")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	if *config != "" {
		if _, err := (configSource{path: *config, profile: *profile, only: "calendar-holidays"}).apply(fs); err != nil {
			return err
		}
	} else if *profile != "" {
		return fmt.Errorf("--profile は --config と一緒に使ってください")
	}
	if conf := filepath.Join(dir, recordsConfig); fileExists(conf) {
		if _, err := (configSource{path: conf, only: "calendar-holidays"}).apply(fs); err != nil {
			return err
		}
	}
	hide, err := calendar.HolidaySetting(*holidays)
	if err != nil {
		return err
	}
	context, err := stateLintContext(*state)
	if err != nil {
		return err
	}
	report := doctor.CheckWithState(dir, *links, context)
	if !hide {
		loc := task.JST
		if cfg, err := task.LoadConfig(dir); err == nil {
			loc = cfg.Location()
		}
		if note := calendar.HolidayDataNote(time.Now().In(loc)); note != "" {
			report.Notes = append(report.Notes, note)
		}
	}
	for _, e := range report.Errors {
		fmt.Println("誤り: " + e)
	}
	for _, n := range report.Notes {
		fmt.Println("お知らせ: " + n)
	}
	fmt.Printf("点検しました。誤りは%d件、お知らせは%d件です。\n", len(report.Errors), len(report.Notes))
	if len(report.Errors) > 0 {
		return fmt.Errorf("記録の形に、誤りが%d件あります", len(report.Errors))
	}
	return nil
}
