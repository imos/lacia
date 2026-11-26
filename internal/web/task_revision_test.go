package web

import (
	"context"
	"github.com/imos/lacia/internal/testrecords"
	"net/http"
	"testing"

	"github.com/imos/lacia/internal/task"
)

func TestTaskListAdvertisesPublishedRevision(t *testing.T) {
	store, err := task.Open(testrecords.Init(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	const revision = "0123456789012345678901234567890123456789"
	server := &Server{Store: store, PublishedRevision: func(context.Context) string { return revision }}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	status, _, header := get(t, handler, "/api/tasks")
	if status != http.StatusOK || header.Get(task.RevisionHeader) != revision {
		t.Fatalf("status=%d revision=%q", status, header.Get(task.RevisionHeader))
	}
}
