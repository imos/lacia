package main

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/imos/lacia/internal/discord"
	"github.com/imos/lacia/internal/request"
)

const (
	mailOwner  = "owner"
	mailFamily = "family"
)

type discordMailSettings struct {
	Connector string                      `json:"connector"`
	Account   string                      `json:"account"`
	Topics    map[string]discordMailTopic `json:"topics"`
}

type discordMailTopic struct {
	From []string `json:"from"`
	To   string   `json:"to"`
}

var (
	mailMemberID  = regexp.MustCompile(`^[0-9]+$`)
	mailConnector = regexp.MustCompile(`^connector_[a-f0-9]{32}$`)
	mailAddress   = regexp.MustCompile(`^[A-Za-z0-9._+%-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	mailDomain    = regexp.MustCompile(`^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	mailQuestion  = regexp.MustCompile(`^(.{1,48}?)(?:の|から(?:の)?)(?:(?:登録|確認)メール|メール|返信)(?:は|が|、)?[ 　]*(?:来た|きた|届いた|来てる|来ている|届いてる|届いている|届きましたか|来ましたか)[？?。]?$`)
)

func (f members) validateMail() error {
	for _, m := range f.Members {
		if m.MailAccess == "" {
			continue
		}
		if f.Mail == nil || m.Role != discord.Adult || !mailMemberID.MatchString(m.ID) || (m.MailAccess != mailOwner && m.MailAccess != mailFamily) {
			return errors.New("メール確認を許可する方には、DiscordのIDと、本人か家族かを指定してください")
		}
	}
	if f.Mail == nil {
		return nil
	}
	if !mailConnector.MatchString(f.Mail.Connector) || !mailAddress.MatchString(f.Mail.Account) {
		return errors.New("メール確認に使うアカウントとメール連携のIDを指定してください")
	}
	for name, topic := range f.Mail.Topics {
		if name == "" || strings.ContainsAny(name, "\r\n") || !mailAddress.MatchString(topic.To) || len(topic.From) == 0 || len(topic.From) > 8 {
			return errors.New("家族と共有するメール確認の名前、送信元、宛先を指定してください")
		}
		for _, from := range topic.From {
			if !mailAddress.MatchString(from) && !mailDomain.MatchString(from) {
				return errors.New("メールの送信元には、実際のメールアドレスかドメイン名を指定してください")
			}
		}
	}
	return nil
}

// mailTarget recognizes the explicit command and ordinary questions about
// a configured topic. Other Discord conversation goes to its usual runner.
func (f members) mailTarget(text string) (string, bool) {
	text = strings.TrimSpace(text)
	const prefix = "メール確認"
	if rest, ok := strings.CutPrefix(text, prefix); ok && (rest == "" || strings.ContainsRune(" :：　", []rune(rest)[0])) {
		return strings.Trim(rest, " :：　"), true
	}
	match := mailQuestion.FindStringSubmatch(text)
	if len(match) > 1 {
		if _, ok := f.Mail.Topics[match[1]]; ok {
			return match[1], true
		}
	}
	return "", false
}

func (f members) mailQuery(r request.Request) (query string, reply string) {
	author, _ := strings.CutPrefix(r.Author, "discord:")
	access := ""
	for _, m := range f.Members {
		if m.ID != "" && m.ID == author && m.Role == discord.Adult {
			access = m.MailAccess
			break
		}
	}
	if access == "" || r.DiscordSourceChannel == "" || !strings.HasPrefix(r.ReplyTo, "discord:") {
		return "", "メールは、確認を任されている方の依頼でお調べします。"
	}
	if r.DiscordMailTarget == "" {
		return "", "「メール確認」に続けて、確認したい用件を書いてください。"
	}
	if topic, ok := f.Mail.Topics[r.DiscordMailTarget]; ok {
		from := make([]string, 0, len(topic.From))
		for _, value := range topic.From {
			from = append(from, "from:"+value)
		}
		return "{" + strings.Join(from, " ") + "} to:" + topic.To, ""
	}
	if access == mailOwner && mailAddress.MatchString(r.DiscordMailTarget) {
		return "from:" + r.DiscordMailTarget, ""
	}
	if access == mailOwner {
		return "", "送信元のメールアドレスか、ご家族で共有する登録済みの用件を指定してください。詳しい確認は仕事モードのWebでお受けします。"
	}
	return "", "ご家族で共有する用件として登録されているものを確認できます。この件は、メールの持ち主にお尋ねください。"
}

func (f members) withMail(run request.Runner, lookup func(context.Context, string, string) (bool, error)) request.Runner {
	return func(ctx context.Context, text string) (string, string, error) {
		r := request.Current(ctx)
		if f.Mail == nil || !r.DiscordMail || r.Source != "Discord" || r.Kind != request.User {
			return run(ctx, text)
		}
		query, reply := f.mailQuery(r)
		if reply != "" {
			return reply, "", nil
		}
		ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		found, err := lookup(ctx, f.Mail.Account, query+" newer_than:14d -in:sent -in:trash")
		switch {
		case err != nil:
			slog.Warn("discord mail lookup failed", "request", r.ID)
			return "メールの接続先か検索結果を確かめられませんでした。少し待ってから、もう一度お尋ねください。", "", nil
		case found:
			return "直近14日間で、条件に合うメールが見つかりました。詳しい内容は、メールを受け取った方がGmailで確認できます。", "", nil
		default:
			return "直近14日間には、条件に合うメールが見つかりませんでした。", "", nil
		}
	}
}
