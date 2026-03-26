package acp

import (
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

	"MattiasHognas/Kennel/internal/discovery"

	acpsdk "github.com/coder/acp-go-sdk"
)

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
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{RequestPermission: true},
		},
	}

	resp, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		SessionId: "session",
		ToolCall:  acpsdk.RequestPermissionToolCall{ToolCallId: "tool-call"},
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

func TestRequestPermissionBlocksWhenACPToolIsDisabled(t *testing.T) {
	client := &localClient{
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{RequestPermission: false},
		},
	}

	_, err := client.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
		SessionId: "session",
		ToolCall:  acpsdk.RequestPermissionToolCall{ToolCallId: "tool-call"},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
		}},
	})
	if err == nil {
		t.Fatal("RequestPermission returned nil error, want acp tool denial")
	}
	if !strings.Contains(err.Error(), "requestPermission") {
		t.Fatalf("RequestPermission error = %v, want requestPermission denial", err)
	}
}

func TestWriteTextFileResolvesRelativePathsWithinWorkplace(t *testing.T) {
	workplace := t.TempDir()
	client := &localClient{
		workplace: workplace,
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{ReadTextFile: true, WriteTextFile: true},
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
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{WriteTextFile: false},
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
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{ReadTextFile: true},
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
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{CreateTerminal: true, KillTerminal: true, WaitForTerminal: true},
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

	if _, err := client.KillTerminalCommand(context.Background(), acpsdk.KillTerminalCommandRequest{TerminalId: created.TerminalId}); err != nil {
		t.Fatalf("KillTerminalCommand returned error: %v", err)
	}
}

func TestWaitForTerminalExitUsesRecordedExitStatusAndReleaseRemovesTerminal(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		terminals: make(map[string]*terminalState),
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{CreateTerminal: true, TerminalOutput: true, ReleaseTerminal: true, WaitForTerminal: true},
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
	servers, err := buildMCPServers([]discovery.MCPServer{
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

func TestCreateTerminalBlocksWhenACPToolIsDisabled(t *testing.T) {
	client := &localClient{
		workplace: t.TempDir(),
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{CreateTerminal: false},
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
		permissions: discovery.PermissionsConfig{
			ACP: discovery.ACPPermissions{TerminalOutput: false},
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
		permissions: discovery.PermissionsConfig{
			Git: discovery.GitPermissions{Status: false, Diff: false, History: false},
			ACP: discovery.ACPPermissions{ReadTextFile: true},
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
		permissions: discovery.PermissionsConfig{
			Git: discovery.GitPermissions{Status: true, Diff: false, History: false},
			ACP: discovery.ACPPermissions{CreateTerminal: true},
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
		permissions: discovery.PermissionsConfig{
			Git: discovery.GitPermissions{Status: false, Diff: false, History: false},
			ACP: discovery.ACPPermissions{CreateTerminal: true},
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
