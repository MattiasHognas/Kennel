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
		return nil, fmt.Errorf("stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
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
	cmd *exec.Cmd
	mu  sync.Mutex
	buf strings.Builder
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

func (c *localClient) checkInWorkplace(targetPath string) error {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	absWorkplace, err := filepath.Abs(c.workplace)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Clean(absTarget)+string(filepath.Separator), filepath.Clean(absWorkplace)+string(filepath.Separator)) {
		return fmt.Errorf("path is outside of workspace")
	}
	return nil
}

func (c *localClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("validation failed: %w", err)
	}
	if err := c.checkInWorkplace(params.Path); err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("access denied: %w", err)
	}
	content, err := os.ReadFile(params.Path)
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
	if err := c.checkInWorkplace(params.Path); err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("access denied: %w", err)
	}
	err = os.WriteFile(params.Path, []byte(params.Content), 0644)
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
	if permissionOption == nil && len(params.Options) > 0 {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, fmt.Errorf("no allow once permission option found")
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
	state := &terminalState{cmd: terminalCmd}
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
		_ = terminalCmd.Wait()
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
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("invalid terminal ID")
	}
	if state.cmd.Process != nil {
		_ = state.cmd.Process.Release()
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
	if state.cmd.Process != nil {
		_ = state.cmd.Wait()
		if state.cmd.ProcessState != nil {
			exitCode := state.cmd.ProcessState.ExitCode()
			return acp.WaitForTerminalExitResponse{ExitCode: &exitCode}, nil
		}
	}
	return acp.WaitForTerminalExitResponse{}, nil
}
