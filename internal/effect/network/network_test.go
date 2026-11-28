package network

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	for target, want := range map[string]bool{
		"https://discord.com/api/v10/gateway":          true,
		"wss://gateway-us-east1-b.discord.gg/?v=10":    true,
		"https://www.googleapis.com/calendar/v3":       true,
		"http://127.0.0.1:8080/api":                    true,
		"http://[::1]:8080/api":                        true,
		"http://localhost:8080/api":                    true,
		"https://example.com/":                         false,
		"https://discord.gg.example.com/":              false,
		"https://evildiscord.com/":                     false,
		"https://cdn.discordapp.com.example.net/a.png": false,
	} {
		u, _ := url.Parse(target)
		if got := check(u) == nil; got != want {
			t.Errorf("check(%s) = %v, want %v", target, got, want)
		}
	}
}

func TestInternetRefusesUnlistedHosts(t *testing.T) {
	_, err := Internet(time.Second).Get("https://example.com/")
	if err == nil || !strings.Contains(err.Error(), "hosts.go") {
		t.Errorf("Get = %v, want a refusal", err)
	}
	if _, err := WebSocket(t.Context(), "wss://example.com/"); err == nil || !strings.Contains(err.Error(), "hosts.go") {
		t.Errorf("WebSocket = %v, want a refusal", err)
	}
	var _ http.RoundTripper = listedOnly{}
}

func TestRestartRetriesRefusedDial(t *testing.T) {
	dials := 0
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	got, err := dialWhileRestarting(t.Context(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		if dials == 1 {
			return nil, syscall.ECONNREFUSED
		}
		return client, nil
	}, "tcp", "fixture", time.Second, time.Millisecond)
	if err != nil || got != client || dials != 2 {
		t.Fatalf("connection=%v dials=%d err=%v", got, dials, err)
	}
	dials = 0
	_, err = dialWhileRestarting(t.Context(), func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, io.ErrUnexpectedEOF
	}, "tcp", "fixture", time.Second, time.Millisecond)
	if !errors.Is(err, io.ErrUnexpectedEOF) || dials != 1 {
		t.Fatalf("unexpected error was retried: dials=%d err=%v", dials, err)
	}
}
