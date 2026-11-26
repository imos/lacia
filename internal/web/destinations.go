package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/imos/lacia/internal/caller"
	"github.com/imos/lacia/internal/task"
)

// For a kind-only handover, prefer the configured local destination
// when it supports that kind. Otherwise, resolve it on this server.
func (s *Server) resolveHandover(value string) (string, error) {
	to, err := caller.ParseDestination(value)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("申し送りの宛先を指定してください")
	}
	if s.ResolveDestination == nil {
		return to.String(), nil
	}
	if to.Place == "" {
		if local, err := s.ResolveDestination("local-" + to.Kind); err == nil {
			return local.String(), nil
		}
	}
	resolved, err := s.ResolveDestination(value)
	if err != nil {
		return "", err
	}
	return resolved.String(), nil
}

func (s *Server) readyValue(to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return task.OnReadyStart, nil
	}
	value := task.OnReadyStart + " " + to
	if _, err := task.ReadyDestination(value); err != nil {
		return "", err
	}
	if s.ResolveDestination != nil {
		if _, err := s.ResolveDestination(to); err != nil {
			return "", err
		}
	}
	return value, nil
}

func (s *Server) setReadyValue(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.FormValue("start") == "" {
		return "", true
	}
	value, err := s.readyValue(r.FormValue("to"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return value, true
}

func (s *Server) targetReady(t task.Task) (caller.Destination, error) {
	raw, err := task.ReadyDestination(t.OnReady)
	if err != nil || raw == "" {
		return caller.Destination{}, err
	}
	if s.ResolveDestination == nil {
		return caller.Destination{}, fmt.Errorf("%q がどの会話を指すのか、確かめられません", raw)
	}
	return s.ResolveDestination(raw)
}
