package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

type handoff struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	Label  string `json:"label"`
}

// runHandoff passes something the user has to deal with from the chat side
// to the task side's server, where it becomes a task on the web pages. The chat
// side can also ask how far a task it handed over has come; it cannot read
// anything else there.
func runHandoff(args []string) error {
	// The peer profile takes it over: its address is the server's setting,
	// unless LACIA_HANDOFF_API names another.
	base := strings.TrimRight(os.Getenv("LACIA_HANDOFF_API"), "/")
	if base == "" {
		var got struct{ URL string }
		if err := apiCall("GET", "/api/peer", nil, &got); err != nil || got.URL == "" {
			return errors.New("引き継ぐ相手が分かりません。サーバーの設定に相手のプロファイル（peer）を書くか、LACIA_HANDOFF_API にアドレスを設定してください")
		}
		base = got.URL
	}
	// apiCall reads the address from LACIA_API.
	os.Setenv("LACIA_API", base)
	if len(args) > 0 && args[0] == "status" {
		if len(args) != 2 {
			return errors.New("使い方: lacia handoff status <引き継ぎのID>")
		}
		var got handoff
		if err := apiCall("GET", "/api/handoffs/"+args[1], nil, &got); err != nil {
			return err
		}
		fmt.Printf("%s（%s）「%s」は、いま「%s」です。\n", got.ID, got.TaskID, got.Title, got.Label)
		return nil
	}
	fs := commandFlags("handoff")
	title := fs.String("title", "", "タスクの題名")
	from := fs.String("from", "", "もとの依頼をした人の呼び名")
	id := fs.String("id", "", "引き継ぎのID（送り直すときに同じものを指定します）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), " ")
	if text == "-" || text == "" {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return err
		}
		text = string(data)
	}
	if *id == "" {
		buf := make([]byte, 4)
		rand.Read(buf)
		*id = "h-" + hex.EncodeToString(buf)
	}
	var got handoff
	if err := apiCall("POST", "/api/handoffs", map[string]string{"id": *id, "title": *title, "text": text, "from": *from}, &got); err != nil {
		return fmt.Errorf("%w\n届いたかどうか分からないときは、--id %s を付けて同じ内容を送り直してください。同じIDの引き継ぎは1件として受け付けます", err, *id)
	}
	fmt.Printf("相手のプロファイルに引き継ぎました。引き継ぎのIDは %s、相手のタスクは %s です。\n", got.ID, got.TaskID)
	return nil
}
