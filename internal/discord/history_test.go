package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func humanMessage(id, channel, author string) Message {
	m := Message{ID: id, ChannelID: channel, GuildID: "90", Content: "本文"}
	m.Author.ID = author
	return m
}

func TestMonitorAllChannelsReviewsOnlyHumanPosts(t *testing.T) {
	lookups, reactions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			reactions++
			w.WriteHeader(204)
			return
		}
		lookups++
		json.NewEncoder(w).Encode(Channel{ID: "200", GuildID: "90", Type: typeText})
	}))
	defer server.Close()
	b := New(Config{GuildID: "90", Channels: []string{"100"}, MonitorAllChannels: true, Members: []Member{{ID: "20"}, {ID: "21", Role: Child}}, State: testDir(t, t.TempDir()), StateName: "state", api: server.URL})
	b.botID = "1"
	var received []Incoming
	b.Receive = func(in Incoming) (bool, error) { received = append(received, in); return true, nil }
	cases := []Message{humanMessage("1", "200", "20"), humanMessage("2", "200", "20"), humanMessage("3", "200", "20"), humanMessage("4", "200", "20"), humanMessage("5", "200", "99"), humanMessage("6", "200", "20")}
	cases[0].Author.Bot = true
	cases[1].WebhookID = "9"
	cases[2].Type = 7
	cases[3].GuildID = "91"
	for _, m := range cases[:5] {
		b.handle(context.Background(), m, m.GuildID)
	}
	if lookups != 0 || len(received) != 0 {
		t.Fatalf("excluded posts did work: lookups=%d received=%v", lookups, received)
	}
	b.handle(context.Background(), cases[5], "90")
	if len(received) != 1 || !received[0].ReviewReply || reactions != 0 {
		t.Fatalf("review: %+v reactions=%d", received, reactions)
	}
	direct := humanMessage("7", "200", "20")
	direct.Content = "<@1> お願いします"
	direct.Mentions = []messageMention{{ID: "1"}}
	b.handle(context.Background(), direct, "90")
	if len(received) != 2 || received[1].ReviewReply || reactions != 1 {
		t.Fatalf("direct: %+v reactions=%d", received, reactions)
	}
	b.state.Paused = map[string]time.Time{"21": time.Now().Add(time.Hour)}
	b.handle(context.Background(), humanMessage("8", "200", "21"), "90")
	if len(received) != 2 || reactions != 1 {
		t.Fatalf("pause started work or reacted: %+v %d", received, reactions)
	}
	b.handle(context.Background(), humanMessage("9", "100", "20"), "90")
	if len(received) != 3 || received[2].ReviewReply {
		t.Fatalf("home channel: %+v", received)
	}
}

func TestHistoryRevalidatesAnchorAndFiltersWithPagination(t *testing.T) {
	anchor := humanMessage("500", "200", "20")
	good := humanMessage("490", "200", "20")
	good.Attachments = []Attachment{{Filename: "photo.png", ContentType: "image/png", Size: 12, URL: "https://secret.invalid/"}}
	own := humanMessage("480", "200", "1")
	own.Author.Bot = true
	bot := humanMessage("470", "200", "2")
	bot.Author.Bot = true
	unregistered := humanMessage("460", "200", "99")
	webhook := humanMessage("450", "200", "20")
	webhook.WebhookID = "9"
	system := humanMessage("440", "200", "20")
	system.Type = 6
	foreign := humanMessage("430", "200", "20")
	foreign.GuildID = "91"
	otherChannel := humanMessage("420", "201", "20")
	posts := []Message{good, own, bot, unregistered, webhook, system, foreign, otherChannel}
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/200":
			json.NewEncoder(w).Encode(Channel{ID: "200", GuildID: "90", Type: typeText})
		case "/channels/200/messages/500":
			json.NewEncoder(w).Encode(anchor)
		case "/channels/200/messages":
			listCalls++
			if r.URL.Query().Get("limit") != "8" {
				t.Errorf("limit=%s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(posts)
		default:
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	b := New(Config{GuildID: "90", MonitorAllChannels: true, Members: []Member{{ID: "20"}}, State: testDir(t, t.TempDir()), StateName: "state", api: server.URL})
	b.botID = "1"
	page, err := b.History(context.Background(), "200", "500", "", 8)
	if err != nil || len(page.Messages) != 2 || page.NextBefore != "420" || page.Messages[1].Author != "レイシア" || page.Messages[0].Attachments[0].Name != "photo.png" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	data, _ := json.Marshal(page)
	if strings.Contains(string(data), "secret.invalid") {
		t.Fatal("signed URL leaked")
	}
	for _, args := range [][3]string{{"201", "500", ""}, {"200", "999", ""}, {"200", "500", "bad"}} {
		if _, err := b.History(context.Background(), args[0], args[1], args[2], 8); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	anchor.Author.ID = "99"
	if _, err := b.History(context.Background(), "200", "500", "", 8); err == nil {
		t.Fatal("unregistered anchor accepted")
	}
	if listCalls != 1 {
		t.Fatalf("unauthorized list calls: %d", listCalls)
	}
}

func TestMonitorCatchUpDiscoversChannelsAndSkipsFirstHistory(t *testing.T) {
	historyCalls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/guilds/90/channels":
			json.NewEncoder(w).Encode([]Channel{{ID: "200", Type: typeText}, {ID: "300", Type: 4}})
		case "/guilds/90/threads/active":
			json.NewEncoder(w).Encode(map[string]any{"threads": []Channel{{ID: "201", GuildID: "90", Type: typePublicThread, ParentID: "200"}}})
		case "/channels/200", "/channels/201":
			json.NewEncoder(w).Encode(Channel{ID: r.URL.Path[len("/channels/"):], GuildID: "90", Type: typeText})
		case "/channels/200/messages", "/channels/201/messages":
			channel := strings.Split(r.URL.Path, "/")[2]
			historyCalls[channel]++
			m := humanMessage("500", channel, "20")
			if r.URL.Query().Get("after") == "500" {
				m.ID = "501"
			}
			json.NewEncoder(w).Encode([]Message{m})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	b := New(Config{GuildID: "90", MonitorAllChannels: true, Authors: []string{"20"}, State: testDir(t, t.TempDir()), StateName: "state", api: server.URL})
	var received []Incoming
	b.Receive = func(in Incoming) (bool, error) { received = append(received, in); return true, nil }
	b.catchUp(context.Background())
	if len(received) != 0 || b.state.Cursors["200"] != "500" || b.state.Cursors["201"] != "500" {
		t.Fatalf("initial history replayed: %+v %+v", received, b.state.Cursors)
	}
	b.catchUp(context.Background())
	if len(received) != 2 || !received[0].ReviewReply || !received[1].ReviewReply || historyCalls["200"] != 2 || historyCalls["201"] != 2 {
		t.Fatalf("catch-up failed: %+v %+v", received, historyCalls)
	}
}
