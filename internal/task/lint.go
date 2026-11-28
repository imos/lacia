package task

import (
	"fmt"
	"regexp"
	"strings"
)

// Warning points at wording in task.md that tends to be hard for a person
// to read. Warnings are advice for the writer; they never block a save.
type Warning struct {
	Line    int
	Message string
}

func (w Warning) String() string {
	if w.Line == 0 {
		return w.Message
	}
	return fmt.Sprintf("%d行目: %s", w.Line, w.Message)
}

// MaxBodyLines is the size at which task.md should shed detail into
// notes.md or artifacts.
const MaxBodyLines = 100

var (
	linkTarget = regexp.MustCompile(`\]\([^)]*\)`)
	inlineCode = regexp.MustCompile("`[^`]*`")
	bareURL    = regexp.MustCompile(`https?://\S+`)
	// A slash joins two terms when Japanese text touches it; dates (11/13) and
	// names (openai/codex) are left alone.
	slashChain   = regexp.MustCompile(`[^\s/、。（）「」\[\]]*(?:[\p{Han}\p{Hiragana}\p{Katakana}]/[^\s/、。（）「」\[\]]|[^\s/、。（）「」\[\]]/[\p{Han}\p{Hiragana}\p{Katakana}])[^\s、。（）「」\[\]]*`)
	dotChain     = regexp.MustCompile(`[^\s・、。（）「」]+(?:・[^\s・、。（）「」]+){2,}`)
	nounEnding   = regexp.MustCompile(`(済み|確認|検証|保持|維持|引継ぎ|引き継ぎ|実施|予定|待ち|完了|保存|記録|追加|修正|対応|調査|報告|受領|反映|更新|削除|作成|移行|実証)[。．]`)
	machineID    = regexp.MustCompile(`event-[0-9a-f]{8,}|\b[0-9a-f]{12,40}\b`)
	borrowedVerb = regexp.MustCompile(`[A-Za-z]{3,}(する|して|した|済み)`)
)

// Lint checks the body of a task.md against the writing rules in
// docs/design/writing.md that can be checked mechanically.
func Lint(body string) []Warning {
	out := LintContent(body)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) > MaxBodyLines {
		out = append(out, Warning{0, fmt.Sprintf("本文が%d行あります。%d行を超える内容は notes.md か成果物へ移してください。", len(lines), MaxBodyLines)})
	}
	fenced := false
	for i, raw := range lines {
		if strings.HasPrefix(strings.TrimSpace(raw), "```") {
			fenced = !fenced
			continue
		}
		if fenced || strings.HasPrefix(raw, "#") {
			continue
		}
		line := bareURL.ReplaceAllString(inlineCode.ReplaceAllString(linkTarget.ReplaceAllString(raw, "]"), ""), "")
		add := func(format string, args ...any) {
			out = append(out, Warning{i + 1, fmt.Sprintf(format, args...)})
		}
		if m := slashChain.FindString(line); m != "" {
			add("「%s」は名詞をスラッシュでつないでいます。助詞と動詞を使って書いてください。", m)
		}
		if m := dotChain.FindString(line); m != "" {
			add("「%s」は名詞を中黒で3つ以上つないでいます。文にするか、箇条書きにしてください。", m)
		}
		if m := nounEnding.FindString(line); m != "" {
			add("「%s」で終わる文があります。だれが何をしたのか、これからするのかを、動詞で書いてください。", m)
		}
		if m := machineID.FindString(line); m != "" {
			add("「%s」のようなIDやハッシュは、本文ではなく履歴（lacia task note）に書いてください。", m)
		}
		if m := borrowedVerb.FindString(line); m != "" {
			add("「%s」は英語をそのまま動詞にしています。日本語の動詞で書いてください。", m)
		}
	}
	return out
}

// allowedHeadings are the headings a task.md may use. Anything longer
// belongs in notes.md.
var allowedHeadings = map[string]bool{"ひとことで": true, "このタスクを作ったわけ": true, "次にすること": true, "今回したこと": true, "いまの状況": true, "いまの資料": true}

func Body(summary, next string) string {
	return "## ひとことで\n\n" + summary + "\n\n## 次にすること\n\n" + next + "\n"
}

// Placeholder is what `lacia task new` writes where text is still missing.
const Placeholder = "（まだ書かれていません）"

// Check looks at the shape of a task: the sections task.md must have, and
// files that do not belong in the task directory. It complements Lint,
// which looks at the wording.
func Check(t Task, files []string) []Warning {
	out := ShapeProblems(t)
	if t.Status != Todo && strings.Contains(t.Body, Placeholder) {
		out = append(out, Warning{0, "「" + Placeholder + "」が残っています。着手したら、「ひとことで」と「次にすること」を書いてください。"})
	}
	next := Section(t.Body, "次にすること")
	switch t.Status {
	case Review:
		if !namesReviewer(next) {
			out = append(out, Warning{0, "確認待ちです。「次にすること」に、確認する方の名前と、その方に見ていただきたい内容を書いてください。"})
		}
		if onlyAsksToLook(next) {
			out = append(out, Warning{0, "確認待ちですが、「次にすること」の最初の文が「ご確認いただきます」のように、確認のお願いだけで終わっています。決めていただくことや、していただくことを、最初の文に書いてください。お願いが、完了にしてよいかの確認だけなら、`closing`（完了確認待ち）にしてください。"})
		}
	case Closing:
		if word := containsAny(firstParagraph(next), closingDecisionWords); word != "" {
			out = append(out, Warning{0, "完了確認待ちですが、「次にすること」の最初の段落に、判断や指示をお願いする言葉（「" + word + "」）があります。お願いが残るときは、`review`（確認待ち）にしてください。"})
		}
	}
	// A routine's directory also holds its definition, procedures and runs;
	// lacia doctor checks what may be there.
	if t.Routine {
		files = nil
	}
	for _, f := range files {
		switch {
		case f == "task.md", f == "notes.md", f == HistoryFile, strings.HasPrefix(f, "artifacts/"), strings.HasPrefix(f, "files/"):
		default:
			out = append(out, Warning{0, fmt.Sprintf("%s は、タスクのディレクトリーに置くファイルではありません。見ていただくものは artifacts/ か files/ に、作業用のものはGitに入れない local/ に置いてください。", f)})
		}
	}
	return out
}

// ShapeProblems lists what breaks the shape of a task.md: headings it may
// not use, and the sections it must have. A direct write (Store.WriteDoc)
// refuses a body with any of these, since pages and other conversations
// read the task by its sections; everything else Check and Lint find is
// advice.
func ShapeProblems(t Task) []Warning {
	var out []Warning
	seen := map[string]bool{}
	previous := ""
	fenced := false
	for i, line := range strings.Split(t.Body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if fenced || !strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.TrimSpace(strings.TrimLeft(line, "#"))
		switch {
		case !strings.HasPrefix(line, "## "):
			out = append(out, Warning{i + 1, fmt.Sprintf("見出し「%s」の書式が違います。task.md の見出しは「## 」で始めてください。", name)})
		case !allowedHeadings[name]:
			out = append(out, Warning{i + 1, fmt.Sprintf("「%s」という見出しは task.md では使いません。背景や条件は notes.md に、経過は lacia task note で履歴に書いてください。", name)})
		case seen[name]:
			out = append(out, Warning{i + 1, fmt.Sprintf("見出し「%s」が2回あります。", name)})
		case name == "このタスクを作ったわけ" && (previous != "ひとことで" || seen["次にすること"]):
			out = append(out, Warning{i + 1, "「このタスクを作ったわけ」は、「ひとことで」の直後、「次にすること」の前に置いてください。"})
		}
		seen[name] = true
		previous = name
	}
	// A task written before 「ひとことで」 has only the state; either says
	// what the task is.
	if !seen["いまの状況"] && !seen["ひとことで"] {
		out = append(out, Warning{0, "「## ひとことで」がありません。"})
	}
	if !t.Status.Closed() && !seen["次にすること"] {
		out = append(out, Warning{0, "「## 次にすること」がありません。だれが次に何をするのかを書いてください。"})
	}
	return out
}

// The lists and the home page show the first paragraph of 「次にすること」.
// These checks look at whether that paragraph tells the two kinds of asking
// apart: a decision, an answer or an action (review), or only a look at
// finished work (closing). They are advice; the wording is not parsed further.
var (
	// lookOnlyEndings end a sentence that asks for nothing but a look. Other
	// ways to say it (「見ていただきます」) are left alone: the advice names
	// the one wording that hid the difference most often.
	lookOnlyEndings = []string{"ご確認いただきます", "ご確認いただきたいです", "ご確認ください", "確認していただきます"}
	// reviewDecisionWords show that a paragraph names a decision or an
	// action after all. The list is wide, to keep the advice quiet.
	reviewDecisionWords = []string{"決めて", "お決め", "選んで", "お選び", "判断", "ご指定", "ご指示", "お知らせ", "お返事", "許可", "承認", "採否", "かどうか", "よいか", "するか", "どちら"}
	// closingDecisionWords are the ones that do not fit a task whose only
	// question is whether it may be closed. The list is narrow: a closing
	// task may well say 「直したいところがあれば、お知らせください」.
	closingDecisionWords = []string{"決めて", "お決め", "選んで", "お選び", "ご判断", "判断して", "判断を", "ご指定", "ご指示", "採否"}
)

func firstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "\n\n"); i >= 0 {
		text = text[:i]
	}
	return text
}

func firstSentence(text string) string {
	text = firstParagraph(text)
	if i := strings.Index(text, "。"); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

func containsAny(text string, words []string) string {
	for _, word := range words {
		if strings.Contains(text, word) {
			return word
		}
	}
	return ""
}

// onlyAsksToLook reports whether the first sentence ends with a request to
// confirm and the rest of its paragraph names nothing to decide or to do.
func onlyAsksToLook(next string) bool {
	sentence := firstSentence(next)
	for _, ending := range lookOnlyEndings {
		if strings.HasSuffix(sentence, ending) {
			return containsAny(firstParagraph(next), reviewDecisionWords) == ""
		}
	}
	return false
}

func namesReviewer(text string) bool {
	names := reviewers
	if names == nil {
		names = []string{OwnerName}
	}
	for _, name := range names {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
}
