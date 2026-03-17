package agent

import (
	eventbus "MattiasHognas/Kennel/internal/events"
	"fmt"
	"sync"
	"time"
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
	defaultTick   = 3 * time.Second
)

type AgentContract interface {
	Name() string
	Run() eventbus.EventChan
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
	stopCh     chan struct{}
	started    bool
	tick       time.Duration
	sequence   int
}

func (a *Agent) Name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.name
}

func (a *Agent) Run() eventbus.EventChan {
	a.mu.Lock()
	if !a.started {
		a.started = true
		a.stopCh = make(chan struct{})
		go a.loop(a.stopCh)
	}
	a.state = Running
	a.mu.Unlock()

	a.publishActivity("started")
	return a.activityCh
}

func (a *Agent) Stop() AgentState {
	a.mu.Lock()
	wasActive := a.started || a.state == Running || a.state == Completed
	if a.started {
		close(a.stopCh)
		a.started = false
		a.stopCh = nil
	}
	a.state = Stopped
	a.sequence = 0
	a.mu.Unlock()

	if wasActive {
		a.publishActivity("stopped")
	}
	return Stopped
}

func (a *Agent) Complete() AgentState {
	a.mu.Lock()
	wasActive := a.started || a.state == Running || a.state == Completed
	if a.started {
		close(a.stopCh)
		a.started = false
		a.stopCh = nil
	}
	a.state = Completed
	a.sequence = 0
	a.mu.Unlock()

	if wasActive {
		a.publishActivity("completed")
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
		tick:       defaultTick,
	}
}

func (a *Agent) loop(stopCh chan struct{}) {
	ticker := time.NewTicker(a.tick)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			a.mu.Lock()
			if a.state != Running {
				a.mu.Unlock()
				continue
			}

			a.sequence++
			sequence := a.sequence
			a.mu.Unlock()

			a.eventBus.Publish(activityTopic, eventbus.Event{Payload: fmt.Sprintf("reported activity %d", sequence)})
		}
	}
}

func (a *Agent) publishActivity(action string) {
	a.eventBus.Publish(activityTopic, eventbus.Event{Payload: action})
}
