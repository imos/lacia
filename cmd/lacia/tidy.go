package main

import (
	"path"
	"path/filepath"
	"time"

	"github.com/imos/lacia/internal/daylog"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/self"
)

// tidySource marks the notice that hands candidates for memory/ to the
// conversation.
const tidySource = "夜の整理"

// wireTidy turns on the nightly tidying of memory/: the day's topic and the
// candidates go into that day's log (kept under logPath in the repository),
// and the candidates reach the conversation as a notice. The notice has no
// destination, so nothing of it is posted to Discord. Once the conversation
// has answered it, the candidates are hers; an answer that failed, or whose
// outcome is unknown, leaves them to be sent again the next night.
func wireTidy(keeper *self.Keeper, queue *request.Queue, log *daylog.Log, logPath string) {
	if log != nil {
		keeper.TidyLog = func(day time.Time, text string) (string, error) {
			day = day.In(keeper.Zone())
			_, err := log.Add(daylog.Entry{Key: "tidy:" + day.Format("2006-01-02"), At: day, Who: "夜の整理", Place: "その日の話題と、覚えておく候補", Text: text,
				Remark: "サーバーが夜の整理で書き足しました。その日の記録の末尾と分かるように、見出しの時刻を23:59にしています。"})
			return path.Join(filepath.ToSlash(logPath), day.Format("2006-01"), day.Format("2006-01-02")+".md"), err
		}
	}
	keeper.TidyTell = func(notice, text string) error {
		_, _, err := queue.Add(request.Request{ID: notice, Kind: request.System, Source: tidySource, Text: text})
		return err
	}
	finished := queue.Finished
	queue.Finished = func(r request.Request) {
		if finished != nil {
			finished(r)
		}
		if r.Source == tidySource && r.Status == request.Done {
			keeper.TidyDone(r.ID)
		}
	}
}
