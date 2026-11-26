package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestAPITaskDetailMatchesCLIView(t *testing.T) {
	_, h := setup(t)
	status, body, _ := get(t, h, "/api/tasks/T1002?detail=1")
	var detail task.Detail
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || !strings.HasSuffix(detail.Dir, "/T1002") || !strings.HasPrefix(detail.Markdown, "---\nid: T1002\n") || !strings.Contains(detail.Markdown, "## 次にすること") {
		t.Fatalf("detail: status %d, %+v", status, detail)
	}
	if !strings.Contains(strings.Join(detail.Files, " "), "artifacts/result.md") {
		t.Fatalf("artifact missing: %v", detail.Files)
	}

	status, body, _ = get(t, h, "/api/tasks/T1001?detail=1")
	detail = task.Detail{}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || len(detail.Children) != 1 || detail.Children[0].ID != "T1002" {
		t.Fatalf("children: status %d, %+v", status, detail.Children)
	}

	status, body, _ = get(t, h, "/api/tasks/T1002")
	var item task.Task
	if err := json.Unmarshal([]byte(body), &item); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || item.ID != "T1002" {
		t.Fatalf("ordinary lookup: status %d, %+v", status, item)
	}
}
