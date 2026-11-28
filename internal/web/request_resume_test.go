package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

func TestRequestResumeAPIAndWeb(t *testing.T) {
	store, _ := setup(t)
	q, _ := request.Open(testDir(t, t.TempDir()))
	s := &Server{Store: store, Requests: q}
	h, _ := s.Handler()
	q.Add(request.Request{ID: "original-request", TaskID: "T1002", Text: "private-original"})
	q.Resolve("original-request", request.Unknown, "partial-result", "interrupted", false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(`{"resume":"original-request","id":"resume-command"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var saved request.Request
	json.Unmarshal(rec.Body.Bytes(), &saved)
	current, _ := store.Get("T1002")
	if saved.ResumeOf != "original-request" || current.Status != task.Doing {
		t.Fatalf("bad recovery: %+v %s", saved, current.Status)
	}
	// The form and API share durable deduplication even when they use different IDs.
	rec = post(h, "/requests/original-request/resume", "", url.Values{})
	if rec.Code != http.StatusSeeOther || q.Pending() != 1 {
		t.Fatal("duplicate form recovery")
	}
	_, body, _ := get(t, h, "/requests/"+saved.ID)
	if !strings.Contains(body, "/requests/original-request") || !strings.Contains(body, "private-original") {
		t.Fatal("missing original link")
	}
	_, body, _ = get(t, h, "/requests/original-request")
	if !strings.Contains(body, "partial-result") {
		t.Fatal("missing previous result")
	}
	for _, status := range []task.Status{task.Done, task.Cancelled} {
		store.SetStatus("T1002", status, "test", "")
		if _, _, err := s.resume("original-request", "", "Web"); err == nil {
			t.Fatal("closed task resumed")
		}
	}
}

func TestRequestAdmissionDoesNotCopyTextAndDeduplicates(t *testing.T) {
	store, _ := setup(t)
	q, _ := request.Open(testDir(t, t.TempDir()))
	s := &Server{Store: store, Requests: q}
	h, _ := s.Handler()
	const private = "private-request-marker-should-not-enter-git"
	before := len(store.List())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.accept(request.Request{ID: "same-admission", Source: private, Text: private}, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(store.List()) != before+1 || q.Pending() != 1 {
		t.Fatal("duplicate task or request")
	}
	// Trigger the repeated-write guard with distinct accepted request IDs.
	for i := 0; i < 11; i++ {
		payload, _ := json.Marshal(map[string]string{"id": "repeat-" + request.NewToken(), "task": "T1002", "text": private})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(string(payload))))
	}
	q.Add(request.Request{ID: "stopped-original", TaskID: "T1002", Text: "original"})
	q.Resolve("stopped-original", request.Unknown, "", "interrupted", false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/requests", strings.NewReader(`{"resume":"stopped-original"}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("resume bypassed API write stop: %d", rec.Code)
	}
	rec = post(h, "/requests/stopped-original/resume", "", url.Values{})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("resume bypassed Web write stop: %d", rec.Code)
	}
	filepath.WalkDir(store.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), private) {
				t.Errorf("private request leaked to %s", path)
			}
		}
		return nil
	})
}
