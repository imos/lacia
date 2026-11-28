package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
)

const restartRequestFile = "restart-when-idle"

func runIdleRestart(args []string) error {
	fs := commandFlags("restart")
	whenIdle := fs.Bool("when-idle", false, "処理が終わったときに、サーバーを終了して監督プロセスに再起動を任せます")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*whenIdle || fs.NArg() != 0 {
		return errors.New("lacia restart --when-idle の形で指定してください")
	}
	if err := askIdleRestart(); err != nil {
		return err
	}
	fmt.Println("サーバーの再起動を予約しました。処理が終わると、監督プロセスが起動し直します。")
	return nil
}

// askIdleRestart asks the running server to keep the request. A server
// from before this route answers that it does not know it, and that is
// said plainly: a request it never watches for would wait for ever.
func askIdleRestart() error {
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	err := apiCall(http.MethodPost, "/api/restart", nil, nil)
	var status *apiStatusError
	if errors.As(err, &status) && (status.code == http.StatusNotFound || status.code == http.StatusMethodNotAllowed) {
		return errors.New("稼働中のサーバーは、コマンドからの再起動の予約に対応していない版です。処理が空いているときに、監督プロセスの手順でサーバーを起動し直して、新しい版へ切り替えてください。")
	}
	return err
}

// requestIdleRestart leaves the request where the server looks for it.
func requestIdleRestart(state *disk.Dir) error {
	err := state.CreateNew(restartRequestFile, nil, 0o600)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := state.Lstat(restartRequestFile)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("再起動の予約を保存する場所に、通常のファイル以外のものがあります。サーバーの状態ディレクトリーを確かめてください")
	}
	return nil
}

type idleRestart struct {
	state    *disk.Dir
	stopping atomic.Bool
	mu       sync.Mutex
	active   int
	last     time.Time
}

func (r *idleRestart) pending() bool {
	if r.stopping.Load() {
		return true
	}
	if r.state == nil {
		return false
	}
	info, err := r.state.Lstat(restartRequestFile)
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
	if err := r.state.RemoveFile(restartRequestFile); err != nil {
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
				slog.Error("consume idle restart", "detail", "再起動の予約を消せなかったため、サーバーは稼働を続けます。", "error", err)
			}
			if ok {
				slog.Info("stopping for planned restart", "detail", "予約した再起動のため、サーバーをいったん終了します。")
				stop()
				return
			}
		}
	}
}
