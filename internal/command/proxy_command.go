package command

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kohkimakimoto/enclave/v3/internal/config"
	"github.com/kohkimakimoto/enclave/v3/internal/unboxexec"
	"github.com/urfave/cli/v3"
)

func ProxyCommand() *cli.Command {
	return &cli.Command{
		Name:      "proxy",
		Usage:     "Run the host-side unboxexec command proxy",
		UsageText: "enclave proxy [options]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "Path to a config file"},
			&cli.StringFlag{Name: "socket", Usage: "Unix socket path (default: user config directory/proxy.sock)"},
			&cli.BoolFlag{Name: "foreground", Usage: "Run in the foreground (the default)"},
		},
		Action: proxyAction,
	}
}

func proxyAction(ctx context.Context, cmd *cli.Command) error {
	cfg, err := loadProxyConfig(cmd.String("config"))
	if err != nil {
		return err
	}
	allowedCommands, err := config.CompileAllowedCommands(cfg.UnboxexecAllowedCommands)
	if err != nil {
		return fmt.Errorf("failed to compile unboxexec_allowed_commands: %w", err)
	}

	sockPath := cmd.String("socket")
	if sockPath == "" {
		sockPath = filepath.Join(config.UserConfigDir(), "proxy.sock")
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	server, err := unboxexec.Start(ctx, sockPath, allowedCommands)
	if err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}
	fmt.Fprintln(cmd.Writer, "enclave host command proxy started")
	fmt.Fprintf(cmd.Writer, "socket: %s\n", sockPath)
	fmt.Fprintf(cmd.Writer, "export ENCLAVE_UNBOXEXEC_SOCK=%q\n", sockPath)
	server.Wait()
	return nil
}

func loadProxyConfig(configPath string) (*config.Config, error) {
	if configPath == "" {
		return config.Load()
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file not found: %s", configPath)
	}
	return config.LoadFile(configPath)
}
