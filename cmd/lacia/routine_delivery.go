package main

import (
	"github.com/imos/lacia/internal/request"
	"github.com/imos/lacia/internal/routine"
	"github.com/imos/lacia/internal/schedule"
)

// syncRoutineDeliveries records only a successful existing text delivery. It
// never sends a message, retries an ambiguous POST, or marks a receipt reviewed.
func syncRoutineDeliveries(store *routine.Store, jobs []schedule.Job, get func(string) (request.Request, bool)) error {
	for _, job := range jobs {
		if job.ID == "" {
			continue
		}
		snapshot, err := store.Snapshot(job.ID)
		if err != nil {
			return err
		}
		for _, receipt := range snapshot.Receipts {
			if receipt.Notified {
				continue
			}
			r, ok := get(receipt.RunID)
			if !ok || r.Kind != request.System || r.ReplyTo == "" || r.Delivery != "sent" {
				continue
			}
			if err := store.MarkNotified(job.ID, receipt.Sequence); err != nil {
				return err
			}
		}
	}
	return nil
}
