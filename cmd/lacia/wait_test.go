package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeKubectl writes a script that prints the given output.
func fakeKubectl(t *testing.T, output string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubectl")
	out := filepath.Join(filepath.Dir(path), "out.json")
	if err := os.WriteFile(out, []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat '" + out + "'\necho 'error: boom' >&2\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKubectlJobsFindsTheJobsInWhatWasApplied(t *testing.T) {
	cases := []struct {
		name, output string
		want         []jobRef
	}{
		{"one job", `{"kind":"Job","metadata":{"name":"score-1","namespace":"ns"}}`, []jobRef{{"score-1", "ns"}}},
		{"a list", `{"kind":"List","items":[{"kind":"ConfigMap","metadata":{"name":"c"}},{"kind":"Job","metadata":{"name":"a","namespace":"ns"}},{"kind":"Job","metadata":{"name":"b","namespace":"ns"}}]}`, []jobRef{{"a", "ns"}, {"b", "ns"}}},
		{"several values", "{\"kind\":\"Job\",\"metadata\":{\"name\":\"a\",\"namespace\":\"ns\"}}\n{\"kind\":\"Service\",\"metadata\":{\"name\":\"s\"}}", []jobRef{{"a", "ns"}}},
		{"no job", `{"kind":"Deployment","metadata":{"name":"d"}}`, nil},
	}
	for _, c := range cases {
		got, err := kubectlJobs(fakeKubectl(t, c.output, 0), []string{"apply"})
		if err != nil || len(got) != len(c.want) {
			t.Fatalf("%s: %v, %v", c.name, got, err)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			}
		}
	}
	if _, err := kubectlJobs(fakeKubectl(t, "", 1), []string{"apply"}); err == nil {
		t.Fatal("a failed kubectl was not reported")
	}
	if _, err := kubectlJobs(fakeKubectl(t, "not json", 0), []string{"apply"}); err == nil {
		t.Fatal("unreadable output was not reported")
	}
}

func TestTellAndWaitCommandsUseSeparateAPIs(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/handovers":
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in["kind"] != "" || in["then"] != "依頼です。" {
				t.Errorf("tell body = %#v", in)
			}
			io.WriteString(w, `{"handover":{"id":"H123","task_id":"T1","next":"claude"}}`)
		case r.Method == "PATCH":
			io.WriteString(w, `{"id":"H123","task_id":"T1","acked":"2026-11-24T00:00:00Z","state":"acked"}`)
		case r.URL.Path == "/api/waits/W123":
			io.WriteString(w, `{"id":"W123","task_id":"T1","handover_id":"H123","state":"finished"}`)
		case r.URL.Path == "/api/waits":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"watching is disabled"}`)
		case r.URL.Path == "/api/handovers/H123":
			io.WriteString(w, `{"id":"H123","task_id":"T1","state":"queued","then":"続きです。"}`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	defer srv.Close()
	t.Setenv("LACIA_API", srv.URL)
	for _, run := range []func() error{
		func() error { return runTell([]string{"--task", "T1", "--to", "claude", "依頼です。"}) },
		func() error { return runWait([]string{"ack", "H123"}) },
		func() error { return runWait([]string{"ack", "W123"}) },
		func() error { return runWait([]string{"cancel", "H123"}) },
		func() error { return runWait([]string{"show", "W123"}) },
		func() error { return runWait([]string{"list", "--task", "T1", "--all"}) },
	} {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"POST /api/handovers", "PATCH /api/handovers/H123", "PATCH /api/waits/W123", "PATCH /api/handovers/H123", "GET /api/waits/W123", "GET /api/handovers/H123", "GET /api/waits?task=T1&all=1", "GET /api/handovers?task=T1&all=1"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("API calls = %q, want %q", calls, want)
	}
}
