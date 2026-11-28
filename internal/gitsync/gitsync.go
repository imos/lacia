// Package gitsync saves the task repository to Git in the background.
// Pages never wait for it: the files on disk are what the server shows, and
// commits and pushes follow within about a minute.
//
// Something that keeps writing to one task, such as a script stuck in a
// loop, must not turn into an endless row of commits. Each task therefore
// has a budget of commits: a burst is saved at once, and after it the
// changes of that task are gathered into one commit every few minutes.
package gitsync

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/effect/program"
)

// Syncer commits and pushes whatever changed in Dir.
type Syncer struct {
	Dir      string
	Interval time.Duration // how often to look for changes made outside the server
	Settle   time.Duration // how long to wait after Kick, to batch quick edits
	Prepare  func() error  // runs before each look, e.g. to regenerate TODO.md
	Author   string        // "Name <email>"; empty uses the repository's identity
	// Burst is how many commits in a row one task may cause before its
	// changes are held back; Refill is how long it takes to earn one more.
	// Zero means 10 commits and 5 minutes.
	Burst  int
	Refill time.Duration
	Now    func() time.Time // replaceable in tests

	budget map[string]*budget // per task ID; "" stands for files outside tasks
	held   bool               // the last look left changes for a later commit

	kick chan struct{}
	mu   sync.Mutex
	err  string
}

// Kick asks for a save soon. It never blocks.
func (s *Syncer) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Notice describes the last failure, or is empty when Git is up to date.
func (s *Syncer) Notice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// PublishedRevision reads the remote-tracking ref updated by Git after a
// successful push. Local commits waiting for a push are not advertised.
func (s *Syncer) PublishedRevision(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	revision, err := s.git(ctx, "rev-parse", "--verify", "refs/remotes/origin/main^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(revision)
}

// Run loops until ctx ends, then saves one last time.
func (s *Syncer) Run(ctx context.Context) {
	if s.Interval == 0 {
		s.Interval = time.Minute
	}
	if s.Settle == 0 {
		s.Settle = 5 * time.Second
	}
	s.kick = make(chan struct{}, 1)
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.once(context.Background(), true)
			return
		case <-ticker.C:
		case <-s.kick:
			select {
			case <-ctx.Done():
			case <-time.After(s.Settle):
			}
		}
		s.once(ctx, false)
	}
}

func (s *Syncer) once(ctx context.Context, force bool) {
	err := s.sync(ctx, force)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		slog.Error("git sync", "error", err)
		s.err = "Gitへの保存に失敗しています。画面の内容は最新ですが、まだバックアップされていません。（" + err.Error() + "）"
		return
	}
	s.err = ""
}

var taskDir = regexp.MustCompile(`/tasks/[0-9]{4}-[0-9]{2}/([A-Z][0-9]+)`)

// budget counts the commits one task may still cause right now.
type budget struct {
	tokens float64
	at     time.Time
}

// spend decides whether changes to these tasks may be committed now. They
// may when at least one of the tasks is within its budget: its commit
// carries the changes of the others along, at no cost in commits. When
// every task has used its budget up, the changes wait, and a later look
// commits them together.
func (s *Syncer) spend(ids []string) bool {
	burst, refill := float64(s.Burst), s.Refill
	if burst <= 0 {
		burst = 10
	}
	if refill <= 0 {
		refill = 5 * time.Minute
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if s.budget == nil {
		s.budget = map[string]*budget{}
	}
	// A budget that has filled up again says nothing; forget it.
	for id, b := range s.budget {
		if b.tokens+now.Sub(b.at).Seconds()/refill.Seconds() >= burst {
			delete(s.budget, id)
		}
	}
	allowed := false
	for _, id := range ids {
		b := s.budget[id]
		if b == nil {
			b = &budget{tokens: burst, at: now}
			s.budget[id] = b
		}
		b.tokens, b.at = b.tokens+now.Sub(b.at).Seconds()/refill.Seconds(), now
		if b.tokens >= 1 {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	for _, id := range ids {
		if b := s.budget[id]; b.tokens >= 1 {
			b.tokens--
		}
	}
	return true
}

// Sync commits pending changes and pushes any commits the remote lacks,
// whatever the budgets say.
func (s *Syncer) Sync(ctx context.Context) error {
	return s.sync(ctx, true)
}

func (s *Syncer) sync(ctx context.Context, force bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if s.Prepare != nil {
		if err := s.Prepare(); err != nil {
			return fmt.Errorf("保存の準備ができませんでした: %w", err)
		}
	}
	status, err := s.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	hold := strings.TrimSpace(status) != "" && !force && !s.spend(changed(status))
	if hold != s.held {
		if s.held = hold; hold {
			slog.Warn("git sync: commits come too fast, gathering changes", "tasks", strings.Join(changed(status), ","))
		}
	}
	if strings.TrimSpace(status) != "" && !hold {
		if _, err := s.git(ctx, "add", "--all"); err != nil {
			return err
		}
		args := []string{"commit", "--quiet", "-m", message(status)}
		if s.Author != "" {
			args = append(args, "--author", s.Author)
		}
		if _, err := s.git(ctx, args...); err != nil {
			return err
		}
	}
	// A repository kept on one machine alone has nowhere to push to: the
	// commits are the whole of the saving.
	if _, err := s.git(ctx, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		return nil
	}
	// Push whenever the remote is behind, including after an earlier failure.
	ahead, err := s.git(ctx, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(ahead) == "0" {
		return nil
	}
	_, err = s.git(ctx, "push", "--quiet")
	return err
}

// changed lists the tasks with pending changes, and "" when a file outside
// the tasks changed. TODO.md does not count: the server rewrites it from
// the tasks, so it never changes on its own.
func changed(status string) []string {
	ids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if m := taskDir.FindStringSubmatch(line); m != nil {
			ids[m[1]] = true
		} else if !strings.HasSuffix(line, " TODO.md") {
			ids[""] = true
		}
	}
	var list []string
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	return list
}

// message names the tasks that changed, so the history can be skimmed.
func message(status string) string {
	files := len(strings.Split(strings.TrimSpace(status), "\n"))
	var list []string
	for _, id := range changed(status) {
		if id != "" {
			list = append(list, id)
		}
	}
	switch {
	case len(list) == 0:
		return fmt.Sprintf("chore(sync): %d個のファイルの変更を保存", files)
	case len(list) > 6:
		return fmt.Sprintf("chore(sync): %s ほか%d件のタスクの変更を保存", strings.Join(list[:6], ", "), len(list)-6)
	}
	return fmt.Sprintf("chore(sync): %s の変更を保存", strings.Join(list, ", "))
}

func (s *Syncer) git(ctx context.Context, args ...string) (string, error) {
	cmd := program.Git().Command(ctx, append([]string{"-C", s.Dir}, args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errOut.String())
		return "", fmt.Errorf("git %s が失敗しました: %v: %s", args[0], err, detail[:min(len(detail), 300)])
	}
	return out.String(), nil
}
