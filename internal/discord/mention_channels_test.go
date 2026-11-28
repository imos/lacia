package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/daylog"
)

func TestMentionChannelsAcceptOnlyDirectMentionsFromPermittedPeople(t *testing.T) {
	f := newFake(t)
	f.channels["100"] = Channel{ID: "100", GuildID: "G", Name: "lacia"}
	f.channels["300"] = Channel{ID: "300", GuildID: "G", Name: "general"}
	f.channels["301"] = Channel{ID: "301", GuildID: "G", Type: typeVoice}
	f.channels["302"] = Channel{ID: "302", GuildID: "G", Type: typePublicThread, ParentID: "300"}
	f.channels["303"] = Channel{ID: "303", GuildID: "Other"}
	f.channels["304"] = Channel{ID: "304", GuildID: "G", Type: 4}
	f.channels["200"] = Channel{ID: "200", GuildID: "G", Type: typePublicThread, ParentID: "100", OwnerID: "1"}

	dir := t.TempDir()
	cfg := Config{Token: "the-token", GuildID: "G", Channels: []string{"100"}, MentionAllChannels: true,
		Members: []Member{{ID: "20", Name: "許可ユーザー", Role: Adult}, {ID: "21", Name: "一時停止中のユーザー", Role: Child}},
		State:   testDir(t, dir), StateName: "discord.json", api: f.server.URL + "/api"}
	bot := New(cfg)
	bot.botID = "1"
	bot.Log = daylog.Discord(testDir(t, dir), "log")

	var got []string
	bot.Receive = func(in Incoming) (bool, error) {
		got = append(got, in.ID+"@"+in.Channel)
		if in.SourceChannel != in.Channel {
			t.Errorf("new-channel reply was routed away from the source: %+v", in)
		}
		return true, nil
	}
	if _, _, err := bot.Pause("21", time.Hour); err != nil {
		t.Fatal(err)
	}
	mention := `,"mentions":[{"id":"1","bot":true}]`
	cases := []string{
		message("10", "100", "20", "いつものチャンネルです", ""),
		message("11", "200", "20", "いつものスレッドです", ""),
		message("12", "300", "20", "保存対象外の普通の発言", ""),
		message("13", "300", "20", "保存対象外の返信の通知", mention),
		message("14", "300", "20", "<@1> 保存対象外の平文", ""),
		message("15", "300", "99", "<@1> 保存対象外の未登録", mention),
		message("16", "300", "20", "<@1> 保存対象外のウェブフック", mention+`,"webhook_id":"4"`),
		message("17", "300", "1", "<@1> 保存対象外の自分の投稿", mention),
		strings.Replace(message("18", "300", "50", "<@1> 保存対象外の他のBot", mention), `"global_name":"X"`, `"global_name":"X","bot":true`, 1),
		strings.Replace(message("19", "300", "20", "<@1> 保存対象外の別ギルド", mention), `"guild_id":"G"`, `"guild_id":"Other"`, 1),
		message("20", "303", "20", "<@1> 保存対象外の別ギルドのチャンネル", mention),
		message("21", "304", "20", "<@1> 保存対象外のカテゴリー", mention),
		message("22", "300", "21", "<@1> 受付を一時停止しています", mention),
		message("23", "300", "20", "<@1> スレッドで詳しく教えて", mention),
		message("24", "301", "20", "<@!1> ボイスのチャット欄です", mention),
		message("25", "302", "20", "<@1> 別チャンネルのスレッドです", mention),
		message("26", "302", "20", "保存対象外のメンションなしの続き", ""),
		message("27", "300", "20", "`<@1>` 保存対象外のコード例", mention),
		message("28", "300", "20", "\\<@1> 保存対象外のエスケープされた例", mention),
	}
	for _, payload := range cases {
		var m Message
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatal(err)
		}
		bot.handle(context.Background(), m, m.GuildID)
	}
	if want := []string{"10@100", "11@200", "23@300", "24@301", "25@302"}; !reflect.DeepEqual(got, want) {
		t.Errorf("accepted = %v, want %v", got, want)
	}
	if log := readLogOrEmpty(bot.Log, time.Now()); strings.Contains(log, "保存対象外") || !strings.Contains(log, "ボイスのチャット欄です") {
		t.Errorf("unexpected Discord log: %s", log)
	}

	// Disabling the setting keeps the Discord gateway and the old routes.
	bot.cfg.MentionAllChannels = false
	var m Message
	json.Unmarshal([]byte(message("29", "300", "20", "<@1> 追加の受付を止めています", mention)), &m)
	bot.handle(context.Background(), m, m.GuildID)
	if len(got) != 5 {
		t.Errorf("opt-out accepted a request in another channel: %v", got)
	}
}

func TestMentionChannelsRecoverFromTheirFirstRequestAfterRestart(t *testing.T) {
	f := newFake(t)
	for _, id := range []string{"100", "300", "301"} {
		f.channels[id] = Channel{ID: id, GuildID: "G"}
	}
	cfg := Config{GuildID: "G", Channels: []string{"100"}, MentionAllChannels: true, Authors: []string{"20"},
		State: testDir(t, t.TempDir()), StateName: "discord.json", api: f.server.URL + "/api"}
	authorMessage := func(id, channel, text string) Message {
		m := Message{ID: id, ChannelID: channel, Content: text}
		m.Author.ID = "20"
		m.Mentions = []messageMention{{ID: "1"}}
		return m
	}
	f.history["300"] = []Message{
		authorMessage("20", "300", "<@1> 参加する前の投稿"),
		authorMessage("30", "300", "<@1> 最初のメンション"),
		authorMessage("31", "300", "メンションのない普段の投稿"),
		authorMessage("32", "300", "<@1> 再接続までに届いたメンション"),
	}
	f.history["301"] = []Message{authorMessage("29", "301", "<@1> まだ話したことのない場所")}
	bot := New(cfg)
	bot.botID = "1"
	bot.Receive = func(Incoming) (bool, error) { return false, errors.New("cannot store request") }
	if bot.handle(context.Background(), f.history["300"][1], "G") {
		t.Fatal("storage failure was not reported")
	}

	bot = New(cfg)
	bot.botID = "1"
	var got []string
	bot.Receive = func(in Incoming) (bool, error) { got = append(got, in.ID); return true, nil }
	bot.catchUp(context.Background())
	bot.catchUp(context.Background())
	if want := []string{"30", "32"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed %v, want %v", got, want)
	}
}

func TestMentionChannelAttachmentsKeepGuildChannelAndAuthorChecks(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/300":
			json.NewEncoder(w).Encode(Channel{ID: "300", GuildID: "G", Type: typeVoice})
		case "/channels/301":
			json.NewEncoder(w).Encode(Channel{ID: "301", GuildID: "Elsewhere"})
		case "/channels/302":
			json.NewEncoder(w).Encode(Channel{ID: "302", GuildID: "G"})
		case "/channels/300/messages/550", "/channels/300/messages/551":
			id := filepath.Base(r.URL.Path)
			m := Message{ID: id, ChannelID: "300", Attachments: []Attachment{{ID: "700"}}}
			m.Author.ID = "20"
			if id == "551" {
				m.Author.ID = "40"
			}
			json.NewEncoder(w).Encode(m)
		default:
			t.Errorf("unexpected Discord lookup: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	bot := New(Config{GuildID: "G", Channels: []string{"100"}, MentionAllChannels: true, Authors: []string{"20"},
		State: testDir(t, t.TempDir()), StateName: "discord.json", api: api.URL})
	bot.state.MentionChannels["300"] = true
	bot.state.MentionChannels["301"] = true
	refs, err := bot.HistoryImages(context.Background(), "300", "550")
	if err != nil || len(refs) != 1 || refs[0].Channel != "300" {
		t.Fatalf("history in the source channel: %v, %v", refs, err)
	}
	if _, err := bot.imageMessage(context.Background(), refs[0]); err != nil {
		t.Fatalf("refresh of an authorized image: %v", err)
	}
	for _, pair := range [][2]string{{"301", "550"}, {"302", "550"}, {"300", "551"}} {
		if _, err := bot.HistoryImages(context.Background(), pair[0], pair[1]); err == nil {
			t.Errorf("accepted an unauthorized image: %v", pair)
		}
	}
}
