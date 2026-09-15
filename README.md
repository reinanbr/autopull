# autopull

[![CI](https://img.shields.io/github/actions/workflow/status/reinanbr/autopull/ci.yml?branch=main&label=CI&logo=githubactions&logoColor=white)](https://github.com/reinanbr/autopull/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/reinanbr/autopull?label=release&logo=github)](https://github.com/reinanbr/autopull/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/reinanbr/autopull/total?label=downloads&logo=github)](https://github.com/reinanbr/autopull/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/reinanbr/autopull)](go.mod)
[![License: MIT](https://img.shields.io/github/license/reinanbr/autopull)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS-informational?logo=linux&logoColor=white)](#linux-packaging)

Watches a git repository and runs `git pull` whenever a new commit lands on the tracked branch. Optionally runs a command after each pull.

Pure Go · zero dependencies · Linux & macOS · systemd-ready

---

## Install

Quick installer (auto-detects Linux/macOS, amd64/arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/reinanbr/autopull/main/install.sh | sh
```

- Downloads the latest release binary and installs to `/usr/local/bin/autopull`.
- Requires `curl` and `git` on `PATH`.

Uninstall:

```bash
curl -fsSL https://raw.githubusercontent.com/reinanbr/autopull/main/install.sh | sh -s -- uninstall
```

Manual install (from local build artifact):

```bash
./scripts/release-linux.sh v1.2.5
tar -xzf dist/auto_pull_linux_amd64_v1.2.5.tar.gz -C /tmp
cd /tmp/auto_pull_linux_amd64_v1.2.5
sudo ./install.sh
```

Build from source (Go 1.21+):

```bash
go build -o autopull .
```

Then move `autopull` somewhere on `PATH`, e.g. `/usr/local/bin/`.

Run tests:

```bash
go test ./...
```

---

## Quick start

```bash
cd /path/to/your/repo
autopull init        # generates config_auto_pull.json
autopull dry-run     # verify connectivity before running
autopull             # start watching
```

---

## Configuration

`autopull init` creates `config_auto_pull.json` in the current repo.  
Edit it as needed — every field is reloaded on every tick, no restart required.  
Changes to `check_interval_seconds` reset the polling timer immediately.

```json
{
  "repo_path": "/srv/myapp",
  "branch": "main",
  "check_interval_seconds": 10,
  "post_pull_command": "systemctl restart myapp",
  "post_pull_workdir": "",
  "log_file": "auto_pull.log",
  "notify_on_pull": false,
  "git_recovery_mode": "off"
}
```

| Field | Default | Description |
|---|---|---|
| `repo_path` | — | Absolute path to the local repository *(required)* |
| `branch` | `main` | Branch to track |
| `check_interval_seconds` | `5` | Polling interval |
| `post_pull_command` | — | Shell command to run after each pull |
| `post_pull_workdir` | `repo_path` | Working directory for the post-pull command |
| `log_file` | `auto_pull.log` | Log file path (absolute or relative to config) |
| `notify_on_pull` | `true` | Desktop notification on pull (Linux: `notify-send`, macOS: `osascript`) |
| `git_recovery_mode` | `off` | Auto-recovery strategy when git state blocks pull: `off`, `stash`, `hard-reset` |

**`github_token` is not a valid field.** Tokens belong in the environment.

`git_recovery_mode` values:
- `off`: only diagnose and log exact recovery commands.
- `stash`: auto-stash local changes (`git stash push --include-untracked`) and continue.
- `hard-reset`: force sync to `origin/<branch>` when local branch is ahead/diverged (destructive).

Recommended for deployment clones: keep daemon runtime files ignored in the application repository.

```bash
echo 'auto_pull.log' >> .gitignore
echo 'auto_pull.log.1' >> .gitignore
echo '.auto_pull.pid' >> .gitignore
echo '.auto_pull.state.json' >> .gitignore
echo 'config_auto_pull.json' >> .gitignore
```

---

## Authentication

For private repositories, provide a token via environment variable or `.env` file:

```bash
# environment variable (preferred)
export AUTOPULL_TOKEN=ghp_xxxxxxxxxxxx

# or: .env file in repo_path (never commit this)
echo 'AUTOPULL_TOKEN=ghp_xxxxxxxxxxxx' >> /srv/myapp/.env
echo '.env' >> /srv/myapp/.gitignore
```

Resolution order: `AUTOPULL_TOKEN` → `GITHUB_TOKEN` → `.env` in `repo_path`.  
Tokens set in `config_auto_pull.json` are rejected at startup.

---

## Usage

```
autopull [command] [config]
```

| Command | Description |
|---|---|
| *(none)* | Start the watcher (default config: `./config_auto_pull.json`) |
| `daemon` | Start watcher detached in background |
| `start` | Alias for `daemon` |
| `init` | Scaffold `config_auto_pull.json` for the current git repo |
| `status` | Show daemon state plus git diagnostics: dirty files, ahead/behind, and recovery hints |
| `stop` | Send SIGTERM to the running daemon |
| `logs [N]` | Print last N lines of the log (default: 50) |
| `dry-run` | Validate config and test remote connectivity without pulling |
| `service <action>` | Manage systemd service (`install`, `start`, `stop`, `restart`, `status`, `logs [N]`, `uninstall`) |
| `--version` | Print version |
| `--help` | Print this reference |

Config path can be passed as the last argument to any command:

```bash
autopull status /etc/auto_pull/config_auto_pull.json
autopull logs 100 /etc/auto_pull/config_auto_pull.json
autopull daemon /etc/auto_pull/config_auto_pull.json
autopull service install /etc/auto_pull/config_auto_pull.json
autopull service status
autopull service logs 100
```

### Background mode (built-in)

Start detached from your terminal using the native command:

```bash
autopull daemon /etc/auto_pull/config_auto_pull.json
# equivalent alias
autopull start /etc/auto_pull/config_auto_pull.json
```

Monitor and control as usual:

```bash
autopull status /etc/auto_pull/config_auto_pull.json
autopull logs 100 /etc/auto_pull/config_auto_pull.json
autopull stop /etc/auto_pull/config_auto_pull.json
```

`autopull daemon` runs the same watcher process in a detached session and reuses the same pid/state/log files.

Important:
- `autopull daemon` (or `autopull start`) starts the built-in background watcher.
- `autopull service start` controls the Linux systemd service.

### Native systemd management (Linux)

`autopull` can create and manage a systemd unit directly. The unit name is derived
from the config path (`autopull-<repo-dir>-<hash>`), so **each repo gets its own
service** — running `service install` for a second repo does not disturb the first:

```bash
# requires root privileges to write /etc/systemd/system/autopull-<name>-<hash>.service
sudo autopull service install /etc/auto_pull/config_auto_pull.json

# pass the same config path to any other 'service' subcommand to target that repo
autopull service status /etc/auto_pull/config_auto_pull.json
autopull service logs 200 /etc/auto_pull/config_auto_pull.json
sudo autopull service restart /etc/auto_pull/config_auto_pull.json
sudo autopull service stop /etc/auto_pull/config_auto_pull.json
sudo autopull service uninstall /etc/auto_pull/config_auto_pull.json
```

If you omit the config path, it defaults to `./config_auto_pull.json` (same as every
other command) — so running `service status` etc. from inside the repo you installed
from also works without repeating the path.

Notes:
- `service` subcommands are available only on Linux.
- `install`/`uninstall`/`start`/`stop`/`restart` usually require elevated permissions.
- Service user is resolved from `AUTOPULL_SERVICE_USER`, then `SUDO_USER`, then `USER`.
- Upgrading from a version that used a single global `autopull.service`: run
  `sudo systemctl disable --now autopull && sudo rm /etc/systemd/system/autopull.service`
  once, then `sudo autopull service install <config>` for each repo you're watching.

---

## How it works

```
every N seconds
  ├── reload config_auto_pull.json (no restart needed)
  ├── git fetch origin <branch>
  ├── compare local HEAD with origin/<branch>
  ├── dirty check — skip pull if tracked files have uncommitted changes
  ├── if hashes differ → git pull origin <branch>
  └── run post_pull_command via sh -c
```

- **No GitHub API** — uses native `git fetch` + hash comparison  
- **Hot config reload** — `config_auto_pull.json` is re-read every tick; changes to any field (including `check_interval_seconds`) take effect immediately  
- **15s timeout** on every git command; failures are logged and backed off  
- **Exponential backoff** on consecutive failures, capped at 5 minutes  
- **Log rotation** at ~5 MB (`auto_pull.log` → `auto_pull.log.1`); override with `AUTOPULL_LOG_MAX_BYTES`  
- **Token injection** via temporary `GIT_ASKPASS` script; `GIT_TERMINAL_PROMPT=0` prevents interactive prompts  
- **Graceful shutdown** on `SIGINT`/`SIGTERM` — logger is flushed and closed  

---

## Running as a systemd service

After `sudo ./install.sh`, a systemd unit is registered automatically.

```bash
sudo systemctl status autopull
sudo journalctl -u autopull -f
```

The service reads `/etc/auto_pull/config_auto_pull.json`.  
Place your token in `/etc/auto_pull/.env` or set `Environment=AUTOPULL_TOKEN=...` in the unit override:

```bash
sudo systemctl edit autopull
```

```ini
[Service]
Environment=AUTOPULL_TOKEN=ghp_xxxxxxxxxxxx
```

---

## Linux packaging

```bash
# build portable tar.gz
./scripts/release-linux.sh v1.2.5

# install
tar -xzf dist/auto_pull_linux_amd64_v1.2.5.tar.gz -C /tmp
cd /tmp/auto_pull_linux_amd64_v1.2.5
sudo ./install.sh

# uninstall
sudo ./uninstall.sh

# uninstall + remove config and logs
sudo ./uninstall.sh --purge
```

`install.sh` places the binary at `/usr/local/bin/autopull`, writes a default config to `/etc/auto_pull/`, and registers the systemd unit if systemd is available.

---

## Post-pull command examples

```json
"post_pull_command": "systemctl restart myapp"
```
```json
"post_pull_command": "npm ci --silent && pm2 reload ecosystem.config.js"
```
```json
"post_pull_command": "go build -o bin/app . && ./bin/app"
```

### Docker

Pick the flavor that matches how the service is deployed:

```json
"post_pull_command": "docker compose up -d --build"
```
Rebuilds the image(s) when the Dockerfile or dependencies changed. Slower, but always in sync with the pulled code.

```json
"post_pull_command": "docker compose up -d --build --no-deps <service>"
```
Rebuilds and restarts a single service in a multi-service stack, leaving the rest untouched.

```json
"post_pull_command": "docker compose restart"
```
Just restarts the running containers without rebuilding — fast, but only correct if the image doesn't need to change (e.g. bind-mounted source, interpreted languages).

```json
"post_pull_command": "docker compose down && docker compose up -d --build"
```
Full recreate: tears the stack down before bringing it back up. More downtime, but clears stale networks/volumes state — useful after compose file changes.

```json
"post_pull_command": "docker build -t myapp:latest . && docker stop myapp && docker rm myapp && docker run -d --name myapp -p 8080:8080 myapp:latest"
```
Single container without compose.

```json
"post_pull_command": "docker build -t registry.local/myapp:latest . && docker push registry.local/myapp:latest && docker service update --image registry.local/myapp:latest myapp_stack"
```
Docker Swarm: build, push to the registry, and roll the service.

```json
"post_pull_command": "kubectl rollout restart deployment/myapp"
```
Kubernetes: not Docker directly, but the same post-pull hook works for triggering a rolling restart.

`post_pull_command` is executed via `sh -c` in `post_pull_workdir`. Treat the config as trusted input.

---

## Files created at runtime

| File | Description |
|---|---|
| `.auto_pull.pid` | PID of the running daemon (next to config) |
| `.auto_pull.state.json` | Pull count, last pull time, error state, backoff |
| `auto_pull.log` | Daemon log (path set by `log_file`) |
| `auto_pull.log.1` | Previous log, kept after rotation |

---

## Development

Project layout (Go 1.21+, module `github.com/reinanbr/autopull`):

```
auto_pull/
├── main.go              — CLI entry point and command routing
├── autopull/            — core package (package autopull)
│   ├── config.go        — Config type, LoadConfig, token resolution
│   ├── logger.go        — structured logger with rotation
│   ├── git.go           — fetch/pull, dirty check, recovery hints
│   ├── state.go         — PID/state files, backoff, TailFile
│   ├── watcher.go       — main watch loop, RunWatcher
│   └── commands.go      — CmdInit, CmdDaemon, CmdStatus, CmdStop, CmdLogs, CmdDryRun, CmdService
└── tests/               — external test suite (package tests)
    ├── config_test.go
    ├── git_test.go
    ├── state_test.go
    └── commands_test.go
```

Build:

```bash
go build -o autopull .
```

Test:

```bash
go test ./...
```

Coverage:

```bash
go test ./... -coverprofile=cover.out && go tool cover -func=cover.out
```

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow and checks.

---

## License

MIT