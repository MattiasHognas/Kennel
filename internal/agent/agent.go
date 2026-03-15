package agent

import "time"

type AgentState int

const (
	Running AgentState = iota
	Stopped
)

type AgentContract interface {
	Run() EventChan
	Stop() AgentState
	State() AgentState
}

type Agent struct {
	state    AgentState
	eventBus *EventBus
}

func (a *Agent) Run() EventChan {
	a.state = Running

	// wait  5 secods then publish event to output topic
	go func() {
		for {
			time.Sleep(time.Second * 5)
			a.eventBus.Publish("output", Event{Payload: "Hello from Agent"})
		}
	}()

	return a.eventBus.Subscribe("output")
}

func (a *Agent) Stop() AgentState {
	a.state = Stopped
	a.eventBus.subscribers = make(map[string][]EventChan) // Clear all subscribers
	return Stopped
}

func (a *Agent) State() AgentState {
	return a.state
}

func NewAgent() AgentContract {
	return &Agent{
		state:    Stopped,
		eventBus: NewEventBus(),
	}
}
