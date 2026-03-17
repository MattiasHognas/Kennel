package agent

import (
	"testing"
	"time"

	eventbus "MattiasHognas/Kennel/internal/events"
)

func TestStopPublishesOnlyAfterRun(t *testing.T) {
	a := NewAgent("Tester")
	activityCh := a.SubscribeActivity()

	a.Stop()
	assertNoActivity(t, activityCh)

	a.Run()
	assertActivity(t, activityCh, "started")

	a.Stop()
	assertActivity(t, activityCh, "stopped")
}

func assertActivity(t *testing.T, ch <-chan eventbus.Event, want string) {
	t.Helper()

	select {
	case event := <-ch:
		if got := event.Payload; got != want {
			t.Fatalf("activity payload = %v, want %q", got, want)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for activity %q", want)
	}
}

func assertNoActivity(t *testing.T, ch <-chan eventbus.Event) {
	t.Helper()

	select {
	case event := <-ch:
		t.Fatalf("unexpected activity: %v", event.Payload)
	default:
	}
}
