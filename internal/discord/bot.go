package discord

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/chat"
	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/effect/network"
)

// Config says which messages count as requests.
type Config struct {
	Token              string
	GuildID            string
	Channels           []string // text channels that listen without a mention
	MonitorAllChannels bool     // review registered human posts in every accessible guild channel
	MentionAllChannels bool     // accept a direct mention elsewhere in this guild
	Authors            []string // who may ask, unless AnyHuman
	AnyHuman           bool     // accept every human in the channels (a private server)
	Members            []Member // when set, only these are accepted, and each has a role
	State              string   // file remembering how far each channel was read

	api     string // REST base, for tests
	gateway string // gateway URL, for tests
}

// Incoming is a message accepted as a request.
type Incoming struct {
	ID            string // Discord message ID
	Channel       string // where the reply goes: the channel, or a thread
	SourceChannel string // where the triggering message arrived
	BotID         string
	AuthorID      string
	Author        string // display name
	Member        Member // who this is, when members are configured
	Text          string
	ReviewReply   bool // decide whether to reply; receiving this post must stay silent
	Images        []ImageRef
}

// Bot listens to the configured channels and posts replies.
type Bot struct {
	cfg            Config
	rest           *rest
	imageTransport http.RoundTripper // test seam; nil uses the standard HTTPS transport
	// Receive stores a request and reports whether it was new. It must be
	// safe to call twice for the same message.
	Receive func(Incoming) (bool, error)
	// Ready is called once the first catch-up has finished.
	Ready func()
	// Log, when set, keeps what people wrote and what was posted.
	Log *daylog.Log

	attachmentsMu sync.Mutex
	mu            sync.Mutex
	botID         string
	state         state
	kinds         map[string]Channel // channel lookups, cached
	once          sync.Once
}

type state struct {
	Cursors         map[string]string `json:"cursors"`                    // channel -> last message seen
	Threads         map[string]bool   `json:"threads"`                    // threads this bot answers in
	MentionChannels map[string]bool   `json:"mention_channels,omitempty"` // channels with accepted direct mentions
	// Pinned binds a member configured by username to the account first
	// seen with it. Paused says until when a child's messages are not taken.
	Pinned map[string]string    `json:"pinned,omitempty"`
	Paused map[string]time.Time `json:"paused,omitempty"`
}

// New prepares a bot.
func New(cfg Config) *Bot {
	if cfg.api == "" {
		cfg.api = defaultAPI
	}
	b := &Bot{cfg: cfg, rest: &rest{api: cfg.api, token: cfg.Token, http: network.Internet(30 * time.Second)},
		kinds: map[string]Channel{}}
	if data, err := os.ReadFile(cfg.State); err == nil {
		json.Unmarshal(data, &b.state)
	}
	if b.state.Cursors == nil {
		b.state.Cursors = map[string]string{}
	}
	if b.state.Threads == nil {
		b.state.Threads = map[string]bool{}
	}
	if b.state.MentionChannels == nil {
		b.state.MentionChannels = map[string]bool{}
	}
	b.cleanIncomingImages()
	return b
}

func (b *Bot) save() {
	data, _ := json.MarshalIndent(b.state, "", "  ")
	disk.WriteFileNoSync(b.cfg.State, append(data, '\n'), 0o600)
}

// Run stays connected until ctx ends.
func (b *Bot) Run(ctx context.Context) error {
	g := &gateway{rest: b.rest, token: b.cfg.Token, dialURL: b.cfg.gateway,
		onReady: func(ctx context.Context, botID string, resumed bool) {
			if resumed {
				return // a resumed session replays what was missed
			}
			b.mu.Lock()
			b.botID = botID
			b.mu.Unlock()
			b.catchUp(ctx)
			if b.Ready != nil {
				b.once.Do(b.Ready)
			}
		},
		onMessage: func(ctx context.Context, m Message) { b.handle(ctx, m, m.GuildID) },
	}
	return g.run(ctx)
}

// catchUp reads what arrived while disconnected. The very first time a
// channel is seen, its history is skipped: old messages are not requests.
func (b *Bot) catchUp(ctx context.Context) {
	b.mu.Lock()
	targets := append([]string{}, b.cfg.Channels...)
	for id := range b.state.Threads {
		targets = append(targets, id)
	}
	if b.cfg.MentionAllChannels || b.cfg.MonitorAllChannels {
		for id := range b.state.MentionChannels {
			if !has(targets, id) {
				targets = append(targets, id)
			}
		}
	}
	b.mu.Unlock()
	if b.cfg.MonitorAllChannels {
		channels, err := b.rest.guildChannels(ctx, b.cfg.GuildID)
		if err != nil {
			slog.Warn("discord monitored channels", "error", err)
		}
		for _, c := range channels {
			if c.GuildID == b.cfg.GuildID && c.acceptsMessages() && !has(targets, c.ID) {
				targets = append(targets, c.ID)
			}
		}
	}
	for _, id := range targets {
		b.mu.Lock()
		cursor := b.state.Cursors[id]
		b.mu.Unlock()
		list, err := b.rest.messagesAfter(ctx, id, cursor)
		if err != nil {
			slog.Warn("discord catch-up", "channel", id, "error", err)
			continue
		}
		if cursor == "" {
			if len(list) > 0 {
				b.advance(id, list[len(list)-1].ID)
			}
			continue
		}
		for _, m := range list {
			m.ChannelID = id
			if !b.handle(ctx, m, b.cfg.GuildID) {
				break // retry a request that could not be stored, from the same cursor
			}
			b.advance(id, m.ID)
		}
	}
}

func (b *Bot) advance(channel, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if newer(message, b.state.Cursors[channel]) {
		b.state.Cursors[channel] = message
		b.save()
	}
}

var (
	threadWanted = regexp.MustCompile(`(スレッド|thread)(で|に|を作|を立|を使)`)
	threadDenied = regexp.MustCompile(`(スレッド|thread).{0,14}(不要|なし|使わない|作らない|しない|ではなく|じゃなく|やめ)`)
)

// wantsThread reports whether the message asks for the answer in a thread.
func wantsThread(text string) bool {
	lower := strings.ToLower(text)
	return threadWanted.MatchString(lower) && !threadDenied.MatchString(lower)
}

func (b *Bot) lookup(ctx context.Context, id string) (Channel, error) {
	b.mu.Lock()
	c, ok := b.kinds[id]
	b.mu.Unlock()
	if ok {
		return c, nil
	}
	c, err := b.rest.channel(ctx, id)
	if err == nil {
		b.mu.Lock()
		b.kinds[id] = c
		b.mu.Unlock()
	}
	return c, err
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (b *Bot) regularThread(c Channel, botID string, known bool) bool {
	return c.Type == typePublicThread && has(b.cfg.Channels, c.ParentID) && (known || botID != "" && c.OwnerID == botID)
}

func (b *Bot) mentionChannel(c Channel, id string) bool {
	return (b.cfg.MentionAllChannels || b.cfg.MonitorAllChannels) && c.ID == id && c.GuildID == b.cfg.GuildID && c.acceptsMessages()
}

// handle decides whether a message is a request, and if so hands it over.
// False means the message was a request but could not be stored.
func (b *Bot) handle(ctx context.Context, m Message, guild string) bool {
	b.mu.Lock()
	botID := b.botID
	knownThread := b.state.Threads[m.ChannelID]
	b.mu.Unlock()
	switch {
	case guild != b.cfg.GuildID || m.GuildID != "" && m.GuildID != guild:
		return true
	case !m.isConversation() || m.Author.Bot || m.WebhookID != "" || m.Author.ID == botID || m.Author.ID == "":
		return true
	case len(b.cfg.Members) == 0 && !b.cfg.AnyHuman && !has(b.cfg.Authors, m.Author.ID):
		return true
	}
	var who Member
	if len(b.cfg.Members) > 0 {
		var ok bool
		if who, ok = b.member(m); !ok {
			return true
		}
	}
	inParent := has(b.cfg.Channels, m.ChannelID)
	regularThread, mentionOnly := false, false
	if !inParent {
		// Existing threads carry on normally. Monitoring also accepts
		// ordinary human posts elsewhere for a silent relevance review.
		direct := b.cfg.MonitorAllChannels || b.cfg.MentionAllChannels && m.directlyMentions(botID)
		c, err := b.lookup(ctx, m.ChannelID)
		if err != nil {
			return !direct
		}
		regularThread = b.regularThread(c, botID, knownThread)
		mentionOnly = !regularThread && direct && b.mentionChannel(c, m.ChannelID)
		if !regularThread && !mentionOnly {
			return true
		}
	}
	if regularThread || mentionOnly {
		b.mu.Lock()
		if regularThread {
			b.state.Threads[m.ChannelID] = true
		} else if !b.state.MentionChannels[m.ChannelID] {
			b.state.MentionChannels[m.ChannelID] = true
			// Do not replay anything before the first qualifying post,
			// even if the process stops before that post is queued.
			if id, err := strconv.ParseUint(m.ID, 10, 64); err == nil && id > 0 && !newer(b.state.Cursors[m.ChannelID], m.ID) {
				b.state.Cursors[m.ChannelID] = strconv.FormatUint(id-1, 10)
			}
			b.save()
		}
		b.mu.Unlock()
	}

	text := m.Content
	for _, a := range m.Attachments {
		text += "\n添付ファイル: " + a.Filename
	}
	if strings.TrimSpace(text) == "" {
		b.advance(m.ChannelID, m.ID)
		return true
	}
	reviewReply := b.cfg.MonitorAllChannels && !inParent && !regularThread && !m.directlyMentions(botID)
	if !reviewReply {
		b.heard(ctx, m, who)
	}
	if who.ID != "" && b.paused(who.ID) {
		// Not queued and not answered: the conversation has already said
		// why. The reaction shows the message was seen and left alone.
		if err := b.reactUnlessReview(ctx, m, "💤", reviewReply); err != nil {
			slog.Warn("discord reaction", "error", err)
		}
		b.advance(m.ChannelID, m.ID)
		return true
	}
	in := Incoming{ID: m.ID, Channel: m.ChannelID, SourceChannel: m.ChannelID, BotID: botID, AuthorID: m.Author.ID, Author: m.Name(), Member: who, Text: strings.TrimSpace(text), ReviewReply: reviewReply}
	for _, a := range m.Attachments {
		if len(in.Images) < MaxIncomingImages {
			in.Images = append(in.Images, ImageRef{Channel: m.ChannelID, Message: m.ID, Author: m.Author.ID, Attachment: a.ID})
		}
	}
	if len(m.Attachments) > MaxIncomingImages {
		in.Text += "\n添付ファイルが4件を超えています。確認するのは最初の4件までです。"
	}
	if inParent && wantsThread(m.Content) {
		name := strings.Join(strings.Fields(m.Content), " ")
		if r := []rune(name); len(r) > 60 {
			name = string(r[:60])
		}
		if thread, err := b.rest.startThread(ctx, m.ChannelID, m.ID, name); err == nil && thread.ID != "" {
			in.Channel = thread.ID
			b.mu.Lock()
			b.state.Threads[thread.ID] = true
			b.save()
			b.mu.Unlock()
		} else {
			slog.Warn("discord thread", "error", err)
		}
	}
	created, err := b.Receive(in)
	if err != nil {
		slog.Error("discord receive", "message", m.ID, "error", err)
		return false
	}
	b.advance(m.ChannelID, m.ID)
	if created && !reviewReply {
		// The reaction tells the sender the message was taken; failing to
		// add it must not lose the request.
		if err := b.rest.react(ctx, m.ChannelID, m.ID, "👀"); err != nil {
			slog.Warn("discord reaction", "error", err)
		}
	}
	return true
}

// Send posts text to a channel or thread, split to fit. key identifies the
// reply, so that sending it again within a few minutes posts nothing twice.
// Standalone addressee annotations are resolved to registered members. Raw
// <@ID> mentions are still supported. Each registered ID is allowed to notify
// in only one part of this logical post.
func (b *Bot) Send(ctx context.Context, channel, key, text string) error {
	text = chat.Render(text, b.resolveAddressee)
	var posted []string
	var err error
	notified := map[string]bool{}
	for i, part := range Split(text) {
		nonce := key + "-" + strconv.Itoa(i)
		if len(nonce) > 25 {
			nonce = nonce[len(nonce)-25:]
		}
		var mention []string
		for _, id := range b.mentioned(part) {
			if !notified[id] {
				mention = append(mention, id)
				notified[id] = true
			}
		}
		if err = b.rest.post(ctx, channel, part, nonce, mention); err != nil {
			break
		}
		posted = append(posted, part)
	}
	if len(posted) > 0 {
		e := daylog.Entry{Key: "post:" + key, At: time.Now(), Who: "レイシア", Place: b.place(ctx, channel), Text: text}
		if err != nil {
			e.Text, e.Remark = strings.Join(posted, "\n"), "投稿が途中で止まりました。ここにはDiscordに届いた分だけを記録しています。"
		}
		b.record(e)
	}
	return err
}

// heard keeps a message from someone on Discord in the log. The text is kept as it
// was written; of an attachment, only the name, kind and size.
func (b *Bot) heard(ctx context.Context, m Message, who Member) {
	if b.Log == nil {
		return
	}
	e := daylog.Entry{Key: "message:" + m.ID, At: SentAt(m.ID), Who: who.Name, Place: b.place(ctx, m.ChannelID), Text: m.Content}
	if e.Who == "" {
		e.Who = m.Name()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	for _, a := range m.Attachments {
		e.Files = append(e.Files, daylog.File{Name: a.Filename, Type: a.ContentType, Size: a.Size})
	}
	b.record(e)
}

// record never fails the caller: a message is answered whether or not it
// could be written down.
func (b *Bot) record(e daylog.Entry) {
	if b.Log == nil {
		return
	}
	if _, err := b.Log.Add(e); err != nil {
		slog.Error("discord log", "key", e.Key, "error", err)
	}
}

// place names a channel or thread for the log, falling back to its ID.
func (b *Bot) place(ctx context.Context, id string) string {
	if b.Log == nil {
		return ""
	}
	c, err := b.lookup(ctx, id)
	if err != nil || c.Name == "" {
		return "チャンネル " + id
	}
	if c.isThread() && c.ParentID != "" {
		if parent, err := b.lookup(ctx, c.ParentID); err == nil && parent.Name != "" {
			return "#" + parent.Name + " › " + c.Name
		}
	}
	return "#" + c.Name
}

func (b *Bot) reactUnlessReview(ctx context.Context, m Message, emoji string, review bool) error {
	if review {
		return nil
	}
	return b.rest.react(ctx, m.ChannelID, m.ID, emoji)
}
