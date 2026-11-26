package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/imos/lacia/internal/calendar"
	"github.com/imos/lacia/internal/task"
)

// Fetcher keeps the snapshot file up to date from Google Calendar.
type Fetcher struct {
	Location  *time.Location
	Store     *Store
	Client    *Client
	Path      string // snapshot file to write
	Overrides string // optional file with the user's display decisions
	Days      int    // how many days ahead to read; default 7
	Now       func() time.Time
	Publish   func([]byte) error // optional guarded snapshot writer

	fetchMu     sync.Mutex
	mu          sync.Mutex
	readers     map[[2]string]calendarReader
	resolvedFor string // the connected accounts the answer was found for
	resolved    time.Time
	loadDirect  sync.Once
	direct      atomic.Bool
}

type calendarReader struct {
	account string
	denied  bool
}

// Readers says, for each calendar on the page, which connected account can
// read it: its own account when connected, otherwise another connected
// account the calendar is shared with. A calendar nobody can read is absent.
// The answer is remembered for a few minutes, since finding it costs calls.
func (f *Fetcher) Readers(ctx context.Context) map[[2]string]string {
	readers := map[[2]string]string{}
	for key, reader := range f.resolveReaders(ctx) {
		if reader.account != "" {
			readers[key] = reader.account
		}
	}
	return readers
}

func (f *Fetcher) resolveReaders(ctx context.Context) map[[2]string]calendarReader {
	f.mu.Lock()
	defer f.mu.Unlock()
	accounts := f.Store.Accounts()
	key := strings.Join(accounts, ",")
	if f.readers != nil && key == f.resolvedFor && time.Since(f.resolved) < 10*time.Minute {
		return f.readers
	}
	out := map[[2]string]calendarReader{}
	for _, entry := range calendar.Scope {
		out[[2]string{entry.Account, entry.CalendarID}] = f.findReader(ctx, entry, accounts)
	}
	f.readers, f.resolved, f.resolvedFor = out, time.Now(), key
	return out
}

func (f *Fetcher) findReader(ctx context.Context, entry calendar.ScopeEntry, accounts []string) calendarReader {
	own, connected := f.Store.Get(entry.Account)
	if entry.CalendarID == "primary" {
		if connected {
			return calendarReader{account: entry.Account}
		}
		return calendarReader{denied: true}
	}
	result := calendarReader{denied: true}
	check := func(account string, cred Credential) bool {
		err := f.Client.readAccess(ctx, cred, entry.CalendarID)
		if err == nil {
			result = calendarReader{account: account}
			return true
		}
		if !errors.Is(err, errCalendarAccess) {
			result.denied = false
		}
		return false
	}
	if connected && check(entry.Account, own) {
		return result
	}
	for _, account := range accounts {
		if account != entry.Account {
			if cred, ok := f.Store.Get(account); ok && check(account, cred) {
				return result
			}
		}
	}
	return result
}

// Forget drops the remembered answer, after an account was connected or
// disconnected.
func (f *Fetcher) Forget() {
	f.mu.Lock()
	f.readers = nil
	f.mu.Unlock()
}

// Ready reports whether direct fetching can start or already owns the
// snapshot. After the takeover, a failed or removed grant does not restore
// the older producer's event details.
func (f *Fetcher) Ready() bool {
	f.loadDirect.Do(func() {
		f.direct.Store(f.previous().Direct)
	})
	if f.direct.Load() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return len(calendar.Scope) > 0 && len(f.Readers(ctx)) == len(calendar.Scope)
}

type snapshotFile struct {
	Direct     bool             `json:"direct_google,omitempty"`
	RangeStart string           `json:"range_start"`
	RangeEnd   string           `json:"range_end"`
	Sources    []map[string]any `json:"sources"`
}

func (f *Fetcher) previous() snapshotFile {
	var old snapshotFile
	if data, err := os.ReadFile(f.Path); err == nil {
		if json.Unmarshal(data, &old) != nil {
			return snapshotFile{}
		}
	}
	return old
}

func (f *Fetcher) overrides() map[string]Override {
	out := map[string]Override{}
	data, err := os.ReadFile(f.Overrides)
	if err != nil {
		return out
	}
	var file struct {
		Events []Override `json:"events"`
	}
	if json.Unmarshal(data, &file) == nil {
		for _, o := range file.Events {
			out[o.ID] = o
		}
	}
	return out
}

// Once reads every calendar and writes the snapshot. A calendar that fails
// keeps the events it had, marked partial, so a network error never empties
// the page.
func (f *Fetcher) Once(ctx context.Context) error {
	f.fetchMu.Lock()
	defer f.fetchMu.Unlock()
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	days := f.Days
	if days == 0 {
		days = 7
	}
	loc := f.Location
	if loc == nil {
		loc = task.JST
	}
	local := now.In(loc)
	from := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, days)

	old := f.previous()
	previous := map[[2]string]map[string]any{}
	for _, source := range old.Sources {
		account, _ := source["account"].(string)
		id, _ := source["calendar_id"].(string)
		previous[[2]string{account, id}] = source
	}
	overrides := f.overrides()
	var sources []map[string]any
	var firstErr error
	readers := f.resolveReaders(ctx)
	direct := old.Direct || len(calendar.Scope) != 0
	for _, entry := range calendar.Scope {
		key := [2]string{entry.Account, entry.CalendarID}
		reader := readers[key]
		cred, ok := f.Store.Get(reader.account)
		var source map[string]any
		err := errors.New("カレンダーを読み取れる Google アカウントを確認できませんでした")
		if ok {
			source, err = f.Client.Source(ctx, cred, reader.account, entry, entry.Label, from, to, now, overrides)
		} else if reader.denied {
			err = errCalendarAccess
		}
		if err != nil && !old.Direct {
			direct = false
		}
		if err == nil {
			slog.Info("calendar source fetched", "account", entry.Account, "calendar_id", entry.CalendarID, "updated_at", source["updated_at"], "events", len(source["events"].([]map[string]any)))
		}
		if err != nil {
			slog.Warn("calendar fetch", "account", entry.Account, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			source = previous[key]
			if errors.Is(err, errCalendarAccess) {
				f.Forget()
				source = emptySource(entry)
				source["status"], source["access_revoked"] = "unavailable", true
				source["error"] = errCalendarAccess.Error()
			} else {
				if source == nil {
					source = emptySource(entry)
				}
				if _, ok := source["range_start"]; !ok && old.RangeStart != "" && old.RangeEnd != "" && source["status"] == "ok" {
					source["range_start"], source["range_end"] = old.RangeStart, old.RangeEnd
				}
				source["status"] = "partial"
				source["error"] = "今回は予定を読み取れませんでした。以前に取得した予定があれば、その内容を表示しています。"
			}
			source["complete"] = false
		} else {
			source["range_start"], source["range_end"] = from.Format(time.DateOnly), to.Format(time.DateOnly)
		}
		source["attempted_at"] = now.UTC().Format(time.RFC3339)
		sources = append(sources, source)
	}
	snapshot := map[string]any{
		"schema_version": 1, "timezone": loc.String(), "updated_at": now.UTC().Format(time.RFC3339),
		"range_start": from.Format(time.DateOnly), "range_end": to.Format(time.DateOnly), "sources": sources,
		"direct_google": direct,
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	// Never publish a file the pages cannot read.
	if _, err := calendar.Parse(data); err != nil {
		return fmt.Errorf("作ったスナップショットが検証を通りませんでした: %w", err)
	}
	if f.Publish != nil {
		if err := f.Publish(append(data, '\n')); err != nil {
			return err
		}
		if direct {
			f.direct.Store(true)
		}
		return firstErr
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		return err
	}
	if direct {
		f.direct.Store(true)
	}
	return firstErr
}

func emptySource(entry calendar.ScopeEntry) map[string]any {
	return map[string]any{"account": entry.Account, "calendar_id": entry.CalendarID, "label": entry.Label, "events": []any{}, "updated_at": nil}
}

// Run fetches at the interval while every calendar is connected.
func (f *Fetcher) Run(ctx context.Context, interval time.Duration, kick <-chan struct{}) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-kick:
		}
		if f.Ready() {
			if err := f.Once(ctx); err != nil {
				slog.Error("calendar fetch", "error", err)
			} else {
				slog.Info("calendar fetched")
			}
		}
		timer.Reset(interval)
	}
}
