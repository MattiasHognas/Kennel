package acp

import (
	"context"
	"fmt"
	"os/exec"
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

func NewWrapper(ctx context.Context, binary string, args []string, eb *eventbus.EventBus, topic string) (*Wrapper, error) {
	cmd := exec.CommandContext(ctx, binary, args...)

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

	handler := &localClient{eb: eb, topic: topic}
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

type localClient struct {
	mu       sync.Mutex
	eb       *eventbus.EventBus
	topic    string
	textChan chan string
}

func (c *localClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	c.eb.Publish(c.topic, eventbus.Event{Payload: eventbus.WorkerMessageEvent{Chunk: "received update"}})

	// Send text chunks to the waiting channel
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

func (c *localClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, nil
}
func (c *localClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}
func (c *localClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}
func (c *localClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, nil
}
func (c *localClient) KillTerminalCommand(ctx context.Context, params acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {
	return acp.KillTerminalCommandResponse{}, nil
}
func (c *localClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}
func (c *localClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}
func (c *localClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}
