package agent

import (
	"context"
	"sync"

	eventbus "MattiasHognas/Kennel/internal/events"
)

type AgentState int

const (
	Stopped AgentState = iota
	Running
	Completed
	Failed
)

const (
	activityTopic = "output"
	defaultName   = "Agent"
)

type AgentContract interface {
	Name() string
	Run(ctx context.Context) eventbus.EventChan
	Stop() AgentState
	Complete() AgentState
	Fail(err error) AgentState
	State() AgentState
	Hydrate(state AgentState)
	SubscribeActivity() eventbus.EventChan
}

type Agent struct {
	mu         sync.RWMutex
	name       string
	state      AgentState
	eventBus   *eventbus.EventBus
	activityCh eventbus.EventChan
	started    bool
}

func (a *Agent) Name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.name
}

func (a *Agent) Run(ctx context.Context) eventbus.EventChan {
	a.mu.Lock()
	if !a.started {
		a.started = true
	}
	a.state = Running
	a.mu.Unlock()

	a.publishActivity(eventbus.WorkerMessageEvent{Chunk: "started"})
	return a.activityCh
}

func (a *Agent) Stop() AgentState {
	a.mu.Lock()
	wasActive := a.started || a.state == Running || a.state == Completed || a.state == Failed
	if a.started {
		a.started = false
	}
	a.state = Stopped
	a.mu.Unlock()

	if wasActive {
		a.publishActivity(eventbus.WorkerCancellationEvent{Reason: "stopped"})
	}
	return Stopped
}

func (a *Agent) Complete() AgentState {
	a.mu.Lock()
	wasActive := a.started || a.state == Running || a.state == Completed || a.state == Failed
	if a.started {
		a.started = false
	}
	a.state = Completed
	a.mu.Unlock()

	if wasActive {
		a.publishActivity(eventbus.WorkerCompletionEvent{Result: "completed"})
	}
	return Completed
}

func (a *Agent) Fail(err error) AgentState {
	a.mu.Lock()
	wasActive := a.started || a.state == Running || a.state == Completed || a.state == Failed
	if a.started {
		a.started = false
	}
	a.state = Failed
	a.mu.Unlock()

	if wasActive {
		a.publishActivity(eventbus.WorkerFailureEvent{Error: err})
	}
	return Failed
}

func (a *Agent) State() AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
}

func (a *Agent) Hydrate(state AgentState) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.state = state
	a.started = state == Running
}

func (a *Agent) SubscribeActivity() eventbus.EventChan {
	return a.activityCh
}

func (a AgentState) String() string {
	switch a {
	case Running:
		return "running"
	case Completed:
		return "completed"
	case Failed:
		return "failed"
	default:
		return "stopped"
	}
}

func NewAgent(name string) AgentContract {
	if name == "" {
		name = defaultName
	}

	eventBus := eventbus.NewEventBus()

	return &Agent{
		name:       name,
		state:      Stopped,
		eventBus:   eventBus,
		activityCh: eventBus.Subscribe(activityTopic),
	}
}

func (a *Agent) publishActivity(action any) {
	a.eventBus.Publish(activityTopic, eventbus.Event{Payload: action})
}
