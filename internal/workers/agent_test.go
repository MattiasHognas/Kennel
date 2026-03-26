package workers

import (
	data "MattiasHognas/Kennel/internal/data"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestStopPublishesOnlyAfterRun(t *testing.T) {
	a := NewAgent("Tester")
	activityCh := a.SubscribeActivity()

	a.Stop()
	assertNoActivity(t, activityCh)

	a.Run(context.Background())
	assertActivityType(t, activityCh, data.WorkerMessageEvent{})

	a.Stop()
	assertActivityType(t, activityCh, data.WorkerCancellationEvent{})
}

func TestCompletePublishesOnlyAfterActivation(t *testing.T) {
	a := NewAgent("Tester")
	activityCh := a.SubscribeActivity()

	a.Complete()
	assertNoActivity(t, activityCh)

	a.Run(context.Background())
	assertActivityType(t, activityCh, data.WorkerMessageEvent{})

	a.Complete()
	assertActivityType(t, activityCh, data.WorkerCompletionEvent{})

	if got := a.State(); got != Completed {
		t.Fatalf("state = %s, want %s", got, Completed)
	}
}

func TestHydrateSetsStateWithoutPublishingActivity(t *testing.T) {
	a := NewAgent("Tester")
	activityCh := a.SubscribeActivity()

	a.Hydrate(Running)
	if got := a.State(); got != Running {
		t.Fatalf("state = %s, want %s", got, Running)
	}
	assertNoActivity(t, activityCh)

	a.Hydrate(Completed)
	if got := a.State(); got != Completed {
		t.Fatalf("state = %s, want %s", got, Completed)
	}
	assertNoActivity(t, activityCh)
}

func TestFailPublishesFailureAfterActivation(t *testing.T) {
	a := NewAgent("Tester")
	activityCh := a.SubscribeActivity()

	a.Fail(nil)
	assertNoActivity(t, activityCh)

	a.Run(context.Background())
	assertActivityType(t, activityCh, data.WorkerMessageEvent{})

	a.Fail(fmt.Errorf("boom"))
	assertActivityType(t, activityCh, data.WorkerFailureEvent{})

	if got := a.State(); got != Failed {
		t.Fatalf("state = %s, want %s", got, Failed)
	}
}

func assertActivityType(t *testing.T, ch <-chan data.Event, wantType interface{}) {
	t.Helper()

	select {
	case event := <-ch:
		t1 := fmt.Sprintf("%T", event.Payload)
		t2 := fmt.Sprintf("%T", wantType)
		if t1 != t2 {
			t.Fatalf("activity payload type = %T, want %T", event.Payload, wantType)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timed out waiting for activity")
	}
}

func assertNoActivity(t *testing.T, ch <-chan data.Event) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected activity: %v", event.Payload)
	default:
	}
}
