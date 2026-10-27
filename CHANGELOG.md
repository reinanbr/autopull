# Changelog

All notable changes to this project will be documented in this file.

## [v1.2.13] - 2026-10-27

### Fixed
- **A systemd service whose repo was moved or deleted crash-looped forever.** A missing config exited with status 1, and `Restart=always` restarted it every 3s indefinitely (seen in production: 270,000+ restarts of a unit pointing at a renamed directory). Unrecoverable setup errors (missing/invalid config, `repo_path` not a git repo) now exit with status 78 (`EX_CONFIG`), and generated units carry `RestartPreventExitStatus=78`, so systemd stops the unit instead. Reinstall existing units (`sudo autopull service install <config>`) to pick up the new directive.
- **A running watcher kept going after its repo directory was deleted**, failing every tick against a ghost path (and, under systemd, never exiting). If the config file or `repo_path` is missing for 3 consecutive checks, the watcher now logs why and exits with status 78, removing its pid file.
- **Network failures were reported as authentication failures.** ssh always appends "Could not read from remote repository" to any error, so DNS outages (`Could not resolve hostname`), dropped connections (`closed by remote host`) and timeouts printed "Authentication failed. Check AUTOPULL_TOKEN…". These now get a network hint; `Permission denied (publickey)` is now explicitly classified as an auth failure.

### Added
- `autopull service list` shows every installed autopull unit, its state and its config path, flagging units whose config no longer exists (with the uninstall command).
- `autopull status` shows whether a systemd service is installed for the config, and when the watcher isn't running and no service exists, points out that `autopull daemon` does not survive a reboot and suggests `service install`. `autopull daemon` prints the same note.

### Changed
- Version bumped to v1.2.13.

## [v1.2.12] - 2026-10-09

### Fixed
- **The watcher stopped pulling in repos where `config_auto_pull.json` already existed in git.** On every startup (and after every hard-reset) autopull rewrote its own config file, reformatting it and filling in defaults. A committed config therefore always looked locally modified; `--assume-unchanged` hid that from `git status`, but every later pull that touched the file aborted with "local changes would be overwritten by merge", indefinitely. The config is now never rewritten (only `autopull init` writes it), and the stale `--assume-unchanged` bits set by older versions are cleared on startup.
- autopull's own files (config, state) are now protected around every merge and hard-reset: a locally edited committed config is kept (local wins) instead of blocking the pull; an untracked local config no longer blocks a pull that adds the same file upstream; and a config is restored if upstream untracks it (e.g. after someone commits the recommended `git rm --cached`) instead of being deleted from the server.
- An empty `repo_path` was fatal, and a relative one was resolved against the process cwd (`/` under systemd). Both now resolve relative to the config file's directory, so a committed config works on every machine. `post_pull_workdir` relative paths resolve against `repo_path`.
- `git_recovery_mode: stash` used `--include-untracked`, which also stashed `.env` (losing the token) and an un-ignored config, leaving the watcher unable to fetch or reload. It now stashes tracked changes only.
- `hard-reset` recovery that moved the branch to new commits skipped `post_pull_command`, the notification and the pull counter. Any update of HEAD now counts as a pull.
- `git fetch` now uses an explicit refspec, so `origin/<branch>` is also updated in single-branch clones (previously it could silently stay stale forever).
- Fetch/merge/reset get a 2-minute timeout instead of 15s, so a large fetch over a slow link can finish instead of timing out on every tick.
- `autopull daemon` validated nothing and discarded the child's stderr, so a bad config reported "started" while the process died immediately. It now validates first, sends startup errors to the log, and reports an immediate exit.

### Changed
- Updates use `git merge --ff-only origin/<branch>` instead of `git pull`: no second network round-trip, independent of `pull.rebase`/`pull.ff` settings, and never a merge commit on the server.
- Persistent problems (dirty tree, diverged branch, blocked merge, invalid config) are logged once until the situation changes instead of every few seconds; the backoff line is gone (the error line now says when it retries).
- The first check runs immediately on startup instead of after one interval.
- autopull adds its untracked runtime files to `.git/info/exclude` (local, never committed) on startup and on `init`, so `git add -A` can't commit them by accident.
- `autopull init` detects the repo from the config's location and writes `repo_path` relative to the config when it is inside the repo.
- `git_recovery_mode` also accepts `hard_reset`; `.env` accepts `export KEY=value` lines; new hints for untracked-file collisions and non-fast-forward updates.

## [v1.2.11] - 2026-09-19

### Fixed
- A repo whose `config_auto_pull.json` (or `.auto_pull.pid`/`.auto_pull.state.json`/log file) had accidentally been committed could still fail a plain `git pull` with "local changes would be overwritten by merge" even when `autopull status` reported the tree as clean. Cause: `--assume-unchanged`, which autopull sets on these files once tracked to keep `git status`/`autopull status` quiet, has no effect on git's merge-safety check against the real file content — so `git pull` still refused the moment origin's history touched that same path. `autopull status` and the daemon log now check the git index directly and print a one-time "untrack + gitignore" fix command for any of autopull's own files that are tracked, independent of whether they currently show as dirty.
- The "working tree is dirty" recovery hint now suggests `git add -u` instead of `git add -A`. `-A` also stages *untracked* files, which is how autopull's own config/log/state files could end up committed into a watched repo in the first place (the root cause of the issue above); `-u` only stages changes to files git already tracks, matching what the dirty-check actually detects.

## [v1.2.10] - 2026-09-15

### Fixed
- Fixed a hang that could permanently wedge the watch loop: `exec.CommandContext`'s 15s timeout kills the `git` process itself, but if `git` had spawned a subprocess (e.g. `git-remote-https`, `ssh`) that inherited the stdout/stderr pipes and was left running — plausible under host-level CPU steal / IO stalls — `cmd.Wait()` could block forever waiting for those pipes to close, even though `git` was already dead. One repo's watcher would then go silent indefinitely (no more log lines, no crash, no systemd restart, since the process never actually exited) until manually restarted. `cmd.WaitDelay` is now set on every git invocation so `Wait()` is bounded to `gitTimeout + 5s` regardless of orphaned children.

## [v1.2.9] - 2026-09-15

### Fixed
- `autopull service install/uninstall/start/stop/restart/status/logs` now derive a unique systemd unit name per config path (`autopull-<repo-dir>-<hash>`) instead of always writing/managing a single hardcoded `autopull.service`. Previously, installing the service for a second repo silently overwrote the first repo's unit, leaving it with no running (and no auto-restarting) watcher. All `service` subcommands accept an optional trailing config path to select which repo's service to manage, matching the rest of the CLI.
- Fixed a race in `autopull daemon`/`start`: the "already running" check read a pid file that the child process only wrote *after* starting, so two near-simultaneous invocations could both pass the check and end up running two unsupervised watcher processes against the same repo (duplicate pulls, duplicate `post_pull_command` runs). The watcher now takes an exclusive `flock` on the pid file for its entire lifetime; a losing process exits immediately instead of running alongside the winner.

### Changed
- Version bumped to v1.2.9.

## [v1.2.8] - 2026-07-26

### Changed
- Repository renamed to `autopull`; module path is now `github.com/reinanbr/autopull`.
- All GitHub URLs (README badges/links, `install.sh`, `packaging/linux/install.sh`) updated to point at `reinanbr/autopull`.
- `autopull --version` now prints `autopull` instead of `auto_pull`.
- Version bumped to v1.2.8.

### Added
- `CONTRIBUTING.md` with the development workflow, pre-PR checks, and release steps.

## [v1.2.7] - 2026-07-08

### Changed
- README `post_pull_command` examples expanded to cover more Docker deployment scenarios: `docker compose up -d --build`, single-service rebuild (`--no-deps <service>`), restart-only, full recreate (`down && up`), plain `docker run`, Docker Swarm (`docker service update`), and Kubernetes (`kubectl rollout restart`) as a bonus for non-Docker orchestration.
- Version bumped to v1.2.7.

## [v1.2.5] - 2026-06-27

### Fixed
- `hard-reset` recovery mode now also applies when the working tree is dirty (previously it only acted on ahead commits; dirty trees fell through to the skip-pull default).
- Auto_pull's own runtime files (`.auto_pull.pid`, `.auto_pull.state.json`) are now silenced via `git update-index --assume-unchanged` at daemon startup if they are tracked in git, preventing them from blocking pulls on every cycle.
- After a `hard-reset`, `config_auto_pull.json` is restored from the in-memory config and marked as assume-unchanged, ensuring settings like `git_recovery_mode` survive the reset.

### Added
- `silenceOwnFiles()`: called at startup and after every hard-reset to protect auto_pull's own files from dirtying the working tree.
- `artifactGitignoreHints` now also detects transient database files (`.db-shm`, `.db-wal`, `.db-journal`) and auto_pull runtime files, and suggests the correct `gitignore` + `git rm --cached` commands to fix them permanently.

## [v1.2.1] - 2026-04-02

### Changed
- Removed `ignore_local_changes` from config to avoid destructive behavior and accidental deletion risk.
- Recommended approach is now `.gitignore` for daemon runtime files (`auto_pull.log`, `.auto_pull.pid`, `.auto_pull.state.json`, local `config_auto_pull.json`) in deployment repositories.
- Version bumped to v1.2.1.

## [v1.2.0] - 2026-04-02

### Added
- New `start` command as an alias to `autopull daemon` (built-in detached watcher).
- `autopull status` now performs git diagnostics and reports:
  - tracked dirty files (up to 5 entries)
  - ahead/behind counters versus `origin/<branch>`
  - actionable recovery hints for common blocked states
- New config field `git_recovery_mode` with supported values: `off`, `stash`, `hard-reset`.

### Changed
- Watcher now emits clearer guidance for blocked pulls (dirty tree, diverged branch, auth/ref failures).
- Optional automatic recovery behavior:
  - `stash`: stashes local changes and continues pull.
  - `hard-reset`: force-syncs local branch to `origin/<branch>` when ahead/diverged.
- Unknown command handling is stricter and no longer misinterprets arbitrary words as config paths.
- Version bumped to v1.2.0.

## [v1.1.6] - 2026-04-02

### Added
- New `autopull daemon` command to start watcher detached in background.
- New `autopull service` command family for Linux systemd integration: `install`, `start`, `stop`, `restart`, `status`, `logs [N]`, `uninstall`.

### Fixed
- PID/state handling now detects suspended processes (`T` state on Linux). `autopull status` reports them as suspended, `autopull daemon` no longer mistakes them for active watchers, and `autopull stop` force-stops suspended instances.
- `autopull stop` now waits for graceful SIGTERM shutdown and falls back to SIGKILL after timeout, reducing stuck background processes.

### Changed
- CLI help/usage updated to include `daemon` and `service` commands.
- README expanded with native background and systemd command examples, including permission notes.
- Version bumped to v1.1.6.

## [v1.1.5] - 2026-04-02

### Fixed
- Dirty working tree check now ignores untracked files (`--untracked-files=no`), preventing self-block when `auto_pull.log` is created inside the repo.

### Changed
- Warning message now clarifies that only tracked uncommitted changes block pull.
- Version bumped to v1.1.5.

## [v1.1.4] - 2026-03-31

### Fixed
- Installer go-install fallback now finds either the expected binary name (`autopull`) or the module basename, avoiding false negatives when Go emits `auto_pull_go`.

### Changed
- Version bumped to v1.1.4.

## [v1.1.3] - 2026-04-01

### Added
- CI now triggers on tags (`v*`) and publishes release binaries automatically; manual dispatch supported.

### Changed
- Version bumped to v1.1.3.

## [v1.1.2] - 2026-04-01

### Added
- Installer now falls back to `go install github.com/reinanbr/auto_pull_go@<version>` when a release binary is missing, avoiding 404s.

### Changed
- Version bumped to v1.1.2.

## [v1.1.1] - 2026-04-01

### Added
- Single-command installer via curl (`install.sh`) that auto-detects Linux/macOS and downloads the latest release binary to `/usr/local/bin/autopull`.
- Uninstall flag in the installer: `sh -s -- uninstall` removes the installed binary.

### Changed
- README documents the curl installer/uninstaller alongside manual and source install flows.

## [v1.1.0] - 2026-04-01

### Added
- `autopull dry-run` validates config and connectivity without pulling.

### Changed
- Config validation forbids `github_token` in JSON; tokens must come from environment variables or `.env`. Docs and examples updated accordingly.
- CLI docs now match runtime state (status reports pid, pulls, errors/backoff, log path).

## [v1.0.8] - 2026-03-31

### Added
- New CLI subcommands: `autopull init` (generate config in current git repo), `status` (pid + pulls + backoff + errors), `stop` (terminate daemon via pid), `logs` (tail recent log lines).
- Runtime state persisted to `.auto_pull.state.json` (pull count, bytes transferred, last pull, consecutive errors, backoff).

### Changed
- The watcher writes its PID to `.auto_pull.pid` (alongside the config) for status/stop commands.

## [v1.0.7] - 2026-03-31

### Added
- Token can also be supplied via `GITHUB_TOKEN` (env or .env), alongside `AUTOPULL_TOKEN`.

### Changed
- Docs updated to reflect env-first token flow and `.env` usage; JSON token remains legacy.

## [v1.0.6] - 2026-03-31

### Added
- Token loading from `.env` (AUTOPULL_TOKEN) in the repo directory; env wins over JSON.
- Log rotation size configurable via env `AUTOPULL_LOG_MAX_BYTES` (default ~5MB).
- Repo validation at startup to fail fast if the path is not a git repo.

### Changed
- Dirty working tree now skips pull with a clear warning (no accidental stash/pop).
- Safer hash rendering avoids panics on short hashes.
- Backoff hardens after repeated failures (caps to 5 minutes after 5 errors).
- Multi-repo configs are deprecated; only the first entry is processed when present.

## [v1.0.5] - 2026-03-31

### Added
- Release builds now embed the version string via `-X main.version`.
- Default binary version set to `v1.0.5` for tagged builds.

## [v1.0.0] - 2026-03-31

### Added
- MIT license file.
- Base `.gitignore` for Go and local development artifacts.
- Distro-agnostic Linux packaging structure under `packaging/linux/`.
- Portable release builder script at `scripts/release-linux.sh`.
- Example Linux systemd unit: `packaging/linux/auto_pull.service`.
- Linux install script: `packaging/linux/install.sh`.
- Linux uninstall script: `packaging/linux/uninstall.sh`.
- Example config for Linux install flow: `packaging/linux/config_auto_pull.example.json`.
- Global command wrapper `autopull` (installed at `/usr/local/bin/autopull`) to run from any folder.

### Changed
- Updated `README.md` with Linux distro-agnostic distribution and installation flow.
- Added global command usage docs (`autopull`) and config resolution behavior.
- Marked `run.sh` as executable.

### Notes
- `autopull` resolves config in this order when run without arguments:
  1. `./config_auto_pull.json` (current directory)
  2. `/etc/auto_pull/config_auto_pull.json` (fallback)
- systemd service uses `/etc/auto_pull/config_auto_pull.json`.

## [v1.0.2] - 2026-03-31

### Added
- Multiple repositories support via `repos` array in config.
- Exponential backoff (capped) per repository on git failures.
- Env token fallback: uses `AUTOPULL_TOKEN` when `github_token` is empty.
- Simple log rotation around ~5MB (`log_file.1`).

### Changed
- README documents OS limitation (Linux/macOS), multi-repo, backoff, log rotation, and env token.

## [v1.0.3] - 2026-03-31

### Added
- `--version` flag in the binary.
- Signal handling (SIGINT/SIGTERM) for graceful shutdown.
- Config example in root: `config_auto_pull.example.json`; `config_auto_pull.json` ignored by git.
- CI workflow (GitHub Actions) running gofmt, vet, test, build.

### Changed
- README documents OS support, CI, multi-repo, and root config example.

## [v1.0.4] - 2026-03-31

### Added
- run.sh: `--config` flag and positional config path support.

### Changed
- run.sh: daemon no longer redirects logs (avoids duplication); `--stop` waits for termination with timeout.
- go.mod: module is now `github.com/reinanbr/auto_pull_go`.
