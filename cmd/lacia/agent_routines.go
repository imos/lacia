package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/imos/lacia/internal/notify"
)

func ackRoutineNotices(ctx context.Context, client *http.Client, base string, results []notify.RoutineNotice, manifest notify.Manifest) {
	handedOff := map[string]bool{}
	for _, n := range manifest.Notices {
		handedOff[n.ID] = true
	}
	for _, result := range results {
		if !handedOff[result.ID()] {
			continue
		}
		path := fmt.Sprintf("%s/api/routine-notices/%s/%d", base, url.PathEscape(result.RoutineID), result.Sequence)
		if result.Action {
			path += "?action=1"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		if err != nil {
			slog.Warn("routine notice acknowledgement", "id", result.ID(), "error", err)
		}
	}
}
