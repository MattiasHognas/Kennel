package eventbus

import (
	"sync"
	"testing"
)

func TestEventBus(t *testing.T) {
	eb := NewEventBus()
	ch := eb.Subscribe("test")

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		ev := <-ch
		if _, ok := ev.Payload.(WorkerMessageEvent); !ok {
			t.Errorf("expected WorkerMessageEvent")
		}
	}()

	eb.Publish("test", Event{Payload: WorkerMessageEvent{Chunk: "hello"}})
	wg.Wait()

	eb.Unsubscribe("test", ch)
}
