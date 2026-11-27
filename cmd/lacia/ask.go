package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/imos/lacia/internal/request"
)

// runAsk sends a request to the server. The id is made here, before the
// first attempt, so that sending again after a lost reply cannot create a
// second request.
func runAsk(args []string) error {
	fs := commandFlags("ask")
	resume := fs.String("resume", "", "再開する依頼のID。ご本人が窓口に直接送ったタスクの依頼で、状態が「結果不明」か「失敗」のときに使えます")
	taskID := fs.String("task", "", "対象のタスクのID")
	area := fs.String("area", "", "この依頼でタスクを作るときの領域")
	source := fs.String("source", "コマンド", "どこからの依頼か")
	id := fs.String("id", "", "依頼のID（送り直すときに同じものを指定します）")
	wait := fs.Bool("wait", false, "返事が届くまで待ちます")
	keepStatus := fs.Bool("keep-status", false, "記録だけを直すときに、指定したタスクの状態を保ちます")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *resume != "" && (*taskID != "" || *area != "" || *keepStatus || len(fs.Args()) > 0) {
		return errors.New("--resume と一緒に、依頼の文章、--task、--area、--keep-status は指定できません")
	}
	if *keepStatus && *taskID == "" {
		return errors.New("--keep-status には --task で既存のタスクを指定してください")
	}
	text := strings.Join(fs.Args(), " ")
	if *resume == "" && (text == "-" || text == "") {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return err
		}
		text = string(data)
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	if *id == "" {
		buf := make([]byte, 8)
		rand.Read(buf)
		*id = "ask-" + hex.EncodeToString(buf)
	}
	policy := ""
	if *keepStatus {
		policy = request.KeepStatus
	}
	var saved request.Request
	if err := apiCall("POST", "/api/requests", map[string]string{"id": *id, "text": text, "task": *taskID, "area": *area, "source": *source, "status_policy": policy, "resume": *resume}, &saved); err != nil {
		return fmt.Errorf("%w\n届いたかどうか分からないときは、--id %s を付けて同じ内容を送り直してください。同じIDの依頼は1件として受け付けます", err, *id)
	}
	receipt := fmt.Sprintf("依頼 %s を受け付けました。", saved.ID)
	if saved.TaskID != "" {
		receipt += fmt.Sprintf("タスクは %s です。", saved.TaskID)
	}
	fmt.Fprintln(os.Stderr, receipt)
	if !*wait {
		return nil
	}
	for saved.Status == request.Queued || saved.Status == request.Running {
		time.Sleep(2 * time.Second)
		if err := apiCall("GET", "/api/requests/"+saved.ID, nil, &saved); err != nil {
			return err
		}
	}
	if saved.Reply != "" {
		fmt.Println(saved.Reply)
	}
	if saved.Status != request.Done {
		return fmt.Errorf("依頼は「%s」で終わりました。%s", saved.StatusLabel(), saved.Error)
	}
	return nil
}
