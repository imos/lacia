package discord

import (
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// Roles a member can have. What a role may ask for is not decided here:
// the conversation is told the role and judges.
const (
	Adult = "adult"
	Child = "child"
)

// Member is someone whose messages count as requests. When any member is
// configured, messages from everybody else are ignored.
type Member struct {
	ID       string `json:"id,omitempty"`       // Discord user ID
	Username string `json:"username,omitempty"` // used until the ID is known; see pin
	Name     string `json:"name"`               // how people there call them, e.g. "Xさん"
	Role     string `json:"role"`               // Adult or Child
	Birthday string `json:"birthday,omitempty"` // YYYY-MM-DD; the age and school year told to the conversation are worked out from it
	About    string `json:"about,omitempty"`    // one more line for the conversation
}

// maxPause bounds a pause the conversation asks for.
const maxPause = 24 * time.Hour

// member finds who wrote m. A member configured by username only is bound
// to the first account seen with that username; from then on the ID
// decides, because a username can be changed and taken by somebody else.
func (b *Bot) member(m Message) (Member, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, x := range b.cfg.Members {
		if x.ID != "" && x.ID == m.Author.ID {
			return x, true
		}
	}
	username := strings.ToLower(m.Author.Username)
	for _, x := range b.cfg.Members {
		key := strings.ToLower(x.Username)
		if x.ID != "" || key == "" {
			continue
		}
		pinned := b.state.Pinned[key]
		if pinned == "" && key == username {
			if b.state.Pinned == nil {
				b.state.Pinned = map[string]string{}
			}
			b.state.Pinned[key], pinned = m.Author.ID, m.Author.ID
			b.save()
			slog.Info("discord member bound", "username", key, "id", m.Author.ID)
		}
		if pinned == m.Author.ID {
			x.ID = pinned
			return x, true
		}
	}
	return Member{}, false
}

var userMention = regexp.MustCompile(`<@!?([^<>]+)>`)

// mentioned returns the IDs of the members that text mentions as <@ID>.
func (b *Bot) mentioned(text string) []string {
	if !strings.Contains(text, "<@") {
		return nil
	}
	known := map[string]bool{}
	for _, m := range b.Members() {
		if m.ID != "" {
			known[m.ID] = true
		}
	}
	var ids []string
	for _, match := range userMention.FindAllStringSubmatch(text, -1) {
		if known[match[1]] {
			ids = append(ids, match[1])
			delete(known, match[1])
		}
	}

	return ids
}

// resolveAddressee accepts only a unique registered identity with a known ID.
// An unknown ID among same-named members still makes the name ambiguous.
func (b *Bot) resolveAddressee(who string) (string, bool) {
	var found []Member
	for _, s := range b.Members() {
		if who != "" && (who == s.ID || (s.Username != "" && strings.EqualFold(who, s.Username)) || who == s.Name || who+"さん" == s.Name) {
			found = append(found, s.Member)
		}
	}
	if len(found) != 1 || found[0].ID == "" {
		return "", false
	}
	return "<@" + found[0].ID + ">", true
}

// Members lists the configured members, with the IDs learned so far and
// until when each one is paused.
func (b *Bot) Members() []MemberStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []MemberStatus
	for _, x := range b.cfg.Members {
		if x.ID == "" {
			x.ID = b.state.Pinned[strings.ToLower(x.Username)]
		}
		s := MemberStatus{Member: x}
		if until := b.state.Paused[x.ID]; x.ID != "" && until.After(time.Now()) {
			s.PausedUntil = &until
		}
		out = append(out, s)
	}
	return out
}

// MemberStatus is a member and whether they are paused.
type MemberStatus struct {
	Member
	PausedUntil *time.Time `json:"paused_until,omitempty"`
}

func (b *Bot) find(who string) (Member, error) {
	who = strings.TrimSpace(who)
	for _, s := range b.Members() {
		if who != "" && (who == s.ID || strings.EqualFold(who, s.Username) || who == s.Name || who+"さん" == s.Name) {
			if s.ID == "" {
				return Member{}, errors.New(s.Name + "のDiscordのIDは、まだ分かりません。その方がDiscordで発言すると確認できます")
			}
			return s.Member, nil
		}
	}
	return Member{}, errors.New("設定に「" + who + "」という名前やIDの方は見つかりませんでした")
}

// Pause stops taking messages from a child for a while. Messages that
// arrive meanwhile get a reaction and are not queued. Adults cannot be
// paused: the conversation must not be able to lock them out.
func (b *Bot) Pause(who string, d time.Duration) (Member, time.Time, error) {
	m, err := b.find(who)
	switch {
	case err != nil:
		return Member{}, time.Time{}, err
	case m.Role != Child:
		return Member{}, time.Time{}, errors.New("お子さんからの依頼だけ、受け付けを一時的に止められます")
	case d <= 0 || d > maxPause:
		return Member{}, time.Time{}, errors.New("止める時間は、1分から1,440分（24時間）までの整数で指定してください")
	}
	until := time.Now().Add(d)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Paused == nil {
		b.state.Paused = map[string]time.Time{}
	}
	b.state.Paused[m.ID] = until
	b.save()
	return m, until, nil
}

// Resume ends a pause early.
func (b *Bot) Resume(who string) (Member, error) {
	m, err := b.find(who)
	if err != nil {
		return Member{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state.Paused, m.ID)
	b.save()
	return m, nil
}

func (b *Bot) paused(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.state.Paused[id]
	if ok && !until.After(time.Now()) {
		delete(b.state.Paused, id)
		b.save()
		return false
	}
	return ok
}
