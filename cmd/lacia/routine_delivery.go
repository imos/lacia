package main

import (
	"errors"
	"fmt"

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

// syncExistingRoutineProblems opens a receipt for a current failure recorded
// before the server started using the review queue for failed runs.
func syncExistingRoutineProblems(store *routine.Store, jobs []schedule.Job) error {
	var failures []error
	for _, job := range jobs {
		if job.ID == "" || !job.Enabled || job.Err != "" {
			continue
		}
		health := schedule.HealthOf(store.Root, job.ID)
		if run := health.LatestExecution; run.HasProblem() {
			if _, _, err := store.Accept(job.ID, run.ProblemReport(health.LastSuccessAt)); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", job.ID, err))
			}
		}
	}
	return errors.Join(failures...)
}
