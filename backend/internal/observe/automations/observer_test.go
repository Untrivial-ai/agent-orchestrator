package automations

import (
	"context"
	"testing"
	"time"
)

type fakePoller struct {
	times []time.Time
	err   error
}

func (f *fakePoller) Tick(_ context.Context, at time.Time) error {
	f.times = append(f.times, at)
	return f.err
}

// The observer owns cadence only; schedule decisions stay in the service and
// receive one normalized timestamp per poll.
func TestPollDelegatesUTCNowToService(t *testing.T) {
	local := time.Date(2026, time.August, 25, 18, 30, 0, 0, time.FixedZone("IST", 5*60*60+30*60))
	poller := &fakePoller{}
	observer := New(poller, Config{Clock: func() time.Time { return local }})
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(poller.times) != 1 || poller.times[0].Location() != time.UTC || !poller.times[0].Equal(local) {
		t.Fatalf("poll times = %#v, want one UTC instant %s", poller.times, local.UTC())
	}
}
