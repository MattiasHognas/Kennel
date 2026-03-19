package supervisor_test

import (
"context"
"testing"

"MattiasHognas/Kennel/internal/acp"
"MattiasHognas/Kennel/internal/supervisor"
)

func TestExecutionFlowSupervisor(t *testing.T) {
// 5. Add worker/supervisor tests with a fake ACP client proving streamed SessionUpdate
fakeAcpClient := &acp.FakeClient{
Response: "Test planned output",
}

super := supervisor.NewSupervisor(fakeAcpClient)

ctx := context.Background()
res, err := super.RunPlan(ctx, "execute phase 6 integration fake")
if err != nil {
t.Fatalf("unexpected error running plan: %v", err)
}

if res != "Test planned output" {
t.Fatalf("expected Test planned output, but got %q", res)
}
}

