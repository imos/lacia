package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/testrecords"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

func TestTalkingToTheHomeMode(t *testing.T) {
	open := func(areas string) *task.Store {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
		os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"areas":[`+areas+`]}`), 0o644)
		store, err := task.Open(testrecords.Init(t, root))
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	queue := func() *request.Queue {
		q, err := request.Open(testDir(t, t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	homeQueue, workQueue, sent := queue(), queue(), queue()
	home, err := (&Server{Store: open(`"family"`), Requests: homeQueue, DoorOnly: true, Inbox: &Inbox{Discord: "discord:1"}, Label: "家庭モード", PeerLabel: "仕事モード", People: "ご家族"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	homeServer := httptest.NewServer(home)
	workStore := open(`"work","family"`)
	work, err := (&Server{Store: workStore, Requests: workQueue, Site: "https://lacia.example.test", Peer: &Peer{URL: homeServer.URL, Sent: sent}, Label: "仕事モード", PeerLabel: "家庭モード", People: "ご家族"}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(h http.Handler, method, target, body, remote, host, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.RemoteAddr, req.Host = remote, host
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const pod, self, form, js = "10.1.2.3:40000", "127.0.0.1:40000", "application/x-www-form-urlencoded", "application/json"

	// From another Pod, the chat side's server opens its door and nothing else.
	for _, target := range []string{"/", "/tasks", "/api/tasks", "/requests"} {
		if rec := call(home, "GET", target, "", pod, "chat.internal", ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s on the chat side's server from another Pod = %d", target, rec.Code)
		}
	}
	if rec := call(home, "GET", "/api/tasks", "", self, "127.0.0.1:8081", ""); rec.Code != http.StatusOK {
		t.Errorf("the chat side's server from its own Pod = %d", rec.Code)
	}
	if rec := call(home, "GET", "/api/inbox/m-unknown", "", pod, "chat.internal", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown message = %d", rec.Code)
	}

	// What the user types on a page arrives as the user's own words.
	const original = "しおりの集合時刻を、9時に直してください。"
	typed := url.Values{"text": {original}, "discord": {"1"}}.Encode()
	if rec := call(work, "POST", "/peer/messages", typed, self, "127.0.0.1:8080", form); rec.Code != http.StatusForbidden {
		t.Errorf("speaking as the user from inside the Pod = %d", rec.Code)
	}
	if rec := call(work, "POST", "/peer/messages", typed, pod, "lacia.example.test", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("tell from the page = %d %s", rec.Code, rec.Body.String())
	}
	told := sent.Recent(0, "", true)
	if len(told) != 1 || told[0].Status != request.Queued || told[0].Source != "Web から家庭モードへ（返事は Discord にも）" {
		t.Fatalf("record on the task side = %+v", told)
	}
	got, ok := homeQueue.Get(inboxPrefix + told[0].ID)
	if !ok || got.Kind != request.User || got.From != "" || got.Source != "Web" || got.ReplyTo != "discord:1" ||
		!strings.HasPrefix(got.Text, "オーナーさんがWebで入力した文です。") || !strings.HasSuffix(got.Text, "\n\n"+original) {
		t.Errorf("request on the chat side = %+v", got)
	}
	if len(homeQueue.Recent(0, "", true)) != 1 || len(open(`"family"`).List()) != 0 {
		t.Errorf("the chat side must not get a task")
	}

	// The reply is read from the chat side's server when asked for, and is also
	// pushed back when the chat side finishes.
	homeQueue.Resolve(got.ID, request.Done, "直しました。", "", false)
	if rec := call(work, "GET", "/api/peer/messages/"+told[0].ID, "", self, "127.0.0.1:8080", ""); !strings.Contains(rec.Body.String(), "直しました。") {
		t.Errorf("reply read through = %s", rec.Body.String())
	}

	// The task side's Lacia cannot speak as the user.
	rec := call(work, "POST", "/api/peer/messages", `{"id":"m-fromwork1","kind":"user","text":"引き継いだ件は、来週になります。"}`, self, "127.0.0.1:8080", js)
	if rec.Code != http.StatusOK {
		t.Fatalf("tell from the command = %d %s", rec.Code, rec.Body.String())
	}
	notice, _ := homeQueue.Get(inboxPrefix + "m-fromwork1")
	if notice.From != "仕事モードのレイシア" || notice.ReplyTo != "" || !strings.Contains(notice.Text, "本人の言葉ではありません") {
		t.Errorf("notice on the chat side = %+v", notice)
	}
	// With the labels of the settings, the wording is what it was when the
	// labels were written in the code.
	const marked = "仕事モードのレイシアが書いた伝言です。オーナーさん本人の言葉ではありません。" +
		"あなたの返事は、オーナーさんの Web の画面（仕事モード）に表示されます。Discord には投稿されません。\n\n引き継いだ件は、来週になります。"
	if notice.Text != marked || notice.Source != "仕事モード" {
		t.Errorf("message wording changed: %q\n%s", notice.Source, notice.Text)
	}
	if r, _ := sent.Get("m-fromwork1"); r.Source != "仕事モードから家庭モードへ" || r.From != "仕事モードのレイシア" {
		t.Errorf("record of the message = %+v", r)
	}
	if rec := call(work, "GET", "/api/peer", "", self, "127.0.0.1:8080", ""); !strings.Contains(rec.Body.String(), `"peer":"家庭モード"`) || !strings.Contains(rec.Body.String(), `"label":"仕事モード"`) {
		t.Errorf("labels for the command = %s", rec.Body.String())
	}
	workServer := httptest.NewServer(work)
	defer workServer.Close()
	notice, _ = homeQueue.Resolve(notice.ID, request.Done, "承知しました。", "", false)
	ReplyToPeer(context.Background(), nil, workServer.URL, homeQueue, notice)
	if r, _ := sent.Get("m-fromwork1"); r.Status != request.Done || r.Reply != "承知しました。" {
		t.Errorf("pushed reply = %+v", r)
	}

	// Closing a handed-over task is told to the chat side at once.
	if rec := call(work, "POST", "/api/handoffs", `{"id":"h-12345678","title":"宿の支払いを済ませる","from":"Xさん","text":"残金の支払いが要ります。"}`, pod, "tasks.internal", js); rec.Code != http.StatusCreated {
		t.Fatalf("handoff = %d %s", rec.Code, rec.Body.String())
	}
	handed := workQueue.Recent(1, "", false)[0].TaskID
	if rec := call(work, "POST", "/api/tasks/"+handed, `{"status":"done","actor":"Web"}`, self, "127.0.0.1:8080", js); rec.Code != http.StatusOK {
		t.Fatalf("close = %d %s", rec.Code, rec.Body.String())
	}
	closed, ok := homeQueue.Get(inboxPrefix + "st-h-12345678-done")
	if !ok || closed.Kind != request.System || closed.ReplyTo != "discord:1" || !strings.Contains(closed.Text, "宿の支払いを済ませる") || !strings.Contains(closed.Text, "完了") {
		t.Errorf("status notice = %+v %v", closed, ok)
	}

	// With the chat side's server down, the sender is told at once, and the record
	// says that it did not arrive.
	homeServer.Close()
	rec = call(work, "POST", "/api/peer/messages", `{"id":"m-whiledown","text":"届かない伝言です。"}`, self, "127.0.0.1:8080", js)
	if r, _ := sent.Get("m-whiledown"); rec.Code != http.StatusBadGateway || r.Status != request.Failed || !strings.HasPrefix(r.Error, "家庭モードへ届けられませんでした。") {
		t.Errorf("while down: %d %+v", rec.Code, r)
	}
	rec = call(work, "GET", "/peer", "", pod, "lacia.example.test", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "応答がありません") || !strings.Contains(rec.Body.String(), "承知しました。") {
		t.Errorf("page = %d", rec.Code)
	}
	for _, want := range []string{
		"<title>家庭モード", `<a href="/peer">家庭モード</a>`, "<h1>家庭モード</h1>",
		"家庭モードのレイシアに送った伝言と、その返事を読めます。",
		"いま、家庭モードのサーバーから応答がありません。",
		`placeholder="家庭モードのレイシアへ伝えることを書いてください"`, `<button class="primary">家庭モードへ伝える</button>`,
		"下の一覧には、わたしが仕事モードから送った伝言も含まれます。ご家族と家庭モードのレイシアの会話は、ここには表示しません。",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	page := call(work, "GET", "/tasks/"+handed, "", pod, "lacia.example.test", "").Body.String()
	for _, want := range []string{
		"<h2>家庭モードのレイシアへ伝える</h2>", `placeholder="このタスクについて、家庭モードのレイシアへ伝えることを書いてください"`,
		"<button>家庭モードへ伝える</button>", "入力した文は、仕事モードの会話を通さず、そのまま家庭モードのレイシアへ届けます。",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the task page lacks %q", want)
		}
	}
	if log := string(mustRead(t, filepath.Join(workStore.Root(), mustGet(t, workStore, handed).Dir, task.HistoryFile))); !strings.Contains(log, "家庭モードへ、「完了」になったことを知らせました。") {
		t.Errorf("log of the handed-over task:\n%s", log)
	}
}

// Without labels in the settings, the texts use general words and carry
// nobody's names for the profiles.
func TestProfilesWithoutLabels(t *testing.T) {
	s := &Server{}
	if s.label() != "通常モード" || s.peerLabel() != "もう一方のモード" {
		t.Errorf("defaults = %q, %q", s.label(), s.peerLabel())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustGet(t *testing.T, store *task.Store, id string) task.Task {
	t.Helper()
	got, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
