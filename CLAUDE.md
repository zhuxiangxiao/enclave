# enclave

## Overview

A wrapper tool to safely run any command or AI agent in a sandboxed environment on macOS, or run a standalone Host Command Proxy daemon for external sandboxes.
Implemented in Go.

## Features

### Sandboxed Execution

- Execute commands using macOS `sandbox-exec`
- Sandbox profile is configured via `sandbox_profile` in TOML config; if not set, built-in default is used
- Transparent argument passing via `enclave run -- <command> [args...]`

### Standalone Host Command Proxy & Unboxexec Execution

- Run standalone daemon via `enclave proxy` (`--foreground`, `--background`, `--status`, `--stop`)
- Communication via Unix Domain Socket (defaults to `~/.config/enclave/proxy.sock` or via `ENCLAVE_UNBOXEXEC_SOCK`)
- Execute commands outside sandbox via `enclave unboxexec -- <command>`

### Configuration File

- Three-tier TOML configuration with layered merging:
  1. User: `~/.config/enclave/config.toml`
  2. Project: `./enclave.toml` in working directory
  3. Local: `./enclave.local.toml` in working directory (gitignore-friendly overrides)
- Optional `--config` file override.

```toml
sandbox_profile = '''
(version 1)
(allow default)
(deny file-write*)
'''

unboxexec_allowed_commands = [
    "^playwright-cli",
    "^git ",
    "^./gradlew ",
]
```

## Architecture

```text
Host
┌─────────────────────────────────────────────┐
│                                             │
│   enclave proxy / daemon                    │
│        │                                    │
│        │ Unix Domain Socket                 │
│        ▼                                    │
│   Host command execution                    │
│                                             │
└────────────────▲────────────────────────────┘
                 │
                 │ Unix Socket
                 │
      ┌──────────┴──────────┐
      │                     │
 OpenCode Sandbox       macOS Sandbox
      │                     │
      └── enclave ──────────┘
             unboxexec
```

### Unboxexec Communication Protocol

JSON over Unix Domain Socket:

**Request**:
```json
{
  "command": "git",
  "args": ["status"],
  "env": {"KEY": "value"},
  "dir": "/path/to/workdir",
  "timeout": 300
}
```

**Response**:
```json
{
  "stdout": "...",
  "stderr": "...",
  "exit_code": 0,
  "error": ""
}
```

## Package Structure

| Package | Description |
|---|---|
| `cmd/enclave` | Entry point (`main.go`) |
| `internal/command` | CLI application setup, subcommand definitions (`run`, `proxy`, `init`, `config`, `profile`, `unboxexec`, etc.) |
| `internal/config` | TOML configuration loading and allowed-command compilation |
| `internal/sandbox` | Sandbox profile building, environment variable helpers |
| `internal/unboxexec` | Unboxexec daemon (`Server`) and client (`SendRequest`) |
| `internal/version` | Version and commit hash |

## Environment Variables

| Variable | Description |
|---|---|
| `ENCLAVE_SANDBOX` | Set to `1` inside the sandbox |
| `ENCLAVE_UNBOXEXEC_SOCK` | Unix socket path for communicating with the unboxexec proxy daemon |
| `ENCLAVE_CONFIG` | Path to the effective config dump file |

## Development

- Sandboxed execution (`enclave run`) is macOS-specific (`sandbox-exec`). `enclave proxy` and `enclave unboxexec` use standard Unix domain sockets.
- `make build` — Build dev binary to `.dev/build/dev/enclave`
- `make test` — Run tests (`go test ./...`)
