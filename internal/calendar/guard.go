package calendar

import (
	"encoding/json"
	"fmt"
)

// Guard protects the snapshot from a run that lost most of a calendar's
// events. While Codex produces the snapshot, a fetch that could not verify
// its data has replaced a full calendar with an almost empty one; a person
// looking at the page cannot tell that from a day with no plans. When a
// source falls below half of what the previous snapshot had, its previous
// events are kept and the source is marked partial with an explanation.
//
// It returns the JSON to store and whether it differs from current.
func Guard(previous, current []byte) ([]byte, bool, error) {
	var prev, cur map[string]any
	if err := json.Unmarshal(current, &cur); err != nil {
		return nil, false, fmt.Errorf("新しいスナップショットを読めません: %w", err)
	}
	if json.Unmarshal(previous, &prev) != nil {
		return current, false, nil // nothing to compare against
	}
	before := map[string]map[string]any{}
	for _, s := range sources(prev) {
		before[key(s)] = s
	}
	changed := false
	for _, s := range sources(cur) {
		old := before[key(s)]
		if old == nil {
			continue
		}
		had, has := events(old), events(s)
		if len(had) < 4 || len(has)*2 >= len(had) {
			continue
		}
		s["events"] = old["events"]
		s["status"], s["complete"] = "partial", false
		s["error"] = fmt.Sprintf("今回の取得では予定が%d件から%d件に減ったため、前回の内容を残しています。", len(had), len(has))
		if at, ok := old["updated_at"]; ok {
			s["updated_at"] = at
		}
		changed = true
	}
	if !changed {
		return current, false, nil
	}
	out, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

func sources(snapshot map[string]any) []map[string]any {
	list, _ := snapshot["sources"].([]any)
	var out []map[string]any
	for _, item := range list {
		if s, ok := item.(map[string]any); ok {
			out = append(out, s)
		}
	}
	return out
}

func events(source map[string]any) []any {
	list, _ := source["events"].([]any)
	return list
}

func key(source map[string]any) string {
	account, _ := source["account"].(string)
	id, _ := source["calendar_id"].(string)
	return account + "\x00" + id
}
