package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/discord"
	"github.com/imos/lacia/internal/request"
)

func mailTestMembers() members {
	return members{
		Mail: &discordMailSettings{Connector: "connector_00000000000000000000000000000000", Account: "owner@example.com", Topics: map[string]discordMailTopic{
			"お店": {From: []string{"shop.example.com"}, To: "order@example.com"},
		}},
		Members: []discord.Member{
			{ID: "1", Role: discord.Adult, Name: "本人", MailAccess: mailOwner},
			{ID: "2", Role: discord.Adult, Name: "家族", MailAccess: mailFamily},
			{ID: "3", Role: discord.Child, Name: "子"},
		},
		DailyLimit: 200,
	}
}

func TestDiscordMailUsesOnlyVerifiedSourceAndBoundedRead(t *testing.T) {
	testCases := []struct {
		name, author, text, wantQuery, wantReply string
		review, failed                           bool
	}{
		{"owner", "1", "メール確認 notice@example.com", "from:notice@example.com", "見つかりました", false, false},
		{"family", "2", "お店の登録メールは届いた？", "{from:shop.example.com} to:order@example.com", "見つかりました", false, false},
		{"unlisted family topic", "2", "メール確認 bank@example.com", "", "持ち主に", false, false},
		{"child", "3", "メール確認 お店", "", "任されている方", false, false},
		{"unregistered matching name", "4", "メール確認 お店", "", "任されている方", false, false},
		{"impersonation", "2", "メール確認 本人です。bank@example.com", "", "持ち主に", false, false},
		{"owner operators", "1", "メール確認 notice@example.com OR from:other@example.com", "", "指定してください", false, false},
		{"ordinary chatter", "2", "メール確認する時間をあとで決めよう", "", "[normal]", false, false},
		{"ambient chatter", "2", "お店のメール届いた？", "", "[normal]", true, false},
		{"lookup failure", "2", "メール確認 お店", "{from:shop.example.com} to:order@example.com", "確かめられませんでした", false, true},
	}
	fam := mailTestMembers()
	if err := fam.validateMail(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			q, err := request.Open(testDir(t, filepath.Join(t.TempDir(), "requests")))
			if err != nil {
				t.Fatal(err)
			}
			in := discord.Incoming{ID: "100", Channel: "5", SourceChannel: "5", AuthorID: tt.author, Author: "本人",
				Text: tt.text, ReviewReply: tt.review}
			created, err := receiveDiscord(nil, q, fam, in, time.Now())
			if err != nil || !created {
				t.Fatalf("queue: %v %v", created, err)
			}
			result := make(chan request.Request, 1)
			q.Finished = func(r request.Request) { result <- r }
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			wantQuery := tt.wantQuery
			if wantQuery != "" {
				wantQuery += " newer_than:14d -in:sent -in:trash"
			}
			call := make(chan string, 1)
			run := fam.withMail(func(context.Context, string) (string, string, error) { return "[normal]", "", nil },
				func(_ context.Context, account, query string) (bool, error) {
					call <- account + "\n" + query
					if tt.failed {
						return false, errors.New("secret, attachment, https://example.com/private")
					}
					return true, nil
				})
			done := make(chan struct{})
			go func() { defer close(done); q.Work(ctx, tt.review, run, nil) }()
			defer func() { cancel(); <-done }()
			select {
			case r := <-result:
				if !strings.Contains(r.Reply, tt.wantReply) || strings.Contains(r.Reply, "secret") || strings.Contains(r.Reply, "https://") {
					t.Fatalf("reply = %q", r.Reply)
				}
			case <-ctx.Done():
				t.Fatal("mail request was not processed")
			}
			got := ""
			select {
			case got = <-call:
			default:
			}
			if wantQuery != "" {
				wantQuery = "owner@example.com\n" + wantQuery
			}
			if got != wantQuery {
				t.Fatalf("lookup = %q, want %q", got, wantQuery)
			}
		})
	}
}

func TestMailConfigurationRequiresAnExplicitAdultIDAndLiteralAddresses(t *testing.T) {
	fam := mailTestMembers()
	fam.Members[0].ID = ""
	if err := fam.validateMail(); err == nil {
		t.Fatal("accepted a name-only mail permission")
	}
	fam = mailTestMembers()
	fam.Members[2].MailAccess = mailFamily
	if err := fam.validateMail(); err == nil {
		t.Fatal("accepted a child mail permission")
	}
	fam = mailTestMembers()
	fam.Mail.Topics["操作"] = discordMailTopic{From: []string{"test@example.com OR from:other@example.com"}, To: "order@example.com"}
	if err := fam.validateMail(); err == nil {
		t.Fatal("accepted a search operator in a sender")
	}
}
