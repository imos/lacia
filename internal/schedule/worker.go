package schedule

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/imos/lacia/internal/request"
	"golang.org/x/sys/unix"
)

// WorkerConfig belongs to the device, outside the records repository.
// AuthCheck is read-only; exit 77 means that human authentication is needed.
type WorkerConfig struct {
	URL        string   `json:"url"`
	Device     string   `json:"device"`
	SecretFile string   `json:"secret_file"`
	Root       string   `json:"root"`
	AuthCheck  []string `json:"auth_check,omitempty"`
}
type workerRun struct {
	Claim     string    `json:"claim"`
	Execution Execution `json:"execution"`
	Phase     string    `json:"phase"`
	Result    Result    `json:"result"`
	Unknown   bool      `json:"unknown,omitempty"`
}
type workerState struct {
	Runs   map[string]workerRun `json:"runs"`
	Events []DeviceStatus       `json:"events,omitempty"`
	Status string               `json:"status"`
}
type Worker struct {
	Config WorkerConfig
	State  string
	Client *http.Client
	// Available guards the local forwarding port while it is down.
	Available func() bool
	// RunConversation receives the stored routing and the prepared request.
	SelectProfile   func(Execution) (profile, login string, err error)
	RunConversation func(context.Context, Execution, string) (string, error)
	data            workerState
}

var errWorkerState = errors.New("端末の実行記録を保存できませんでした")

func (w *Worker) save() error {
	if err := atomicJSON(w.State, w.data); err != nil {
		return fmt.Errorf("%w: %v", errWorkerState, err)
	}
	return nil
}
func (w *Worker) event(status string) error {
	if w.data.Status == status {
		return nil
	}
	w.data.Status = status
	w.data.Events = append(w.data.Events, DeviceStatus{Status: status, At: time.Now(), ExecutionProfiles: w.SelectProfile != nil})
	return w.save()
}

// Credential reads the mode-checked secret shared with the handover receiver.
func (w *Worker) Credential() (string, error) {
	secret, err := privateSecret(w.Config.SecretFile)
	if err != nil {
		return "", errors.New("端末の秘密ファイルを読めません")
	}
	return string(secret), nil
}

func (w *Worker) call(ctx context.Context, path string, in, out any) error {
	if w.Available != nil && !w.Available() {
		return errors.New("予約を管理する側への接続を待っています")
	}
	secret, err := w.Credential()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(w.Config.URL, "/")+"/api/schedule-worker/"+w.Config.Device+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errWorkerAuth
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("予約を管理するサーバーが HTTP %d を返しました", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return nil
}

var errWorkerAuth = errors.New("端末の認証を待っています")

func (w *Worker) Validate() error {
	u, err := url.Parse(w.Config.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("予約を管理するサーバーの URL が正しくありません")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
		return errors.New("端末の接続にはHTTPSか手元の転送ポートを使ってください")
	}
	if !namePattern.MatchString(w.Config.Device) || w.Config.Device == "local" || w.Config.Root == "" || w.Config.SecretFile == "" {
		return errors.New("端末のID、秘密ファイルと記録のディレクトリーを指定してください")
	}
	if w.RunConversation == nil {
		return errors.New("予約を動かす会話を指定してください")
	}
	return nil
}

// Run holds an OS lock for the lifetime of the worker. Each execution's
// started marker is durable before spawning, and is never replayed after
// restart. Completed results remain in the outbox until acknowledged.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.State), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(w.State+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("この端末では、予約を受け取る処理がすでに動いています")
	}
	if err = w.load(); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err = w.Tick(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, errWorkerState) {
				return err
			}
			slog.Warn("schedule worker: waiting", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(20 * time.Second):
		}
	}
}
func (w *Worker) load() error {
	w.data = workerState{Runs: map[string]workerRun{}}
	raw, err := os.ReadFile(w.State)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal(raw, &w.data); err != nil {
			return err
		}
		if w.data.Runs == nil {
			return errors.New("端末の実行記録が壊れています")
		}
	}
	for id, r := range w.data.Runs {
		if r.Phase == "started" {
			r.Phase = "finished"
			r.Unknown = true
			r.Result.Settled, r.Result.Failed = true, true
			r.Result.Error = "端末が止まったため、結果が分かりません。この回は自動でやり直さず、結果の確認を待ちます。"
			r.Result.Finished = time.Now()
			w.data.Runs[id] = r
		}
	}
	return w.save()
}
func (w *Worker) Tick(ctx context.Context) error {
	if w.data.Runs == nil {
		if err := w.load(); err != nil {
			return err
		}
	}
	if len(w.Config.AuthCheck) > 0 {
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := exec.CommandContext(probeCtx, w.Config.AuthCheck[0], w.Config.AuthCheck[1:]...).Run()
		cancel()
		if err != nil {
			status := WaitingConnection
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 77 {
				status = WaitingAuth
			}
			if saveErr := w.event(status); saveErr != nil {
				return saveErr
			}
			// The API may still be reachable when a service login is
			// needed. Publish the observation, without claiming work.
			for len(w.data.Events) > 0 {
				if callErr := w.call(ctx, "/poll", w.data.Events[0], nil); callErr != nil {
					return callErr
				}
				w.data.Events = w.data.Events[1:]
				if saveErr := w.save(); saveErr != nil {
					return saveErr
				}
			}
			return nil
		}
	}
	// Replay locally observed outages before publishing readiness. This works
	// even when authentication was a prerequisite to reaching the API itself.
	for len(w.data.Events) > 0 {
		e := w.data.Events[0]
		if err := w.call(ctx, "/poll", e, nil); err != nil {
			return w.connectionError(err)
		}
		w.data.Events = w.data.Events[1:]
		if err := w.save(); err != nil {
			return err
		}
	}
	for id, r := range w.data.Runs {
		if r.Phase == "finished" {
			if err := w.call(ctx, "/"+id+"/finish", FinishRequest{Claim: r.Claim, Result: r.Result, Unknown: r.Unknown}, nil); err != nil {
				return w.connectionError(err)
			}
			r.Phase = "acknowledged"
			w.data.Runs[id] = r
			if err := w.save(); err != nil {
				return err
			}
		}
	}
	var executions []Execution
	if err := w.call(ctx, "/poll", DeviceStatus{Status: "ready", At: time.Now(), ExecutionProfiles: w.SelectProfile != nil}, &executions); err != nil {
		return w.connectionError(err)
	}
	w.data.Status = "ready"
	if err := w.save(); err != nil {
		return err
	}
	for _, e := range executions {
		r, exists := w.data.Runs[e.ID]
		if exists && r.Phase != "prepared" {
			continue
		}
		if !exists {
			if e.Status == Executing {
				continue
			}
			nonce := make([]byte, 32)
			if _, err := rand.Read(nonce); err != nil {
				return err
			}
			r = workerRun{Execution: e, Phase: "prepared", Claim: hex.EncodeToString(nonce)}
			w.data.Runs[e.ID] = r
			if err := w.save(); err != nil {
				return err
			}
		}
		if err := w.call(ctx, "/"+e.ID+"/start", StartRequest{Claim: r.Claim}, nil); err != nil {
			return w.connectionError(err)
		}
		r.Phase = "started"
		w.data.Runs[e.ID] = r
		if err := w.save(); err != nil {
			return err
		}
		started := time.Now()
		profile, loginName := "", ""
		var selectionErr error
		if w.SelectProfile != nil {
			profile, loginName, selectionErr = w.SelectProfile(e)
		} else if e.Job.Profile != "" {
			selectionErr = errors.New("この端末は実行プロファイルに対応していません")
		}
		r.Result = Result{Profile: profile, Login: loginName, Device: w.Config.Device, Started: started}
		w.data.Runs[e.ID] = r
		if err := w.save(); err != nil {
			return err
		}
		var reply string
		err := selectionErr
		if err == nil {
			reply, err = w.execute(ctx, e)
		}
		r.Phase = "finished"
		r.Result = Result{Profile: profile, Login: loginName, Device: w.Config.Device, Settled: true, Failed: err != nil, Reply: reply, Started: started, Finished: time.Now()}
		if err != nil {
			r.Result.Error = "端末の実行コマンドが正常に終わりませんでした。端末の記録を確かめてください。"
		}
		if selectionErr != nil {
			r.Result.Error = selectionErr.Error()
		}
		if ctx.Err() != nil {
			r.Unknown = true
		}
		w.data.Runs[e.ID] = r
		if err := w.save(); err != nil {
			return err
		}
		if err := w.call(ctx, "/"+e.ID+"/finish", FinishRequest{Claim: r.Claim, Result: r.Result, Unknown: r.Unknown}, nil); err != nil {
			return w.connectionError(err)
		}
		r.Phase = "acknowledged"
		w.data.Runs[e.ID] = r
		if err := w.save(); err != nil {
			return err
		}
	}
	return nil
}
func (w *Worker) connectionError(err error) error {
	status := WaitingConnection
	if errors.Is(err, errWorkerAuth) {
		status = WaitingAuth
	}
	if saveErr := w.event(status); saveErr != nil {
		return saveErr
	}
	return err
}

func (w *Worker) execute(ctx context.Context, execution Execution) (string, error) {
	label := execution.Job.Title
	if label == "" {
		label = execution.Job.Name
	}
	prompt := request.Prompt(request.Request{ID: execution.ID, Kind: request.System, Source: "予約「" + label + "」", ReplyTo: execution.Job.ReplyTo, Side: execution.Job.Side, Text: execution.Prompt})
	return w.RunConversation(ctx, execution, prompt)
}

type StartRequest struct {
	Claim string `json:"claim"`
}

type FinishRequest struct {
	Claim   string `json:"claim"`
	Result  Result `json:"result"`
	Unknown bool   `json:"unknown"`
}
