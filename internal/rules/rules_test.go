package rules

import (
	"github.com/imos/lacia/internal/chat"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedRulesAreForAnyOwner(t *testing.T) {
	list := List("", "")
	if len(list) == 0 || list[0].Name != Core {
		t.Fatalf("core must come first: %+v", list)
	}
	for _, doc := range list {
		if doc.Title == "" || doc.When == "" {
			t.Errorf("%s: needs a title and a line that says when to read it", doc.Name)
		}
		text, err := Load("", doc.Name, "田中さん")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, OwnerMark) {
			t.Errorf("%s: the owner's name was not filled in", doc.Name)
		}
	}
	core, _ := Load("", Core, "田中さん")
	if !strings.Contains(core, "田中さん") {
		t.Error("core does not name the owner")
	}
	voice, err := Load("", "voice", "田中さん")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(voice, "役割を指すときは「オーナーさん」と書きます") || !strings.Contains(voice, "その方を呼ぶときは「田中さん」を使います") {
		t.Error("voice must keep the role wording and the configured name")
	}
}

func TestRepositoryReplacesAndExtends(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules/local.md", "## わたしの決まり\n\n{{呼び名}}は朝が早いです。\n")
	write("rules/writing.md", "# 自分の書き方\n\n短く書きます。\n")
	write("rules/attendance.md", "# 勤務表\n\n勤務表を付けるときに読みます。\n")

	core, err := Load(root, Core, "田中さん")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(core, "田中さんは朝が早いです。\n") {
		t.Errorf("local.md was not appended to core:\n%s", core[max(0, len(core)-200):])
	}
	if text, _ := Load(root, "writing", ""); text != "# 自分の書き方\n\n短く書きます。\n" {
		t.Errorf("the repository's writing.md was not used: %q", text)
	}
	names := map[string]Doc{}
	for _, doc := range List(root, "") {
		names[doc.Name] = doc
	}
	if !names["writing"].Own || names["tasks"].Own {
		t.Errorf("Own is wrong: %+v", names)
	}
	if doc, ok := names["attendance"]; !ok || doc.When != "勤務表を付けるときに読みます。" {
		t.Errorf("the repository's own document is not listed: %+v", doc)
	}
	if _, ok := names[Local]; ok {
		t.Error("local is part of core and must not be listed")
	}
	if _, err := Load(root, "../AGENTS", ""); err == nil {
		t.Error("a name must not reach outside rules/")
	}
}

func TestManaged(t *testing.T) {
	root := t.TempDir()
	if Managed(root) {
		t.Error("a repository without AGENTS.md is left as it is")
	}
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# 決まり\n\nここに全部書いてあります。\n"), 0o644)
	if Managed(root) {
		t.Error("a repository with rules of its own keeps them")
	}
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("`lacia rules` を実行して、決まりを読んでください。\n"), 0o644)
	if !Managed(root) {
		t.Error("an AGENTS.md that points at lacia rules is managed")
	}
}

func TestFillPeer(t *testing.T) {
	text := "- 1行目\n- " + PeerMark + "へも伝えます。\n- 3行目\n"
	if got := FillPeer(text, "家庭モード"); got != "- 1行目\n- 家庭モードへも伝えます。\n- 3行目\n" {
		t.Errorf("with a peer: %q", got)
	}
	// Without a peer, the lines about one are left out.
	if got := FillPeer(text, ""); got != "- 1行目\n- 3行目\n" {
		t.Errorf("without a peer: %q", got)
	}
}

func TestLocalAdditions(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	os.WriteFile(filepath.Join(root, Dir, "git.local.md"), []byte("## このリポジトリーで足すこと\n\n{{呼び名}}の決まりです。\n"), 0o644)
	os.WriteFile(filepath.Join(root, Dir, "mine.md"), []byte("# 自分の文書\n\n読むときです。\n"), 0o644)
	os.WriteFile(filepath.Join(root, Dir, "mine.local.md"), []byte("足した行です。\n"), 0o644)
	os.WriteFile(filepath.Join(root, Dir, "local.md"), []byte("## いつも\n\nいつもの決まりです。\n"), 0o644)
	os.WriteFile(filepath.Join(root, Dir, "core.local.md"), []byte("## 芯に足す\n\n足しました。\n"), 0o644)
	git, err := Load(root, "git", "田中さん")
	if err != nil || !strings.HasSuffix(git, "\n\n## このリポジトリーで足すこと\n\n田中さんの決まりです。\n") {
		t.Errorf("git = %q, %v", git, err)
	}
	if mine, _ := Load(root, "mine", ""); mine != "# 自分の文書\n\n読むときです。\n\n足した行です。\n" {
		t.Errorf("mine = %q", mine)
	}
	core, _ := Load(root, Core, "")
	if !strings.HasSuffix(core, "\n\n## 芯に足す\n\n足しました。\n\n## いつも\n\nいつもの決まりです。\n") {
		t.Errorf("core ends with %q", core[len(core)-80:])
	}
	for _, doc := range List(root, "") {
		if strings.Contains(doc.Name, ".") || doc.Name == Local {
			t.Errorf("listed %s", doc.Name)
		}
	}
	if _, err := Load(root, "git.local", ""); err == nil {
		t.Error("an addition was read on its own")
	}
}

func TestCoreIncludesTheSharedChatBehavior(t *testing.T) {
	core, err := Load("", Core, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(core, strings.TrimSpace(chat.Behavior)) || strings.Contains(core, "{{案内の直接の宛先}}") {
		t.Fatal("core does not include the shared behavior")
	}
}

// The design guides introduce the settings to a reader. Their shared rules
// and examples should then match what the binary prints for the default owner.
func TestDesignGuidesMatchEmbeddedRules(t *testing.T) {
	for _, guide := range []struct{ name, heading string }{
		{"voice", "## 人柄"},
		{"writing", "## いちばん大事なこと"},
	} {
		t.Run(guide.name, func(t *testing.T) {
			path := filepath.Join("..", "..", "docs", "design", guide.name+".md")
			design, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			embedded, err := Load("", guide.name, DefaultOwner)
			if err != nil {
				t.Fatal(err)
			}
			heading := "\n" + guide.heading + "\n"
			_, designBody, inDesign := strings.Cut(string(design), heading)
			_, embeddedBody, inEmbedded := strings.Cut(embedded, heading)
			if !inDesign || !inEmbedded {
				t.Fatalf("both versions of %s need the heading %q", guide.name, guide.heading)
			}
			if designBody != embeddedBody {
				t.Errorf("%s and lacia rules %s differ after %q", path, guide.name, guide.heading)
			}
		})
	}
}

func TestFit(t *testing.T) {
	text := "前の段落です。\n\n" + TasksBegin + "\n## タスク\n\nタスクを作ります。\n" + TasksEnd + "\n" +
		NoTasksBegin + "\n## 覚え書き\n\n覚え書きに書きます。\n" + NoTasksEnd + "\n\n" +
		TasksBegin + "\n## プロジェクト\n\nまとめます。\n" + TasksEnd + "\n\n## 次の節\n\n- いつも\n" +
		TasksBegin + "\n- タスクのときだけ\n" + TasksEnd + "\n- いつも\n"
	with := "前の段落です。\n\n## タスク\n\nタスクを作ります。\n\n## プロジェクト\n\nまとめます。\n\n## 次の節\n\n- いつも\n- タスクのときだけ\n- いつも\n"
	without := "前の段落です。\n\n## 覚え書き\n\n覚え書きに書きます。\n\n## 次の節\n\n- いつも\n- いつも\n"
	if got := Fit(text, true); got != with {
		t.Errorf("with tasks:\n%q\nwant\n%q", got, with)
	}
	if got := Fit(text, false); got != without {
		t.Errorf("without tasks:\n%q\nwant\n%q", got, without)
	}
	plain := "印のない文書です。\n\n\n2行あけた段落も、そのままです。\n"
	if Fit(plain, false) != plain || Fit(plain, true) != plain {
		t.Error("a text without marks must not change")
	}
}

// Every embedded document and the instructions are read with the marks in
// pairs, so that a mistake cannot leave half of a block in either use.
func TestEmbeddedMarksArePaired(t *testing.T) {
	entries, err := docs.ReadDir("docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, _ := docs.ReadFile("docs/" + e.Name())
		open := ""
		for i, line := range strings.Split(string(data), "\n") {
			switch strings.TrimSpace(line) {
			case TasksBegin, NoTasksBegin:
				if open != "" {
					t.Errorf("%s:%d: %s opens inside %s", e.Name(), i+1, line, open)
				}
				open = line
			case TasksEnd, NoTasksEnd:
				if open == "" || strings.TrimPrefix(strings.TrimSpace(line), "<!-- /") != strings.TrimPrefix(open, "<!-- ") {
					t.Errorf("%s:%d: %s does not close %q", e.Name(), i+1, line, open)
				}
				open = ""
			}
		}
		if open != "" {
			t.Errorf("%s: %s is not closed", e.Name(), open)
		}
	}
}

func TestRepositoryWithoutTasks(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, ".lacia", "config.json"), []byte(`{"format": 1, "tasks": false}`), 0o644)
	if KeepsTasks(root) || !KeepsTasks("") {
		t.Fatal("the setting was not read")
	}
	for _, doc := range List(root, "") {
		if doc.Name == "tasks" || doc.Name == "projects" || doc.Name == "waits" {
			t.Errorf("%s is listed for a repository without tasks", doc.Name)
		}
		text, err := Load(root, doc.Name, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "<!--") {
			t.Errorf("%s: a mark is left", doc.Name)
		}
	}
	core, _ := Load(root, Core, "")
	for _, gone := range []string{"\n## タスク\n", "\n## プロジェクト\n", "lacia task new --area", "| `tasks` |", "`task.md`、`notes.md`、履歴"} {
		if strings.Contains(core, gone) {
			t.Errorf("core without tasks still has %q", gone)
		}
	}
	if !strings.Contains(core, "\n## タスクを持たない記録\n") || strings.Contains(core, "\n\n\n") {
		t.Errorf("core without tasks is not in shape")
	}
	withTasks, _ := Load("", Core, "")
	if strings.Contains(withTasks, "タスクを持たない記録") || strings.Contains(withTasks, "<!--") || strings.Contains(withTasks, "\n\n\n") {
		t.Error("the default core must be the one for tasks")
	}
	// The repository's own document stays on the list.
	os.MkdirAll(filepath.Join(root, "rules"), 0o755)
	os.WriteFile(filepath.Join(root, "rules", "tasks.md"), []byte("# 自分のタスクの決まり\n\n読むときです。\n"), 0o644)
	found := false
	for _, doc := range List(root, "") {
		found = found || doc.Name == "tasks"
	}
	if !found {
		t.Error("the repository's own tasks.md must be listed")
	}
}
