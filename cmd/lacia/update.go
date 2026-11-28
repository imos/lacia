package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/home"
)

// installScript is the way in of a machine set up by the script.
const installScript = "https://raw.githubusercontent.com/imos/lacia/main/install.sh"

// runUpdate runs the way in that set this machine up once more: make install
// in the clone at ~/.lacia/src, or the install script. Either ends in lacia
// setup of the new binary, which replaces this one.
func runUpdate(args []string) error {
	if len(args) > 0 {
		return usageError("update")
	}
	ctx := context.Background()
	tools, err := disk.OpenTools(home.Dir(os.Getenv))
	if err != nil {
		return err
	}
	defer tools.Close()
	src := filepath.Join(tools.Path(), "src")
	if !fileExists(src) {
		if _, err := program.Curl().Output(ctx, "", "--proto", "=https", "--tlsv1.2", "-fsSL", "-o", filepath.Join(tools.Path(), "tmp", "install.sh"), "--create-dirs", installScript); err != nil {
			return fmt.Errorf("導入のスクリプトを取得できませんでした: %w", err)
		}
		defer tools.Remove("tmp/install.sh")
		return attached(program.Sh().Command(ctx, filepath.Join(tools.Path(), "tmp", "install.sh")), "")
	}
	git := program.Git()
	if changes, err := git.Output(ctx, src, "status", "--porcelain"); err != nil {
		return err
	} else if len(strings.TrimSpace(string(changes))) > 0 {
		fmt.Printf("%s に手元の変更があるので、取得せずに、いまあるものをビルドします。\n", src)
	} else if _, err := git.Output(ctx, src, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		if err := attached(git.Command(ctx, "pull", "--ff-only"), src); err != nil {
			return err
		}
	}
	return attached(program.Make().Command(ctx, "install"), src)
}

// attached runs the command with this terminal, so that its questions and
// what it prints reach the person.
func attached(cmd *program.Cmd, dir string) error {
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// runUninstall takes away what lacia setup put on this machine: the
// registration of the server, the notifier, the link in ~/.local/bin and
// ~/.lacia. The records, and the repository on GitHub, are left.
func runUninstall(args []string) error {
	fs := commandFlags("uninstall")
	yes := fs.Bool("yes", false, "確かめずに消します")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError("uninstall")
	}
	ctx := context.Background()
	dir := home.Dir(os.Getenv)
	root, _ := machineRoot()
	src := filepath.Join(dir, "src")
	if info, err := os.Lstat(src); err == nil && info.IsDir() {
		if changes, _ := program.Git().Output(ctx, src, "status", "--porcelain"); len(strings.TrimSpace(string(changes))) > 0 && !*yes {
			return fmt.Errorf("%s に、コミットしていない変更があります。残すものを別の場所へ移してから実行するか、消してよければ --yes を付けてください", src)
		}
	}
	fmt.Printf("次のものを消します。記録（%s）と、GitHub のリポジトリーは残します。\n  ログインのときにサーバーを起動する登録\n  %s\n  ~/.local/bin/lacia のリンク\n", orNone(root), dir)
	if notifierInstalled() {
		fmt.Println("  通知アプリと、その設定")
	}
	if !*yes {
		answer, err := newAsker().ask("消してよければ、yes と入力してください。", "", "--yes")
		if err != nil {
			return err
		}
		if answer != "yes" {
			return fmt.Errorf("何も消さずに止めました")
		}
	}
	if err := stopServing(ctx, serveLabel()); err != nil {
		return err
	}
	if notifierInstalled() {
		tools, err := disk.OpenTools(dir)
		if err != nil {
			return err
		}
		err = withNotifierSource(tools, func(src string) error { return manage(ctx, src, "uninstall", "--remove-state") })
		tools.Close()
		if err != nil {
			return err
		}
	}
	if link, err := os.Readlink(filepath.Join(os.Getenv("HOME"), ".local", "bin", "lacia")); err == nil && within(dir, link) {
		if err := disk.UnlinkCommand(); err != nil {
			return err
		}
	}
	if err := disk.RemoveTools(dir); err != nil {
		return err
	}
	fmt.Println("消しました。記録は残っています。")
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "ありません"
	}
	return s
}
