package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

func TestSplit(t *testing.T) {
	long := strings.Repeat("あ", 1700) + "\n" + strings.Repeat("い", 300) + "\n" + strings.Repeat("う", 2500)
	parts := Split(long)
	if len(parts) != 3 || parts[0] != strings.Repeat("あ", 1700) {
		t.Fatalf("parts = %d, first has %d characters", len(parts), utf8.RuneCountInString(parts[0]))
	}
	joined := ""
	for _, p := range parts {
		if utf8.RuneCountInString(p) > Limit {
			t.Errorf("a part has %d characters", utf8.RuneCountInString(p))
		}
		joined += strings.ReplaceAll(p, "\n", "")
	}
	if joined != strings.ReplaceAll(long, "\n", "") {
		t.Error("text was lost in splitting")
	}
	if got := Split("  短い返事  "); len(got) != 1 || got[0] != "短い返事" {
		t.Errorf("short = %q", got)
	}
}

func TestWantsThread(t *testing.T) {
	for text, want := range map[string]bool{
		"スレッドで教えて": true, "threadを作って答えて": true, "明日の予定を教えて": false,
		"スレッドは不要です": false, "スレッドでなくここに書いて、スレッドは使わないで": false,
	} {
		if got := wantsThread(text); got != want {
			t.Errorf("wantsThread(%q) = %v", text, got)
		}
	}
}

// fake is a minimal Discord: REST for one guild and a gateway that sends
// whatever the test pushes. As Discord does, the gateway lets a dropped
// session resume and replays the events after the client's last sequence.
type fake struct {
	t        *testing.T
	mu       sync.Mutex
	posts    []map[string]any
	reacted  []string
	history  map[string][]Message
	channels map[string]Channel
	events   chan string
	sent     []string      // every event pushed; event i has sequence i+2, after READY
	more     chan struct{} // closed when sent grows
	dropAt   int           // the gateway drops the connection once after sending this sequence
	beat     int           // heartbeat interval in milliseconds
	resumes  int
	server   *httptest.Server
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, history: map[string][]Message{}, channels: map[string]Channel{}, events: make(chan string, 16), more: make(chan struct{}), beat: 1000}
	go func() {
		for payload := range f.events {
			f.mu.Lock()
			f.sent = append(f.sent, payload)
			close(f.more)
			f.more = make(chan struct{})
			f.mu.Unlock()
		}
	}()
	t.Cleanup(func() { close(f.events) })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/channels/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := f.channels[r.PathValue("id")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(c)
	})
	mux.HandleFunc("GET /api/channels/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []Message
		after := r.URL.Query().Get("after")
		list := f.history[r.PathValue("id")]
		for i := len(list) - 1; i >= 0; i-- { // newest first, as Discord does
			if after == "" || newer(list[i].ID, after) {
				out = append(out, list[i])
			}
		}
		if after == "" && len(out) > 1 {
			out = out[:1]
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/channels/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot the-token" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["channel"] = r.PathValue("id")
		f.mu.Lock()
		f.posts = append(f.posts, body)
		f.mu.Unlock()
		w.Write([]byte(`{"id":"999"}`))
	})
	mux.HandleFunc("PUT /api/channels/{id}/messages/{mid}/reactions/{emoji}/@me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reacted = append(f.reacted, r.PathValue("mid"))
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /api/channels/{id}/messages/{mid}/threads", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"7001","type":11,"parent_id":"` + r.PathValue("id") + `","owner_id":"1"}`))
	})
	mux.HandleFunc("GET /gateway", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"op":10,"d":{"heartbeat_interval":%d}}`, f.beat)))
		_, data, err := conn.Read(ctx)
		var identify struct {
			Op int
			D  struct {
				SessionID    string `json:"session_id"`
				Intents, Seq int
			}
		}
		if err == nil {
			err = json.Unmarshal(data, &identify)
		}
		f.mu.Lock()
		next := len(f.sent)
		f.mu.Unlock()
		switch {
		case err == nil && identify.Op == 2 && identify.D.Intents == 33281 && strings.Contains(string(data), "the-token"):
			conn.Write(ctx, websocket.MessageText, []byte(`{"op":0,"t":"READY","s":1,"d":{"session_id":"s","resume_gateway_url":"ws://unused","user":{"id":"1"}}}`))
		case err == nil && identify.Op == 6 && identify.D.SessionID == "s" && strings.Contains(string(data), "the-token"):
			next = identify.D.Seq - 1
			f.mu.Lock()
			f.resumes++
			f.mu.Unlock()
		default:
			t.Errorf("identify = %s (%v)", data, err)
			return
		}
		go func() {
			defer cancel()
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				if strings.Contains(string(data), `"op":1`) {
					conn.Write(ctx, websocket.MessageText, []byte(`{"op":11}`))
				}
			}
		}()
		for {
			f.mu.Lock()
			pending, more := f.sent[next:], f.more
			f.mu.Unlock()
			for _, payload := range pending {
				next++
				seq := next + 1
				if conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"op":0,"t":"MESSAGE_CREATE","s":%d,"d":%s}`, seq, payload))) != nil {
					return
				}
				f.mu.Lock()
				drop := seq == f.dropAt
				if drop {
					f.dropAt = 0
				}
				f.mu.Unlock()
				if drop {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-more:
			}
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func message(id, channel, author, content string, extra string) string {
	return fmt.Sprintf(`{"id":%q,"channel_id":%q,"guild_id":"G","content":%q,"author":{"id":%q,"username":"user%s","global_name":"X"}%s}`, id, channel, content, author, author, extra)
}

func TestBotAcceptsOnlyRequests(t *testing.T) {
	f := newFake(t)
	f.channels["200"] = Channel{ID: "200", Type: typePublicThread, ParentID: "100", OwnerID: "1"}  // started by the bot
	f.channels["201"] = Channel{ID: "201", Type: typePublicThread, ParentID: "100", OwnerID: "50"} // someone else's
	f.channels["202"] = Channel{ID: "202", Type: typePublicThread, ParentID: "999", OwnerID: "1"}  // other parent
	f.history["100"] = []Message{{ID: "10", Content: "切り替えの前の古い投稿"}}

	dir := t.TempDir()
	cfg := Config{Token: "the-token", GuildID: "G", Channels: []string{"100"}, AnyHuman: true, State: testDir(t, dir), StateName: "discord.json",
		api: f.server.URL + "/api", gateway: "ws" + strings.TrimPrefix(f.server.URL, "http") + "/gateway"}
	var mu sync.Mutex
	var got []Incoming
	seen := map[string]bool{}
	receive := func(in Incoming) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if seen[in.ID] {
			return false, nil
		}
		seen[in.ID] = true
		got = append(got, in)
		return true, nil
	}
	ready := make(chan struct{})
	bot := New(cfg)
	bot.Receive, bot.Ready = receive, func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { bot.Run(ctx); close(done) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the bot never became ready")
	}

	f.mu.Lock()
	f.dropAt = 8 // after the thread message, so the rest arrive on the resumed session
	f.mu.Unlock()
	f.events <- message("11", "100", "20", "明日の予定を教えて", "")
	f.events <- message("11", "100", "20", "明日の予定を教えて", "")                                // the same message again
	f.events <- message("12", "100", "1", "自分の投稿", "")                                     // the bot itself
	f.events <- message("13", "100", "30", "ほかのbot", `,"webhook_id":"5"`)                  // a webhook
	f.events <- strings.Replace(message("14", "100", "20", "別のサーバー", ""), `"G"`, `"X"`, 1) // another guild
	f.events <- message("15", "300", "20", "聞いていないチャンネル", "")
	f.events <- message("16", "200", "20", "スレッドの続き", "")
	f.events <- message("17", "201", "20", "他人のスレッド", "")
	f.events <- message("18", "202", "20", "別の親のスレッド", "")
	f.events <- message("19", "100", "20", "スレッドで詳しく教えて", "")
	f.events <- message("20", "100", "20", "", `,"attachments":[{"id":"400","filename":"a.png","url":"https://cdn.example/a.png"}]`)

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		f.mu.Lock()
		n = min(n, len(f.reacted)) // a reaction follows its request
		f.mu.Unlock()
		if n >= 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	var ids []string
	for _, in := range got {
		if in.ID == "20" {
			if strings.Contains(in.Text, "https://") || len(in.Images) != 1 || in.Images[0].Channel != "100" || in.Images[0].Attachment != "400" {
				t.Errorf("attachment-only input: %+v", in)
			}
		}
		ids = append(ids, in.ID+"@"+in.Channel)
	}
	if strings.Join(ids, " ") != "11@100 16@200 19@7001 20@100" {
		t.Fatalf("accepted = %v", ids)
	}
	if got[1].SourceChannel != "200" || got[2].SourceChannel != "100" {
		t.Fatalf("wrong source for existing or newly created thread: %q / %q", got[1].SourceChannel, got[2].SourceChannel)
	}
	if got[0].Author != "X" || got[0].AuthorID != "20" || got[0].Text != "明日の予定を教えて" || !strings.Contains(got[3].Text, "a.png") {
		t.Errorf("incoming = %+v / %+v", got[0], got[3])
	}
	f.mu.Lock()
	reacted := strings.Join(f.reacted, " ")
	f.mu.Unlock()
	if reacted != "11 16 19 20" || f.resumes == 0 {
		t.Errorf("reactions = %q, resumed %d times", reacted, f.resumes)
	}

	if err := bot.Send(context.Background(), "100", "r-abc", strings.Repeat("長い返事です。", 400)+"@everyone"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.posts) != 2 || f.posts[0]["nonce"] == f.posts[1]["nonce"] || f.posts[0]["enforce_nonce"] != true ||
		len(f.posts[0]["allowed_mentions"].(map[string]any)["parse"].([]any)) != 0 || f.posts[0]["channel"] != "100" {
		t.Errorf("posts = %v", f.posts)
	}
	f.mu.Unlock()
	cancel()
	<-done

	// After a restart, only what arrived in between is picked up.
	f.mu.Lock()
	f.history["100"] = append(f.history["100"], Message{ID: "20"}, Message{ID: "21", Content: "止まっているあいだの依頼"})
	f.history["100"][2].Author.ID, f.history["100"][1].Author.ID = "20", "20"
	f.mu.Unlock()
	mu.Unlock()
	again := New(cfg)
	ready2 := make(chan struct{})
	again.Receive, again.Ready = receive, func() { close(ready2) }
	ctx2, cancel2 := context.WithCancel(context.Background())
	go again.Run(ctx2)
	<-ready2
	cancel2()
	mu.Lock()
	if last := got[len(got)-1]; len(got) != 5 || last.ID != "21" {
		t.Errorf("after restart: %d requests, last %+v", len(got), last)
	}
}

// A connection found dead while a message is handled must not cancel the
// handling: the resumed session will not send that message again.
func TestBotFinishesAMessageWhenTheConnectionDies(t *testing.T) {
	f := newFake(t)
	f.beat = 20 // a slow Receive keeps the acknowledgements unread, so the bot drops the connection
	cfg := Config{Token: "the-token", GuildID: "G", Channels: []string{"100"}, AnyHuman: true, State: testDir(t, t.TempDir()), StateName: "discord.json",
		api: f.server.URL + "/api", gateway: "ws" + strings.TrimPrefix(f.server.URL, "http") + "/gateway"}
	bot := New(cfg)
	ready := make(chan struct{})
	bot.Ready = func() { close(ready) }
	bot.Receive = func(Incoming) (bool, error) { time.Sleep(200 * time.Millisecond); return true, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bot.Run(ctx)
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the bot never became ready")
	}
	f.events <- message("11", "100", "20", "明日の予定を教えて", "")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		f.mu.Lock()
		reacted := strings.Join(f.reacted, " ")
		f.mu.Unlock()
		if reacted == "11" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reactions = %q", reacted)
		}
	}
}
