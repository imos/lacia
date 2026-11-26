package main

import (
	"fmt"
	"os"
	"time"

	"github.com/imos/lacia/internal/migrate"
)

// runMigrate moves a record repository to its next layout, or puts it
// back with --undo. It does not commit: whoever runs it looks at the result
// first.
func runMigrate(args []string) error {
	fs := commandFlags("migrate")
	root := fs.String("root", "", "記録のリポジトリー")
	moves := fs.String("moves", "", "追加で移す場所の一覧。1行に移動元と移動先をタブで区切って書きます。消すときは移動先に - を指定します")
	dryRun := fs.Bool("dry-run", false, "ファイルを書き換えずに、移す内容を点検します")
	undo := fs.Bool("undo", false, "移行時に保存した記録を使って、ファイルを元に戻します")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := findRoot(*root)
	if err != nil {
		return err
	}
	if *undo {
		if readOnlyCopy(dir) {
			return fmt.Errorf("ここは読み取り専用のコピーです。記録の形式を戻すには、サーバーが使う正本で lacia migrate --undo を実行してください")
		}
		n, err := migrate.Undo(dir)
		if err != nil {
			return fmt.Errorf("%d件を元に戻したところで、復元が止まりました: %w", n, err)
		}
		fmt.Printf("移行時の操作を%d件取り消し、元に戻しました。git status で結果を確かめてください。\n", n)
		return nil
	}
	now := time.Now()
	plan, err := migrate.Make(migrate.Options{Root: dir, Moves: *moves, DryRun: *dryRun, ReadOnly: readOnlyCopy(dir), Now: now})
	if err != nil {
		return err
	}
	if *dryRun {
		plan.Report(os.Stdout, true)
		return nil
	}
	if err := plan.Run(now); err != nil {
		return fmt.Errorf("移している途中で止まりました。lacia migrate --undo で戻せます: %w", err)
	}
	plan.Report(os.Stdout, false)
	fmt.Println("変更はまだコミットしていません。結果を確かめたら、git add -A でステージしてコミットしてください。元に戻すには lacia migrate --undo を使います。")
	return nil
}
