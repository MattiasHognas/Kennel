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
	State() AgentState
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
	wasActive := a.started || a.state == Running || a.state == Completed
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
	wasActive := a.started || a.state == Running || a.state == Completed
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

func (a *Agent) State() AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
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
