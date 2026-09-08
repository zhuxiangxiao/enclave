package unboxexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ExecRequest represents a command execution request from inside the sandbox.
type ExecRequest struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Dir     string            `json:"dir"`
	Timeout int               `json:"timeout"`
}

// ExecResponse represents the result of a command execution.
type ExecResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error"`
}

const defaultTimeout = 60 // seconds

// Server manages the unboxexec Unix Domain Socket daemon lifecycle.
type Server struct {
	SocketPath      string
	PIDPath         string
	AllowedCommands []*regexp.Regexp

	listener net.Listener
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	doneChan chan struct{}
	stopped  bool
}

// NewServer creates a new Server instance.
func NewServer(sockPath string, allowedCommands []*regexp.Regexp) *Server {
	return &Server{
		SocketPath:      sockPath,
		PIDPath:         sockPath + ".pid",
		AllowedCommands: allowedCommands,
		doneChan:        make(chan struct{}),
	}
}

// IsServerRunning checks if a daemon is actively listening on the given socket path.
func IsServerRunning(sockPath string) bool {
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		return false
	}

	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		return true
	}
	return false
}

// Start starts listening on the Unix domain socket.
// It detects and cleans up stale socket/PID files if the server is not active.
func (s *Server) Start(parentCtx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.SocketPath == "" {
		return errors.New("socket path is required")
	}

	// Check if server is already running
	if IsServerRunning(s.SocketPath) {
		return fmt.Errorf("server is already running on %s", s.SocketPath)
	}

	// Clean up stale socket and PID file if present
	_ = os.Remove(s.SocketPath)
	if s.PIDPath != "" {
		_ = os.Remove(s.PIDPath)
	}

	// Ensure parent directory exists
	if dir := filepath.Dir(s.SocketPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create socket directory %s: %w", dir, err)
		}
	}

	listener, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.SocketPath, err)
	}
	s.listener = listener

	// Write PID file
	if s.PIDPath != "" {
		pidStr := fmt.Sprintf("%d\n", os.Getpid())
		_ = os.WriteFile(s.PIDPath, []byte(pidStr), 0644)
	}

	if parentCtx == nil {
		parentCtx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(parentCtx)

	// Start accept loop
	go s.acceptLoop()

	// Monitor context cancellation
	go func() {
		<-s.ctx.Done()
		s.Stop()
	}()

	return nil
}

// Stop gracefully shuts down the server and cleans up the socket file and PID file.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true

	if s.cancel != nil {
		s.cancel()
	}

	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}

	_ = os.Remove(s.SocketPath)
	if s.PIDPath != "" {
		_ = os.Remove(s.PIDPath)
	}

	s.mu.Unlock()
	return err
}

// Wait blocks until the server accept loop terminates.
func (s *Server) Wait() {
	<-s.doneChan
}

func (s *Server) acceptLoop() {
	defer close(s.doneChan)
	defer func() {
		_ = os.Remove(s.SocketPath)
		if s.PIDPath != "" {
			_ = os.Remove(s.PIDPath)
		}
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
		}
		go handleConnection(s.ctx, conn, s.AllowedCommands)
	}
}

// StartDaemon starts a Unix Domain Socket server that accepts command execution
// requests. It runs in a goroutine and stops when ctx is cancelled.
// Kept for backward compatibility with existing callers.
func StartDaemon(ctx context.Context, sockPath string, allowedCommands []*regexp.Regexp) error {
	srv := NewServer(sockPath, allowedCommands)
	return srv.Start(ctx)
}

// handleConnection processes a single request on the connection.
func handleConnection(ctx context.Context, conn net.Conn, allowedCommands []*regexp.Regexp) {
	defer conn.Close()

	var req ExecRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		resp := ExecResponse{Error: fmt.Sprintf("failed to decode request: %v", err)}
		_ = json.NewEncoder(conn).Encode(resp)
		return
	}

	resp := executeCommand(ctx, &req, allowedCommands)
	_ = json.NewEncoder(conn).Encode(resp)
}

// validateCommand checks whether the command is allowed by the configured patterns.
// It joins the command and args with spaces, then checks against each pattern.
func validateCommand(req *ExecRequest, allowedCommands []*regexp.Regexp) error {
	if len(allowedCommands) == 0 {
		return fmt.Errorf("command not allowed: no allowed_commands configured")
	}

	cmdStr := req.Command
	if len(req.Args) > 0 {
		cmdStr = cmdStr + " " + strings.Join(req.Args, " ")
	}

	for _, re := range allowedCommands {
		if re.MatchString(cmdStr) {
			return nil
		}
	}

	return fmt.Errorf("command not allowed: %q does not match any allowed pattern", cmdStr)
}

// executeCommand runs the requested command and returns the response.
func executeCommand(ctx context.Context, req *ExecRequest, allowedCommands []*regexp.Regexp) ExecResponse {
	if req.Command == "" {
		return ExecResponse{Error: "command is required"}
	}

	if err := validateCommand(req, allowedCommands); err != nil {
		return ExecResponse{Error: err.Error()}
	}

	if req.Dir != "" {
		info, err := os.Stat(req.Dir)
		if err != nil {
			return ExecResponse{Error: fmt.Sprintf("working directory does not exist on host: %s", req.Dir)}
		}
		if !info.IsDir() {
			return ExecResponse{Error: fmt.Sprintf("working directory path is not a directory on host: %s", req.Dir)}
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, req.Command, req.Args...)

	// Set environment variables
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	resp := ExecResponse{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			resp.ExitCode = -1
			resp.Error = err.Error()
		}
	}

	return resp
}
