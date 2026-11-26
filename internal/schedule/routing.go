package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/imos/lacia/internal/caller"
)

const LocalDevice = "local"

type placeSetting struct {
	Device     string `json:"device"`
	Default    string `json:"default"`
	SecretFile string `json:"secret_file"`
}

// Routing is trusted installation configuration. A place is either this
// server or a registered device with a credential outside the records.
type Routing struct {
	here       string
	serverKind string
	kinds      map[string]bool
	places     map[string]placeSetting
	devices    map[string]string
}

func LoadRouting(raw, base, here, defaultKind string, kinds ...string) (Routing, error) {
	if here == "" {
		here = LocalDevice
	}
	cfg := Routing{
		here: here, serverKind: defaultKind, kinds: make(map[string]bool),
		places: make(map[string]placeSetting), devices: make(map[string]string),
	}
	if !validPlace(here) {
		return cfg, fmt.Errorf("場所の名前 %q を読めません", here)
	}
	for _, kind := range kinds {
		cfg.kinds[kind] = true
	}
	if cfg.serverKind == "" {
		switch {
		case cfg.kinds[caller.Codex]:
			cfg.serverKind = caller.Codex
		case cfg.kinds[caller.Claude]:
			cfg.serverKind = caller.Claude
		}
	} else if !cfg.kinds[cfg.serverKind] {
		return cfg, fmt.Errorf("既定の会話 %q を、このサーバーで起動する設定がありません", cfg.serverKind)
	}
	if raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg.places); err != nil {
			return cfg, fmt.Errorf("places の設定を JSON の対応表として読めません: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return cfg, errors.New("places には1つのJSONの対応表を書いてください")
		}
	}
	seenSecrets := make(map[string]string)
	for name, place := range cfg.places {
		if !validPlace(name) || name == here {
			return cfg, fmt.Errorf("places の場所の名前 %q は使えません", name)
		}
		if place.Default == "" {
			if place.Device == LocalDevice {
				place.Default = cfg.serverKind
			} else {
				place.Default = caller.Codex
			}
		}
		if place.Default != caller.Claude && place.Default != caller.Codex {
			return cfg, fmt.Errorf("場所 %s の default には codex か claude を指定してください", name)
		}
		if place.Device == LocalDevice {
			if place.SecretFile != "" || !cfg.kinds[place.Default] {
				return cfg, fmt.Errorf("場所 %s はこのサーバーを指しています。default にはこのサーバーで起動できる会話を選び、secret_file は指定しないでください", name)
			}
		} else {
			if !namePattern.MatchString(place.Device) || place.SecretFile == "" {
				return cfg, fmt.Errorf("場所 %s に登録済みの端末と秘密ファイルを指定してください", name)
			}
			if !filepath.IsAbs(place.SecretFile) {
				place.SecretFile = filepath.Join(base, place.SecretFile)
			}
			secret, err := privateSecret(place.SecretFile)
			if err != nil {
				return cfg, fmt.Errorf("端末 %s の秘密ファイルを読めません", place.Device)
			}
			if other := seenSecrets[string(secret)]; other != "" && other != place.Device {
				return cfg, errors.New("端末ごとに別の秘密ファイルを用意してください")
			}
			if previous := cfg.devices[place.Device]; previous != "" && previous != place.SecretFile {
				return cfg, fmt.Errorf("端末 %s の秘密ファイルは1つにしてください", place.Device)
			}
			seenSecrets[string(secret)] = place.Device
			cfg.devices[place.Device] = place.SecretFile
		}
		cfg.places[name] = place
	}
	return cfg, nil
}

func validPlace(name string) bool {
	destination, err := caller.ParseDestination(name)
	return err == nil && destination.Place == name && destination.Kind == "" && name != ""
}

// Route fills in both components and returns the delivery device. A named
// place or kind is never silently replaced.
func (r Routing) Route(value string) (caller.Destination, string, error) {
	destination, err := caller.ParseDestination(value)
	if err != nil {
		return destination, "", err
	}
	if destination.Place == "" {
		destination.Place = r.here
	}
	place, ok := r.places[destination.Place]
	if destination.Place == r.here {
		place, ok = placeSetting{Device: LocalDevice, Default: r.serverKind}, true
	}
	if !ok {
		return destination, "", fmt.Errorf("宛先の場所 %q が設定されていません", destination.Place)
	}
	if destination.Kind == "" {
		destination.Kind = place.Default
	}
	if place.Device == LocalDevice {
		if !r.kinds[destination.Kind] {
			return destination, "", fmt.Errorf("このサーバーで宛先 %q の会話を起動する設定がありません", destination.String())
		}
		destination.Place = r.here
	}
	return destination, place.Device, nil
}

func (r Routing) Resolve(value string) (caller.Destination, error) {
	destination, _, err := r.Route(value)
	return destination, err
}

// Check preserves parse errors and marks unknown destinations the same way
// so both the schedule list and its health notice can report them.
func (r Routing) Check(jobs []Job) []Job {
	for i := range jobs {
		job := &jobs[i]
		if job.Enabled && job.Err == "" {
			if _, err := r.Resolve(job.To); err != nil {
				job.Err, job.Enabled = err.Error(), false
			}
		}
	}
	return jobs
}
