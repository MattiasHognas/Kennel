# Kennel

Kennel is a framework for orchestrating AI agents.

### Development
Run tests
```go
go test ./...
```
Code coverage
```go
go test -coverprofile="coverage.out" ./...
go tool cover -html="coverage.out"
```

## Features to come

### As a user you get
- Project configuration
- Agent orchestration
- Visualization of feedback & evaluation
- Real time monitoring
- Configure guardrails
- Declare tool usage
- Configure feedback loops

### Tool functionality
- Automatic handover between agents
- Automatic creation of agents
- Automatic destruction of agents
- Automatic reporting of agent status and progress
- Automatic feedback loop