package autopull

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	RepoPath             string `json:"repo_path"`
	Branch               string `json:"branch"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	PostPullCommand      string `json:"post_pull_command"`
	PostPullWorkdir      string `json:"post_pull_workdir"`
	LogFile              string `json:"log_file"`
	NotifyOnPull         bool   `json:"notify_on_pull"`
	GitRecoveryMode      string `json:"git_recovery_mode"`

	// GithubToken is intentionally excluded from JSON serialization.
	// Set via AUTOPULL_TOKEN or GITHUB_TOKEN env var, or .env file in repo_path.
	GithubToken string `json:"-"`
}

// configFile is the on-disk representation — mirrors Config but omits the token.
type configFile struct {
	RepoPath             string `json:"repo_path"`
	Branch               string `json:"branch"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	PostPullCommand      string `json:"post_pull_command"`
	PostPullWorkdir      string `json:"post_pull_workdir"`
	LogFile              string `json:"log_file"`
	NotifyOnPull         bool   `json:"notify_on_pull"`
	GitRecoveryMode      string `json:"git_recovery_mode"`
}

func LoadDotEnvToken(baseDir string) string {
	f, err := os.Open(filepath.Join(baseDir, ".env"))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key == "AUTOPULL_TOKEN" || key == "GITHUB_TOKEN" {
			return val
		}
	}
	if scanner.Err() != nil {
		return ""
	}
	return ""
}

func tokenFromEnv() string {
	if v := os.Getenv("AUTOPULL_TOKEN"); v != "" {
		return v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return v
	}
	return ""
}

func ResolveConfigPath(p string) string {
	if p == "" {
		p = "config_auto_pull.json"
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// reject legacy multi-repo configs explicitly
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if _, hasRepos := raw["repos"]; hasRepos {
		return nil, fmt.Errorf(
			"'repos' field is not supported: each repository should have its own config_auto_pull.json. " +
				"Run 'autopull init' inside each repo directory",
		)
	}
	if _, hasToken := raw["github_token"]; hasToken {
		return nil, fmt.Errorf(
			"'github_token' must not be set in config_auto_pull.json — " +
				"use AUTOPULL_TOKEN or GITHUB_TOKEN in a .env file or environment variable instead",
		)
	}

	var cf configFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg := &Config{
		RepoPath:             cf.RepoPath,
		Branch:               cf.Branch,
		CheckIntervalSeconds: cf.CheckIntervalSeconds,
		PostPullCommand:      cf.PostPullCommand,
		PostPullWorkdir:      cf.PostPullWorkdir,
		LogFile:              cf.LogFile,
		NotifyOnPull:         cf.NotifyOnPull,
		GitRecoveryMode:      NormalizeRecoveryMode(cf.GitRecoveryMode),
	}

	// defaults
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.CheckIntervalSeconds <= 0 {
		cfg.CheckIntervalSeconds = 5
	}
	if cfg.LogFile == "" {
		cfg.LogFile = "auto_pull.log"
	}

	// token resolution: env var → .env file in repo dir → empty
	token := tokenFromEnv()
	if token == "" {
		baseDir := cfg.RepoPath
		if baseDir == "" {
			baseDir = filepath.Dir(path)
		}
		token = LoadDotEnvToken(baseDir)
	}
	cfg.GithubToken = token

	return cfg, nil
}

func NormalizeRecoveryMode(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "", "off":
		return "off"
	case "stash":
		return "stash"
	case "hard-reset":
		return "hard-reset"
	default:
		return "off"
	}
}

func configToFile(cfg *Config) configFile {
	return configFile{
		RepoPath:             cfg.RepoPath,
		Branch:               cfg.Branch,
		CheckIntervalSeconds: cfg.CheckIntervalSeconds,
		PostPullCommand:      cfg.PostPullCommand,
		PostPullWorkdir:      cfg.PostPullWorkdir,
		LogFile:              cfg.LogFile,
		NotifyOnPull:         cfg.NotifyOnPull,
		GitRecoveryMode:      cfg.GitRecoveryMode,
	}
}

func writeConfig(path string, cfg configFile) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func detectBranch(repoPath string) string {
	branch, err := runGit(repoPath, "", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "" || branch == "HEAD" {
		return "main"
	}
	return branch
}

// trackedInIndex returns only those files (relative to repoPath) that git tracks.
func trackedInIndex(repoPath string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"ls-files", "--"}, files...)
	out, err := runGit(repoPath, "", args...)
	if err != nil || out == "" {
		return nil
	}
	var tracked []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f != "" {
			tracked = append(tracked, f)
		}
	}
	return tracked
}

// assumeUnchanged marks files with --assume-unchanged so git status ignores
// their local modifications. Only meaningful for files already in the index.
func assumeUnchanged(repoPath string, files []string) {
	if len(files) == 0 {
		return
	}
	args := append([]string{"update-index", "--assume-unchanged"}, files...)
	runGit(repoPath, "", args...)
}

// silenceOwnFiles hides auto_pull's own tracked runtime files from `git
// status` via --assume-unchanged. Called at startup and after every
// hard-reset (which clears that index bit). Also re-writes the config so
// recovery settings survive the reset.
//
// This only quiets status noise — it does NOT stop `git pull` from aborting
// with "local changes would be overwritten by merge" the moment origin's
// history touches the same path (verified: --skip-worktree doesn't prevent
// that either). The only real fix is to fully untrack the file, which is why
// OwnFileTrackingHints surfaces a one-time "gitignore + git rm --cached" hint
// for any of these files it finds tracked, in both the daemon log and
// `autopull status`.
func silenceOwnFiles(cfg *Config, cfgPath string, l *logger) {
	candidates := []string{
		filepath.Base(pidFilePath(cfgPath)),
		filepath.Base(stateFilePath(cfgPath)),
		filepath.Base(cfgPath),
	}
	if tracked := trackedInIndex(cfg.RepoPath, candidates); len(tracked) > 0 {
		assumeUnchanged(cfg.RepoPath, tracked)
		if l != nil {
			l.info(fmt.Sprintf("silenced %d tracked runtime file(s) via --assume-unchanged: %s",
				len(tracked), strings.Join(tracked, ", ")))
		}
	}
	if hints := OwnFileTrackingHints(cfg, cfgPath); len(hints) > 0 && l != nil {
		l.warn("auto_pull's own file(s) are tracked by git — this can make `git pull` abort with " +
			"\"local changes would be overwritten by merge\" any time origin's history touches them. " +
			"Fix permanently by running:")
		for _, cmd := range hints {
			l.warn("  " + cmd)
		}
	}
	// Re-write config so any settings (e.g. git_recovery_mode) that were wiped
	// by a hard-reset are restored from the in-memory copy.
	if err := writeConfig(cfgPath, configToFile(cfg)); err != nil && l != nil {
		l.warn(fmt.Sprintf("could not restore config after reset: %v", err))
	}
}

// OwnFileTrackingHints returns ready-to-run shell commands to permanently
// untrack any of auto_pull's own runtime files (pid, state, config, log) that
// are tracked in the repo's git index. It checks the index directly, rather
// than the `git status` dirty list that ArtifactGitignoreHints works from,
// because silenceOwnFiles sets --assume-unchanged on these files once
// tracked — which hides them from `git status`/TrackedChanges entirely, so
// the ordinary dirty-file hint never gets a chance to fire for them even
// though the file being tracked at all is what makes `git pull` liable to
// abort later.
func OwnFileTrackingHints(cfg *Config, cfgPath string) []string {
	candidates := []string{
		filepath.Base(pidFilePath(cfgPath)),
		filepath.Base(stateFilePath(cfgPath)),
		filepath.Base(cfgPath),
		filepath.Base(cfg.LogFile),
	}
	tracked := trackedInIndex(cfg.RepoPath, candidates)
	cmds := make([]string, 0, len(tracked))
	for _, f := range tracked {
		cmds = append(cmds, gitignoreUntrackCmd(f, f))
	}
	return cmds
}
