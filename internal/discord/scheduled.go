package discord

import (
	"context"
	"errors"
	"fmt"
)

// SendScheduled validates a schedule's destination before using the
// ordinary delivery path. A scheduled notice may only go to an address
// explicitly given in the schedule and verified in this bot's guild.
func (b *Bot) SendScheduled(ctx context.Context, channel, key, text string) error {
	if !snowflake.MatchString(channel) {
		return errors.New("予約の投稿先には、DiscordのチャンネルIDを数字で指定してください")
	}
	if !has(b.cfg.Channels, channel) && !b.cfg.MentionAllChannels {
		return errors.New("予約の投稿先には、依頼を受け付けるDiscordのチャンネルを指定してください")
	}
	// Read current metadata instead of using the cache from the gateway.
	destination, err := b.rest.channel(ctx, channel)
	if err != nil {
		return fmt.Errorf("予約の投稿先をDiscordで確かめられませんでした。%w", err)
	}
	if b.cfg.GuildID == "" || destination.ID != channel || destination.GuildID != b.cfg.GuildID {
		return errors.New("予約の投稿先には、設定したDiscordサーバーにあるチャンネルを指定してください")
	}
	// Discord can reopen an archived thread when a message is posted to it.
	// Keep recurring posts in channels with their own text chat.
	if !destination.acceptsMessages() || destination.isThread() {
		return errors.New("予約の返事はスレッドには投稿できません。メッセージを直接投稿できるチャンネルを指定してください")
	}
	return b.Send(ctx, channel, key, text)
}
