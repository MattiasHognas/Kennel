package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectLoggerWritesSingleProjectFile(t *testing.T) {
	rootDir := t.TempDir()
	logger := NewProjectLogger(rootDir, 7, "Example Project")

	logger.LogProject("PROJECT_START", "started")
	logger.LogAgentInput("tester", "run tests")
	logger.LogAgentOutput("tester", "all green")

	logRoot := filepath.Join(rootDir, "logs")
	entries, err := os.ReadDir(logRoot)
	if err != nil {
		t.Fatalf("ReadDir returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1 project log file", len(entries))
	}
	if entries[0].IsDir() {
		t.Fatalf("log entry %q is a directory, want a single log file", entries[0].Name())
	}

	content, err := os.ReadFile(filepath.Join(logRoot, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	text := string(content)
	for _, fragment := range []string{"PROJECT_START", "INPUT", "OUTPUT", "agent=tester", "run tests", "all green"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("project log missing %q:\n%s", fragment, text)
		}
	}
}

func TestProjectLoggerWritesExplicitErrorEntries(t *testing.T) {
	rootDir := t.TempDir()
	logger := NewProjectLogger(rootDir, 7, "Example Project")

	logger.LogProjectError("project blew up")
	logger.LogAgentError("tester", "agent blew up")

	entries, err := os.ReadDir(filepath.Join(rootDir, "logs"))
	if err != nil {
		t.Fatalf("ReadDir returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1", len(entries))
	}

	content, err := os.ReadFile(filepath.Join(rootDir, "logs", entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	text := string(content)
	for _, fragment := range []string{"PROJECT_ERROR", "ERROR", "project blew up", "agent=tester", "agent blew up"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("project log missing %q:\n%s", fragment, text)
		}
	}
}
