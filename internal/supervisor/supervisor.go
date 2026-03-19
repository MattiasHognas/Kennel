package supervisor

import (
"context"
"MattiasHognas/Kennel/internal/acp"
)

type Supervisor struct {
client acp.Client
}

func NewSupervisor(client acp.Client) *Supervisor {
return &Supervisor{client: client}
}

func (s *Supervisor) RunPlan(ctx context.Context, plan string) (string, error) {
return s.client.Prompt(ctx, plan)
}

