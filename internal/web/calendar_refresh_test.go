package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCalendarRefresh(t *testing.T) {
	store, _ := setup(t)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	calls := 0
	failure := false
	s := &Server{Store: store, CalendarPath: path, Calendars: &Calendars{Refresh: func(ctx context.Context) error {
		calls++
		if failure {
			return errors.New("offline")
		}
		return os.WriteFile(path, []byte(`{"updated_at":"fresh"}`), 0600)
	}}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/calendar/refresh", nil))
		if r.Code != want {
			t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
		}
		if !failure && r.Body.String() != `{"updated_at":"fresh"}` {
			t.Fatal(r.Body.String())
		}
		failure = true
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	s.Calendars = nil
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/calendar/refresh", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code)
	}
}
