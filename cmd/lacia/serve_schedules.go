package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/imos/lacia/internal/login"
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
	"github.com/imos/lacia/internal/task"
)

// serveSchedules runs the scheduled jobs of the records, on this server or on
// another device, and keeps what each run did.
type serveSchedules struct {
	*serveRun
	dispatch *schedule.Dispatch
	reviews  *routine.Store
	actions  routineActionRuntime
	runs     *schedule.RunLog
	runner   *schedule.Runner
}

// startSchedules starts answering the requests of the queue and running the
// scheduled jobs.
func (s *serveRun) startSchedules(agents requestRunners) error {
	dispatch, err := s.openDispatch()
	if err != nil {
		return err
	}
	server, store := s.server, s.store
	reviews := routine.NewStore(s.dir)
	server.Routines = reviews
	sc := &serveSchedules{serveRun: s, dispatch: dispatch, reviews: reviews,
		actions: routineActionRuntime{state: s.state, tasks: store, receipts: reviews, queue: s.queue}}
	server.RoutineActionsEnabled = sc.actions.enabled
	sc.runs = sc.newRunLog()
	sc.runner = &schedule.Runner{Root: s.dir, Location: store.Location(), State: schedule.StateFile{Dir: s.state, Name: "schedules.json"}, Check: s.routing.Check, TaskClosed: func(id string) bool {
		t, err := store.Get(id)
		return err == nil && t.Status.Closed()
	}, Submit: sc.submit}
	server.Schedules = sc.list
	server.Runs = sc.runs
	jobs, _ := server.Schedules()
	if !sc.actions.enabled() {
		if err := syncExistingRoutineProblems(reviews, jobs); err != nil {
			slog.Error("routine failure receipt", "detail", "定期タスクの実行履歴に残っていた問題を、受付に記録できませんでした。", "error", err)
		}
	}
	deferNewWork := func() bool { return s.draining() || s.restart.pending() }
	go s.queue.Work(s.ctx, false, sc.unlessStopped(agents.main), deferNewWork)
	go s.queue.Work(s.ctx, true, sc.unlessStopped(agents.side), deferNewWork)
	go sc.tick()
	return nil
}

func (s *serveRun) openDispatch() (*schedule.Dispatch, error) {
	dir, store, queue := s.dir, s.store, s.queue
	dispatch, err := schedule.OpenDispatch(schedule.StateFile{Dir: s.state, Name: "schedule-dispatch.json"}, s.routing)
	if err != nil {
		return nil, err
	}
	dispatch.Paused = s.draining
	dispatch.HandoffTaskOpen = func(id string) bool {
		t, err := store.Get(id)
		return err == nil && t.ID == id && !t.Status.Closed()
	}
	dispatch.StartsPaused = s.restart.pending
	s.restartBusy = func() bool { return s.draining() || queue.Working() || dispatch.RunningRemotely() }
	dispatch.Active = func(current schedule.Job) bool {
		for _, job := range schedule.LoadIn(dir, store.Location()) {
			if job.ID != "" && job.OccurrenceName() == current.OccurrenceName() && job.Err == "" {
				return job.Enabled
			}
		}
		return true
	}
	s.server.ScheduleDispatch = dispatch
	return dispatch, nil
}

func (sc *serveSchedules) newRunLog() *schedule.RunLog {
	dir, store, routineActions, reviews := sc.dir, sc.store, sc.actions, sc.reviews
	return &schedule.RunLog{Root: dir, Location: store.Location(), State: schedule.StateFile{Dir: sc.state, Name: "schedule-runs.json"}, Execution: sc.dispatch.Get,
		ResolveName: func(name string) string {
			for _, job := range schedule.LoadIn(dir, store.Location()) {
				if job.LegacyName == name && job.ID != "" {
					return job.ID
				}
			}
			return name
		},
		KeepSuccess: func(name string, at time.Time) bool {
			if routineActions.enabled() && task.ValidRoutineID(name) {
				return true
			}
			for _, t := range store.List() {
				if t.Status.Closed() || t.AfterRun == "" {
					continue
				}
				since, err := time.Parse(time.RFC3339Nano, t.AfterRunSince)
				if err != nil || !at.After(since) {
					continue
				}
				if t.AfterRun == name {
					return true
				}
				if job, ok := schedule.Resolve(dir, t.AfterRun); ok && job.Name == name {
					return true
				}
			}
			return false
		},
		Review: func(id string, _ schedule.Run, report routine.Result) error {
			if routineActions.enabled() {
				return nil
			}
			_, _, err := reviews.Accept(id, report)
			return err
		},
		Observe: routineActions.observe,
	}
}

// submit starts one occurrence of a scheduled job, here or on the device it
// is routed to.
func (sc *serveSchedules) submit(id string, job schedule.Job) error {
	dir, store, queue, dispatch, runs, routineActions := sc.dir, sc.store, sc.queue, sc.dispatch, sc.runs, sc.actions
	nowPath := *sc.c.str("now")
	job.ExplicitMatters = *sc.c.on("routine-explicit-matters") && job.ID != ""

	label := job.Title
	if label == "" {
		label = job.Name
	}
	// Once the server reads the calendars itself, the Codex job that
	// used to produce the same file must not overwrite it.
	if job.OccurrenceName() == "calendar" && sc.calendarIsDirect() {
		return nil
	}
	if restored, err := restoreScheduledExecution(queue, dispatch, runs, id, time.Now()); restored || err != nil {
		return err
	}
	to, destination, err := sc.routing.Route(job.To)
	if err != nil {
		return err
	}
	if destination != schedule.LocalDevice {
		prompt := job.Prompt + job.Late + schedule.SituationIn(dir, nowPath, time.Now(), store.Location()) + job.TaskInstruction(dir, routineActions.collects(job)) + job.ReportInstruction(id, routineActions.collects(job)) + routineActions.instruction(job)
		if err := dispatch.Enqueue(id, job, prompt, time.Now()); err != nil {
			if errors.Is(err, schedule.ErrDispatchBusy) {
				if err := dispatch.Skip(id, job, time.Now()); err != nil {
					return err
				}
				return runs.Skip(job, time.Now())
			}
			return err
		}
		return runs.Begin(job, id, time.Now())
	}
	if _, exists := queue.Get(id); exists {
		return runs.Begin(job, id, time.Now())
	}
	// A run that outlasts its interval must not pile up a backlog:
	// skip this occurrence while the previous one is unfinished.
	if queue.Busy("sched-" + job.OccurrenceName() + "-") {
		if err := dispatch.Skip(id, job, time.Now()); err != nil {
			return err
		}
		slog.Warn("schedule skipped; previous run unfinished", "detail", "前の回がまだ終わっていないため、今回の実行は見送りました。", "job", job.Name, "id", id)
		if err := runs.Skip(job, time.Now()); err != nil {
			slog.Error("schedule run log", "detail", "定期タスクの実行履歴を記録できませんでした。", "job", job.Name, "error", err)
		}
		return nil
	}
	prompt := job.Prompt + job.Late + schedule.SituationIn(dir, nowPath, time.Now(), store.Location()) + job.TaskInstruction(dir, routineActions.collects(job)) + job.ReportInstruction(id, routineActions.collects(job)) + routineActions.instruction(job)
	if err := dispatch.Enqueue(id, job, prompt, time.Now()); err != nil {
		return err
	}
	if _, _, err := queue.Add(request.Request{ID: id, Kind: request.System, Source: "予約「" + label + "」", Side: job.Side, Agent: to.Kind, ReplyTo: job.ReplyTo, Text: prompt}); err != nil {
		return err
	}
	// The log is a record of what ran; failing to write it must
	// not stop the job itself.
	if err := runs.Begin(job, id, time.Now()); err != nil {
		slog.Error("schedule run log", "detail", "定期タスクの実行履歴を記録できませんでした。", "job", job.Name, "error", err)
	}
	return nil
}

// settle writes what the runs that finished did.
func (sc *serveSchedules) settle(now time.Time) {
	dispatch, queue, routineActions := sc.dispatch, sc.queue, sc.actions
	if err := dispatch.SkipStopped(now); err != nil {
		slog.Error("schedule stop", "detail", "停止した予約の状況を実行記録に反映できませんでした。", "error", err)
	}
	sc.syncLocalResults()
	sc.importRemoteResults()
	err := sc.runs.Settle(func(id string) (schedule.Result, bool) {
		if res, ok := dispatch.Result(id); ok {
			return res, true
		}
		r, ok := queue.Get(id)
		return schedule.Result{
			Settled: r.Status != request.Queued && r.Status != request.Running,
			Failed:  r.Status != request.Done,
			Reply:   r.Reply, Error: r.Error, Started: r.Started, Finished: r.Finished,
		}, ok
	}, now)
	if err != nil {
		slog.Error("schedule run log", "detail", "定期タスクの実行履歴を記録できませんでした。", "error", err)
	}
	jobs := sc.routing.Check(schedule.LoadIn(sc.dir, sc.store.Location()))
	if err := routineActions.applyBaseline(); err != nil {
		slog.Error("routine action baseline", "detail", "定期タスクで対応する用件の移行を終えられませんでした。", "error", err)
	}
	if err := routineActions.syncTroubles(jobs, dispatch.Devices(), now); err != nil {
		slog.Error("routine action health", "detail", "定期タスクの実行を妨げる問題を、対応するタスクの記録に反映できませんでした。", "error", err)
	}
	if !routineActions.enabled() {
		if err := syncRoutineDeliveries(sc.reviews, jobs, queue.Get); err != nil {
			slog.Error("routine delivery receipt", "detail", "定期タスクの用件が届いたかどうかを、受付に記録できませんでした。", "error", err)
		}
	}
}

func (sc *serveSchedules) syncLocalResults() {
	for _, execution := range sc.dispatch.List("") {
		if execution.Device != schedule.LocalDevice {
			continue
		}
		r, ok := sc.queue.Get(execution.ID)
		if !ok {
			continue
		}
		if err := sc.dispatch.SyncLocal(execution.ID, r.Status, schedule.Result{Settled: r.Status != request.Queued && r.Status != request.Running, Failed: r.Status != request.Done, Reply: r.Reply, Error: r.Error, Started: r.Started, Finished: r.Finished}); err != nil {
			slog.Error("schedule local result", "detail", "このサーバーで動く定期タスクの実行状況を記録できませんでした。", "id", execution.ID, "error", err)
		}
	}
}

// importRemoteResults imports the durable outbox of the other devices. It
// runs before the shared run log is settled; repetition also recovers a
// crash between saving and delivery.
func (sc *serveSchedules) importRemoteResults() {
	for _, execution := range sc.dispatch.List("") {
		if execution.Device == schedule.LocalDevice || execution.Status == "missed" || execution.Status == schedule.Skipped || !execution.Result.Settled {
			continue
		}
		status := request.Done
		if execution.Result.Failed {
			status = request.Failed
		}
		if execution.Status == schedule.Unknown {
			status = request.Unknown
		}
		label := execution.Job.Title
		if label == "" {
			label = execution.Job.Name
		}
		if err := sc.queue.RecordExternal(request.Request{ID: execution.ID, Kind: request.System, Source: "予約「" + label + "」", Side: execution.Job.Side, Agent: execution.Kind, ReplyTo: execution.Job.ReplyTo, Text: execution.Prompt, Status: status, Reply: execution.Result.Reply, Error: execution.Result.Error, Created: execution.Queued, Started: execution.Started, Finished: execution.Result.Finished}); err != nil {
			slog.Error("schedule result import", "detail", "別の端末で実行した定期タスクの結果を取り込めませんでした。", "id", execution.ID, "error", err)
		}
	}
}

// list returns the scheduled jobs and when each last ran.
func (sc *serveSchedules) list() ([]schedule.Job, map[string]string) {
	jobs, last := sc.routing.Check(schedule.LoadIn(sc.dir, sc.store.Location())), sc.runner.Last()
	for _, job := range jobs {
		last[job.Name] = max(last[job.Name], last[job.OccurrenceName()])
	}
	return jobs, last
}

// unlessStopped skips a scheduled run that was stopped, and runs one that
// names a profile in a conversation of that profile.
func (sc *serveSchedules) unlessStopped(run request.Runner) request.Runner {
	dispatch := sc.dispatch
	return func(ctx context.Context, text string) (string, string, error) {
		skipped, err := dispatch.SkipLocalIfStopped(request.Current(ctx).ID, time.Now())
		if err != nil {
			return "", "", err
		}
		if skipped {
			return "報告はありません。", "", nil
		}
		if e, ok := dispatch.Get(request.Current(ctx).ID); ok && e.Device == schedule.LocalDevice {
			device, _ := os.Hostname()
			if e.Job.Profile != "" {
				list, err := login.LoadList(login.ListPath(os.Getenv))
				if err != nil {
					return "", "", err
				}
				conversations := scheduleConversations{root: sc.dir, state: sc.state, base: scheduleServerURL(*sc.c.str("addr")), place: *sc.c.str("profile"), logins: list}
				profile, loginName, err := conversations.selection(e)
				if err != nil {
					return "", "", err
				}
				if err := dispatch.SelectLocal(e.ID, device, profile, loginName); err != nil {
					return "", "", err
				}
				reply, err := conversations.run(ctx, e, text)
				return reply, "", err
			}
			if err := dispatch.SelectLocal(e.ID, device, "default", ""); err != nil {
				return "", "", err
			}
		}
		return run(ctx, text)
	}
}

func (sc *serveSchedules) tick() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sc.ctx.Done():
			return
		case now := <-ticker.C:
			if sc.draining() {
				continue
			}
			// Write the runs that finished before queuing new ones.
			sc.settle(now)
			if sc.restart.pending() {
				continue
			}
			if err := sc.runner.Tick(now); err != nil {
				slog.Error("schedule", "detail", "定期タスクを予定どおりに実行する処理で問題が起きました。", "error", err)
			}
		}
	}
}
