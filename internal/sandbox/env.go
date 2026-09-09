package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kohkimakimoto/enclave/v3/internal/config"
)

// SocketPath returns the path for the daemon's Unix Domain Socket.
func SocketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("enclave-unboxexec-%d.sock", os.Getpid()))
}

// ConfigDumpPath returns the path for the effective config dump file.
func ConfigDumpPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("enclave-config-%d.toml", os.Getpid()))
}

// DefaultProxySocketPath returns the default socket path for the enclave proxy.
// If ENCLAVE_UNBOXEXEC_SOCK environment variable is set, it returns its value.
// Otherwise, it returns proxy.sock inside the user config directory (~/.config/enclave/proxy.sock).
func DefaultProxySocketPath() string {
	if sock := os.Getenv("ENCLAVE_UNBOXEXEC_SOCK"); sock != "" {
		return sock
	}
	return filepath.Join(config.UserConfigDir(), "proxy.sock")
}
