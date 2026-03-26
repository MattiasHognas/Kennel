package data

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var writeMu sync.Mutex

type ProjectLogger struct {
	rootDir   string
	projectID int64
	name      string
	runID     string
}

func NewProjectLogger(rootDir string, projectID int64, projectName string) *ProjectLogger {
	return &ProjectLogger{
		rootDir:   rootDir,
		projectID: projectID,
		name:      projectName,
		runID:     time.Now().UTC().Format("20060102T150405.000000000Z"),
	}
}

func (l *ProjectLogger) LogProject(eventType, message string) {
	if l == nil {
		return
	}
	l.writeProjectBlock(eventType, message)
}

func (l *ProjectLogger) LogAgentCreated(agentName string) {
	l.LogAgentEvent(agentName, "AGENT_CREATED", "agent created")
}

func (l *ProjectLogger) LogAgentState(agentName, state string) {
	l.LogAgentEvent(agentName, "STATE", state)
}

func (l *ProjectLogger) LogAgentActivity(agentName, activity string) {
	l.LogAgentEvent(agentName, "ACTIVITY", activity)
}

func (l *ProjectLogger) LogAgentInput(agentName, input string) {
	l.LogAgentEvent(agentName, "INPUT", input)
}

func (l *ProjectLogger) LogAgentOutput(agentName, output string) {
	l.LogAgentEvent(agentName, "OUTPUT", output)
}

func (l *ProjectLogger) LogAgentError(agentName, message string) {
	l.LogAgentEvent(agentName, "ERROR", message)
}

func (l *ProjectLogger) LogProjectError(message string) {
	l.LogProject("PROJECT_ERROR", message)
}

func (l *ProjectLogger) LogAgentEvent(agentName, eventType, message string) {
	if l == nil {
		return
	}
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		agentName = "unknown-agent"
	}

	prefix := fmt.Sprintf("agent=%s", agentName)
	l.writeProjectBlock(eventType, prefix+"\n"+message)
}

func (l *ProjectLogger) writeProjectBlock(eventType, message string) {
	_ = l.appendBlock(l.projectLogPath(), eventType, message)
}

func (l *ProjectLogger) appendBlock(path, eventType, message string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	timestamp := time.Now().Format(time.RFC3339)
	trimmedMessage := strings.TrimSpace(message)
	if trimmedMessage == "" {
		trimmedMessage = "(empty)"
	}

	_, err = fmt.Fprintf(f, "[%s] %s\n%s\n\n", timestamp, eventType, trimmedMessage)
	return err
}

func (l *ProjectLogger) projectLogPath() string {
	name := projectSlug(l.projectID, l.name)
	if strings.TrimSpace(l.runID) != "" {
		name += "-" + sanitizeFileName(l.runID)
	}
	return filepath.Join(l.rootLogDir(), name+".log")
}

func (l *ProjectLogger) rootLogDir() string {
	rootDir := strings.TrimSpace(l.rootDir)
	if rootDir == "" {
		rootDir = "."
	}
	return filepath.Join(rootDir, "logs")
}

func projectSlug(projectID int64, name string) string {
	slug := sanitizeFileName(name)
	if slug == "" {
		slug = "project"
	}
	if projectID > 0 {
		return fmt.Sprintf("project-%d-%s", projectID, slug)
	}
	return fmt.Sprintf("project-%s", slug)
}

func sanitizeFileName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == ' ', r == '.':
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}

	return strings.Trim(builder.String(), "-")
}
