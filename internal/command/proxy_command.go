package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhuxiangxiao/enclave/v3/internal/config"
	"github.com/zhuxiangxiao/enclave/v3/internal/sandbox"
	"github.com/zhuxiangxiao/enclave/v3/internal/unboxexec"
	"github.com/urfave/cli/v3"
)

func ProxyCommand() *cli.Command {
	return &cli.Command{
		Name:  "proxy",
		Usage: "Start or manage the Host Command Proxy daemon",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Path to a config file (overrides automatic config resolution)",
			},
			&cli.StringFlag{
				Name:    "socket",
				Aliases: []string{"s"},
				Usage:   "Path to Unix Domain Socket for proxy daemon",
			},
			&cli.BoolFlag{
				Name:    "foreground",
				Usage:   "Run proxy daemon in foreground mode (default behavior)",
			},
			&cli.BoolFlag{
				Name:    "background",
				Aliases: []string{"d"},
				Usage:   "Run proxy daemon in background mode",
			},
			&cli.BoolFlag{
				Name:  "status",
				Usage: "Check proxy daemon status",
			},
			&cli.BoolFlag{
				Name:  "stop",
				Usage: "Stop running proxy daemon",
			},
		},
		Action: proxyAction,
	}
}

func proxyAction(ctx context.Context, cmd *cli.Command) error {
	out := cmd.Writer

	sockPath := cmd.String("socket")
	if sockPath == "" {
		sockPath = sandbox.DefaultProxySocketPath()
	}

	pidPath := sockPath + ".pid"

	// Handle --status
	if cmd.Bool("status") {
		if unboxexec.IsServerRunning(sockPath) {
			pidStr := ""
			if data, err := os.ReadFile(pidPath); err == nil {
				pidStr = strings.TrimSpace(string(data))
			}
			if pidStr != "" {
				fmt.Fprintf(out, "enclave host command proxy status: running (PID %s)\n", pidStr)
			} else {
				fmt.Fprintln(out, "enclave host command proxy status: running")
			}
			fmt.Fprintf(out, "socket: %s\n", sockPath)
			return nil
		}
		fmt.Fprintln(out, "enclave host command proxy status: stopped")
		fmt.Fprintf(out, "socket: %s\n", sockPath)
		return nil
	}

	// Handle --stop
	if cmd.Bool("stop") {
		if !unboxexec.IsServerRunning(sockPath) {
			// Clean up stale pid file if present
			_ = os.Remove(pidPath)
			fmt.Fprintln(out, "enclave host command proxy is not running")
			return nil
		}

		pidData, err := os.ReadFile(pidPath)
		if err != nil {
			return fmt.Errorf("failed to read PID file %s: %w", pidPath, err)
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil {
			return fmt.Errorf("invalid PID in PID file: %w", err)
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("failed to find process %d: %w", pid, err)
		}

		if err := proc.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to send SIGTERM to process %d: %w", pid, err)
		}

		// Wait for socket to be removed / server to stop
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if !unboxexec.IsServerRunning(sockPath) {
				fmt.Fprintln(out, "enclave host command proxy stopped")
				return nil
			}
		}

		return fmt.Errorf("timed out waiting for proxy daemon (PID %d) to stop", pid)
	}

	// Handle --background
	if cmd.Bool("background") {
		if unboxexec.IsServerRunning(sockPath) {
			return fmt.Errorf("proxy daemon is already running on %s", sockPath)
		}

		// Self-exec with --foreground (or without --background)
		args := []string{}
		for _, arg := range os.Args[1:] {
			if arg == "--background" || arg == "-d" {
				continue
			}
			args = append(args, arg)
		}
		args = append(args, "--foreground")

		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}

		bgCmd := exec.Command(exe, args...)
		bgCmd.Stdout = nil
		bgCmd.Stderr = nil
		bgCmd.Stdin = nil
		bgCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

		if err := bgCmd.Start(); err != nil {
			return fmt.Errorf("failed to start proxy daemon in background: %w", err)
		}

		// Wait briefly to confirm daemon started
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			if unboxexec.IsServerRunning(sockPath) {
				fmt.Fprintln(out, "enclave host command proxy started in background")
				fmt.Fprintf(out, "socket: %s\n", sockPath)
				return nil
			}
		}

		return fmt.Errorf("daemon started with PID %d but socket was not created", bgCmd.Process.Pid)
	}

	// Default / Foreground mode
	if unboxexec.IsServerRunning(sockPath) {
		return fmt.Errorf("proxy daemon is already running on %s", sockPath)
	}

	var cfg *config.Config
	var err error

	if configPath := cmd.String("config"); configPath != "" {
		if _, err = os.Stat(configPath); os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", configPath)
		}
		cfg, err = config.LoadFile(configPath)
		if err != nil {
			return err
		}
	} else {
		cfg, err = config.Load()
		if err != nil {
			return err
		}
	}

	allowedCommands, err := config.CompileAllowedCommands(cfg.UnboxexecAllowedCommands)
	if err != nil {
		return fmt.Errorf("failed to compile unboxexec_allowed_commands: %w", err)
	}

	srv := unboxexec.NewServer(sockPath, allowedCommands)

	sigCtx, stopSignal := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	if err := srv.Start(sigCtx); err != nil {
		return err
	}

	fmt.Fprintln(out, "enclave host command proxy started")
	fmt.Fprintf(out, "socket: %s\n", sockPath)

	<-sigCtx.Done()
	fmt.Fprintln(out, "\nshutting down enclave host command proxy...")
	_ = srv.Stop()
	srv.Wait()
	fmt.Fprintln(out, "enclave host command proxy stopped")

	return nil
}
