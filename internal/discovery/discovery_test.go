package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentDefinitions(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, "agents")
	os.MkdirAll(agentsDir, 0755)

	// global copilot.json
	os.WriteFile(filepath.Join(agentsDir, "copilot.json"), []byte(`{"binary":"global-copilot"}`), 0644)

	// agent 1: uses global
	agent1Dir := filepath.Join(agentsDir, "agent1")
	os.MkdirAll(agent1Dir, 0755)
	os.WriteFile(filepath.Join(agent1Dir, "instructions.md"), []byte(`test instructions`), 0644)

	// agent 2: overrides with agent.json
	agent2Dir := filepath.Join(agentsDir, "agent2")
	os.MkdirAll(agent2Dir, 0755)
	os.WriteFile(filepath.Join(agent2Dir, "instructions.md"), []byte(`test instructions`), 0644)
	os.WriteFile(filepath.Join(agent2Dir, "agent.json"), []byte(`{"binary":"custom-copilot"}`), 0644)

	// agent 3: no instructions (should be skipped)
	agent3Dir := filepath.Join(agentsDir, "agent3")
	os.MkdirAll(agent3Dir, 0755)

	defs, err := LoadAgentDefinitions(tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(defs))
	}

	var a1, a2 *AgentDefinition
	for i := range defs {
		if defs[i].Name == "agent1" {
			a1 = &defs[i]
		}
		if defs[i].Name == "agent2" {
			a2 = &defs[i]
		}
	}

	if a1 == nil || a2 == nil {
		t.Fatalf("missing expected agents")
	}

	if a1.LaunchConfig.Binary != "global-copilot" {
		t.Errorf("agent1 expected global config binary 'global-copilot', got %q", a1.LaunchConfig.Binary)
	}

	if a2.LaunchConfig.Binary != "custom-copilot" {
		t.Errorf("agent2 expected custom config binary 'custom-copilot', got %q", a2.LaunchConfig.Binary)
	}
}
