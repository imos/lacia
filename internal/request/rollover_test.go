package request

import (
	"context"
	"testing"
	"time"
)

func TestMainReservationWaitsForRequestsAndKeepsSideIndependent(t *testing.T) {
	q, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	add := func(r Request) Request {
		t.Helper()
		saved, _, err := q.Add(r)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	wait := func(event <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-event:
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %s", what, ctx.Err())
		}
	}

	first := add(Request{Text: "最初の窓口への依頼"})
	noReservation := func() { t.Error("the main lane is busy") }
	q.WhenMainIdle(noReservation) // A queued main request has priority.
	firstRun, allowFirst := make(chan struct{}), make(chan struct{})
	finishing, allowFinish := make(chan struct{}), make(chan struct{})
	secondRun := make(chan struct{})
	q.Finished = func(r Request) {
		if r.ID == first.ID {
			close(finishing)
			select {
			case <-allowFinish:
			case <-ctx.Done():
			}
		}
	}
	go q.Work(ctx, false, func(ctx context.Context, _ string) (string, string, error) {
		if Current(ctx).ID == first.ID {
			close(firstRun)
			select {
			case <-allowFirst:
			case <-ctx.Done():
			}
		} else {
			close(secondRun)
		}
		return "返事", "", nil
	}, nil)
	wait(firstRun, "the first request")
	q.WhenMainIdle(noReservation)
	close(allowFirst)
	wait(finishing, "reply delivery")
	q.WhenMainIdle(noReservation)
	close(allowFinish)

	sideRun, allowSide := make(chan struct{}), make(chan struct{})
	add(Request{Text: "別の会話への依頼", Side: true})
	go q.Work(ctx, true, func(ctx context.Context, _ string) (string, string, error) {
		close(sideRun)
		select {
		case <-allowSide:
		case <-ctx.Done():
		}
		return "返事", "", nil
	}, nil)
	wait(sideRun, "a running side request")

	reserved, release, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(released)
		for ctx.Err() == nil {
			q.WhenMainIdle(func() {
				close(reserved)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			select {
			case <-reserved:
				return
			case <-ctx.Done():
			case <-time.After(time.Millisecond):
			}
		}
	}()
	wait(reserved, "an idle main lane alongside a running side request")
	second := add(Request{Text: "切り替えの間に届いた窓口への依頼"})
	if got, ok := q.Get(second.ID); !ok || got.Status != Queued {
		t.Fatalf("the new request did not stay queued during rollover: %+v", got)
	}
	select {
	case <-secondRun:
		t.Fatal("a main request started during rollover")
	default:
	}
	close(release)
	wait(released, "the end of rollover")
	wait(secondRun, "the queued request starting after rollover")
	close(allowSide)
}
