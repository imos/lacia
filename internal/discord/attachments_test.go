package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestReadPDF(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "local"), 0700)
	pdf := []byte("%PDF-1.7\nexample")
	os.WriteFile(filepath.Join(root, "local", "a.pdf"), pdf, 0600)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "private.pdf"), pdf, 0600)
	os.Symlink(filepath.Join(outside, "private.pdf"), filepath.Join(root, "local", "escape.pdf"))
	os.Symlink(outside, filepath.Join(root, "local", "escape"))
	os.WriteFile(filepath.Join(root, "local", "wrong.pdf"), []byte("not pdf"), 0600)
	if err := syscall.Mkfifo(filepath.Join(root, "local", "pipe.pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(filepath.Join(root, "local", "big.pdf"))
	f.Truncate(MaxAttachmentBytes + 1)
	f.Close()
	for _, path := range []string{"../private.pdf", "/mnt/data/a.pdf", "escape.pdf", "escape/private.pdf", "wrong.pdf", "pipe.pdf", "big.pdf", "missing.pdf", "a.txt", "a\n.pdf", "."} {
		if _, _, err := ReadPDF(root, path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	name, data, err := ReadPDF(root, "a.pdf")
	if err != nil || name != "a.pdf" || !bytes.Equal(data, pdf) {
		t.Fatalf("read = %s %q %v", name, data, err)
	}
	// A replacement of the export directory cannot redirect reads outside the repository.
	os.Rename(filepath.Join(root, "local"), filepath.Join(root, "old-local"))
	os.Symlink(outside, filepath.Join(root, "local"))
	if _, _, err := ReadPDF(root, "private.pdf"); err == nil {
		t.Fatal("followed external local root")
	}
}

func TestSendPDFMultipartAndRestartDedup(t *testing.T) {
	pdf := []byte("%PDF-1.7\nattachment")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/channels/channel/messages" || r.Header.Get("Authorization") != "Bot test" {
			t.Error("route/auth")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		var payload struct {
			Nonce    string `json:"nonce"`
			Enforce  bool   `json:"enforce_nonce"`
			Mentions struct {
				Parse []string `json:"parse"`
			} `json:"allowed_mentions"`
			Attachments []struct {
				ID       int    `json:"id"`
				Filename string `json:"filename"`
			} `json:"attachments"`
		}
		if err := json.Unmarshal([]byte(r.FormValue("payload_json")), &payload); err != nil {
			t.Error(err)
		}
		if payload.Nonce == "" || len(payload.Nonce) > 25 || !payload.Enforce || len(payload.Mentions.Parse) != 0 || len(payload.Attachments) != 1 || payload.Attachments[0].Filename != "out.pdf" {
			t.Errorf("payload=%+v", payload)
		}
		f, header, err := r.FormFile("files[0]")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		if header.Filename != "out.pdf" || !bytes.Equal(data, pdf) {
			t.Error("wrong attachment")
		}
		fmt.Fprintf(w, `{"id":"message","channel_id":"channel","attachments":[{"id":"file","filename":"out.pdf","size":%d}]}`, len(pdf))
	}))
	defer server.Close()
	cfg := Config{State: filepath.Join(t.TempDir(), "discord.json"), Token: "test", api: server.URL}
	b := New(cfg)
	result, err := b.SendPDF(context.Background(), "channel", "request-a", "out.pdf", pdf)
	if err != nil || result.Status != "sent" || result.MessageID != "message" {
		t.Fatalf("result=%+v %v", result, err)
	}
	b = New(cfg)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			result, err := b.SendPDF(context.Background(), "channel", "different-request", "renamed.pdf", pdf)
			if err != nil || result.Status != "sent" {
				t.Errorf("repeat=%+v %v", result, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestSendPDFUnknownNeverRetries(t *testing.T) {
	for _, failure := range []string{"timeout", "429", "500", "empty", "no-attachment", "wrong-size", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch failure {
				case "timeout":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
				case "429":
					w.WriteHeader(429)
					io.WriteString(w, `{"retry_after":0}`)
				case "500":
					w.WriteHeader(500)
				case "redirect":
					w.Header().Set("Location", "/other")
					w.WriteHeader(307)
				case "empty":
					io.WriteString(w, `{`)
				case "no-attachment":
					io.WriteString(w, `{"id":"message","channel_id":"channel"}`)
				case "wrong-size":
					io.WriteString(w, `{"id":"message","channel_id":"channel","attachments":[{"id":"file","filename":"out.pdf","size":1}]}`)
				}
			}))
			defer server.Close()
			cfg := Config{State: filepath.Join(t.TempDir(), "discord.json"), api: server.URL}
			b := New(cfg)
			pdf := []byte("%PDF-test")
			result, err := b.SendPDF(context.Background(), "channel", "request", "out.pdf", pdf)
			if err == nil || result.Status != "unknown" {
				t.Fatalf("result=%+v %v", result, err)
			}
			result, err = New(cfg).SendPDF(context.Background(), "channel", "request", "out.pdf", pdf)
			if err != nil || result.Status != "unknown" || calls.Load() != 1 {
				t.Fatalf("repeat=%+v %v calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestSendPDFNoNetworkWithoutDurableClaim(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "blocked"), []byte("file"), 0600)
	for _, state := range []string{"", filepath.Join(root, "blocked", "discord.json")} {
		_, err := New(Config{State: state, api: server.URL}).SendPDF(context.Background(), "channel", "request", "out.pdf", []byte("%PDF-test"))
		if err == nil {
			t.Fatal("accepted missing state")
		}
	}
	if calls != 0 {
		t.Fatal("sent without claim")
	}
}

func TestSendPDFValidation(t *testing.T) {
	b := New(Config{State: filepath.Join(t.TempDir(), "discord.json")})
	for _, data := range [][]byte{[]byte("text"), []byte("%PDF-" + strings.Repeat("x", MaxAttachmentBytes))} {
		if _, err := b.SendPDF(context.Background(), "channel", "request", "out.pdf", data); err == nil {
			t.Fatal("accepted invalid PDF")
		}
	}
}
