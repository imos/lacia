package main

import (
	"context"
	"strings"

	"github.com/imos/lacia/internal/request"
)

type scheduledDiscordSender interface {
	discordSender
	SendScheduled(context.Context, string, string, string) error
}

type discordSendFunc func(context.Context, string, string, string) error

func (f discordSendFunc) Send(ctx context.Context, channel, key, text string) error {
	return f(ctx, channel, key, text)
}

func deliverDiscordResult(ctx context.Context, bot scheduledDiscordSender, queue *request.Queue, r request.Request) {
	if !strings.HasPrefix(r.ReplyTo, "discord:") || r.Delivery != "" {
		return
	}
	origin, ok := discordResultOrigin(queue, r)
	if !ok {
		queue.SetDelivery(r.ID, "missing_route")
		return
	}
	// Schedules have a durable run ID and source for both local and remote
	// runs, including pending deliveries created by earlier versions.
	if origin.Kind == request.System && strings.HasPrefix(origin.ID, "sched-") && strings.HasPrefix(origin.Source, "予約「") {
		deliverDiscord(ctx, discordSendFunc(bot.SendScheduled), queue, r)
		return
	}
	deliverDiscord(ctx, bot, queue, r)
}

// A child notice inherits the destination and routing rules of the request
// that started it. A missing or mismatched origin must never change the route.
func discordResultOrigin(queue *request.Queue, r request.Request) (request.Request, bool) {
	if r.ParentID == "" {
		return r, true
	}
	seen := map[string]bool{}
	for r.ParentID != "" {
		if seen[r.ID] {
			return request.Request{}, false
		}
		seen[r.ID] = true
		parent, ok := queue.Get(r.ParentID)
		if !ok || parent.ReplyTo != r.ReplyTo {
			return request.Request{}, false
		}
		r = parent
	}
	return r, true
}
