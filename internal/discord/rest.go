// Package discord connects Lacia to a Discord server: messages in the
// channel become requests, and replies are posted back.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// API is Discord's REST endpoint; tests point it at a fake.
const defaultAPI = "https://discord.com/api/v10"

// Message is the part of a Discord message that is used here.
type Message struct {
	Type      int    `json:"type"`
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Content   string `json:"content"`
	WebhookID string `json:"webhook_id"`
	Author    struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		Bot        bool   `json:"bot"`
	} `json:"author"`
	Member struct {
		Nick string `json:"nick"`
	} `json:"member"`
	Mentions    []messageMention `json:"mentions"`
	Attachments []Attachment     `json:"attachments"`
}

type messageMention struct {
	ID string `json:"id"`
}

// directlyMentions excludes role mentions and the implicit ping of a reply.
func (m Message) directlyMentions(id string) bool {
	if id == "" {
		return false
	}
	for _, mention := range m.Mentions {
		if mention.ID == id {
			return explicitMention(m.Content, "<@"+id+">", "<@!"+id+">")
		}
	}
	return false
}

// The mention list can include the replied-to user even when the apparent
// mention in the text is only an escaped example or code.
func explicitMention(text, plain, nickname string) bool {
	backticks := 0
	for len(text) > 0 {
		switch text[0] {
		case '\\':
			if backticks == 0 && len(text) > 1 {
				text = text[2:]
				continue
			}
		case '`':
			rest := strings.TrimLeft(text, "`")
			width := len(text) - len(rest)
			if backticks == 0 {
				backticks = width
			} else if backticks == width {
				backticks = 0
			}
			text = rest
			continue
		case '<':
			if backticks == 0 && (strings.HasPrefix(text, plain) || strings.HasPrefix(text, nickname)) {
				return true
			}
		}
		text = text[1:]
	}
	return false
}

// Attachment is Discord metadata, never an arbitrary URL in message text.
type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Name is what the author is called in this server.
func (m Message) Name() string {
	for _, n := range []string{m.Member.Nick, m.Author.GlobalName, m.Author.Username} {
		if n != "" {
			return n
		}
	}
	return m.Author.ID
}

// Channel is the part of a channel or thread that is used here.
type Channel struct {
	ID       string `json:"id"`
	Type     int    `json:"type"`
	GuildID  string `json:"guild_id"`
	ParentID string `json:"parent_id"`
	OwnerID  string `json:"owner_id"`
	Name     string `json:"name"`
}

const (
	typeText               = 0
	typeVoice              = 2
	typeAnnouncement       = 5
	typeAnnouncementThread = 10
	typePublicThread       = 11
	typePrivateThread      = 12
	typeStageVoice         = 13
)

// acceptsMessages covers guild channels with a message timeline. A forum
// or media post is a public thread, rather than a message in its parent.
func (c Channel) acceptsMessages() bool {
	switch c.Type {
	case typeText, typeVoice, typeAnnouncement, typeAnnouncementThread, typePublicThread, typePrivateThread, typeStageVoice:
		return true
	default:
		return false
	}
}

func (c Channel) isThread() bool {
	return c.Type == typeAnnouncementThread || c.Type == typePublicThread || c.Type == typePrivateThread
}

type rest struct {
	api   string
	token string
	http  *http.Client
}

// do sends one request, waiting out rate limits a few times.
func (r *rest) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, r.api+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bot "+r.token)
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/imos/lacia, 1)")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := r.http.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			var limit struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.Unmarshal(data, &limit)
			wait := time.Duration(limit.RetryAfter*float64(time.Second)) + 100*time.Millisecond
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if resp.StatusCode >= 300 {
			return fmt.Errorf("Discordが %s %s に %d を返しました", method, strings.Split(path, "?")[0], resp.StatusCode)
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

func (r *rest) channel(ctx context.Context, id string) (Channel, error) {
	var c Channel
	err := r.do(ctx, "GET", "/channels/"+id, nil, &c)
	return c, err
}

// messagesAfter returns up to 100 messages newer than after, oldest first.
// With after empty it returns only the newest message.
func (r *rest) messagesAfter(ctx context.Context, channelID, after string) ([]Message, error) {
	q := url.Values{"limit": {"100"}}
	if after == "" {
		q.Set("limit", "1")
	} else {
		q.Set("after", after)
	}
	var list []Message
	if err := r.do(ctx, "GET", "/channels/"+channelID+"/messages?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, nil
}

// post sends one message. Only the users listed in mention are notified;
// every other mention stays plain text. The nonce makes Discord drop an
// identical retry instead of posting it twice.
func (r *rest) post(ctx context.Context, channelID, content, nonce string, mention []string) error {
	allowed := map[string]any{"parse": []string{}, "replied_user": false}
	if len(mention) > 0 {
		allowed["users"] = mention
	}
	return r.do(ctx, "POST", "/channels/"+channelID+"/messages", map[string]any{
		"content": content, "nonce": nonce, "enforce_nonce": true,
		"allowed_mentions": allowed,
	}, nil)
}

func (r *rest) react(ctx context.Context, channelID, messageID, emoji string) error {
	return r.do(ctx, "PUT", "/channels/"+channelID+"/messages/"+messageID+"/reactions/"+url.PathEscape(emoji)+"/@me", nil, nil)
}

func (r *rest) startThread(ctx context.Context, channelID, messageID, name string) (Channel, error) {
	var c Channel
	err := r.do(ctx, "POST", "/channels/"+channelID+"/messages/"+messageID+"/threads", map[string]any{"name": name, "auto_archive_duration": 1440}, &c)
	return c, err
}

func (r *rest) gatewayURL(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := r.do(ctx, "GET", "/gateway/bot", nil, &out)
	return out.URL, err
}

// Limit is how many characters one Discord message may carry here; the hard
// limit is 2000.
const Limit = 1800

// Split cuts a reply into messages of at most Limit characters, preferring
// to break at a line end in the second half of each piece.
func Split(text string) []string {
	text = strings.TrimSpace(text)
	var out []string
	for utf8.RuneCountInString(text) > Limit {
		runes := []rune(text)
		cut := Limit
		for i := Limit - 1; i >= Limit/2; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		text = strings.TrimLeft(string(runes[cut:]), "\n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

// newer reports whether Discord ID a was created after b.
func newer(a, b string) bool {
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	return x > y
}

// isConversation excludes join notices, pins, commands, and other system posts.
func (m Message) isConversation() bool { return m.Type == 0 || m.Type == 19 }

func (r *rest) guildChannels(ctx context.Context, guild string) ([]Channel, error) {
	var channels []Channel
	if err := r.do(ctx, "GET", "/guilds/"+guild+"/channels", nil, &channels); err != nil {
		return nil, err
	}
	// The guild channel listing omits guild_id on some responses.
	for i := range channels {
		if channels[i].GuildID == "" {
			channels[i].GuildID = guild
		}
	}
	var threads struct {
		Threads []Channel `json:"threads"`
	}
	if err := r.do(ctx, "GET", "/guilds/"+guild+"/threads/active", nil, &threads); err != nil {
		return channels, err
	}
	for _, c := range threads.Threads {
		if c.GuildID == "" {
			c.GuildID = guild
		}
		channels = append(channels, c)
	}
	return channels, nil
}
