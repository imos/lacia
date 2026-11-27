package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDesignDocuments(t *testing.T) {
	store, h := setup(t)
	setupLink := (&url.URL{Path: "/design/setup", Fragment: "ローカル環境で始める"}).String()
	for _, check := range []struct {
		url  string
		want string
	}{
		{"/", `href="/design/"`},
		{"/design/", `href="/design/rules"`},
		{"/design/", `href="` + setupLink + `"`},
		{"/design/setup", `id="ローカル環境で始める"`},
		{"/design/setup", `href="/design/license"`},
		{"/design/setup", `href="/design/"`},
		{"/design/rules", `id="記録の側での上書き"`},
		{"/design/rules?view=source", `# 決まりと会話の起動`},
		{"/design/rules", "版：test"},
	} {
		code, body, _ := get(t, h, check.url)
		if code != http.StatusOK || !strings.Contains(body, check.want) {
			t.Errorf("%s: status %d, want %q", check.url, code, check.want)
		}
	}
	for _, url := range []string{"/design/missing", "/design/rules.md", "/design/%2e%2e%2fREADME.md", "/design/.lacia", "/files/.lacia/secret.json"} {
		if code, _, _ := get(t, h, url); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", url, code)
		}
	}
	door, err := (&Server{Store: store, DoorOnly: true}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := get(t, door, "/design/rules"); code != http.StatusForbidden {
		t.Errorf("door-only design page status %d, want 403", code)
	}
}
