package todo

import (
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/testrecords"
)

func TestRenderListsClosingBelowReview(t *testing.T) {
	store, err := task.Open(testrecords.Init(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	asked, _ := store.Create(task.NewTask{Area: "work", Title: "決めてほしいこと", Status: task.Review})
	finished, _ := store.Create(task.NewTask{Area: "work", Title: "終わったこと", Status: task.Closing})
	store.Create(task.NewTask{Area: "work", Title: "進めていること", Status: task.Doing})

	got := Render(store, time.Now())
	review := strings.Index(got, "## 要対応")
	closing := strings.Index(got, "## 完了確認待ち")
	doing := strings.Index(got, "## 進行中")
	if review < 0 || closing < review || doing < closing {
		t.Fatalf("sections out of order (review %d, closing %d, doing %d):\n%s", review, closing, doing, got)
	}
	if line := "- [" + finished.ID + "](" + finished.Dir + "/task.md) 終わったこと"; !strings.Contains(got[closing:doing], line) {
		t.Errorf("the closing section lacks %q:\n%s", line, got)
	}
	if strings.Contains(got[review:closing], finished.ID) || !strings.Contains(got[review:closing], asked.ID) {
		t.Errorf("the review section must keep only the task that asks for a decision:\n%s", got)
	}
}
