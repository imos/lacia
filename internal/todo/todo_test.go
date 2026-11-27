package todo

import (
	"github.com/imos/lacia/internal/testrecords"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
)

func TestRender(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"area_labels":{"work":"仕事","personal":"個人"}}`), 0o644)
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := store.Create(task.NewTask{Area: "work", Title: "確認してほしいこと", Status: task.Review})
	// A record from before a waiting task needed a condition.
	old, _ := store.Create(task.NewTask{Area: "personal", Title: "外の返事を待つこと（再開条件が未登録）"})
	testrecords.WaitWithoutCondition(t, root, old.Dir)
	store.Refresh(old.ID)
	store.Create(task.NewTask{Area: "work", Title: "終わったこと", Status: task.Done})
	later, _ := store.Create(task.NewTask{Area: "work", Title: "来年のこと"})
	store.SetField(later.ID, "resume_on", "2999-01-01", "テスト")
	both, _ := store.Create(task.NewTask{Area: "work", Title: "返事を受けてから頼むこと"})
	store.SetField(both.ID, "resume_on", "2999-01-01", "テスト")
	store.SetField(both.ID, "review_at", "2999-01-02", "テスト")
	store.SetField(both.ID, "after", later.ID, "テスト")
	store.SetField(both.ID, "on_ready", "start", "テスト")
	path := filepath.Join(store.Root(), a.Dir, "task.md")
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "## 次にすること\n\n（まだ書かれていません）", "## 次にすること\n\n佐藤さんが[結果](artifacts/a.md)を\n確認します。", 1)), 0o644)
	store.Reload()

	got := Render(store, time.Now())
	for _, want := range []string{
		"## 要対応\n\n### 仕事\n\n- [" + a.ID + "](" + a.Dir + "/task.md) 確認してほしいこと\n  佐藤さんが結果を 確認します。\n",
		"## 待機",
		"### 個人\n\n- [T1002]",
		"外の返事を待つこと（再開条件が未登録）",
		"### 仕事\n\n- [" + both.ID + "]",
		"来年のこと（2999-01-01以降要対応）",
		"返事を受けてから頼むこと（2999-01-02以降かつ" + later.ID + "終了後再開）",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("TODO.md lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "終わったこと") || strings.Contains(got, "着手待ち") || strings.Contains(got, "## 進行中") {
		t.Errorf("closed task or empty section was listed:\n%s", got)
	}
	if err := Write(store, time.Now()); err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(filepath.Join(store.Root(), "TODO.md")); string(saved) != got {
		t.Error("Write saved different content")
	}
}
