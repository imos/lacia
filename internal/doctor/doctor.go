// Package doctor looks at the shape of a record repository and says what is
// wrong with it. It never changes anything. See docs/design/records.md.
package doctor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/imos/lacia"
	"github.com/imos/lacia/internal/mdlink"
	"github.com/imos/lacia/internal/project"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// Known are the top-level names lacia reads and writes, including the
// schedules/ and routines/ of older layouts, which are pointed out on their
// own.
var Known = []string{".lacia", "AGENTS.md", "CLAUDE.md", "README.md", "TODO.md", "rules", "memory", "schedules", "routines", "log", "areas", "archive", "local"}

// template are the top-level names the template adds to Known.
var template = []string{"LACIA_SOURCE", "LACIA_VERSION", "Makefile", "tools"}

// taskEntries are what a task directory may hold.
var taskEntries = map[string]bool{"task.md": true, "notes.md": true, task.HistoryFile: true, task.ArtifactsDir: true, task.FilesDir: true}

const MaxBrokenShown = 20

// Report is the result of a check. Errors are what must be fixed; Notes
// are for the owner to know.
type Report struct {
	Errors []string
	Notes  []string
	// Broken counts the relative links whose target is missing, when links
	// were checked.
	Broken int
}

func (r *Report) errorf(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Report) notef(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// Check looks at the repository at root. links also follows every relative
// link of the Markdown files outside archive/.
func Check(root string, links bool) Report {
	return CheckWithState(root, links, task.StateContext{Now: time.Now()})
}

// CheckWithState also checks known command-use times supplied by the caller.
func CheckWithState(root string, links bool, context task.StateContext) Report {
	var r Report
	cfg, err := task.LoadConfig(root)
	if err != nil {
		r.errorf("%v", err)
		return r
	}
	if err := cfg.RequireCurrentFormat(); err != nil {
		r.notef("%v", err)
		return r
	}
	checkLayout(&r, root, cfg)
	checkAreaRoutines(&r, root, cfg)
	jobs := schedule.Load(root)
	store, err := task.Open(root)
	if err != nil {
		r.errorf("タスクを読めません: %v", err)
	} else {
		for _, p := range store.Problems() {
			r.errorf("%s を読めません: %s", p.Path, p.Err)
		}
		tasks := store.List()
		byID := map[string]task.Task{}
		for _, t := range tasks {
			byID[t.ID] = t
		}
		for _, t := range tasks {
			for _, w := range task.StateWarnings(t, byID, context) {
				r.notef("%s: %s", t.ID, w)
			}
			checkTask(&r, root, t)
		}
		checkProjects(&r, root, cfg)
		checkDocuments(&r, root, cfg, jobs)
	}
	for _, job := range jobs {
		if job.Err != "" {
			r.errorf("予約 %s の定義を読めません: %s", job.Name, job.Err)
		}
		if !cfg.KeepsTasks() && job.Output == "task" {
			r.notef("定期タスク %s は output: task ですが、この記録はタスクを持たない使い方（.lacia/config.json の tasks が false）なので、タスクを作れません。結果を返事で伝えるだけなら output: none にしてください。", job.Name)
		}
	}
	checkTaskSetting(&r, root, cfg)
	checkIgnore(&r, root)
	if links {
		checkLinks(&r, root)
	}
	return r
}

// checkTaskSetting looks at what does not fit the repository's choice to
// keep tasks or not (task.Config.Tasks), and at a replaced core, which no
// longer receives what is added to the embedded one.
func checkTaskSetting(r *Report, root string, cfg task.Config) {
	if isFile(filepath.Join(root, "rules", "core.md")) {
		r.notef("rules/core.md が、実行ファイルの決まり（core）の代わりに使われています。core に足された決まりは、この記録には届きません。タスクを持たない使い方のために置いているときは、.lacia/config.json に \"tasks\": false を書けば、core がその使い方に合わせて切り替わります。この記録だけの決まりは rules/local.md へ移して、rules/core.md を外せます。")
	}
	if cfg.KeepsTasks() {
		return
	}
	if isFile(filepath.Join(root, "TODO.md")) {
		r.notef("この記録はタスクを持たない使い方なので、サーバーは TODO.md を作りません。残っている TODO.md は、消して構いません。")
	}
	store, err := task.Open(root)
	if err != nil {
		return
	}
	for _, t := range store.List() {
		if !t.Status.Closed() {
			r.notef("%s は閉じていないタスクです。この記録はタスクを持たない使い方なので、状態を変えられません。続きがあるなら覚え書きへ移し、閉じるときは、いったん .lacia/config.json の tasks を外してください。", t.ID)
		}
	}
}

// checkLayout looks at the top of a repository.
func checkLayout(r *Report, root string, cfg task.Config) {
	known := map[string]bool{}
	for _, name := range append(append([]string{}, Known...), template...) {
		known[name] = true
	}
	inConfig := map[string]bool{}
	for _, area := range cfg.Areas {
		inConfig[area] = true
		if known[area] {
			r.errorf("領域の名前 %s は、lacia が使う最上位の名前と同じです。別の名前にしてください。", area)
		}
		if isDir(filepath.Join(root, area)) {
			r.errorf("最上位に、領域と同じ名前のディレクトリー %s があります。領域は areas/%s に置いてください。", area, area)
		}
		if !isDir(filepath.Join(root, "areas", area)) {
			r.errorf("設定の領域 %s のディレクトリー areas/%s がありません。", area, area)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "areas"))
	for _, e := range entries {
		if e.IsDir() && !inConfig[e.Name()] {
			r.errorf("areas/%s は、設定の areas にない領域です。設定に足すか、移してください。", e.Name())
		}
	}
	top, _ := os.ReadDir(root)
	var own []string
	for _, e := range top {
		name := e.Name()
		if strings.HasPrefix(name, ".") && name != ".lacia" || known[name] || inConfig[name] {
			continue
		}
		own = append(own, name)
	}
	if len(own) > 0 {
		r.notef("最上位に、lacia の読まない名前があります。各自のものとして扱います: %s", strings.Join(own, "、"))
	}
}

func checkAreaRoutines(r *Report, root string, cfg task.Config) {
	if exists(root, "routines") {
		r.errorf("最上位の routines/ が残っています。形式2の定期タスクは areas/<領域>/routines/<ID>/ に置いてください。")
	}
	allowed := map[string]bool{
		"routine.md": true, "task.md": true, "notes.md": true, "procedure.md": true, "runs": true,
		"scripts": true, "ledger.json": true, task.HistoryFile: true, task.ArtifactsDir: true, task.FilesDir: true,
	}
	for _, area := range cfg.Areas {
		base := task.RoutineBase(area)
		routines, err := os.ReadDir(filepath.Join(root, base))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			r.errorf("%s を読めません: %v", base, err)
			continue
		}
		for _, entry := range routines {
			dir := filepath.Join(base, entry.Name())
			if !entry.IsDir() || !isFile(filepath.Join(root, dir, "routine.md")) {
				r.errorf("%s に routine.md のある定期タスクを置いてください。", dir)
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, dir))
			if err != nil {
				r.errorf("%s を読めません: %v", dir, err)
				continue
			}
			for _, f := range files {
				if !allowed[f.Name()] {
					r.errorf("%s は定期タスクの直下に置くものではありません。", filepath.Join(dir, f.Name()))
				}
			}
		}
	}
}

// checkTask looks at the files of one task.
func checkTask(r *Report, root string, t task.Task) {
	entries, err := os.ReadDir(filepath.Join(root, t.Dir))
	if err != nil {
		r.errorf("%s を読めません: %v", t.Dir, err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."), taskEntries[name]:
		case name == task.LogFile:
			r.errorf("%s/%s が残っています。形式1では、履歴は %s に書きます。", t.Dir, name, task.HistoryFile)
		default:
			r.errorf("%s/%s は、タスクのディレクトリーに置くものではありません。置けるのは task.md、notes.md、%s、artifacts/、files/ です。", t.Dir, name, task.HistoryFile)
		}
	}
	checkHistory(r, filepath.Join(root, t.Dir, task.HistoryFile), t.Dir+"/"+task.HistoryFile)
}

// checkHistory looks at each line of a log.jsonl.
func checkHistory(r *Report, file, rel string) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e struct{ At, Type string }
		switch {
		case json.Unmarshal([]byte(line), &e) != nil:
			r.errorf("%s の%d行目を、JSON として読めません。", rel, n)
		case e.At == "" || e.Type == "":
			r.errorf("%s の%d行目に、at か type がありません。", rel, n)
		}
	}
	if err := scanner.Err(); err != nil {
		r.errorf("%s を読めません: %v", rel, err)
	}
}

// checkProjects looks for directories under projects/ that are not
// projects, and at the histories of the projects.
func checkProjects(r *Report, root string, cfg task.Config) {
	for _, area := range cfg.Areas {
		dir := task.AreaDir(area) + "/projects"
		entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			rel := dir + "/" + e.Name()
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), project.File)); err != nil {
				r.errorf("%s には project.md がありません。projects/ の下には、プロジェクトだけを置きます。", rel)
				continue
			}
			checkHistory(r, filepath.Join(root, filepath.FromSlash(rel), task.HistoryFile), rel+"/"+task.HistoryFile)
		}
	}
}

// checkIgnore looks for the lines .gitignore needs: local/, the clone of the
// code under .lacia/source/, and files/ of the tasks and routines stay out of
// Git.
func checkIgnore(r *Report, root string) {
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	have := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	wants := []string{"/local/", "/.lacia/source/", "/areas/*/tasks/*/*/files/", "/areas/*/routines/*/files/"}
	var missing []string
	for _, want := range wants {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		r.notef(".gitignore に、次の行がありません: %s", strings.Join(missing, "、"))
	}
}

// checkLinks follows the relative links of the Markdown files outside
// archive/, local/ and the files/ of tasks.
func checkLinks(r *Report, root string) {
	var broken []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || rel == "archive" || rel == "local" ||
				d.Name() == task.FilesDir && strings.Contains(rel, "/tasks/")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		fenced := false
		for n, line := range strings.Split(string(data), "\n") {
			if mdlink.Fence(line) {
				fenced = !fenced
				continue
			}
			if fenced {
				continue
			}
			for _, l := range mdlink.Find(line) {
				if dest, err := url.Parse(l.Dest); err == nil && dest.Host == "" && !dest.IsAbs() {
					name, isDesign := strings.CutPrefix(dest.Path, lacia.DesignRoot)
					if isDesign {
						if !lacia.DesignDocumentExists(name) {
							broken = append(broken, fmt.Sprintf("%s:%d %s", rel, n+1, l.Dest))
						}
						continue
					}
				}
				target, _, ok := mdlink.Resolve(rel, l.Dest)
				if !ok || exists(root, target) {
					continue
				}
				broken = append(broken, fmt.Sprintf("%s:%d %s", rel, n+1, l.Dest))
			}
		}
		return nil
	})
	sort.Strings(broken)
	r.Broken = len(broken)
	if len(broken) == 0 {
		return
	}
	shown := broken
	if len(shown) > MaxBrokenShown {
		shown = shown[:MaxBrokenShown]
	}
	r.notef("行き先が見つからない相対リンクが%d個あります。先頭から%d個を表示します。\n  %s", len(broken), len(shown), strings.Join(shown, "\n  "))
}

func exists(root, target string) bool {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err == nil {
		return true
	}
	if plain, err := url.PathUnescape(target); err == nil && plain != target {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(path.Clean(plain))))
		return err == nil
	}
	return false
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
