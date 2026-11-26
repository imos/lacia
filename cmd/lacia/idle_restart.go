package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const restartRequestFile = "restart-when-idle"

func runIdleRestart(args []string) error {
	fs := commandFlags("restart")
	whenIdle := fs.Bool("when-idle", false, "処理が終わったときに、サーバーを終了して監督プロセスに再起動を任せます")
	state := fs.String("state", "", "対象サーバーが使っている状態ディレクトリー")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*whenIdle || *state == "" || fs.NArg() != 0 {
		return errors.New("lacia restart --when-idle --state DIR の形で指定してください")
	}
	path := filepath.Join(*state, restartRequestFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		closeErr := file.Close()
		if closeErr != nil {
			return closeErr
		}
	} else {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return errors.New("再起動の予約を普通のファイルに保存できません")
		}
	}
	fmt.Println("処理が終わったときの再起動を予約しました。")
	return nil
}

type idleRestart struct {
	path     string
	stopping atomic.Bool
	mu       sync.Mutex
	active   int
	last     time.Time
}

func (r *idleRestart) pending() bool {
	if r.stopping.Load() {
		return true
	}
	if r.path == "" {
		return false
	}
	info, err := os.Lstat(r.path)
	return err == nil && info.Mode().IsRegular()
}

func (r *idleRestart) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.stopping.Load() {
			r.mu.Unlock()
			http.Error(w, "サーバーを起動し直しています。", http.StatusServiceUnavailable)
			return
		}
		r.active++
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.active--
			r.last = time.Now()
			r.mu.Unlock()
		}()
		next.ServeHTTP(w, req)
	})
}

// runNightly admits nightly work under the same lock as shutdown.
func (r *idleRestart) runNightly(run func()) {
	r.mu.Lock()
	if r.pending() {
		r.mu.Unlock()
		return
	}
	r.active++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.mu.Unlock()
	}()
	run()
}

func (r *idleRestart) stopIfIdle(now time.Time, busy func() bool) (bool, error) {
	if !r.pending() || busy() {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != 0 || now.Sub(r.last) < 2*time.Second {
		return false, nil
	}
	// Keep both start gates closed when the request is consumed.
	r.stopping.Store(true)
	if err := os.Remove(r.path); err != nil {
		r.stopping.Store(false)
		if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

func (r *idleRestart) run(ctx context.Context, stop func(), busy func() bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			ok, err := r.stopIfIdle(now, busy)
			if err != nil {
				slog.Error("consume idle restart", "error", err)
			}
			if ok {
				slog.Info("stopping for planned restart")
				stop()
				return
			}
		}
	}
}
