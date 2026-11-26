package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/imos/lacia/internal/localstorage"
)

func runDoctorLocal(args []string) error {
	fs := commandFlags("doctor local")
	root := fs.String("root", "", "この端末にある記録のリポジトリー")
	asJSON := fs.Bool("json", false, "点検結果を JSON で出します")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("対象を指定するときは --root DIR を使ってください")
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	report, err := localstorage.Scan(dir, time.Now())
	if err != nil {
		return fmt.Errorf("点検できませんでした: %w", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s の local/: %d バイト（上限 %g GiB）\n", report.Root, report.Bytes, report.LimitGiB)
		for _, entry := range report.Entries {
			fmt.Printf("  %q: %d バイト（最終更新から %.1f 日）\n", entry.Name, entry.Bytes, entry.AgeDays)
		}
		if report.ExceedsLimit {
			fmt.Println("読み取れた容量が上限を超えています。")
		}
		for _, detail := range report.Errors {
			fmt.Println("誤り: " + detail)
		}
	}
	if !report.Complete {
		return fmt.Errorf("読み取れなかった場所が %d 件あります", len(report.Errors))
	}
	return nil
}
