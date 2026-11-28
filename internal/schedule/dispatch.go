package schedule

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
	"github.com/imos/lacia/internal/routine"
)

const (
	WaitingConnection = "waiting_connection"
	QueuedLocal       = "queued"
	WaitingAuth       = "waiting_auth"
	Executing         = "running"
	Completed         = "completed"
	Unknown           = "unknown"
	Skipped           = "skipped"
)

func privateSecret(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("秘密を保存するファイルには、所有者以外に権限を与えていない通常のファイルを指定してください（例: 権限 0600）")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) < 32 {
		return nil, errors.New("秘密ファイルの中身は、前後の空白を除いて32バイト以上必要です")
	}
	return data, nil
}
func (c Routing) Authorized(id, secret string) bool {
	secretFile, ok := c.devices[id]
	if !ok {
		return false
	}
	want, err := privateSecret(secretFile)
	return err == nil && subtle.ConstantTimeCompare(want, []byte(secret)) == 1
}

type Execution struct {
	Claim   string          `json:"claim,omitempty"`
	ID      string          `json:"id"`
	Job     Job             `json:"job"`
	To      string          `json:"to,omitempty"`
	Kind    string          `json:"kind,omitempty"`
	Target  string          `json:"target,omitempty"` // historical executions
	Device  string          `json:"device"`
	Prompt  string          `json:"prompt"`
	Status  string          `json:"status"`
	Queued  time.Time       `json:"queued"`
	Started time.Time       `json:"started,omitzero"`
	Result  Result          `json:"result"`
	Report  *routine.Result `json:"report,omitempty"`

	UnverifiedHandoffs []string `json:"unverified_handoffs,omitempty"`
}

func (e Execution) DestinationLabel() string {
	if e.To != "" {
		return e.To
	}
	return e.Target
}

func (e Execution) StatusLabel() string {
	switch e.Status {
	case "missed":
		return "実行されませんでした"
	case Skipped:
		return "停止中のため見送りました"
	case QueuedLocal:
		return "順番を待っています"
	case "waiting_profile":
		return "実行プロファイルに対応した端末を待っています"
	case WaitingConnection:
		return "接続を待っています"
	case WaitingAuth:
		return "認証を待っています"
	case Executing:
		return "実行中です"
	case Unknown:
		return "結果を確かめられません"
	case Completed:
		return "終わりました"
	}
	return e.Status
}

type DeviceStatus struct {
	ExecutionProfiles bool      `json:"execution_profiles,omitempty"`
	Status            string    `json:"status"`
	At                time.Time `json:"at"`
}
type DeviceEvent struct {
	Device string `json:"device"`
	DeviceStatus
}

type dispatchState struct {
	Events     []DeviceEvent           `json:"events,omitempty"`
	Executions map[string]Execution    `json:"executions"`
	Devices    map[string]DeviceStatus `json:"devices"`
}
type Dispatch struct {
	State           StateFile
	Routing         Routing
	Active          func(Job) bool
	HandoffTaskOpen func(string) bool // checks that the exact destination is open; the reporter has already read back the saved finding
	Paused          func() bool       // blocks state writes while an offline migration owns the files
	StartsPaused    func() bool       // blocks new claims while a server restart is pending
	mu              sync.Mutex
	data            dispatchState
}

func OpenDispatch(state StateFile, cfg Routing) (*Dispatch, error) {
	d := &Dispatch{State: state, Routing: cfg, data: dispatchState{Executions: map[string]Execution{}, Devices: map[string]DeviceStatus{}}}
	raw, err := state.read()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &d.data); err != nil {
			return nil, err
		}
	}
	if d.data.Executions == nil || d.data.Devices == nil {
		return nil, errors.New("予約の実行状態が壊れています")
	}
	return d, nil
}

// StateFile is a file of the scheduler's state: Name inside Dir.
type StateFile struct {
	Dir  *disk.Dir
	Name string
}

func (f StateFile) read() ([]byte, error) { return f.Dir.ReadFile(f.Name) }

func (f StateFile) writeJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return f.Dir.WriteFile(f.Name, append(data, '\n'), 0o600)
}

// update rolls memory back if persistence fails. No delivery precedes a durable write.
var ErrDispatchPaused = errors.New("予約の状態の更新を止めています。あとで再試行してください")

func (d *Dispatch) update(fn func()) error {
	if d.Paused != nil && d.Paused() {
		return ErrDispatchPaused
	}
	old, _ := json.Marshal(d.data)
	fn()
	if err := d.State.writeJSON(d.data); err != nil {
		var restored dispatchState
		json.Unmarshal(old, &restored)
		d.data = restored
		return err
	}
	return nil
}

var ErrDispatchBusy = errors.New("前の回の終了を待っています")

func (d *Dispatch) Enqueue(id string, job Job, prompt string, now time.Time) error {
	job.CheckScopes = append([]string(nil), job.CheckScopes...)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.data.Executions[id]; ok {
		return nil
	}
	to, dest, err := d.Routing.Route(job.To)
	if err != nil {
		return err
	}

	for _, e := range d.data.Executions {
		if e.Job.OccurrenceName() == job.OccurrenceName() && !e.Result.Settled && e.Status != Completed && e.Status != Unknown {
			return ErrDispatchBusy
		}
	}
	return d.update(func() {
		status := WaitingConnection
		if dest == "local" {
			status = QueuedLocal
		}
		d.data.Executions[id] = Execution{ID: id, Job: job, To: to.String(), Kind: to.Kind, Device: dest, Prompt: prompt, Status: status, Queued: now}
	})
}

// Devices returns what each registered device last said about itself.
func (d *Dispatch) Devices() map[string]DeviceStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]DeviceStatus, len(d.data.Devices))
	for name, status := range d.data.Devices {
		out[name] = status
	}
	return out
}

func (d *Dispatch) List(name string) []Execution {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Execution
	for _, e := range d.data.Executions {
		if name != "" && e.Job.Name != name && e.Job.OccurrenceName() != name {
			continue
		}
		if e.Status == WaitingConnection || e.Status == WaitingAuth {
			s := d.data.Devices[e.Device]
			e.Status = WaitingConnection
			if s.Status == WaitingAuth {
				e.Status = WaitingAuth
			}
			if e.Job.Profile != "" && !s.ExecutionProfiles {
				e.Status = "waiting_profile"
			}
		}
		e.Claim = ""
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Queued.Before(out[j].Queued) })
	return out
}
func (d *Dispatch) Poll(device string, status DeviceStatus) ([]Execution, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if status.Status != "ready" && status.Status != WaitingAuth && status.Status != WaitingConnection {
		return nil, errors.New("端末の状態が正しくありません")
	}
	if status.At.IsZero() || status.At.After(time.Now().Add(time.Minute)) {
		return nil, errors.New("端末の確認時刻が正しくありません")
	}
	err := d.update(func() {
		for id, e := range d.data.Executions {
			if e.Device == device {
				d.skipIfStopped(id, e, status.At)
			}
		}
		if status.At.After(d.data.Devices[device].At) {
			if d.data.Devices[device].Status != status.Status {
				d.data.Events = append(d.data.Events, DeviceEvent{Device: device, DeviceStatus: status})
			}
			d.data.Devices[device] = status
		}
	})
	if err != nil {
		return nil, err
	}
	var out []Execution
	if status.Status == "ready" {
		for _, e := range d.data.Executions {
			if e.Device == device && !e.Result.Settled && (e.Job.Profile == "" || status.ExecutionProfiles) {
				e.Claim = ""
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Queued.Before(out[j].Queued) })
	return out, nil
}

// RunningRemotely reports whether any remote device still owns a running execution.
// The local queue tracks its own running work.
func (d *Dispatch) RunningRemotely() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.data.Executions {
		if e.Device != LocalDevice && e.Status == Executing {
			return true
		}
	}
	return false
}

func (d *Dispatch) Start(device, id, claim string, now time.Time) error {
	if len(claim) < 32 {
		return errors.New("この回の実行を引き受けるためのIDには、32バイト以上の値を指定してください")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Paused != nil && d.Paused() {
		return ErrDispatchPaused
	}
	e, ok := d.data.Executions[id]
	if !ok || e.Device != device {
		return errors.New("この端末に割り当てられた予約に、指定の回が見つかりません")
	}
	if d.stoppedBeforeStart(e) {
		if err := d.update(func() { d.skipIfStopped(id, e, now) }); err != nil {
			return err
		}
		return errors.New("定期タスクが止まっているため、この回を見送りました")
	}
	if e.Status == Executing {
		if e.Claim != claim {
			return errors.New("別の処理が、この回の実行を先に始めています")
		}
		return nil
	}
	if e.Status != WaitingConnection && e.Status != WaitingAuth {
		return errors.New("この回は開始できません")
	}
	if d.StartsPaused != nil && d.StartsPaused() {
		return ErrDispatchPaused
	}
	return d.update(func() {
		e.Claim = claim
		e.Status = Executing
		e.Started = now
		d.data.Executions[id] = e
	})
}
func (d *Dispatch) Finish(device, id, claim string, res Result, unknown bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[id]
	if !ok || e.Device != device {
		return errors.New("この端末に割り当てられた予約に、指定の回が見つかりません")
	}
	if claim == "" || e.Claim != claim {
		return errors.New("予約の実行を引き受けたときのIDと一致しません")
	}
	if e.Status == Completed || e.Status == Unknown {
		return nil
	}
	if e.Status != Executing || !res.Settled || res.Finished.IsZero() {
		return errors.New("予約の終了を記録するには、実行中の回に結果と終了時刻を指定してください")
	}
	return d.update(func() {
		e.Status = Completed
		if unknown {
			e.Status = Unknown
			res.Failed = true
		}
		res.Started = e.Started
		e.Result = res
		d.data.Executions[id] = e
	})
}
func (d *Dispatch) Result(id string) (Result, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[id]
	if !ok {
		return Result{}, false
	}
	return e.Result, true
}

func (d *Dispatch) Events(device string) []DeviceEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []DeviceEvent
	for _, e := range d.data.Events {
		if device == "" || e.Device == device {
			out = append(out, e)
		}
	}
	return out
}

func (d *Dispatch) Get(id string) (Execution, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[id]
	e.Job.CheckScopes = append([]string(nil), e.Job.CheckScopes...)
	return e, ok
}

// SyncLocal mirrors the trusted local queue without using worker delivery.
func (d *Dispatch) SyncLocal(id, status string, res Result) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[id]
	if !ok || e.Device != "local" {
		return errors.New("このサーバーで動かす予約に、指定の回が見つかりません")
	}
	if e.Status == Skipped {
		return nil
	}
	if res.Profile == "" {
		res.Profile = e.Result.Profile
	}
	if res.Login == "" {
		res.Login = e.Result.Login
	}
	if res.Device == "" {
		res.Device = e.Result.Device
	}
	next := QueuedLocal
	if status == "running" {
		next = Executing
	}
	if res.Settled {
		next = Completed
	}
	if status == "unknown" {
		next = Unknown
	}
	if e.Status == next && e.Result == res {
		return nil
	}
	return d.update(func() { e.Status = next; e.Result = res; e.Started = res.Started; d.data.Executions[id] = e })
}

func (d *Dispatch) Skip(id string, job Job, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.data.Executions[id]; ok {
		return nil
	}
	to, dest, err := d.Routing.Route(job.To)
	if err != nil {
		return err
	}
	return d.update(func() {
		d.data.Executions[id] = Execution{ID: id, Job: job, To: to.String(), Kind: to.Kind, Device: dest, Queued: now, Status: "missed", Result: Result{Settled: true, Failed: true, Finished: now, Error: "前の回が終わっていなかったので、今回は見送りました。"}}
	})
}

// Report saves the structured result before the producer may advance its own ledger.
func (d *Dispatch) Report(routineID, runID string, report routine.Result) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[runID]
	if !ok || (e.Job.ID != routineID && !(e.Job.ID == "" && e.Job.Name == routineID)) {
		return ErrRoutineReport
	}
	if err := e.Job.NormalizeCoverage(&report); err != nil {
		return ErrRoutineReport
	}
	if err := report.RequireScopes(e.Job.CheckScopes); err != nil {
		return ErrRoutineReport
	}
	if e.Job.CheckResult && report.CheckStatus == "" {
		return ErrRoutineReport
	}
	// Empty lists are omitted on disk. Keep retry comparisons stable after restart.
	if len(report.Checks) == 0 {
		report.Checks = nil
	}
	if len(report.Unverified) == 0 {
		report.Unverified = nil
	}
	if len(report.Matters) == 0 {
		report.Matters = nil
	}
	if len(report.Handoffs) == 0 {
		report.Handoffs = nil
	}
	report.RunID = runID
	if e.Report != nil {
		if reflect.DeepEqual(*e.Report, report) {
			return nil
		}
		return ErrRoutineReport
	}
	if e.Status != Executing && !(e.Device == LocalDevice && e.Status == QueuedLocal) {
		return ErrRoutineReport
	}
	checkedTasks := map[string]bool{}
	for _, handoff := range report.Handoffs {
		if checkedTasks[handoff.TaskID] {
			continue
		}
		checkedTasks[handoff.TaskID] = true
		if d.HandoffTaskOpen == nil || !d.HandoffTaskOpen(handoff.TaskID) {
			e.UnverifiedHandoffs = append(e.UnverifiedHandoffs, handoff.TaskID)
		}
	}
	return d.update(func() {
		e.Report = &report
		d.data.Executions[runID] = e
	})
}

func (d *Dispatch) stoppedBeforeStart(e Execution) bool {
	return (e.Status == WaitingConnection || e.Status == WaitingAuth) &&
		d.Active != nil && !d.Active(e.Job)
}

func (d *Dispatch) skipIfStopped(id string, e Execution, now time.Time) {
	if d.stoppedBeforeStart(e) {
		e.Status = Skipped
		e.Result = Result{Settled: true, Finished: now, Error: "定期タスクが止まっているため、この回を見送りました。"}
		d.data.Executions[id] = e
	}
}

// SkipStopped settles opted-out device runs even while the device is offline.
func (d *Dispatch) SkipStopped(now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.data.Executions {
		if d.stoppedBeforeStart(e) {
			return d.update(func() {
				for id, e := range d.data.Executions {
					d.skipIfStopped(id, e, now)
				}
			})
		}
	}
	return nil
}

// SkipLocalIfStopped runs before the local conversation starts.
func (d *Dispatch) SkipLocalIfStopped(id string, now time.Time) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[id]
	if !ok || e.Device != LocalDevice {
		return false, nil
	}
	if e.Status == Skipped {
		return true, nil
	}
	if e.Status != QueuedLocal || d.Active == nil || d.Active(e.Job) {
		return false, nil
	}
	err := d.update(func() {
		e.Status = Skipped
		e.Result = Result{Settled: true, Finished: now, Error: "定期タスクが止まっているため、この回を見送りました。"}
		d.data.Executions[id] = e
	})
	return err == nil, err
}

// SelectLocal records the local machine's selection before starting its process.
func (d *Dispatch) SelectLocal(id, device, profile, login string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.data.Executions[id]
	if !ok || e.Device != LocalDevice {
		return errors.New("このサーバーの実行回が見つかりません")
	}
	return d.update(func() {
		e.Result.Device, e.Result.Profile, e.Result.Login = device, profile, login
		d.data.Executions[id] = e
	})
}
