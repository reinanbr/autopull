package autopull

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// RunWatcher resolves the config path, sets up signal handling, and starts the
// watch loop. It is the main entry point called by the CLI.
func RunWatcher(cfgPath string) {
	cfgPath = ResolveConfigPath(cfgPath)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\n", cfgPath)
		fmt.Fprintln(os.Stderr, "Run 'autopull init' to create one, or see 'autopull --help'")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	watch(ctx, cfgPath)
}

func watch(ctx context.Context, cfgPath string) {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	if err := EnsureGitRepo(cfg.RepoPath); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	pidPath := pidFilePath(cfgPath)
	lockFile, err := acquireInstanceLock(pidPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v (pid file: %s)\n", err, pidPath)
		os.Exit(1)
	}
	defer lockFile.Close()

	if err := writePID(pidPath, os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: could not write pid file: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(pidPath)

	statePath := stateFilePath(cfgPath)
	runtimeState := loadRuntimeState(statePath)

	logPath := cfg.LogFile
	if !filepath.IsAbs(logPath) {
		logPath = filepath.Join(filepath.Dir(cfgPath), logPath)
	}

	l, err := newLogger(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: could not open log file: %v\n", err)
		os.Exit(1)
	}
	defer l.close()

	l.info("═══════════════════════════════════════════")
	l.info("         auto_pull started")
	l.info(fmt.Sprintf("  repo    : %s", cfg.RepoPath))
	l.info(fmt.Sprintf("  branch  : %s", cfg.Branch))
	l.info(fmt.Sprintf("  interval: %ds", cfg.CheckIntervalSeconds))
	l.info(fmt.Sprintf("  log     : %s", logPath))
	l.info(fmt.Sprintf("  recovery: %s", cfg.GitRecoveryMode))
	if cfg.GithubToken != "" {
		l.info("  token   : (set)")
	}
	if cfg.PostPullCommand != "" {
		l.info(fmt.Sprintf("  post-pull: %s", cfg.PostPullCommand))
	}
	l.info("═══════════════════════════════════════════")

	// Silence auto_pull's own runtime files at startup so they never dirty the
	// working tree and block pulls.
	silenceOwnFiles(cfg, cfgPath, l)

	interval := time.Duration(cfg.CheckIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	st := &repoState{}

	for {
		select {
		case <-ctx.Done():
			l.info("shutting down (signal received)")
			return
		case <-ticker.C:
		}

		newCfg, err := LoadConfig(cfgPath)
		if err != nil {
			l.warn(fmt.Sprintf("invalid config, keeping previous: %v", err))
		} else {
			newInterval := time.Duration(newCfg.CheckIntervalSeconds) * time.Second
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}
			cfg = newCfg
		}

		if time.Now().Before(st.backoffUntil) {
			l.warn(fmt.Sprintf("in backoff until %s", st.backoffUntil.Format(time.RFC3339)))
			continue
		}

		processRepo(cfg, cfgPath, st, runtimeState, l)

		if err := saveRuntimeState(statePath, runtimeState); err != nil {
			l.warn(fmt.Sprintf("could not persist state: %v", err))
		}
	}
}

func processRepo(cfg *Config, cfgPath string, st *repoState, rs *RuntimeState, l *logger) {
	local, err := localCommit(cfg.RepoPath)
	if err != nil {
		st.consecutiveErrors++
		st.backoffUntil = time.Now().Add(BackoffDuration(st.consecutiveErrors))
		rs.ConsecutiveErrors = st.consecutiveErrors
		rs.BackoffUntil = st.backoffUntil
		rs.LastError = err.Error()
		l.errLog(fmt.Sprintf("git rev-parse (local) failed (%dx): %v", st.consecutiveErrors, err))
		return
	}

	remote, err := remoteCommit(cfg.RepoPath, cfg.Branch, cfg.GithubToken)
	if err != nil {
		st.consecutiveErrors++
		st.backoffUntil = time.Now().Add(BackoffDuration(st.consecutiveErrors))
		rs.ConsecutiveErrors = st.consecutiveErrors
		rs.BackoffUntil = st.backoffUntil
		rs.LastError = err.Error()
		l.errLog(fmt.Sprintf("git fetch failed (%dx): %v", st.consecutiveErrors, err))
		return
	}

	// success — reset backoff
	st.consecutiveErrors = 0
	st.backoffUntil = time.Time{}
	rs.ConsecutiveErrors = 0
	rs.BackoffUntil = time.Time{}
	rs.LastError = ""

	ahead, behind, err := aheadBehind(cfg.RepoPath, cfg.Branch)
	if err != nil {
		l.warn(fmt.Sprintf("could not compute ahead/behind: %v", err))
	}

	changes, _ := TrackedChanges(cfg.RepoPath)
	if len(changes) > 0 {
		switch cfg.GitRecoveryMode {
		case "stash":
			out, stashErr := autoStash(cfg.RepoPath)
			if stashErr != nil {
				l.errLog(fmt.Sprintf("auto-recovery stash failed: %v\n%s", stashErr, out))
				return
			}
			l.warn("auto-recovery applied: stashed local changes to continue pull")
		case "hard-reset":
			out, resetErr := autoHardReset(cfg.RepoPath, cfg.Branch)
			if resetErr != nil {
				l.errLog(fmt.Sprintf("auto-recovery hard-reset (dirty) failed: %v\n%s", resetErr, out))
				return
			}
			l.warn("auto-recovery applied: hard-reset to clear dirty working tree")
			// hard-reset wipes the assume-unchanged bit and may revert config_auto_pull.json;
			// restore both so recovery settings survive and runtime files stay quiet.
			silenceOwnFiles(cfg, cfgPath, l)
		default:
			l.warn("working tree has tracked uncommitted changes — skipping pull to avoid conflicts")
			for _, f := range changes {
				l.warn("  " + f)
			}
			if fixCmds := ArtifactGitignoreHints(changes); len(fixCmds) > 0 {
				l.warn("hint: these look like build artifacts — run to fix permanently:")
				for _, cmd := range fixCmds {
					l.warn("  " + cmd)
				}
			}
			for _, hint := range GitRecoveryHints(ahead, behind, true) {
				l.warn("hint: " + hint)
			}
			return
		}
	}

	if ahead > 0 {
		switch cfg.GitRecoveryMode {
		case "hard-reset":
			out, resetErr := autoHardReset(cfg.RepoPath, cfg.Branch)
			if resetErr != nil {
				l.errLog(fmt.Sprintf("auto-recovery hard-reset failed: %v\n%s", resetErr, out))
				return
			}
			l.warn("auto-recovery applied: hard-reset to origin branch")
			silenceOwnFiles(cfg, cfgPath, l)
			local, err = localCommit(cfg.RepoPath)
			if err != nil {
				l.errLog(fmt.Sprintf("local commit check failed after hard-reset: %v", err))
				return
			}
		default:
			if behind > 0 {
				l.warn(fmt.Sprintf("branch diverged (ahead %d, behind %d) — skipping pull", ahead, behind))
			} else {
				l.warn(fmt.Sprintf("local branch ahead by %d commit(s) — skipping pull", ahead))
			}
			for _, hint := range GitRecoveryHints(ahead, behind, false) {
				l.warn("hint: " + hint)
			}
			return
		}
	}

	if local == remote {
		return // nothing new
	}

	l.ok(fmt.Sprintf("new commit detected: %s → %s", ShortHash(local), ShortHash(remote)))

	out, err := pull(cfg.RepoPath, cfg.Branch, cfg.GithubToken)
	if err != nil {
		l.errLog(fmt.Sprintf("git pull failed: %v\n%s", err, out))
		for _, hint := range GitPullErrorHints(err.Error() + "\n" + out) {
			l.warn("hint: " + hint)
		}
		return
	}
	l.ok("git pull completed")
	if out != "" {
		l.info("  " + strings.ReplaceAll(out, "\n", "\n  "))
	}

	rs.Pulls++
	rs.LastPull = time.Now()

	if cfg.NotifyOnPull {
		notify("auto_pull", fmt.Sprintf("Pull done: %s@%s", filepath.Base(cfg.RepoPath), cfg.Branch))
	}

	if err := runPostCommand(cfg, l); err != nil {
		l.errLog(fmt.Sprintf("post-pull command failed: %v", err))
	} else if cfg.PostPullCommand != "" {
		l.ok("post-pull command completed successfully")
	}
}

func runPostCommand(cfg *Config, l *logger) error {
	if cfg.PostPullCommand == "" {
		return nil
	}

	workdir := cfg.PostPullWorkdir
	if workdir == "" {
		workdir = cfg.RepoPath
	}

	l.info(fmt.Sprintf("Running post-pull command: %s", cfg.PostPullCommand))

	cmd := exec.Command("sh", "-c", cfg.PostPullCommand)
	cmd.Dir = workdir
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if output != "" {
		for _, line := range strings.Split(output, "\n") {
			l.info("  > " + line)
		}
	}
	return err
}

func notify(title, body string) {
	if err := exec.Command("notify-send", title, body).Run(); err != nil {
		escape := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
		_ = exec.Command("osascript", "-e",
			fmt.Sprintf(`display notification "%s" with title "%s"`, escape(body), escape(title))).Run()
	}
}
