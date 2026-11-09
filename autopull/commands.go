package autopull

import (
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ServiceName derives a stable, unique systemd unit name from a config path so
// multiple repos on the same host can each run their own supervised instance
// instead of colliding on a single "autopull" unit.
func ServiceName(cfgPath string) string {
	cfgPath = ResolveConfigPath(cfgPath)
	base := sanitizeServiceName(filepath.Base(filepath.Dir(cfgPath)))
	h := fnv.New32a()
	h.Write([]byte(cfgPath))
	return fmt.Sprintf("autopull-%s-%08x", base, h.Sum32())
}

func sanitizeServiceName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "repo"
	}
	return out
}

func CmdInit(cfgPath string) error {
	cfgPath = ResolveConfigPath(cfgPath)
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("config already exists: %s (start it with 'autopull start', or delete it to re-run init)", cfgPath)
	}
	// Prefer the repo the config will live in; fall back to the cwd's repo
	// for configs kept outside the work tree (e.g. /etc/autopull).
	cfgDir := filepath.Dir(cfgPath)
	repoRoot, err := repoTopLevel(cfgDir)
	if err != nil {
		if repoRoot, err = repoTopLevel("."); err != nil {
			return fmt.Errorf("init requires a git repository: %w", err)
		}
	}
	// Inside the repo, store repo_path relative to the config so the file
	// keeps working if the repo is moved or cloned on another machine.
	repoPathValue := repoRoot
	if rel, err := filepath.Rel(filepath.Dir(realPath(cfgPath)), repoRoot); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		repoPathValue = rel
	}
	branch := detectBranch(repoRoot)
	cf := configFile{
		RepoPath:             repoPathValue,
		Branch:               branch,
		CheckIntervalSeconds: 5,
		PostPullCommand:      "",
		PostPullWorkdir:      "",
		LogFile:              "auto_pull.log",
		NotifyOnPull:         true,
		GitRecoveryMode:      "off",
	}
	if err := writeConfig(cfgPath, cf); err != nil {
		return err
	}
	fmt.Printf("Created %s\n", cfgPath)
	fmt.Printf("  repo   : %s\n", repoRoot)
	fmt.Printf("  branch : %s\n", branch)
	// Keep the config and runtime files out of `git add -A` (local only).
	prepareOwnFiles(&Config{RepoPath: repoRoot, LogFile: cf.LogFile}, cfgPath, nil)
	fmt.Println()
	fmt.Println("For private repos, set your token in .env:")
	fmt.Println("  echo 'AUTOPULL_TOKEN=ghp_xxxx' >> .env")
	fmt.Println("  echo '.env' >> .gitignore")
	return nil
}

func CmdStatus(cfgPath string) error {
	cfgPath = ResolveConfigPath(cfgPath)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	logPath := resolveLogPath(cfg, cfgPath)
	pidPath := pidFilePath(cfgPath)
	pid, pidErr := readPID(pidPath)
	running := pidErr == nil && processRunning(pid)
	stopped := pidErr == nil && processStopped(pid)
	state := loadRuntimeState(stateFilePath(cfgPath))

	fmt.Printf("Config  : %s\n", cfgPath)
	if pidErr != nil {
		fmt.Printf("Status  : stopped (no pid file)\n")
	} else {
		status := "stopped"
		if running {
			status = "running"
		} else if stopped {
			status = "stopped (suspended)"
		}
		fmt.Printf("Status  : %s (pid %d)\n", status, pid)
	}
	if svc := ServiceName(cfgPath); serviceInstalled(svc) {
		fmt.Printf("Service : %s (systemd, starts on boot)\n", svc)
	} else if !running {
		fmt.Println("hint    : not running. 'autopull daemon' does not survive a reboot; for a")
		fmt.Printf("hint    : persistent watcher run: sudo autopull service install %s\n", cfgPath)
	}
	fmt.Printf("Pulls   : %d\n", state.Pulls)
	if !state.LastPull.IsZero() {
		fmt.Printf("Last pull: %s\n", state.LastPull.Format(time.RFC3339))
	}
	if state.ConsecutiveErrors > 0 {
		fmt.Printf("Errors  : %d consecutive\n", state.ConsecutiveErrors)
	}
	if !state.BackoffUntil.IsZero() && time.Now().Before(state.BackoffUntil) {
		fmt.Printf("Backoff : until %s\n", state.BackoffUntil.Format(time.RFC3339))
	}
	if state.LastError != "" {
		fmt.Printf("Last err: %s\n", state.LastError)
	}
	fmt.Printf("Log     : %s\n", logPath)

	fmt.Println("Git     :")
	branch, err := currentBranch(cfg.RepoPath)
	if err != nil {
		fmt.Printf("  branch: failed to read current branch: %v\n", err)
	} else {
		fmt.Printf("  branch: %s (config: %s)\n", branch, cfg.Branch)
	}

	_, own := ownFilesInRepo(cfg, cfgPath)
	changes, err := TrackedChanges(cfg.RepoPath)
	changes = filterOwnChanges(changes, own)
	if err != nil {
		fmt.Printf("  dirty : check failed: %v\n", err)
	} else if len(changes) == 0 {
		fmt.Printf("  dirty : no tracked local changes\n")
	} else {
		fmt.Printf("  dirty : yes (%d tracked change(s))\n", len(changes))
		for i, line := range changes {
			if i >= 5 {
				fmt.Printf("  dirty : ... and %d more\n", len(changes)-i)
				break
			}
			fmt.Printf("  dirty : %s\n", line)
		}
	}

	if hints := OwnFileTrackingHints(cfg, cfgPath); len(hints) > 0 {
		fmt.Println("  warn  : autopull's own file(s) are committed in this repo. The watcher protects")
		fmt.Println("  warn  : them during pulls, but untracking them is cleaner. Run once, from the repo root:")
		for _, cmd := range hints {
			fmt.Printf("  warn  :   %s\n", cmd)
		}
	}

	fetchErr := ""
	if out, err := fetchBranch(cfg.RepoPath, cfg.Branch, cfg.GithubToken); err != nil {
		fetchErr = strings.TrimSpace(err.Error() + " " + out)
	}
	if fetchErr != "" {
		fmt.Printf("  remote: fetch failed: %s\n", fetchErr)
		for _, hint := range GitPullErrorHints(fetchErr) {
			fmt.Printf("  hint  : %s\n", hint)
		}
	} else {
		ahead, behind, err := aheadBehind(cfg.RepoPath, cfg.Branch)
		if err != nil {
			fmt.Printf("  sync  : failed to compare with origin/%s: %v\n", cfg.Branch, err)
		} else {
			fmt.Printf("  sync  : ahead %d, behind %d\n", ahead, behind)
			for _, hint := range GitRecoveryHints(ahead, behind, len(changes) > 0) {
				fmt.Printf("  hint  : %s\n", hint)
			}
		}
	}
	fmt.Printf("Recovery: %s (config git_recovery_mode)\n", cfg.GitRecoveryMode)
	fmt.Println(postPullStatusLine(cfg))
	return nil
}

// postPullStatusLine describes what runs after a successful pull, so a
// missing deploy hook is visible without opening the config.
func postPullStatusLine(cfg *Config) string {
	if strings.TrimSpace(cfg.PostPullCommand) == "" {
		return "Post-pull: none (config post_pull_command; only git is updated)"
	}
	workdir := cfg.PostPullWorkdir
	if workdir == "" {
		workdir = cfg.RepoPath
	}
	return fmt.Sprintf("Post-pull: %s (in %s)", cfg.PostPullCommand, workdir)
}

func CmdStop(cfgPath string) error {
	cfgPath = ResolveConfigPath(cfgPath)
	msg, err := stopProcess(pidFilePath(cfgPath))
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func CmdLogs(cfgPath string, lines int) error {
	cfgPath = ResolveConfigPath(cfgPath)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	logPath := resolveLogPath(cfg, cfgPath)
	out, err := TailFile(logPath, lines)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func CmdDryRun(cfgPath string) error {
	cfgPath = ResolveConfigPath(cfgPath)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}

	fmt.Println("=== dry-run: checking configuration ===")
	fmt.Printf("  config  : %s\n", cfgPath)
	fmt.Printf("  repo    : %s\n", cfg.RepoPath)
	fmt.Printf("  branch  : %s\n", cfg.Branch)
	fmt.Printf("  interval: %ds\n", cfg.CheckIntervalSeconds)
	if cfg.GithubToken != "" {
		fmt.Printf("  token   : (set)\n")
	} else {
		fmt.Printf("  token   : (not set — public repo or SSH assumed)\n")
	}

	fmt.Print("  git repo: ")
	if err := EnsureGitRepo(cfg.RepoPath); err != nil {
		fmt.Printf("FAIL — %v\n", err)
		return err
	}
	fmt.Println("OK")

	fmt.Print("  fetch   : ")
	if out, err := fetchBranch(cfg.RepoPath, cfg.Branch, cfg.GithubToken); err != nil {
		fmt.Printf("FAIL — %v\n%s\n", err, out)
		for _, hint := range GitPullErrorHints(err.Error() + "\n" + out) {
			fmt.Printf("  hint    : %s\n", hint)
		}
		return err
	}
	fmt.Println("OK")

	fmt.Print("  remote  : ")
	remote, err := runGit(cfg.RepoPath, "", "rev-parse", fmt.Sprintf("origin/%s", cfg.Branch))
	if err != nil {
		fmt.Printf("FAIL — %v\n", err)
		return err
	}
	fmt.Printf("%s\n", ShortHash(remote))

	fmt.Println("=== dry-run passed — ready to run autopull ===")
	return nil
}

func CmdDaemon(cfgPath string) error {
	cfgPath = ResolveConfigPath(cfgPath)
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("config file not found: %s", cfgPath)
	}
	// Validate up front: the detached child's errors would otherwise vanish.
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	if err := EnsureGitRepo(cfg.RepoPath); err != nil {
		return err
	}

	pidPath := pidFilePath(cfgPath)
	if lockFile, err := acquireInstanceLock(pidPath); err == nil {
		// Lock was free — release it immediately so the spawned child can take
		// it for real. This is still a check-then-act race in theory (two
		// invocations could both observe the lock as free before either child
		// starts), but that's now harmless: whichever child starts first wins
		// the real lock in watch(), and the loser exits immediately instead of
		// running unsupervised alongside it.
		lockFile.Close()
	} else {
		if pid, pidErr := readPID(pidPath); pidErr == nil {
			fmt.Printf("already running in background (pid %d)\n", pid)
		} else {
			fmt.Println("already running in background")
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	// The child's stderr goes to its log, so startup failures (FATAL lines)
	// are visible via `autopull logs` instead of being discarded.
	logPath := resolveLogPath(cfg, cfgPath)
	stderr, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		stderr = devNull
	} else {
		defer stderr.Close()
	}

	cmd := exec.Command(exe, cfgPath)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return fmt.Errorf("daemon exited right after starting (%v); see %s or run 'autopull %s' in the foreground", err, logPath, cfgPath)
	case <-time.After(time.Second):
	}

	fmt.Printf("autopull started in background (pid %d)\n", cmd.Process.Pid)
	fmt.Println("use 'autopull status' and 'autopull logs' to monitor")
	if runtime.GOOS == "linux" && !serviceInstalled(ServiceName(cfgPath)) {
		fmt.Println("note: a daemon does not survive a reboot; to start on boot use:")
		fmt.Printf("  sudo autopull service install %s\n", cfgPath)
	}
	return nil
}

func serviceUnitPath(svcName string) string {
	return filepath.Join("/etc/systemd/system", svcName+".service")
}

func serviceInstalled(svcName string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := os.Stat(serviceUnitPath(svcName))
	return err == nil
}

func ServiceUser() string {
	if v := os.Getenv("AUTOPULL_SERVICE_USER"); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v := os.Getenv("SUDO_USER"); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if v := os.Getenv("USER"); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func SystemdUnitContent(execPath, cfgPath, runAsUser string) string {
	return fmt.Sprintf(`[Unit]
Description=autopull watcher
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%q %q
Restart=always
RestartSec=3
RestartPreventExitStatus=%d
User=%s

[Install]
WantedBy=multi-user.target
`, execPath, cfgPath, ExitConfig, runAsUser)
}

// unitConfigPath extracts the config path (the last ExecStart argument) from
// a unit written by SystemdUnitContent.
func unitConfigPath(unitPath string) string {
	data, err := os.ReadFile(unitPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return ""
		}
		if p, err := strconv.Unquote(fields[len(fields)-1]); err == nil {
			return p
		}
		return fields[len(fields)-1]
	}
	return ""
}

func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func CmdService(args []string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("service command is only available on Linux")
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: autopull service <install|uninstall|start|stop|restart|status|logs|list> [config|lines]")
	}

	action := args[0]

	switch action {
	case "install":
		cfgPath := ""
		if len(args) > 1 {
			cfgPath = args[1]
		}
		cfgPath = ResolveConfigPath(cfgPath)
		if _, err := os.Stat(cfgPath); err != nil {
			return fmt.Errorf("config file not found: %s", cfgPath)
		}

		execPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot determine executable path: %w", err)
		}
		execPath, _ = filepath.Abs(execPath)

		runAsUser := ServiceUser()
		if runAsUser == "" {
			return fmt.Errorf("could not determine service user; set AUTOPULL_SERVICE_USER")
		}

		svcName := ServiceName(cfgPath)
		servicePath := serviceUnitPath(svcName)
		content := SystemdUnitContent(execPath, cfgPath, runAsUser)

		if err := os.WriteFile(servicePath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write %s (try with sudo): %w", servicePath, err)
		}

		if out, err := runCommand("systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload failed: %v\n%s", err, out)
		}
		if out, err := runCommand("systemctl", "enable", "--now", svcName); err != nil {
			return fmt.Errorf("systemctl enable --now failed: %v\n%s", err, out)
		}

		fmt.Printf("installed and started systemd service: %s\n", svcName)
		fmt.Printf("  config: %s\n", cfgPath)
		fmt.Println("pass this same config path to other 'service' subcommands to manage it, e.g.:")
		fmt.Printf("  autopull service status %s\n", cfgPath)
		return nil

	case "uninstall":
		cfgPath := ""
		if len(args) > 1 {
			cfgPath = args[1]
		}
		svcName := ServiceName(cfgPath)
		if out, err := runCommand("systemctl", "disable", "--now", svcName); err != nil {
			// keep going even if service wasn't active
			fmt.Fprintf(os.Stderr, "warning: disable failed: %v\n%s\n", err, out)
		}
		servicePath := serviceUnitPath(svcName)
		if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed removing %s: %w", servicePath, err)
		}
		if out, err := runCommand("systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload failed: %v\n%s", err, out)
		}
		fmt.Printf("uninstalled systemd service: %s\n", svcName)
		return nil

	case "start", "stop", "restart", "status":
		cfgPath := ""
		if len(args) > 1 {
			cfgPath = args[1]
		}
		svcName := ServiceName(cfgPath)
		out, err := runCommand("systemctl", action, svcName)
		if out != "" {
			fmt.Println(out)
		}
		if action == "status" {
			// systemctl status exits non-zero when the service is inactive; not an error
			return nil
		}
		if err != nil {
			return fmt.Errorf("systemctl %s failed: %v", action, err)
		}
		fmt.Printf("service %s: %s\n", svcName, action)
		return nil

	case "list":
		units, _ := filepath.Glob(serviceUnitPath("autopull-*"))
		if len(units) == 0 {
			fmt.Println("no autopull services installed")
			return nil
		}
		for _, unit := range units {
			svcName := strings.TrimSuffix(filepath.Base(unit), ".service")
			active, _ := runCommand("systemctl", "is-active", svcName)
			cfgPath := unitConfigPath(unit)
			note := ""
			if _, err := os.Stat(cfgPath); err != nil {
				note = fmt.Sprintf("  ⚠ config missing — remove with: sudo autopull service uninstall %s", cfgPath)
			}
			fmt.Printf("%-45s %-12s %s%s\n", svcName, active, cfgPath, note)
		}
		return nil

	case "logs":
		lines := "50"
		cfgPath := ""
		for _, a := range args[1:] {
			if _, err := strconv.Atoi(a); err == nil {
				lines = a
			} else {
				cfgPath = a
			}
		}
		svcName := ServiceName(cfgPath)
		out, err := runCommand("journalctl", "-u", svcName, "-n", lines, "--no-pager")
		if err != nil {
			return fmt.Errorf("journalctl failed: %v\n%s", err, out)
		}
		fmt.Println(out)
		return nil

	default:
		return fmt.Errorf("unknown service action: %s", action)
	}
}
