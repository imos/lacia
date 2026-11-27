package web

import (
	"context"
	"github.com/imos/lacia/internal/testrecords"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imos/lacia/internal/self"
	"github.com/imos/lacia/internal/task"
)

func TestSelf(t *testing.T) {
	open := func(mode string, home bool) (*task.Store, *self.Keeper) {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, ".lacia"), 0o755)
		os.WriteFile(filepath.Join(root, task.ConfigFile), []byte(`{"areas":["work"]}`), 0o644)
		store, err := task.Open(testrecords.Init(t, root))
		if err != nil {
			t.Fatal(err)
		}
		return store, &self.Keeper{Path: filepath.Join(root, "memory", "self.md"), Dir: t.TempDir(), Label: mode, ChatSide: home}
	}
	homeStore, homeKeeper := open("家庭モード", true)
	home, err := (&Server{Store: homeStore, DoorOnly: true, Self: homeKeeper}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	homeServer := httptest.NewServer(home)
	defer homeServer.Close()
	workStore, workKeeper := open("仕事モード", false)
	workKeeper.At = "02:30"
	link := &Peer{URL: homeServer.URL}
	work, err := (&Server{Store: workStore, Site: "https://lacia.example.test", Peer: link, Self: workKeeper}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(h http.Handler, method, target, body, remote, host, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.RemoteAddr, req.Host = remote, host
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const pod, local, form, js = "10.1.2.3:40000", "127.0.0.1:40000", "application/x-www-form-urlencoded", "application/json"

	// Lacia jots things down from inside the Pod; other Pods cannot.
	if rec := call(work, "POST", "/api/self/notes", `{"text":"表より先に、おすすめを求められました。"}`, local, "127.0.0.1:8080", js); rec.Code != http.StatusCreated {
		t.Fatalf("note = %d %s", rec.Code, rec.Body)
	}
	if rec := call(work, "POST", "/api/self/notes", `{"text":"x","task":"T404"}`, local, "127.0.0.1:8080", js); rec.Code != http.StatusBadRequest {
		t.Errorf("a note about an unknown task = %d", rec.Code)
	}
	if rec := call(work, "POST", "/api/self/notes", `{"text":"よそからの書き留めです。"}`, pod, "tasks.internal", js); rec.Code != http.StatusForbidden {
		t.Errorf("a note from another Pod = %d", rec.Code)
	}

	// What the user writes on the page is kept as the user's words, and only
	// the public address may speak as the user.
	typed := url.Values{"text": {"もう少し率直でよいです。"}}.Encode()
	if rec := call(work, "POST", "/self/comments", typed, local, "127.0.0.1:8080", form); rec.Code != http.StatusForbidden {
		t.Errorf("a comment from inside the Pod = %d", rec.Code)
	}
	if rec := call(work, "POST", "/self/comments", typed, pod, "lacia.example.test", form); rec.Code != http.StatusSeeOther {
		t.Errorf("a comment from the page = %d", rec.Code)
	}
	notes := workKeeper.Notes()
	if len(notes) != 2 || notes[0].From != "" || notes[1].From != "オーナーさん" {
		t.Fatalf("notes = %+v", notes)
	}
	page := call(work, "GET", "/self", "", pod, "lacia.example.test", "")
	for _, want := range []string{"もう少し率直でよいです。", "表より先に、おすすめを求められました。", "自分についての記録はまだありません。", "この記録を自動で書き直す設定になっていません。", `<a href="/self">レイシアのこと</a>`} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// HTML uses reader-facing labels; the API and file keep first-person headings.
	doc := "# わたしのこと\n\n## わたし自身\n\n- 本文です。\n\n## 仕事モードで\n"
	os.MkdirAll(filepath.Dir(workKeeper.Path), 0o755)
	if err := os.WriteFile(workKeeper.Path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	page = call(work, "GET", "/self", "", pod, "lacia.example.test", "")
	for _, want := range []string{"レイシアのこと", "レイシア自身", "本文です。"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	api := call(work, "GET", "/api/self", "", local, "127.0.0.1:8080", "")
	if !strings.Contains(api.Body.String(), "# わたしのこと") || !strings.Contains(api.Body.String(), "## わたし自身") || workKeeper.Document() != doc {
		t.Fatal(api.Body.String())
	}
	// No model is set up here, so there is nothing to start.
	if rec := call(work, "POST", "/api/self/sleep", `{}`, local, "127.0.0.1:8080", js); rec.Code != http.StatusNotFound {
		t.Errorf("sleep without a model = %d", rec.Code)
	}

	// The door for the core is on the chat side's server only.
	if rec := call(work, "GET", "/api/self/core", "", pod, "tasks.internal", ""); rec.Code != http.StatusNotFound {
		t.Errorf("the core door on the task side's server = %d", rec.Code)
	}
	if rec := call(home, "GET", "/api/self", "", pod, "chat.internal", ""); rec.Code != http.StatusForbidden {
		t.Errorf("the chat side's view from another Pod = %d", rec.Code)
	}
	ctx := context.Background()
	if core, err := link.Core(ctx); err != nil || core != "" {
		t.Errorf("core before the first night = %q, %v", core, err)
	}
	if err := link.ShareCore(ctx, "- 結論を先に聞きたい方だと考えています。"); err != nil {
		t.Fatal(err)
	}
	if core, err := link.Core(ctx); err != nil || core != "- 結論を先に聞きたい方だと考えています。" {
		t.Errorf("core = %q, %v", core, err)
	}
	if err := link.ShareCore(ctx, " "); err == nil {
		t.Error("an empty core was accepted")
	}
	if !strings.Contains(homeKeeper.Document(), "## 家庭モードで") {
		t.Errorf("home document:\n%s", homeKeeper.Document())
	}
}

func TestSelfReaderHeadings(t *testing.T) {
	doc := "# わたしのこと\n\n## わたし自身\n\n- わたし自身について考えます。\n\n## 家庭モードで\n"
	got := selfDisplayDocument(doc)
	if !strings.Contains(got, "# レイシアのこと\n") || !strings.Contains(got, "## レイシア自身\n") || !strings.Contains(got, "- わたし自身について考えます。") {
		t.Fatal(got)
	}
	if strings.Contains(got, "## わたし自身\n") {
		t.Fatal(got)
	}
}

func TestSelfItemDates(t *testing.T) {
	got := string(selfMarkdown("- [2026-11-24] 手紙のことを知りたいです。\n- 日付のない項目です。\n\n本文の [2026-11-24] は、そのままです。"))
	if !strings.Contains(got, `<li><span class="item-date">2026-11-24</span> 手紙のことを知りたいです。</li>`) || !strings.Contains(got, "<li>日付のない項目です。</li>") ||
		!strings.Contains(got, "本文の [2026-11-24] は、そのままです。") {
		t.Fatal(got)
	}
}

func TestSelfImprovementsAPI(t *testing.T) {
	root := t.TempDir()
	told := 0
	keeper := &self.Keeper{Path: filepath.Join(root, "repo", "memory", "self.md"), Dir: filepath.Join(root, "state"), Label: "仕事モード",
		At: "02:30", ImproveTell: func(string, string) error { told++; return nil }}
	keeper.Think = func(context.Context, string) (string, error) {
		return "<わたしのこと>\n# わたしのこと\n\n## わたし自身\n\n- 項目です。\n\n## 仕事モードで\n\n- 仕事の項目です。\n</わたしのこと>\n<今夜変えたこと>\n最初の版です。\n</今夜変えたこと>\n" +
			"<仕組みで直せそうなこと>\n- 題名: 書き留めのきっかけを作る\n  理由: 何晩も続いています。\n</仕組みで直せそうなこと>\n", nil
	}
	if night, err := keeper.Sleep(context.Background(), true); err != nil || !night.Accepted || told != 1 {
		t.Fatalf("night = %+v, %v, told %d", night, err, told)
	}
	os.MkdirAll(filepath.Join(root, "repo", ".lacia"), 0o755)
	os.WriteFile(filepath.Join(root, "repo", task.ConfigFile), []byte(`{"areas":["work"]}`), 0o644)
	store, err := task.Open(testrecords.Init(t, filepath.Join(root, "repo")))
	if err != nil {
		t.Fatal(err)
	}
	h, err := (&Server{Store: store, Self: keeper}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.RemoteAddr, req.Host = "127.0.0.1:40000", "127.0.0.1:8080"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("GET", "/api/self/improvements", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"K1"`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := call("POST", "/api/self/improvements/K1", `{"outcome":"task","task":"T404"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown task = %d %s", rec.Code, rec.Body)
	}
	if rec := call("POST", "/api/self/improvements/K1", `{"outcome":"skipped","note":"願いごとでした。"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"skipped"`) {
		t.Errorf("skip = %d %s", rec.Code, rec.Body)
	}
	if rec := call("GET", "/self", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "書き留めのきっかけを作る") || !strings.Contains(rec.Body.String(), `class="item-date"`) || !strings.Contains(rec.Body.String(), "毎日 02:30 に書き留めを読み返し") {
		t.Errorf("page = %d", rec.Code)
	}
}
