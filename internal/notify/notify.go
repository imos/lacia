// Package notify plans the notifications shown by the macOS app.
// It is the planning half of the notifier: the caller fetches tasks and the
// calendar snapshot, and the Swift app registers whatever the manifest lists.
// Nothing here touches the network.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/imos/lacia/internal/task"
)

// Version is the format version of both the state file and the manifest.
const Version = 1

const (
	KindTask    = "task"
	KindMeeting = "meeting"
)

// Health values written to the manifest.
const (
	HealthOK          = "ok"
	HealthPartial     = "partial"
	HealthUnavailable = "unavailable"
	HealthDisabled    = "disabled"
	HealthFailed      = "failed"
)

// Notice is one notification the Swift app should register. TaskKey and
// Generation are only meaningful for task notices.
type Notice struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	URL        string  `json:"url"`
	Fire       float64 `json:"fire"`    // Unix seconds
	Expires    float64 `json:"expires"` // Unix seconds
	TaskKey    string  `json:"task_key,omitempty"`
	Generation int     `json:"generation,omitempty"`
}

// MarshalJSON always writes task_key and generation for task notices, so a
// generation of 0 survives, and never writes them for meetings.
func (n Notice) MarshalJSON() ([]byte, error) {
	type plain Notice
	if n.Kind != KindTask {
		p := plain(n)
		p.TaskKey, p.Generation = "", 0
		return marshal(p)
	}
	return marshal(struct {
		plain
		TaskKey    string `json:"task_key"`
		Generation int    `json:"generation"`
	}{plain(n), n.TaskKey, n.Generation})
}

// Health is the status block of the manifest.
type Health struct {
	CheckedAt  float64        `json:"checked_at"` // Unix seconds
	Tasks      string         `json:"tasks"`
	Calendar   string         `json:"calendar"`
	Transport  string         `json:"transport"`
	Sync       string         `json:"sync,omitempty"`
	SyncReview []SyncRecovery `json:"sync_review,omitempty"`
}

// SyncRecovery identifies a local backup that may need an operator's review.
type SyncRecovery struct {
	Path  string   `json:"path"`
	Tasks []string `json:"tasks"`
}

// SyncState is the durable state of a single continuous outage and its notices.
type SyncState struct {
	FailureSince time.Time      `json:"failure_since,omitempty"`
	Notice       *Notice        `json:"notice,omitempty"`
	Review       []SyncRecovery `json:"review,omitempty"`
}

// Manifest is the content of manifest.json.
type Manifest struct {
	Version           int            `json:"version"`
	Notices           []Notice       `json:"notices"`
	Health            Health         `json:"health"`
	ConfirmedMeetings []string       `json:"confirmed_meetings"`
	MeetingChecks     map[string]int `json:"meeting_checks"`
}

// TaskEntry remembers whether a task needed action when last seen.
type TaskEntry struct {
	Action     bool `json:"action"`
	Generation int  `json:"generation"`
	// Stopped is the task's stopped_at as last seen. A new value is told
	// about even when the task was waiting on the user already.
	Stopped string `json:"stopped,omitempty"`
	// Closing says that the action was only to confirm finished work.
	Closing bool `json:"closing,omitempty"`
}

// State is what survives between polls. A nil Tasks map means no baseline has
// been taken yet; an empty non-nil map is a baseline with no tasks.
type State struct {
	Version       int                  `json:"version"`
	Tasks         map[string]TaskEntry `json:"tasks"`
	Outbox        []Notice             `json:"outbox"`
	Meetings      []Notice             `json:"meetings"`
	MeetingChecks map[string]int       `json:"meeting_checks,omitempty"`
	Sync          *SyncState           `json:"sync,omitempty"`
}

// NewState returns the state of a first run.
func NewState() *State {
	return &State{Version: Version, Outbox: []Notice{}, Meetings: []Notice{}}
}

// Config is the part of config.json the planner reads.
type Config struct {
	Location             *time.Location // repository zone; nil uses snapshot metadata for meetings
	Enabled              bool
	TaskNotifications    bool
	MeetingNotifications bool
	MeetingSource        string // "linked", "snapshot" or "eventkit"
	// Sources are the calendars that produce meeting notices, as account
	// and calendar ID. It is not read from the file: the server says which
	// calendars it shows. nil means none outside the tests.
	Sources [][2]string
	// ExcludeMeetings is the decoded exclude_meetings value. It is validated
	// while planning, so a broken rule makes the calendar unavailable instead
	// of dropping task notifications. Leave it nil for no rules.
	ExcludeMeetings []any
	// excludeInvalid marks an exclude_meetings value that is not an array.
	excludeInvalid bool
}

// DefaultConfig is the configuration of an empty config.json.
func DefaultConfig() Config {
	return Config{Enabled: true, TaskNotifications: true, MeetingNotifications: true, MeetingSource: "linked"}
}

// ParseConfig reads config.json. Missing keys take their defaults; other keys
// are ignored.
func ParseConfig(raw []byte) (Config, error) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Config{}, err
	}
	if fields == nil {
		return Config{}, errors.New("config_not_object")
	}
	c := DefaultConfig()
	if v, ok := fields["enabled"]; ok {
		c.Enabled = truthy(v)
	}
	if v, ok := fields["task_notifications"]; ok {
		c.TaskNotifications = truthy(v)
	}
	if v, ok := fields["meeting_notifications"]; ok {
		c.MeetingNotifications = truthy(v)
	}
	if v, ok := fields["meeting_source"]; ok {
		c.MeetingSource, _ = v.(string)
	}
	if v, ok := fields["exclude_meetings"]; ok {
		c.ExcludeMeetings, ok = v.([]any)
		c.excludeInvalid = !ok
	}
	return c, nil
}

// Input is everything one poll fetched.
type Input struct {
	Config Config
	// Tasks is the decoded GET /api/tasks response. Set TasksErr when the
	// fetch or the decoding failed; an empty list without an error is taken
	// as "there are no tasks".
	Tasks    []task.Task
	TasksErr error
	// Alerts is the decoded GET /api/schedule-troubles response: things
	// other than tasks that need the user. AlertsOK is false when it could
	// not be fetched (an older server has no such list); what was told
	// about before is then left as it is.
	Alerts         []Alert
	AlertsOK       bool
	RoutineNotices []RoutineNotice
	// Snapshot is the raw calendar snapshot JSON, or SnapshotErr.
	Snapshot    []byte
	SnapshotErr error
	Now         time.Time
	// SiteURL is the public origin, such as "https://lacia.example".
	SiteURL string
	// Transport is the transport health; empty means "ok".
	Transport string
}

// Plan runs one poll: it updates state and returns the manifest to write.
// When a fetch failed, the notices planned earlier are kept.
func Plan(in Input, state *State) Manifest {
	now := seconds(in.Now)
	health := Health{CheckedAt: now, Tasks: HealthDisabled, Calendar: HealthDisabled, Transport: in.Transport}
	if health.Transport == "" {
		health.Transport = HealthOK
	}
	notices := []Notice{}
	confirmedMeetings := []string{}
	state.Outbox = keep(state.Outbox, func(n Notice) bool { return n.Expires > now })
	if in.Config.Enabled {
		if in.Config.TaskNotifications {
			if in.TasksErr != nil {
				health.Tasks = HealthUnavailable
			} else {
				PlanTasks(in.Tasks, state, in.Now, in.SiteURL)
				if in.AlertsOK {
					PlanAlerts(in.Alerts, state, in.Now, in.SiteURL)
				}
				health.Tasks = HealthOK
			}
			planRoutines(in.RoutineNotices, state, in.Now, in.SiteURL)
			notices = append(notices, state.Outbox...)
		}
		if in.Config.MeetingNotifications {
			switch in.Config.MeetingSource {
			case "linked":
				err := in.SnapshotErr
				var planned []Notice
				var complete bool
				confirmed := map[string]bool{}
				if err == nil {
					planned, complete, err = planMeetings(in.Snapshot, in.Config, state, in.Now, in.SiteURL, confirmed)
				}
				if err == nil && health.Transport == HealthOK {
					for _, notice := range planned {
						if confirmed[notice.ID] {
							confirmedMeetings = append(confirmedMeetings, notice.ID)
						}
					}
				}
				switch {
				case err != nil:
					planned = keep(state.Meetings, func(n Notice) bool { return n.Expires > now })
					health.Calendar = HealthUnavailable
				case complete:
					health.Calendar = HealthOK
				default:
					health.Calendar = HealthPartial
				}
				notices = append(notices, planned...)
			case "snapshot":
				// The old /api/calendar route no longer exists.
				health.Calendar = HealthUnavailable
			}
		}
	}
	if state.Meetings == nil {
		state.Meetings = []Notice{}
	}
	return Manifest{Version: Version, Notices: notices, Health: health, ConfirmedMeetings: confirmedMeetings, MeetingChecks: state.MeetingChecks}
}

// seconds converts to Unix seconds the way Python's datetime.timestamp does,
// so values derived from it match the original implementation bit for bit.
func seconds(t time.Time) float64 { return float64(t.UnixMicro()) / 1e6 }

// keep returns the elements that satisfy ok, in order, as a non-nil slice.
func keep(list []Notice, ok func(Notice) bool) []Notice {
	out := make([]Notice, 0, len(list))
	for _, n := range list {
		if ok(n) {
			out = append(out, n)
		}
	}
	return out
}

// truthy follows Python's truth test for decoded JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
