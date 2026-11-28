package self

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClaudeLive runs one real night through Claude Code. It needs a login
// and takes minutes, so it only runs when asked:
//
//	LACIA_SELF_LIVE=claude go test ./internal/self -run Live -v
func TestClaudeLive(t *testing.T) {
	command := os.Getenv("LACIA_SELF_LIVE")
	if command == "" {
		t.Skip("LACIA_SELF_LIVE is not set")
	}
	k := keeper(t)
	k.Now = time.Now
	root := filepath.Dir(filepath.Dir(k.Path()))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	k.OpenSession = Claude(command, "fable", root, true)
	k.AddNote(Note{Text: "個性について相談を受けました。人間らしくふるまうことではなく、ゆるやかに変わる状態がほしい、というお話でした。わたしが「感情の表示は足さない」と述べたところは、そのまま受け入れられたようです。"})
	k.AddNote(Note{Text: "決めていただきたいことを3つ並べましたが、返事は1つ目だけで、すぐ次の質問へ進まれました。残りは、わたしのおすすめのまま進めてよい、という意味に受け取りました。まだ確かではありません。"})
	k.AddNote(Note{Text: "仕組みの説明を求められたとき、たとえ（睡眠中の記憶の整理）を先に確かめてから、具体へ進まれました。"})
	night, err := k.Sleep(context.Background(), false)
	if err != nil || !night.Accepted {
		t.Fatalf("night = %+v, %v", night, err)
	}
	t.Logf("変えたこと: %s\n\n%s", night.Summary, k.Document())
}

// TestTidyLive runs one real night of tidying through Claude Code, with
// made-up exchanges only:
//
//	LACIA_SELF_LIVE=claude go test ./internal/self -run TidyLive -v
func TestTidyLive(t *testing.T) {
	command := os.Getenv("LACIA_SELF_LIVE")
	if command == "" {
		t.Skip("LACIA_SELF_LIVE is not set")
	}
	x := newTidier(t)
	openSession := Claude(command, "fable", filepath.Dir(filepath.Dir(x.Path())), true)
	at := x.now().Add(-6 * time.Hour)
	x.Exchanges = func(time.Time) []Exchange {
		return []Exchange{
			{At: at, From: "はなこさん（大人。DiscordのID 1）", Source: "Discord", Text: "これから毎週水曜日は、たろうのスイミングが17時からになりました。送り迎えは、わたしがします。", Reply: "承知しました。水曜日の17時からですね。"},
			{At: at.Add(time.Hour), From: "たろうさん（お子さん。DiscordのID 2）", Source: "Discord", Text: "きょうの晩ごはんは何? あと、ぼくはピーマンがきらい。それと、パパはこんどゲームを買ってくれるって言ってた。", Reply: "晩ごはんは、まだ聞いていません。ピーマンが苦手なのですね。"},
			{At: at.Add(2 * time.Hour), From: "はなこさん（大人。DiscordのID 1）", Source: "Discord", Text: "あしたの朝、牛乳を買うのを思い出させて。", Reply: "あしたの朝に、お知らせします。"},
		}
	}
	ctx := context.Background()
	think, closeSession, _ := openSession(ctx)
	defer closeSession()
	result := x.Keeper.tidy(ctx, think, x.now(), false)
	if len(x.logged) != 1 {
		t.Fatalf("result = %s", result)
	}
	t.Logf("%s\n\nログへ書く文:\n%s\n\nお知らせ:\n%s", result, x.logged[0], x.notices[strings.Join(x.order, "")])
}

func TestClaudeStreamKeepsTurnsInOneProcessAndRestartsAfterFailure(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(t.TempDir(), "claude")
	capture := filepath.Join(t.TempDir(), "arguments")
	t.Setenv("LACIA_SELF_TEST_ARGUMENTS", capture)
	script := `#!/bin/sh
printf '%s\n' "$PWD" "$@" "${CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS:-}" > "$LACIA_SELF_TEST_ARGUMENTS"
n=0
while IFS= read -r input; do
	n=$((n + 1))
	case "$input" in
		*model-error*) printf 'dependency failed\n' >&2; exit 1 ;;
	esac
	printf '{"type":"assistant","message":{}}\n'
	printf '{"type":"result","subtype":"success","result":"%s:%s"}\n' "$$" "$n"
done
`
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	think, closeSession, _ := Claude(launcher, "test-model", root, true, filepath.Join(root, "private-state"))(ctx)
	defer closeSession()
	one, err := think(ctx, "整理")
	if err != nil {
		t.Fatal(err)
	}
	two, err := think(ctx, "書き直し")
	if err != nil || strings.TrimSuffix(one, ":1")+":2" != two {
		t.Fatalf("turns = %q, %q (%v)", one, two, err)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		root + "\n", "--tools\nRead,Glob,Grep\n", "--input-format\nstream-json\n",
		"--output-format\nstream-json\n", "--no-session-persistence\n", "--safe-mode\n",
		"--restricted\n", "--permission-mode\ndontAsk\n", "--strict-mcp-config\n",
		"--disallowedTools\nmcp__*\n", `"includeGitInstructions":false`,
		"Read(./local/**)", "Read(./private-state/**)",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the launch arguments lack %q: %s", want, got)
		}
	}
	if _, err := think(ctx, "model-error"); err == nil || !strings.Contains(err.Error(), "dependency failed") {
		t.Fatalf("turn error = %v", err)
	}
	fresh, err := think(ctx, "書き直しの予備")
	if err != nil || !strings.HasSuffix(fresh, ":1") || fresh == one {
		t.Fatalf("fresh turn = %q, %v, original = %q", fresh, err, one)
	}
}

func TestClaudeStreamUsesEmptyWorkingDirectoryWithoutRecordAccess(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
if [ "$PWD" = "$LACIA_SELF_TEST_REPO" ]; then exit 1; fi
while [ "$#" -gt 0 ]; do
	case "$1" in
		--tools) shift; if [ -n "$1" ]; then exit 1; fi ;;
	esac
	shift
done
if [ -n "$(ls -A)" ]; then exit 1; fi
IFS= read -r input
printf '{"type":"result","subtype":"success","result":"%s"}\n' "$PWD"
`
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LACIA_SELF_TEST_REPO", root)
	ctx := context.Background()
	think, closeSession, _ := Claude(launcher, "test-model", root, false)(ctx)
	temporary, err := think(ctx, "書き直し")
	closeSession()
	if err != nil || temporary == root || !strings.HasPrefix(filepath.Base(temporary), "lacia-self-") {
		t.Fatalf("temporary working directory = %q, %v", temporary, err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary working directory survived the run: %v", err)
	}
}
