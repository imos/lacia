package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/imos/lacia/internal/discord"
)

// runDiscord delivers approved PDF attachments, shows the members,
// pauses or resumes taking a child's messages, and rebuilds the log of past
// days.
func runDiscord(args []string) error {
	// The backfill reads and writes files on the machine it runs on; it
	// does not go through the server.
	if len(args) > 0 && args[0] == "backfill" {
		return runDiscordBackfill(args[1:])
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	if len(args) > 0 && args[0] == "attach" {
		fs := commandFlags("discord attach")
		requestID := fs.String("request", "", "PDF を添付して返す元の Discord の依頼ID")
		path := fs.String("file", "", "添付する PDF のパス（サーバーの記録の local/ からの相対パス）")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *requestID == "" || *path == "" || fs.NArg() != 0 {
			return errors.New("使い方: lacia discord attach --request <依頼ID> --file <local内の相対パス.pdf>")
		}
		var result discord.AttachmentResult
		if err := apiCall("POST", "/api/discord/attachments", map[string]string{"request_id": *requestID, "path": *path}, &result); err != nil {
			return err
		}
		if result.Status != "sent" {
			return errors.New("添付が届いたか分かりません。重ねて送るのを避けるため、Discord で元の依頼のチャンネルを確かめてください")
		}
		fmt.Printf("Discord が添付ファイル %s を受け付けました（メッセージID %s）。このコマンドで送るのは添付ファイルだけです。\n", result.Filename, result.MessageID)
		return nil
	}
	switch {
	case len(args) == 0 || args[0] == "members":
		var list []discord.MemberStatus
		if err := apiCall("GET", "/api/discord/members", nil, &list); err != nil {
			return err
		}
		for _, m := range list {
			role, id, paused := "大人", m.ID, ""
			if m.Role == discord.Child {
				role = "お子さん"
			}
			if id == "" {
				id = "ユーザー名 " + m.Username + "（IDはまだ分かりません）"
			} else {
				id = "ID " + id
			}
			if m.PausedUntil != nil {
				paused = "　" + m.PausedUntil.In(commandLocation()).Format("1月2日 15:04") + " まで受け付けを止めています"
			}
			fmt.Printf("%s　%s　Discord の%s%s\n", m.Name, role, id, paused)
		}
		return nil
	case args[0] == "pause" && len(args) == 3:
		minutes, err := strconv.Atoi(args[2])
		if err != nil {
			return errors.New("受け付けを止める時間を、分単位の整数で指定してください（例: lacia discord pause Xさん 30）")
		}
		var got discord.MemberStatus
		if err := apiCall("POST", "/api/discord/pause", map[string]any{"who": args[1], "minutes": minutes}, &got); err != nil {
			return err
		}
		fmt.Printf("%sからの発言の受け付けを %s まで止めました。\n", got.Name, got.PausedUntil.In(commandLocation()).Format("1月2日 15:04"))
		return nil
	case args[0] == "resume" && len(args) == 2:
		var got discord.MemberStatus
		if err := apiCall("POST", "/api/discord/resume", map[string]any{"who": args[1]}, &got); err != nil {
			return err
		}
		fmt.Printf("%sからの発言の受け付けを再開しました。\n", got.Name)
		return nil
	}
	return errors.New("使い方: lacia discord attach --request <依頼ID> --file <local内の相対パス.pdf> | members | pause <名前かID> <分> | resume <名前かID> | backfill --state <ディレクトリー>")
}
