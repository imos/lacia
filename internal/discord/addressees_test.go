package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSendAddressees(t *testing.T) {
	type post struct {
		Content string `json:"content"`
		Allowed struct {
			Parse   []string `json:"parse"`
			Users   []string `json:"users"`
			Replied bool     `json:"replied_user"`
		} `json:"allowed_mentions"`
	}
	var posts []post
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p post
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		posts = append(posts, p)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	b := New(Config{api: server.URL, State: filepath.Join(t.TempDir(), "state.json"), Members: []Member{
		{ID: "1", Name: "美咲さん"}, {ID: "2", Name: "直人さん"}, {ID: "3", Name: "同名さん"}, {ID: "4", Name: "同名さん"}, {Username: "unbound", Name: "未確定さん"},
	}})
	text := "[[to:直人]] [[to:美咲]]\n直人さん、美咲さん、説明します。\n" + strings.Repeat("続きの本文です。", 500) + "\n[[to:直人]]\n<@2> 続きです。\n[[to:同名]] [[to:未確定]] [[to:知らない人]] [[to:everyone]] [[to:<@&5>]]\n@everyone @here <@&5> <@99>\n```\n[[to:美咲]]\n```"
	if err := b.Send(context.Background(), "channel", "reply-key", text); err != nil {
		t.Fatal(err)
	}
	if len(posts) < 2 {
		t.Fatal("expected split posts")
	}
	var users []string
	var content strings.Builder
	for _, p := range posts {
		if len(p.Allowed.Parse) != 0 || p.Allowed.Replied {
			t.Fatalf("unsafe allowed mentions: %+v", p.Allowed)
		}
		users = append(users, p.Allowed.Users...)
		content.WriteString(p.Content)
	}
	if !reflect.DeepEqual(users, []string{"2", "1"}) {
		t.Fatalf("notifications, with duplicates or unknown users: %v", users)
	}
	if !strings.HasPrefix(content.String(), "<@2> <@1>\n直人さん") {
		t.Fatal(content.String())
	}
	if !strings.Contains(content.String(), "同名 未確定 知らない人 everyone") {
		t.Fatal("unknown recipients did not remain plain")
	}
	if !strings.Contains(content.String(), "```\n[[to:美咲]]\n```") {
		t.Fatal("code annotation was changed")
	}
	posts = nil
	if err := b.Send(context.Background(), "channel", "legacy-key", "<@!1> 既存の直接メンションです。<@2> <@99> @everyone <@&5>"); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || !reflect.DeepEqual(posts[0].Allowed.Users, []string{"1", "2"}) {
		t.Fatalf("legacy mentions: %+v", posts)
	}
}

func TestResolveAddresseeDoesNotGuess(t *testing.T) {
	b := New(Config{State: filepath.Join(t.TempDir(), "state.json"), Members: []Member{
		{ID: "1", Name: "同名さん"}, {Username: "other", Name: "同名さん"}, {Username: "unbound", Name: "未確定さん"},
	}})
	for _, who := range []string{"同名", "未確定", "unknown", "@everyone", "@here", "<@&1>", "9"} {
		if value, ok := b.resolveAddressee(who); ok {
			t.Fatalf("resolved %q to %q", who, value)
		}
	}
	if value, ok := b.resolveAddressee("1"); !ok || value != "<@1>" {
		t.Fatalf("known ID: %q %v", value, ok)
	}
	b.member(authored("8", "unbound"))
	if value, ok := b.resolveAddressee("未確定"); !ok || value != "<@8>" {
		t.Fatalf("bound member: %q %v", value, ok)
	}
}
