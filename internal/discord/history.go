package discord

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// HistoryMessage contains text and attachment metadata, never signed CDN URLs.
type HistoryMessage struct {
	ID          string              `json:"id"`
	Channel     string              `json:"channel"`
	AuthorID    string              `json:"author_id"`
	Author      string              `json:"author"`
	Text        string              `json:"text"`
	At          time.Time           `json:"at"`
	Attachments []HistoryAttachment `json:"attachments,omitempty"`
}

type HistoryAttachment struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Size int64  `json:"size"`
}

// HistoryPage is newest first. NextBefore advances over filtered messages too.
type HistoryPage struct {
	Messages   []HistoryMessage `json:"messages"`
	NextBefore string           `json:"next_before,omitempty"`
}

// History reads only the triggering channel/thread. The caller binds channel and
// requestMessage to the current request, rather than accepting them from a model.
// Revalidating the anchor also checks the bot's current access to the channel.
func (b *Bot) History(ctx context.Context, channel, requestMessage, before string, limit int) (HistoryPage, error) {
	empty := HistoryPage{Messages: []HistoryMessage{}}
	if !snowflake.MatchString(channel) || !snowflake.MatchString(requestMessage) || (before != "" && !snowflake.MatchString(before)) || limit < 1 || limit > 100 {
		return empty, errors.New("チャンネルと投稿のIDは1〜20桁の数字で指定してください。1回に指定できる件数は1〜100件です。")
	}
	anchor, err := b.imageMessage(ctx, ImageRef{Channel: channel, Message: requestMessage})
	if err != nil {
		return empty, err
	}
	if b.paused(anchor.Author.ID) {
		return empty, errors.New("この方からの依頼は、一時的に受け付けていません。")
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if before != "" {
		q.Set("before", before)
	}
	var messages []Message
	if err := b.rest.do(ctx, "GET", "/channels/"+channel+"/messages?"+q.Encode(), nil, &messages); err != nil {
		return empty, err
	}
	b.mu.Lock()
	botID := b.botID
	b.mu.Unlock()
	page := empty
	for _, m := range messages {
		if !snowflake.MatchString(m.ID) || m.ChannelID != channel || (m.GuildID != "" && m.GuildID != b.cfg.GuildID) || !m.isConversation() || m.WebhookID != "" || m.Author.ID == "" {
			continue
		}
		own := botID != "" && m.Author.ID == botID
		if !own {
			if m.Author.Bot {
				continue
			}
			if len(b.cfg.Members) > 0 {
				if _, ok := b.member(m); !ok {
					continue
				}
			} else if !b.cfg.AnyHuman && !has(b.cfg.Authors, m.Author.ID) {
				continue
			}
		}
		item := HistoryMessage{ID: m.ID, Channel: channel, AuthorID: m.Author.ID, Author: m.Name(), Text: m.Content, At: SentAt(m.ID)}
		if own {
			item.Author = "レイシア"
		}
		for _, a := range m.Attachments {
			item.Attachments = append(item.Attachments, HistoryAttachment{Name: a.Filename, Type: a.ContentType, Size: a.Size})
		}
		page.Messages = append(page.Messages, item)
	}
	if len(messages) == limit {
		last := messages[len(messages)-1].ID
		if snowflake.MatchString(last) && (before == "" || newer(before, last)) {
			page.NextBefore = last
		}
	}
	return page, nil
}
