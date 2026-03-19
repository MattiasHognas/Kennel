package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type AgentDefinition struct {
	Name             string       `json:"name"`
	InstructionsPath string       `json:"instructionsPath"`
	LaunchConfig     LaunchConfig `json:"launchConfig"`
}

type LaunchConfig struct {
	Binary string            `json:"binary"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
}

func LoadAgentDefinitions(rootDir string) ([]AgentDefinition, error) {
	agentsDir := filepath.Join(rootDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents directory: %w", err)
	}

	var definitions []AgentDefinition

	// Default Config
	var defaultConfig LaunchConfig
	defaultConfigPath := filepath.Join(agentsDir, "copilot.json")
	if data, err := os.ReadFile(defaultConfigPath); err == nil {
		if err := json.Unmarshal(data, &defaultConfig); err != nil {
			return nil, fmt.Errorf("parse global copilot.json: %w", err)
		}
	} else {
		defaultConfig = LaunchConfig{
			Binary: "copilot",
			Args:   []string{"--acp"},
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		agentName := entry.Name()
		agentPath := filepath.Join(agentsDir, agentName)
		instructionsPath := filepath.Join(agentPath, "instructions.md")

		// Ensure instructions.md exists
		if _, err := os.Stat(instructionsPath); os.IsNotExist(err) {
			continue
		}

		launchConfig := defaultConfig
		agentJSONPath := filepath.Join(agentPath, "agent.json")
		if data, err := os.ReadFile(agentJSONPath); err == nil {
			if err := json.Unmarshal(data, &launchConfig); err != nil {
				return nil, fmt.Errorf("parse agent.json for %s: %w", agentName, err)
			}
		}

		definitions = append(definitions, AgentDefinition{
			Name:             agentName,
			InstructionsPath: instructionsPath,
			LaunchConfig:     launchConfig,
		})
	}

	return definitions, nil
}
