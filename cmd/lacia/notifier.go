package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/macos/notifier"
)

// notifierApp is where manage.py puts the app.
func notifierApp() string {
	return filepath.Join(os.Getenv("HOME"), "Applications", "Lacia Notifications.app")
}

func notifierInstalled() bool { return runtime.GOOS == "darwin" && fileExists(notifierApp()) }

// withNotifierSource writes the source the binary carries into the tools
// directory, runs f with the directory, and takes it away again.
func withNotifierSource(tools *disk.Dir, f func(dir string) error) error {
	const build = "tmp/notifier"
	if err := tools.RemoveAll(build); err != nil {
		return err
	}
	defer tools.RemoveAll(build)
	err := fs.WalkDir(notifier.Source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, _ := notifier.Source.ReadFile(path)
		return tools.WriteFile(filepath.Join(build, path), data, 0o600)
	})
	if err != nil {
		return err
	}
	return f(filepath.Join(tools.Path(), build))
}

func manage(ctx context.Context, dir string, args ...string) error {
	cmd := program.Python3().Command(ctx, append([]string{filepath.Join(dir, "manage.py")}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("通知アプリの manage.py %s に失敗しました: %w", args[0], err)
	}
	return nil
}

// installNotifier builds the notifier app from the source of this binary
// and installs it, or replaces the installed one keeping its settings.
func installNotifier(ctx context.Context, tools *disk.Dir, bin, addr string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("通知アプリは、macOS でだけ使えます")
	}
	if err := requireDeveloperTools(ctx, program.XcodeSelect(), "-p"); err != nil {
		return err
	}
	return withNotifierSource(tools, func(dir string) error {
		app := filepath.Join(dir, "Lacia Notifications.app")
		if err := manage(ctx, dir, "build", app); err != nil {
			return err
		}
		if notifierInstalled() {
			return manage(ctx, dir, "upgrade", app)
		}
		if err := manage(ctx, dir, "install", app, "--lacia", bin, "--url", "http://"+addr); err != nil {
			return err
		}
		if err := manage(ctx, dir, "start"); err != nil {
			return err
		}
		fmt.Println("通知アプリを入れました。メニューバーのアイコンから「通知を許可する」を選んでください。")
		return nil
	})
}

// requireDeveloperTools checks that the program runs. On macOS, git, make
// and the compilers come with Apple's command line tools; when they are
// missing, the window that installs them is opened, and setup stops until
// they are in.
func requireDeveloperTools(ctx context.Context, p program.Program, args ...string) error {
	if _, err := p.Output(ctx, "", args...); err == nil {
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("%s を使えません。入れてから、lacia setup をもう1度実行してください", p.Name())
	}
	program.XcodeSelect().Output(ctx, "", "--install")
	return fmt.Errorf("%s を使うには、Xcode のコマンドラインツールが要ります。入れる画面を開きました。入れ終わったら、lacia setup をもう1度実行してください", filepath.Base(p.Name()))
}
