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
	"regexp"
	"strings"
	"syscall"
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

// Server owns the lifecycle of a unboxexec Unix socket server.
// It may be used by a long-lived host proxy or by enclave run.
type Server struct {
	listener net.Listener
	sockPath string
	done     chan struct{}
}

// Start starts a server at sockPath. A live socket is never removed: callers
// receive an error instead. A stale Unix socket is removed before listening.
func Start(ctx context.Context, sockPath string, allowedCommands []*regexp.Regexp) (*Server, error) {
	if err := prepareSocket(sockPath); err != nil {
		return nil, err
	}

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		listener.Close()
		os.Remove(sockPath)
		return nil, fmt.Errorf("failed to secure socket %s: %w", sockPath, err)
	}

	s := &Server{listener: listener, sockPath: sockPath, done: make(chan struct{})}
	go s.serve(ctx, allowedCommands)
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return s, nil
}

func prepareSocket(sockPath string) error {
	info, err := os.Lstat(sockPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to inspect socket %s: %w", sockPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %s", sockPath)
	}

	conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return fmt.Errorf("unboxexec daemon is already running at %s", sockPath)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot determine whether socket %s is live: %w", sockPath, err)
	}
	if err := os.Remove(sockPath); err != nil {
		return fmt.Errorf("failed to remove stale socket %s: %w", sockPath, err)
	}
	return nil
}

func (s *Server) serve(ctx context.Context, allowedCommands []*regexp.Regexp) {
	defer close(s.done)
	defer s.listener.Close()
	defer os.Remove(s.sockPath)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go handleConnection(ctx, conn, allowedCommands)
	}
}

// Stop stops accepting new connections and removes the socket after the accept
// loop exits. It is safe to call more than once.
func (s *Server) Stop() error {
	err := s.listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Wait blocks until the server has stopped and its socket has been removed.
func (s *Server) Wait() { <-s.done }

// StartDaemon starts a Unix Domain Socket server that accepts command execution
// requests. It runs in a goroutine and stops when ctx is cancelled.
// The socket file is cleaned up on shutdown.
// allowedCommands specifies regex patterns that the command string must match.
// If allowedCommands is empty, all commands are rejected.
func StartDaemon(ctx context.Context, sockPath string, allowedCommands []*regexp.Regexp) error {
	_, err := Start(ctx, sockPath, allowedCommands)
	return err
}

// handleConnection processes a single request on the connection.
func handleConnection(ctx context.Context, conn net.Conn, allowedCommands []*regexp.Regexp) {
	defer conn.Close()

	var req ExecRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		resp := ExecResponse{Error: fmt.Sprintf("failed to decode request: %v", err)}
		json.NewEncoder(conn).Encode(resp)
		return
	}

	resp := executeCommand(ctx, &req, allowedCommands)
	json.NewEncoder(conn).Encode(resp)
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

	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		resp.ExitCode = -1
		resp.Error = fmt.Sprintf("command timed out after %d seconds", timeout)
	} else if err != nil {
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
