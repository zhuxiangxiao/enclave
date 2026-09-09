package unboxexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestServerLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "server.sock")

	srv := NewServer(sockPath, allowAll())

	if IsServerRunning(sockPath) {
		t.Fatal("expected server not to be running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	if !IsServerRunning(sockPath) {
		t.Fatal("expected server to be running")
	}

	// Verify PID file exists
	pidPath := sockPath + ".pid"
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Errorf("expected PID file to exist at %s", pidPath)
	}

	// Verify duplicate start returns error
	srv2 := NewServer(sockPath, allowAll())
	if err := srv2.Start(ctx); err == nil {
		t.Fatal("expected duplicate start to fail")
	}

	// Stop server
	if err := srv.Stop(); err != nil {
		t.Fatalf("failed to stop server: %v", err)
	}
	srv.Wait()

	if IsServerRunning(sockPath) {
		t.Fatal("expected server to be stopped")
	}

	// Verify socket and PID file are cleaned up
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("expected socket file to be removed")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("expected PID file to be removed")
	}
}

func TestStaleSocketCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "stale.sock")
	pidPath := sockPath + ".pid"

	// Create dummy stale socket and pid file
	if err := os.WriteFile(sockPath, []byte("stale"), 0644); err != nil {
		t.Fatalf("failed to write dummy socket: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte("999999"), 0644); err != nil {
		t.Fatalf("failed to write dummy pid: %v", err)
	}

	if IsServerRunning(sockPath) {
		t.Fatal("expected dummy file not to be recognized as running server")
	}

	srv := NewServer(sockPath, allowAll())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server over stale socket: %v", err)
	}

	if !IsServerRunning(sockPath) {
		t.Fatal("expected server to start after stale socket cleanup")
	}

	_ = srv.Stop()
	srv.Wait()
}

func TestInvalidDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "dir.sock")

	srv := NewServer(sockPath, allowAll())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		_ = srv.Stop()
		srv.Wait()
	}()

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "pwd",
		Dir:     filepath.Join(tmpDir, "nonexistent-dir"),
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.Error == "" {
		t.Fatal("expected error for nonexistent working directory")
	}
}

func TestStderrAndExitCode(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "stderr.sock")

	srv := NewServer(sockPath, allowAll())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		_ = srv.Stop()
		srv.Wait()
	}()

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "echo error_msg >&2; exit 3"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.ExitCode != 3 {
		t.Errorf("expected exit code 3, got %d", resp.ExitCode)
	}
	if resp.Stderr != "error_msg\n" {
		t.Errorf("expected stderr %q, got %q", "error_msg\n", resp.Stderr)
	}
}

func TestCommandTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "timeout.sock")

	srv := NewServer(sockPath, allowAll())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		_ = srv.Stop()
		srv.Wait()
	}()

	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "sleep",
		Args:    []string{"10"},
		Timeout: 1, // 1 second timeout
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.ExitCode == 0 && resp.Error == "" {
		t.Fatal("expected command to fail or be cancelled due to timeout")
	}

	// Ensure server continues servicing other requests after timeout
	resp2, err := SendRequest(sockPath, &ExecRequest{
		Command: "echo",
		Args:    []string{"ok"},
	})
	if err != nil {
		t.Fatalf("subsequent SendRequest failed: %v", err)
	}
	if resp2.Stdout != "ok\n" {
		t.Errorf("expected stdout %q, got %q", "ok\n", resp2.Stdout)
	}
}

func TestConcurrentRequests(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "concurrent.sock")

	srv := NewServer(sockPath, allowAll())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		_ = srv.Stop()
		srv.Wait()
	}()

	var wg sync.WaitGroup
	workers := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			msg := fmt.Sprintf("worker-%d", id)
			resp, err := SendRequest(sockPath, &ExecRequest{
				Command: "echo",
				Args:    []string{msg},
			})
			if err != nil {
				t.Errorf("worker %d request failed: %v", id, err)
				return
			}
			if resp.Stdout != msg+"\n" {
				t.Errorf("worker %d expected stdout %q, got %q", id, msg+"\n", resp.Stdout)
			}
		}(i)
	}

	wg.Wait()
}

func TestMultilineExecution(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "multiline.sock")

	srv := NewServer(sockPath, allowAll())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() {
		_ = srv.Stop()
		srv.Wait()
	}()

	// Test python multiline script passed with real newlines
	resp, err := SendRequest(sockPath, &ExecRequest{
		Command: "python3",
		Args:    []string{"-c", "\nimport sys\nprint('hello from multiline python')\n"},
	})
	if err != nil {
		t.Fatalf("SendRequest failed: %v", err)
	}

	if resp.ExitCode != 0 || resp.Error != "" {
		t.Fatalf("command failed with exit code %d, error %q, stderr %q", resp.ExitCode, resp.Error, resp.Stderr)
	}

	expected := "hello from multiline python\n"
	if resp.Stdout != expected {
		t.Errorf("expected stdout %q, got %q", expected, resp.Stdout)
	}
}
