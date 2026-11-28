package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/home"
)

// serveLabel names the definition that starts the server at login. The
// template registered the same one, so a machine set up from it is taken
// over. LACIA_LABEL names another, for a trial beside a real one.
func serveLabel() string {
	if label := os.Getenv("LACIA_LABEL"); label != "" {
		return label
	}
	return "jp.imoz.lacia.serve"
}

func launchAgentPath(label string) string {
	return filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", label+".plist")
}

func launchTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// servePlist is the definition of the server started at login. The binary
// is named directly, so macOS lists it as lacia, and launchd, which reads
// no shell settings, is given the PATH and LANG of this terminal: with only
// its own PATH it finds neither Codex, Claude Code nor gh. HOME is written
// too, so a trial run with another home keeps its files there.
func servePlist(label, bin, config string) []byte {
	lang := os.Getenv("LANG")
	if lang == "" {
		lang = "ja_JP.UTF-8"
	}
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	log := esc(serveLog())
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>serve</string><string>--config</string><string>%s</string></array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key><string>%s</string>
		<key>LANG</key><string>%s</string>
		<key>HOME</key><string>%s</string>
	</dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>30</integer>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, esc(label), esc(bin), esc(config), esc(os.Getenv("PATH")), esc(lang), esc(os.Getenv("HOME")), log, log)
}

// ours reports whether the definition at the path is one lacia wrote, or
// there is none: a file of the same name put by someone else is left alone.
func ours(label string) (existing []byte, ok bool) {
	data, err := os.ReadFile(launchAgentPath(label))
	if errors.Is(err, os.ErrNotExist) {
		return nil, true
	}
	return data, err == nil && bytes.Contains(data, []byte("<string>"+label+"</string>"))
}

func loaded(ctx context.Context, label string) bool {
	_, err := program.Launchctl().Output(ctx, "", "print", launchTarget()+"/"+label)
	return err == nil
}

// keepServing registers the server to start at login, or takes the
// registration away. A server already running is asked to restart once it
// is idle when the binary was replaced; one that is registered anew is
// waited for, so that the page opened next answers.
func keepServing(ctx context.Context, c *serveConfig, bin string, autostart, replaced bool) error {
	label := serveLabel()
	if runtime.GOOS != "darwin" {
		fmt.Printf("ログインのときにサーバーを起動する登録は、macOS でだけ行います。この端末では、%s serve でサーバーを起動してください。\n", bin)
		return nil
	}
	existing, ok := ours(label)
	if !ok {
		return fmt.Errorf("%s は、別のものが置いたファイルのようです。そのままにしました", launchAgentPath(label))
	}
	if !autostart {
		return stopServing(ctx, label)
	}
	tools, err := disk.OpenTools(home.Dir(os.Getenv))
	if err != nil {
		return err
	}
	defer tools.Close()
	if err := tools.MkdirAll("logs"); err != nil {
		return err
	}
	plist := servePlist(label, bin, *c.str("config"))
	addr := c.value("addr")
	if loaded(ctx, label) {
		if !bytes.Equal(existing, plist) {
			if err := disk.InstallLaunchAgent(label, plist); err != nil {
				return err
			}
			fmt.Println("ログインのときにサーバーを起動する定義を、いまの PATH で書き直しました。新しい定義は、次にログインしたときから使います。")
		}
		if replaced {
			if err := apiClient("http://"+addr).call(http.MethodPost, "/api/restart", nil, nil); err != nil {
				return fmt.Errorf("動いているサーバーに、新しい版で起動し直すよう頼めませんでした。処理が空いているときに lacia restart --when-idle を実行してください: %w", err)
			}
			fmt.Println("動いているサーバーに、処理が終わったら新しい版で起動し直すよう頼みました。")
		}
		return nil
	}
	if err := disk.InstallLaunchAgent(label, plist); err != nil {
		return err
	}
	if _, err := program.Launchctl().Output(ctx, "", "bootstrap", launchTarget(), launchAgentPath(label)); err != nil {
		return err
	}
	fmt.Println("ログインのときに、サーバーが自動で起動するようにしました。いまも起動しています。")
	return waitServing(addr)
}

// stopServing takes the server out of the login items and stops it.
func stopServing(ctx context.Context, label string) error {
	if !fileExists(launchAgentPath(label)) {
		return nil
	}
	program.Launchctl().Output(ctx, "", "bootout", launchTarget()+"/"+label)
	if err := disk.RemoveLaunchAgent(label); err != nil {
		return err
	}
	fmt.Println("ログインのときにサーバーを起動する登録を外し、サーバーを止めました。")
	return nil
}

// waitServing waits until the server answers, or says where to look.
func waitServing(addr string) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := apiClient("http://"+addr).request(http.MethodGet, "/healthz", nil, nil, 2*time.Second)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("サーバーが、20秒たっても応えませんでした。ログ（%s）を見てください: %w", serveLog(), err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
