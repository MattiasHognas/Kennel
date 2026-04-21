package workers

import (
	data "MattiasHognas/Kennel/internal/data"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

type fakePromptConnection struct {
	prompt func(context.Context, acpsdk.PromptRequest) (acpsdk.PromptResponse, error)
	cancel func(context.Context, acpsdk.CancelNotification) error
}

func (c *fakePromptConnection) Prompt(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	if c.prompt != nil {
		return c.prompt(ctx, req)
	}
	return acpsdk.PromptResponse{}, nil
}

func (c *fakePromptConnection) Cancel(ctx context.Context, req acpsdk.CancelNotification) error {
	if c.cancel != nil {
		return c.cancel(ctx, req)
	}
	return nil
}

func isUnsupportedSymlinkError(err error) bool {
	if errors.Is(err, os.ErrPermission) {
		return true
	}

	var errno syscall.Errno
	if errors.As(err, &errno) && errno == syscall.Errno(1314) {
		return true
	}

	var linkErr *os.LinkError
	return errors.As(err, &linkErr) && (errors.Is(linkErr.Err, syscall.ENOTSUP) || errors.Is(linkErr.Err, syscall.ENOSYS) || errors.Is(linkErr.Err, os.ErrPermission))
}

func terminalHelperCommand(t *testing.T, mode string, args ...string) (string, []string) {
	t.Helper()

	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable returned error: %v", err)
	}

	commandArgs := []string{"-test.run=TestTerminalHelperProcess", "--", mode}
	commandArgs = append(commandArgs, args...)
	return binary, commandArgs
}

func terminalHelperArgs() []string {
	for i, arg := range os.Args {
		if arg != "--" {
			continue
		}
		if i+1 >= len(os.Args) {
			return nil
		}
		return os.Args[i+1:]
	}

	return nil
}

func TestTerminalHelperProcess(t *testing.T) {
	helperArgs := terminalHelperArgs()
	if len(helperArgs) == 0 {
		return
	}

	switch helperArgs[0] {
	case "sleep":
		if len(helperArgs) != 2 {
			os.Exit(2)
		}
		duration, err := time.ParseDuration(helperArgs[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
		os.Exit(0)
	case "emit-exit":
		if len(helperArgs) != 3 {
			os.Exit(2)
		}
		fmt.Print(helperArgs[1])
		exitCode, err := strconv.Atoi(helperArgs[2])
		if err != nil {
			os.Exit(2)
		}
		os.Exit(exitCode)
	default:
		os.Exit(2)
	}
}

func TestRequestPermissionWithoutAllowOnceReturnsCancelled(t *testing.T) {
	client := &localClient{
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{RequestPermission: true},
		},
	}

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		SessionId: "session",
		ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tool-call"},
		Options: []acpsdk.PermissionOption{{
			OptionId: "reject",
			Name:     "Reject",
			Kind:     acpsdk.PermissionOptionKindRejectOnce,
		}},
	})
	if err != nil {
		t.Fatalf("RequestPermission returned error: %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("outcome = %#v, want cancelled", resp.Outcome)
	}
}

func TestRequestPermissionReturnsCancelledWhenACPToolIsDisabled(t *testing.T) {
	client := &localClient{
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{RequestPermission: false},
		},
	}

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		SessionId: "session",
		ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tool-call"},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
		}},
	})
	if err != nil {
		t.Fatalf("RequestPermission returned error: %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("outcome = %#v, want cancelled", resp.Outcome)
	}
}

func TestRequestPermissionDisabledDoesNotLogAccessDeniedError(t *testing.T) {
	rootDir := t.TempDir()
	client := &localClient{
		topic:  "tester",
		logger: data.NewProjectLogger(rootDir, 5, "ACP Errors"),
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{RequestPermission: false},
		},
	}

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		SessionId: "session",
		ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tool-call"},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
		}},
	})
	if err != nil {
		t.Fatalf("RequestPermission returned error: %v", err)
	}
	if resp.Outcome.Cancelled == nil {
		t.Fatalf("outcome = %#v, want cancelled", resp.Outcome)
	}

	entries, readErr := os.ReadDir(filepath.Join(rootDir, "logs"))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return
		}
		t.Fatalf("ReadDir returned error: %v", readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1", len(entries))
	}

	content, readErr := os.ReadFile(filepath.Join(rootDir, "logs", entries[0].Name()))
	if readErr != nil {
		t.Fatalf("ReadFile returned error: %v", readErr)
	}
	text := string(content)
	if strings.Contains(text, "access denied") || strings.Contains(text, "requestPermission") {
		t.Fatalf("project log unexpectedly contains permission error details:\n%s", text)
	}
}

func TestWrapperPromptAggregatesChunksWithoutProjectChunkLogs(t *testing.T) {
	rootDir := t.TempDir()
	handler := &localClient{}
	wrapper := &Wrapper{
		conn: &fakePromptConnection{
			prompt: func(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
				handler.mu.Lock()
				ch := handler.textChan
				handler.mu.Unlock()
				for _, chunk := range []string{"hello", " ", "world"} {
					ch <- chunk
				}
				return acpsdk.PromptResponse{}, nil
			},
		},
		handler: handler,
		topic:   "tester",
		session: "session",
		logger:  data.NewProjectLogger(rootDir, 7, "Example Project"),
	}

	result, err := wrapper.Prompt(context.Background(), "say hello")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("Prompt result = %q, want %q", result, "hello world")
	}

	entries, err := os.ReadDir(filepath.Join(rootDir, "logs"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("ReadDir returned error: %v", err)
	}

	for _, entry := range entries {
		content, readErr := os.ReadFile(filepath.Join(rootDir, "logs", entry.Name()))
		if readErr != nil {
			t.Fatalf("ReadFile returned error: %v", readErr)
		}
		if strings.Contains(string(content), "OUTPUT_CHUNK") {
			t.Fatalf("project log unexpectedly contains OUTPUT_CHUNK entries:\n%s", string(content))
		}
	}
}

func TestWrapperPromptPrependsAgentInstructions(t *testing.T) {
	handler := &localClient{}
	var seenPrompt acpsdk.PromptRequest
	wrapper := &Wrapper{
		conn: &fakePromptConnection{
			prompt: func(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
				seenPrompt = req
				handler.mu.Lock()
				ch := handler.textChan
				handler.mu.Unlock()
				ch <- "ok"
				return acpsdk.PromptResponse{}, nil
			},
		},
		handler:      handler,
		topic:        "branch-setup",
		session:      "session",
		instructions: "Create or check out the project branch.",
	}

	_, err := wrapper.Prompt(context.Background(), "Task: Initialize branch context")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}

	if len(seenPrompt.Prompt) != 1 {
		t.Fatalf("prompt block count = %d, want 1", len(seenPrompt.Prompt))
	}
	if seenPrompt.Prompt[0].Text == nil {
		t.Fatalf("prompt content = %#v, want text block", seenPrompt.Prompt[0])
	}

	text := seenPrompt.Prompt[0].Text.Text
	if !strings.Contains(text, "Agent instructions:\nCreate or check out the project branch.") {
		t.Fatalf("prompt text = %q, want injected agent instructions", text)
	}
	if !strings.Contains(text, "Task prompt:\nTask: Initialize branch context") {
		t.Fatalf("prompt text = %q, want task prompt section", text)
	}
}

func TestWrapperPromptRetriesOnceAfterEmptyOutput(t *testing.T) {
	handler := &localClient{}
	attempts := 0
	wrapper := &Wrapper{
		conn: &fakePromptConnection{
			prompt: func(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
				attempts++
				if attempts == 1 {
					return acpsdk.PromptResponse{}, nil
				}

				handler.mu.Lock()
				ch := handler.textChan
				handler.mu.Unlock()
				ch <- "review complete"
				return acpsdk.PromptResponse{}, nil
			},
		},
		handler: handler,
		topic:   "code-reviewer",
		session: "session",
	}

	result, err := wrapper.Prompt(context.Background(), "Review the implementation")
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if result != "review complete" {
		t.Fatalf("Prompt result = %q, want %q", result, "review complete")
	}
	if attempts != 2 {
		t.Fatalf("prompt attempts = %d, want 2", attempts)
	}
}

func TestWrapperPromptReturnsErrorWhenAgentProducesNoOutput(t *testing.T) {
	attempts := 0
	wrapper := &Wrapper{
		conn: &fakePromptConnection{
			prompt: func(ctx context.Context, req acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
				attempts++
				return acpsdk.PromptResponse{}, nil
			},
		},
		handler: &localClient{},
		topic:   "planner",
		session: "session",
	}

	_, err := wrapper.Prompt(context.Background(), "Task: Plan the work")
	if err == nil {
		t.Fatal("Prompt returned nil error, want empty-output failure")
	}
	if !strings.Contains(err.Error(), "agent produced no output") {
		t.Fatalf("Prompt error = %v, want empty-output failure", err)
	}
	if attempts != promptEmptyOutputRetryLimit {
		t.Fatalf("prompt attempts = %d, want %d", attempts, promptEmptyOutputRetryLimit)
	}
}

func TestLoadAgentInstructionsTrimsWhitespace(t *testing.T) {
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("\n# system prompt\n\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	instructions, err := loadAgentInstructions(instructionsPath)
	if err != nil {
		t.Fatalf("loadAgentInstructions returned error: %v", err)
	}
	if instructions != "# system prompt" {
		t.Fatalf("instructions = %q, want trimmed content", instructions)
	}
}

func TestNormalizeWorkplacePathReturnsAbsolutePath(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}

	relativeWorkplace, err := filepath.Rel(workingDir, filepath.Join(workingDir, "testdata", "sample-project"))
	if err != nil {
		t.Fatalf("Rel returned error: %v", err)
	}

	resolved, err := normalizeWorkplacePath(relativeWorkplace)
	if err != nil {
		t.Fatalf("normalizeWorkplacePath returned error: %v", err)
	}

	want := filepath.Join(workingDir, "testdata", "sample-project")
	if resolved != want {
		t.Fatalf("resolved workplace = %q, want %q", resolved, want)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("resolved workplace = %q, want absolute path", resolved)
	}
}

func TestWriteTextFileResolvesRelativePathsWithinWorkplace(t *testing.T) {
	workplace := t.TempDir()
	client := &localClient{
		workplace: workplace,
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{ReadTextFile: true, WriteTextFile: true},
		},
	}

	_, err := client.WriteTextFile(context.Background(), acpsdk.WriteTextFileRequest{
		Path:    "todo.txt",
		Content: "hello",
	})
	if err != nil {
		t.Fatalf("WriteTextFile returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(workplace, "todo.txt"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q, want %q", string(content), "hello")
	}
}

func TestWriteTextFileBlocksWhenACPToolIsDisabled(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{WriteTextFile: false},
		},
	}

	_, err := client.WriteTextFile(context.Background(), acpsdk.WriteTextFileRequest{
		Path:    "todo.txt",
		Content: "hello",
	})
	if err == nil {
		t.Fatal("WriteTextFile returned nil error, want acp tool denial")
	}
	if !strings.Contains(err.Error(), "writeTextFile") {
		t.Fatalf("WriteTextFile error = %v, want writeTextFile denial", err)
	}
}

func TestReadTextFileRejectsSymlinkEscape(t *testing.T) {
	workplace := t.TempDir()
	outside := t.TempDir()
	linkedDir := filepath.Join(workplace, "linked")
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(outside, linkedDir); err != nil {
		if isUnsupportedSymlinkError(err) {
			t.Skipf("symlink creation unsupported in this environment: %v", err)
		}
		t.Fatalf("Symlink returned error: %v", err)
	}

	client := &localClient{
		workplace: workplace,
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{ReadTextFile: true},
		},
	}
	_, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{
		Path: filepath.Join("linked", "secret.txt"),
	})
	if err == nil {
		t.Fatal("ReadTextFile returned nil error, want access denied")
	}
}

func TestWaitForTerminalExitRespectsContext(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		terminals: make(map[string]*terminalState),
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{CreateTerminal: true, KillTerminal: true, WaitForTerminal: true},
		},
	}
	command, args := terminalHelperCommand(t, "sleep", "1s")

	created, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{
		Command: command,
		Args:    args,
	})
	if err != nil {
		t.Fatalf("CreateTerminal returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = client.WaitForTerminalExit(ctx, acpsdk.WaitForTerminalExitRequest{TerminalId: created.TerminalId})
	if err == nil {
		t.Fatal("WaitForTerminalExit returned nil error, want context deadline exceeded")
	}

	if _, err := client.KillTerminal(context.Background(), acpsdk.KillTerminalRequest{TerminalId: created.TerminalId}); err != nil {
		t.Fatalf("KillTerminal returned error: %v", err)
	}
}

func TestWaitForTerminalExitUsesRecordedExitStatusAndReleaseRemovesTerminal(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		terminals: make(map[string]*terminalState),
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{CreateTerminal: true, TerminalOutput: true, ReleaseTerminal: true, WaitForTerminal: true},
		},
	}
	command, args := terminalHelperCommand(t, "emit-exit", "output", "7")

	created, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{
		Command: command,
		Args:    args,
	})
	if err != nil {
		t.Fatalf("CreateTerminal returned error: %v", err)
	}

	resp, err := client.WaitForTerminalExit(context.Background(), acpsdk.WaitForTerminalExitRequest{TerminalId: created.TerminalId})
	if err != nil {
		t.Fatalf("WaitForTerminalExit returned error: %v", err)
	}
	if resp.ExitCode == nil || *resp.ExitCode != 7 {
		t.Fatalf("exit code = %#v, want 7", resp.ExitCode)
	}

	output, err := client.TerminalOutput(context.Background(), acpsdk.TerminalOutputRequest{TerminalId: created.TerminalId})
	if err != nil {
		t.Fatalf("TerminalOutput returned error: %v", err)
	}
	if output.Output != "output" {
		t.Fatalf("output = %q, want %q", output.Output, "output")
	}

	if _, err := client.ReleaseTerminal(context.Background(), acpsdk.ReleaseTerminalRequest{TerminalId: created.TerminalId}); err != nil {
		t.Fatalf("ReleaseTerminal returned error: %v", err)
	}
	if _, err := client.TerminalOutput(context.Background(), acpsdk.TerminalOutputRequest{TerminalId: created.TerminalId}); err == nil {
		t.Fatal("TerminalOutput returned nil error after release, want invalid terminal ID")
	}
}

func TestBuildMCPServersMapsTransportsAndMetadata(t *testing.T) {
	servers, err := buildMCPServers([]data.MCPServer{
		{
			Transport: "stdio",
			Name:      "playwright",
			Command:   "npx",
			Args:      []string{"@playwright/mcp@latest"},
			Env:       map[string]string{"DEBUG": "1"},
		},
		{
			Transport: "http",
			Name:      "language",
			URL:       "https://mcp.example.test/http",
			Headers:   map[string]string{"Authorization": "Bearer token"},
		},
	})
	if err != nil {
		t.Fatalf("buildMCPServers returned error: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("server count = %d, want 2", len(servers))
	}
	if servers[0].Stdio == nil || servers[0].Stdio.Name != "playwright" || servers[0].Stdio.Command != "npx" {
		t.Fatalf("stdio server = %#v, want playwright stdio config", servers[0])
	}
	if len(servers[0].Stdio.Env) != 1 || servers[0].Stdio.Env[0].Name != "DEBUG" || servers[0].Stdio.Env[0].Value != "1" {
		t.Fatalf("stdio env = %#v, want DEBUG=1", servers[0].Stdio.Env)
	}
	if servers[1].Http == nil || servers[1].Http.Type != "http" || servers[1].Http.Url != "https://mcp.example.test/http" {
		t.Fatalf("http server = %#v, want mapped http config", servers[1])
	}
	if len(servers[1].Http.Headers) != 1 || servers[1].Http.Headers[0].Name != "Authorization" {
		t.Fatalf("http headers = %#v, want Authorization header", servers[1].Http.Headers)
	}
}

func TestBuildMCPServersReturnsEmptyArrayForMissingConfig(t *testing.T) {
	servers, err := buildMCPServers(nil)
	if err != nil {
		t.Fatalf("buildMCPServers returned error: %v", err)
	}
	if servers == nil {
		t.Fatal("buildMCPServers returned nil slice, want empty slice")
	}
	if len(servers) != 0 {
		t.Fatalf("server count = %d, want 0", len(servers))
	}
}

func TestBuildMCPServersUsesEmptyArraysForOptionalMetadata(t *testing.T) {
	servers, err := buildMCPServers([]data.MCPServer{
		{
			Transport: "stdio",
			Name:      "context7",
			Command:   "npx",
		},
		{
			Transport: "http",
			Name:      "remote",
			URL:       "https://mcp.example.test/http",
		},
		{
			Transport: "sse",
			Name:      "events",
			URL:       "https://mcp.example.test/sse",
		},
	})
	if err != nil {
		t.Fatalf("buildMCPServers returned error: %v", err)
	}
	if len(servers) != 3 {
		t.Fatalf("server count = %d, want 3", len(servers))
	}
	if servers[0].Stdio == nil {
		t.Fatalf("stdio server = %#v, want stdio config", servers[0])
	}
	if servers[0].Stdio.Args == nil {
		t.Fatal("stdio args = nil, want empty slice")
	}
	if servers[0].Stdio.Env == nil {
		t.Fatal("stdio env = nil, want empty slice")
	}
	if len(servers[0].Stdio.Args) != 0 {
		t.Fatalf("stdio args length = %d, want 0", len(servers[0].Stdio.Args))
	}
	if len(servers[0].Stdio.Env) != 0 {
		t.Fatalf("stdio env length = %d, want 0", len(servers[0].Stdio.Env))
	}
	if servers[1].Http == nil {
		t.Fatalf("http server = %#v, want http config", servers[1])
	}
	if servers[1].Http.Headers == nil {
		t.Fatal("http headers = nil, want empty slice")
	}
	if len(servers[1].Http.Headers) != 0 {
		t.Fatalf("http headers length = %d, want 0", len(servers[1].Http.Headers))
	}
	if servers[2].Sse == nil {
		t.Fatalf("sse server = %#v, want sse config", servers[2])
	}
	if servers[2].Sse.Headers == nil {
		t.Fatal("sse headers = nil, want empty slice")
	}
	if len(servers[2].Sse.Headers) != 0 {
		t.Fatalf("sse headers length = %d, want 0", len(servers[2].Sse.Headers))
	}
}

func TestCreateTerminalBlocksWhenACPToolIsDisabled(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{CreateTerminal: false},
		},
	}

	_, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{Command: "git", Args: []string{"status"}})
	if err == nil {
		t.Fatal("CreateTerminal returned nil error, want acp tool denial")
	}
	if !strings.Contains(err.Error(), "createTerminal") {
		t.Fatalf("CreateTerminal error = %v, want createTerminal denial", err)
	}
}

func TestTerminalOutputBlocksWhenACPToolIsDisabled(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		terminals: map[string]*terminalState{"123": {}},
		permissions: data.PermissionsConfig{
			ACP: data.ACPPermissions{TerminalOutput: false},
		},
	}

	_, err := client.TerminalOutput(context.Background(), acpsdk.TerminalOutputRequest{TerminalId: "123"})
	if err == nil {
		t.Fatal("TerminalOutput returned nil error, want acp tool denial")
	}
	if !strings.Contains(err.Error(), "terminalOutput") {
		t.Fatalf("TerminalOutput error = %v, want terminalOutput denial", err)
	}
}

func TestReadTextFileBlocksGitMetadataWhenGitVisibilityRestricted(t *testing.T) {
	workplace := t.TempDir()
	gitPath := filepath.Join(workplace, ".git")
	if err := os.MkdirAll(gitPath, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitPath, "HEAD"), []byte("ref: refs/heads/main"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	client := &localClient{
		workplace: workplace,
		permissions: data.PermissionsConfig{
			Git: data.GitPermissions{Status: false, Diff: false, History: false},
			ACP: data.ACPPermissions{ReadTextFile: true},
		},
	}

	_, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{Path: filepath.Join(".git", "HEAD")})
	if err == nil {
		t.Fatal("ReadTextFile returned nil error, want git metadata denial")
	}
	if !strings.Contains(err.Error(), "git metadata is hidden") {
		t.Fatalf("ReadTextFile error = %v, want git metadata denial", err)
	}
}

func TestCreateTerminalBlocksRestrictedGitCommands(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		permissions: data.PermissionsConfig{
			Git: data.GitPermissions{Status: true, Diff: false, History: false},
			ACP: data.ACPPermissions{CreateTerminal: true},
		},
	}

	_, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{Command: "git", Args: []string{"log", "--oneline"}})
	if err == nil {
		t.Fatal("CreateTerminal returned nil error for git log, want access denied")
	}
	if !strings.Contains(err.Error(), "git history access is disabled") {
		t.Fatalf("git log error = %v, want history denial", err)
	}

	_, err = client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{Command: "git", Args: []string{"diff"}})
	if err == nil {
		t.Fatal("CreateTerminal returned nil error for git diff, want access denied")
	}
	if !strings.Contains(err.Error(), "git diff access is disabled") {
		t.Fatalf("git diff error = %v, want diff denial", err)
	}

	err = client.checkTerminalPermissions("git", []string{"status"})
	if err != nil {
		t.Fatalf("checkTerminalPermissions returned error for allowed git status: %v", err)
	}
}

func TestCreateTerminalBlocksGitInsideShellScript(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		permissions: data.PermissionsConfig{
			Git: data.GitPermissions{Status: false, Diff: false, History: false},
			ACP: data.ACPPermissions{CreateTerminal: true},
		},
		terminals: make(map[string]*terminalState),
	}

	_, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{
		Command: "pwsh",
		Args:    []string{"-NoProfile", "-Command", "git status"},
	})
	if err == nil {
		t.Fatal("CreateTerminal returned nil error for shell git invocation, want access denied")
	}
	if !strings.Contains(err.Error(), "git status access is disabled") {
		t.Fatalf("shell git error = %v, want status denial", err)
	}
}
