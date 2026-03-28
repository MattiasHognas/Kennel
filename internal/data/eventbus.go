package data

import "sync"

const SupervisorTopic = "supervisor"

type PlanUpdateEvent struct{ Plan string }
type SupervisorSyncEvent struct {
	ProjectID   int64
	AgentID     int64
	Agent       string
	InstanceKey string
	State       string
	Activity    string
}
type WorkerMessageEvent struct{ Chunk string }
type ToolCallEvent struct {
	ToolName string
	Args     string
}
type WorkerCompletionEvent struct{ Result string }
type WorkerCancellationEvent struct{ Reason string }
type WorkerFailureEvent struct{ Error error }

type Event struct {
	Payload any
}

type (
	EventChan chan Event
)

type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]EventChan
}

func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string][]EventChan),
	}
}

func (eb *EventBus) Publish(topic string, event Event) {
	eb.mu.RLock()
	subscribers := append([]EventChan{}, eb.subscribers[topic]...)
	eb.mu.RUnlock()

	for _, subscriber := range subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (eb *EventBus) Subscribe(topic string) EventChan {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	ch := make(EventChan, 16)
	eb.subscribers[topic] = append(eb.subscribers[topic], ch)
	return ch
}

func (eb *EventBus) Unsubscribe(topic string, ch EventChan) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	if subscribers, ok := eb.subscribers[topic]; ok {
		for i, subscriber := range subscribers {
			if ch == subscriber {
				eb.subscribers[topic] = append(subscribers[:i], subscribers[i+1:]...)
				return
			}
		}
	}
}
