package caller

import (
	"fmt"
	"strings"
)

// Destination names a place, a conversation kind, or both. Empty fields
// are resolved by the caller's defaults.
type Destination struct {
	Place string
	Kind  string
}

func (d Destination) String() string {
	if d.Place == "" {
		return d.Kind
	}
	if d.Kind == "" {
		return d.Place
	}
	return d.Place + "-" + d.Kind
}

func ParseDestination(value string) (Destination, error) {
	switch value {
	case "":
		return Destination{}, nil
	case Codex, Claude:
		return Destination{Kind: value}, nil
	}
	place, kind := value, ""
	if prefix, ok := strings.CutSuffix(value, "-codex"); ok {
		place, kind = prefix, Codex
	} else if prefix, ok := strings.CutSuffix(value, "-claude"); ok {
		place, kind = prefix, Claude
	}
	if !placeName.MatchString(place) {
		return Destination{}, fmt.Errorf("宛先 %q は、場所か「場所-種類」の形で書いてください", value)
	}
	return Destination{Place: place, Kind: kind}, nil
}
