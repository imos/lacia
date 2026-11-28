package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestMailChecksAccountBeforeReadAndKeepsResultSmall(t *testing.T) {
	for _, test := range []struct {
		name, connected, results string
		found, invalid           bool
	}{
		{"wrong account", "other@example.com", `{"message_ids":["private-1"]}`, false, true},
		{"match", "me@example.com", `{"message_ids":["private-1"],"subject":"private-2"}`, true, false},
		{"none", "me@example.com", `{"message_ids":[]}`, false, false},
		{"malformed", "me@example.com", `{"messages":"very private"}`, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var methods []string
			rpc := func(_ context.Context, method string, params any, out any) error {
				methods = append(methods, method)
				data := ""
				switch method {
				case "thread/start":
					if params.(map[string]any)["ephemeral"] != true {
						t.Fatal("the mail thread is persistent")
					}
					data = `{"thread":{"id":"test"}}`
				case "mcpServerStatus/list":
					data = `{"data":[{"name":"codex_apps","tools":{"gmail.get_profile":{},"gmail.search_email_ids":{}}}]}`
				case "mcpServer/tool/call":
					tool := params.(map[string]any)["tool"].(string)
					methods[len(methods)-1] = tool
					if tool == "gmail.get_profile" {
						data = `{"structuredContent":{"email":"` + test.connected + `"}}`
					} else {
						if got := params.(map[string]any)["arguments"]; !reflect.DeepEqual(got, map[string]any{"query": "from:example.com", "max_results": 1}) {
							t.Fatalf("arguments = %#v", got)
						}
						data = `{"structuredContent":` + test.results + `}`
					}
				case "thread/unsubscribe":
					return nil
				default:
					t.Fatalf("unexpected method %s", method)
				}
				return json.Unmarshal([]byte(data), out)
			}
			found, err := lookupMail(context.Background(), rpc, "/workspace", "me@example.com", "from:example.com")
			if found != test.found || (err != nil) != test.invalid || (err != nil && err != errMailUnavailable) {
				t.Fatalf("found, error = %v, %v", found, err)
			}
			want := []string{"thread/start", "mcpServerStatus/list", "gmail.get_profile"}
			if test.connected == "me@example.com" {
				want = append(want, "gmail.search_email_ids")
			}
			want = append(want, "thread/unsubscribe")
			if !reflect.DeepEqual(methods, want) {
				t.Fatalf("methods = %v, want %v", methods, want)
			}
		})
	}
}
