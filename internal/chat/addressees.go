// Package chat implements the shared output contract for mention-capable chats.
package chat

import (
	_ "embed"
	"regexp"
	"strings"
)

// OwnerMark stands for the name Lacia calls the record's owner by.
const OwnerMark = "{{呼び名}}"

//go:embed behavior.md
var behaviorTemplate string

// BehaviorFor renders the shared naming and recipient policy. The core
// and conversation instructions use the same text to check for duplicates.
func BehaviorFor(owner string) string {
	return strings.ReplaceAll(strings.TrimSpace(behaviorTemplate), OwnerMark, owner)
}

// WithBehavior includes the shared naming and recipient policy once,
// even when the repository maintains its own rules.
func WithBehavior(text, owner string) string {
	behavior := BehaviorFor(owner)
	if strings.Contains(text, behavior) {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n" + behavior + "\n"
}

// Instructions describes the transport-neutral annotation, not how recipients
// are chosen. That behavior lives in Behavior and is shared by every entry point.
const Instructions = "この返事では、メンションで宛先の方に知らせられます。共通の決まりに沿って宛先を選び、その方に届ける段落の直前に `[[to:登録名]]` と1行で書いてください。複数の方に届けるときは、用件との関わりが強い方から `[[to:登録名1]] [[to:登録名2]]` のように並べます。コードや引用の外に、宛先だけの行を置いてください。\n登録された方の名前かIDを使います。どなたか分からなければ、本文で普通に呼びかけてください。送信する側が宛先を登録情報と照合し、本文でだれをメンションするか、だれへの通知を許可するかを決めます。本文に自然な呼びかけがあれば、そのまま残してください。\n"

var recipient = regexp.MustCompile(`\[\[to:([^\[\]\r\n]+)\]\]`)

// Render resolves standalone annotation lines, preserving their order and
// position. resolve must reject ambiguous or unregistered identities. Unknown
// names stay readable without turning into mentions. Code and quoted lines
// remain literal. A recipient is rendered only once in a logical post.
func Render(text string, resolve func(string) (string, bool)) string {
	lines := strings.Split(text, "\n")
	seen := map[string]bool{}
	fence := ""
	fenceLength := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			kind := trimmed[:1]
			n := len(trimmed) - len(strings.TrimLeft(trimmed, kind))
			if fence == "" {
				fence, fenceLength = kind, n
			} else if fence == kind && n >= fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}

		matches := recipient.FindAllStringSubmatch(trimmed, -1)
		if len(matches) == 0 || strings.TrimSpace(recipient.ReplaceAllString(trimmed, "")) != "" {
			continue
		}
		var out []string
		for _, match := range matches {
			name := strings.TrimSpace(match[1])
			value, ok := resolve(name)
			if !ok {
				value = strings.ReplaceAll(name, "@", "@\u200b")
			}
			if value != "" && !seen[value] {
				out = append(out, value)
				seen[value] = true
			}
		}
		lines[i] = strings.Join(out, " ")
	}
	return strings.Join(lines, "\n")
}
