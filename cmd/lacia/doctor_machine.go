package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/effect/program"
	"github.com/imos/lacia/internal/home"
	"github.com/imos/lacia/internal/web"
)

// setUpHere reports whether root is the records this machine serves, as
// lacia setup leaves it: the settings of the machine name it, and it is not
// a read-only copy of records served elsewhere. Only then is the machine
// itself looked at.
func setUpHere(root string) bool {
	machine, err := machineRoot()
	return err == nil && machine != "" && machine == root && !readOnlyCopy(root)
}

// machineIssues finds what keeps this machine from serving the records.
// The server asks only what it can see from inside; a terminal also asks
// what the server cannot: whether the command is in PATH, whether the
// server is registered and answering, and whether it can push.
func machineIssues(ctx context.Context, c *serveConfig, terminal bool) []web.MachineIssue {
	var issues []web.MachineIssue
	add := func(problem, fix, section string) {
		issues = append(issues, web.MachineIssue{Problem: problem, Fix: fix, Guide: guide(section)})
	}
	if moves := home.Moves(os.Getenv); len(moves) > 0 {
		add("端末の設定を、以前の版の置き場所から読んでいます。", strings.Join(moves, "\n"), "")
	}
	path := os.Getenv("PATH")
	if terminal && runtime.GOOS == "darwin" {
		if registered := registeredPath(); registered != "" {
			path = registered
		}
	}
	agents := 0
	for _, kind := range []string{"codex", "claude"} {
		fields := strings.Fields(*c.str(kind))
		if len(fields) == 0 {
			continue
		}
		agents++
		if !findIn(fields[0], path) {
			add(fmt.Sprintf("常駐のサーバーから、%s（%s）が見つかりません。依頼を受け付けられません。", kind, fields[0]), "lacia setup\n（いまのターミナルの PATH で、常駐の定義を書き直します）", "agents")
		}
	}
	if agents == 0 {
		add("依頼を渡す Codex も Claude Code も、設定されていません。", "端末の設定（"+*c.str("config")+"）に「claude = claude」か「codex = codex」を書いて、lacia setup", "agents")
	}
	if !terminal {
		return issues
	}
	if _, err := program.Lacia("lacia").Find(); err != nil {
		add("lacia が、シェルの PATH にありません。", `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc`+"\n（そのあと、ターミナルを開き直します）", "path")
	}
	if runtime.GOOS == "darwin" && !loaded(ctx, serveLabel()) {
		add("ログインのときにサーバーを起動する登録が、ありません。", "lacia setup", "server")
	} else if !serving(c) {
		add("サーバーが応えません。", "ログ（"+serveLog()+"）を見てください。直したら、lacia setup", "server")
	}
	if *c.on("git-sync") {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := program.Git().Output(ctx, c.root, "push", "--dry-run", "--quiet"); err != nil {
			add("記録を GitHub へプッシュできません。", "gh auth login\ngh auth setup-git\n（記録の取得先が SSH の URL なら、HTTPS の URL に変えます: git -C "+c.root+" remote set-url origin https://github.com/<名前>/<リポジトリー>.git）", "github")
		}
	}
	return issues
}

// installGuide is the guide for people who set lacia up, on the public
// repository. guideSections names the parts of it that the issues point to,
// by the anchors GitHub gives their headings.
const installGuide = "https://github.com/imos/lacia/blob/main/docs/install-guide.md"

var guideSections = map[string]string{
	"agents": "依頼を受け付けられない",
	"path":   "lacia-が見つからない",
	"server": "サーバーが動いていない",
	"github": "github-へつなぐ",
}

func guide(section string) string {
	if section == "" {
		return ""
	}
	return installGuide + "#" + guideSections[section]
}

// registeredPath is the PATH the definition of the server gives it.
var registeredPathPattern = regexp.MustCompile(`<key>PATH</key><string>([^<]*)</string>`)

func registeredPath() string {
	data, err := os.ReadFile(launchAgentPath(serveLabel()))
	if err != nil {
		return ""
	}
	if m := registeredPathPattern.FindSubmatch(data); m != nil {
		return strings.ReplaceAll(strings.ReplaceAll(string(m[1]), "&amp;", "&"), "&lt;", "<")
	}
	return ""
}

// findIn reports whether the command can be started with the PATH given.
func findIn(command, path string) bool {
	if strings.Contains(command, "/") {
		return executable(command)
	}
	for _, dir := range filepath.SplitList(path) {
		if dir != "" && executable(filepath.Join(dir, command)) {
			return true
		}
	}
	return false
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func serving(c *serveConfig) bool {
	return apiClient("http://"+c.value("addr")).request(http.MethodGet, "/healthz", nil, nil, 2*time.Second) == nil
}

// machineReport is the part of lacia doctor about this machine: what it is
// running and where, and then what to fix. It returns how many to fix.
func machineReport(out io.Writer, c *serveConfig) int {
	ctx := context.Background()
	fmt.Fprintln(out, "\nこの端末")
	rev := version()
	if release != "" {
		rev = release + "（" + rev + "）"
	}
	fmt.Fprintf(out, "  版: %s\n  記録: %s\n  端末の設定: %s\n  ログ: %s\n", rev, c.root, *c.str("config"), serveLog())
	state := "応えません"
	if serving(c) {
		state = "動いています"
	}
	fmt.Fprintf(out, "  サーバー（http://%s/）: %s\n", c.value("addr"), state)
	if pushed, err := program.Git().Output(ctx, c.root, "log", "-1", "--format=%cd", "--date=format-local:%Y-%m-%d %H:%M", "@{upstream}"); err == nil {
		fmt.Fprintf(out, "  最後にプッシュしたコミット: %s\n", strings.TrimSpace(string(pushed)))
	}
	fmt.Fprintf(out, "  夜の処理: %s\n", nightSummary(c))
	if kind, _, err := nightAgentOf(c).find(); *c.str("claude") != "" || err == nil && kind != nightCodex {
		fmt.Fprintln(out, "  Claude Code のログインには、期限があります。常駐のサーバーは、ログインをし直せません。claude を実行したときに期限の注意が出たら、ターミナルで claude auth login をし直してください。")
	}
	issues := machineIssues(ctx, c, true)
	for _, issue := range issues {
		fmt.Fprintf(out, "直すこと: %s\n  %s\n", issue.Problem, strings.ReplaceAll(issue.Fix, "\n", "\n  "))
		if issue.Guide != "" {
			fmt.Fprintf(out, "  案内: %s\n", issue.Guide)
		}
	}
	return len(issues)
}

// machineWatch keeps what the server found about its machine, looked at
// again every ten minutes, for the home page.
func machineWatch(c *serveConfig) func() []web.MachineIssue {
	var mu sync.Mutex
	var found []web.MachineIssue
	var at time.Time
	return func() []web.MachineIssue {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) > 10*time.Minute {
			found, at = machineIssues(context.Background(), c, false), time.Now()
		}
		return found
	}
}
