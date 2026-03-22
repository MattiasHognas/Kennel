package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestRequestPermissionWithoutAllowOnceReturnsCancelled(t *testing.T) {
	client := &localClient{}

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

func TestWriteTextFileResolvesRelativePathsWithinWorkplace(t *testing.T) {
	workplace := t.TempDir()
	client := &localClient{workplace: workplace}

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

func TestReadTextFileRejectsSymlinkEscape(t *testing.T) {
	workplace := t.TempDir()
	outside := t.TempDir()
	linkedDir := filepath.Join(workplace, "linked")
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink(outside, linkedDir); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("symlink creation unsupported in this environment: %v", err)
		}
		t.Fatalf("Symlink returned error: %v", err)
	}

	client := &localClient{workplace: workplace}
	_, err := client.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{
		Path: filepath.Join("linked", "secret.txt"),
	})
	if err == nil {
		t.Fatal("ReadTextFile returned nil error, want access denied")
	}
}

func TestWaitForTerminalExitRespectsContext(t *testing.T) {
	client := &localClient{workplace: t.TempDir(), terminals: make(map[string]*terminalState)}

	created, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 1"},
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
	client := &localClient{workplace: t.TempDir(), terminals: make(map[string]*terminalState)}

	created, err := client.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf output && exit 7"},
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
