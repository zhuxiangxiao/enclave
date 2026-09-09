package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuxiangxiao/enclave/v3/internal/unboxexec"
)

func TestProxyCommand_StatusStopped(t *testing.T) {
	testChdirTemp(t)
	testSetupFakeXDGConfig(t)

	sockPath := filepath.Join(t.TempDir(), "test.sock")

	buf := &bytes.Buffer{}
	cmd := ProxyCommand()
	cmd.Writer = buf

	err := cmd.Run(context.Background(), []string{"proxy", "--socket", sockPath, "--status"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertContains(t, buf.String(), "enclave host command proxy status: stopped")
}

func TestProxyCommand_StartStatusStop(t *testing.T) {
	testChdirTemp(t)
	testSetupFakeXDGConfig(t)

	sockPath := filepath.Join(t.TempDir(), "proxy-test.sock")

	// Write a config allowing echo
	cfgPath := filepath.Join(t.TempDir(), "enclave.toml")
	if err := os.WriteFile(cfgPath, []byte(`unboxexec_allowed_commands = ["^echo"]`), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start proxy in a goroutine
	done := make(chan error, 1)
	go func() {
		cmd := ProxyCommand()
		cmd.Writer = &bytes.Buffer{}
		done <- cmd.Run(ctx, []string{"proxy", "--socket", sockPath, "--config", cfgPath})
	}()

	// Wait for server to start
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		if unboxexec.IsServerRunning(sockPath) {
			break
		}
	}

	if !unboxexec.IsServerRunning(sockPath) {
		t.Fatal("proxy daemon failed to start")
	}

	// Test status
	bufStatus := &bytes.Buffer{}
	cmdStatus := ProxyCommand()
	cmdStatus.Writer = bufStatus
	if err := cmdStatus.Run(context.Background(), []string{"proxy", "--socket", sockPath, "--status"}); err != nil {
		t.Fatalf("status command failed: %v", err)
	}
	assertContains(t, bufStatus.String(), "enclave host command proxy status: running")

	// Test unboxexec execution through socket
	t.Setenv("ENCLAVE_UNBOXEXEC_SOCK", sockPath)
	bufUnbox := &bytes.Buffer{}
	cmdUnbox := UnboxexecCommand()
	cmdUnbox.Writer = bufUnbox
	if err := cmdUnbox.Run(context.Background(), []string{"unboxexec", "--", "echo", "hello"}); err != nil {
		t.Fatalf("unboxexec failed: %v", err)
	}

	// Test stop
	cmdStop := ProxyCommand()
	cmdStop.Writer = &bytes.Buffer{}
	if err := cmdStop.Run(context.Background(), []string{"proxy", "--socket", sockPath, "--stop"}); err != nil {
		t.Fatalf("stop command failed: %v", err)
	}

	cancel()
	<-done

	if unboxexec.IsServerRunning(sockPath) {
		t.Fatal("expected proxy daemon to be stopped after --stop")
	}
}
