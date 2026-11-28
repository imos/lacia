package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/imos/lacia/internal/codex"
	"github.com/imos/lacia/internal/effect/disk"
)

// runCodexProbe sends one message through the bridge on a throwaway
// conversation, to check that this machine's Codex speaks the protocol.
func runCodexProbe(args []string) error {
	fs := commandFlags("codex-probe")
	command := fs.String("command", "codex", "Codex を起動するコマンド")
	workspace := fs.String("workspace", "", "作業ディレクトリー")
	state := fs.String("state", "", "接続用のファイルを置くディレクトリー")
	timeout := fs.Duration("timeout", 3*time.Minute, "待つ時間")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *workspace == "" || *state == "" || fs.NArg() == 0 {
		return errors.New("--workspace、--state、送る文章を指定してください")
	}
	stateDir, err := disk.OpenTools(*state)
	if err != nil {
		return err
	}
	defer stateDir.Close()
	bridge := codex.New(codex.Config{
		Command: strings.Fields(*command), Workspace: *workspace, State: stateDir, Ephemeral: true,
		Instructions: "これは接続の試験です。指示された文章にだけ、短く答えてください。",
	})
	bridge.OnChild = func(c codex.Child) { fmt.Fprintf(os.Stderr, "サブエージェント: %+v\n", c) }
	defer bridge.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	start := time.Now()
	res, err := bridge.Run(ctx, strings.Join(fs.Args(), " "))
	fmt.Fprintf(os.Stderr, "会話のID: %s　返事のID: %s　かかった時間: %s\n", bridge.ThreadID(), res.TurnID, time.Since(start).Round(time.Millisecond))
	if err != nil {
		return err
	}
	fmt.Println(res.Reply)
	return nil
}

// runCodexStop ends the Codex processes of a server, so that the next
// request starts them again with the current environment.
func runCodexStop(args []string) error {
	fs := commandFlags("codex-stop")
	state := fs.String("state", "", "サーバーの --state と同じディレクトリー")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *state == "" {
		return errors.New("--state を指定してください")
	}
	for _, name := range []string{"codex", "codex-side"} {
		if err := codex.StopServer(*state + "/" + name); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			continue
		}
		fmt.Printf("%s の Codex を止めました。\n", name)
	}
	return nil
}
