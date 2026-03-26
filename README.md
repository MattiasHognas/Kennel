# Kennel

Kennel is a terminal-based framework for orchestrating AI agents. It provides a
TUI (Terminal User Interface) that lets you configure projects, assign agents to
them and watch the agents work through a planned set of tasks in real time.

## Overview

Kennel uses the [Agent Control Protocol (ACP)](https://github.com/coder/acp-go-sdk)
to communicate with AI coding agents. When a project is started the built-in
supervisor:

1. Asks a **planner** agent to decompose the project instructions into a
   structured execution plan.
2. Runs a **branch-setup** agent to initialise the repository / branch context.
3. Executes the planned tasks across one or more parallel streams, launching
   the appropriate agents (backend-developer, frontend-developer, tester, …)
   via ACP.
4. Persists every state change and activity to a local SQLite database so runs
   can be resumed after a restart.

## Architecture

```
agents/              – Agent definitions (one directory per agent with instructions.md)
cmd/app/             – Application entry point (Bubble Tea TUI)
data/                – SQLite database file (projects, agents, activities)
internal/
  acp/               – ACP SDK wrapper for launching and communicating with agents
  data/              – SQLite repository (projects, agents, activities)
  discovery/         – Discovers agent definitions from the agents/ directory
  events/            – In-process pub/sub event bus
  logic/             – TUI model, lifecycle management, editor, activity tracking
  supervisor/        – Orchestration engine that plans and executes agent tasks
  ui/                – Terminal styles and a custom table widget
  ui/table/          – Custom table widget with support for multiple independent tables and keyboard navigation
  workers/           – Agent abstraction and state machine
```

## Prerequisites

- **Go 1.26+**
- An ACP-compatible agent binary (the default configuration expects a `copilot`
  binary on `PATH` — see `agents/cli.json`).

## Getting Started

```bash
# Clone the repository
git clone https://github.com/MattiasHognas/Kennel.git
cd Kennel

# Build the application
go build -o kennel ./cmd/app

# Run
./kennel
```

On first launch a sample project is seeded into the local SQLite database
(`data/kennel.db`). Use the TUI to edit the project name, workplace path, and
instructions before starting it.

## Usage

### Keyboard shortcuts

| Key              | Action                                      |
|------------------|---------------------------------------------|
| `tab` / `→`      | Move focus to the next table                |
| `shift+tab` / `←`| Move focus to the previous table             |
| `enter`          | Edit the selected project                   |
| `space`          | Cycle the selected project/agent state      |
| `s`              | Start the selected project or agent         |
| `p`              | Stop the selected project or agent          |
| `esc` / `q`      | Quit                                        |

### Project editor

Press `enter` on a project row to open the editor. Fill in the **Name**,
**Workplace** (absolute path to the target repository) and **Instructions**,
then tab to `[ OK ]` and press `enter` to save.

## Agents

Agents are discovered from the `agents/` directory at the location of the
executable. Each agent is a sub-directory containing an `instructions.md` file.
An optional `agent.json` can override the default launch configuration.

### Pre-configured agents

| Agent               | Role                                         |
|---------------------|----------------------------------------------|
| `branch-setup`      | Creates/checks out project branches          |
| `planner`           | Decomposes instructions into an execution plan |
| `backend-developer` | Implements backend features, APIs, data models |
| `frontend-developer`| Implements frontend features and components  |
| `code-reviewer`     | Reviews and validates code changes           |
| `tester`            | Creates and runs test suites                 |
| `devops`            | Manages build scripts, CI/CD, deployment     |
| `docs-writer`       | Creates and updates documentation            |

### Adding a custom agent

1. Create a new directory under `agents/`, e.g. `agents/my-agent/`.
2. Add an `instructions.md` with the agent's system prompt.
3. Optionally add an `agent.json` to override the launch binary/args.

## Development

### Build

```bash
go build ./cmd/app
```

### Run tests

```bash
go test ./...
```

### Code coverage

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Debugging

VS Code launch configurations are included in `.vscode/launch.json` for
debugging with `dlv-dap`.

## Roadmap

- Configure guardrails for agent actions
- Declare and restrict tool usage per agent
- Configurable feedback loops between agents
- Real-time visualisation of feedback and evaluation results
- Automatic creation and destruction of agents based on project needs

## License

[MIT](LICENSE) — Copyright © 2026 Mattias Högnäs