package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Store is the in-memory index of every task under a repository root. The
// files stay the source of truth: every write re-reads task.md from disk
// first, so text that Codex edited in the meantime is kept.
type Store struct {
	root     string
	location *time.Location
	format   int // the shape of the repository; see Config.Format
	areas    []string
	labels   map[string]string // how the pages call an area
	handoff  string            // the area for tasks a peer hands over
	prefix   string            // letter of newly allocated IDs
	noTasks  bool              // the repository keeps no tasks (Config.Tasks)
	now      func() time.Time
	// live reports a condition the server keeps outside task.md, such as a
	// watched job or a handover nobody has taken yet (SetLiveCondition).
	live func(id string) bool

	mu       sync.RWMutex
	tasks    map[string]*Task
	aliases  map[string]string // former long IDs such as work-20261110-05
	problems []Problem
}

// Problem is a task.md that could not be loaded. It is shown to the user
// instead of being dropped silently.
type Problem struct {
	Path string
	Err  string
}

// ErrNotFound is returned when no task has the requested ID.
var ErrNotFound = errors.New("タスクが見つかりません")

// DefaultAreas are the top-level directories that hold tasks in a
// repository that names none.
var DefaultAreas = []string{"work", "personal"}

// Prefix is the letter of newly allocated IDs; FirstNumber is where the
// series starts, above every ID that existed before the redesign.
const (
	Prefix      = "T"
	FirstNumber = 1001
)

// AliasFile maps former IDs to current ones, one "old<TAB>new" per line.
// AliasFile1 is where a repository of format 1 keeps it.
const (
	AliasFile  = "migration/id-map.tsv"
	AliasFile1 = ".lacia/id-map.tsv"
)

// LatestFormat is the newest shape of a record repository this binary
// reads and writes. Format 0 keeps the areas at the top and the history in
// log.md; format 1 keeps the areas under areas/ and the history in
// log.jsonl. Format 2 also puts routines inside their areas.
const LatestFormat = AreaRoutineFormat

// AreaDir is the directory of an area, relative to the repository root.
func AreaDir(format int, area string) string {
	if format >= 1 {
		return "areas/" + area
	}
	return area
}

// Config is the optional .lacia/config.json of a task repository. A
// repository without one holds work and personal tasks numbered T1001 up.
type Config struct {
	// Format is the shape of the repository. A repository that does not
	// say is of format 0, the shape before areas/ and log.jsonl.
	Format int `json:"format"`
	// Timezone is the IANA name of the zone the repository lives in.
	Timezone string   `json:"timezone,omitempty"`
	Areas    []string `json:"areas"`
	IDPrefix string   `json:"id_prefix"`
	// AreaLabels says how the pages and TODO.md call an area, such as
	// {"work": "仕事"}. An area without one is shown by its name.
	AreaLabels map[string]string `json:"area_labels,omitempty"`
	// HandoffArea is the area where a task handed over by the peer profile
	// is created. Without it, that is the first area.
	HandoffArea string `json:"handoff_area,omitempty"`
	// Tasks says whether the repository keeps tasks. Without it, it does.
	// A repository that answers in the chat and keeps what is left for
	// later in a memo, such as one for a family, says false: the rules,
	// the instructions, the pages and the checks then leave the tasks out,
	// and no new task is made or moved (KeepsTasks).
	Tasks *bool `json:"tasks,omitempty"`
	// User is the name Lacia calls the owner of the repository by, such as
	// "田中さん". The rules that travel in the binary are filled in with it.
	User string `json:"user,omitempty"`
	// Reviewers are others who may be asked to look at a task, such as a
	// family member, besides the owner.
	Reviewers []string `json:"reviewers,omitempty"`
	// CalendarHidden lists titles of events the owner does not want shown,
	// as regular expressions: Always on every page, Week in the seven-day
	// list only. See calendar.SetHidden.
	CalendarHidden struct {
		Always []string `json:"always,omitempty"`
		Week   []string `json:"week,omitempty"`
	} `json:"calendar_hidden,omitempty"`
}

// KeepsTasks reports whether the repository keeps tasks: true unless its
// settings say "tasks": false.
func (cfg Config) KeepsTasks() bool {
	return cfg.Tasks == nil || *cfg.Tasks
}

// ErrNoTasks is what a repository that keeps no tasks answers to a request
// to make a task or to move one.
var ErrNoTasks = errors.New("この記録は、タスクを持たない使い方です（.lacia/config.json の tasks が false）。タスクは作れず、状態も変えられません。続きがあることの残し方は、lacia rules の「タスクを持たない記録」にあります")

// DefaultOwner is what Lacia calls the person she works for when nobody
// has told her a name: `lacia init` asks for the real one.
const DefaultOwner = "オーナーさん"

// OwnerName resolves the name for a conversation, including the override
// that a server passes to its child processes.
func (cfg Config) OwnerName() string {
	if name := os.Getenv("LACIA_USER"); name != "" {
		return name
	}
	if cfg.User != "" {
		return cfg.User
	}
	return DefaultOwner
}

// OwnerName is the name Lacia calls the person she works for. It stays
// DefaultOwner until the environment or the repository says otherwise
// (SetOwner).
var OwnerName = DefaultOwner

// reviewers are the people a task waiting for review may name as the one
// to look at it. nil means the owner alone.
var reviewers []string

// SetOwner takes the owner's name from the environment (LACIA_USER, which
// a server passes to its conversations) or from the repository's settings.
// A repository that says neither keeps DefaultOwner.
func SetOwner(root string) {
	cfg := Config{}
	if root != "" {
		cfg, _ = LoadConfig(root)
	}
	OwnerName = cfg.OwnerName()
	reviewers = append([]string{OwnerName}, cfg.Reviewers...)
}

// ConfigFile is where a repository says which areas and ID series it uses.
const ConfigFile = ".lacia/config.json"

// LoadConfig reads the repository's settings, filling in the defaults.
func LoadConfig(root string) (Config, error) {
	cfg := Config{}
	data, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("%s を読めません: %w", ConfigFile, err)
		}
	} else if !os.IsNotExist(err) {
		return cfg, err
	}
	if cfg.Format < 0 || cfg.Format > LatestFormat {
		return cfg, fmt.Errorf("%s の format %d は、この lacia では扱えません。新しい lacia を入れてください", ConfigFile, cfg.Format)
	}
	if cfg.Timezone == "" {
		cfg.Timezone = JST.String()
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil || cfg.Timezone == "Local" {
		return cfg, fmt.Errorf("%s の timezone %q は IANA のタイムゾーン名で指定してください", ConfigFile, cfg.Timezone)
	}
	if len(cfg.Areas) == 0 {
		cfg.Areas = DefaultAreas
	}
	if cfg.IDPrefix == "" {
		cfg.IDPrefix = Prefix
	}
	if len(cfg.IDPrefix) != 1 || cfg.IDPrefix[0] < 'A' || cfg.IDPrefix[0] > 'Z' {
		return cfg, fmt.Errorf("%s の id_prefix は英大文字1つにしてください", ConfigFile)
	}
	return cfg, nil
}

// RequireCurrentFormat refuses the older layouts before anything is written.
// lacia doctor still reads them, to tell what it finds.
func (c Config) RequireCurrentFormat() error {
	if c.Format < LatestFormat {
		return fmt.Errorf("この記録は形式%dです。この lacia は形式%dの記録だけを扱います。形式%dへ移すには、lacia migrate が入っている 2026-11-28 より前の版を使ってください", c.Format, LatestFormat, LatestFormat)
	}
	return nil
}

// Open reads the modern task layout shared by formats 1 and 2. Normal
// runtime entry points require the latest format before opening a store.
// Areas given here override the repository's own settings.
func Open(root string, areas ...string) (*Store, error) {
	cfg, err := LoadConfig(root)
	if err != nil {
		return nil, err
	}
	if cfg.Format == 0 {
		return nil, cfg.RequireCurrentFormat()
	}
	if len(areas) == 0 {
		areas = cfg.Areas
	}
	s := &Store{root: root, location: cfg.Location(), format: cfg.Format, areas: areas, labels: cfg.AreaLabels, handoff: cfg.HandoffArea, prefix: cfg.IDPrefix, noTasks: !cfg.KeepsTasks(), now: time.Now}
	return s, s.Reload()
}

// AreaLabel is how the pages call an area: the label the repository gives
// it, or its name.
func (s *Store) AreaLabel(area string) string {
	if label := s.labels[area]; label != "" {
		return label
	}
	return area
}

// KeepsTasks reports whether the repository keeps tasks (Config.KeepsTasks).
func (s *Store) KeepsTasks() bool { return !s.noTasks }

// HandoffArea is the area where a task handed over by the peer profile is
// created.
func (s *Store) HandoffArea() string {
	if s.handoff != "" || len(s.areas) == 0 {
		return s.handoff
	}
	return s.areas[0]
}

// SetLiveCondition tells the store how to ask for conditions that are not
// in task.md. A task with one may wait without a date or a dependency.
func (s *Store) SetLiveCondition(f func(id string) bool) { s.live = f }

func (s *Store) hasLiveCondition(ref string) bool {
	id, ok := s.Resolve(ref)
	return ok && s.live != nil && s.live(id)
}

// Root returns the repository root.
func (s *Store) Root() string { return s.root }

// Location is the local time zone of this repository.
func (s *Store) Location() *time.Location {
	if s.location == nil {
		return JST
	}
	return s.location
}

// Location resolves the configured zone, or the default for a zero Config.
func (c Config) Location() *time.Location {
	if loc, err := time.LoadLocation(c.Timezone); c.Timezone != "" && err == nil {
		return loc
	}
	return JST
}

// Format returns the shape of the repository (Config.Format).
func (s *Store) Format() int { return s.format }

// AreaDir is the directory of an area, relative to the repository root.
func (s *Store) AreaDir(area string) string { return AreaDir(LatestFormat, area) }

// LogName is the file of a task that holds its history.
func (s *Store) LogName() string { return HistoryFile }

// Areas returns the configured areas.
func (s *Store) Areas() []string { return s.areas }

// Reload rescans the repository. Unreadable tasks are recorded as problems.
func (s *Store) Reload() error {
	tasks := map[string]*Task{}
	var problems []Problem
	for _, area := range s.areas {
		paths, err := filepath.Glob(filepath.Join(s.root, filepath.FromSlash(s.AreaDir(area)), "tasks", "*", "*", "task.md"))
		if err != nil {
			return err
		}
		if s.format >= AreaRoutineFormat {
			routines, err := filepath.Glob(filepath.Join(s.root, filepath.FromSlash(RoutineBase(s.format, area)), "*", "task.md"))
			if err != nil {
				return err
			}
			paths = append(paths, routines...)
		}
		for _, path := range paths {
			rel, _ := filepath.Rel(s.root, path)
			t, err := s.read(area, filepath.ToSlash(filepath.Dir(rel)))
			switch {
			case err != nil:
				problems = append(problems, Problem{rel, err.Error()})
			case tasks[t.ID] != nil:
				problems = append(problems, Problem{rel, fmt.Sprintf("ID %s は %s でも使われています", t.ID, tasks[t.ID].Dir)})
			default:
				tasks[t.ID] = t
			}
		}
	}
	aliases := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(s.root, AliasFile1)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if old, id, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
				aliases[old] = id
			}
		}
	}
	s.mu.Lock()
	s.tasks, s.aliases, s.problems = tasks, aliases, problems
	s.mu.Unlock()
	return nil
}

func (s *Store) read(area, dir string) (*Task, error) {
	path := filepath.Join(s.root, dir, "task.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := ParseDoc(string(data))
	if err != nil {
		return nil, err
	}
	routine := routineTaskDir(dir)
	t, err := parseTask(doc, routine)
	if err != nil {
		return nil, err
	}
	if routine {
		t.Title = routineTitle(s.root, dir, t.ID)
	}
	name := filepath.Base(dir)
	if name != t.ID && !strings.HasPrefix(name, t.ID+"-") {
		return nil, fmt.Errorf("ディレクトリー名 %s が ID %s で始まっていません", name, t.ID)
	}
	if info, err := os.Stat(path); err == nil {
		t.ModTime = info.ModTime()
	}
	t.Area, t.Dir = area, dir
	t.Timezone = s.Location().String()
	return &t, nil
}

// Problems returns the task files that failed to load.
func (s *Store) Problems() []Problem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Problem(nil), s.problems...)
}

// Resolve turns an ID, a former ID, or a lower-case ID into the current ID.
func (s *Store) Resolve(ref string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref = strings.TrimSpace(ref)
	for _, candidate := range []string{ref, strings.ToUpper(ref), s.aliases[ref]} {
		if s.tasks[candidate] != nil {
			return candidate, true
		}
	}
	return "", false
}

// Get returns a copy of the task.
func (s *Store) Get(ref string) (Task, error) {
	id, ok := s.Resolve(ref)
	if !ok {
		if routine := strings.ToUpper(strings.TrimSpace(ref)); ValidRoutineID(routine) {
			if _, _, found := s.routineDirOf(routine); found {
				return Task{}, fmt.Errorf("%w: %s（定期タスク %s には、まだタスクの本文がありません。lacia task write %s task.md <ファイル> で作れます）", ErrNotFound, ref, routine, routine)
			}
		}
		return Task{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.annotated(s.tasks[id]), nil
}

// annotated copies a task and fills in which of the tasks it waits for are
// still open. A task that does not exist counts as open, so that a typo
// holds the task back visibly instead of letting it through.
func (s *Store) annotated(t *Task) Task {
	out := *t
	out.Timezone = s.Location().String()
	out.Blockers = nil
	for _, id := range t.After {
		if dep := s.tasks[id]; dep == nil || !dep.Status.Closed() {
			out.Blockers = append(out.Blockers, id)
		}
	}
	return out
}

// List returns copies of all tasks, newest ID first within each area.
func (s *Store) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		// A routine's task never closes; it is opened from the routine.
		if t.Routine {
			continue
		}
		out = append(out, s.annotated(t))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return idLess(out[j].ID, out[i].ID)
	})
	return out
}

// Children returns the tasks whose parent is id.
func (s *Store) Children(id string) []Task {
	var out []Task
	for _, t := range s.List() {
		if t.Parent == id {
			out = append(out, t)
		}
	}
	return out
}

func idLess(a, b string) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	x, _ := strconv.Atoi(a[1:])
	y, _ := strconv.Atoi(b[1:])
	return x < y
}

// NewTask describes a task to create.
type NewTask struct {
	Area   string
	Title  string
	Status Status // defaults to todo
	Parent string
	Slug   string // optional ASCII suffix for the directory name
	Body   string // optional; a skeleton is written when empty
	Actor  string // who asked, for the log
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Create allocates the next ID and writes the task directory. Allocation
// happens under the store lock, so one process never hands out an ID twice.
func (s *Store) Create(n NewTask) (Task, error) {
	if s.noTasks {
		return Task{}, ErrNoTasks
	}
	if n.Status == "" {
		n.Status = Todo
	}
	title := strings.TrimSpace(n.Title)
	switch {
	case !contains(s.areas, n.Area):
		return Task{}, fmt.Errorf("領域 %q はありません。%s のどれかを指定してください", n.Area, strings.Join(s.areas, ", "))
	case title == "" || strings.ContainsAny(title, "\n\r"):
		return Task{}, errors.New("題名を1行で指定してください")
	case !n.Status.Valid():
		return Task{}, fmt.Errorf("%q という状態はありません", n.Status)
	case n.Status == Waiting:
		return Task{}, fmt.Errorf("%w。タスクは外部待ち（waiting）の状態では作れません。作ったあとで、いつ、何がそろえば戻るのかを登録してから、lacia task set <ID> waiting を実行してください", ErrWaitingNeedsCondition)
	case n.Slug != "" && !slugPattern.MatchString(n.Slug):
		return Task{}, fmt.Errorf("slug %q には英小文字、数字、ハイフンだけを使えます", n.Slug)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.Parent != "" && s.tasks[n.Parent] == nil {
		return Task{}, fmt.Errorf("親のタスク %s がありません", n.Parent)
	}
	if n.Parent != "" && s.tasks[n.Parent].Routine {
		return Task{}, fmt.Errorf("%s は定期タスクです。親には通常のタスクを指定してください", n.Parent)
	}
	next := FirstNumber
	for id := range s.tasks {
		if number, err := strconv.Atoi(id[1:]); err == nil && strings.HasPrefix(id, s.prefix) && number >= next {
			next = number + 1
		}
	}
	now := s.now().In(s.Location())
	id := s.prefix + strconv.Itoa(next)
	name := id
	if n.Slug != "" {
		name += "-" + n.Slug
	}
	dir := filepath.ToSlash(filepath.Join(s.AreaDir(n.Area), "tasks", now.Format("2006-01"), name))
	doc := Doc{Body: n.Body}
	if strings.TrimSpace(doc.Body) == "" {
		doc.Body = Body(s.format, Placeholder, Placeholder)
	}
	doc.Set("id", id)
	doc.Set("title", title)
	doc.Set("status", string(n.Status))
	doc.Set("created", now.Format(time.DateOnly))
	doc.Set("parent", n.Parent)
	if n.Status.Closed() {
		doc.Set("done", now.Format(time.DateOnly))
	}
	abs := filepath.Join(s.root, dir)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return Task{}, err
	}
	// O_EXCL guards against a second process that allocated the same ID.
	f, err := os.OpenFile(filepath.Join(abs, "task.md"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Task{}, err
	}
	_, err = f.WriteString(doc.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Task{}, err
	}
	created := Event{Type: EventCreated, By: n.Actor, Status: string(n.Status), Text: fmt.Sprintf("タスクを作りました。状態は「%s」です。", n.Status.Label())}
	if err := s.record(dir, now, created); err != nil {
		return Task{}, err
	}
	t, err := s.read(n.Area, dir)
	if err != nil {
		return Task{}, err
	}
	s.tasks[id] = t
	return *t, nil
}

// SetStatus changes the status and records the change in the history. It
// returns the task and whether anything changed.
func (s *Store) SetStatus(ref string, status Status, actor, note string) (Task, bool, error) {
	if s.noTasks {
		return Task{}, false, ErrNoTasks
	}
	if !status.Valid() {
		return Task{}, false, fmt.Errorf("%q という状態はありません。%s のどれかを指定してください", status, StatusNames)
	}
	current, err := s.Get(ref)
	if err == nil && current.Routine && !status.RoutineStatus() {
		return Task{}, false, ErrRoutineStatus
	}
	// Asked before the lock is taken: the answer comes from the server.
	live := status == Waiting && s.hasLiveCondition(ref)
	return s.update(ref, func(doc *Doc, now time.Time) (Event, error) {
		old := Status(doc.Get("status"))
		if current.Routine && (doc.Get(routineActionsField) != "" || status == Review && old != status) {
			state, err := readRoutineActions(*doc)
			if err != nil {
				return Event{}, err
			}
			managed := state.ManagedReview
			state.ManagedReview = false
			if status == Review && old != status {
				state.Notice++
			}
			if managed || status == Review && old != status {
				if err := saveRoutineActionState(doc, state); err != nil {
					return Event{}, err
				}
				if old == status {
					return Event{Type: EventNote, By: actor, Text: "定期タスクの状態を担当が確認しました。"}, nil
				}
			}
		}
		if old == status {
			return Event{}, nil
		}
		// The task of a routine waits for the next run of its routine.
		if status == Waiting && !current.Routine && !docHasReadyConditions(doc) && !live {
			return Event{}, WaitingNeedsCondition(doc.Get("id"))
		}
		doc.Set("status", string(status))
		doc.Set("ready_since", "")
		if status != Doing {
			doc.Set("ready_run_ref", "")
		}
		doc.Set("stopped_at", "")
		// The owner is whoever is working now; nobody is in any other status.
		doc.Set("owner", "")
		doc.Set("owner_session", "")
		if status.Closed() {
			doc.Set("done", now.Format(time.DateOnly))
		} else {
			doc.Set("done", "")
		}
		return Event{Type: EventStatus, By: actor, From: string(old), To: string(status),
			Text:  statusChangeText(old, status, ""),
			After: strings.TrimSpace(note)}, nil
	})
}

// Owner names a conversation that works on a task.
type Owner struct {
	Label   string // shown to people, such as "local-claude"
	Session string // key in the registry of conversations
}

// Claim makes the conversation the owner of the task and moves the task to
// "doing" when it is not there yet. It is how a conversation starts work,
// and how one takes a task over from another.
func (s *Store) Claim(ref string, owner Owner, actor, note string) (Task, bool, error) {
	if s.noTasks {
		return Task{}, false, ErrNoTasks
	}
	if owner.Label == "" || owner.Session == "" {
		return Task{}, false, errors.New("担当にする会話が分かりません")
	}
	return s.update(ref, func(doc *Doc, now time.Time) (Event, error) {
		old, before := Status(doc.Get("status")), doc.Get("owner")
		if old == Doing && doc.Get("owner_session") == owner.Session {
			return Event{}, nil
		}
		doc.Set("status", string(Doing))
		doc.Set("ready_since", "")
		doc.Set("stopped_at", "")
		doc.Set("done", "")
		doc.Set("owner", owner.Label)
		doc.Set("owner_session", owner.Session)
		e := Event{By: actor, Owner: owner.Label, Session: owner.Session, After: strings.TrimSpace(note)}
		switch {
		case old != Doing:
			e.Type, e.From, e.To = EventStatus, string(old), string(Doing)
			e.Text = fmt.Sprintf("状態を「%s」から「%s」に変えました。担当は %s です。", old.Label(), Doing.Label(), owner.Label)
		case before == "":
			e.Type = EventClaimed
			e.Text = fmt.Sprintf("担当を %s にしました。", owner.Label)
		default:
			e.Type, e.Previous = EventClaimed, before
			e.Text = fmt.Sprintf("担当を %s から %s に替えました。", before, owner.Label)
		}
		return e, nil
	})
}

// SetField changes a user-editable front matter field. An empty value
// removes an optional field.
func (s *Store) SetField(ref, key, value, actor string) (Task, bool, error) {
	value = strings.TrimSpace(value)
	if t, err := s.Get(ref); err == nil && t.Routine {
		return Task{}, false, ErrRoutineField
	}
	switch key {
	case "title":
		if value == "" {
			return Task{}, false, errors.New("題名は空にできません")
		}
	case "parent":
		if value != "" {
			if parent, err := s.Get(value); err == nil && parent.Routine {
				return Task{}, false, fmt.Errorf("%s は定期タスクです。親には通常のタスクを指定してください", value)
			}
		}
	case "due", "priority", "project":
	case "after":
		ids := ParseIDs(value)
		for _, id := range ids {
			if !ValidID(id) {
				return Task{}, false, fmt.Errorf("%q がタスクのIDの形になっていません。X58, X61 のように書いてください", id)
			}
			if resolved, ok := s.Resolve(id); !ok {
				return Task{}, false, fmt.Errorf("%s というタスクはありません", id)
			} else if self, _ := s.Resolve(ref); resolved == self {
				return Task{}, false, errors.New("after にこのタスク自身のIDは指定できません")
			} else if dep, _ := s.Get(resolved); dep.Routine {
				return Task{}, false, fmt.Errorf("%s は定期タスクで、完了しません。定期タスクの次の実行を待つときは after_run を使ってください", id)
			}
		}
		value = strings.Join(ids, ", ")
	case "after_run":
		if value != "" && !validAfterRun(value) {
			return Task{}, false, errors.New("after_run には定期タスクのID（例 S1001）か従来の名前を指定してください")
		}
	case "resume_on", "review_at":
		if value != "" {
			when, err := parseWhenIn(value, s.Location())
			if err != nil {
				return Task{}, false, err
			}
			value = formatWhenIn(when, s.Location())
		}
	case "on_ready":
		if _, err := ReadyDestination(value); err != nil {
			return Task{}, false, err
		}
	default:
		return Task{}, false, fmt.Errorf("項目 %q はここでは変えられません。title, due, parent, priority, project, after, after_run, resume_on, review_at, on_ready のどれかを指定してください", key)
	}
	live := value == "" && contains(conditionKeys, key) && s.hasLiveCondition(ref)
	return s.update(ref, func(doc *Doc, at time.Time) (Event, error) {
		if doc.Get(key) == value {
			if key != "after_run" {
				return Event{}, nil
			}
			since := doc.Get("after_run_since")
			_, registered := runRegisteredAt(since)
			if value == "" && since == "" || value != "" && registered {
				return Event{}, nil
			}
		}
		if doc.Get("recheck_every") != "" && Status(doc.Get("status")) == Waiting {
			id := doc.Get("id")
			switch {
			case key == "resume_on" && value == "":
				return Event{}, fmt.Errorf("%s は、外部の変化を繰り返し確かめながら待っています。次の確認の日時は消せません。確認をやめるときは、lacia task set %s doing や lacia task set %s review で状態を変えてください", id, id, id)
			case key == "resume_on":
				next, _ := parseWhenIn(value, s.Location())
				if limit, err := parseWhenIn(doc.Get("recheck_until"), s.Location()); err == nil && next.After(limit) {
					return Event{}, fmt.Errorf("次の確認の日時 %s が、期限（%s）より後になります。期限を変えるときは、lacia task recheck %s --every %s --until 日時 で登録し直してください", value, doc.Get("recheck_until"), id, doc.Get("recheck_every"))
				}
			case value != "" && (key == "after" || key == "after_run" || key == "review_at" || key == "on_ready"):
				return Event{}, recheckConflict(id, key)
			}
		}
		doc.Set(key, value)
		if value == "" && contains(conditionKeys, key) && Status(doc.Get("status")) == Waiting && !docHasReadyConditions(doc) && !live {
			return Event{}, lastConditionError(doc.Get("id"), key)
		}
		switch key {
		case "after_run":
			doc.Set("after_run_since", "")
			doc.Set("ready_run_ref", "")
			if value != "" {
				doc.Set("after_run_since", at.UTC().Format(time.RFC3339Nano))
				doc.Set("ready_since", "")
			}
		case "after", "resume_on", "review_at", "on_ready":
			doc.Set("ready_run_ref", "")
		}
		e := Event{Type: EventField, By: actor, Key: key, Value: ptr(value)}
		if value == "" {
			e.Text = fmt.Sprintf("%s を消しました。", key)
		} else {
			e.Text = fmt.Sprintf("%s を「%s」にしました。", key, value)
		}
		return e, nil
	})
}

// Wake checks existing task and time conditions without a routine run source.
func (s *Store) Wake(now time.Time) []Task {
	return s.WakeWithRuns(now, nil)
}

// RoutineCompletion is the result of the first finished run after a wait
// was registered. A failure requires review even if on_ready asks to start.
type RoutineCompletion struct {
	ID, Reference, Outcome string
	Failed                 bool
}

// WakeWithRuns moves an open task to review after all of its conditions
// are met, including any after_run condition found by find. The consumed
// conditions are removed. ReadySince marks a task that is ready to start.
//
// A task marked to start keeps its status and is returned with OnReady set
// for the caller to start it, although the field is removed from task.md.
func (s *Store) WakeWithRuns(now time.Time, find func(name string, since time.Time) (RoutineCompletion, bool)) []Task {
	var woken []Task
	for _, t := range s.List() {
		if t.Status.Closed() || !t.HasReadyConditions() || len(t.Blockers) > 0 {
			continue
		}
		// The next round of a task that is looked at again and again is
		// not a date to wake it: Recheck moves it on.
		if t.Status == Waiting && t.Rechecks() {
			continue
		}
		ready := true
		var why []string
		for _, value := range []string{t.ResumeOn, t.ReviewAt} {
			if value == "" {
				continue
			}
			if when, err := parseWhenIn(value, s.Location()); err != nil || when.After(now) {
				ready = false
			} else {
				why = append(why, "指定の日時（"+value+"）になりました")
			}
		}
		if !ready {
			continue
		}
		var run RoutineCompletion
		if t.AfterRun != "" {
			since, registered := runRegisteredAt(t.AfterRunSince)
			if !registered || find == nil {
				continue
			}
			run, ready = find(t.AfterRun, since)
			if !ready || run.ID == "" {
				continue
			}
			run.Reference = readyRunReference(run.Reference)
		}
		snapshot := t
		start := snapshot.StartsWhenReady() && !run.Failed
		updated, changed, err := s.update(t.ID, func(doc *Doc, at time.Time) (Event, error) {
			old := Status(doc.Get("status"))
			if old.Closed() || doc.Get("after") != strings.Join(snapshot.After, ", ") ||
				doc.Get("after_run") != snapshot.AfterRun || doc.Get("after_run_since") != snapshot.AfterRunSince ||
				doc.Get("resume_on") != snapshot.ResumeOn || doc.Get("review_at") != snapshot.ReviewAt || doc.Get("on_ready") != snapshot.OnReady {
				return Event{}, nil // edited meanwhile; look again next time
			}
			var done, dropped []string
			for _, id := range snapshot.After {
				dep := s.tasks[id]
				if dep == nil || !dep.Status.Closed() {
					return Event{}, nil
				}
				if dep.Status == Cancelled {
					dropped = append(dropped, id)
				} else {
					done = append(done, id)
				}
			}
			if len(done) > 0 {
				why = append(why, strings.Join(done, "、")+" が完了しました")
			}
			if len(dropped) > 0 {
				why = append(why, strings.Join(dropped, "、")+" は取りやめになりました")
			}
			if run.ID != "" {
				why = append(why, fmt.Sprintf("定期タスク %s の実行（%s）が終了しました", t.AfterRun, run.ID))
				if run.Outcome != "" {
					why = append(why, fmt.Sprintf("実行の結果は「%s」です", run.Outcome))
				}
			}
			for _, key := range append([]string{"after", "after_run", "after_run_since", "resume_on", "review_at", "on_ready"}, recheckKeys...) {
				doc.Set(key, "")
			}
			doc.Set("ready_run_ref", run.Reference)
			e := Event{Type: EventWoke, Action: WakeClearedConditions, Request: run.ID, Path: run.Reference, Text: strings.Join(why, "。") + "。"}
			switch {
			case start:
				e.Text += "自動で着手する設定なので、会話に作業の開始を依頼します。"
			case run.Failed:
				doc.Set("ready_since", "")
				e.Text += "定期タスクの結果を確認するため、要対応に出します。"
			case old != Review:
				doc.Set("ready_since", formatWhenIn(at, s.Location()))
				e.Text += fmt.Sprintf("着手できるようになったので、状態を「%s」から「%s」に変えました。", old.Label(), Review.Label())
			}
			if !start && old != Review {
				doc.Set("status", string(Review))
				if run.ID != "" {
					doc.Set("owner", "")
					doc.Set("owner_session", "")
				}
				e.From, e.To = string(old), string(Review)
			}
			return e, nil
		})
		if err == nil && changed {
			if start {
				updated.OnReady = snapshot.OnReady
			}
			woken = append(woken, updated)
		}
	}
	return woken
}

func statusChangeText(from, to Status, reason string) string {
	change := fmt.Sprintf("状態を「%s」から「%s」に変えました。", from.Label(), to.Label())
	if reason == "" {
		return change
	}
	return reason + "\n" + change
}

// Ready moves an open task to "review" because something it waited for
// outside the task list has come, and says why in the log. Like Wake, it
// sets ready_since, so the task shows as one that can be taken up. A task
// that is already being worked on only gets the log entry, unless
// evenIfDoing is set: a request the worker sent to another conversation
// and nobody took is something the user has to see.
func (s *Store) Ready(ref, why string, evenIfDoing bool) (Task, bool, error) {
	return s.ready(ref, Event{Type: EventWoke, Text: strings.TrimSpace(why)}, evenIfDoing)
}

// ReadyForHandover makes an explicit action visible without interrupting
// a conversation that is already working on the task.
func (s *Store) ReadyForHandover(ref string, e Event) (Task, bool, error) {
	e.Type = EventWoke
	return s.ready(ref, e, false)
}

func (s *Store) ready(ref string, e Event, evenIfDoing bool) (Task, bool, error) {
	return s.update(ref, func(doc *Doc, at time.Time) (Event, error) {
		old := Status(doc.Get("status"))
		if old.Closed() {
			return Event{}, nil
		}
		if old != Review && (old != Doing || evenIfDoing) {
			doc.Set("status", string(Review))
			doc.Set("ready_since", formatWhenIn(at, s.Location()))
			doc.Set("owner", "")
			doc.Set("owner_session", "")
			e.From, e.To = string(old), string(Review)
			e.Text = statusChangeText(old, Review, e.Text)
		}
		return e, nil
	})
}

// Stop records that the server stopped taking writes to the task, because
// the same write kept coming, and says why in the log. An open task moves to
// "review" even while somebody works on it: a loop does not end by itself,
// so the user has to see it. stopped_at is set anew each time, which lets
// the notification client report a task that was in "review" already.
func (s *Store) Stop(ref, why string) (Task, bool, error) {
	return s.update(ref, func(doc *Doc, at time.Time) (Event, error) {
		old := Status(doc.Get("status"))
		e := Event{Type: EventStopped, Text: strings.TrimSpace(why)}
		doc.Set("stopped_at", at.In(s.Location()).Format("2006-01-02 15:04"))
		if !old.Closed() && old != Review {
			doc.Set("status", string(Review))
			doc.Set("ready_since", "")
			doc.Set("owner", "")
			doc.Set("owner_session", "")
			e.From, e.To = string(old), string(Review)
			e.Text = statusChangeText(old, Review, e.Text)
		}
		return e, nil
	})
}

// DependencyProblems reports "after" entries that can never be satisfied:
// tasks waiting for each other in a circle.
func (s *Store) DependencyProblems() []string {
	tasks := map[string]Task{}
	for _, t := range s.List() {
		tasks[t.ID] = t
	}
	var out []string
	for id, t := range tasks {
		if t.Status.Closed() {
			continue
		}
		seen := map[string]bool{}
		var visit func(string) bool
		visit = func(at string) bool {
			for _, next := range tasks[at].After {
				if next == id {
					return true
				}
				if !seen[next] && !tasks[next].Status.Closed() {
					seen[next] = true
					if visit(next) {
						return true
					}
				}
			}
			return false
		}
		if visit(id) {
			out = append(out, fmt.Sprintf("%s: after に指定したタスクをたどると、このタスクに戻ります。互いの完了を待っているので、このままでは着手できません。", id))
		}
	}
	sort.Strings(out)
	return out
}

// Log adds an event to the task's history. In log.md it becomes one line,
// the sentence followed by who wrote it in brackets.
func (s *Store) Log(ref string, e Event) error {
	id, ok := s.Resolve(ref)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record(s.tasks[id].Dir, s.now().In(s.Location()), e)
}

// Refresh re-reads one task after its files were edited outside the store.
func (s *Store) Refresh(ref string) error {
	id, ok := s.Resolve(ref)
	if !ok {
		if s.loadRoutineTask(ref) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.read(s.tasks[id].Area, s.tasks[id].Dir)
	if err != nil {
		return err
	}
	s.tasks[id] = t
	return nil
}

func (s *Store) update(ref string, change func(*Doc, time.Time) (Event, error)) (Task, bool, error) {
	id, ok := s.Resolve(ref)
	if !ok {
		return Task{}, false, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.tasks[id]
	path := filepath.Join(s.root, current.Dir, "task.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, false, err
	}
	doc, err := ParseDoc(string(data))
	if err != nil {
		return Task{}, false, fmt.Errorf("%s: %w", current.Dir, err)
	}
	now := s.now().In(s.Location())
	before := Status(doc.Get("status"))
	entry, err := change(&doc, now)
	if err != nil {
		return Task{}, false, err
	}
	changed := entry.Type != ""
	// Whatever moves a task out of waiting ends its rounds: they belong
	// to the wait, and the next round would bring the task back.
	if changed && before == Waiting && Status(doc.Get("status")) != Waiting && doc.Get("recheck_every") != "" {
		clearRecheck(&doc)
		entry.After += "外部待ちを離れたので、繰り返しの確認を止めました。"
	}
	if changed {
		if _, err := parseTask(doc, current.Routine); err != nil {
			return Task{}, false, err
		}
		if err := writeAtomic(path, doc.String()); err != nil {
			return Task{}, false, err
		}
		if err := s.record(current.Dir, now, entry); err != nil {
			return Task{}, false, err
		}
		// Format 1 keeps the body as it stood when the work changed hands,
		// so that what a conversation left can be read later.
		if after := Status(doc.Get("status")); before == Doing && after != Doing {
			if err := s.record(current.Dir, now, handover(before, after, doc.Body)); err != nil {
				return Task{}, false, err
			}
		}
	}
	t, err := s.read(current.Area, current.Dir)
	if err != nil {
		return Task{}, false, err
	}
	s.tasks[id] = t
	return s.annotated(t), changed, nil
}

// record writes an event to the task's JSON history.
func (s *Store) record(dir string, now time.Time, e Event) error {
	return AppendEvent(filepath.Join(s.root, dir, HistoryFile), now, e)
}

func writeAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".task-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Files lists what is in a task's directory, relative to it.
func (s *Store) Files(t Task) []string {
	var out []string
	base := filepath.Join(s.root, t.Dir)
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			rel, _ := filepath.Rel(base, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}
