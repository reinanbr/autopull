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

// ExitConfig is the exit status for unrecoverable setup errors (missing or
// invalid config, repo_path gone). The generated systemd unit lists it in
// RestartPreventExitStatus, so a service whose repo was moved or deleted
// stops instead of crash-looping every few seconds forever.
const ExitConfig = 78 // EX_CONFIG from sysexits.h

// goneTicks is how many consecutive checks the config or repo_path must be
// missing before the watcher gives up. More than one, so an editor that saves
// by delete+rename doesn't stop the watcher.
const goneTicks = 3

// RunWatcher resolves the config path, sets up signal handling, and starts the
// watch loop. It is the main entry point called by the CLI.
func RunWatcher(cfgPath string) {
	cfgPath = ResolveConfigPath(cfgPath)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config file not found: %s\n", cfgPath)
		fmt.Fprintln(os.Stderr, "Run 'autopull init' to create one, or see 'autopull --help'")
		os.Exit(ExitConfig)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := watch(ctx, cfgPath)
	stop()
	if code != 0 {
		os.Exit(code)
	}
}

// missingPath reports which of the config file and repo_path no longer exist.
func missingPath(cfgPath, repoPath string) string {
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		return "config " + cfgPath
	}
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return "repo_path " + repoPath
	}
	return ""
}

func watch(ctx context.Context, cfgPath string) int {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		return ExitConfig
	}

	if err := EnsureGitRepo(cfg.RepoPath); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		return ExitConfig
	}

	pidPath := pidFilePath(cfgPath)
	lockFile, err := acquireInstanceLock(pidPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v (pid file: %s)\n", err, pidPath)
		return 1
	}
	defer lockFile.Close()

	if err := writePID(pidPath, os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: could not write pid file: %v\n", err)
		return 1
	}
	defer os.Remove(pidPath)

	statePath := stateFilePath(cfgPath)
	runtimeState := loadRuntimeState(statePath)

	logPath := resolveLogPath(cfg, cfgPath)
	l, err := newLogger(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: could not open log file: %v\n", err)
		return 1
	}
	defer l.close()

	l.info("═══════════════════════════════════════════")
	l.info("         auto_pull started")
	l.info(fmt.Sprintf("  config  : %s", cfgPath))
	l.info(fmt.Sprintf("  repo    : %s", cfg.RepoPath))
	l.info(fmt.Sprintf("  branch  : %s", cfg.Branch))
	l.info(fmt.Sprintf("  interval: %ds", cfg.CheckIntervalSeconds))
	l.info(fmt.Sprintf("  log     : %s", logPath))
	l.info(fmt.Sprintf("  recovery: %s", cfg.GitRecoveryMode))
	if cfg.GithubToken != "" {
		l.info("  token   : (set)")
	}
	tokenUnused := cfg.GithubToken != "" && originIsSSH(cfg.RepoPath)
	if cfg.PostPullCommand != "" {
		l.info(fmt.Sprintf("  post-pull: %s", cfg.PostPullCommand))
	}
	l.info("═══════════════════════════════════════════")
	if tokenUnused {
		l.warn("origin is an SSH remote, so AUTOPULL_TOKEN/GITHUB_TOKEN is not used — fetches authenticate with SSH keys only")
	}

	prepareOwnFiles(cfg, cfgPath, l)

	interval := time.Duration(cfg.CheckIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	st := &repoState{}
	first := true
	gone := 0

	for {
		if !first {
			select {
			case <-ctx.Done():
				l.info("shutting down (signal received)")
				return 0
			case <-ticker.C:
			}

			// A deleted or moved repo would otherwise leave the watcher
			// failing (and, under systemd, alive) forever on a ghost path.
			if what := missingPath(cfgPath, cfg.RepoPath); what != "" {
				gone++
				if gone >= goneTicks {
					l.errLog(fmt.Sprintf("%s no longer exists — stopping watcher", what))
					return ExitConfig
				}
				continue
			}
			gone = 0

			newCfg, err := LoadConfig(cfgPath)
			if err != nil {
				st.notice("config:"+err.Error(), func() {
					l.warn(fmt.Sprintf("invalid config, keeping previous: %v", err))
				})
			} else {
				st.clearNotice("config:")
				newInterval := time.Duration(newCfg.CheckIntervalSeconds) * time.Second
				if newInterval != interval {
					interval = newInterval
					ticker.Reset(interval)
					l.info(fmt.Sprintf("check interval changed to %ds", newCfg.CheckIntervalSeconds))
				}
				cfg = newCfg
			}
		}
		first = false

		if time.Now().Before(st.backoffUntil) {
			continue
		}

		processRepo(cfg, cfgPath, st, runtimeState, l)

		if err := saveRuntimeState(statePath, runtimeState); err != nil {
			st.notice("state:"+err.Error(), func() {
				l.warn(fmt.Sprintf("could not persist state: %v", err))
			})
		}
	}
}

func (st *repoState) fail(rs *RuntimeState, l *logger, what string, err error) {
	st.consecutiveErrors++
	wait := BackoffDuration(st.consecutiveErrors)
	st.backoffUntil = time.Now().Add(wait)
	rs.ConsecutiveErrors = st.consecutiveErrors
	rs.BackoffUntil = st.backoffUntil
	rs.LastError = err.Error()
	l.errLog(fmt.Sprintf("%s failed (%dx, retrying in %s): %v", what, st.consecutiveErrors, wait, err))
	for _, hint := range GitPullErrorHints(err.Error()) {
		st.notice("hint:"+hint, func() { l.warn("hint: " + hint) })
	}
}

func processRepo(cfg *Config, cfgPath string, st *repoState, rs *RuntimeState, l *logger) {
	before, err := localCommit(cfg.RepoPath)
	if err != nil {
		st.fail(rs, l, "git rev-parse (local)", err)
		return
	}

	remote, err := remoteCommit(cfg.RepoPath, cfg.Branch, cfg.GithubToken)
	if err != nil {
		st.fail(rs, l, "git fetch", err)
		return
	}

	// success — reset backoff
	if st.consecutiveErrors > 0 {
		l.ok("fetch recovered")
	}
	st.consecutiveErrors = 0
	st.backoffUntil = time.Time{}
	rs.ConsecutiveErrors = 0
	rs.BackoffUntil = time.Time{}
	rs.LastError = ""

	ahead, behind, err := aheadBehind(cfg.RepoPath, cfg.Branch)
	if err != nil {
		l.warn(fmt.Sprintf("could not compute ahead/behind: %v", err))
	}

	_, own := ownFilesInRepo(cfg, cfgPath)
	changes, _ := TrackedChanges(cfg.RepoPath)
	changes = filterOwnChanges(changes, own)

	// Nothing new upstream: done. The exception is hard-reset mode, which
	// also discards a dirty tree when there is nothing to pull.
	if before == remote && (len(changes) == 0 || cfg.GitRecoveryMode != "hard-reset") {
		st.clearNotice("")
		return
	}

	target := "origin/" + cfg.Branch
	hardReset := func(why string) bool {
		guard := guardOwnFiles(cfg, cfgPath, target)
		out, resetErr := autoHardReset(cfg.RepoPath, cfg.Branch)
		for _, note := range guard.restore() {
			l.info(note)
		}
		if resetErr != nil {
			l.errLog(fmt.Sprintf("auto-recovery hard-reset (%s) failed: %v\n%s", why, resetErr, out))
			return false
		}
		l.warn(fmt.Sprintf("auto-recovery applied: hard-reset to %s (%s)", target, why))
		return true
	}

	if len(changes) > 0 {
		switch cfg.GitRecoveryMode {
		case "stash":
			out, stashErr := autoStash(cfg.RepoPath)
			if stashErr != nil {
				l.errLog(fmt.Sprintf("auto-recovery stash failed: %v\n%s", stashErr, out))
				return
			}
			l.warn("auto-recovery applied: stashed local changes to continue pull (see: git stash list)")
		case "hard-reset":
			if !hardReset("dirty working tree") {
				return
			}
		default:
			st.notice("dirty:"+remote+":"+strings.Join(changes, "|"), func() {
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
				l.warn("(this message is shown once until the situation changes)")
			})
			return
		}
	}

	head, err := localCommit(cfg.RepoPath)
	if err != nil {
		l.errLog(fmt.Sprintf("local commit check failed: %v", err))
		return
	}

	if head != remote && ahead > 0 {
		switch cfg.GitRecoveryMode {
		case "hard-reset":
			if !hardReset(fmt.Sprintf("local ahead %d, behind %d", ahead, behind)) {
				return
			}
		default:
			st.notice(fmt.Sprintf("ahead:%s:%s", head, remote), func() {
				if behind > 0 {
					l.warn(fmt.Sprintf("branch diverged (ahead %d, behind %d) — skipping pull", ahead, behind))
				} else {
					l.warn(fmt.Sprintf("local branch ahead by %d commit(s) — skipping pull", ahead))
				}
				for _, hint := range GitRecoveryHints(ahead, behind, false) {
					l.warn("hint: " + hint)
				}
				l.warn("(this message is shown once until the situation changes)")
			})
			return
		}
	}

	if head, err = localCommit(cfg.RepoPath); err != nil {
		l.errLog(fmt.Sprintf("local commit check failed: %v", err))
		return
	}

	if head != remote {
		l.ok(fmt.Sprintf("new commit detected: %s → %s", ShortHash(head), ShortHash(remote)))
		guard := guardOwnFiles(cfg, cfgPath, target)
		out, mergeErr := mergeFastForward(cfg.RepoPath, cfg.Branch)
		for _, note := range guard.restore() {
			l.info(note)
		}
		if mergeErr != nil {
			st.notice("merge:"+remote+":"+out, func() {
				l.errLog(fmt.Sprintf("git merge --ff-only %s failed: %v\n%s", target, mergeErr, out))
				for _, hint := range GitPullErrorHints(mergeErr.Error() + "\n" + out) {
					l.warn("hint: " + hint)
				}
				l.warn("(will keep retrying silently until the situation changes)")
			})
			return
		}
		if out != "" {
			l.info("  " + strings.ReplaceAll(out, "\n", "\n  "))
		}
	}

	after, err := localCommit(cfg.RepoPath)
	if err != nil || after == before {
		st.clearNotice("")
		return
	}

	st.clearNotice("")
	l.ok(fmt.Sprintf("updated %s → %s", ShortHash(before), ShortHash(after)))
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
