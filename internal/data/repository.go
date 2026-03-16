package repository

import agent "MattiasHognas/Kennel/internal/workers"

type Project struct {
	Name       string
	State      agent.AgentState
	Agents     []agent.AgentContract
	Activities []string
}
