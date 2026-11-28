package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/testrecords"

	"github.com/imos/lacia/internal/discord"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/task"
)

type attachmentRecorder struct {
	calls                    int
	channel, requestID, name string
	fail                     bool
}

func (a *attachmentRecorder) SendPDF(_ context.Context, channel, requestID, name string, data []byte) (discord.AttachmentResult, error) {
	a.calls++
	a.channel, a.requestID, a.name = channel, requestID, name
	if a.fail {
		return discord.AttachmentResult{Status: "unknown"}, errors.New("送信結果が不明です")
	}
	return discord.AttachmentResult{Status: "sent", MessageID: "123", Filename: name, RequestID: requestID}, nil
}

func TestAttachmentAPIUsesOriginalRouteAndSeparateResult(t *testing.T) {
	root := t.TempDir()
	store, err := task.Open(testrecords.Init(t, root))
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(root, "local"), 0700)
	os.WriteFile(filepath.Join(root, "local", "out.pdf"), []byte("%PDF-1.7\nexample"), 0600)
	q, err := request.Open(testDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	q.Add(request.Request{ID: "original", Text: "PDFを返してください", ReplyTo: "discord:thread-original"})
	q.Add(request.Request{ID: "other", Text: "ほかの依頼", ReplyTo: "discord:other-channel"})
	q.Add(request.Request{ID: "web", Text: "Webの依頼"})
	sender := &attachmentRecorder{}
	s := &Server{Store: store, Requests: q, Attachments: sender, DoorOnly: true}
	handler, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(body, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://127.0.0.1/api/discord/attachments", strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	good := `{"request_id":"original","path":"out.pdf","channel":"other-channel"}`
	if rec := call(good, "10.1.2.3:123"); rec.Code != http.StatusForbidden {
		t.Fatalf("external=%d", rec.Code)
	}
	for _, body := range []string{`{`, `{"request_id":"missing","path":"out.pdf"}`, `{"request_id":"web","path":"out.pdf"}`, `{"request_id":"original","path":"../out.pdf"}`, `{"request_id":"original","path":"/mnt/data/out.pdf"}`} {
		if rec := call(body, "127.0.0.1:123"); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid=%d %s", rec.Code, rec.Body)
		}
	}
	if sender.calls != 0 {
		t.Fatal("sent invalid request")
	}
	rec := call(good, "127.0.0.1:123")
	if rec.Code != 200 || sender.channel != "thread-original" || sender.requestID != "original" || sender.name != "out.pdf" {
		t.Fatalf("send=%d %+v %s", rec.Code, sender, rec.Body)
	}
	var result discord.AttachmentResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Status != "sent" {
		t.Fatalf("result=%+v %v", result, err)
	}
	original, _ := q.Get("original")
	if original.Delivery != "" {
		t.Fatal("attachment overwrote text delivery")
	}
	sender.fail = true
	if rec := call(good, "127.0.0.1:123"); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"status":"unknown"`) {
		t.Fatalf("uncertain=%d %s", rec.Code, rec.Body)
	}
	s.Attachments = nil
	if rec := call(good, "127.0.0.1:123"); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled=%d", rec.Code)
	}
}
