package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/testrecords"
)

func TestScheduleWorkerAPIAndHistory(t *testing.T) {
	root := testrecords.Init(t, t.TempDir())
	store, err := task.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("k", 40)
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte(secret), 0600)
	routing, err := schedule.LoadRouting(fmt.Sprintf(`{"local":{"device":"desk","secret_file":%q}}`, key), "", "work", "", "codex")
	if err != nil {
		t.Fatal(err)
	}
	d, err := schedule.OpenDispatch(stateFile(t, filepath.Join(t.TempDir(), "dispatch.json")), routing)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job := schedule.Job{Name: "daily", To: "local-claude", Daily: "06:00", Slot: now}
	if err := d.Enqueue("sched-daily-2026-11-24-0600", job, "prompt", now); err != nil {
		t.Fatal(err)
	}
	h, err := (&Server{Store: store, ScheduleDispatch: d, Schedules: func() ([]schedule.Job, map[string]string) { return []schedule.Job{job}, nil }}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(schedule.DeviceStatus{Status: "ready", At: now})
	for _, auth := range []string{"", secret, "Bearer wrong"} {
		w := call("/api/schedule-worker/desk/poll", auth, string(body))
		if w.Code != 401 {
			t.Fatalf("invalid auth status %d", w.Code)
		}
	}
	if w := call("/api/schedule-worker/other/poll", "Bearer "+secret, string(body)); w.Code != 401 {
		t.Fatal("device ID impersonation accepted")
	}
	w := call("/api/schedule-worker/desk/poll", "Bearer "+secret, string(body))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "sched-daily-") {
		t.Fatalf("poll %d %s", w.Code, w.Body.String())
	}
	status, page, _ := get(t, h, "/routines/daily")
	if status != 200 || !strings.Contains(page, "接続を待っています") || !strings.Contains(page, "local-claude（desk）") {
		t.Fatal("waiting page missing", status)
	}
	local := schedule.Job{Name: "server", Every: time.Hour, Slot: now}
	if err := d.Enqueue("sched-server-2026-11-24-0600", local, "local prompt", now); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncLocal("sched-server-2026-11-24-0600", "done", schedule.Result{Settled: true, Reply: "server completed", Finished: now}); err != nil {
		t.Fatal(err)
	}
	_, common, _ := get(t, h, "/api/schedule-runs")
	if !strings.Contains(common, "sched-server-") || !strings.Contains(common, "sched-daily-") || !strings.Contains(common, "server completed") {
		t.Fatal("common history is incomplete")
	}
	status, history, _ := get(t, h, "/api/schedule-runs?name=daily")
	if status != 200 || !strings.Contains(history, "waiting_connection") || !strings.Contains(history, `"kind":"claude"`) || strings.Contains(history, secret) {
		t.Fatal("history or secret exposure")
	}
	if w = call("/api/schedule-worker/desk/sched-daily-2026-11-24-0600/start", "Bearer "+secret, `{"claim":"cccccccccccccccccccccccccccccccc"}`); w.Code != 200 {
		t.Fatal("start", w.Code)
	}
	finish, _ := json.Marshal(schedule.FinishRequest{Claim: strings.Repeat("c", 32), Result: schedule.Result{Settled: true, Reply: "確認しました。", Finished: now}})
	d.Paused = func() bool { return true }
	for _, request := range []struct{ path, body string }{
		{"/api/schedule-worker/desk/poll", string(body)},
		{"/api/schedule-worker/desk/sched-daily-2026-11-24-0600/start", `{"claim":"cccccccccccccccccccccccccccccccc"}`},
		{"/api/schedule-worker/desk/sched-daily-2026-11-24-0600/finish", string(finish)},
	} {
		if paused := call(request.path, "Bearer "+secret, request.body); paused.Code != http.StatusServiceUnavailable {
			t.Fatalf("paused status=%d", paused.Code)
		}
	}
	if current, _ := d.Get("sched-daily-2026-11-24-0600"); current.Status != schedule.Executing || current.Result.Settled {
		t.Fatal("paused result was acknowledged")
	}
	d.Paused = nil

	if w = call("/api/schedule-worker/desk/sched-daily-2026-11-24-0600/finish", "Bearer "+secret, string(finish)); w.Code != 200 {
		t.Fatal("finish", w.Code)
	}
	_, history, _ = get(t, h, "/api/schedule-runs?name=daily")
	if !strings.Contains(history, "completed") || !strings.Contains(history, "確認しました。") {
		t.Fatal("result missing")
	}
}
