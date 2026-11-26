package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
)

// peerLabel asks the server how the settings call the peer profile. A
// server that cannot say leaves the general word.
func peerLabel() string {
	var got struct{ Peer string }
	if err := apiCall("GET", "/api/peer", nil, &got); err != nil || got.Peer == "" {
		return self.DefaultPeerLabel
	}
	return got.Peer
}

// runPeer is how Lacia talks to the peer profile, such as the one that
// answers on Discord: `tell` passes a message on, and `status` shows the
// reply. The server marks what goes through here as written by her, not by
// the user.
func runPeer(args []string) error {
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	if len(args) == 2 && args[0] == "status" {
		var got request.Request
		if err := apiCall("GET", "/api/peer/messages/"+args[1], nil, &got); err != nil {
			return err
		}
		return printTold(got, peerLabel())
	}
	if len(args) == 0 || args[0] != "tell" {
		return errors.New("使い方: lacia peer tell [--task ID] [--discord] [--wait] <文章|->、lacia peer status <伝言のID>")
	}
	fs := commandFlags("peer tell")
	taskID := fs.String("task", "", "話の対象のタスクのID")
	discord := fs.Bool("discord", false, "相手のレイシアの返事を、相手側の Discord にも投稿します")
	id := fs.String("id", "", "伝言のID（送り直すときに同じものを指定します）")
	wait := fs.Bool("wait", false, "返事が届くまで待ちます")
	if err := fs.Parse(args[1:]); err != nil {
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
		*id = "m-" + request.NewToken()
	}
	var got request.Request
	if err := apiCall("POST", "/api/peer/messages", map[string]any{"id": *id, "text": text, "task": *taskID, "discord": *discord}, &got); err != nil {
		return fmt.Errorf("%w\n届いたかどうか分からないときは、lacia peer status %s で確かめてください", err, *id)
	}
	peer := peerLabel()
	fmt.Fprintf(os.Stderr, "%sへ伝えました。伝言のIDは %s です。\n", peer, got.ID)
	if !*wait {
		return nil
	}
	for got.Status == request.Queued || got.Status == request.Running {
		time.Sleep(3 * time.Second)
		if err := apiCall("GET", "/api/peer/messages/"+got.ID, nil, &got); err != nil {
			return err
		}
	}
	return printTold(got, peer)
}

func printTold(r request.Request, peer string) error {
	switch r.Status {
	case request.Queued, request.Running:
		fmt.Printf("伝言 %s は、%sで「%s」です。返事は、まだありません。\n", r.ID, peer, r.StatusLabel())
	case request.Done:
		fmt.Println(r.Reply)
	default:
		return fmt.Errorf("伝言 %s は「%s」で終わりました。%s", r.ID, r.StatusLabel(), r.Error)
	}
	return nil
}
