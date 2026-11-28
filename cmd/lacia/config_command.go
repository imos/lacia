package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/home"
)

// release is the version a release build is made as
// (-ldflags "-X main.release=v1.2.3"). A build from a clone has none.
var release string

// runVersion tells the version and the commit the binary was built from.
func runVersion(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("lacia version は引数を取りません")
	}
	name := release
	if name == "" {
		name = "開発版（リリースではないビルド）"
	}
	fmt.Println("lacia " + name)
	if rev := version(); rev != "" {
		fmt.Println("コミット: " + rev)
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.time" {
				fmt.Println("コミットの日時: " + s.Value)
			}
		}
		fmt.Println("Go: " + info.GoVersion)
	}
	if path, err := os.Executable(); err == nil {
		fmt.Println("実行ファイル: " + path)
	}
	return nil
}

// runConfig shows each setting of lacia serve with its value and where the
// value came from, read as the server would read them with the same
// arguments. It only reads.
func runConfig(args []string) error {
	c, err := loadServeConfig(args)
	if err != nil {
		return err
	}
	machine := *c.str("config")
	if machine == "" {
		machine = home.Settings(os.Getenv).New + "（ありません）"
	}
	recordsFile := filepath.Join(c.root, recordsConfig)
	if !fileExists(recordsFile) {
		recordsFile += "（ありません）"
	}
	fmt.Printf("端末の設定: %s\n記録の設定: %s\n道具の置き場所: %s\n\n", machine, recordsFile, home.Dir(os.Getenv))
	fmt.Printf("夜の処理\n  %s\n  これは、いまのシェルの PATH で Claude Code と Codex を探した結果です。サーバーは、サーバーの PATH で探します。\n", nightSummary(c))
	fmt.Print("\n値のあとのかっこは、その値を決めた場所です。かっこのない値は、既定のままです。\n")
	group := ""
	for _, s := range serveSettings {
		if s.group != group {
			group = s.group
			fmt.Printf("\n%s\n", group)
		}
		value := c.value(s.name)
		if value == "" {
			value = "（なし）"
		}
		if from := c.origin[s.name]; from != "" {
			value += "（" + from + "）"
		}
		fmt.Printf("  %s = %s\n", s.name, value)
	}
	if moves := home.Moves(os.Getenv); len(moves) > 0 {
		fmt.Printf("\n%s\n", oldPlacesText(moves))
	}
	return nil
}

// oldPlacesText tells that settings are read from the place of older
// versions, and how to bring them over.
func oldPlacesText(moves []string) string {
	return "端末の設定を、以前の版の置き場所から読んでいます。いまのままでも動きます。次のコマンドで、新しい置き場所（~/.lacia）へ移せます。\n  " + strings.Join(moves, "\n  ")
}

// noticeOldPlaces tells once, on a terminal, that the settings of the
// machine lie in the place of older versions. The conversations run lacia
// without a terminal, and are not told.
func noticeOldPlaces(args []string, out io.Writer, terminal bool) {
	if !terminal || len(args) == 0 {
		return
	}
	switch args[0] {
	case "serve", "agent", "config", "codex-login":
		return
	}
	moves := home.Moves(os.Getenv)
	if len(moves) == 0 {
		return
	}
	// Told only if it can be remembered: a machine that cannot keep the
	// mark would be told on every command.
	tools, err := disk.OpenTools(home.Dir(os.Getenv))
	if err != nil {
		return
	}
	defer tools.Close()
	if tools.CreateNew(filepath.Join("state", "old-places-told"), nil, 0o600) != nil {
		return
	}
	fmt.Fprintln(out, oldPlacesText(moves)+"\n  この知らせは、1度だけ出します。lacia config でも読めます。")
}

// isTerminal reports whether the file is a terminal a person reads.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
