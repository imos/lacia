package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/imos/lacia/internal/caller"
)

// A script stuck in a loop once wrote the same six files and the same note
// to one task every ten seconds for two hours. Nothing in the server found
// that odd. The guard below does: when one source sends the same write to
// one task again and again, the server stops taking that source's writes
// to the task for a while, says so in the log, and puts the task before
// the user. Writes from the browser are never stopped, nor are those of
// other conversations.
const (
	floodWindow  = 30 * time.Minute // how far back writes are counted
	floodRepeats = 10               // the same write this often is a loop
	floodWrites  = 1000             // so is this many writes of any kind
	floodPause   = 30 * time.Minute // how long the source is then refused
)

type floodGuard struct {
	mu    sync.Mutex
	same  map[string][]time.Time // task, source and write -> when it came
	all   map[string][]time.Time // task and source -> when anything came
	until map[string]time.Time   // task and source -> refused until then
}

func recent(times []time.Time, now time.Time) []time.Time {
	for len(times) > 0 && now.Sub(times[0]) > floodWindow {
		times = times[1:]
	}
	return times
}

// stopped reports until when writes from the source are refused.
func (g *floodGuard) stopped(source string, now time.Time) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.until[source]
	if ok && !now.Before(until) {
		delete(g.until, source)
		ok = false
	}
	return until, ok
}

// record counts one write, and reports whether it made the source a loop:
// the time until which it is refused from now on, and how often the same
// write came.
func (g *floodGuard) record(source, write string, now time.Time) (until time.Time, repeats int, tripped bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.same == nil {
		g.same, g.all, g.until = map[string][]time.Time{}, map[string][]time.Time{}, map[string]time.Time{}
	}
	// Forget what has gone quiet, so the maps stay small.
	if len(g.same) > 4096 {
		for key, times := range g.same {
			if len(recent(times, now)) == 0 {
				delete(g.same, key)
			}
		}
		for key, times := range g.all {
			if len(recent(times, now)) == 0 {
				delete(g.all, key)
			}
		}
	}
	key := source + "\x00" + write
	g.same[key] = append(recent(g.same[key], now), now)
	g.all[source] = append(recent(g.all[source], now), now)
	repeats = len(g.same[key])
	if repeats < floodRepeats && len(g.all[source]) < floodWrites {
		return time.Time{}, repeats, false
	}
	// Start counting afresh once the pause is over.
	delete(g.same, key)
	delete(g.all, source)
	until = now.Add(floodPause)
	g.until[source] = until
	return until, repeats, true
}

// floodSource names who writes to which task: a conversation by its key,
// and every command that no conversation ran as one source.
func floodSource(id string, sess caller.Session, known bool) string {
	if known {
		return id + "\x00" + sess.Key
	}
	return id + "\x00"
}

// floodStopped answers the request itself, and returns true, while writes
// from its source to the task are refused.
func (s *Server) floodStopped(w http.ResponseWriter, ref string, sess caller.Session, known bool) bool {
	id, ok := s.Store.Resolve(ref)
	if !ok {
		return false
	}
	until, stopped := s.flood.stopped(floodSource(id, sess, known), s.Now())
	if !stopped {
		return false
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf(
		"短い間に書き込みが続いたため、この呼び出し元から %s への書き込みを %s まで止めています。スクリプトや定期実行の設定で、同じタスクに何度も書き込んでいないか確かめてください。",
		id, until.In(s.Store.Location()).Format("15:04"))})
	return true
}

// floodWrote counts a write that was taken. what describes it for people;
// content is what makes two writes the same.
func (s *Server) floodWrote(ref string, sess caller.Session, known bool, what, content string) {
	id, ok := s.Store.Resolve(ref)
	if !ok {
		return
	}
	sum := sha256.Sum256([]byte(content))
	until, repeats, tripped := s.flood.record(floodSource(id, sess, known), hex.EncodeToString(sum[:8]), s.Now())
	if !tripped {
		return
	}
	reason := fmt.Sprintf("同じ書き込み（%s）が、30分のあいだに%d回繰り返されました。", what, repeats)
	if repeats < floodRepeats {
		reason = fmt.Sprintf("30分のあいだに、書き込みが%d回に達しました。", floodWrites)
	}
	why := fmt.Sprintf("%s%s からこのタスクへの書き込みを、%s まで止めます。スクリプトや定期実行の設定（launchctl など）を確かめてください。",
		reason, actorFor("", sess, known), until.In(s.Store.Location()).Format("15:04"))
	if _, _, err := s.Store.Stop(id, why); err == nil && s.Changed != nil {
		s.Changed()
	}
}
