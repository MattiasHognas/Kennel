package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	eventbus "MattiasHognas/Kennel/internal/events"

	"github.com/coder/acp-go-sdk"
)

type Client interface {
	Prompt(ctx context.Context, msg string) (string, error)
	Close() error
}

type FakeClient struct {
	Response string
}

func (f *FakeClient) Prompt(ctx context.Context, msg string) (string, error) {
	return f.Response, nil
}

func (f *FakeClient) Close() error { return nil }

type Wrapper struct {
	cmd     *exec.Cmd
	conn    *acp.ClientSideConnection
	handler *localClient
	eb      *eventbus.EventBus
	topic   string
	session acp.SessionId
}

func NewWrapper(ctx context.Context, binary string, args []string, eb *eventbus.EventBus, workplace string, topic string) (*Wrapper, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workplace

	inw, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}

	outr, err := cmd.StdoutPipe()
	if err != nil {
		inw.Close()
		return nil, fmt.Errorf("stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		inw.Close()
		return nil, fmt.Errorf("start: %w", err)
	}

	handler := &localClient{
		eb:        eb,
		topic:     topic,
		workplace: workplace,
		terminals: make(map[string]*terminalState),
	}
	conn := acp.NewClientSideConnection(handler, inw, outr)

	_, err = conn.Initialize(ctx, acp.InitializeRequest{
		ClientInfo: &acp.Implementation{
			Name:    "Kennel",
			Version: "1.0",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}

	sessionRes, err := conn.NewSession(ctx, acp.NewSessionRequest{McpServers: []acp.McpServer{}})
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}

	return &Wrapper{
		cmd:     cmd,
		conn:    conn,
		handler: handler,
		eb:      eb,
		topic:   topic,
		session: sessionRes.SessionId,
	}, nil
}

func (w *Wrapper) Prompt(ctx context.Context, msg string) (string, error) {
	textChan := make(chan string, 100)

	w.handler.mu.Lock()
	// Channel to signal and stream text chunks
	w.handler.textChan = textChan
	w.handler.mu.Unlock()

	errChan := make(chan error, 1)

	go func() {
		_, err := w.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: w.session,
			Prompt: []acp.ContentBlock{
				acp.TextBlock(msg),
			},
		})
		// The acp-go-sdk signals termination here by returning from Prompt.
		errChan <- err

		w.handler.mu.Lock()
		w.handler.textChan = nil
		w.handler.mu.Unlock()

		close(textChan)
	}()

	var sb strings.Builder
	// Block and aggregate chunks until the agent finishes processing
	for chunk := range textChan {
		sb.WriteString(chunk)
	}

	err := <-errChan
	if err != nil {
		return "", err
	}

	result := sb.String()
	if result == "" {
		return "Prompt sent successfully", nil
	}
	return result, nil
}

func (w *Wrapper) Close() error {
	w.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: w.session})
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
	return nil
}

func FormatPrompt(msg string, activities []string) string {
	if len(activities) == 0 {
		return msg
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- Resume context (last %d activities) ---\n", len(activities)))
	for _, a := range activities {
		sb.WriteString(a + "\n")
	}
	sb.WriteString("--- End resume context ---\n")
	sb.WriteString(msg)
	return sb.String()
}

type terminalState struct {
	cmd      *exec.Cmd
	mu       sync.Mutex
	buf      strings.Builder
	done     chan struct{}
	waitErr  error
	exitCode *int
}

func (t *terminalState) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Write(p)
}

func (t *terminalState) ReadAndClear() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	res := t.buf.String()
	t.buf.Reset()
	return res
}

func (t *terminalState) recordExit(waitErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.waitErr = waitErr
	if t.cmd != nil && t.cmd.ProcessState != nil {
		exitCode := t.cmd.ProcessState.ExitCode()
		t.exitCode = &exitCode
	}
	close(t.done)
}

func (t *terminalState) exitResult() (*int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, t.waitErr
}

type localClient struct {
	mu        sync.Mutex
	eb        *eventbus.EventBus
	topic     string
	workplace string
	textChan  chan string

	terminalsMu sync.Mutex
	terminals   map[string]*terminalState
}

func (c *localClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	c.eb.Publish(c.topic, eventbus.Event{Payload: eventbus.WorkerMessageEvent{Chunk: "received update"}})
	if params.Update.AgentMessageChunk != nil {
		if textBlock := params.Update.AgentMessageChunk.Content.Text; textBlock != nil {
			c.mu.Lock()
			ch := c.textChan
			c.mu.Unlock()
			if ch != nil {
				func() {
					defer func() { _ = recover() }()
					ch <- textBlock.Text
				}()
			}
		}
	}
	return nil
}

func (c *localClient) checkInWorkplace(targetPath string) (string, error) {
	absWorkplace, err := filepath.Abs(c.workplace)
	if err != nil {
		return "", err
	}
	resolvedWorkplace, err := filepath.EvalSymlinks(absWorkplace)
	if err != nil {
		return "", err
	}

	resolvedTarget := targetPath
	if !filepath.IsAbs(resolvedTarget) {
		resolvedTarget = filepath.Join(resolvedWorkplace, resolvedTarget)
	}
	resolvedTarget, err = filepath.Abs(resolvedTarget)
	if err != nil {
		return "", err
	}

	if evalTarget, evalErr := filepath.EvalSymlinks(resolvedTarget); evalErr == nil {
		resolvedTarget = evalTarget
	} else if !os.IsNotExist(evalErr) {
		return "", evalErr
	} else {
		// Target (or some parent) does not exist. Walk up to the deepest existing
		// ancestor directory, resolve its symlinks, and then reconstruct the full path.
		originalTarget := resolvedTarget
		dir := filepath.Dir(originalTarget)

		var (
			resolvedAncestor string
			ancestorErr      error
		)

		for {
			resolvedAncestor, ancestorErr = filepath.EvalSymlinks(dir)
			if ancestorErr == nil {
				break
			}
			if !os.IsNotExist(ancestorErr) {
				return "", ancestorErr
			}
			nextDir := filepath.Dir(dir)
			if nextDir == dir {
				// Reached filesystem root without finding an existing ancestor.
				return "", ancestorErr
			}
			dir = nextDir
		}

		remaining, relErr := filepath.Rel(dir, originalTarget)
		if relErr != nil {
			return "", relErr
		}

		resolvedTarget = filepath.Join(resolvedAncestor, remaining)
	}

	relPath, err := filepath.Rel(resolvedWorkplace, resolvedTarget)
	if err != nil {
		return "", err
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path is outside of workspace")
	}

	return resolvedTarget, nil
}

func (c *localClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	resolvedPath, err := c.checkInWorkplace(params.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("access denied: %w", err)
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("failed to read file: %w", err)
	}
	return acp.ReadTextFileResponse{Content: string(content)}, nil
}

func (c *localClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	resolvedPath, err := c.checkInWorkplace(params.Path)
	if err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("access denied: %w", err)
	}
	err = os.WriteFile(resolvedPath, []byte(params.Content), 0644)
	if err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("failed to write file: %w", err)
	}
	return acp.WriteTextFileResponse{}, nil
}

func (c *localClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.RequestPermissionResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	var permissionOption *acp.PermissionOption
	for _, option := range params.Options {
		if option.Kind == acp.PermissionOptionKindAllowOnce {
			permissionOption = &option
			break
		}
	}
	if permissionOption == nil {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(permissionOption.OptionId)}, nil
}

func (c *localClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	terminalCmd := exec.Command(params.Command, params.Args...)
	terminalCmd.Dir = c.workplace
	state := &terminalState{cmd: terminalCmd, done: make(chan struct{})}
	terminalCmd.Stdout = state
	terminalCmd.Stderr = state
	err = terminalCmd.Start()
	if err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("failed to start terminal command: %w", err)
	}
	termID := fmt.Sprintf("%d", terminalCmd.Process.Pid)
	c.terminalsMu.Lock()
	c.terminals[termID] = state
	c.terminalsMu.Unlock()
	go func() {
		state.recordExit(terminalCmd.Wait())
	}()
	return acp.CreateTerminalResponse{TerminalId: termID}, nil
}

func (c *localClient) KillTerminalCommand(ctx context.Context, params acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.KillTerminalCommandResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	c.terminalsMu.Lock()
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.KillTerminalCommandResponse{}, fmt.Errorf("invalid terminal ID")
	}
	if state.cmd.Process != nil {
		_ = state.cmd.Process.Kill()
	}
	c.terminalsMu.Lock()
	delete(c.terminals, params.TerminalId)
	c.terminalsMu.Unlock()
	return acp.KillTerminalCommandResponse{}, nil
}

func (c *localClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.TerminalOutputResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	c.terminalsMu.Lock()
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.TerminalOutputResponse{}, fmt.Errorf("invalid terminal ID")
	}
	out := state.ReadAndClear()
	return acp.TerminalOutputResponse{Output: out}, nil
}

func (c *localClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	c.terminalsMu.Lock()
	_, exists := c.terminals[params.TerminalId]
	if exists {
		delete(c.terminals, params.TerminalId)
	}
	c.terminalsMu.Unlock()
	if !exists {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("invalid terminal ID")
	}
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *localClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	c.terminalsMu.Lock()
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("invalid terminal ID")
	}

	select {
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	case <-state.done:
		exitCode, waitErr := state.exitResult()
		if exitCode != nil {
			return acp.WaitForTerminalExitResponse{ExitCode: exitCode}, nil
		}
		if waitErr != nil {
			return acp.WaitForTerminalExitResponse{}, waitErr
		}
	}
	return acp.WaitForTerminalExitResponse{}, nil
}
