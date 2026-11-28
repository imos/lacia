package task

import (
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imos/lacia/internal/mdlink"
)

var headingDate = regexp.MustCompile(`\d{4}[-/]\d{1,2}[-/]\d{1,2}|\d{1,2}月\d{1,2}日`)
var listMarker = regexp.MustCompile(`^(?:[-*] |[0-9]+\. )`)
var relativeLabel = regexp.MustCompile(`今回|前回|けさ|最新|旧版`)

// proseLines excludes fenced examples from document checks.
func proseLines(body string) []string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	fenced := false
	for i, line := range lines {
		if mdlink.Fence(line) {
			fenced = !fenced
			lines[i] = ""
			continue
		}
		if fenced {
			lines[i] = ""
		}
	}
	return lines
}

// LintContent checks the length of the sections and the links of いまの資料.
func LintContent(body string) []Warning {
	var out []Warning
	summary := strings.Join(proseLines(Section(body, "ひとことで")), "\n")
	if strings.Count(summary, "。")+strings.Count(summary, "！")+strings.Count(summary, "？") > 3 {
		out = append(out, Warning{0, "「ひとことで」が3文を超えています。詳しい内容は「いまの状況」へ移してください。"})
	}
	reason := strings.Join(proseLines(Section(body, "このタスクを作ったわけ")), "\n")
	if strings.Count(reason, "。")+strings.Count(reason, "！")+strings.Count(reason, "？") > 3 {
		out = append(out, Warning{0, "「このタスクを作ったわけ」が3文を超えています。きっかけと解決したい困りごとを1〜3文で書き、細かな前提は notes.md に、経過は履歴へ移してください。"})
	}
	next := strings.TrimSpace(Section(body, "次にすること"))
	first, _, _ := strings.Cut(next, "\n\n")
	if utf8.RuneCountInString(strings.ReplaceAll(first, "\n", "")) > 140 {
		out = append(out, Warning{0, "「次にすること」の最初の段落が140字を超えています。一覧では途中で切れます。"})
	}
	count := 0
	for _, line := range proseLines(Section(body, "いまの資料")) {
		for _, link := range mdlink.Find(line) {
			count++
			if relativeLabel.MatchString(line[strings.LastIndex(line[:link.Start], "[")+1 : link.Start-2]) {
				out = append(out, Warning{0, "「いまの資料」のリンク名に、読む時期によって意味が変わる言葉があります。どの資料か分かるように日付を書いてください。"})
			}
		}
	}
	if count > 12 {
		out = append(out, Warning{0, "「いまの資料」のリンク数が12個を超えています。古い版へのリンクが残っていないか確かめてください。"})
	}
	return out
}

// LintNotesFile reads optional notes; it never writes or rejects them.
func LintNotesFile(path string) []Warning {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return LintDocument("notes", string(data))
}

// LintDocument returns advisory warnings for related documents. kind is
// notes, project, schedule, knowledge or procedures.
func LintDocument(kind, source string) []Warning {
	var out []Warning
	add := func(line int, message string) { out = append(out, Warning{line, message}) }
	body := source
	if kind != "notes" {
		doc, err := ParseDoc(source)
		if err != nil {
			add(0, "冒頭の項目を読めません。")
		} else {
			body = doc.Body
			keys := map[string]bool{}
			switch kind {
			case "project":
				for _, key := range []string{"title", "status"} {
					keys[key] = true
				}
			case "schedule":
				for _, key := range []string{"id", "title", "area", "output", "legacy_name", "check_result", "check_scopes", "matters_task", "every", "daily", "at", "weekdays", "conversation", "profile", "to", "reply", "enabled", "task", "done"} {
					keys[key] = true
				}
			default:
				for _, key := range []string{"title", "when", "updated"} {
					keys[key] = true
					if doc.Get(key) == "" {
						add(0, "冒頭に "+key+" がありません。")
					}
				}
				if v := doc.Get("updated"); v != "" {
					if _, err := time.Parse("2006-01-02", v); err != nil {
						add(0, "updated は YYYY-MM-DD の日付で書いてください。")
					}
				}
			}
			for _, field := range doc.Fields {
				if !keys[field.Key] && !strings.HasPrefix(field.Key, "x_") {
					add(0, field.Key+" は定義されていない項目です。独自に加える項目の名前は x_ で始めてください。")
				}
			}
		}
	}
	lines := proseLines(body)
	switch kind {
	case "notes":
		var headings []string
		for i, line := range lines {
			if strings.HasPrefix(line, "## ") {
				name := strings.TrimPrefix(line, "## ")
				headings = append(headings, name)
			}
			if strings.HasPrefix(line, "#") && (headingDate.MatchString(line) || strings.Contains(line, "今回")) {
				add(i+1, "日付や「今回」のある見出しがあります。経緯は履歴へ、決定は「決まったこと」へ書いてください。")
			}
		}
		if len(headings) < 2 || headings[0] != "ここまでできたら完了" || headings[1] != "してよいことと、お尋ねすること" {
			add(0, "先頭の2つの見出しは「ここまでできたら完了」「してよいことと、お尋ねすること」にしてください。")
		}
		if len(strings.Split(strings.TrimRight(body, "\n"), "\n")) > 60 {
			add(0, "補足が60行を超えています。詳しい内容は成果物へ移してください。")
		}
	case "project":
		for _, line := range proseLines(Section(body, "進め方")) {
			text := listMarker.ReplaceAllString(strings.TrimSpace(line), "")
			if headingDate.FindStringIndex(text) != nil && headingDate.FindStringIndex(text)[0] == 0 {
				add(0, "「進め方」の項目が日付から始まっています。いま有効な方針を書き、出どころを文末へ添えてください。")
			}
		}
	case "schedule":
		for _, name := range []string{"めざすこと", "ふだんの状況"} {
			if Section(body, name) == "" {
				add(0, "「"+name+"」がありません。")
			}
		}
		if steps := strings.TrimSpace(Section(body, "すること")); steps != "" && len(strings.Split(steps, "\n")) > 5 {
			add(0, "予約の「すること」が5行を超えています。詳しい手順は手順の文書へ移してください。")
		}
	case "procedures":
		if Section(body, "大まかな流れ") == "" {
			add(0, "「大まかな流れ」がありません。")
		}
	}
	return out
}
