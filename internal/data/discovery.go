package data

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

type AgentDefinition struct {
	Name             string            `json:"name"`
	InstructionsPath string            `json:"instructionsPath"`
	LaunchConfig     LaunchConfig      `json:"launchConfig"`
	MCPServers       []MCPServer       `json:"mcpServers,omitempty"`
	PromptContext    PromptContext     `json:"promptContext,omitempty"`
	Permissions      PermissionsConfig `json:"permissions,omitempty"`
}

type LaunchConfig struct {
	Binary string            `json:"binary"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
}

type MCPServer struct {
	Transport string            `json:"transport"`
	Name      string            `json:"name"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type PromptContext struct {
	PreviousOutput bool `json:"previousOutput"`
}

type PermissionsConfig struct {
	Git GitPermissions `json:"git"`
	ACP ACPPermissions `json:"acp"`
}

type GitPermissions struct {
	Status  bool `json:"status"`
	Diff    bool `json:"diff"`
	History bool `json:"history"`
}

type ACPPermissions struct {
	ReadTextFile      bool `json:"readTextFile"`
	WriteTextFile     bool `json:"writeTextFile"`
	RequestPermission bool `json:"requestPermission"`
	CreateTerminal    bool `json:"createTerminal"`
	KillTerminal      bool `json:"killTerminal"`
	TerminalOutput    bool `json:"terminalOutput"`
	ReleaseTerminal   bool `json:"releaseTerminal"`
	WaitForTerminal   bool `json:"waitForTerminal"`
}

type agentConfigFile struct {
	LaunchConfig
	MCPServers    []MCPServer       `json:"mcpServers,omitempty"`
	PromptContext PromptContext     `json:"promptContext,omitempty"`
	Permissions   PermissionsConfig `json:"permissions,omitempty"`
}

func defaultAgentConfig() agentConfigFile {
	return agentConfigFile{
		PromptContext: PromptContext{PreviousOutput: true},
		Permissions: PermissionsConfig{
			Git: GitPermissions{
				Status:  true,
				Diff:    true,
				History: true,
			},
			ACP: ACPPermissions{
				ReadTextFile:      true,
				WriteTextFile:     true,
				RequestPermission: true,
				CreateTerminal:    true,
				KillTerminal:      true,
				TerminalOutput:    true,
				ReleaseTerminal:   true,
				WaitForTerminal:   true,
			},
		},
	}
}

func cloneAgentConfig(config agentConfigFile) agentConfigFile {
	cloned := agentConfigFile{
		LaunchConfig: LaunchConfig{
			Binary: config.LaunchConfig.Binary,
			Args:   slices.Clone(config.LaunchConfig.Args),
			Env:    cloneStringMap(config.LaunchConfig.Env),
		},
		PromptContext: config.PromptContext,
		Permissions:   config.Permissions,
	}

	if len(config.MCPServers) == 0 {
		return cloned
	}

	cloned.MCPServers = make([]MCPServer, 0, len(config.MCPServers))
	for _, server := range config.MCPServers {
		cloned.MCPServers = append(cloned.MCPServers, MCPServer{
			Transport: server.Transport,
			Name:      server.Name,
			Command:   server.Command,
			Args:      slices.Clone(server.Args),
			Env:       cloneStringMap(server.Env),
			URL:       server.URL,
			Headers:   cloneStringMap(server.Headers),
		})
	}

	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}

	return cloned
}

func validateAgentConfig(agentName string, config agentConfigFile) error {
	for index, server := range config.MCPServers {
		switch server.Transport {
		case "stdio":
			if server.Command == "" {
				return fmt.Errorf("agent %s mcpServers[%d] transport stdio requires command", agentName, index)
			}
		case "http", "sse":
			if server.URL == "" {
				return fmt.Errorf("agent %s mcpServers[%d] transport %s requires url", agentName, index, server.Transport)
			}
		default:
			return fmt.Errorf("agent %s mcpServers[%d] has unsupported transport %q", agentName, index, server.Transport)
		}

		if server.Name == "" {
			return fmt.Errorf("agent %s mcpServers[%d] requires name", agentName, index)
		}
	}

	return nil
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
	defaultConfig := defaultAgentConfig()
	defaultConfigPath := filepath.Join(agentsDir, "default.json")
	if data, err := os.ReadFile(defaultConfigPath); err == nil {
		if err := json.Unmarshal(data, &defaultConfig); err != nil {
			return nil, fmt.Errorf("parse default.json: %w", err)
		}
		if err := validateAgentConfig("default", defaultConfig); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("read default.json: %w", err)
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

		agentConfig := cloneAgentConfig(defaultConfig)
		agentJSONPath := filepath.Join(agentPath, "agent.json")
		if data, err := os.ReadFile(agentJSONPath); err == nil {
			if err := json.Unmarshal(data, &agentConfig); err != nil {
				return nil, fmt.Errorf("parse agent.json for %s: %w", agentName, err)
			}
			if err := validateAgentConfig(agentName, agentConfig); err != nil {
				return nil, err
			}
		}

		definitions = append(definitions, AgentDefinition{
			Name:             agentName,
			InstructionsPath: instructionsPath,
			LaunchConfig:     agentConfig.LaunchConfig,
			MCPServers:       agentConfig.MCPServers,
			PromptContext:    agentConfig.PromptContext,
			Permissions:      agentConfig.Permissions,
		})
	}

	return definitions, nil
}
