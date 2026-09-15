package autopull

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const gitTimeout = 15 * time.Second

// knownArtifactDirs are top-level directory names that are typically generated
// by build tools and should not be tracked by git.
var knownArtifactDirs = []string{
	".next", "dist", "build", "out", ".nuxt", ".output", ".svelte-kit", ".solid",
	"node_modules", "vendor",
	"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache",
	"target", ".gradle", ".m2",
	".parcel-cache", "coverage", ".turbo",
}

// knownTransientSuffixes are file suffixes for database WAL/journal temp files
// generated at runtime that should never be tracked by git.
var knownTransientSuffixes = []string{".db-shm", ".db-wal", ".db-journal"}

// knownRuntimeFiles are exact filenames that auto_pull writes at runtime.
var knownRuntimeFiles = []string{".auto_pull.pid", ".auto_pull.state.json"}

func runGit(dir string, token string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Bound how long Wait() can block after the process is killed. Without
	// this, a hung grandchild (e.g. git-remote-https or ssh, left behind if
	// the VPS stalls under CPU/IO pressure) can keep stdout/stderr pipes open
	// forever, and CombinedOutput would never return even though git itself
	// was already killed by the context timeout — wedging this goroutine,
	// and with it the whole watch loop, permanently.
	cmd.WaitDelay = 5 * time.Second

	env := os.Environ()
	var cleanup func()

	if token != "" {
		askpassPath, cleanupFn, err := createAskPassScript()
		if err != nil {
			return "", err
		}
		cleanup = cleanupFn
		env = append(env,
			"GIT_ASKPASS="+askpassPath,
			"GIT_TERMINAL_PROMPT=0",
			"GIT_USERNAME=oauth2",
			"GIT_TOKEN="+token,
		)
	}

	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if cleanup != nil {
		cleanup()
	}

	if ctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(string(out)), fmt.Errorf("git command timed out after %s", gitTimeout)
	}

	return strings.TrimSpace(string(out)), err
}

// createAskPassScript creates a minimal POSIX sh script (no bash dependency)
// that prints the token when git asks for a password.
func createAskPassScript() (string, func(), error) {
	f, err := os.CreateTemp("", "autopull-askpass-*")
	if err != nil {
		return "", func() {}, err
	}

	// Use /bin/sh instead of bash — works on Alpine, Debian, macOS, etc.
	script := "#!/bin/sh\nprintf '%s' \"$GIT_TOKEN\"\n"
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Chmod(0700); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}

	cleanup := func() { _ = os.Remove(f.Name()) }
	return f.Name(), cleanup, nil
}

func localCommit(dir string) (string, error) {
	return runGit(dir, "", "rev-parse", "HEAD")
}

func remoteCommit(dir, branch, token string) (string, error) {
	if _, err := runGit(dir, token, "fetch", "origin", branch); err != nil {
		return "", fmt.Errorf("git fetch failed: %w", err)
	}
	return runGit(dir, token, "rev-parse", fmt.Sprintf("origin/%s", branch))
}

func pull(dir, branch, token string) (string, error) {
	return runGit(dir, token, "pull", "origin", branch)
}

func ShortHash(s string) string {
	if len(s) >= 7 {
		return s[:7]
	}
	return s
}

func EnsureGitRepo(path string) error {
	if path == "" {
		return fmt.Errorf("repo_path is required in config")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("repo_path not accessible: %w", err)
	}
	if _, err := runGit(path, "", "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("repo_path is not a git repository: %s", path)
	}
	return nil
}

func IsRepoDirty(path string) bool {
	// Ignore untracked files (like auto_pull.log) so watcher artifacts do not
	// permanently block pulls.
	out, err := runGit(path, "", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

func TrackedChanges(path string) ([]string, error) {
	out, err := runGit(path, "", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	res := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		res = append(res, line)
	}
	return res, nil
}

func currentBranch(path string) (string, error) {
	return runGit(path, "", "rev-parse", "--abbrev-ref", "HEAD")
}

func ParseAheadBehind(raw string) (int, int, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", raw)
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, err
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

func aheadBehind(path, branch string) (int, int, error) {
	raw, err := runGit(path, "", "rev-list", "--left-right", "--count", fmt.Sprintf("HEAD...origin/%s", branch))
	if err != nil {
		return 0, 0, err
	}
	return ParseAheadBehind(raw)
}

func autoStash(path string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	msg := fmt.Sprintf("autopull-auto-stash-%s", stamp)
	return runGit(path, "", "stash", "push", "--include-untracked", "-m", msg)
}

func autoHardReset(path, branch string) (string, error) {
	return runGit(path, "", "reset", "--hard", fmt.Sprintf("origin/%s", branch))
}

func GitRecoveryHints(ahead, behind int, dirty bool) []string {
	hints := []string{}
	if dirty {
		hints = append(hints,
			"Working tree is dirty. Resolve with: git add -A && git commit -m \"save local changes\"",
			"Or temporarily shelve changes: git stash push -m \"autopull-temp\"",
			"Or discard local modifications: git reset --hard HEAD",
		)
	}
	if ahead > 0 && behind > 0 {
		hints = append(hints,
			"Branch is diverged. To mirror remote exactly: git fetch origin && git reset --hard origin/<branch>",
			"If you need local commits, rebase first: git pull --rebase origin <branch>",
		)
	} else if ahead > 0 {
		hints = append(hints,
			"Local branch is ahead of origin. Push local commits or reset to remote.",
		)
	} else if behind > 0 {
		hints = append(hints,
			"Local branch is behind origin. A pull should fast-forward once working tree is clean.",
		)
	}
	return hints
}

// ArtifactGitignoreHints inspects the dirty file list and returns ready-to-run
// shell commands for any entries that look like untracked build artifacts or
// runtime-generated files.
func ArtifactGitignoreHints(statusLines []string) []string {
	seen := map[string]bool{}
	var cmds []string
	for _, line := range statusLines {
		path := line
		if len(line) > 3 {
			path = strings.TrimSpace(line[2:])
		}
		base := filepath.Base(path)
		top := strings.SplitN(path, "/", 2)[0]

		// known build artifact directories
		for _, dir := range knownArtifactDirs {
			if strings.EqualFold(top, dir) && !seen[top] {
				seen[top] = true
				entry := top + "/"
				cmds = append(cmds,
					fmt.Sprintf("echo '%s' >> .gitignore && git rm -r --cached %q && git add .gitignore && git commit -m 'chore: gitignore %s'",
						entry, top, entry),
				)
			}
		}

		// transient database WAL/journal files
		for _, suffix := range knownTransientSuffixes {
			pattern := "*" + suffix
			if strings.HasSuffix(base, suffix) && !seen[pattern] {
				seen[pattern] = true
				cmds = append(cmds,
					fmt.Sprintf("echo '%s' >> .gitignore && git rm --cached %q && git add .gitignore && git commit -m 'chore: gitignore %s'",
						pattern, path, pattern),
				)
			}
		}

		// auto_pull own runtime files
		for _, rf := range knownRuntimeFiles {
			if base == rf && !seen[rf] {
				seen[rf] = true
				cmds = append(cmds,
					fmt.Sprintf("echo '%s' >> .gitignore && git rm --cached %q && git add .gitignore && git commit -m 'chore: gitignore %s'",
						rf, path, rf),
				)
			}
		}
	}
	return cmds
}

func GitPullErrorHints(msg string) []string {
	raw := strings.ToLower(msg)
	hints := []string{}
	switch {
	case strings.Contains(raw, "authentication failed"),
		strings.Contains(raw, "repository not found"),
		strings.Contains(raw, "could not read from remote repository"):
		hints = append(hints,
			"Authentication failed. Check AUTOPULL_TOKEN/GITHUB_TOKEN or SSH key access.",
		)
	case strings.Contains(raw, "couldn't find remote ref"),
		strings.Contains(raw, "unknown revision"):
		hints = append(hints,
			"Branch reference not found on origin. Confirm config.branch and run: git branch -r",
		)
	case strings.Contains(raw, "merge conflict"),
		strings.Contains(raw, "conflict"):
		hints = append(hints,
			"Pull created conflicts. Resolve manually and commit, or force sync: git fetch origin && git reset --hard origin/<branch>",
		)
	case strings.Contains(raw, "would be overwritten by merge"),
		strings.Contains(raw, "local changes"):
		hints = append(hints,
			"Local tracked changes are blocking pull. Commit/stash/discard local modifications first.",
		)
	}
	return hints
}
