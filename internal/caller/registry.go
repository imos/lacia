package caller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imos/lacia/internal/effect/disk"
)

// Session is one conversation the server has heard from.
type Session struct {
	Key  string `json:"key"`
	Name string `json:"name"` // "local-claude"
	// Number tells conversations with the same name apart. The first one
	// has number 1, which is not shown.
	Number int       `json:"number"`
	Caller Caller    `json:"caller"`
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
}

// Label is the name shown to people: "local-claude", or "local-claude 2"
// when another conversation with the same name was active at the time.
func (s Session) Label() string {
	if s.Number > 1 {
		return fmt.Sprintf("%s %d", s.Name, s.Number)
	}
	return s.Name
}

// Actor is the label for the log, with the agents that called this one.
func (s Session) Actor() string {
	if via := s.Caller.Via(); len(via) > 0 {
		return s.Label() + "、" + strings.Join(via, "、") + " から"
	}
	return s.Label()
}

const (
	// Active is how long after its last command a conversation still counts
	// as running, for numbering and for the list of who is working.
	Active = 12 * time.Hour
	// kept is how long a silent conversation stays in the file.
	kept = 30 * 24 * time.Hour
)

// Registry remembers the conversations that ran commands. It lives in the
// state directory, outside Git: process IDs are of no use in the history.
type Registry struct {
	Now func() time.Time // replaceable in tests

	// dir is nil for a registry kept in memory only.
	dir      *disk.Dir
	name     string
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewRegistry keeps the registry in memory only.
func NewRegistry() *Registry {
	return &Registry{Now: time.Now, sessions: map[string]*Session{}}
}

// OpenRegistry reads the file of that name in dir, and keeps the registry
// there.
func OpenRegistry(dir *disk.Dir, name string) (*Registry, error) {
	r := NewRegistry()
	r.dir, r.name = dir, name
	data, err := dir.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Session
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir.Path(), name), err)
	}
	for _, s := range list {
		r.sessions[s.Key] = s
	}
	return r, nil
}

// Seen records that the caller ran a command just now and returns its
// session. The second result is false when no agent ran the command.
func (r *Registry) Seen(c Caller) (Session, bool) {
	key := c.Key()
	if key == "" {
		return Session{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.Now()
	s := r.sessions[key]
	if s == nil {
		s = &Session{Key: key, Name: c.Name(), First: now}
		used := map[int]bool{}
		for _, other := range r.sessions {
			if other.Name == s.Name && now.Sub(other.Last) < Active {
				used[other.Number] = true
			}
		}
		for s.Number = 1; used[s.Number]; s.Number++ {
		}
		r.sessions[key] = s
	}
	s.Caller, s.Last = c, now
	for k, other := range r.sessions {
		if now.Sub(other.Last) > kept {
			delete(r.sessions, k)
		}
	}
	// Losing the file only loses the details; the request still goes on.
	_ = r.save()
	return *s, true
}

func (r *Registry) Get(key string) (Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[key]
	if s == nil {
		return Session{}, false
	}
	return *s, true
}

// List returns every remembered session, the most recently heard first.
func (r *Registry) List() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Last.Equal(list[j].Last) {
			return list[i].Last.After(list[j].Last)
		}
		return list[i].Key < list[j].Key
	})
	return list
}

func (r *Registry) save() error {
	if r.dir == nil {
		return nil
	}
	list := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	data, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	return r.dir.WriteFileNoSync(r.name, data, 0o600)
}
