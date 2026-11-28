package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imos/lacia/internal/handover"
	"github.com/imos/lacia/internal/task"
	"github.com/imos/lacia/internal/wait"
)

func TestWaitOutboxRecoversBothSaveBoundaries(t *testing.T) {
	for _, failLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "after observation", true: "after message save"}[failLink], func(t *testing.T) {
			store, _ := setup(t)
			wd, hd := t.TempDir(), t.TempDir()
			ws, _ := wait.Open(testDir(t, wd))
			hs, _ := handover.Open(testDir(t, hd))
			s := &Server{Store: store, Waits: ws, Handovers: hs, Now: time.Now}
			item, _, err := ws.Add(wait.Wait{TaskID: "T1001", Kind: wait.Job, Context: "ctx", Namespace: "ns", Name: "job", By: time.Now().Add(time.Hour), Next: wait.NextClaude, Then: "結果を集計します。"})
			if err != nil {
				t.Fatal(err)
			}
			item, _, err = ws.Observe(item.ID, wait.Succeeded, "")
			if err != nil {
				t.Fatal(err)
			}
			if failLink {
				path := filepath.Join(wd, item.ID+".json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				s.ensureHandover(item)
				if len(hs.List(nil)) != 1 {
					t.Fatal("message was not saved before link failure")
				}
				if got, _ := ws.Get(item.ID); got.HandoverID != "" {
					t.Fatal("failed save changed memory")
				}
				os.Remove(path)
				if err = os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ws, err = wait.Open(testDir(t, wd))
			if err != nil {
				t.Fatal(err)
			}
			hs, err = handover.Open(testDir(t, hd))
			if err != nil {
				t.Fatal(err)
			}
			s.Waits, s.Handovers = ws, hs
			for range 3 {
				s.TickWaits()
			}
			got, _ := ws.Get(item.ID)
			messages := hs.List(nil)
			if len(messages) != 1 || got.HandoverID != messages[0].ID || messages[0].WaitID != item.ID {
				t.Fatalf("outbox not recovered: %+v %+v", got, messages)
			}
		})
	}
}
func TestAcknowledgedMessageRepairsUnappliedTaskClaim(t *testing.T) {
	store, s, _, now := setupWaits(t)
	h, _, err := s.Handovers.Add(handover.Handover{TaskID: "T1001", Next: "codex", Then: "結果を集計します。"})
	if err != nil {
		t.Fatal(err)
	}
	s.Handovers.Update(h.ID, func(h *handover.Handover) bool {
		h.State = handover.Acked
		h.Acked = *now
		h.AckedBy = "local-codex"
		h.AckedKey = "repair-session"
		h.AckedLabel = "local-codex"
		h.ClaimPending = true
		return true
	})
	s.TickWaits()
	s.TickWaits()
	got, _ := store.Get("T1001")
	repaired, _ := s.Handovers.Get(h.ID)
	if got.Status != task.Doing || got.OwnerSession != "repair-session" || repaired.ClaimPending {
		t.Fatalf("claim was not repaired: %+v %+v", got, repaired)
	}
}
func TestStaleHandoverCannotStartWithoutTick(t *testing.T) {
	_, s, h, now := setupWaits(t)
	item, _, _ := s.Handovers.Add(handover.Handover{TaskID: "T1001", Next: "codex", Then: "結果を集計します。"})
	*now = now.Add(24 * time.Hour)
	if got := status(h, macCodex, "/api/handovers/"+item.ID, `{"start":"mac"}`); got != 409 {
		t.Fatalf("stale start = %d", got)
	}
	item, _ = s.Handovers.Get(item.ID)
	if item.State != handover.Skipped {
		t.Fatalf("state = %s", item.State)
	}
}

func TestWaitCompatibilityAcceptsCLIPatch(t *testing.T) {
	_, s, h, _ := setupWaits(t)
	id := addWait(t, h, macClaude, `,"next":"codex"`)
	as(t, h, macClaude, "POST", "/api/waits/"+id, `{"seen":"succeeded"}`)
	as(t, h, macCodex, "PATCH", "/api/waits/"+id, `{"ack":true}`)
	w, _ := s.Waits.Get(id)
	message, _ := s.Handovers.Get(w.HandoverID)
	if message.State != handover.Acked {
		t.Fatalf("W ack did not reach H: %+v", message)
	}
	id = addWait(t, h, macClaude, `,"next":"codex"`)
	as(t, h, macClaude, "PATCH", "/api/waits/"+id, `{"cancel":true}`)
	w, _ = s.Waits.Get(id)
	if w.State != wait.Cancelled {
		t.Fatalf("W cancel: %+v", w)
	}
}

func TestRecoveredWaitDoesNotResetStartDeadline(t *testing.T) {
	_, s, h, now := setupWaits(t)
	id := addWait(t, h, macClaude, `,"next":"codex"`)
	// Simulate a crash after persisting the observed end, before message creation.
	s.Waits.Observe(id, wait.Succeeded, "")
	*now = now.Add(25 * time.Hour)
	s.TickWaits()
	w, _ := s.Waits.Get(id)
	message, _ := s.Handovers.Get(w.HandoverID)
	if message.State != handover.Skipped {
		t.Fatalf("recovery reset the deadline: %+v", message)
	}
}

func TestDeliveredHandoverIsNeverStartedAgainAfterEscalation(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "grace expires", true: "runner ends"}[ended], func(t *testing.T) {
			_, s, h, now := setupWaits(t)
			message, _, err := s.Handovers.Add(handover.Handover{TaskID: "T1001", Next: "claude", Then: "結果を集計します。"})
			if err != nil {
				t.Fatal(err)
			}
			as(t, h, macCodex, "PATCH", "/api/handovers/"+message.ID, `{"start":"mac"}`)
			if ended {
				as(t, h, macCodex, "PATCH", "/api/handovers/"+message.ID, `{"end":1}`)
			} else {
				*now = now.Add(handover.Grace)
				s.TickWaits()
			}
			message, _ = s.Handovers.Get(message.ID)
			if message.State != handover.ToldDelivered {
				t.Fatalf("delivered state after escalation = %s", message.State)
			}
			for range 3 {
				s.TickWaits()
				if got := status(h, macCodex, "/api/handovers/"+message.ID, `{"start":"mac"}`); got != 409 {
					t.Fatalf("second start after escalation = %d", got)
				}
			}
		})
	}
}

func TestHandoverDelayPreservesStatusAndIsRecordedOnce(t *testing.T) {
	for _, want := range []task.Status{task.Waiting, task.Doing, task.Review} {
		t.Run(string(want), func(t *testing.T) {
			store, s, h, now := setupWaits(t)
			dir := t.TempDir()
			var err error
			s.Handovers, err = handover.Open(testDir(t, dir))
			if err != nil {
				t.Fatal(err)
			}
			if want == task.Waiting {
				waitWithoutCondition(t, store, "T1001")
			} else if _, _, err := store.SetStatus("T1001", want, "", ""); err != nil {
				t.Fatal(err)
			}
			item, _, err := s.Handovers.Add(handover.Handover{TaskID: "T1001", Next: "claude", Then: "結果を集計します。", Created: *now})
			if err != nil {
				t.Fatal(err)
			}

			// Stop between saving the history and marking the delay in the
			// handover file. The next server skips the duplicate history.
			reason := subject(item) + " の受け取りの知らせが、まだ届いていません。"
			if err := s.recordHandoverIssue(item, "delayed", reason); err != nil {
				t.Fatal(err)
			}
			s.Handovers, err = handover.Open(testDir(t, dir))
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(handover.Grace)
			s.TickWaits()
			s.TickWaits()
			got, _ := store.Get("T1001")
			current, _ := s.Handovers.Get(item.ID)
			if got.Status != want || current.State != handover.Told || !current.Runnable() {
				t.Fatalf("status = %s, handover = %s", got.Status, current.State)
			}
			if count := strings.Count(logOf(t, store, "T1001"), reason); count != 1 {
				t.Fatalf("delay history count = %d", count)
			}
			page := httptest.NewRecorder()
			h.ServeHTTP(page, httptest.NewRequest("GET", "/tasks/T1001", nil))
			if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "宛先への配信が遅れています。") {
				t.Fatalf("task page (%d) lacks the delivery delay", page.Code)
			}
		})
	}
}

func TestHandoverExplicitOwnerActionCanMoveWaitingTaskToReview(t *testing.T) {
	store, s, h, now := setupWaits(t)
	waitWithoutCondition(t, store, "T1001")
	const action = "Macで仕事用のアカウントにログインしてください。"
	out := as(t, h, macCodex, "POST", "/api/handovers", `{"task":"T1001","then":"結果を集計します。","owner_action":"`+action+`"}`)
	id := out["handover"].(map[string]any)["id"].(string)
	*now = now.Add(handover.Grace)
	s.TickWaits()
	s.TickWaits()
	got, _ := store.Get("T1001")
	if got.Status != task.Review || got.ReadySince == "" {
		t.Fatalf("status = %s, ready since = %q", got.Status, got.ReadySince)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest("GET", "/tasks/T1001", nil))
	if !strings.Contains(page.Body.String(), "<strong>"+action+"</strong>") {
		t.Fatal("task page lacks the requested action")
	}
	home := httptest.NewRecorder()
	h.ServeHTTP(home, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(home.Body.String(), action) {
		t.Fatal("review list lacks the requested action")
	}

	*now = now.Add(handover.StartWithin)
	s.TickWaits()
	item, _ := s.Handovers.Get(id)
	if item.State != handover.Skipped || item.Runnable() {
		t.Fatalf("expired handover = %+v", item)
	}
	log := logOf(t, store, "T1001")
	if strings.Count(log, action) != 1 || !strings.Contains(log, "24時間たったため") {
		t.Fatalf("log = %s", log)
	}
}

func TestExpiredHandoverWithoutOwnerActionStaysWaiting(t *testing.T) {
	store, s, h, now := setupWaits(t)
	waitWithoutCondition(t, store, "T1001")
	item, _, err := s.Handovers.Add(handover.Handover{TaskID: "T1001", Next: "claude", Then: "結果を集計します。", Created: now.Add(-handover.StartWithin)})
	if err != nil {
		t.Fatal(err)
	}
	if code := status(h, macClaude, "/api/handovers/"+item.ID, `{"start":"mac"}`); code != http.StatusConflict {
		t.Fatalf("starting an expired handover = %d", code)
	}
	s.TickWaits()
	got, _ := store.Get("T1001")
	item, _ = s.Handovers.Get(item.ID)
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest("GET", "/tasks/T1001", nil))
	if got.Status != task.Waiting || item.State != handover.Skipped ||
		strings.Count(logOf(t, store, "T1001"), "24時間たったため") != 1 ||
		!strings.Contains(page.Body.String(), "続きの開始を見送りました。") {
		t.Fatalf("status = %s, handover = %s, page = %d", got.Status, item.State, page.Code)
	}
}
