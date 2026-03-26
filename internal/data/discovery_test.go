package data

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadAgentDefinitions(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, "agents")
	os.MkdirAll(agentsDir, 0755)

	// global default.json
	os.WriteFile(filepath.Join(agentsDir, "default.json"), []byte(`{
		"binary":"global-copilot",
		"args":["--acp"],
		"mcpServers":[{
			"transport":"stdio",
			"name":"shared-language-server",
			"command":"uvx",
			"args":["python-mcp"]
		}],
		"permissions":{
			"git":{
				"status":true,
				"diff":true,
				"history":true
			},
			"acp":{
				"readTextFile":true,
				"writeTextFile":true,
				"requestPermission":true,
				"createTerminal":true,
				"killTerminal":true,
				"terminalOutput":true,
				"releaseTerminal":true,
				"waitForTerminal":true
			}
		}
	}`), 0644)

	// agent 1: uses global
	agent1Dir := filepath.Join(agentsDir, "agent1")
	os.MkdirAll(agent1Dir, 0755)
	os.WriteFile(filepath.Join(agent1Dir, "instructions.md"), []byte(`test instructions`), 0644)

	// agent 2: overrides with agent.json
	agent2Dir := filepath.Join(agentsDir, "agent2")
	os.MkdirAll(agent2Dir, 0755)
	os.WriteFile(filepath.Join(agent2Dir, "instructions.md"), []byte(`test instructions`), 0644)
	os.WriteFile(filepath.Join(agent2Dir, "agent.json"), []byte(`{
		"binary":"custom-copilot",
		"args":["--acp","--model","gpt-5.4"],
		"promptContext":{"previousOutput":false},
		"permissions":{
			"git":{
				"status":false,
				"diff":false,
				"history":false
			},
			"acp":{
				"createTerminal":false,
				"terminalOutput":false,
				"waitForTerminal":false
			}
		},
		"mcpServers":[{
			"transport":"stdio",
			"name":"playwright",
			"command":"npx",
			"args":["@playwright/mcp@latest"]
		}]
	}`), 0644)

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

	if !slices.Equal(a2.LaunchConfig.Args, []string{"--acp", "--model", "gpt-5.4"}) {
		t.Errorf("agent2 expected custom args override, got %#v", a2.LaunchConfig.Args)
	}

	if !a1.PromptContext.PreviousOutput {
		t.Errorf("agent1 expected default previousOutput to be enabled")
	}

	if len(a1.MCPServers) != 1 || a1.MCPServers[0].Name != "shared-language-server" {
		t.Fatalf("agent1 expected inherited MCP server, got %#v", a1.MCPServers)
	}

	if a2.PromptContext.PreviousOutput {
		t.Errorf("agent2 expected previousOutput override to be disabled")
	}

	if a2.Permissions.Git.Status || a2.Permissions.Git.Diff || a2.Permissions.Git.History {
		t.Errorf("agent2 expected git permissions override to disable status, diff, and history: %#v", a2.Permissions.Git)
	}

	if a2.Permissions.ACP.CreateTerminal || a2.Permissions.ACP.TerminalOutput || a2.Permissions.ACP.WaitForTerminal {
		t.Errorf("agent2 expected selected acp permissions override to disable terminal tooling: %#v", a2.Permissions.ACP)
	}

	if !a2.Permissions.ACP.ReadTextFile || !a2.Permissions.ACP.WriteTextFile || !a2.Permissions.ACP.RequestPermission || !a2.Permissions.ACP.KillTerminal || !a2.Permissions.ACP.ReleaseTerminal {
		t.Errorf("agent2 expected unspecified acp permissions to inherit defaults: %#v", a2.Permissions.ACP)
	}

	if len(a2.MCPServers) != 1 || a2.MCPServers[0].Name != "playwright" {
		t.Fatalf("agent2 expected overridden MCP server, got %#v", a2.MCPServers)
	}
}

func TestLoadAgentDefinitionsRejectsInvalidMCPServerConfig(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, "agents")
	os.MkdirAll(agentsDir, 0755)

	os.WriteFile(filepath.Join(agentsDir, "default.json"), []byte(`{"binary":"global-copilot"}`), 0644)
	agentDir := filepath.Join(agentsDir, "tester")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte(`test instructions`), 0644)
	os.WriteFile(filepath.Join(agentDir, "agent.json"), []byte(`{
		"mcpServers":[{"transport":"stdio","name":"broken"}]
	}`), 0644)

	_, err := LoadAgentDefinitions(tmp)
	if err == nil {
		t.Fatal("expected invalid mcp server config to return an error")
	}
}
