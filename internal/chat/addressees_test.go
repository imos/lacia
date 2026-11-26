package chat

import (
	"strings"
	"testing"
)

func TestRenderAddresses(t *testing.T) {
	resolve := func(who string) (string, bool) {
		m := map[string]string{"直人": "<@2>", "美咲": "<@1>", "naoto": "<@2>"}
		value, ok := m[who]
		return value, ok
	}
	for _, tt := range []struct{ name, in, want string }{
		{"proxy answer", "[[to:直人]]\n直人さん、説明します。", "<@2>\n直人さん、説明します。"},
		{"morning paragraph", "おはようございます。\n\n[[to:美咲]]\n美咲さん、お出かけには傘をどうぞ。", "おはようございます。\n\n<@1>\n美咲さん、お出かけには傘をどうぞ。"},
		{"ordered recipients", "[[to:直人]] [[to:美咲]]\nお願いします。", "<@2> <@1>\nお願いします。"},
		{"duplicate identity", "[[to:直人]]\n説明します。\n[[to:naoto]]\n続きです。", "<@2>\n説明します。\n\n続きです。"},
		{"schedule list", "直人さんは学校です。\n美咲さんはお仕事です。", "直人さんは学校です。\n美咲さんはお仕事です。"},
		{"inline discussion", "例は [[to:直人]] です。", "例は [[to:直人]] です。"},
		{"quoted", "> [[to:直人]]", "> [[to:直人]]"},
		{"indented code", "    [[to:直人]]", "    [[to:直人]]"},
		{"code fences", "```text\n[[to:直人]]\n```\n[[to:美咲]]", "```text\n[[to:直人]]\n```\n<@1>"},
		{"tilde fences", "~~~\n[[to:直人]]\n~~~", "~~~\n[[to:直人]]\n~~~"},
		{"long fence", "````text\n```\n[[to:直人]]\n````", "````text\n```\n[[to:直人]]\n````"},
		{"unknown", "[[to:未登録]]\n説明します。", "未登録\n説明します。"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Render(tt.in, resolve); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	// A future adapter supplies its own resolver without reimplementing policy.
	if got := Render("[[to:美咲]]\n本文です。", func(string) (string, bool) { return "@chat-user", true }); got != "@chat-user\n本文です。" {
		t.Fatal(got)
	}
	if got := Render("[[to:<@2>]]", resolve); strings.Contains(got, "<@2>") {
		t.Fatal("unknown annotation became a raw mention")
	}
}
