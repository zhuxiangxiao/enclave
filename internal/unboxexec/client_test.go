package unboxexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// allowAll returns a permissive pattern that matches any command.
func allowAll() []*regexp.Regexp {
	return []*regexp.Regexp{regexp.MustCompile(".*")}
}

func TestServerStopCleansSocketAndRejectsDuplicate(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := Start(ctx, sockPath, allowAll())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket was not created: %v", err)
	}
	if _, err := Start(context.Background(), sockPath, allowAll()); err == nil {
		t.Fatal("expected duplicate server start to fail")
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	server.Wait()
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("socket was not removed after stop: %v", err)
	}
}

func TestServerStderrExitCodeTimeoutAndContinuesServing(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := StartDaemon(ctx, sockPath, allowAll()); err != nil {
		t.Fatal(err)
	}

	failed, err := SendRequest(sockPath, &ExecRequest{Command: "sh", Args: []string{"-c", "echo error >&2; exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Stderr != "error\n" || failed.ExitCode != 3 || failed.Error != "" {
		t.Fatalf("unexpected failed-command response: %#v", failed)
	}

	timedOut, err := SendRequest(sockPath, &ExecRequest{Command: "sleep", Args: []string{"100"}, Timeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	if timedOut.ExitCode != -1 || !strings.Contains(timedOut.Error, "timed out") {
		t.Fatalf("expected timeout response, got %#v", timedOut)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{Command: "echo", Args: []string{"still-serving"}})
	if err != nil || resp.Stdout != "still-serving\n" {
		t.Fatalf("daemon did not continue serving: response=%#v err=%v", resp, err)
	}
}

func TestServerHandlesConcurrentClients(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := StartDaemon(ctx, sockPath, allowAll()); err != nil {
		t.Fatal(err)
	}

	const clients = 12
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := range clients {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := fmt.Sprintf("client-%d", i)
			resp, err := SendRequest(sockPath, &ExecRequest{Command: "echo", Args: []string{value}})
			if err != nil || resp.Stdout != value+"\n" || resp.Stderr != "" {
				errs <- fmt.Errorf("client %d: response=%#v err=%v", i, resp, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestSendRequest(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := StartDaemon(ctx, sockPath, allowAll()); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}
	if resp.Stdout != "hello\n" {
		t.Errorf("expected stdout %q, got %q", "hello\n", resp.Stdout)
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestSendRequestWithDir(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := StartDaemon(ctx, sockPath, allowAll()); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "pwd",
		Dir:     os.TempDir(),
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestSendRequestWithEnv(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := StartDaemon(ctx, sockPath, allowAll()); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "echo $TEST_VAR"},
		Env:     map[string]string{"TEST_VAR": "myvalue"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}
	if resp.Stdout != "myvalue\n" {
		t.Errorf("expected stdout %q, got %q", "myvalue\n", resp.Stdout)
	}
}

func TestSendRequestNoCommand(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := StartDaemon(ctx, sockPath, allowAll()); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.Error == "" {
		t.Error("expected error for empty command")
	}
}

func TestSendRequestConnectionError(t *testing.T) {
	_, err := SendRequest("/tmp/nonexistent.sock", &ExecRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err == nil {
		t.Error("expected error for nonexistent socket")
	}
}

func TestCommandAllowed(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	allowed := []*regexp.Regexp{regexp.MustCompile("^echo")}
	if err := StartDaemon(ctx, sockPath, allowed); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.Error != "" {
		t.Errorf("expected no error, got: %s", resp.Error)
	}
	if resp.Stdout != "hello\n" {
		t.Errorf("expected stdout %q, got %q", "hello\n", resp.Stdout)
	}
}

func TestCommandRejected(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	allowed := []*regexp.Regexp{regexp.MustCompile("^playwright")}
	if err := StartDaemon(ctx, sockPath, allowed); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.Error == "" {
		t.Error("expected error for rejected command")
	}
}

func TestCommandRejectedNoPatterns(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := StartDaemon(ctx, sockPath, nil); err != nil {
		t.Fatalf("failed to start daemon: %v", err)
	}

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.Error == "" {
		t.Error("expected error for empty allowed_commands")
	}
}
