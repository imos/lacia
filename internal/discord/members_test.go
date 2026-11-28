package discord

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func authored(id, username string) Message {
	var m Message
	m.Author.ID, m.Author.Username = id, username
	return m
}

func TestMembersAreKnownByIDAndBoundByUsernameOnce(t *testing.T) {
	cfg := Config{State: testDir(t, t.TempDir()), StateName: "discord.json", Members: []Member{
		{ID: "1", Name: "佐藤さん", Role: Adult},
		{Username: "Example_Child", Name: "Xさん", Role: Child},
	}}
	b := New(cfg)
	if _, ok := b.member(authored("9", "stranger")); ok {
		t.Fatal("a stranger was accepted")
	}
	if _, _, err := b.Pause("X", time.Hour); err == nil {
		t.Fatal("paused a member whose ID is not known yet")
	}
	if m, ok := b.member(authored("7", "example_child")); !ok || m.ID != "7" || m.Role != Child {
		t.Fatalf("the child was not bound: %+v %v", m, ok)
	}
	// Somebody else taking the username later is not the child.
	if _, ok := b.member(authored("8", "example_child")); ok {
		t.Fatal("a second account with the same username was accepted")
	}
	// The child is still known after renaming, and after a restart.
	b = New(cfg)
	if m, ok := b.member(authored("7", "renamed")); !ok || m.Name != "Xさん" {
		t.Fatalf("the binding was lost: %+v %v", m, ok)
	}

	if _, _, err := b.Pause("佐藤さん", time.Hour); err == nil {
		t.Fatal("an adult was paused")
	}
	if _, _, err := b.Pause("X", 25*time.Hour); err == nil {
		t.Fatal("a pause longer than a day was accepted")
	}
	if _, until, err := b.Pause("X", time.Hour); err != nil || !b.paused("7") || time.Until(until) < 59*time.Minute {
		t.Fatalf("pause: %v", err)
	}
	if list := New(cfg).Members(); len(list) != 2 || list[1].ID != "7" || list[1].PausedUntil == nil {
		t.Fatalf("members after restart: %+v", list)
	}
	if _, err := b.Resume("7"); err != nil || b.paused("7") {
		t.Fatalf("resume: %v", err)
	}
}

func TestOnlyMembersCanBeMentioned(t *testing.T) {
	b := New(Config{State: testDir(t, t.TempDir()), StateName: "discord.json", Members: []Member{
		{ID: "1", Name: "佐藤さん", Role: Adult},
		{Username: "example_child", Name: "Xさん", Role: Child},
	}})
	if got := b.mentioned("<@7> 雨です。"); len(got) != 0 {
		t.Fatalf("mentioned a member before the ID was bound: %v", got)
	}
	b.member(authored("7", "example_child"))
	if got := b.mentioned("<@7> 雨です。<@9> @everyone"); len(got) != 1 || got[0] != "7" {
		t.Fatalf("mentioned = %v", got)
	}
	if got := b.mentioned("Xさん、雨です。"); got != nil {
		t.Fatalf("mentioned = %v", got)
	}
}

func TestConfiguredMembersRejectUnknownEvenWithChannelHumans(t *testing.T) {
	var table struct {
		Members []Member `json:"members"`
	}
	if err := json.Unmarshal([]byte(`{"members":[{"id":"known-adult","name":"大人の検証用","role":"adult"},{"id":"known-child","name":"子どもの検証用","role":"child"}]}`), &table); err != nil {
		t.Fatal(err)
	}
	b := New(Config{State: testDir(t, t.TempDir()), StateName: "state.json", GuildID: "guild", Channels: []string{"channel"}, Members: table.Members, AnyHuman: true, Authors: []string{"stranger"}})
	var got []Member
	b.Receive = func(in Incoming) (bool, error) { got = append(got, in.Member); return false, nil }
	for i, id := range []string{"stranger", "known-adult", "known-child"} {
		msg := authored(id, "user")
		msg.ID = string(rune('1' + i))
		msg.ChannelID = "channel"
		msg.Content = "検証の質問です。"
		b.handle(context.Background(), msg, "guild")
	}
	if len(got) != 2 {
		t.Fatalf("accepted %d requests, want only the two configured members", len(got))
	}
	for i, m := range got {
		if m != table.Members[i] {
			t.Error("accepted member identity or role changed")
		}
	}
}
