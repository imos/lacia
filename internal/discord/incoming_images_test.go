package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type imageRoundTrip func(*http.Request) (*http.Response, error)

func (f imageRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHistoryImagesOnlyReturnsRefsForPermittedMessages(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/100":
			json.NewEncoder(w).Encode(Channel{ID: "100", GuildID: "500"})
		case "/channels/101":
			json.NewEncoder(w).Encode(Channel{ID: "101", GuildID: "500"})
		case "/channels/100/messages/200", "/channels/100/messages/201", "/channels/100/messages/202":
			id := filepath.Base(r.URL.Path)
			m := Message{ID: id, ChannelID: "100"}
			m.Author.ID = "300"
			m.Attachments = []Attachment{{ID: "400"}}
			if id == "201" {
				m.Author.ID = "301"
			}
			if id == "202" {
				m.ChannelID = "101"
			}
			json.NewEncoder(w).Encode(m)
		default:
			t.Errorf("unexpected Discord lookup: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	bot := New(Config{GuildID: "500", Channels: []string{"100"}, Authors: []string{"300"}, State: filepath.Join(t.TempDir(), "state"), api: api.URL})
	refs, err := bot.HistoryImages(context.Background(), "100", "200")
	if err != nil || len(refs) != 1 || refs[0] != (ImageRef{Channel: "100", Message: "200", Author: "300", Attachment: "400"}) {
		t.Fatalf("unexpected refs: %v %v", refs, err)
	}
	for _, pair := range [][2]string{{"101", "200"}, {"100", "201"}, {"100", "202"}, {"100", "https://invalid.example/image.png"}} {
		if refs, err := bot.HistoryImages(context.Background(), pair[0], pair[1]); err == nil || len(refs) > 0 {
			t.Fatalf("unexpectedly accepted %v: %v %v", pair, refs, err)
		}
	}
}

func TestIncomingImagesRefreshValidateAndClean(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	png.Encode(&buf, img)
	data := buf.Bytes()
	ref := ImageRef{Channel: "100", Message: "200", Author: "300", Attachment: "400"}
	for _, test := range []string{"ok", "no-content-type", "gif", "http-failure", "redirect", "too-large", "corrupt", "wrong-author", "wrong-channel", "wrong-guild", "wrong-attachment", "foreign-host", "wrong-path", "expired", "unsupported"} {
		t.Run(test, func(t *testing.T) {
			downloads := 0
			data := data
			contentType := "image/png"
			if test == "no-content-type" {
				contentType = ""
			}
			if test == "gif" {
				var buf bytes.Buffer
				gif.Encode(&buf, img, nil)
				data = buf.Bytes()
				contentType = "image/gif"
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bot synthetic-token" {
					t.Error("missing REST authorization")
				}
				if r.URL.Path == "/channels/100" {
					json.NewEncoder(w).Encode(Channel{ID: "100", GuildID: "500"})
					return
				}
				if r.URL.Path != "/channels/100/messages/200" {
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				m := Message{ID: "200", ChannelID: "100", GuildID: "500"}
				m.Author.ID = "300"
				a := Attachment{ID: "400", Filename: "label.png", URL: "https://cdn.discordapp.com/attachments/100/400/label.png?ex=ffffffff&hm=synthetic", ContentType: contentType, Size: int64(len(data))}
				switch test {
				case "wrong-author":
					m.Author.ID = "301"
				case "wrong-channel":
					m.ChannelID = "101"
				case "wrong-guild":
					m.GuildID = "501"
				case "wrong-attachment":
					a.ID = "401"
				case "foreign-host":
					a.URL = "https://example.com/attachments/100/400/label.png"
				case "wrong-path":
					a.URL = "https://cdn.discordapp.com/attachments/101/400/label.png"
				case "expired":
					a.URL = "https://cdn.discordapp.com/attachments/100/400/label.png?ex=1"
				case "unsupported":
					a.ContentType = "application/pdf"
				}
				m.Attachments = []Attachment{a}
				json.NewEncoder(w).Encode(m)
			}))
			defer api.Close()
			bot := New(Config{Token: "synthetic-token", GuildID: "500", Channels: []string{"100"}, Authors: []string{"300"}, State: filepath.Join(t.TempDir(), "state"), api: api.URL})
			bot.imageTransport = imageRoundTrip(func(r *http.Request) (*http.Response, error) {
				downloads++
				if r.Header.Get("Authorization") != "" || r.URL.Host != "cdn.discordapp.com" {
					t.Error("CDN authorization or wrong target")
				}
				status, body := 200, data
				switch test {
				case "http-failure":
					status = 403
				case "redirect":
					status = 302
				case "too-large":
					body = make([]byte, MaxAttachmentBytes+1)
				case "corrupt":
					body = bytes.Repeat([]byte("!"), len(data))
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://example.com/leak"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
			})
			paths, remarks, cleanup := bot.PrepareImages(context.Background(), []ImageRef{ref})
			if test == "ok" || test == "no-content-type" || test == "gif" {
				if len(paths) != 1 || !strings.Contains(remarks, "この会話に画像として渡しました") {
					t.Fatalf("paths %v remarks %s", paths, remarks)
				}
				info, err := os.Stat(paths[0])
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("unsafe file: %v %v", info, err)
				}
				got, _ := os.ReadFile(paths[0])
				if test == "gif" {
					if _, format, err := image.Decode(bytes.NewReader(got)); err != nil || format != "png" {
						t.Fatalf("GIF not converted to PNG: %s %v", format, err)
					}
				} else if !bytes.Equal(got, data) {
					t.Error("image changed")
				}
			} else if len(paths) != 0 || remarks == "" {
				t.Fatalf("failure wasn't reported: %v %s", paths, remarks)
			}
			if strings.Contains(remarks, "synthetic") || strings.Contains(remarks, "https://") {
				t.Error("URL or secret leaked")
			}
			if test == "redirect" && downloads != 1 {
				t.Error("redirect followed")
			}
			if strings.HasPrefix(test, "wrong-") || test == "foreign-host" || test == "expired" || test == "unsupported" {
				if downloads != 0 {
					t.Error("unrelated or unsupported attachment fetched")
				}
			}
			cleanup()
			entries, _ := os.ReadDir(bot.cfg.State + ".incoming-images")
			if len(entries) != 0 {
				t.Error("temporary images remain")
			}
		})
	}
}

func TestIncomingImagesRejectedBeforeAccess(t *testing.T) {
	bot := New(Config{State: filepath.Join(t.TempDir(), "state")})
	bot.rest.http.Transport = imageRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("untrusted reference accessed network")
		return nil, nil
	})
	_, remark, cleanup := bot.PrepareImages(context.Background(), []ImageRef{{Channel: "../../other"}})
	defer cleanup()
	if remark == "" {
		t.Fatal("missing failure description")
	}
}

func TestIncomingImagesCleanAfterRestart(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	root := state + ".incoming-images"
	os.MkdirAll(filepath.Join(root, "turn-old"), 0700)
	os.WriteFile(filepath.Join(root, "turn-old", "1.png"), []byte("private synthetic image"), 0600)
	os.WriteFile(filepath.Join(root, "unrelated"), []byte("keep"), 0600)
	New(Config{State: state})
	if _, err := os.Stat(filepath.Join(root, "turn-old")); !os.IsNotExist(err) {
		t.Fatal("old images remain")
	}
	if _, err := os.Stat(filepath.Join(root, "unrelated")); err != nil {
		t.Fatal("unrelated file removed")
	}
}
