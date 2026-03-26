package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"MattiasHognas/Kennel/internal/discovery"
	eventbus "MattiasHognas/Kennel/internal/events"
	logging "MattiasHognas/Kennel/internal/logging"

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
	logger  *logging.ProjectLogger
}

func logAndWrapAgentError(logger *logging.ProjectLogger, agentName, prefix string, err error) error {
	wrapped := fmt.Errorf("%s: %w", prefix, err)
	if logger != nil {
		logger.LogAgentError(agentName, wrapped.Error())
	}
	return wrapped
}

func logAndReturnAgentError(logger *logging.ProjectLogger, agentName, message string) error {
	err := errors.New(message)
	if logger != nil {
		logger.LogAgentError(agentName, err.Error())
	}
	return err
}

func NewWrapper(ctx context.Context, definition discovery.AgentDefinition, eb *eventbus.EventBus, workplace string, topic string) (*Wrapper, error) {
	cmd := exec.CommandContext(ctx, definition.LaunchConfig.Binary, definition.LaunchConfig.Args...)
	cmd.Dir = workplace
	cmd.Env = appendCommandEnv(definition.LaunchConfig.Env)

	inw, err := cmd.StdinPipe()
	if err != nil {
		return nil, logAndWrapAgentError(nil, topic, "stdin", err)
	}

	outr, err := cmd.StdoutPipe()
	if err != nil {
		inw.Close()
		return nil, logAndWrapAgentError(nil, topic, "stdout", err)
	}

	if err := cmd.Start(); err != nil {
		inw.Close()
		return nil, logAndWrapAgentError(nil, topic, "start", err)
	}

	handler := &localClient{
		eb:          eb,
		topic:       topic,
		workplace:   workplace,
		permissions: definition.Permissions,
		terminals:   make(map[string]*terminalState),
	}
	conn := acp.NewClientSideConnection(handler, inw, outr)

	_, err = conn.Initialize(ctx, acp.InitializeRequest{
		ClientInfo: &acp.Implementation{
			Name:    "Kennel",
			Version: "1.0",
		},
	})
	if err != nil {
		return nil, logAndWrapAgentError(nil, topic, "init", err)
	}

	mcpServers, err := buildMCPServers(definition.MCPServers)
	if err != nil {
		return nil, logAndWrapAgentError(nil, topic, "build mcp servers", err)
	}

	sessionRes, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: workplace, McpServers: mcpServers})
	if err != nil {
		return nil, logAndWrapAgentError(nil, topic, "session", err)
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
		if w.logger != nil && strings.TrimSpace(chunk) != "" {
			w.logger.LogAgentEvent(w.topic, "OUTPUT_CHUNK", chunk)
		}
	}

	err := <-errChan
	if err != nil {
		return "", logAndWrapAgentError(w.logger, w.topic, "prompt", err)
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

func appendCommandEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}

	env := os.Environ()
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}

	return env
}

func buildMCPServers(configs []discovery.MCPServer) ([]acp.McpServer, error) {
	if len(configs) == 0 {
		return []acp.McpServer{}, nil
	}

	servers := make([]acp.McpServer, 0, len(configs))
	for _, config := range configs {
		switch config.Transport {
		case "stdio":
			servers = append(servers, acp.McpServer{
				Stdio: &acp.McpServerStdio{
					Name:    config.Name,
					Command: config.Command,
					Args:    append([]string(nil), config.Args...),
					Env:     mapEnvVariables(config.Env),
				},
			})
		case "http":
			servers = append(servers, acp.McpServer{
				Http: &acp.McpServerHttp{
					Name:    config.Name,
					Type:    "http",
					Url:     config.URL,
					Headers: mapHTTPHeaders(config.Headers),
				},
			})
		case "sse":
			servers = append(servers, acp.McpServer{
				Sse: &acp.McpServerSse{
					Name:    config.Name,
					Type:    "sse",
					Url:     config.URL,
					Headers: mapHTTPHeaders(config.Headers),
				},
			})
		default:
			return nil, logAndReturnAgentError(nil, "mcp", fmt.Sprintf("unsupported mcp transport %q", config.Transport))
		}
	}

	return servers, nil
}

func mapEnvVariables(values map[string]string) []acp.EnvVariable {
	if len(values) == 0 {
		return nil
	}

	keys := sortedKeys(values)
	result := make([]acp.EnvVariable, 0, len(keys))
	for _, key := range keys {
		result = append(result, acp.EnvVariable{Name: key, Value: values[key]})
	}

	return result
}

func mapHTTPHeaders(values map[string]string) []acp.HttpHeader {
	if len(values) == 0 {
		return nil
	}

	keys := sortedKeys(values)
	result := make([]acp.HttpHeader, 0, len(keys))
	for _, key := range keys {
		result = append(result, acp.HttpHeader{Name: key, Value: values[key]})
	}

	return result
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type terminalState struct {
	cmd      *exec.Cmd
	mu       sync.Mutex
	buf      strings.Builder
	done     chan struct{}
	waitErr  error
	exitCode *int
	logger   *logging.ProjectLogger
	agent    string
}

func (t *terminalState) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	n, err = t.buf.Write(p)
	t.mu.Unlock()

	if t.logger != nil && len(p) > 0 {
		t.logger.LogAgentEvent(t.agent, "TERMINAL_OUTPUT", string(p))
	}

	return n, err
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
	if t.logger != nil {
		message := "terminal completed"
		if t.exitCode != nil {
			message = fmt.Sprintf("terminal exit code=%d", *t.exitCode)
		}
		if waitErr != nil {
			message = fmt.Sprintf("terminal error=%v", waitErr)
		}
		t.logger.LogAgentEvent(t.agent, "TERMINAL_EXIT", message)
	}
	close(t.done)
}

func (t *terminalState) exitResult() (*int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, t.waitErr
}

type localClient struct {
	mu          sync.Mutex
	eb          *eventbus.EventBus
	topic       string
	workplace   string
	textChan    chan string
	permissions discovery.PermissionsConfig
	logger      *logging.ProjectLogger

	terminalsMu sync.Mutex
	terminals   map[string]*terminalState
}

func (c *localClient) failWrap(prefix string, err error) error {
	return logAndWrapAgentError(c.logger, c.topic, prefix, err)
}

func (c *localClient) fail(message string) error {
	return logAndReturnAgentError(c.logger, c.topic, message)
}

func (w *Wrapper) SetLogger(logger *logging.ProjectLogger) {
	w.logger = logger
	if w.handler != nil {
		w.handler.logger = logger
	}
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
		return "", c.fail("path is outside of workspace")
	}

	return resolvedTarget, nil
}

func (c *localClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.ReadTextFileResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolReadTextFile); err != nil {
		return acp.ReadTextFileResponse{}, c.failWrap("access denied", err)
	}
	resolvedPath, err := c.checkInWorkplace(params.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, c.failWrap("access denied", err)
	}
	if err := c.checkPathPermissions(resolvedPath); err != nil {
		return acp.ReadTextFileResponse{}, c.failWrap("access denied", err)
	}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return acp.ReadTextFileResponse{}, c.failWrap("failed to read file", err)
	}
	return acp.ReadTextFileResponse{Content: string(content)}, nil
}

func (c *localClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.WriteTextFileResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolWriteTextFile); err != nil {
		return acp.WriteTextFileResponse{}, c.failWrap("access denied", err)
	}
	resolvedPath, err := c.checkInWorkplace(params.Path)
	if err != nil {
		return acp.WriteTextFileResponse{}, c.failWrap("access denied", err)
	}
	if err := c.checkPathPermissions(resolvedPath); err != nil {
		return acp.WriteTextFileResponse{}, c.failWrap("access denied", err)
	}
	err = os.WriteFile(resolvedPath, []byte(params.Content), 0644)
	if err != nil {
		return acp.WriteTextFileResponse{}, c.failWrap("failed to write file", err)
	}
	return acp.WriteTextFileResponse{}, nil
}

func (c *localClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.RequestPermissionResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolRequestPermission); err != nil {
		return acp.RequestPermissionResponse{}, c.failWrap("access denied", err)
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
		return acp.CreateTerminalResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolCreateTerminal); err != nil {
		return acp.CreateTerminalResponse{}, c.failWrap("access denied", err)
	}
	if err := c.checkTerminalPermissions(params.Command, params.Args); err != nil {
		return acp.CreateTerminalResponse{}, c.failWrap("access denied", err)
	}
	terminalCmd := exec.Command(params.Command, params.Args...)
	terminalCmd.Dir = c.workplace
	state := &terminalState{cmd: terminalCmd, done: make(chan struct{}), logger: c.logger, agent: c.topic}
	terminalCmd.Stdout = state
	terminalCmd.Stderr = state
	err = terminalCmd.Start()
	if err != nil {
		return acp.CreateTerminalResponse{}, c.failWrap("failed to start terminal command", err)
	}
	if c.logger != nil {
		commandLine := strings.TrimSpace(strings.Join(append([]string{params.Command}, params.Args...), " "))
		c.logger.LogAgentEvent(c.topic, "TERMINAL_START", commandLine)
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
		return acp.KillTerminalCommandResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolKillTerminal); err != nil {
		return acp.KillTerminalCommandResponse{}, c.failWrap("access denied", err)
	}
	c.terminalsMu.Lock()
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.KillTerminalCommandResponse{}, c.fail("invalid terminal ID")
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
		return acp.TerminalOutputResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolTerminalOutput); err != nil {
		return acp.TerminalOutputResponse{}, c.failWrap("access denied", err)
	}
	c.terminalsMu.Lock()
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.TerminalOutputResponse{}, c.fail("invalid terminal ID")
	}
	out := state.ReadAndClear()
	return acp.TerminalOutputResponse{Output: out}, nil
}

func (c *localClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.ReleaseTerminalResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolReleaseTerminal); err != nil {
		return acp.ReleaseTerminalResponse{}, c.failWrap("access denied", err)
	}
	c.terminalsMu.Lock()
	_, exists := c.terminals[params.TerminalId]
	if exists {
		delete(c.terminals, params.TerminalId)
	}
	c.terminalsMu.Unlock()
	if !exists {
		return acp.ReleaseTerminalResponse{}, c.fail("invalid terminal ID")
	}
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *localClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	err := params.Validate()
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, c.failWrap("validation failed", err)
	}
	if err := c.checkACPToolPermission(acpToolWaitForTerminal); err != nil {
		return acp.WaitForTerminalExitResponse{}, c.failWrap("access denied", err)
	}
	c.terminalsMu.Lock()
	state, exists := c.terminals[params.TerminalId]
	c.terminalsMu.Unlock()
	if !exists {
		return acp.WaitForTerminalExitResponse{}, c.fail("invalid terminal ID")
	}

	select {
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, c.failWrap("wait for terminal exit", ctx.Err())
	case <-state.done:
		exitCode, waitErr := state.exitResult()
		if exitCode != nil {
			return acp.WaitForTerminalExitResponse{ExitCode: exitCode}, nil
		}
		if waitErr != nil {
			return acp.WaitForTerminalExitResponse{}, c.failWrap("wait for terminal exit", waitErr)
		}
	}
	return acp.WaitForTerminalExitResponse{}, nil
}

func (c *localClient) checkPathPermissions(resolvedPath string) error {
	if c.permissions.Git.Status && c.permissions.Git.Diff && c.permissions.Git.History {
		return nil
	}

	relPath, err := filepath.Rel(c.workplace, resolvedPath)
	if err != nil {
		return err
	}

	segments := strings.Split(filepath.Clean(relPath), string(filepath.Separator))
	for _, segment := range segments {
		if segment == ".git" {
			return fmt.Errorf("git metadata is hidden for this agent")
		}
	}

	return nil
}

type acpToolPermission string

const (
	acpToolReadTextFile      acpToolPermission = "readTextFile"
	acpToolWriteTextFile     acpToolPermission = "writeTextFile"
	acpToolRequestPermission acpToolPermission = "requestPermission"
	acpToolCreateTerminal    acpToolPermission = "createTerminal"
	acpToolKillTerminal      acpToolPermission = "killTerminal"
	acpToolTerminalOutput    acpToolPermission = "terminalOutput"
	acpToolReleaseTerminal   acpToolPermission = "releaseTerminal"
	acpToolWaitForTerminal   acpToolPermission = "waitForTerminal"
)

func (c *localClient) checkACPToolPermission(tool acpToolPermission) error {
	allowed := false

	switch tool {
	case acpToolReadTextFile:
		allowed = c.permissions.ACP.ReadTextFile
	case acpToolWriteTextFile:
		allowed = c.permissions.ACP.WriteTextFile
	case acpToolRequestPermission:
		allowed = c.permissions.ACP.RequestPermission
	case acpToolCreateTerminal:
		allowed = c.permissions.ACP.CreateTerminal
	case acpToolKillTerminal:
		allowed = c.permissions.ACP.KillTerminal
	case acpToolTerminalOutput:
		allowed = c.permissions.ACP.TerminalOutput
	case acpToolReleaseTerminal:
		allowed = c.permissions.ACP.ReleaseTerminal
	case acpToolWaitForTerminal:
		allowed = c.permissions.ACP.WaitForTerminal
	default:
		return fmt.Errorf("unknown acp tool permission %q", tool)
	}

	if allowed {
		return nil
	}

	return fmt.Errorf("acp tool %s is disabled for this agent", tool)
}

func (c *localClient) checkTerminalPermissions(command string, args []string) error {
	git := c.permissions.Git
	if git.Status && git.Diff && git.History {
		return nil
	}

	classifications := classifyGitInvocations(command, args)
	for _, class := range classifications {
		switch class {
		case gitCommandStatus:
			if !git.Status {
				return fmt.Errorf("git status access is disabled for this agent")
			}
		case gitCommandDiff:
			if !git.Diff {
				return fmt.Errorf("git diff access is disabled for this agent")
			}
		case gitCommandHistory, gitCommandUnknown:
			if !git.History {
				return fmt.Errorf("git history access is disabled for this agent")
			}
		}
	}

	return nil
}

type gitCommandClass string

const (
	gitCommandStatus  gitCommandClass = "status"
	gitCommandDiff    gitCommandClass = "diff"
	gitCommandHistory gitCommandClass = "history"
	gitCommandUnknown gitCommandClass = "unknown"
)

var gitShellInvocationPattern = regexp.MustCompile(`(?i)(?:^|[^\w])git(?:\.exe)?\s+((?:--?[\w-]+(?:\s+[^;&|]+)?\s+)*)?([\w-]+)`)

func classifyGitInvocations(command string, args []string) []gitCommandClass {
	base := strings.ToLower(filepath.Base(command))
	if runtime.GOOS == "windows" {
		base = strings.TrimSuffix(base, ".exe")
	}

	if base == "git" {
		subcommand := extractGitSubcommand(args)
		if subcommand == "" {
			return []gitCommandClass{gitCommandUnknown}
		}
		return []gitCommandClass{classifyGitSubcommand(subcommand)}
	}

	if !isShellCommand(base) {
		return nil
	}

	joined := strings.Join(args, " ")
	matches := gitShellInvocationPattern.FindAllStringSubmatch(joined, -1)
	if len(matches) == 0 {
		return nil
	}

	classifications := make([]gitCommandClass, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			classifications = append(classifications, gitCommandUnknown)
			continue
		}
		classifications = append(classifications, classifyGitSubcommand(match[2]))
	}

	return classifications
}

func extractGitSubcommand(args []string) string {
	flagsWithValue := map[string]struct{}{
		"-c":          {},
		"-C":          {},
		"--exec-path": {},
		"--git-dir":   {},
		"--work-tree": {},
	}

	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "" {
			continue
		}
		if _, ok := flagsWithValue[arg]; ok {
			index++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}

	return ""
}

func classifyGitSubcommand(subcommand string) gitCommandClass {
	switch strings.ToLower(strings.TrimSpace(subcommand)) {
	case "status":
		return gitCommandStatus
	case "diff", "difftool":
		return gitCommandDiff
	case "log", "show", "blame", "reflog", "rev-list", "shortlog", "whatchanged", "rev-parse", "show-ref", "cat-file", "ls-tree":
		return gitCommandHistory
	default:
		return gitCommandUnknown
	}
}

func isShellCommand(base string) bool {
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "cmd", "powershell", "pwsh", "bash", "sh", "zsh":
		return true
	default:
		return false
	}
}
